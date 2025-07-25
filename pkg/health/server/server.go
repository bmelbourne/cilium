// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package server

import (
	"fmt"
	"log/slog"
	"path"
	"time"

	"github.com/cilium/cilium/api/v1/client/daemon"
	healthModels "github.com/cilium/cilium/api/v1/health/models"
	healthApi "github.com/cilium/cilium/api/v1/health/server"
	"github.com/cilium/cilium/api/v1/health/server/restapi"
	"github.com/cilium/cilium/api/v1/models"
	"github.com/cilium/cilium/pkg/api"
	ciliumPkg "github.com/cilium/cilium/pkg/client"
	ciliumDefaults "github.com/cilium/cilium/pkg/defaults"
	healthClientPkg "github.com/cilium/cilium/pkg/health/client"
	"github.com/cilium/cilium/pkg/health/defaults"
	"github.com/cilium/cilium/pkg/health/probe"
	"github.com/cilium/cilium/pkg/health/probe/responder"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/metrics"
	"github.com/cilium/cilium/pkg/node"
	"github.com/cilium/cilium/pkg/option"
)

// Config stores the configuration data for a cilium-health server.
type Config struct {
	Debug         bool
	CiliumURI     string
	ICMPReqsCount int
	ProbeDeadline time.Duration
	HTTPPathPort  int
	HealthAPISpec *healthApi.Spec
}

// ipString is an IP address used as a more descriptive type name in maps.
type ipString string

// nodeMap maps IP addresses to healthNode objects for convenient access to
// node information.
type nodeMap map[ipString]healthNode

// Server is the cilium-health daemon that is in charge of performing health
// and connectivity checks periodically, and serving the cilium-health API.
type Server struct {
	logger *slog.Logger

	healthApi.Server  // Server to provide cilium-health API
	*ciliumPkg.Client // Client to "GET /healthz" on cilium daemon
	Config
	// clientID is the client ID returned by the cilium-agent that should
	// be used when making frequent requests. The server will return
	// a diff of the nodes added and removed based on this clientID.
	clientID int64

	httpPathServer *responder.Server // HTTP server for external pings
	startTime      time.Time

	// The lock protects against read and write access to the IP->Node map,
	// the list of statuses as most recently seen, and the last time a
	// probe was conducted.
	lock.RWMutex
	connectivity *healthReport
	localStatus  *healthModels.SelfStatus

	nodesSeen map[string]struct{}
}

// DumpUptime returns the time that this server has been running.
func (s *Server) DumpUptime() string {
	return time.Since(s.startTime).String()
}

// getNodes fetches the nodes added and removed from the last time the server
// made a request to the daemon.
func (s *Server) getNodes() (nodeMap, nodeMap, error) {
	scopedLog := s.logger
	if s.CiliumURI != "" {
		scopedLog = s.logger.With(logfields.URI, s.CiliumURI)
	}
	scopedLog.Debug("Sending request for /cluster/nodes ...")

	clusterNodesParam := daemon.NewGetClusterNodesParams()
	s.RWMutex.RLock()
	cID := s.clientID
	s.RWMutex.RUnlock()
	clusterNodesParam.SetClientID(&cID)
	resp, err := s.Daemon.GetClusterNodes(clusterNodesParam)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get nodes' cluster: %w", err)
	}
	scopedLog.Debug("Got cilium /cluster/nodes")

	if resp == nil || resp.Payload == nil {
		return nil, nil, fmt.Errorf("received nil health response")
	}

	s.RWMutex.Lock()
	s.clientID = resp.Payload.ClientID

	if resp.Payload.Self != "" {
		s.localStatus = &healthModels.SelfStatus{
			Name: resp.Payload.Self,
		}
	}
	s.RWMutex.Unlock()

	nodesAdded := nodeElementSliceToNodeMap(resp.Payload.NodesAdded)
	nodesRemoved := nodeElementSliceToNodeMap(resp.Payload.NodesRemoved)

	return nodesAdded, nodesRemoved, nil
}

// nodeElementSliceToNodeMap returns a slice of models.NodeElement into a
// nodeMap.
func nodeElementSliceToNodeMap(nodeElements []*models.NodeElement) nodeMap {
	nodes := make(nodeMap)
	for _, n := range nodeElements {
		if n.PrimaryAddress != nil {
			if n.PrimaryAddress.IPV4 != nil {
				nodes[ipString(n.PrimaryAddress.IPV4.IP)] = NewHealthNode(n)
			}
			if n.PrimaryAddress.IPV6 != nil {
				nodes[ipString(n.PrimaryAddress.IPV6.IP)] = NewHealthNode(n)
			}
		}
		for _, addr := range n.SecondaryAddresses {
			nodes[ipString(addr.IP)] = NewHealthNode(n)
		}
		if n.HealthEndpointAddress != nil {
			if n.HealthEndpointAddress.IPV4 != nil {
				nodes[ipString(n.HealthEndpointAddress.IPV4.IP)] = NewHealthNode(n)
			}
			if n.HealthEndpointAddress.IPV6 != nil {
				nodes[ipString(n.HealthEndpointAddress.IPV6.IP)] = NewHealthNode(n)
			}
		}
	}
	return nodes
}

// updateCluster makes the specified health report visible to the API.
//
// It only updates the server's API-visible health report if the provided
// report started at the same time as or after the current report.
func (s *Server) updateCluster(report *healthReport) {
	s.Lock()
	defer s.Unlock()

	if s.connectivity.startTime.Compare(report.startTime) <= 0 {
		if s.connectivity.startTime.Compare(report.startTime) < 0 {
			// New probe, clear nodesSeen
			s.nodesSeen = make(map[string]struct{})
		}
		s.collectNodeConnectivityMetrics(report)
		s.connectivity = report
	}
}

// collectNodeConnectivityMetrics updates the metrics based on the provided
// health report.
func (s *Server) collectNodeConnectivityMetrics(report *healthReport) {
	if s.localStatus == nil || report == nil {
		return
	}
	localClusterName, localNodeName := getClusterNodeName(s.localStatus.Name)

	endpointStatuses := make(map[healthClientPkg.ConnectivityStatusType]int)
	nodeStatuses := make(map[healthClientPkg.ConnectivityStatusType]int)

	for _, n := range report.nodes {
		if n == nil || n.Host == nil || n.Host.PrimaryAddress == nil || n.HealthEndpoint == nil || n.HealthEndpoint.PrimaryAddress == nil {
			continue
		}

		nodePathPrimaryAddress := healthClientPkg.GetHostPrimaryAddress(n)
		nodePathSecondaryAddress := healthClientPkg.GetHostSecondaryAddresses(n)

		endpointPathStatus := n.HealthEndpoint

		isHealthEndpointReachable := healthClientPkg.SummarizePathConnectivityStatusType(healthClientPkg.GetAllEndpointAddresses(n))
		isHealthNodeReachable := healthClientPkg.SummarizePathConnectivityStatusType(healthClientPkg.GetAllHostAddresses(n))

		// Update idempotent metrics here (to prevent overwriting with nil values).
		// Aggregate health connectivity statuses
		for connectivityStatusType, value := range isHealthEndpointReachable {
			endpointStatuses[connectivityStatusType] += value
		}
		for connectivityStatusType, value := range isHealthNodeReachable {
			nodeStatuses[connectivityStatusType] += value
		}

		// In order to avoid updating non-idempotent metrics, considers the possible cases.
		// Case 1: If the report is newer than the current one, update the connectivity status report and all metrics.
		// Case 2: If the report is from the same interval as the current one, update the report and only the new metrics.
		if s.connectivity != nil && s.connectivity.startTime.Compare(report.startTime) == 0 {
			if s.nodesSeen == nil {
				continue
			}
			if _, ok := s.nodesSeen[n.Name]; ok {
				// Skip updating non-idempotent latency metrics for nodes already seen.
				continue
			}
		}

		s.nodesSeen[n.Name] = struct{}{}

		// HTTP endpoint primary
		collectConnectivityMetric(s.logger, endpointPathStatus.PrimaryAddress.HTTP, localClusterName, localNodeName,
			metrics.LabelPeerEndpoint, metrics.LabelTrafficHTTP, metrics.LabelAddressTypePrimary)

		// HTTP endpoint secondary
		for _, secondary := range endpointPathStatus.SecondaryAddresses {
			collectConnectivityMetric(s.logger, secondary.HTTP, localClusterName, localNodeName,
				metrics.LabelPeerEndpoint, metrics.LabelTrafficHTTP, metrics.LabelAddressTypeSecondary)
		}

		// HTTP node primary
		collectConnectivityMetric(s.logger, nodePathPrimaryAddress.HTTP, localClusterName, localNodeName,
			metrics.LabelPeerNode, metrics.LabelTrafficHTTP, metrics.LabelAddressTypePrimary)

		// HTTP node secondary
		for _, secondary := range nodePathSecondaryAddress {
			collectConnectivityMetric(s.logger, secondary.HTTP, localClusterName, localNodeName,
				metrics.LabelPeerNode, metrics.LabelTrafficHTTP, metrics.LabelAddressTypeSecondary)
		}

		// ICMP endpoint primary
		collectConnectivityMetric(s.logger, endpointPathStatus.PrimaryAddress.Icmp, localClusterName, localNodeName,
			metrics.LabelPeerEndpoint, metrics.LabelTrafficICMP, metrics.LabelAddressTypePrimary)

		// ICMP endpoint secondary
		for _, secondary := range endpointPathStatus.SecondaryAddresses {
			collectConnectivityMetric(s.logger, secondary.Icmp, localClusterName, localNodeName,
				metrics.LabelPeerEndpoint, metrics.LabelTrafficICMP, metrics.LabelAddressTypeSecondary)
		}

		// ICMP node primary
		collectConnectivityMetric(s.logger, nodePathPrimaryAddress.Icmp, localClusterName, localNodeName,
			metrics.LabelPeerNode, metrics.LabelTrafficICMP, metrics.LabelAddressTypePrimary)

		// ICMP node secondary
		for _, secondary := range nodePathSecondaryAddress {
			collectConnectivityMetric(s.logger, secondary.Icmp, localClusterName, localNodeName,
				metrics.LabelPeerNode, metrics.LabelTrafficICMP, metrics.LabelAddressTypeSecondary)
		}
	}

	// Aggregated health statuses for endpoint connectivity
	metrics.NodeHealthConnectivityStatus.WithLabelValues(
		localClusterName, localNodeName, metrics.LabelPeerEndpoint, metrics.LabelReachable).
		Set(float64(endpointStatuses[healthClientPkg.ConnStatusReachable]))

	metrics.NodeHealthConnectivityStatus.WithLabelValues(
		localClusterName, localNodeName, metrics.LabelPeerEndpoint, metrics.LabelUnreachable).
		Set(float64(endpointStatuses[healthClientPkg.ConnStatusUnreachable]))

	metrics.NodeHealthConnectivityStatus.WithLabelValues(
		localClusterName, localNodeName, metrics.LabelPeerEndpoint, metrics.LabelUnknown).
		Set(float64(endpointStatuses[healthClientPkg.ConnStatusUnknown]))

	// Aggregated health statuses for node connectivity
	metrics.NodeHealthConnectivityStatus.WithLabelValues(
		localClusterName, localNodeName, metrics.LabelPeerNode, metrics.LabelReachable).
		Set(float64(nodeStatuses[healthClientPkg.ConnStatusReachable]))

	metrics.NodeHealthConnectivityStatus.WithLabelValues(
		localClusterName, localNodeName, metrics.LabelPeerNode, metrics.LabelUnreachable).
		Set(float64(nodeStatuses[healthClientPkg.ConnStatusUnreachable]))

	metrics.NodeHealthConnectivityStatus.WithLabelValues(
		localClusterName, localNodeName, metrics.LabelPeerNode, metrics.LabelUnknown).
		Set(float64(nodeStatuses[healthClientPkg.ConnStatusUnknown]))
}

func collectConnectivityMetric(logger *slog.Logger, status *healthModels.ConnectivityStatus, labels ...string) {
	if status != nil {
		if status.Status == "" {
			metricValue := float64(status.Latency) / float64(time.Second)
			metrics.NodeHealthConnectivityLatency.WithLabelValues(labels...).Observe(metricValue)
		} else {
			metrics.NodeHealthConnectivityLatency.WithLabelValues(labels...).Observe(probe.HttpTimeout.Seconds())
		}
	}
}

// getClusterNodeName returns the cluster name and node name if possible.
func getClusterNodeName(str string) (string, string) {
	clusterName, nodeName := path.Split(str)
	if len(clusterName) == 0 {
		return ciliumDefaults.ClusterName, nodeName
	}
	// remove forward slash at the end if any for cluster name
	return path.Dir(clusterName), nodeName
}

// GetStatusResponse returns the most recent cluster connectivity status.
func (s *Server) GetStatusResponse() *healthModels.HealthStatusResponse {
	s.RLock()
	defer s.RUnlock()

	var name string
	// Check if localStatus is populated already. If not, the name is empty
	if s.localStatus != nil {
		name = s.localStatus.Name
	}

	return &healthModels.HealthStatusResponse{
		Local: &healthModels.SelfStatus{
			Name: name,
		},
		Nodes:         s.connectivity.nodes,
		Timestamp:     s.connectivity.startTime.Format(time.RFC3339),
		ProbeInterval: s.connectivity.probeInterval.String(),
	}
}

// FetchStatusResponse returns the results of the most recent probe.
func (s *Server) FetchStatusResponse() (*healthModels.HealthStatusResponse, error) {
	return s.GetStatusResponse(), nil
}

// Run services that are actively probing other hosts and endpoints over
// ICMP and HTTP, and hosting the health admin API on a local Unix socket.
// Blocks indefinitely, or returns any errors that occur hosting the Unix
// socket API server.
func (s *Server) runActiveServices() error {
	// Set time in initial empty health report.
	s.updateCluster(&healthReport{startTime: time.Now()})

	// We can safely ignore nodesRemoved since it's the first time we are
	// fetching the nodes from the server.
	nodesAdded, _, _ := s.getNodes()
	prober := newProber(s, nodesAdded)
	prober.RunLoop()
	defer prober.Stop()

	// Periodically update the cluster status, without waiting for the
	// probing interval to pass.
	go func() {
		tick := time.NewTicker(60 * time.Second)
	loop:
		for {
			select {
			case <-prober.stop:
				break loop
			case <-tick.C:
				// We don't want to report stale nodes in metrics.
				// We don't update added nodes in the middle of a probing interval.
				if nodesAdded, nodesRemoved, err := prober.server.getNodes(); err != nil {
					// reset the cache by setting clientID to 0 and removing all current nodes
					prober.server.clientID = 0
					prober.setNodes(nil, prober.nodes)
					s.logger.Error("unable to get cluster nodes", logfields.Error, err)
				} else {
					// (1) setNodes implementation doesn't override results for existing nodes.
					// (2) Remove stale nodes so we don't report them in metrics before updating results
					prober.setNodes(nodesAdded, nodesRemoved)
					// (2) Update results without stale nodes
					prober.server.updateCluster(prober.getResults())
				}
			}
		}
		tick.Stop()
	}()

	return s.Server.Serve()
}

// Serve spins up the following goroutines:
//   - HTTP API Server: Responder to the health API "/hello" message
//   - Prober: Periodically run pings across the cluster at a configured interval
//     and update the server's connectivity status cache.
//   - Unix API Server: Handle all health API requests over a unix socket.
//
// Callers should first defer the Server.Shutdown(), then call Serve().
func (s *Server) Serve() (err error) {
	errors := make(chan error)

	go func() {
		errors <- s.httpPathServer.Serve()
	}()

	go func() {
		errors <- s.runActiveServices()
	}()

	// Block for the first error, then return.
	err = <-errors
	return err
}

// Shutdown server and clean up resources
func (s *Server) Shutdown() {
	s.httpPathServer.Shutdown()
	s.Server.Shutdown()
}

// newServer instantiates a new instance of the health API server on the
// defaults unix socket.
func (s *Server) newServer(logger *slog.Logger, spec *healthApi.Spec) *healthApi.Server {
	logger = logger.With(logfields.LogSubsys, "cilium-health-api-server")
	restAPI := restapi.NewCiliumHealthAPIAPI(spec.Document)
	restAPI.Logger = logger.Info

	// Admin API
	restAPI.GetHealthzHandler = NewGetHealthzHandler(s)
	restAPI.ConnectivityGetStatusHandler = NewGetStatusHandler(s)
	restAPI.ConnectivityPutStatusProbeHandler = NewPutStatusProbeHandler(s)

	api.DisableAPIs(logger, spec.DeniedAPIs, restAPI.AddMiddlewareFor)
	srv := healthApi.NewServer(restAPI)
	srv.EnabledListeners = []string{"unix"}
	srv.SocketPath = defaults.SockPath

	srv.ConfigureAPI()

	return srv
}

// NewServer creates a server to handle health requests.
func NewServer(logger *slog.Logger, config Config) (*Server, error) {
	server := &Server{
		logger:       logger,
		startTime:    time.Now(),
		Config:       config,
		connectivity: &healthReport{},
		nodesSeen:    make(map[string]struct{}),
	}

	cl, err := ciliumPkg.NewClient(config.CiliumURI)
	if err != nil {
		return nil, err
	}

	server.Client = cl
	server.Server = *server.newServer(logger, config.HealthAPISpec)

	server.httpPathServer = responder.NewServers(getAddresses(logger), config.HTTPPathPort)

	return server, nil
}

// Get internal node ipv4/ipv6 addresses based on config enabled.
// If it fails to get either of internal node address, it returns "0.0.0.0" if ipv4 or "::" if ipv6.
func getAddresses(logger *slog.Logger) []string {
	addresses := make([]string, 0, 2)

	if option.Config.EnableIPv4 {
		if ipv4 := node.GetInternalIPv4(logger); ipv4 != nil {
			addresses = append(addresses, ipv4.String())
		} else {
			// if Get ipv4 fails, then listen on all ipv4 addr.
			addresses = append(addresses, "0.0.0.0")
		}
	}

	if option.Config.EnableIPv6 {
		if ipv6 := node.GetInternalIPv6(logger); ipv6 != nil {
			addresses = append(addresses, ipv6.String())
		} else {
			// if Get ipv6 fails, then listen on all ipv6 addr.
			addresses = append(addresses, "::")
		}
	}

	return addresses
}

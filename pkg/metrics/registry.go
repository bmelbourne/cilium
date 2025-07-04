// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package metrics

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"

	"github.com/cilium/hive"
	"github.com/cilium/hive/cell"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"

	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging/logfields"
	metricpkg "github.com/cilium/cilium/pkg/metrics/metric"
	"github.com/cilium/cilium/pkg/option"
)

var defaultRegistryConfig = RegistryConfig{
	PrometheusServeAddr: "",
}

type RegistryConfig struct {
	// PrometheusServeAddr IP:Port on which to serve prometheus metrics (pass ":Port" to bind on all interfaces, "" is off)
	PrometheusServeAddr string
	// This is a list of metrics to be enabled or disabled, format is `+`/`-` + `{metric name}`
	Metrics []string
}

func (rc RegistryConfig) Flags(flags *pflag.FlagSet) {
	flags.String("prometheus-serve-addr", rc.PrometheusServeAddr, "IP:Port on which to serve prometheus metrics (pass \":Port\" to bind on all interfaces, \"\" is off)")
	flags.StringSlice("metrics", rc.Metrics, "Metrics that should be enabled or disabled from the default metric list. (+metric_foo to enable metric_foo, -metric_bar to disable metric_bar)")
}

// RegistryParams are the parameters needed to construct a Registry
type RegistryParams struct {
	cell.In

	Logger     *slog.Logger
	Shutdowner hive.Shutdowner
	Lifecycle  cell.Lifecycle

	AutoMetrics []metricpkg.WithMetadata `group:"hive-metrics"`
	Config      RegistryConfig

	DaemonConfig *option.DaemonConfig
}

// Registry is a cell around a prometheus registry. This registry starts an HTTP server as part of its lifecycle
// on which all enabled metrics will be available. A reference to this registry can also be used to dynamically
// register or unregister `prometheus.Collector`s.
type Registry struct {
	// inner registry of metrics.
	// Served under the default /metrics endpoint. Each collector is wrapped with
	// [metric.EnabledCollector] to only collect enabled metrics.
	inner *prometheus.Registry

	// collectors holds all registered collectors. Used to periodically sample the
	// metrics.
	collectors collectorSet

	params RegistryParams
}

func NewRegistry(params RegistryParams) *Registry {
	reg := &Registry{
		inner:  prometheus.NewPedanticRegistry(),
		params: params,
	}

	reg.registerMetrics()

	if params.Config.PrometheusServeAddr != "" {
		// The Handler function provides a default handler to expose metrics
		// via an HTTP server. "/metrics" is the usual endpoint for that.
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(reg.inner, promhttp.HandlerOpts{}))
		srv := http.Server{
			Addr:    params.Config.PrometheusServeAddr,
			Handler: mux,
		}

		params.Lifecycle.Append(cell.Hook{
			OnStart: func(hc cell.HookContext) error {
				go func() {
					params.Logger.Info("Serving prometheus metrics", logfields.Address, params.Config.PrometheusServeAddr)
					err := srv.ListenAndServe()
					if err != nil && !errors.Is(err, http.ErrServerClosed) {
						params.Shutdowner.Shutdown(hive.ShutdownWithError(err))
					}
				}()
				return nil
			},
			OnStop: func(hc cell.HookContext) error {
				return srv.Shutdown(hc)
			},
		})
	}

	return reg
}

// Register registers a collector
func (r *Registry) Register(c prometheus.Collector) error {
	r.collectors.add(c)
	return r.inner.Register(metricpkg.EnabledCollector{C: c})
}

// Unregister unregisters a collector
func (r *Registry) Unregister(c prometheus.Collector) bool {
	r.collectors.remove(c)
	return r.inner.Unregister(c)
}

// goCustomCollectorsRX tracks enabled go runtime metrics.
var goCustomCollectorsRX = regexp.MustCompile(`^/sched/latencies:seconds`)

// Reinitialize creates a new internal registry and re-registers metrics to it.
func (r *Registry) registerMetrics() {
	// Default metrics which can't be disabled.
	r.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{Namespace: Namespace}))
	r.MustRegister(collectors.NewGoCollector(
		collectors.WithGoCollectorRuntimeMetrics(
			collectors.GoRuntimeMetricsRule{Matcher: goCustomCollectorsRX},
		)))

	metrics := make(map[string]metricpkg.WithMetadata)
	for i, autoMetric := range r.params.AutoMetrics {
		metrics[autoMetric.Opts().GetConfigName()] = r.params.AutoMetrics[i]
	}

	// This is a bodge for a very specific feature, inherited from the old `Daemon.additionalMetrics`.
	// We should really find a more generic way to handle such cases.
	metricFlags := r.params.Config.Metrics
	if r.params.DaemonConfig.DNSProxyConcurrencyLimit > 0 {
		metricFlags = append(metricFlags, "+"+Namespace+"_"+SubsystemFQDN+"_semaphore_rejected_total")
	}

	for _, metricFlag := range metricFlags {
		metricFlag = strings.TrimSpace(metricFlag)

		// This is a temporary hack which allows us to get rid of the centralized metric config without refactoring the
		// dynamic map pressure registration/unregistion mechanism.
		// Long term the map pressure metric becomes a smarter component so this is no longer needed.
		if metricFlag[1:] == "-"+Namespace+"_"+SubsystemBPF+"_map_pressure" {
			BPFMapPressure = false
			continue
		}

		metric := metrics[metricFlag[1:]]
		if metric == nil {
			continue
		}

		switch metricFlag[0] {
		case '+':
			metric.SetEnabled(true)
		case '-':
			metric.SetEnabled(false)
		default:
			r.params.Logger.Warn(
				fmt.Sprintf(
					"--metrics flag contains value which does not start with + or -, '%s', ignoring",
					metricFlag),
			)
		}
	}

	for _, m := range metrics {
		if c, ok := m.(prometheus.Collector); ok {
			r.MustRegister(c)
		}
	}
}

// MustRegister adds the collector to the registry, exposing this metric to
// prometheus scrapes.
// It will panic on error.
func (r *Registry) MustRegister(cs ...prometheus.Collector) {
	for _, c := range cs {
		r.collectors.add(c)
		r.inner.MustRegister(metricpkg.EnabledCollector{C: c})
	}
}

// RegisterList registers a list of collectors. If registration of one
// collector fails, no collector is registered.
func (r *Registry) RegisterList(list []prometheus.Collector) error {
	registered := []prometheus.Collector{}

	for _, c := range list {
		if err := r.Register(c); err != nil {
			for _, c := range registered {
				r.Unregister(c)
			}
			return err
		}

		registered = append(registered, c)
	}

	return nil
}

// collectorSet holds the prometheus collectors so that we can sample them
// periodically. The collectors are not wrapped with [EnabledCollector] so
// that they're sampled regardless if they're enabled or not.
type collectorSet struct {
	mu         lock.Mutex
	collectors map[prometheus.Collector]struct{}
}

func (cs *collectorSet) collect() <-chan prometheus.Metric {
	ch := make(chan prometheus.Metric, 100)
	go func() {
		cs.mu.Lock()
		defer cs.mu.Unlock()
		defer close(ch)
		for c := range cs.collectors {
			c.Collect(ch)
		}
	}()
	return ch
}

func (cs *collectorSet) add(c prometheus.Collector) {
	cs.mu.Lock()
	if cs.collectors == nil {
		cs.collectors = make(map[prometheus.Collector]struct{})
	}
	cs.collectors[c] = struct{}{}
	cs.mu.Unlock()
}

func (cs *collectorSet) remove(c prometheus.Collector) {
	cs.mu.Lock()
	delete(cs.collectors, c)
	cs.mu.Unlock()
}

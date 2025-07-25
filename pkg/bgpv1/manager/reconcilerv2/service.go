// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package reconcilerv2

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/netip"
	"slices"

	"github.com/cilium/hive/cell"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/cilium/cilium/pkg/bgpv1/manager/instance"
	"github.com/cilium/cilium/pkg/bgpv1/manager/store"
	"github.com/cilium/cilium/pkg/bgpv1/types"
	"github.com/cilium/cilium/pkg/k8s"
	v2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/k8s/resource"
	slim_corev1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/api/core/v1"
	"github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/labels"
	slim_metav1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	ciliumslices "github.com/cilium/cilium/pkg/slices"
)

type ServiceReconcilerOut struct {
	cell.Out

	Reconciler ConfigReconciler `group:"bgp-config-reconciler-v2"`
}

type ServiceReconcilerIn struct {
	cell.In

	Logger       *slog.Logger
	PeerAdvert   *CiliumPeerAdvertisement
	SvcDiffStore store.DiffStore[*slim_corev1.Service]
	EPDiffStore  store.DiffStore[*k8s.Endpoints]
}

type ServiceReconciler struct {
	logger       *slog.Logger
	peerAdvert   *CiliumPeerAdvertisement
	svcDiffStore store.DiffStore[*slim_corev1.Service]
	epDiffStore  store.DiffStore[*k8s.Endpoints]
	metadata     map[string]ServiceReconcilerMetadata
}

func NewServiceReconciler(in ServiceReconcilerIn) ServiceReconcilerOut {
	if in.SvcDiffStore == nil || in.EPDiffStore == nil {
		return ServiceReconcilerOut{}
	}

	return ServiceReconcilerOut{
		Reconciler: &ServiceReconciler{
			logger:       in.Logger,
			peerAdvert:   in.PeerAdvert,
			svcDiffStore: in.SvcDiffStore,
			epDiffStore:  in.EPDiffStore,
			metadata:     make(map[string]ServiceReconcilerMetadata),
		},
	}
}

// ServiceReconcilerMetadata holds any announced service CIDRs per address family.
type ServiceReconcilerMetadata struct {
	ServicePaths          ResourceAFPathsMap
	ServiceAdvertisements PeerAdvertisements
	ServiceRoutePolicies  ResourceRoutePolicyMap
}

func (r *ServiceReconciler) getMetadata(i *instance.BGPInstance) ServiceReconcilerMetadata {
	return r.metadata[i.Name]
}

func (r *ServiceReconciler) setMetadata(i *instance.BGPInstance, metadata ServiceReconcilerMetadata) {
	r.metadata[i.Name] = metadata
}

func (r *ServiceReconciler) Name() string {
	return ServiceReconcilerName
}

func (r *ServiceReconciler) Priority() int {
	return ServiceReconcilerPriority
}

func (r *ServiceReconciler) Init(i *instance.BGPInstance) error {
	if i == nil {
		return fmt.Errorf("BUG: service reconciler initialization with nil BGPInstance")
	}
	r.svcDiffStore.InitDiff(r.diffID(i))
	r.epDiffStore.InitDiff(r.diffID(i))

	r.metadata[i.Name] = ServiceReconcilerMetadata{
		ServicePaths:          make(ResourceAFPathsMap),
		ServiceAdvertisements: make(PeerAdvertisements),
		ServiceRoutePolicies:  make(ResourceRoutePolicyMap),
	}
	return nil
}

func (r *ServiceReconciler) Cleanup(i *instance.BGPInstance) {
	if i != nil {
		r.svcDiffStore.CleanupDiff(r.diffID(i))
		r.epDiffStore.CleanupDiff(r.diffID(i))

		delete(r.metadata, i.Name)
	}
}

func (r *ServiceReconciler) Reconcile(ctx context.Context, p ReconcileParams) error {
	if err := p.ValidateParams(); err != nil {
		return err
	}

	desiredPeerAdverts, err := r.peerAdvert.GetConfiguredAdvertisements(p.DesiredConfig, v2.BGPServiceAdvert)
	if err != nil {
		return err
	}

	reqFullReconcile := r.modifiedServiceAdvertisements(p, desiredPeerAdverts)

	err = r.reconcileServices(ctx, p, desiredPeerAdverts, reqFullReconcile)

	if err == nil && reqFullReconcile {
		// update svc advertisements in metadata only if the reconciliation was successful
		r.updateServiceAdvertisementsMetadata(p, desiredPeerAdverts)
	}
	return err
}

func (r *ServiceReconciler) reconcileServices(ctx context.Context, p ReconcileParams, desiredPeerAdverts PeerAdvertisements, fullReconcile bool) error {
	var (
		toReconcile []*slim_corev1.Service
		toWithdraw  []resource.Key

		desiredSvcRoutePolicies ResourceRoutePolicyMap
		desiredSvcPaths         ResourceAFPathsMap

		err error
	)

	if fullReconcile {
		r.logger.Debug("performing all services reconciliation")

		// get all services to reconcile and to withdraw.
		toReconcile, toWithdraw, err = r.fullReconciliationServiceList(p)
		if err != nil {
			return err
		}
	} else {
		r.logger.Debug("performing modified services reconciliation")

		// get modified services to reconcile and to withdraw.
		// Note: we should call svc diff only once in a reconcile loop.
		toReconcile, toWithdraw, err = r.diffReconciliationServiceList(p)
		if err != nil {
			return err
		}
	}

	// populate locally available services
	ls, err := r.populateLocalServices(p.CiliumNode.Name)
	if err != nil {
		return fmt.Errorf("failed to populate local services: %w", err)
	}

	// get desired service route policies
	desiredSvcRoutePolicies, err = r.getDesiredRoutePolicies(p, desiredPeerAdverts, toReconcile, toWithdraw, ls)
	if err != nil {
		return err
	}

	// reconcile service route policies
	err = r.reconcileSvcRoutePolicies(ctx, p, desiredSvcRoutePolicies)
	if err != nil {
		return fmt.Errorf("failed to reconcile service route policies: %w", err)
	}

	// get desired service paths
	desiredSvcPaths, err = r.getDesiredPaths(p, desiredPeerAdverts, toReconcile, toWithdraw, ls)
	if err != nil {
		return err
	}

	// reconcile service paths
	err = r.reconcilePaths(ctx, p, desiredSvcPaths)
	if err != nil {
		return fmt.Errorf("failed to reconcile service paths: %w", err)
	}

	return nil
}

func (r *ServiceReconciler) reconcileSvcRoutePolicies(ctx context.Context, p ReconcileParams, desiredSvcRoutePolicies ResourceRoutePolicyMap) error {
	var err error
	metadata := r.getMetadata(p.BGPInstance)
	for svcKey, desiredSvcRoutePolicies := range desiredSvcRoutePolicies {
		currentSvcRoutePolicies, exists := metadata.ServiceRoutePolicies[svcKey]
		if !exists && len(desiredSvcRoutePolicies) == 0 {
			continue
		}

		updatedSvcRoutePolicies, rErr := ReconcileRoutePolicies(&ReconcileRoutePoliciesParams{
			Logger:          r.logger.With(types.InstanceLogField, p.DesiredConfig.Name),
			Ctx:             ctx,
			Router:          p.BGPInstance.Router,
			DesiredPolicies: desiredSvcRoutePolicies,
			CurrentPolicies: currentSvcRoutePolicies,
		})

		if rErr == nil && len(desiredSvcRoutePolicies) == 0 {
			delete(metadata.ServiceRoutePolicies, svcKey)
		} else {
			metadata.ServiceRoutePolicies[svcKey] = updatedSvcRoutePolicies
		}
		err = errors.Join(err, rErr)
	}
	r.setMetadata(p.BGPInstance, metadata)

	return err
}

func (r *ServiceReconciler) getDesiredRoutePolicies(p ReconcileParams, desiredPeerAdverts PeerAdvertisements, toUpdate []*slim_corev1.Service, toRemove []resource.Key, ls sets.Set[resource.Key]) (ResourceRoutePolicyMap, error) {
	desiredSvcRoutePolicies := make(ResourceRoutePolicyMap)

	for _, svc := range toUpdate {
		svcKey := resource.Key{
			Name:      svc.GetName(),
			Namespace: svc.GetNamespace(),
		}

		// get desired route policies for the service
		svcRoutePolicies, err := r.getDesiredSvcRoutePolicies(desiredPeerAdverts, svc, ls)
		if err != nil {
			return nil, err
		}

		desiredSvcRoutePolicies[svcKey] = svcRoutePolicies
	}

	for _, svcKey := range toRemove {
		// for withdrawn services, we need to set route policies to nil.
		desiredSvcRoutePolicies[svcKey] = nil
	}

	return desiredSvcRoutePolicies, nil
}

func (r *ServiceReconciler) getDesiredSvcRoutePolicies(desiredPeerAdverts PeerAdvertisements, svc *slim_corev1.Service, ls sets.Set[resource.Key]) (RoutePolicyMap, error) {
	desiredSvcRoutePolicies := make(RoutePolicyMap)

	for peer, afAdverts := range desiredPeerAdverts {
		for fam, adverts := range afAdverts {
			agentFamily := types.ToAgentFamily(fam)

			for _, advert := range adverts {
				labelSelector, err := slim_metav1.LabelSelectorAsSelector(advert.Selector)
				if err != nil {
					return nil, fmt.Errorf("failed constructing LabelSelector: %w", err)
				}
				if !labelSelector.Matches(serviceLabelSet(svc)) {
					continue
				}

				for _, advertType := range []v2.BGPServiceAddressType{v2.BGPLoadBalancerIPAddr, v2.BGPExternalIPAddr, v2.BGPClusterIPAddr} {
					policy, err := r.getServiceRoutePolicy(peer, agentFamily, svc, advert, advertType, ls)
					if err != nil {
						return nil, fmt.Errorf("failed to get desired %s route policy: %w", advertType, err)
					}
					if policy != nil {
						existingPolicy := desiredSvcRoutePolicies[policy.Name]
						if existingPolicy != nil {
							policy, err = MergeRoutePolicies(existingPolicy, policy)
							if err != nil {
								return nil, fmt.Errorf("failed to merge %s route policies: %w", advertType, err)
							}
						}
						desiredSvcRoutePolicies[policy.Name] = policy
					}
				}
			}
		}
	}

	return desiredSvcRoutePolicies, nil
}

func (r *ServiceReconciler) reconcilePaths(ctx context.Context, p ReconcileParams, desiredSvcPaths ResourceAFPathsMap) error {
	var err error
	metadata := r.getMetadata(p.BGPInstance)

	metadata.ServicePaths, err = ReconcileResourceAFPaths(ReconcileResourceAFPathsParams{
		Logger:                 r.logger.With(types.InstanceLogField, p.DesiredConfig.Name),
		Ctx:                    ctx,
		Router:                 p.BGPInstance.Router,
		DesiredResourceAFPaths: desiredSvcPaths,
		CurrentResourceAFPaths: metadata.ServicePaths,
	})

	r.setMetadata(p.BGPInstance, metadata)
	return err
}

// modifiedServiceAdvertisements compares local advertisement state with desiredPeerAdverts, if they differ,
// returns true signaling that full reconciliation is required.
func (r *ServiceReconciler) modifiedServiceAdvertisements(p ReconcileParams, desiredPeerAdverts PeerAdvertisements) bool {
	// current metadata
	serviceMetadata := r.getMetadata(p.BGPInstance)

	// check if BGP advertisement configuration modified
	modified := !PeerAdvertisementsEqual(serviceMetadata.ServiceAdvertisements, desiredPeerAdverts)

	return modified
}

// updateServiceAdvertisementsMetadata updates the provided ServiceAdvertisements in the reconciler metadata.
func (r *ServiceReconciler) updateServiceAdvertisementsMetadata(p ReconcileParams, peerAdverts PeerAdvertisements) {
	// current metadata
	serviceMetadata := r.getMetadata(p.BGPInstance)

	// update ServiceAdvertisements in the metadata
	r.setMetadata(p.BGPInstance, ServiceReconcilerMetadata{
		ServicePaths:          serviceMetadata.ServicePaths,
		ServiceRoutePolicies:  serviceMetadata.ServiceRoutePolicies,
		ServiceAdvertisements: peerAdverts,
	})
}

// Populate locally available services used for externalTrafficPolicy=local handling
func (r *ServiceReconciler) populateLocalServices(localNodeName string) (sets.Set[resource.Key], error) {
	ls := sets.New[resource.Key]()

	epList, err := r.epDiffStore.List()
	if err != nil {
		return nil, fmt.Errorf("failed to list EPs from diffstore: %w", err)
	}

endpointsLoop:
	for _, eps := range epList {
		_, exists, err := r.resolveSvcFromEndpoints(eps)
		if err != nil {
			// Cannot resolve service from EPs. We have nothing to do here.
			continue
		}

		if !exists {
			// No service associated with this endpoint. We're not interested in this.
			continue
		}

		svcKey := resource.Key{
			Name:      eps.ServiceName.Name(),
			Namespace: eps.ServiceName.Namespace(),
		}

		for _, be := range eps.Backends {
			if !be.Terminating && be.NodeName == localNodeName {
				// At least one endpoint is available on this node. We
				// can add service to the local services set.
				ls.Insert(svcKey)
				continue endpointsLoop
			}
		}
	}

	return ls, nil
}

func hasLocalEndpoints(svc *slim_corev1.Service, ls sets.Set[resource.Key]) bool {
	return ls.Has(resource.Key{Name: svc.GetName(), Namespace: svc.GetNamespace()})
}

func (r *ServiceReconciler) resolveSvcFromEndpoints(eps *k8s.Endpoints) (*slim_corev1.Service, bool, error) {
	k := resource.Key{
		Name:      eps.ServiceName.Name(),
		Namespace: eps.ServiceName.Namespace(),
	}
	return r.svcDiffStore.GetByKey(k)
}

func (r *ServiceReconciler) fullReconciliationServiceList(p ReconcileParams) (toReconcile []*slim_corev1.Service, toWithdraw []resource.Key, err error) {
	// re-init diff in diffstores, so that it contains only changes since the last full reconciliation.
	r.svcDiffStore.InitDiff(r.diffID(p.BGPInstance))
	r.epDiffStore.InitDiff(r.diffID(p.BGPInstance))

	// check for services which are no longer present
	serviceAFPaths := r.getMetadata(p.BGPInstance).ServicePaths
	for svcKey := range serviceAFPaths {
		_, exists, err := r.svcDiffStore.GetByKey(svcKey)
		if err != nil {
			return nil, nil, fmt.Errorf("svcDiffStore.GetByKey(): %w", err)
		}

		// if the service no longer exists, withdraw it
		if !exists {
			toWithdraw = append(toWithdraw, svcKey)
		}
	}

	// check all services for advertisement
	toReconcile, err = r.svcDiffStore.List()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list services from svcDiffstore: %w", err)
	}
	return
}

// diffReconciliationServiceList returns a list of services to reconcile and to withdraw when
// performing partial (diff) service reconciliation.
func (r *ServiceReconciler) diffReconciliationServiceList(p ReconcileParams) (toReconcile []*slim_corev1.Service, toWithdraw []resource.Key, err error) {
	upserted, deleted, err := r.svcDiffStore.Diff(r.diffID(p.BGPInstance))
	if err != nil {
		return nil, nil, fmt.Errorf("svc store diff: %w", err)
	}

	// For externalTrafficPolicy=local, we need to take care of
	// the endpoint changes in addition to the service changes.
	// Take a diff of the EPs and get affected services.
	// Also upsert services with deleted endpoints to handle potential withdrawal.
	epsUpserted, epsDeleted, err := r.epDiffStore.Diff(r.diffID(p.BGPInstance))
	if err != nil {
		return nil, nil, fmt.Errorf("EPs store diff: %w", err)
	}

	for _, eps := range slices.Concat(epsUpserted, epsDeleted) {
		svc, exists, err := r.resolveSvcFromEndpoints(eps)
		if err != nil {
			// Cannot resolve service from EPs. We have nothing to do here.
			continue
		}

		if !exists {
			// No service associated with this endpoint. We're not interested in this.
			continue
		}

		// We only need Endpoints tracking for externalTrafficPolicy=Local or internalTrafficPolicy=Local services.
		if svc.Spec.ExternalTrafficPolicy == slim_corev1.ServiceExternalTrafficPolicyLocal ||
			(svc.Spec.InternalTrafficPolicy != nil && *svc.Spec.InternalTrafficPolicy == slim_corev1.ServiceInternalTrafficPolicyLocal) {
			upserted = append(upserted, svc)
		}
	}

	// We may have duplicated services that changes happened for both of
	// service and associated EPs.
	deduped := ciliumslices.UniqueFunc(
		upserted,
		func(i int) resource.Key {
			return resource.Key{
				Name:      upserted[i].GetName(),
				Namespace: upserted[i].GetNamespace(),
			}
		},
	)

	deletedKeys := make([]resource.Key, 0, len(deleted))
	for _, svc := range deleted {
		deletedKeys = append(deletedKeys, resource.Key{Name: svc.Name, Namespace: svc.Namespace})
	}

	return deduped, deletedKeys, nil
}

func (r *ServiceReconciler) getDesiredPaths(p ReconcileParams, desiredPeerAdverts PeerAdvertisements, toReconcile []*slim_corev1.Service, toWithdraw []resource.Key, ls sets.Set[resource.Key]) (ResourceAFPathsMap, error) {
	desiredServiceAFPaths := make(ResourceAFPathsMap)
	for _, svc := range toReconcile {
		svcKey := resource.Key{
			Name:      svc.GetName(),
			Namespace: svc.GetNamespace(),
		}

		afPaths, err := r.getServiceAFPaths(p, desiredPeerAdverts, svc, ls)
		if err != nil {
			return nil, err
		}

		desiredServiceAFPaths[svcKey] = afPaths
	}

	for _, svcKey := range toWithdraw {
		// for withdrawn services, we need to set paths to nil.
		desiredServiceAFPaths[svcKey] = nil
	}

	return desiredServiceAFPaths, nil
}

func (r *ServiceReconciler) getServiceAFPaths(p ReconcileParams, desiredPeerAdverts PeerAdvertisements, svc *slim_corev1.Service, ls sets.Set[resource.Key]) (AFPathsMap, error) {
	desiredFamilyAdverts := make(AFPathsMap)

	for _, peerFamilyAdverts := range desiredPeerAdverts {
		for family, familyAdverts := range peerFamilyAdverts {
			agentFamily := types.ToAgentFamily(family)

			for _, advert := range familyAdverts {
				// get prefixes for the service
				desiredPrefixes, err := r.getServicePrefixes(svc, advert, ls)
				if err != nil {
					return nil, err
				}

				for _, prefix := range desiredPrefixes {
					// we only add path corresponding to the family of the prefix.
					if agentFamily.Afi == types.AfiIPv4 && prefix.Addr().Is4() {
						path := types.NewPathForPrefix(prefix)
						path.Family = agentFamily
						addPathToAFPathsMap(desiredFamilyAdverts, agentFamily, path)
					}
					if agentFamily.Afi == types.AfiIPv6 && prefix.Addr().Is6() {
						path := types.NewPathForPrefix(prefix)
						path.Family = agentFamily
						addPathToAFPathsMap(desiredFamilyAdverts, agentFamily, path)
					}
				}
			}
		}
	}
	return desiredFamilyAdverts, nil
}

func (r *ServiceReconciler) getServicePrefixes(svc *slim_corev1.Service, advert v2.BGPAdvertisement, ls sets.Set[resource.Key]) ([]netip.Prefix, error) {
	if advert.AdvertisementType != v2.BGPServiceAdvert {
		return nil, fmt.Errorf("unexpected advertisement type: %s", advert.AdvertisementType)
	}

	if advert.Selector == nil || advert.Service == nil {
		// advertisement has no selector or no service options, default behavior is not to match any service.
		return nil, nil
	}

	// The vRouter has a service selector, so determine the desired routes.
	svcSelector, err := slim_metav1.LabelSelectorAsSelector(advert.Selector)
	if err != nil {
		return nil, fmt.Errorf("labelSelectorAsSelector: %w", err)
	}

	// Ignore non matching services.
	if !svcSelector.Matches(serviceLabelSet(svc)) {
		return nil, nil
	}

	var desiredRoutes []netip.Prefix
	// Loop over the service upsertAdverts and determine the desired routes.
	for _, svcAdv := range advert.Service.Addresses {
		switch svcAdv {
		case v2.BGPLoadBalancerIPAddr:
			desiredRoutes = append(desiredRoutes, r.getLBSvcPaths(svc, ls, advert)...)
		case v2.BGPClusterIPAddr:
			desiredRoutes = append(desiredRoutes, r.getClusterIPPaths(svc, ls, advert)...)
		case v2.BGPExternalIPAddr:
			desiredRoutes = append(desiredRoutes, r.getExternalIPPaths(svc, ls, advert)...)
		}
	}

	return desiredRoutes, nil
}

func (r *ServiceReconciler) getExternalIPPaths(svc *slim_corev1.Service, ls sets.Set[resource.Key], advert v2.BGPAdvertisement) []netip.Prefix {
	var desiredRoutes []netip.Prefix
	// Ignore externalTrafficPolicy == Local && no local EPs.
	if svc.Spec.ExternalTrafficPolicy == slim_corev1.ServiceExternalTrafficPolicyLocal &&
		!hasLocalEndpoints(svc, ls) {
		return desiredRoutes
	}
	for _, extIP := range svc.Spec.ExternalIPs {
		if extIP == "" {
			continue
		}
		addr, err := netip.ParseAddr(extIP)
		if err != nil {
			continue
		}
		prefix, err := addr.Prefix(getServicePrefixLength(svc, addr, advert, v2.BGPExternalIPAddr))
		if err != nil {
			continue
		}
		desiredRoutes = append(desiredRoutes, prefix)
	}
	return desiredRoutes
}

func (r *ServiceReconciler) getClusterIPPaths(svc *slim_corev1.Service, ls sets.Set[resource.Key], advert v2.BGPAdvertisement) []netip.Prefix {
	var desiredRoutes []netip.Prefix
	// Ignore internalTrafficPolicy == Local && no local EPs.
	if svc.Spec.InternalTrafficPolicy != nil && *svc.Spec.InternalTrafficPolicy == slim_corev1.ServiceInternalTrafficPolicyLocal &&
		!hasLocalEndpoints(svc, ls) {
		return desiredRoutes
	}
	if svc.Spec.ClusterIP == "" || len(svc.Spec.ClusterIPs) == 0 || svc.Spec.ClusterIP == corev1.ClusterIPNone {
		return desiredRoutes
	}
	ips := sets.New[string]()
	if svc.Spec.ClusterIP != "" {
		ips.Insert(svc.Spec.ClusterIP)
	}
	for _, clusterIP := range svc.Spec.ClusterIPs {
		if clusterIP == "" || clusterIP == corev1.ClusterIPNone {
			continue
		}
		ips.Insert(clusterIP)
	}
	for _, ip := range sets.List(ips) {
		addr, err := netip.ParseAddr(ip)
		if err != nil {
			continue
		}
		prefix, err := addr.Prefix(getServicePrefixLength(svc, addr, advert, v2.BGPClusterIPAddr))
		if err != nil {
			continue
		}
		desiredRoutes = append(desiredRoutes, prefix)
	}
	return desiredRoutes
}

func (r *ServiceReconciler) getLBSvcPaths(svc *slim_corev1.Service, ls sets.Set[resource.Key], advert v2.BGPAdvertisement) []netip.Prefix {
	var desiredRoutes []netip.Prefix
	if svc.Spec.Type != slim_corev1.ServiceTypeLoadBalancer {
		return desiredRoutes
	}
	// Ignore externalTrafficPolicy == Local && no local EPs.
	if svc.Spec.ExternalTrafficPolicy == slim_corev1.ServiceExternalTrafficPolicyLocal &&
		!hasLocalEndpoints(svc, ls) {
		return desiredRoutes
	}
	// Ignore service managed by an unsupported LB class.
	if svc.Spec.LoadBalancerClass != nil && *svc.Spec.LoadBalancerClass != v2.BGPLoadBalancerClass {
		// The service is managed by a different LB class.
		return desiredRoutes
	}
	for _, ingress := range svc.Status.LoadBalancer.Ingress {
		if ingress.IP == "" {
			continue
		}
		addr, err := netip.ParseAddr(ingress.IP)
		if err != nil {
			continue
		}
		prefix, err := addr.Prefix(getServicePrefixLength(svc, addr, advert, v2.BGPLoadBalancerIPAddr))
		if err != nil {
			continue
		}
		desiredRoutes = append(desiredRoutes, prefix)
	}
	return desiredRoutes
}

func (r *ServiceReconciler) getServiceRoutePolicy(peer PeerID, family types.Family, svc *slim_corev1.Service, advert v2.BGPAdvertisement, advertType v2.BGPServiceAddressType, ls sets.Set[resource.Key]) (*types.RoutePolicy, error) {
	if peer.Address == "" {
		return nil, nil
	}
	peerAddr, err := netip.ParseAddr(peer.Address)
	if err != nil {
		return nil, fmt.Errorf("failed to parse peer address: %w", err)
	}

	valid, err := checkServiceAdvertisement(advert, advertType)
	if err != nil {
		return nil, fmt.Errorf("failed to check %s advertisement: %w", advertType, err)
	}
	if !valid {
		return nil, nil
	}

	var svcPrefixes []netip.Prefix
	switch advertType {
	case v2.BGPLoadBalancerIPAddr:
		svcPrefixes = r.getLBSvcPaths(svc, ls, advert)
	case v2.BGPExternalIPAddr:
		svcPrefixes = r.getExternalIPPaths(svc, ls, advert)
	case v2.BGPClusterIPAddr:
		svcPrefixes = r.getClusterIPPaths(svc, ls, advert)
	}

	var v4Prefixes, v6Prefixes types.PolicyPrefixMatchList
	for _, prefix := range svcPrefixes {
		if family.Afi == types.AfiIPv4 && prefix.Addr().Is4() {
			v4Prefixes = append(v4Prefixes, &types.RoutePolicyPrefixMatch{CIDR: prefix, PrefixLenMin: prefix.Bits(), PrefixLenMax: prefix.Bits()})
		}
		if family.Afi == types.AfiIPv6 && prefix.Addr().Is6() {
			v6Prefixes = append(v6Prefixes, &types.RoutePolicyPrefixMatch{CIDR: prefix, PrefixLenMin: prefix.Bits(), PrefixLenMax: prefix.Bits()})
		}
	}
	if len(v4Prefixes) == 0 && len(v6Prefixes) == 0 {
		return nil, nil
	}

	policyName := PolicyName(peer.Name, family.Afi.String(), advert.AdvertisementType, fmt.Sprintf("%s-%s-%s", svc.Name, svc.Namespace, advertType))
	policy, err := CreatePolicy(policyName, peerAddr, v4Prefixes, v6Prefixes, advert)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s IP route policy: %w", advertType, err)
	}

	return policy, nil
}

func (r *ServiceReconciler) diffID(instance *instance.BGPInstance) string {
	return fmt.Sprintf("%s-%s", r.Name(), instance.Name)
}

// checkServiceAdvertisement checks if the service advertisement is enabled in the advertisement.
func checkServiceAdvertisement(advert v2.BGPAdvertisement, advertServiceType v2.BGPServiceAddressType) (bool, error) {
	if advert.Service == nil {
		return false, fmt.Errorf("advertisement has no service options")
	}

	// If selector is nil, we do not use this advertisement.
	if advert.Selector == nil {
		return false, nil
	}

	// check service type is enabled in advertisement
	svcTypeEnabled := slices.Contains(advert.Service.Addresses, advertServiceType)
	if !svcTypeEnabled {
		return false, nil
	}

	return true, nil
}

func serviceLabelSet(svc *slim_corev1.Service) labels.Labels {
	svcLabels := maps.Clone(svc.Labels)
	if svcLabels == nil {
		svcLabels = make(map[string]string)
	}
	svcLabels["io.kubernetes.service.name"] = svc.Name
	svcLabels["io.kubernetes.service.namespace"] = svc.Namespace
	return labels.Set(svcLabels)
}

func getServicePrefixLength(svc *slim_corev1.Service, addr netip.Addr, advert v2.BGPAdvertisement, addrType v2.BGPServiceAddressType) int {
	length := addr.BitLen()

	if addrType == v2.BGPClusterIPAddr {
		// for iTP=Local, we always use the full prefix length
		if svc.Spec.InternalTrafficPolicy != nil && *svc.Spec.InternalTrafficPolicy == slim_corev1.ServiceInternalTrafficPolicyLocal {
			return length
		}
	} else {
		// for eTP=Local, we always use the full prefix length
		if svc.Spec.ExternalTrafficPolicy == slim_corev1.ServiceExternalTrafficPolicyLocal {
			return length
		}
	}

	if addr.Is4() && advert.Service.AggregationLengthIPv4 != nil {
		length = int(*advert.Service.AggregationLengthIPv4)
	}

	if addr.Is6() && advert.Service.AggregationLengthIPv6 != nil {
		length = int(*advert.Service.AggregationLengthIPv6)
	}
	return length
}

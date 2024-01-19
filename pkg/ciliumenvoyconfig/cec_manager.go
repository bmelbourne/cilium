// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package ciliumenvoyconfig

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"

	"github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/cilium/cilium/pkg/envoy"
	"github.com/cilium/cilium/pkg/k8s"
	ciliumv2 "github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/k8s/resource"
	"github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/labels"
	slim_metav1 "github.com/cilium/cilium/pkg/k8s/slim/k8s/apis/meta/v1"
	"github.com/cilium/cilium/pkg/loadbalancer"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/node"
	"github.com/cilium/cilium/pkg/option"
	"github.com/cilium/cilium/pkg/policy"
	"github.com/cilium/cilium/pkg/policy/api"
	"github.com/cilium/cilium/pkg/service"
)

type ciliumEnvoyConfigManager struct {
	logger logrus.FieldLogger

	policyUpdater  *policy.Updater
	serviceManager service.ServiceManager

	xdsServer      envoy.XDSServer
	backendSyncer  *envoyServiceBackendSyncer
	resourceParser *cecResourceParser

	mutex           lock.Mutex
	configs         map[resource.Key]*config
	localNodeLabels map[string]string
}

type config struct {
	meta             metav1.ObjectMeta
	spec             *ciliumv2.CiliumEnvoyConfigSpec
	selectsLocalNode bool
}

func newCiliumEnvoyConfigManager(logger logrus.FieldLogger,
	policyUpdater *policy.Updater,
	serviceManager service.ServiceManager,
	xdsServer envoy.XDSServer,
	backendSyncer *envoyServiceBackendSyncer,
	resourceParser *cecResourceParser,
) *ciliumEnvoyConfigManager {
	return &ciliumEnvoyConfigManager{
		logger:         logger,
		policyUpdater:  policyUpdater,
		serviceManager: serviceManager,
		xdsServer:      xdsServer,
		backendSyncer:  backendSyncer,
		resourceParser: resourceParser,
		configs:        map[resource.Key]*config{},
	}
}

func (r *ciliumEnvoyConfigManager) handleCECEvent(ctx context.Context, event resource.Event[*ciliumv2.CiliumEnvoyConfig]) error {
	scopedLogger := r.logger.
		WithField(logfields.K8sNamespace, event.Key.Namespace).
		WithField(logfields.CiliumEnvoyConfigName, event.Key.Name)

	var err error

	switch event.Kind {
	case resource.Upsert:
		scopedLogger.Debug("Received CiliumEnvoyConfig upsert event")
		err = r.configUpserted(ctx, event.Key, &config{meta: event.Object.ObjectMeta, spec: &event.Object.Spec})
		if err != nil {
			scopedLogger.WithError(err).Error("failed to handle CEC upsert")
			err = fmt.Errorf("failed to handle CEC upsert: %w", err)
		}
	case resource.Delete:
		scopedLogger.Debug("Received CiliumEnvoyConfig delete event")
		err = r.configDeleted(ctx, event.Key)
		if err != nil {
			scopedLogger.WithError(err).Error("failed to handle CEC delete")
			err = fmt.Errorf("failed to handle CEC delete: %w", err)
		}
	}

	event.Done(err)

	return err
}

func (r *ciliumEnvoyConfigManager) handleCCECEvent(ctx context.Context, event resource.Event[*ciliumv2.CiliumClusterwideEnvoyConfig]) error {
	scopedLogger := r.logger.
		WithField(logfields.K8sNamespace, event.Key.Namespace).
		WithField(logfields.CiliumClusterwideEnvoyConfigName, event.Key.Name)

	var err error

	switch event.Kind {
	case resource.Upsert:
		scopedLogger.Debug("Received CiliumClusterwideEnvoyConfig upsert event")
		err = r.configUpserted(ctx, event.Key, &config{meta: event.Object.ObjectMeta, spec: &event.Object.Spec})
		if err != nil {
			scopedLogger.WithError(err).Error("failed to handle CCEC upsert")
			err = fmt.Errorf("failed to handle CCEC upsert: %w", err)
		}
	case resource.Delete:
		scopedLogger.Debug("Received CiliumClusterwideEnvoyConfig delete event")
		err = r.configDeleted(ctx, event.Key)
		if err != nil {
			scopedLogger.WithError(err).Error("failed to handle CCEC delete")
			err = fmt.Errorf("failed to handle CCEC delete: %w", err)
		}
	}

	event.Done(err)

	return err
}

func (r *ciliumEnvoyConfigManager) handleLocalNodeEvent(ctx context.Context, localNode node.LocalNode) error {
	r.logger.Debug("Received LocalNode changed event")

	if err := r.handleLocalNodeLabels(ctx, localNode); err != nil {
		r.logger.WithError(err).Error("failed to handle LocalNode changed event")
		return fmt.Errorf("failed to handle LocalNode changed event: %w", err)
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) handleLocalNodeLabels(ctx context.Context, localNode node.LocalNode) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if maps.Equal(r.localNodeLabels, localNode.Labels) {
		r.logger.Debug("Labels of local Node didn't change")
		return nil
	}

	r.localNodeLabels = localNode.Labels
	r.logger.Debug("Labels of local Node changed - updated local store")

	r.logger.Debug("Checking whether existing configs need to be applied or filtered")

	// Error containing all potential errors during reconciliation of the configs.
	// On error, only the reconciliation of the faulty config is skipped. All other
	// configs should be reconciled.
	var reconcileErr error

	for key, cfg := range r.configs {
		scopedLogger := r.logger.WithField("key", key)

		err := r.configUpsertedInternal(ctx, key, cfg, false /* spec didn't change */)
		if err != nil {
			scopedLogger.WithError(err).Error("failed to reconcile config due to changed node labels")
			// don't prevent reconciliation of other configs in case of an error for a particular config
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("failed to reconcile config due to changed node labels (%s): %w", key, err))
			continue
		}
	}

	return reconcileErr
}

func (r *ciliumEnvoyConfigManager) configUpserted(ctx context.Context, key resource.Key, cfg *config) error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	return r.configUpsertedInternal(ctx, key, cfg, true /* spec may have changed */)
}

func (r *ciliumEnvoyConfigManager) configUpsertedInternal(ctx context.Context, key resource.Key, cfg *config, specMayChanged bool) error {
	scopedLogger := r.logger.WithField("key", key)

	selectsLocalNode, err := r.configSelectsLocalNode(cfg)
	if err != nil {
		return fmt.Errorf("failed to match Node labels with config nodeselector (%s): %w", key, err)
	}

	appliedConfig, isApplied := r.configs[key]

	switch {
	case !isApplied && !selectsLocalNode:
		scopedLogger.Debug("New config doesn't select the local Node")

	case !isApplied && selectsLocalNode:
		scopedLogger.Debug("New onfig selects the local node - adding config")
		if err := r.addCiliumEnvoyConfig(cfg.meta, cfg.spec); err != nil {
			return err
		}

	case isApplied && selectsLocalNode && !appliedConfig.selectsLocalNode:
		scopedLogger.Debug("Config now selects the local Node - adding previously filtered config")
		if err := r.addCiliumEnvoyConfig(cfg.meta, cfg.spec); err != nil {
			return err
		}

	case isApplied && selectsLocalNode && appliedConfig.selectsLocalNode && specMayChanged:
		scopedLogger.Debug("Config still selects the local Node - updating applied config")
		if err := r.updateCiliumEnvoyConfig(appliedConfig.meta, appliedConfig.spec, cfg.meta, cfg.spec); err != nil {
			return err
		}

	case isApplied && !selectsLocalNode && !appliedConfig.selectsLocalNode:
		scopedLogger.Debug("Config still doesn't select the local Node")

	case isApplied && !selectsLocalNode && appliedConfig.selectsLocalNode:
		scopedLogger.Debug("Config no longer selects the local Node - deleting previously applied config")
		if err := r.deleteCiliumEnvoyConfig(appliedConfig.meta, appliedConfig.spec); err != nil {
			return err
		}
	}

	r.configs[key] = &config{meta: cfg.meta, spec: cfg.spec, selectsLocalNode: selectsLocalNode}

	return nil
}

func (r *ciliumEnvoyConfigManager) configDeleted(ctx context.Context, key resource.Key) error {
	scopedLogger := r.logger.
		WithField("key", key)

	r.mutex.Lock()
	defer r.mutex.Unlock()

	appliedConfig, isApplied := r.configs[key]

	switch {
	case !isApplied:
		scopedLogger.Warn("Deleted Envoy config has never been applied")

	case isApplied && !appliedConfig.selectsLocalNode:
		scopedLogger.Debug("Deleted CEC was already filtered by NodeSelector")

	case isApplied && appliedConfig.selectsLocalNode:
		scopedLogger.Debug("Deleting applied CEC")
		if err := r.deleteCiliumEnvoyConfig(appliedConfig.meta, appliedConfig.spec); err != nil {
			return err
		}
	}

	delete(r.configs, key)

	return nil
}

func (r *ciliumEnvoyConfigManager) configSelectsLocalNode(cfg *config) (bool, error) {
	if cfg != nil && cfg.spec != nil && cfg.spec.NodeSelector != nil {
		ls, err := slim_metav1.LabelSelectorAsSelector(cfg.spec.NodeSelector)
		if err != nil {
			return false, fmt.Errorf("invalid NodeSelector: %w", err)
		}

		if !ls.Matches(labels.Set(r.localNodeLabels)) {
			return false, nil
		}
	}

	return true, nil
}

func (r *ciliumEnvoyConfigManager) addCiliumEnvoyConfig(cecObjectMeta metav1.ObjectMeta, cecSpec *ciliumv2.CiliumEnvoyConfigSpec) error {
	resources, err := r.resourceParser.parseResources(
		cecObjectMeta.GetNamespace(),
		cecObjectMeta.GetName(),
		cecSpec.Resources,
		len(cecSpec.Services) > 0,
		useOriginalSourceAddress(&cecObjectMeta),
		true,
	)
	if err != nil {
		return fmt.Errorf("malformed Envoy config: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), option.Config.EnvoyConfigTimeout)
	defer cancel()
	if err := r.xdsServer.UpsertEnvoyResources(ctx, resources); err != nil {
		return fmt.Errorf("failed to upsert envoy resources: %w", err)
	}

	name := service.L7LBResourceName{Name: cecObjectMeta.Name, Namespace: cecObjectMeta.Namespace}
	if err := r.addK8sServiceRedirects(name, cecSpec, resources); err != nil {
		return fmt.Errorf("failed to redirect k8s services to Envoy: %w", err)
	}

	if len(resources.Listeners) > 0 {
		// TODO: Policy does not need to be recomputed for this, but if we do not 'force'
		// the bpf maps are not updated with the new proxy ports either. Move from the
		// simple boolean to an enum that can more selectively skip regeneration steps (like
		// we do for the datapath recompilations already?)
		r.policyUpdater.TriggerPolicyUpdates(true, "Envoy Listeners added")
	}

	return err
}

func (r *ciliumEnvoyConfigManager) addK8sServiceRedirects(resourceName service.L7LBResourceName, spec *ciliumv2.CiliumEnvoyConfigSpec, resources envoy.Resources) error {
	// Redirect k8s services to an Envoy listener
	for _, svc := range spec.Services {
		svcListener := ""
		if svc.Listener != "" {
			// Listener names are qualified after parsing, so qualify the listener reference as well for it to match
			svcListener, _ = api.ResourceQualifiedName(resourceName.Namespace, resourceName.Name, svc.Listener, api.ForceNamespace)
		}
		// Find the listener the service is to be redirected to
		var proxyPort uint16
		for _, l := range resources.Listeners {
			if svc.Listener == "" || l.Name == svcListener {
				if addr := l.GetAddress(); addr != nil {
					if sa := addr.GetSocketAddress(); sa != nil {
						proxyPort = uint16(sa.GetPortValue())
					}
				}
			}
		}
		if proxyPort == 0 {
			// Do not return (and later on log) error in case of a service with an empty listener reference.
			// This is the case for the shared CEC in the Cilium namespace, if there is no shared Ingress
			// present in the cluster.
			if svc.Listener == "" {
				r.logger.Infof("Skipping L7LB k8s service redirect for service %s/%s. No Listener found in CEC resources", svc.Namespace, svc.Name)
				continue
			}

			return fmt.Errorf("listener %q not found in resources", svc.Listener)
		}

		// Tell service manager to redirect the service to the port
		serviceName := getServiceName(resourceName, svc.Name, svc.Namespace, true)
		if err := r.serviceManager.RegisterL7LBServiceRedirect(serviceName, resourceName, proxyPort); err != nil {
			return err
		}

		if err := r.registerServiceSync(serviceName, resourceName, nil /* all ports */); err != nil {
			return err
		}
	}
	// Register services for Envoy backend sync
	for _, svc := range spec.BackendServices {
		serviceName := getServiceName(resourceName, svc.Name, svc.Namespace, false)

		if err := r.registerServiceSync(serviceName, resourceName, svc.Ports); err != nil {
			return err
		}
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) updateCiliumEnvoyConfig(
	oldCECObjectMeta metav1.ObjectMeta, oldCECSpec *ciliumv2.CiliumEnvoyConfigSpec,
	newCECObjectMeta metav1.ObjectMeta, newCECSpec *ciliumv2.CiliumEnvoyConfigSpec,
) error {
	oldResources, err := r.resourceParser.parseResources(
		oldCECObjectMeta.GetNamespace(),
		oldCECObjectMeta.GetName(),
		oldCECSpec.Resources,
		len(oldCECSpec.Services) > 0,
		useOriginalSourceAddress(&oldCECObjectMeta),
		false,
	)
	if err != nil {
		return fmt.Errorf("malformed old Envoy Config: %w", err)
	}
	newResources, err := r.resourceParser.parseResources(
		newCECObjectMeta.GetNamespace(),
		newCECObjectMeta.GetName(),
		newCECSpec.Resources,
		len(newCECSpec.Services) > 0,
		useOriginalSourceAddress(&newCECObjectMeta),
		true,
	)
	if err != nil {
		return fmt.Errorf("malformed new Envoy config: %w", err)
	}

	name := service.L7LBResourceName{Name: oldCECObjectMeta.Name, Namespace: oldCECObjectMeta.Namespace}
	if err := r.removeK8sServiceRedirects(name, oldCECSpec, newCECSpec, oldResources, newResources); err != nil {
		return fmt.Errorf("failed to update k8s service redirects: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), option.Config.EnvoyConfigTimeout)
	defer cancel()
	if err := r.xdsServer.UpdateEnvoyResources(ctx, oldResources, newResources); err != nil {
		return fmt.Errorf("failed to update Envoy resources: %w", err)
	}

	if err := r.addK8sServiceRedirects(name, newCECSpec, newResources); err != nil {
		return fmt.Errorf("failed to redirect k8s services to Envoy: %w", err)
	}

	if oldResources.ListenersAddedOrDeleted(&newResources) {
		r.policyUpdater.TriggerPolicyUpdates(true, "Envoy Listeners added or deleted")
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) removeK8sServiceRedirects(resourceName service.L7LBResourceName, oldSpec, newSpec *ciliumv2.CiliumEnvoyConfigSpec, oldResources, newResources envoy.Resources) error {
	removedServices := []*ciliumv2.ServiceListener{}
	for _, oldSvc := range oldSpec.Services {
		found := false
		for _, newSvc := range newSpec.Services {
			if newSvc.Name == oldSvc.Name && newSvc.Namespace == oldSvc.Namespace {
				// Check if listener names match, but handle defaulting to the first listener first.
				oldListener := oldSvc.Listener
				if oldListener == "" && len(oldResources.Listeners) > 0 {
					oldListener = oldResources.Listeners[0].Name
				}
				newListener := newSvc.Listener
				if newListener == "" && len(newResources.Listeners) > 0 {
					newListener = newResources.Listeners[0].Name
				}
				if newListener != "" && newListener == oldListener {
					found = true
					break
				}
			}
		}
		if !found {
			removedServices = append(removedServices, oldSvc)
		}
	}
	for _, oldSvc := range removedServices {
		serviceName := getServiceName(resourceName, oldSvc.Name, oldSvc.Namespace, true)

		// Tell service manager to remove old service registration
		if err := r.serviceManager.DeregisterL7LBServiceRedirect(serviceName, resourceName); err != nil {
			return err
		}

		// Deregister Service from Secret Sync
		if err := r.deregisterServiceSync(serviceName, resourceName); err != nil {
			return err
		}
	}
	removedBackendServices := []*ciliumv2.Service{}
	for _, oldSvc := range oldSpec.BackendServices {
		found := false
		for _, newSvc := range newSpec.BackendServices {
			if newSvc.Name == oldSvc.Name && newSvc.Namespace == oldSvc.Namespace {
				found = true
				break
			}
		}
		if !found {
			removedBackendServices = append(removedBackendServices, oldSvc)
		}
	}
	for _, oldSvc := range removedBackendServices {
		serviceName := getServiceName(resourceName, oldSvc.Name, oldSvc.Namespace, false)

		// Deregister Service from Secret Sync
		if err := r.deregisterServiceSync(serviceName, resourceName); err != nil {
			return err
		}
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) deleteCiliumEnvoyConfig(cecObjectMeta metav1.ObjectMeta, cecSpec *ciliumv2.CiliumEnvoyConfigSpec) error {
	resources, err := r.resourceParser.parseResources(
		cecObjectMeta.GetNamespace(),
		cecObjectMeta.GetName(),
		cecSpec.Resources,
		len(cecSpec.Services) > 0,
		useOriginalSourceAddress(&cecObjectMeta),
		false,
	)
	if err != nil {
		return fmt.Errorf("parsing resources names failed: %w", err)
	}

	name := service.L7LBResourceName{Name: cecObjectMeta.Name, Namespace: cecObjectMeta.Namespace}
	if err := r.deleteK8sServiceRedirects(name, cecSpec); err != nil {
		return fmt.Errorf("failed to delete k8s service redirects: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), option.Config.EnvoyConfigTimeout)
	defer cancel()
	if err := r.xdsServer.DeleteEnvoyResources(ctx, resources); err != nil {
		return fmt.Errorf("failed to delete Envoy resources: %w", err)
	}

	if len(resources.Listeners) > 0 {
		r.policyUpdater.TriggerPolicyUpdates(true, "Envoy Listeners deleted")
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) deleteK8sServiceRedirects(resourceName service.L7LBResourceName, spec *ciliumv2.CiliumEnvoyConfigSpec) error {
	for _, svc := range spec.Services {
		serviceName := getServiceName(resourceName, svc.Name, svc.Namespace, true)

		// Tell service manager to remove old service redirection
		if err := r.serviceManager.DeregisterL7LBServiceRedirect(serviceName, resourceName); err != nil {
			return err
		}

		// Deregister Service from Secret Sync
		if err := r.deregisterServiceSync(serviceName, resourceName); err != nil {
			return err
		}
	}

	for _, svc := range spec.BackendServices {
		serviceName := getServiceName(resourceName, svc.Name, svc.Namespace, false)

		// Deregister Service from Secret Sync
		if err := r.deregisterServiceSync(serviceName, resourceName); err != nil {
			return err
		}
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) registerServiceSync(serviceName loadbalancer.ServiceName, resourceName service.L7LBResourceName, ports []string) error {
	// Register service usage in Envoy backend sync
	r.backendSyncer.RegisterServiceUsageInCEC(serviceName, resourceName, ports)

	// Register Envoy Backend Sync for the specific service in the service manager.
	// A re-registration will trigger an implicit re-synchronization.
	if err := r.serviceManager.RegisterL7LBServiceBackendSync(serviceName, r.backendSyncer); err != nil {
		return err
	}

	return nil
}

func (r *ciliumEnvoyConfigManager) deregisterServiceSync(serviceName loadbalancer.ServiceName, resourceName service.L7LBResourceName) error {
	// Deregister usage of Service from Envoy Backend Sync
	isLastDeregistration := r.backendSyncer.DeregisterServiceUsageInCEC(serviceName, resourceName)

	if isLastDeregistration {
		// Tell service manager to remove backend sync for this service
		if err := r.serviceManager.DeregisterL7LBServiceBackendSync(serviceName, r.backendSyncer); err != nil {
			return err
		}

		return nil
	}

	// There are other CECs using the same service as backend.
	// Re-Register the backend-sync to enforce a synchronization.
	if err := r.serviceManager.RegisterL7LBServiceBackendSync(serviceName, r.backendSyncer); err != nil {
		return err
	}

	return nil
}

// getServiceName enforces namespacing for service references in Cilium Envoy Configs
func getServiceName(resourceName service.L7LBResourceName, name, namespace string, isFrontend bool) loadbalancer.ServiceName {
	if resourceName.Namespace == "" {
		// nonNamespaced Cilium Clusterwide Envoy Config, default service references to
		// "default" namespace.
		if namespace == "" {
			namespace = "default"
		}
	} else {
		// Namespaced Cilium Envoy Config, enforce frontend service references to the
		// namespace of the CEC itself, and default the backend service reference namespace
		// to the namespace of the CEC itself.
		if isFrontend || namespace == "" {
			namespace = resourceName.Namespace
		}
	}
	return loadbalancer.ServiceName{Name: name, Namespace: namespace}
}

// useOriginalSourceAddress returns true if the given object metadata indicates that the owner needs the Envoy listener to assume the identity of Cilium Ingress.
// This can be an explicit label or the presence of an OwnerReference of Kind "Ingress" or "Gateway".
func useOriginalSourceAddress(meta *metav1.ObjectMeta) bool {
	for _, owner := range meta.OwnerReferences {
		if owner.Kind == "Ingress" || owner.Kind == "Gateway" {
			return false
		}
	}

	if meta.GetLabels() != nil {
		if v, ok := meta.GetLabels()[k8s.UseOriginalSourceAddressLabel]; ok {
			if boolValue, err := strconv.ParseBool(v); err == nil {
				return boolValue
			}
		}
	}

	return true
}
// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package lbmap

import (
	"errors"
	"fmt"
	"log/slog"
	"net"

	"golang.org/x/sys/unix"

	"github.com/cilium/cilium/pkg/bpf"
	"github.com/cilium/cilium/pkg/cidr"
	cmtypes "github.com/cilium/cilium/pkg/clustermesh/types"
	datapathTypes "github.com/cilium/cilium/pkg/datapath/types"
	"github.com/cilium/cilium/pkg/ip"
	"github.com/cilium/cilium/pkg/loadbalancer"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/maglev"
	"github.com/cilium/cilium/pkg/metrics"
	"github.com/cilium/cilium/pkg/option"
	"github.com/cilium/cilium/pkg/u8proto"
)

const DefaultMaxEntries = 65536

var (
	// MaxEntries contains the maximum number of entries that are allowed
	// in Cilium LB service, backend and affinity maps.
	ServiceMapMaxEntries        = DefaultMaxEntries
	ServiceBackEndMapMaxEntries = DefaultMaxEntries
	RevNatMapMaxEntries         = DefaultMaxEntries
	AffinityMapMaxEntries       = DefaultMaxEntries
	SourceRangeMapMaxEntries    = DefaultMaxEntries
	MaglevMapMaxEntries         = DefaultMaxEntries
)

// LBBPFMap is an implementation of the LBMap interface.
type LBBPFMap struct {
	logger   *slog.Logger
	lbConfig loadbalancer.Config
	maglev   *maglev.Maglev
}

func New(logger *slog.Logger, lbConfig loadbalancer.Config, maglev *maglev.Maglev) *LBBPFMap {
	return &LBBPFMap{logger, lbConfig, maglev}
}

func (lbmap *LBBPFMap) upsertServiceProto(p *datapathTypes.UpsertServiceParams, ipv6 bool) error {
	var svcKey ServiceKey
	var svcVal ServiceValue

	// Backends should be added to the backend maps for the case when:
	// - Plain IPv6 (to IPv6) or IPv4 (to IPv4) service.
	// - IPv4 to IPv6 will only have a dummy IPv4 service entry (0 backends)
	//   as it will recicle the packet into the IPv6 path.
	// - IPv6 to IPv4 will add its IPv4 backends as IPv4-in-IPv6 backends
	//   to the IPv6 backend map.
	backendsOk := ipv6 || !ipv6 && p.NatPolicy != loadbalancer.SVCNatPolicyNat46

	if ipv6 {
		svcKey = NewService6Key(p.IP, p.Port, u8proto.U8proto(p.Protocol), p.Scope, 0)
		svcVal = &Service6Value{}
	} else {
		svcKey = NewService4Key(p.IP, p.Port, u8proto.U8proto(p.Protocol), p.Scope, 0)
		svcVal = &Service4Value{}
	}

	slot := 1

	// start off with #backends = 0 for updateMasterService()
	backends := make(map[string]*loadbalancer.LegacyBackend)
	if backendsOk {
		backends = p.ActiveBackends
		if len(p.PreferredBackends) > 0 {
			backends = p.PreferredBackends
		}
		if p.UseMaglev {
			if err := lbmap.UpsertMaglevLookupTable(p.ID, backends, ipv6); err != nil {
				return err
			}
		}
		backendIDs := p.GetOrderedBackends()
		for _, backendID := range backendIDs {
			if backendID == 0 {
				return fmt.Errorf("Invalid backend ID 0")
			}
			svcVal.SetBackendID(loadbalancer.BackendID(backendID))
			svcVal.SetRevNat(int(p.ID))
			svcKey.SetBackendSlot(slot)
			svcVal.SetFlags(uint16(0))
			if slot > len(p.ActiveBackends) {
				flag := loadbalancer.NewSvcFlag(&loadbalancer.SvcFlagParam{
					Quarantined: true,
				})
				svcVal.SetFlags(flag.UInt16())
			}
			if err := updateServiceEndpoint(lbmap.logger, svcKey, svcVal); err != nil {
				if errors.Is(err, unix.E2BIG) {
					return fmt.Errorf("Unable to update service entry %+v => %+v: "+
						"Unable to update element for LB bpf map: "+
						"You can resize it with the flag \"--%s\". "+
						"The resizing might break existing connections to services",
						svcKey, svcVal, loadbalancer.LBMapEntriesName)
				}

				return fmt.Errorf("Unable to update service entry %+v => %+v: %w", svcKey, svcVal, err)
			}
			slot++
		}
	}

	zeroValue := svcVal.New().(ServiceValue)
	zeroValue.SetRevNat(int(p.ID)) // TODO change to uint16
	revNATKey := zeroValue.RevNatKey()
	revNATValue := svcKey.RevNatValue()
	if err := updateRevNatLocked(revNATKey, revNATValue); err != nil {
		return fmt.Errorf("Unable to update reverse NAT %+v => %+v: %w", revNATKey, revNATValue, err)
	}

	if err := updateMasterService(lbmap.logger, lbmap.lbConfig, svcKey, svcVal.New().(ServiceValue), len(backends), len(p.NonActiveBackends), int(p.ID),
		p.Type, p.ForwardingMode, p.ExtLocal, p.IntLocal, p.NatPolicy, p.SessionAffinity, p.SessionAffinityTimeoutSec,
		p.SourceRangesPolicy, p.CheckSourceRange, p.ProxyDelegation, p.L7LBProxyPort, p.LoopbackHostport, p.LoadBalancingAlgorithm); err != nil {
		deleteRevNatLocked(revNATKey)
		return fmt.Errorf("Unable to update service %+v: %w", svcKey, err)
	}

	if backendsOk {
		for i := slot; i <= p.PrevBackendsCount; i++ {
			svcKey.SetBackendSlot(i)
			if err := deleteServiceLocked(svcKey); err != nil {
				lbmap.logger.Warn(
					"Unable to delete service entry from BPF map",
					logfields.Error, err,
					logfields.ServiceKey, svcKey,
					logfields.BackendSlot, svcKey.GetBackendSlot(),
				)
			}
		}
	}

	return nil
}

// UpsertService inserts or updates the given service in a BPF map.
//
// The corresponding backend entries (identified with the given backendIDs)
// have to exist before calling the function.
//
// The service's prevActiveBackendCount denotes the count of previously active
// backend entries that were added to the BPF map so that the function can remove
// obsolete ones.
//
// The service's non-active backends are appended to the active backends list,
// and skipped from the service backends count set in the master key so that the
// non-active backends will not be considered for load-balancing traffic. The
// backends count is used in the datapath to determine if a service has any backends.
// The non-active backends are, however, populated in the service map so that they
// can be restored upon agent restart along with their state.
func (lbmap *LBBPFMap) UpsertService(p *datapathTypes.UpsertServiceParams) error {
	if p.ID == 0 {
		return fmt.Errorf("Invalid svc ID 0")
	}
	if err := lbmap.upsertServiceProto(p,
		p.IPv6 || p.NatPolicy == loadbalancer.SVCNatPolicyNat46); err != nil {
		return err
	}
	if p.NatPolicy == loadbalancer.SVCNatPolicyNat46 {
		if err := lbmap.upsertServiceProto(p, false); err != nil {
			return err
		}
	}
	return nil
}

// UpsertMaglevLookupTable calculates Maglev lookup table for given backends, and
// inserts into the Maglev BPF map.
func (lbmap *LBBPFMap) UpsertMaglevLookupTable(svcID uint16, backends map[string]*loadbalancer.LegacyBackend, ipv6 bool) error {
	if len(backends) == 0 {
		deleteMaglevTable(ipv6, svcID)
		return nil
	}
	table := lbmap.maglev.GetLookupTable(
		func(yield func(maglev.BackendInfo) bool) {
			for _, be := range backends {
				if !yield(maglev.BackendInfo{ID: be.ID, Weight: be.Weight, Addr: be.L3n4Addr}) {
					break
				}
			}
		})
	if err := updateMaglevTable(lbmap.logger, ipv6, svcID, table); err != nil {
		return err
	}
	return nil
}

func deleteServiceProto(logger *slog.Logger, svc loadbalancer.L3n4AddrID, backendCount int, useMaglev, ipv6 bool) error {
	var (
		svcKey    ServiceKey
		revNATKey RevNatKey
	)

	u8p, err := u8proto.ParseProtocol(svc.Protocol)
	if err != nil {
		return err
	}

	if ipv6 {
		svcKey = NewService6Key(svc.AddrCluster.AsNetIP(), svc.Port, u8p, svc.Scope, 0)
		revNATKey = NewRevNat6Key(uint16(svc.ID))
	} else {
		svcKey = NewService4Key(svc.AddrCluster.AsNetIP(), svc.Port, u8p, svc.Scope, 0)
		revNATKey = NewRevNat4Key(uint16(svc.ID))
	}

	for slot := 0; slot <= backendCount; slot++ {
		svcKey.SetBackendSlot(slot)
		if err := deleteServiceLocked(svcKey); err != nil {
			logger.Warn(
				"Unable to delete service entry from BPF map",
				logfields.Error, err,
				logfields.ServiceKey, svcKey,
				logfields.BackendSlot, svcKey.GetBackendSlot(),
			)
		}
	}

	if useMaglev {
		deleteMaglevTable(ipv6, uint16(svc.ID))
	}

	if err := deleteRevNatLocked(revNATKey); err != nil {
		return fmt.Errorf("Unable to delete revNAT entry %+v: %w", revNATKey, err)
	}

	return nil
}

// DeleteService removes given service from a BPF map.
func (lbmap *LBBPFMap) DeleteService(svc loadbalancer.L3n4AddrID, backendCount int, useMaglev bool,
	natPolicy loadbalancer.SVCNatPolicy) error {
	if svc.ID == 0 {
		return fmt.Errorf("Invalid svc ID 0")
	}
	if err := deleteServiceProto(lbmap.logger, svc, backendCount, useMaglev,
		svc.IsIPv6() || natPolicy == loadbalancer.SVCNatPolicyNat46); err != nil {
		return err
	}
	if natPolicy == loadbalancer.SVCNatPolicyNat46 {
		if err := deleteServiceProto(lbmap.logger, svc, 0, false, false); err != nil {
			return err
		}
	}
	return nil
}

// AddBackend adds a backend into a BPF map. ipv6 indicates if the backend needs
// to be added in the v4 or v6 backend map.
func (*LBBPFMap) AddBackend(b *loadbalancer.LegacyBackend, ipv6 bool) error {
	var (
		backend Backend
		err     error
	)

	if backend, err = getBackend(b, ipv6); err != nil {
		return err
	}
	if err := updateBackend(backend); err != nil {
		return fmt.Errorf("unable to add backend %+v: %w", backend, err)
	}

	return nil
}

// UpdateBackendWithState updates the state for the given backend.
//
// This function should only be called to update backend's state.
func (*LBBPFMap) UpdateBackendWithState(b *loadbalancer.LegacyBackend) error {
	var (
		backend Backend
		err     error
	)

	if backend, err = getBackend(b, b.L3n4Addr.IsIPv6()); err != nil {
		return err
	}
	if err := updateBackend(backend); err != nil {
		return fmt.Errorf("unable to update backend state %+v: %w", b, err)
	}

	return nil
}

func deleteBackendByIDFamily(id loadbalancer.BackendID, ipv6 bool) error {
	var key BackendKey

	if ipv6 {
		key = NewBackend6KeyV3(loadbalancer.BackendID(id))
	} else {
		key = NewBackend4KeyV3(loadbalancer.BackendID(id))
	}

	if err := deleteBackendLocked(key); err != nil {
		return fmt.Errorf("Unable to delete backend %d (%t): %w", id, ipv6, err)
	}

	return nil
}

// DeleteBackendByID removes a backend identified with the given ID from a BPF map.
func (*LBBPFMap) DeleteBackendByID(id loadbalancer.BackendID) error {
	if id == 0 {
		return fmt.Errorf("Invalid backend ID 0")
	}

	// The backend could be a backend for a NAT64 service, therefore
	// attempt to remove from both backend maps.
	if option.Config.EnableIPv6 {
		deleteBackendByIDFamily(id, true)
	}
	if option.Config.EnableIPv4 {
		deleteBackendByIDFamily(id, false)
	}
	return nil
}

// DeleteAffinityMatch removes the affinity match for the given svc and backend ID
// tuple from the BPF map
func (*LBBPFMap) DeleteAffinityMatch(revNATID uint16, backendID loadbalancer.BackendID) error {
	return AffinityMatchMap.Delete(
		NewAffinityMatchKey(revNATID, backendID).ToNetwork())
}

// AddAffinityMatch adds the given affinity match to the BPF map.
func (*LBBPFMap) AddAffinityMatch(revNATID uint16, backendID loadbalancer.BackendID) error {
	return AffinityMatchMap.Update(
		NewAffinityMatchKey(revNATID, backendID).ToNetwork(),
		&AffinityMatchValue{})
}

// DumpAffinityMatches returns the affinity match map represented as a nested
// map which first key is svc ID and the second - backend ID.
func (*LBBPFMap) DumpAffinityMatches() (datapathTypes.BackendIDByServiceIDSet, error) {
	matches := datapathTypes.BackendIDByServiceIDSet{}

	parse := func(key bpf.MapKey, value bpf.MapValue) {
		matchKey := key.(*AffinityMatchKey).ToHost()
		svcID := matchKey.RevNATID
		backendID := matchKey.BackendID

		if _, ok := matches[svcID]; !ok {
			matches[svcID] = map[loadbalancer.BackendID]struct{}{}
		}
		matches[svcID][backendID] = struct{}{}
	}

	err := AffinityMatchMap.DumpWithCallback(parse)
	if err != nil {
		return nil, err
	}

	return matches, nil
}

func (*LBBPFMap) DumpSourceRanges(ipv6 bool) (datapathTypes.SourceRangeSetByServiceID, error) {
	ret := datapathTypes.SourceRangeSetByServiceID{}
	parser := func(key bpf.MapKey, value bpf.MapValue) {
		k := key.(SourceRangeKey).ToHost()
		revNATID := k.GetRevNATID()
		if _, found := ret[revNATID]; !found {
			ret[revNATID] = []*cidr.CIDR{}
		}
		ret[revNATID] = append(ret[revNATID], k.GetCIDR())
	}

	m := SourceRange4Map
	if ipv6 {
		m = SourceRange6Map
	}
	if err := m.DumpWithCallback(parser); err != nil {
		return nil, err
	}

	return ret, nil
}

func updateRevNatLocked(key RevNatKey, value RevNatValue) error {
	if key.GetKey() == 0 {
		return fmt.Errorf("invalid RevNat ID (0)")
	}
	if err := RevNatMap(key).OpenOrCreate(); err != nil {
		return err
	}

	return RevNatMap(key).Update(key.ToNetwork(), value.ToNetwork())
}

func deleteRevNatLocked(key RevNatKey) error {
	_, err := RevNatMap(key).SilentDelete(key.ToNetwork())
	return err
}

func (*LBBPFMap) UpdateSourceRanges(revNATID uint16, prevSourceRanges []*cidr.CIDR,
	sourceRanges []*cidr.CIDR, ipv6 bool) error {

	m := SourceRange4Map
	if ipv6 {
		m = SourceRange6Map
	}

	srcRangeMap := map[string]*cidr.CIDR{}
	for _, cidr := range sourceRanges {
		// k8s api server does not catch the IP family mismatch, so we need
		// to catch it here and below.
		if ip.IsIPv6(cidr.IP) == !ipv6 {
			continue
		}
		srcRangeMap[cidr.String()] = cidr
	}

	for _, prevCIDR := range prevSourceRanges {
		if ip.IsIPv6(prevCIDR.IP) == !ipv6 {
			continue
		}
		if _, found := srcRangeMap[prevCIDR.String()]; !found {
			m.Delete(srcRangeKey(prevCIDR, revNATID, ipv6))
		} else {
			delete(srcRangeMap, prevCIDR.String())
		}
	}

	for _, cidr := range srcRangeMap {
		if err := m.Update(srcRangeKey(cidr, revNATID, ipv6), &SourceRangeValue{}); err != nil {
			return err
		}
	}

	return nil
}

// DumpServiceMaps dumps the services from the BPF maps.
func (lbmap *LBBPFMap) DumpServiceMaps() ([]*loadbalancer.LegacySVC, []error) {
	newSVCMap := svcMap{}
	errors := []error{}
	flagsCache := map[string]loadbalancer.ServiceFlags{}
	backendValueMap := map[loadbalancer.BackendID]BackendValue{}
	revNatValueMap := map[uint16]RevNatValue{}
	inconsistentServiceKeys := []ServiceKey{}

	parseBackendEntries := func(key bpf.MapKey, value bpf.MapValue) {
		backendKey := key.(BackendKey)
		backendValue := value.(BackendValue).ToHost()
		backendValueMap[backendKey.GetID()] = backendValue
	}

	parseRevNatEntries := func(key bpf.MapKey, value bpf.MapValue) {
		revNatKey := key.(RevNatKey).ToHost()
		revNatValue := value.(RevNatValue).ToHost()
		revNatValueMap[revNatKey.GetKey()] = revNatValue
	}

	parseSVCEntries := func(key bpf.MapKey, value bpf.MapValue) {
		svcKey := key.(ServiceKey).ToHost()
		svcValue := value.(ServiceValue).ToHost()

		serviceID := svcValue.RevNatKey().GetKey()
		revNatValue := svcKey.RevNatValue().String()
		val, found := revNatValueMap[serviceID]
		if !found {
			errors = append(errors, fmt.Errorf("revNat %d not found", serviceID))
			inconsistentServiceKeys = append(inconsistentServiceKeys, svcKey)
			return
		} else if valueStr := val.String(); valueStr != revNatValue {
			errors = append(errors, fmt.Errorf("inconsistent service %s and revNat %s found",
				svcKey, valueStr))
			inconsistentServiceKeys = append(inconsistentServiceKeys, svcKey)
			return
		}

		fe := svcFrontend(svcKey, svcValue)

		// Create master entry in case there are no backends.
		if svcKey.GetBackendSlot() == 0 {
			// Build a cache of flags stored in the value of the master key to
			// map it later.

			flagsCache[fe.String()] = loadbalancer.ServiceFlags(svcValue.GetFlags())
			newSVCMap.addFE(fe)
			return
		}

		backendID := svcValue.GetBackendID()
		backendValue, found := backendValueMap[backendID]
		if !found {
			errors = append(errors, fmt.Errorf("backend %d not found", backendID))
			return
		}
		backendFlags := loadbalancer.ServiceFlags(svcValue.GetFlags())
		be := svcBackend(backendID, backendValue, backendFlags)
		newSVCMap.addFEnBE(fe, be, svcKey.GetBackendSlot())
	}

	if option.Config.EnableIPv4 {
		// TODO(brb) optimization: instead of dumping the backend map, we can
		// pass its content to the function.
		err := Backend4MapV3.DumpWithCallback(parseBackendEntries)
		if err != nil {
			errors = append(errors, err)
		}
		err = RevNat4Map.DumpWithCallback(parseRevNatEntries)
		if err != nil {
			errors = append(errors, err)
		}
		err = Service4MapV2.DumpWithCallback(parseSVCEntries)
		if err != nil {
			errors = append(errors, err)
		}
	}

	if option.Config.EnableIPv6 {
		// TODO(brb) same ^^ optimization applies here as well.
		err := Backend6MapV3.DumpWithCallback(parseBackendEntries)
		if err != nil {
			errors = append(errors, err)
		}
		err = RevNat6Map.DumpWithCallback(parseRevNatEntries)
		if err != nil {
			errors = append(errors, err)
		}
		err = Service6MapV2.DumpWithCallback(parseSVCEntries)
		if err != nil {
			errors = append(errors, err)
		}
	}

	for _, svcKey := range inconsistentServiceKeys {
		lbmap.logger.Warn(
			"Deleting service with inconsistent revNat",
			logfields.ServiceKey, svcKey,
		)
		if err := deleteServiceLocked(svcKey); err != nil {
			lbmap.logger.Warn(
				"Unable to delete service entry from BPF map",
				logfields.Error, err,
				logfields.ServiceKey, svcKey,
			)
		}
	}

	newSVCList := make([]*loadbalancer.LegacySVC, 0, len(newSVCMap))
	for hash := range newSVCMap {
		svc := newSVCMap[hash]
		key := svc.Frontend.String()
		svc.Type = flagsCache[key].SVCType()
		svc.ExtTrafficPolicy = flagsCache[key].SVCExtTrafficPolicy()
		svc.IntTrafficPolicy = flagsCache[key].SVCIntTrafficPolicy()
		svc.NatPolicy = flagsCache[key].SVCNatPolicy(svc.Frontend.L3n4Addr)
		newSVCList = append(newSVCList, &svc)
	}

	return newSVCList, errors
}

// DumpBackendMaps dumps the backend entries from the BPF maps.
func (*LBBPFMap) DumpBackendMaps() ([]*loadbalancer.LegacyBackend, error) {
	backendValueMap := map[loadbalancer.BackendID]BackendValue{}
	lbBackends := []*loadbalancer.LegacyBackend{}

	parseBackendEntries := func(key bpf.MapKey, value bpf.MapValue) {
		// No need to deep copy the key because we are using the ID which
		// is a value.
		backendKey := key.(BackendKey)
		backendValue := value.(BackendValue).ToHost()
		backendValueMap[backendKey.GetID()] = backendValue
	}

	if option.Config.EnableIPv4 {
		err := Backend4MapV3.DumpWithCallback(parseBackendEntries)
		if err != nil {
			return nil, fmt.Errorf("Unable to dump lb4 backends map: %w", err)
		}
	}

	if option.Config.EnableIPv6 {
		err := Backend6MapV3.DumpWithCallback(parseBackendEntries)
		if err != nil {
			return nil, fmt.Errorf("Unable to dump lb6 backends map: %w", err)
		}
	}

	for backendID, backendVal := range backendValueMap {
		ip := backendVal.GetAddress()
		addrCluster := cmtypes.MustAddrClusterFromIP(ip)
		port := backendVal.GetPort()
		proto := loadbalancer.NewL4TypeFromNumber(backendVal.GetProtocol())
		state := loadbalancer.GetBackendStateFromFlags(backendVal.GetFlags())
		zone := backendVal.GetZone()
		lbBackend := loadbalancer.NewBackendWithState(backendID, proto, addrCluster, port, zone, state)
		lbBackends = append(lbBackends, lbBackend)
	}

	return lbBackends, nil
}

// IsMaglevLookupTableRecreated returns true if the maglev lookup BPF map
// was recreated due to the changed M param.
func (*LBBPFMap) IsMaglevLookupTableRecreated(ipv6 bool) bool {
	if ipv6 {
		return maglevRecreatedIPv6
	}
	return maglevRecreatedIPv4
}

func updateMasterService(logger *slog.Logger, lbConfig loadbalancer.Config, fe ServiceKey, v ServiceValue, activeBackends, quarantinedBackends int,
	revNATID int, svcType loadbalancer.SVCType, svcForwardingMode loadbalancer.SVCForwardingMode,
	svcExtLocal, svcIntLocal bool, svcNatPolicy loadbalancer.SVCNatPolicy, sessionAffinity bool,
	sessionAffinityTimeoutSec uint32, svcSourceRangesPolicy loadbalancer.SVCSourceRangesPolicy,
	checkSourceRange bool, svcProxyDelegation loadbalancer.SVCProxyDelegation, l7lbProxyPort uint16,
	loopbackHostport bool, loadBalancingAlgorithm loadbalancer.SVCLoadBalancingAlgorithm) error {
	// isRoutable denotes whether this service can be accessed from outside the cluster.
	isRoutable := !fe.IsSurrogate() &&
		(svcType != loadbalancer.SVCTypeClusterIP || lbConfig.ExternalClusterIP)
	if sessionAffinity && l7lbProxyPort != 0 {
		logger.Warn("Failure in updating master service entry: Service session affinity incompatible with L7 proxy feature")
		return fmt.Errorf("invalid feature combination")
	}
	if loopbackHostport && svcProxyDelegation != loadbalancer.SVCProxyDelegationNone {
		logger.Warn("Failure in updating master service entry: Both HostPort (loopback) and proxy delegation features are incompatible")
		return fmt.Errorf("invalid feature combination")
	}

	fe.SetBackendSlot(0)
	v.SetCount(activeBackends)
	v.SetQCount(quarantinedBackends)
	v.SetRevNat(revNATID)
	v.SetLbAlg(loadBalancingAlgorithm)
	flag := loadbalancer.NewSvcFlag(&loadbalancer.SvcFlagParam{
		SvcType:          svcType,
		SvcFwdModeDSR:    svcForwardingMode == loadbalancer.SVCForwardingModeDSR,
		SvcExtLocal:      svcExtLocal,
		SvcIntLocal:      svcIntLocal,
		SvcNatPolicy:     svcNatPolicy,
		SessionAffinity:  sessionAffinity,
		IsRoutable:       isRoutable,
		SourceRangeDeny:  svcSourceRangesPolicy == loadbalancer.SVCSourceRangesPolicyDeny,
		CheckSourceRange: checkSourceRange,
		L7LoadBalancer:   l7lbProxyPort != 0,
		LoopbackHostport: loopbackHostport || svcProxyDelegation != loadbalancer.SVCProxyDelegationNone,
	})
	v.SetFlags(flag.UInt16())
	if sessionAffinity {
		if err := v.SetSessionAffinityTimeoutSec(sessionAffinityTimeoutSec); err != nil {
			logger.Warn("Failure in updateMasterService due to error from SetSessionAffinityTimeoutSec", logfields.Error, err)
			return err
		}
	}
	if l7lbProxyPort != 0 {
		v.SetL7LBProxyPort(l7lbProxyPort)
	}

	return updateServiceEndpoint(logger, fe, v)
}

func deleteServiceLocked(key ServiceKey) error {
	_, err := ServiceMap(key).SilentDelete(key.ToNetwork())
	return err
}

func getBackend(backend *loadbalancer.LegacyBackend, ipv6 bool) (Backend, error) {
	var (
		lbBackend Backend
		err       error
	)

	if backend.ID == 0 {
		return lbBackend, fmt.Errorf("invalid backend ID 0")
	}

	u8p, err := u8proto.ParseProtocol(backend.Protocol)
	if err != nil {
		return nil, fmt.Errorf("unable to parse protocol lbBackend (%d, %s, %d, %s, %t): %w",
			backend.ID, backend.AddrCluster.String(), backend.Port, backend.Protocol, ipv6, err)
	}

	if ipv6 {
		lbBackend, err = NewBackend6V3(backend.ID, backend.AddrCluster, backend.Port, u8p,
			backend.State, backend.ZoneID)
	} else {
		lbBackend, err = NewBackend4V3(backend.ID, backend.AddrCluster, backend.Port, u8p,
			backend.State, backend.ZoneID)
	}
	if err != nil {
		return lbBackend, fmt.Errorf("unable to create lbBackend (%d, %s, %d, %t): %w",
			backend.ID, backend.AddrCluster.String(), backend.Port, ipv6, err)
	}

	return lbBackend, nil
}

func updateBackend(backend Backend) error {
	if err := BackendMap(backend.GetKey()).OpenOrCreate(); err != nil {
		return err
	}

	return BackendMap(backend.GetKey()).Update(backend.GetKey(), backend.GetValue().ToNetwork())
}

func deleteBackendLocked(key BackendKey) error {
	_, err := BackendMap(key).SilentDelete(key)
	return err
}

func updateServiceEndpoint(logger *slog.Logger, key ServiceKey, value ServiceValue) error {
	if key.GetBackendSlot() != 0 && value.RevNatKey().GetKey() == 0 {
		return fmt.Errorf("invalid RevNat ID (0) in the Service Value")
	}
	if err := ServiceMap(key).OpenOrCreate(); err != nil {
		return err
	}

	if err := ServiceMap(key).Update(key.ToNetwork(), value.ToNetwork()); err != nil {
		return err
	}

	logger.Debug(
		"Upserted service entry",
		logfields.ServiceKey, key,
		logfields.ServiceValue, value,
		logfields.BackendSlot, key.GetBackendSlot(),
	)

	return nil
}

type svcMap map[string]loadbalancer.LegacySVC

// addFE adds the give 'fe' to the svcMap without any backends. If it does not
// yet exist, an entry is created. Otherwise, the existing entry is left
// unchanged.
func (svcs svcMap) addFE(fe *loadbalancer.L3n4AddrID) *loadbalancer.LegacySVC {
	hash := fe.Hash()
	lbsvc, ok := svcs[hash]
	if !ok {
		lbsvc = loadbalancer.LegacySVC{Frontend: *fe}
		svcs[hash] = lbsvc
	}
	return &lbsvc
}

// addFEnBE adds the given 'fe' and 'be' to the svcMap. If 'fe' exists and beIndex is 0,
// the new 'be' will be appended to the list of existing backends. If beIndex is bigger
// than the size of existing backends slice, it will be created a new array with size of
// beIndex and the new 'be' will be inserted on index beIndex-1 of that new array. All
// remaining be elements will be kept on the same index and, in case the new array is
// larger than the number of backends, some elements will be empty.
func (svcs svcMap) addFEnBE(fe *loadbalancer.L3n4AddrID, be *loadbalancer.LegacyBackend, beIndex int) *loadbalancer.LegacySVC {
	hash := fe.Hash()
	lbsvc, ok := svcs[hash]
	if !ok {
		var bes []*loadbalancer.LegacyBackend
		if beIndex == 0 {
			bes = make([]*loadbalancer.LegacyBackend, 1)
			bes[0] = be
		} else {
			bes = make([]*loadbalancer.LegacyBackend, beIndex)
			bes[beIndex-1] = be
		}
		lbsvc = loadbalancer.LegacySVC{
			Frontend: *fe,
			Backends: bes,
		}
	} else {
		var bes []*loadbalancer.LegacyBackend
		if len(lbsvc.Backends) < beIndex {
			bes = make([]*loadbalancer.LegacyBackend, beIndex)
			copy(bes, lbsvc.Backends)
			lbsvc.Backends = bes
		}
		if beIndex == 0 {
			lbsvc.Backends = append(lbsvc.Backends, be)
		} else {
			lbsvc.Backends[beIndex-1] = be
		}
	}

	svcs[hash] = lbsvc
	return &lbsvc
}

// Init updates the map info defaults for sock rev nat {4,6} and LB maps and
// then initializes all LB-related maps.
func Init(registry *metrics.Registry, params InitParams) {
	if params.MaxSockRevNatMapEntries != 0 {
		MaxSockRevNat4MapEntries = params.MaxSockRevNatMapEntries
		MaxSockRevNat6MapEntries = params.MaxSockRevNatMapEntries
	}

	MaglevMapMaxEntries = params.MaglevMapMaxEntries

	initSVC(registry, params)
	initAffinity(registry, params)
	initSourceRange(registry, params)
}

// ExistsSockRevNat checks if the passed entry exists in the sock rev nat map.
func (*LBBPFMap) ExistsSockRevNat(cookie uint64, addr net.IP, port uint16) bool {
	if addr.To4() != nil {
		key := NewSockRevNat4Key(cookie, addr, port)
		if _, err := SockRevNat4Map.Lookup(key); err == nil {
			return true
		}
	} else {
		key := NewSockRevNat6Key(cookie, addr, port)
		if _, err := SockRevNat6Map.Lookup(key); err == nil {
			return true
		}
	}

	return false
}

// InitParams represents the parameters to be passed to Init().
type InitParams struct {
	IPv4, IPv6 bool

	MaxSockRevNatMapEntries                                         int
	ServiceMapMaxEntries, BackEndMapMaxEntries, RevNatMapMaxEntries int
	AffinityMapMaxEntries                                           int
	SourceRangeMapMaxEntries                                        int
	MaglevMapMaxEntries                                             int
	PerSvcLbEnabled                                                 bool
}

func BackendMap(key BackendKey) *bpf.Map {
	switch key.(type) {
	case *Backend4Key:
		return Backend4Map
	case *Backend4KeyV3:
		return Backend4MapV3
	case *Backend6Key:
		return Backend6Map
	case *Backend6KeyV3:
		return Backend6MapV3
	default:
		return nil
	}
}

func ServiceMap(key ServiceKey) *bpf.Map {
	switch key.(type) {
	case *Service4Key:
		return Service4MapV2
	case *Service6Key:
		return Service6MapV2
	default:
		return nil
	}
}

func RevNatMap(key RevNatKey) *bpf.Map {
	switch key.(type) {
	case *RevNat4Key:
		return RevNat4Map
	case *RevNat6Key:
		return RevNat6Map
	default:
		return nil
	}
}

// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package probes

import (
	"fmt"
	"net"
	"sync"

	"github.com/vishvananda/netlink"

	"github.com/cilium/cilium/pkg/datapath/linux/safenetlink"
	"github.com/cilium/cilium/pkg/netns"
)

// HaveManagedNeighbors returns nil if the host supports managed neighbor entries (NTF_EXT_MANAGED).
// On unexpected probe results this function will terminate with log.Fatal().
var HaveManagedNeighbors = sync.OnceValue(func() error {
	ns, err := netns.New()
	if err != nil {
		return fmt.Errorf("create netns: %w", err)
	}
	defer ns.Close()

	// In order to call haveManagedNeighbors safely, it has to be started
	// in a standalone netns
	return ns.Do(func() error {
		// Use a veth device instead of a dummy to avoid the kernel having to modprobe
		// the dummy kmod, which could potentially be compiled out. veth is currently
		// a hard dependency for Cilium, so safe to assume the module is available if
		// not already loaded.
		veth := &netlink.Veth{
			LinkAttrs: netlink.LinkAttrs{Name: "veth0"},
			PeerName:  "veth1",
		}

		if err := netlink.LinkAdd(veth); err != nil {
			return fmt.Errorf("failed to add dummy veth: %w", err)
		}

		neigh := netlink.Neigh{
			LinkIndex: veth.Index,
			IP:        net.IPv4(0, 0, 0, 1),
			Flags:     NTF_EXT_LEARNED,
			FlagsExt:  NTF_EXT_MANAGED,
		}

		if err := netlink.NeighAdd(&neigh); err != nil {
			return fmt.Errorf("failed to add neighbor: %w", err)
		}

		nl, err := safenetlink.NeighList(veth.Index, 0)
		if err != nil {
			return fmt.Errorf("failed to list neighbors: %w", err)
		}

		for _, n := range nl {
			if !n.IP.Equal(neigh.IP) {
				continue
			}
			if n.Flags != NTF_EXT_LEARNED {
				continue
			}
			if n.FlagsExt != NTF_EXT_MANAGED {
				continue
			}

			return nil
		}

		return ErrNotSupported
	})
})

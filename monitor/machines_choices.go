package main

import (
	"context"
	"net"
	"net/netip"
	"sort"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

type machinesChoices struct {
	Hosts     []string
	Addresses []string
}

// Choice discovery reads existing public registrations and local OS interface
// metadata. It opens no listener and performs no network request or probe.
func pairingChoices(ctx context.Context) machinesChoices {
	choices := machinesChoices{Hosts: []string{}, Addresses: []string{}}
	if registry, err := panelMachine().ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err == nil {
		if page, err := registry.Declarations(ctx); err == nil && page.Outcome == wire.DeclarationListOutcomePage {
			for _, state := range page.Declarations {
				d := state.Declaration
				if state.Readiness == wire.DeclarationReadinessDisabled || d.Remote != nil || state.Role == wire.DeclarationRoleRemote {
					continue
				}
				eligible := state.Role == wire.DeclarationRoleHost
				if state.Role == wire.DeclarationRoleProvider {
					for _, contract := range d.Contracts {
						if contract == "abstraction.inference/chat@1" || contract == "abstraction.inference/embed@1" {
							eligible = true
						}
					}
				}
				if eligible {
					choices.Hosts = append(choices.Hosts, d.Name)
				}
			}
		}
	}
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range interfaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addresses, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, address := range addresses {
				prefix, err := netip.ParsePrefix(address.String())
				if err != nil {
					continue
				}
				ip := prefix.Addr().Unmap()
				if pairingPrivateAddress(ip) {
					choices.Addresses = append(choices.Addresses, ip.String())
				}
			}
		}
	}
	sort.Strings(choices.Hosts)
	sort.Strings(choices.Addresses)
	return choices
}

func pairingPrivateAddress(ip netip.Addr) bool {
	return ip.IsValid() && ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

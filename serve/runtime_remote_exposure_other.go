//go:build !windows

package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

func systemRemoteExposure(ip netip.Addr) (remoteExposureSource, error) {
	return remoteExposureSource{check: func(ip netip.Addr) error {
		if ip.IsLoopback() {
			return nil
		}
		addresses, err := net.InterfaceAddrs()
		if err != nil {
			return err
		}
		for _, a := range addresses {
			p, err := netip.ParsePrefix(a.String())
			if err == nil && p.Addr() == ip {
				return nil
			}
		}
		return errors.New("receiving IP is not local")
	}, close: func() error { return nil }}, nil
}

func exclusiveRemoteListen(ctx context.Context, network, address string) (net.Listener, error) {
	return (&net.ListenConfig{}).Listen(ctx, network, address)
}

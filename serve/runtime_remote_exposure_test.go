package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestRemoteExposureNumericAddress(t *testing.T) {
	for _, address := range []string{"localhost:1234", ":1234", "0.0.0.0:1234", "[::]:1234", "[fe80::1%3]:1234", "[::ffff:127.0.0.1]:1234"} {
		if _, err := remoteListenAddress(address); err == nil {
			t.Errorf("accepted %q", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "[::1]:1234", "192.0.2.1:1234"} {
		if _, err := remoteListenAddress(address); err != nil {
			t.Errorf("%s: %v", address, err)
		}
	}
}

func TestRemoteExposurePublicUnknownAndGenerationFence(t *testing.T) {
	for _, phase := range []string{"public", "unknown", "snapshot-change", "bind-change", "safe"} {
		t.Run(phase, func(t *testing.T) {
			changed := make(chan struct{})
			var bound, closed atomic.Int32
			source := func(netip.Addr) (remoteExposureSource, error) {
				return remoteExposureSource{changed: changed, close: func() error { closed.Add(1); return nil }, check: func(netip.Addr) error {
					switch phase {
					case "public", "unknown":
						return errors.New(phase)
					case "snapshot-change":
						close(changed)
					}
					return nil
				}}, nil
			}
			bind := func(ctx context.Context, network, address string) (net.Listener, error) {
				if network != "tcp" || address != "127.0.0.1:0" {
					t.Fatalf("unexpected forwarded bind: %q %q", network, address)
				}
				bound.Add(1)
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if phase == "bind-change" {
					close(changed)
				}
				return l, err
			}
			l, err := listenRemoteExposure(context.Background(), "tcp", "127.0.0.1:0", source, bind)
			if phase == "safe" {
				if err != nil {
					t.Fatal(err)
				}
				if err = l.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				l.Close()
				t.Fatal("unsafe generation accepted")
			}
			if closed.Load() != 1 {
				t.Fatal("source not joined exactly once")
			}
			want := int32(0)
			if phase == "safe" || phase == "bind-change" {
				want = 1
			}
			if bound.Load() != want {
				t.Fatalf("bound %d want %d", bound.Load(), want)
			}
		})
	}
}

func TestRemoteExposureEventClosesOnlyRemote(t *testing.T) {
	changed := make(chan struct{})
	joined := make(chan struct{})
	source := func(netip.Addr) (remoteExposureSource, error) {
		return remoteExposureSource{changed: changed, check: func(netip.Addr) error { return nil }, close: func() error { close(joined); return nil }}, nil
	}
	l, err := listenRemoteExposure(context.Background(), "tcp", "127.0.0.1:0", source, func(ctx context.Context, n, a string) (net.Listener, error) {
		if n != "tcp" || a != "127.0.0.1:0" {
			t.Fatalf("unexpected forwarded bind: %q %q", n, a)
		}
		return net.Listen("tcp", "127.0.0.1:0")
	})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := l.Accept(); result <- err }()
	close(changed)
	select {
	case err := <-result:
		if !errors.Is(err, errRemoteExposure) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept not interrupted")
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("source not joined")
	}
	// Invalidation is sticky: a subsequent Accept cannot consume a connection.
	if _, err := l.Accept(); !errors.Is(err, errRemoteExposure) {
		t.Fatal(err)
	}
}

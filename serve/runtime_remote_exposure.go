package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
)

var errRemoteExposure = errors.New("remote: exposure refused; restart required")

// The platform source is subscribed before check runs. changed is sticky;
// callbacks invalidate rather than running COM or rebinding in Accept.
type remoteExposureSource struct {
	check   func(netip.Addr) error
	changed <-chan struct{}
	close   func() error
}

func remoteListenAddress(address string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || ap.Addr().IsUnspecified() || ap.Addr().Zone() != "" || ap.Addr().Is4In6() {
		return netip.AddrPort{}, fmt.Errorf("%w: explicit numeric local IP and port required", errRemoteExposure)
	}
	return ap, nil
}

func listenRuntimeRemote(ctx context.Context, network, address string) (net.Listener, error) {
	return listenRemoteExposure(ctx, network, address, systemRemoteExposure, exclusiveRemoteListen)
}

func listenRemoteExposure(ctx context.Context, network, address string,
	source func(netip.Addr) (remoteExposureSource, error), bind func(context.Context, string, string) (net.Listener, error)) (result net.Listener, resultErr error) {
	ap, err := remoteListenAddress(address)
	if err != nil || network != "tcp" {
		return nil, fmt.Errorf("%w: invalid receiving address", errRemoteExposure)
	}
	watch, err := source(ap.Addr())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errRemoteExposure, err)
	}
	keep := false
	defer func() {
		if !keep {
			resultErr = errors.Join(resultErr, watch.close())
		}
	}()
	changed := func() bool {
		select {
		case <-watch.changed:
			return true
		default:
			return false
		}
	}
	if err = watch.check(ap.Addr()); err != nil {
		return nil, fmt.Errorf("%w: %v", errRemoteExposure, err)
	}
	if changed() || ctx.Err() != nil {
		return nil, errRemoteExposure
	}
	l, err := bind(ctx, network, address)
	if err != nil {
		return nil, errors.Join(errRemoteExposure, err)
	}
	if changed() || ctx.Err() != nil {
		l.Close()
		return nil, errRemoteExposure
	}
	g := &remoteExposureListener{Listener: l, watch: watch, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(g.done)
		select {
		case <-watch.changed:
			g.Listener.Close()
		case <-g.stop:
		}
	}()
	keep = true
	return g, nil
}

type remoteExposureListener struct {
	net.Listener
	watch      remoteExposureSource
	stop, done chan struct{}
	once       sync.Once
	closeErr   error
}

func (g *remoteExposureListener) unsafe() bool {
	select {
	case <-g.watch.changed:
		return true
	default:
		return false
	}
}

func (g *remoteExposureListener) Accept() (net.Conn, error) {
	if g.unsafe() {
		return nil, errRemoteExposure
	}
	c, err := g.Listener.Accept()
	if g.unsafe() {
		if c != nil {
			c.Close()
		}
		return nil, errRemoteExposure
	}
	return c, err
}

func (g *remoteExposureListener) Close() error {
	g.once.Do(func() {
		close(g.stop)
		err := g.Listener.Close()
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		<-g.done
		g.closeErr = errors.Join(err, g.watch.close())
	})
	return g.closeErr
}

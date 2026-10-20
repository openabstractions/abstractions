package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These are the native netlistmgr.h interfaces. The binding owns one MTA
// thread, one subscription and a single sticky invalidation; callbacks do no I/O.
var nlmClass = windows.GUID{Data1: 0xDCB00C01, Data2: 0x570F, Data3: 0x4A9B, Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
var nlmManager = windows.GUID{Data1: 0xDCB00000, Data2: 0x570F, Data3: 0x4A9B, Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
var nlmEvents = windows.GUID{Data1: 0xDCB00004, Data2: 0x570F, Data3: 0x4A9B, Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B}}
var connectionPoints = windows.GUID{Data1: 0xB196B284, Data2: 0xBAB4, Data3: 0x101A, Data4: [8]byte{0xB6, 0x9C, 0, 0xAA, 0, 0x34, 0x1D, 0x07}}
var unknownInterface = windows.GUID{Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}

//go:uintptrescapes
func remoteCOMCall(object unsafe.Pointer, index int, args ...uintptr) error {
	return comCall(object, index, args...)
}

type networkEventSink struct {
	refs           atomic.Int32
	changed        chan struct{}
	released       chan struct{}
	once           sync.Once
	armed, closing atomic.Bool
}

// Native-visible memory contains only the static vtable pointer. Go callback
// state is rooted here until Unadvise and the last native Release complete.
var networkEventOwners sync.Map

func networkEventOwner(this uintptr) *networkEventSink {
	v, _ := networkEventOwners.Load(this)
	return v.(*networkEventSink)
}

func (s *networkEventSink) invalidate() { s.once.Do(func() { close(s.changed) }) }

var networkEventVtable = [7]uintptr{
	syscall.NewCallback(func(this uintptr, iid *windows.GUID, out *uintptr) uintptr {
		s := networkEventOwner(this)
		if iid == nil || out == nil {
			return 0x80004003
		}
		id := *iid
		if id != unknownInterface && id != nlmEvents {
			*out = 0
			return 0x80004002
		}
		*out = this
		s.refs.Add(1)
		return 0
	}),
	syscall.NewCallback(func(this uintptr) uintptr { return uintptr(networkEventOwner(this).refs.Add(1)) }),
	syscall.NewCallback(func(this uintptr) uintptr {
		s := networkEventOwner(this)
		n := s.refs.Add(-1)
		if n == 1 && s.armed.Load() && !s.closing.Load() {
			s.invalidate()
		}
		if n == 0 {
			close(s.released)
		}
		return uintptr(n)
	}),
	syscall.NewCallback(func(this, id uintptr) uintptr { networkEventOwner(this).invalidate(); return 0 }),
	syscall.NewCallback(func(this, id uintptr) uintptr { networkEventOwner(this).invalidate(); return 0 }),
	syscall.NewCallback(func(this, id, flags uintptr) uintptr { networkEventOwner(this).invalidate(); return 0 }),
	syscall.NewCallback(func(this, id, flags uintptr) uintptr { networkEventOwner(this).invalidate(); return 0 }),
}

// IP Helper covers address/interface reassignment independently of NLM's
// category/connectivity events. Neither callback dereferences the native row.
var remoteAddressCallback = syscall.NewCallback(func(context, row, kind uintptr) uintptr { networkEventOwner(context).invalidate(); return 0 })

type remoteAddressRegister func(uint16, uintptr, unsafe.Pointer, bool, *windows.Handle) error
type remoteAddressWatch struct {
	handles [2]windows.Handle
	cancel  func(windows.Handle) error
}

func (w *remoteAddressWatch) subscribe(object uintptr, registers [2]remoteAddressRegister) error {
	for i, register := range registers {
		if err := register(windows.AF_UNSPEC, remoteAddressCallback, unsafe.Pointer(object), false, &w.handles[i]); err != nil {
			return err
		}
		if w.handles[i] == 0 {
			return errors.New("IP change subscription returned no handle")
		}
	}
	return nil
}
func (w *remoteAddressWatch) close() error {
	var result error
	for i, h := range w.handles {
		if h != 0 {
			if err := w.cancel(h); err != nil {
				result = errors.Join(result, err)
			} else {
				w.handles[i] = 0
			}
		}
	}
	return result
}
func releaseRemoteAddressContext(object uintptr, w *remoteAddressWatch) error {
	if err := w.close(); err != nil {
		return err
	}
	networkEventOwners.Delete(object)
	_, err := windows.LocalFree(windows.Handle(object))
	return err
}

func systemRemoteExposure(ip netip.Addr) (remoteExposureSource, error) {
	if ip.IsLoopback() {
		return remoteExposureSource{check: func(netip.Addr) error { return nil }, close: func() error { return nil }}, nil
	}
	if runtime.GOARCH != "amd64" {
		return remoteExposureSource{}, errors.New("NLM receiving binding requires Windows x64")
	}
	type result struct{ err error }
	ready := make(chan result, 1)
	stop := make(chan struct{})
	done := make(chan error, 1)
	threadDone := make(chan struct{})
	sink := &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})}
	sink.refs.Store(1)
	go func() {
		var terminalErr error
		defer func() { done <- terminalErr; close(threadDone) }()
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED); err != nil {
			ready <- result{err}
			terminalErr = err
			return
		}
		defer coUninitialize.Call()
		var manager, container, point unsafe.Pointer
		hr, _, _ := coCreateInstance.Call(uintptr(unsafe.Pointer(&nlmClass)), 0, 5, uintptr(unsafe.Pointer(&nlmManager)), uintptr(unsafe.Pointer(&manager)))
		if int32(hr) < 0 || manager == nil {
			err := fmt.Errorf("NLM creation HRESULT 0x%08X", uint32(hr))
			ready <- result{err}
			terminalErr = err
			return
		}
		defer comRelease(manager)
		if err := remoteCOMCall(manager, 0, uintptr(unsafe.Pointer(&connectionPoints)), uintptr(unsafe.Pointer(&container))); err != nil {
			ready <- result{err}
			terminalErr = err
			return
		}
		defer comRelease(container)
		if err := remoteCOMCall(container, 4, uintptr(unsafe.Pointer(&nlmEvents)), uintptr(unsafe.Pointer(&point))); err != nil {
			ready <- result{err}
			terminalErr = err
			return
		}
		defer comRelease(point)
		object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
		if err != nil {
			ready <- result{err}
			terminalErr = err
			return
		}
		*(*uintptr)(unsafe.Pointer(object)) = uintptr(unsafe.Pointer(&networkEventVtable[0]))
		networkEventOwners.Store(object, sink)
		addresses := remoteAddressWatch{cancel: windows.CancelMibChangeNotify2}
		defer func() {
			// Cancellation runs on the owning MTA, never on a callback thread,
			// and returns only after its queued callbacks finish. A failed native
			// cancellation leaves the bounded callback context retained and reports
			// an owned unknown; freeing it would permit use-after-free.
			if err := releaseRemoteAddressContext(object, &addresses); err != nil {
				sink.invalidate()
				terminalErr = errors.Join(terminalErr, err)
			}
		}()
		if err := addresses.subscribe(object, [2]remoteAddressRegister{windows.NotifyUnicastIpAddressChange, windows.NotifyIpInterfaceChange}); err != nil {
			ready <- result{err}
			terminalErr = err
			return
		}
		var cookie uint32
		if err := remoteCOMCall(point, 5, object, uintptr(unsafe.Pointer(&cookie))); err != nil {
			ready <- result{err}
			terminalErr = err
			return
		}
		sink.armed.Store(true)
		// Subscription precedes the snapshot. Any intervening event invalidates it.
		err = snapshotRemoteNetwork(manager, ip)
		ready <- result{err}
		if err == nil {
			<-stop
		}
		sink.closing.Store(true)
		unadvise := remoteCOMCall(point, 6, uintptr(cookie))
		if unadvise != nil {
			sink.invalidate()
		}
		if sink.refs.Add(-1) == 0 {
			close(sink.released)
		}
		// COM's subscription owns its reference until Unadvise completes. Retain
		// pinned callback memory until every native reference is actually released.
		<-sink.released
		terminalErr = unadvise
	}()
	r := <-ready
	if r.err != nil {
		cleanupErr := <-done
		<-threadDone
		return remoteExposureSource{}, errors.Join(r.err, cleanupErr)
	}
	var once sync.Once
	var closeErr error
	return remoteExposureSource{changed: sink.changed, check: func(netip.Addr) error {
		select {
		case <-sink.changed:
			return errRemoteExposure
		default:
			return nil
		}
	}, close: func() error { once.Do(func() { close(stop); closeErr = <-done; <-threadDone }); return closeErr }}, nil
}

// The same finite snapshot classifier consumes native reads and inert receiving
// rows. Every connected network on the selected adapter must be known non-Public.
type remoteNetworkState struct {
	adapter   windows.GUID
	connected bool
	category  uint32
}

func checkRemoteNetworkSnapshot(adapter windows.GUID, next func() (remoteNetworkState, bool, error)) error {
	found := false
	for n := 0; n < 1024; n++ {
		state, more, err := next()
		if err != nil {
			return err
		}
		if !more {
			if found {
				return nil
			}
			return errors.New("bound adapter has no connected NLM network")
		}
		if state.adapter != adapter || !state.connected {
			continue
		}
		if state.category != 1 && state.category != 2 {
			return errors.New("bound adapter has Public or unknown NLM category")
		}
		found = true
	}
	return errors.New("NLM connection enumeration exceeds bound")
}
func snapshotRemoteNetwork(manager unsafe.Pointer, ip netip.Addr) error {
	adapter, err := adapterForRemoteIP(ip)
	if err != nil {
		return err
	}
	var enumeration unsafe.Pointer
	if err := remoteCOMCall(manager, 9, uintptr(unsafe.Pointer(&enumeration))); err != nil {
		return err
	}
	defer comRelease(enumeration)
	return checkRemoteNetworkSnapshot(adapter, func() (state remoteNetworkState, more bool, err error) {
		var connection unsafe.Pointer
		var fetched uint32
		if err = remoteCOMCall(enumeration, 8, 1, uintptr(unsafe.Pointer(&connection)), uintptr(unsafe.Pointer(&fetched))); err != nil {
			return
		}
		if fetched == 0 {
			return state, false, nil
		}
		if fetched != 1 || connection == nil {
			return state, false, errors.New("invalid NLM connection enumeration")
		}
		defer comRelease(connection)
		if err = remoteCOMCall(connection, 12, uintptr(unsafe.Pointer(&state.adapter))); err != nil {
			return
		}
		more = true
		if state.adapter != adapter {
			return
		}
		var connected int16
		if err = remoteCOMCall(connection, 9, uintptr(unsafe.Pointer(&connected))); err != nil {
			return
		}
		state.connected = connected != 0
		if !state.connected {
			return
		}
		var network unsafe.Pointer
		if err = remoteCOMCall(connection, 7, uintptr(unsafe.Pointer(&network))); err != nil {
			return
		}
		defer comRelease(network)
		err = remoteCOMCall(network, 18, uintptr(unsafe.Pointer(&state.category)))
		return
	})
}

func adapterForRemoteIP(ip netip.Addr) (windows.GUID, error) {
	// GetAdaptersAddresses requires a contiguous caller buffer; bounded retries
	// handle topology growth without an unbounded allocation or repeated polling.
	size := uint32(15000)
	for attempt := 0; attempt < 3; attempt++ {
		if size > 1<<20 {
			return windows.GUID{}, errors.New("adapter snapshot exceeds bound")
		}
		buf := make([]byte, size)
		first := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, 0, 0, first, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return windows.GUID{}, err
		}
		var selected windows.GUID
		found := false
		count := 0
		for a := first; a != nil; a = a.Next {
			count++
			if count > 1024 {
				return selected, errors.New("adapter count exceeds bound")
			}
			for u, n := a.FirstUnicastAddress, 0; u != nil; u, n = u.Next, n+1 {
				if n > 1024 {
					return selected, errors.New("adapter address count exceeds bound")
				}
				addr, ok := netip.AddrFromSlice(u.Address.IP())
				if !ok {
					continue
				}
				addr = addr.Unmap()
				if addr == ip {
					id, err := windows.GUIDFromString(windows.BytePtrToString(a.AdapterName))
					if err != nil {
						return selected, err
					}
					if found && id != selected {
						return selected, errors.New("ambiguous bound adapter")
					}
					selected = id
					found = true
				}
			}
		}
		runtime.KeepAlive(buf)
		if found {
			return selected, nil
		}
		return selected, errors.New("receiving IP is not assigned to a local adapter")
	}
	return windows.GUID{}, errors.New("unstable adapter snapshot")
}

func exclusiveRemoteListen(ctx context.Context, network, address string) (net.Listener, error) {
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var optionErr error
		err := c.Control(func(fd uintptr) {
			optionErr = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, ^windows.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return optionErr
	}}
	return lc.Listen(ctx, network, address)
}

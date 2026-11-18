package main

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRemotePortExclusive(t *testing.T) {
	l, err := exclusiveRemoteListen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var e error
		err := c.Control(func(fd uintptr) {
			e = windows.SetsockoptInt(windows.Handle(fd), windows.SOL_SOCKET, windows.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return e
	}}
	host, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" {
		t.Fatalf("unexpected exclusive listener host: %q", host)
	}
	other, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", port))
	if err == nil {
		other.Close()
		t.Fatal("REUSEADDR stole exclusive port")
	}
}

func TestRemoteAddressSubscriptionsFenceAndLifetime(t *testing.T) {
	for _, phase := range []string{"snapshot", "bind", "active"} {
		t.Run(phase, func(t *testing.T) {
			object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
			if err != nil {
				t.Fatal(err)
			}
			s := &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})}
			networkEventOwners.Store(object, s)
			var registered int
			var callbacks [2]uintptr
			var watch remoteAddressWatch
			watch.cancel = func(h windows.Handle) error { return nil }
			register := func(family uint16, callback uintptr, ctx unsafe.Pointer, initial bool, out *windows.Handle) error {
				if family != windows.AF_UNSPEC || initial || uintptr(ctx) != object {
					t.Fatal("wrong native subscription")
				}
				callbacks[registered] = callback
				registered++
				*out = windows.Handle(registered)
				return nil
			}
			if err = watch.subscribe(object, [2]remoteAddressRegister{register, register}); err != nil {
				t.Fatal(err)
			}
			fire := func(index int) { syscall.SyscallN(callbacks[index], object, 0, 1) }
			factory := func(netip.Addr) (remoteExposureSource, error) {
				if registered != 2 {
					t.Fatal("snapshot before both subscriptions")
				}
				return remoteExposureSource{changed: s.changed, check: func(netip.Addr) error {
					if phase == "snapshot" {
						fire(0)
					}
					return nil
				}, close: func() error { return releaseRemoteAddressContext(object, &watch) }}, nil
			}
			bind := func(ctx context.Context, n, a string) (net.Listener, error) {
				if n != "tcp" || a != "127.0.0.1:0" {
					t.Fatalf("unexpected forwarded bind: %q %q", n, a)
				}
				l, err := net.Listen("tcp", "127.0.0.1:0")
				if phase == "bind" {
					fire(1)
				}
				return l, err
			}
			l, err := listenRemoteExposure(context.Background(), "tcp", "127.0.0.1:0", factory, bind)
			if phase == "active" {
				if err != nil {
					t.Fatal(err)
				}
				fire(0)
				if _, err = l.Accept(); !errors.Is(err, errRemoteExposure) {
					t.Fatal(err)
				}
				if err = l.Close(); err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, errRemoteExposure) {
				t.Fatal(err)
			}
			if _, exists := networkEventOwners.Load(object); exists {
				t.Fatal("completed cancellation retained context")
			}
		})
	}
	// Fake native cancellation blocks while a queued callback still owns context.
	object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
	if err != nil {
		t.Fatal(err)
	}
	s := &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})}
	networkEventOwners.Store(object, s)
	entered, release, joined := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	watch := remoteAddressWatch{handles: [2]windows.Handle{1, 0}, cancel: func(windows.Handle) error { close(entered); <-release; return nil }}
	go func() { joined <- releaseRemoteAddressContext(object, &watch) }()
	<-entered
	syscall.SyscallN(remoteAddressCallback, object, 0, 1)
	if _, exists := networkEventOwners.Load(object); !exists {
		t.Fatal("context freed before native cancellation returned")
	}
	close(release)
	select {
	case err := <-joined:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation not joined")
	}
}

func TestRemoteAddressSubscriptionFailureRetainsContext(t *testing.T) {
	object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
	if err != nil {
		t.Fatal(err)
	}
	s := &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})}
	networkEventOwners.Store(object, s)
	watch := remoteAddressWatch{handles: [2]windows.Handle{1, 0}, cancel: func(windows.Handle) error { return errors.New("native cancel failed") }}
	if err := releaseRemoteAddressContext(object, &watch); err == nil {
		t.Fatal("failed cancellation accepted")
	}
	if _, exists := networkEventOwners.Load(object); !exists {
		t.Fatal("failed cancellation freed callback context")
	}
	watch.cancel = func(windows.Handle) error { return nil }
	if err := releaseRemoteAddressContext(object, &watch); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteAddressPartialSubscriptionRefuses(t *testing.T) {
	object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
	if err != nil {
		t.Fatal(err)
	}
	networkEventOwners.Store(object, &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})})
	registered, canceled := 0, 0
	register := func(_ uint16, _ uintptr, _ unsafe.Pointer, initial bool, out *windows.Handle) error {
		if initial {
			t.Fatal("initial callback requested")
		}
		registered++
		if registered == 2 {
			return errors.New("interface subscription unavailable")
		}
		*out = 1
		return nil
	}
	watch := remoteAddressWatch{cancel: func(h windows.Handle) error {
		if h != 1 {
			t.Fatal("unknown cancellation handle")
		}
		canceled++
		return nil
	}}
	if err := watch.subscribe(object, [2]remoteAddressRegister{register, register}); err == nil {
		t.Fatal("partial subscription accepted")
	}
	if err := releaseRemoteAddressContext(object, &watch); err != nil {
		t.Fatal(err)
	}
	if registered != 2 || canceled != 1 {
		t.Fatalf("partial lifetime %d %d", registered, canceled)
	}
}

func TestRemoteNetworkSnapshotClassifications(t *testing.T) {
	a, b := windows.GUID{Data1: 1}, windows.GUID{Data1: 2}
	for _, tc := range []struct {
		name    string
		states  []remoteNetworkState
		failure bool
		want    bool
	}{
		{"private-domain", []remoteNetworkState{{a, true, 1}, {a, true, 2}}, false, true},
		{"mixed-public", []remoteNetworkState{{a, true, 1}, {a, true, 0}}, false, false},
		{"mixed-unknown", []remoteNetworkState{{a, true, 1}, {a, true, 9}}, false, false},
		{"other-adapter", []remoteNetworkState{{b, true, 0}, {a, true, 1}}, false, true},
		{"disconnected", []remoteNetworkState{{a, false, 1}}, false, false},
		{"native-error", []remoteNetworkState{{a, true, 1}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			err := checkRemoteNetworkSnapshot(a, func() (remoteNetworkState, bool, error) {
				if i == len(tc.states) {
					if tc.failure {
						return remoteNetworkState{}, false, errors.New("native read failed")
					}
					return remoteNetworkState{}, false, nil
				}
				state := tc.states[i]
				i++
				return state, true, nil
			})
			if (err == nil) != tc.want {
				t.Fatalf("classification: %v", err)
			}
		})
	}
}

func TestRemoteNetworkCallbackLifetime(t *testing.T) {
	// Exercise the real callback vtable with owned native memory, without COM
	// activation or any query/change to this host's network classification.
	object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(object))
	s := &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})}
	s.refs.Store(1)
	networkEventOwners.Store(object, s)
	defer networkEventOwners.Delete(object)
	*(*uintptr)(nativePointer(object)) = uintptr(unsafe.Pointer(&networkEventVtable[0]))
	var out uintptr
	if err := remoteCOMCall(nativePointer(object), 0, uintptr(unsafe.Pointer(&nlmEvents)), uintptr(unsafe.Pointer(&out))); err != nil {
		t.Fatal(err)
	}
	if out != object || s.refs.Load() != 2 {
		t.Fatalf("QueryInterface reference not owned: out=%x object=%x refs=%d", out, object, s.refs.Load())
	}
	s.armed.Store(true)
	if err := comCall(nativePointer(object), 6, 0, 0x10); err != nil {
		t.Fatal(err)
	}
	if err := comCall(nativePointer(object), 3, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.changed:
	default:
		t.Fatal("category event did not invalidate")
	}
	comRelease(nativePointer(object))
	if s.refs.Load() != 1 {
		t.Fatal("native reference not released")
	}
	comRelease(nativePointer(object))
	select {
	case <-s.released:
	default:
		t.Fatal("last release not observed")
	}
}

func TestRemoteNetworkSubscriptionLossInvalidates(t *testing.T) {
	object, err := windows.LocalAlloc(0, uint32(unsafe.Sizeof(uintptr(0))))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.LocalFree(windows.Handle(object))
	s := &networkEventSink{changed: make(chan struct{}), released: make(chan struct{})}
	s.refs.Store(2)
	s.armed.Store(true)
	networkEventOwners.Store(object, s)
	defer networkEventOwners.Delete(object)
	*(*uintptr)(nativePointer(object)) = uintptr(unsafe.Pointer(&networkEventVtable[0]))
	comRelease(nativePointer(object))
	select {
	case <-s.changed:
	default:
		t.Fatal("lost subscription remained safe")
	}
	comRelease(nativePointer(object))
	<-s.released
}

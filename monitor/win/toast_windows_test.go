//go:build windows

package win

import (
	"crypto/rand"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var coCreateInstance = ole32.NewProc("CoCreateInstance")

// Every WinRT step of Show runs: the XML document loads and the toast is
// built with its tag, group, quiet flag and expiry. Windows 11 shows a quiet
// toast for an unregistered sender straight into the notification center,
// and older builds refuse only the final IToastNotifier.Show; either way the
// test removes what it showed. A malformed document is refused at load.
func TestToastShowBuildsQuietToastAndRemovesIt(t *testing.T) {
	center, err := OpenToasts("OpenAbstractions.Test.Unregistered", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer center.Close()
	toast := Toast{XML: `<toast><visual><binding template="ToastGeneric"><text>OpenAbstractions test</text></binding></visual></toast>`, Tag: "a1", Group: "g1", Expires: time.Now().Add(time.Minute), Silently: true}
	if err = center.Show(toast); err != nil {
		if want := "COM call slot 6"; len(err.Error()) < len(want) || err.Error()[:len(want)] != want {
			t.Fatalf("only IToastNotifier.Show may refuse an unregistered sender: %v", err)
		}
	} else if err := center.Remove("a1", "g1"); err != nil {
		t.Fatalf("the shown test toast must be removable: %v", err)
	}
	toast.XML = "<toast"
	if err := center.Show(toast); err == nil || err.Error()[:9] != "toast XML" {
		t.Fatalf("a malformed document is refused at load: %v", err)
	}
	if _, err := center.Enabled(); err != nil {
		t.Fatalf("the notifier setting reads for any sender: %v", err)
	}
}

// The activator class object answers COM the way Windows calls it for a
// toast click: CoCreateInstance through the local-server class table, then
// INotificationActivationCallback.Activate with the click's arguments. No
// registry entry is needed while this process holds the registration.
func TestToastActivatorReceivesActivate(t *testing.T) {
	var clsid windows.GUID
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	clsid = *(*windows.GUID)(unsafe.Pointer(&raw[0]))
	got := make(chan string, 1)
	center, err := OpenToasts("OpenAbstractions.Test", &clsid, func(args string) { got <- args })
	if err != nil {
		t.Fatal(err)
	}
	defer center.Close()

	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		coInitializeEx.Call(0, coinitMultithreaded) //unchecked: a failed initialization fails CoCreateInstance below
		var callback comObject
		if r, _, _ := coCreateInstance.Call(uintptr(unsafe.Pointer(&clsid)), 0, clsctxLocalServer, uintptr(unsafe.Pointer(&iidNotificationActivator)), uintptr(unsafe.Pointer(&callback))); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer
			done <- windows.Errno(r)
			return
		}
		defer callback.release()
		app, _ := windows.UTF16PtrFromString("OpenAbstractions.Test")
		args, _ := windows.UTF16PtrFromString("oa-notification:id=a1&a=load&t=t1")
		done <- callback.call(3, uintptr(unsafe.Pointer(app)), uintptr(unsafe.Pointer(args)), 0, 0)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CoCreateInstance did not return")
	}
	select {
	case args := <-got:
		if args != "oa-notification:id=a1&a=load&t=t1" {
			t.Fatal(args)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the activator never received the click")
	}
}

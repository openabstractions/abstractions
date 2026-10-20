//go:build windows

package win

// An in-house binding for Windows toast notifications (ASK-N7), with no
// Windows App SDK: the WinRT classes Windows.UI.Notifications and
// Windows.Data.Xml.Dom reached through combase.dll, and the classic
// INotificationActivationCallback COM activator that Windows calls when a
// person clicks a toast, including after this process exited.
//
// Interface IDs, parameterized IIDs and vtable order are transcribed from the
// Windows SDK 10.0.26100.0 headers winrt/windows.ui.notifications.h,
// winrt/windows.data.xml.dom.h, winrt/windows.foundation.h and
// um/notificationactivationcallback.h. Every WinRT interface starts with
// IInspectable's six slots: QueryInterface, AddRef, Release, GetIids,
// GetRuntimeClassName and GetTrustLevel. Method slots below count from 6.
//
// An unpackaged program is a toast sender only through a Start-menu shortcut
// carrying its System.AppUserModel.ID and System.AppUserModel.ToastActivatorCLSID,
// with the CLSID's LocalServer32 naming this executable (installer/abstraction.wxs).
// Microsoft documents that pair: "Respond to toast activations",
// learn.microsoft.com/previous-versions/windows/desktop/win32_tile_badge_notif/respond-to-toast-activations.

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	combase                = windows.NewLazySystemDLL("combase.dll")
	windowsCreateString    = combase.NewProc("WindowsCreateString")
	windowsDeleteString    = combase.NewProc("WindowsDeleteString")
	roGetActivationFactory = combase.NewProc("RoGetActivationFactory")
	roActivateInstance     = combase.NewProc("RoActivateInstance")
	coRegisterClassObject  = ole32.NewProc("CoRegisterClassObject")
	coRevokeClassObject    = ole32.NewProc("CoRevokeClassObject")
)

const (
	coinitMultithreaded = 0x0
	clsctxLocalServer   = 0x4
	regclsMultipleUse   = 0x1
	eNoInterface        = 0x80004002
	ePointer            = 0x80004003
	classENoAggregation = 0x80040110
)

// Interface IDs from the Windows SDK 10.0.26100.0 headers named above.
var (
	iidToastNotificationManagerStatics  = windows.GUID{Data1: 0x50ac103f, Data2: 0xd235, Data3: 0x4598, Data4: [8]byte{0xbb, 0xef, 0x98, 0xfe, 0x4d, 0x1a, 0x3a, 0xd4}}
	iidToastNotificationManagerStatics2 = windows.GUID{Data1: 0x7ab93c52, Data2: 0x0e48, Data3: 0x4750, Data4: [8]byte{0xba, 0x9d, 0x1a, 0x41, 0x13, 0x98, 0x18, 0x47}}
	iidToastNotificationFactory         = windows.GUID{Data1: 0x04124b20, Data2: 0x82c6, Data3: 0x4229, Data4: [8]byte{0xb1, 0x09, 0xfd, 0x9e, 0xd4, 0x66, 0x2b, 0x53}}
	iidToastNotification                = windows.GUID{Data1: 0x997e2675, Data2: 0x059e, Data3: 0x4e60, Data4: [8]byte{0x8b, 0x06, 0x17, 0x60, 0x91, 0x7c, 0x8b, 0x80}}
	iidToastNotification2               = windows.GUID{Data1: 0x9dfb9fd1, Data2: 0x143a, Data3: 0x490e, Data4: [8]byte{0x90, 0xbf, 0xb9, 0xfb, 0xa7, 0x13, 0x2d, 0xe7}}
	iidXMLDocument                      = windows.GUID{Data1: 0xf7f3a506, Data2: 0x1e87, Data3: 0x42d6, Data4: [8]byte{0xbc, 0xfb, 0xb8, 0xc8, 0x09, 0xfa, 0x54, 0x94}}
	iidXMLDocumentIO                    = windows.GUID{Data1: 0x6cd0e74e, Data2: 0xee65, Data3: 0x4489, Data4: [8]byte{0x9e, 0xbf, 0xca, 0x43, 0xe8, 0x7b, 0xa6, 0x37}}
	iidPropertyValueStatics             = windows.GUID{Data1: 0x629bdbc8, Data2: 0xd932, Data3: 0x4ff4, Data4: [8]byte{0x96, 0xb9, 0x8d, 0x96, 0xc5, 0xc1, 0xe8, 0x58}}
	iidReferenceDateTime                = windows.GUID{Data1: 0x5541d8a7, Data2: 0x497c, Data3: 0x5aa4, Data4: [8]byte{0x86, 0xfc, 0x77, 0x13, 0xad, 0xbf, 0x2a, 0x2c}}
	// TypedEventHandler<ToastNotification, ToastDismissedEventArgs>.
	iidToastDismissedHandler = windows.GUID{Data1: 0x61c2402f, Data2: 0x0ed0, Data3: 0x5a18, Data4: [8]byte{0xab, 0x69, 0x59, 0xf4, 0xaa, 0x99, 0xa3, 0x68}}
	iidToastDismissedArgs    = windows.GUID{Data1: 0x3f89d935, Data2: 0xd9cb, Data3: 0x4538, Data4: [8]byte{0xa0, 0xf0, 0xff, 0xe7, 0x65, 0x99, 0x38, 0xf8}}
	iidClassFactory          = windows.GUID{Data1: 0x00000001, Data2: 0, Data3: 0, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidNotificationActivator = windows.GUID{Data1: 0x53e31837, Data2: 0x6600, Data3: 0x4a81, Data4: [8]byte{0x93, 0x95, 0x75, 0xcf, 0xfe, 0x74, 0x6f, 0x94}}
	iidUnknownGUID           = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

// Slots counted from the start of the vtable (IInspectable is 0..5).
const (
	slotQueryInterface = 0
	slotRelease        = 2

	slotCreateToastNotifierWithID = 7  // IToastNotificationManagerStatics
	slotGetHistory                = 6  // IToastNotificationManagerStatics2
	slotRemoveGroupedTagWithID    = 8  // IToastNotificationHistory
	slotCreateToastNotification   = 6  // IToastNotificationFactory
	slotPutExpirationTime         = 7  // IToastNotification
	slotAddDismissed              = 9  // IToastNotification
	slotPutTag                    = 6  // IToastNotification2
	slotPutGroup                  = 8  // IToastNotification2
	slotPutSuppressPopup          = 10 // IToastNotification2
	slotShow                      = 6  // IToastNotifier
	slotGetSetting                = 8  // IToastNotifier
	slotLoadXML                   = 6  // IXmlDocumentIO
	slotCreateDateTime            = 21 // IPropertyValueStatics
	slotDismissedReason           = 6  // IToastDismissedEventArgs
)

// comObject is any COM interface pointer: its first word is the vtable. It
// holds the pointer as a pointer, never as a uintptr, so reading the vtable
// converts no integer to a pointer: the race build's checkptr refuses that
// conversion for an object this process implements on the Go heap, such as
// the toast activator a same-process CoCreateInstance hands back. Its one
// field is the whole layout, so an out parameter (&object) receives the
// interface pointer COM writes.
type comObject struct{ p *comHead }

// comHead is the first word of a COM object: its vtable, a run of method
// pointers from slot 0.
type comHead struct{ vtbl *comProc }

func (o comObject) ptr() uintptr { return uintptr(unsafe.Pointer(o.p)) }

func (o comObject) method(slot int) comProc {
	return unsafe.Slice(o.p.vtbl, slot+1)[slot]
}

func (o comObject) call(slot int, args ...uintptr) error {
	if hr := o.method(slot).call(append([]uintptr{o.ptr()}, args...)...); int32(hr) < 0 {
		return fmt.Errorf("COM call slot %d: %#x", slot, uint32(hr))
	}
	return nil
}

func (o comObject) release() {
	if o.p != nil {
		o.method(slotRelease).call(o.ptr())
	}
}

func (o comObject) query(iid *windows.GUID) (comObject, error) {
	var out comObject
	err := o.call(slotQueryInterface, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	return out, err
}

// hstring is a WinRT string handle; free it with deleteHString.
type hstring uintptr

func newHString(s string) (hstring, error) {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return 0, err
	}
	var h hstring
	if r, _, _ := windowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h))); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer
		return 0, fmt.Errorf("WindowsCreateString: %#x", uint32(r))
	}
	return h, nil
}

func (h hstring) delete() {
	if h != 0 {
		windowsDeleteString.Call(uintptr(h)) //unchecked: freeing a string handle has no failure a caller can act on
	}
}

func activationFactory(class string, iid *windows.GUID) (comObject, error) {
	name, err := newHString(class)
	if err != nil {
		return comObject{}, err
	}
	defer name.delete()
	var factory comObject
	if r, _, _ := roGetActivationFactory.Call(uintptr(name), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&factory))); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer
		return comObject{}, fmt.Errorf("RoGetActivationFactory %s: %#x", class, uint32(r))
	}
	return factory, nil
}

func activateInstance(class string) (comObject, error) {
	name, err := newHString(class)
	if err != nil {
		return comObject{}, err
	}
	defer name.delete()
	var instance comObject
	if r, _, _ := roActivateInstance.Call(uintptr(name), uintptr(unsafe.Pointer(&instance))); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer
		return comObject{}, fmt.Errorf("RoActivateInstance %s: %#x", class, uint32(r))
	}
	return instance, nil
}

// Toast is one native notification. XML is the complete toast document;
// Tag and Group address it later for removal.
type Toast struct {
	XML      string
	Tag      string
	Group    string
	Expires  time.Time
	Silently bool
}

// ToastCenter shows and removes this program's toasts and receives the
// clicks Windows delivers to its activator. Every WinRT call runs on one
// locked thread of the COM multithreaded apartment.
type ToastCenter struct {
	appID   string
	work    chan func()
	done    chan struct{}
	once    sync.Once
	cookie  uint32
	factory *classFactory
	// OnDismissed, when set before Show, is called with a toast's tag when
	// the person closed it while this process holds it.
	OnDismissed func(tag string)
}

// OpenToasts starts the toast thread for appID. When clsid is non-nil it also
// registers the activator class object, and onActivate receives each click's
// arguments ("" for none) on a COM worker thread.
func OpenToasts(appID string, clsid *windows.GUID, onActivate func(args string)) (*ToastCenter, error) {
	c := &ToastCenter{appID: appID, work: make(chan func()), done: make(chan struct{})}
	started := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if r, _, _ := coInitializeEx.Call(0, coinitMultithreaded); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer
			started <- fmt.Errorf("CoInitializeEx: %#x", uint32(r))
			return
		}
		if clsid != nil {
			c.factory = newClassFactory(onActivate)
			var cookie uint32
			if r, _, _ := coRegisterClassObject.Call(uintptr(unsafe.Pointer(clsid)), uintptr(unsafe.Pointer(c.factory)), clsctxLocalServer, regclsMultipleUse, uintptr(unsafe.Pointer(&cookie))); int32(r) < 0 { //unchecked: the HRESULT in r1 is the answer
				started <- fmt.Errorf("CoRegisterClassObject: %#x", uint32(r))
				return
			}
			c.cookie = cookie
		}
		started <- nil
		for {
			select {
			case f := <-c.work:
				f()
			case <-c.done:
				if c.cookie != 0 {
					coRevokeClassObject.Call(uintptr(c.cookie)) //unchecked: revoking at exit has no recovery
				}
				return
			}
		}
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return c, nil
}

// Close revokes the activator and stops the toast thread.
func (c *ToastCenter) Close() { c.once.Do(func() { close(c.done) }) }

func (c *ToastCenter) run(f func() error) error {
	result := make(chan error, 1)
	select {
	case c.work <- func() { result <- f() }:
	case <-c.done:
		return errors.New("toasts closed")
	}
	return <-result
}

func (c *ToastCenter) notifier() (comObject, error) {
	statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics)
	if err != nil {
		return comObject{}, err
	}
	defer statics.release()
	id, err := newHString(c.appID)
	if err != nil {
		return comObject{}, err
	}
	defer id.delete()
	var notifier comObject
	if err := statics.call(slotCreateToastNotifierWithID, uintptr(id), uintptr(unsafe.Pointer(&notifier))); err != nil {
		return comObject{}, fmt.Errorf("CreateToastNotifierWithId: %w", err)
	}
	return notifier, nil
}

// Enabled reports whether Windows shows this program's toasts: the person's
// or a policy's notification switch for it, read by IToastNotifier.Setting.
func (c *ToastCenter) Enabled() (bool, error) {
	enabled := false
	err := c.run(func() error {
		notifier, err := c.notifier()
		if err != nil {
			return err
		}
		defer notifier.release()
		var setting int32
		if err := notifier.call(slotGetSetting, uintptr(unsafe.Pointer(&setting))); err != nil {
			return err
		}
		enabled = setting == 0 // NotificationSetting_Enabled
		return nil
	})
	return enabled, err
}

// Show displays one toast, replacing any earlier one with the same tag and group.
func (c *ToastCenter) Show(t Toast) error {
	return c.run(func() error {
		xml, err := activateInstance("Windows.Data.Xml.Dom.XmlDocument")
		if err != nil {
			return err
		}
		defer xml.release()
		io, err := xml.query(&iidXMLDocumentIO)
		if err != nil {
			return err
		}
		defer io.release()
		content, err := newHString(t.XML)
		if err != nil {
			return err
		}
		defer content.delete()
		if err := io.call(slotLoadXML, uintptr(content)); err != nil {
			return fmt.Errorf("toast XML: %w", err)
		}
		document, err := xml.query(&iidXMLDocument)
		if err != nil {
			return err
		}
		defer document.release()
		factory, err := activationFactory("Windows.UI.Notifications.ToastNotification", &iidToastNotificationFactory)
		if err != nil {
			return err
		}
		defer factory.release()
		var toast comObject
		if err := factory.call(slotCreateToastNotification, document.ptr(), uintptr(unsafe.Pointer(&toast))); err != nil {
			return err
		}
		defer toast.release()
		second, err := toast.query(&iidToastNotification2)
		if err != nil {
			return err
		}
		defer second.release()
		for _, set := range []struct {
			slot  int
			value string
		}{{slotPutTag, t.Tag}, {slotPutGroup, t.Group}} {
			h, err := newHString(set.value)
			if err != nil {
				return err
			}
			err = second.call(set.slot, uintptr(h))
			h.delete()
			if err != nil {
				return err
			}
		}
		if t.Silently {
			if err := second.call(slotPutSuppressPopup, 1); err != nil {
				return err
			}
		}
		if !t.Expires.IsZero() {
			if err := setExpiration(toast, t.Expires); err != nil {
				return err
			}
		}
		if c.OnDismissed != nil {
			h := newDismissedHandler(t.Tag, c.OnDismissed)
			var token int64
			//unchecked: a failed registration only loses close reporting, which ASK-N7 makes optional
			_ = toast.call(slotAddDismissed, uintptr(unsafe.Pointer(h)), uintptr(unsafe.Pointer(&token)))
		}
		notifier, err := c.notifier()
		if err != nil {
			return err
		}
		defer notifier.release()
		return notifier.call(slotShow, toast.ptr())
	})
}

// setExpiration gives the toast Windows' own removal time, an
// IReference<DateTime> made by PropertyValue.CreateDateTime.
func setExpiration(toast comObject, at time.Time) error {
	statics, err := activationFactory("Windows.Foundation.PropertyValue", &iidPropertyValueStatics)
	if err != nil {
		return err
	}
	defer statics.release()
	// DateTime is 100-nanosecond intervals since 1601-01-01 UTC.
	universal := at.UTC().UnixNano()/100 + 116444736000000000
	var boxed comObject
	if err := statics.call(slotCreateDateTime, uintptr(universal), uintptr(unsafe.Pointer(&boxed))); err != nil {
		return err
	}
	defer boxed.release()
	reference, err := boxed.query(&iidReferenceDateTime)
	if err != nil {
		return err
	}
	defer reference.release()
	return toast.call(slotPutExpirationTime, reference.ptr())
}

// Remove takes one toast out of the notification center.
func (c *ToastCenter) Remove(tag, group string) error {
	return c.run(func() error {
		statics, err := activationFactory("Windows.UI.Notifications.ToastNotificationManager", &iidToastNotificationManagerStatics2)
		if err != nil {
			return err
		}
		defer statics.release()
		var history comObject
		if err := statics.call(slotGetHistory, uintptr(unsafe.Pointer(&history))); err != nil {
			return err
		}
		defer history.release()
		strings := make([]hstring, 0, 3)
		defer func() {
			for _, h := range strings {
				h.delete()
			}
		}()
		for _, s := range []string{tag, group, c.appID} {
			h, err := newHString(s)
			if err != nil {
				return err
			}
			strings = append(strings, h)
		}
		return history.call(slotRemoveGroupedTagWithID, uintptr(strings[0]), uintptr(strings[1]), uintptr(strings[2]))
	})
}

// classFactory is the IClassFactory COM calls to create the activator.
type classFactory struct {
	vtbl      *classFactoryVtbl
	activator *activator
}
type classFactoryVtbl struct {
	iUnknownVtbl
	CreateInstance comProc
	LockServer     comProc
}

// activator is INotificationActivationCallback.
type activator struct{ vtbl *activatorVtbl }
type activatorVtbl struct {
	iUnknownVtbl
	Activate comProc
}

// liveObjects keeps every COM object this file hands to Windows reachable;
// Windows holds raw pointers to them.
var liveObjects sync.Map

func newClassFactory(onActivate func(string)) *classFactory {
	a := &activator{vtbl: &activatorVtbl{
		iUnknownVtbl: constUnknown(syscall.GUID(iidNotificationActivator)),
		Activate: comProc(syscall.NewCallback(func(this uintptr, appID *uint16, args *uint16, data uintptr, count uint32) uintptr {
			invoked := ""
			if args != nil {
				invoked = windows.UTF16PtrToString(args)
			}
			if onActivate != nil {
				go onActivate(invoked)
			}
			return 0
		})),
	}}
	f := &classFactory{activator: a}
	f.vtbl = &classFactoryVtbl{
		iUnknownVtbl: constUnknown(syscall.GUID(iidClassFactory)),
		CreateInstance: comProc(syscall.NewCallback(func(this, outer uintptr, riid *windows.GUID, ppv *uintptr) uintptr {
			if ppv == nil || riid == nil {
				return ePointer
			}
			*ppv = 0
			if outer != 0 {
				return classENoAggregation
			}
			if *riid != iidNotificationActivator && *riid != iidUnknownGUID {
				return eNoInterface
			}
			*ppv = uintptr(unsafe.Pointer(a))
			return 0
		})),
		LockServer: comProc(syscall.NewCallback(func(this uintptr, lock int32) uintptr { return 0 })),
	}
	liveObjects.Store(f, a)
	return f
}

// dismissedHandler is TypedEventHandler<ToastNotification, ToastDismissedEventArgs>.
// Every handler shares one vtable: syscall.NewCallback trampolines are a
// bounded process-wide resource, and a presenter shows toasts all session.
// Invoke finds its handler through the this pointer, which is the handler.
type dismissedHandler struct {
	vtbl        *handlerVtbl
	tag         string
	onDismissed func(string)
}

var (
	dismissedVtblOnce sync.Once
	dismissedVtbl     *handlerVtbl
)

func newDismissedHandler(tag string, onDismissed func(string)) *dismissedHandler {
	dismissedVtblOnce.Do(func() {
		dismissedVtbl = &handlerVtbl{
			iUnknownVtbl: constUnknown(syscall.GUID(iidToastDismissedHandler)),
			Invoke: comProc(syscall.NewCallback(func(this, sender uintptr, args *comHead) uintptr {
				h, ok := liveObjects.Load(this)
				if !ok || args == nil {
					return 0
				}
				handler := h.(*dismissedHandler)
				dismissed, err := comObject{args}.query(&iidToastDismissedArgs)
				if err != nil {
					return 0
				}
				defer dismissed.release()
				var reason int32
				// ToastDismissalReason: UserCanceled 0, ApplicationHidden 1, TimedOut 2.
				if dismissed.call(slotDismissedReason, uintptr(unsafe.Pointer(&reason))) == nil && reason == 0 {
					go handler.onDismissed(handler.tag)
				}
				return 0
			})),
		}
	})
	h := &dismissedHandler{vtbl: dismissedVtbl, tag: tag, onDismissed: onDismissed}
	liveObjects.Store(uintptr(unsafe.Pointer(h)), h)
	return h
}

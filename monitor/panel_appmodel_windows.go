//go:build windows

package main

import (
	"errors"
	"fmt"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"golang.org/x/sys/windows"
)

// All structs below project the frozen appmodel.h pointer ABI on Windows 64-bit hosts.
// Spans are borrowed only during a call; output buffers are caller-owned.
type panelNativeSpan struct {
	data unsafe.Pointer
	size uintptr
}
type panelNativeBuffer struct {
	data           unsafe.Pointer
	capacity, size uintptr
}
type panelNativeError struct{ word, offset uintptr }
type panelNativeEvent struct {
	kind                             uint32
	pad                              uint32
	context, call, request, callback uint64
}

type panelWindowsModel struct {
	mu             sync.Mutex
	library        windows.Handle
	procedures     map[string]uintptr
	model, session uintptr
	application    string
	verbs          map[uint64]string
	retired        bool
}

var panelCreateNativeModel = loadPanelNativeModel

func loadPanelNativeModel(instance string, manifest []byte) (panelNativeModel, error) {
	program, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return openPanelNativeModel(filepath.Join(filepath.Dir(program), "abstraction_facade_appmodel.dll"), instance, manifest)
}

func openPanelNativeModel(path, instance string, manifest []byte) (_ panelNativeModel, failure error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("Panel native model library must be absolute")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, errors.New("Panel native model projection requires a Windows 64-bit host")
	}
	library, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_DEFAULT_DIRS)
	if err != nil {
		return nil, err
	}
	p := &panelWindowsModel{library: library, procedures: map[string]uintptr{}, verbs: map[uint64]string{}}
	defer func() {
		if failure != nil {
			_ = p.Close()
		}
	}()
	for _, suffix := range []string{"abi_version", "default_bounds", "create_p", "request_id", "pending", "session_activation_p", "callback_fault", "next", "complete_activation_p", "close", "destroy", "session_create_p", "session_receive_p", "session_accept_p", "session_next", "session_retire", "session_destroy"} {
		address, err := windows.GetProcAddress(library, "oa_appmodel_"+suffix)
		if err != nil {
			return nil, err
		}
		p.procedures[suffix] = address
	}
	if p.invoke("abi_version") != 2 {
		return nil, errors.New("unsupported Panel native model ABI")
	}
	parsed, err := registrationwire.DecodeManifest(manifest)
	if err != nil {
		return nil, err
	}
	p.application = parsed.Name
	bounds, err := p.output("default_bounds", nil)
	if err != nil {
		return nil, err
	}
	var nativeError panelNativeError
	if status := p.spans("create", nil, []any{&p.model, &nativeError}, []byte(instance), bounds, []byte("[]")); status != 0 {
		return nil, panelNativeStatus("create", status)
	}
	if status := p.spans("session_create", []any{p.model}, []any{&p.session}, []byte(parsed.Name), []byte("0.8.0")); status != 0 {
		return nil, panelNativeStatus("session create", status)
	}
	for index, verb := range parsed.Verbs {
		canonical := registrationwire.EncodeActivationVerb(&verb)
		callback := uint64(index + 1)
		if status := p.spans("session_activation", []any{p.session}, []any{uintptr(callback), &nativeError}, []byte(parsed.Name), canonical); status != 0 {
			return nil, panelNativeStatus("activation", status)
		}
		p.verbs[callback] = verb.Name
	}
	return p, nil
}

func panelNativeStatus(operation string, status uintptr) error {
	return fmt.Errorf("Panel native model %s refused (status %d)", operation, status)
}

func (p *panelWindowsModel) invoke(name string, args ...any) uintptr {
	address := p.procedures[name]
	if address == 0 {
		return 5
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	raw := make([]uintptr, len(args))
	for index, arg := range args {
		switch value := arg.(type) {
		case uintptr:
			raw[index] = value
		case int:
			raw[index] = uintptr(value)
		case *panelNativeSpan:
			pin.Pin(value)
			if value.data != nil {
				pin.Pin(value.data)
			}
			raw[index] = uintptr(unsafe.Pointer(value))
		case *panelNativeBuffer:
			pin.Pin(value)
			if value.data != nil {
				pin.Pin(value.data)
			}
			raw[index] = uintptr(unsafe.Pointer(value))
		case *panelNativeEvent:
			pin.Pin(value)
			raw[index] = uintptr(unsafe.Pointer(value))
		case *panelNativeError:
			pin.Pin(value)
			raw[index] = uintptr(unsafe.Pointer(value))
		case *uintptr:
			pin.Pin(value)
			raw[index] = uintptr(unsafe.Pointer(value))
		case *uint64:
			pin.Pin(value)
			raw[index] = uintptr(unsafe.Pointer(value))
		case *int32:
			pin.Pin(value)
			raw[index] = uintptr(unsafe.Pointer(value))
		default:
			panic("unsupported private native ABI argument")
		}
	}
	status, _, _ := syscall.SyscallN(address, raw...)
	runtime.KeepAlive(args)
	return status
}

func (p *panelWindowsModel) spans(name string, prefix, suffix []any, values ...[]byte) uintptr {
	spans := make([]panelNativeSpan, len(values))
	args := append([]any{}, prefix...)
	for index, value := range values {
		if len(value) != 0 {
			spans[index].data = unsafe.Pointer(&value[0])
		}
		spans[index].size = uintptr(len(value))
		// The native pointer projection borrows each span for this call.
		args = append(args, &spans[index])
	}
	args = append(args, suffix...)
	status := p.invoke(name+"_p", args...)
	runtime.KeepAlive(values)
	runtime.KeepAlive(spans)
	return status
}

func (p *panelWindowsModel) output(name string, prefix []any) ([]byte, error) {
	buffer := panelNativeBuffer{}
	args := append(append([]any{}, prefix...), &buffer)
	status := p.invoke(name, args...)
	if status != 2 && status != 0 {
		return nil, panelNativeStatus(name, status)
	}
	if buffer.size > panelOwnedFrameLimit {
		return nil, errors.New("Panel native output exceeds frame bound")
	}
	if buffer.size == 0 {
		return nil, nil
	}
	data := make([]byte, buffer.size)
	buffer.data, buffer.capacity = unsafe.Pointer(&data[0]), uintptr(len(data))
	if status := p.invoke(name, args...); status != 0 || buffer.size > buffer.capacity {
		return nil, panelNativeStatus(name, status)
	}
	runtime.KeepAlive(data)
	return data[:buffer.size], nil
}

func (p *panelWindowsModel) Receive(body []byte) ([]panelNativeActivation, [][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retired || p.session == 0 {
		return nil, nil, errors.New("Panel mediation closed")
	}
	var responding int32
	ticket := p.invoke("request_id", p.model)
	if ticket == 0 {
		return nil, nil, errors.New("native request ticket unavailable")
	}
	if status := p.spans("session_receive", []any{p.session, ticket}, []any{&responding}, body); status != 0 {
		return nil, nil, panelNativeStatus("receive", status)
	}
	return p.pump()
}

func (p *panelWindowsModel) Complete(id string, body []byte) ([]panelNativeActivation, [][]byte, error) {
	return p.finishCallback(id, body, false)
}

func (p *panelWindowsModel) Fault(id string) ([]panelNativeActivation, [][]byte, error) {
	return p.finishCallback(id, nil, true)
}

func (p *panelWindowsModel) finishCallback(id string, body []byte, fault bool) ([]panelNativeActivation, [][]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	call, err := strconv.ParseUint(id, 16, 64)
	if err != nil || call == 0 || p.model == 0 {
		return nil, nil, errors.New("invalid native callback ticket")
	}
	var nativeError panelNativeError
	if fault || p.spans("complete_activation", []any{p.model, uintptr(call)}, []any{&nativeError}, body) != 0 {
		if status := p.invoke("callback_fault", p.model, uintptr(call), &nativeError); status != 0 {
			return nil, nil, panelNativeStatus("callback fault", status)
		}
	}
	return p.pump()
}

func (p *panelWindowsModel) pump() ([]panelNativeActivation, [][]byte, error) {
	var activations []panelNativeActivation
	var output [][]byte
	for count := 0; count < 2048; count++ {
		var event panelNativeEvent
		var nativeError panelNativeError
		buffer := panelNativeBuffer{}
		args := []any{p.model, &event, &buffer, &nativeError}
		status := p.invoke("next", args...)
		if status == 1 {
			break
		}
		if status != 2 && status != 0 || buffer.size > panelOwnedFrameLimit {
			return nil, nil, panelNativeStatus("event", status)
		}
		data := make([]byte, buffer.size)
		if len(data) != 0 {
			buffer.data = unsafe.Pointer(&data[0])
		}
		buffer.capacity = uintptr(len(data))
		if status == 2 {
			if status = p.invoke("next", args...); status != 0 || buffer.size > buffer.capacity {
				return nil, nil, panelNativeStatus("event copy", status)
			}
		}
		data = data[:buffer.size]
		var consumed int32
		var ticket uint64
		if status := p.spans("session_accept", []any{p.session, &event}, []any{&consumed, &ticket}, data); status != 0 {
			return nil, nil, panelNativeStatus("accept", status)
		}
		if event.kind == 9 {
			verb, exists := p.verbs[event.callback]
			if !exists {
				return nil, nil, errors.New("unregistered native activation callback")
			}
			request, err := applications.ToJSON(&applications.ActivateRequest{Application: p.application, Verb: verb, Payload: applications.Raw(data)})
			if err != nil {
				return nil, nil, err
			}
			activations = append(activations, panelNativeActivation{ID: strconv.FormatUint(event.call, 16), Request: request})
		}
		runtime.KeepAlive(data)
		if count == 2047 {
			return nil, nil, errors.New("Panel native event batch exceeded bound")
		}
	}
	for count := 0; count < 2048; count++ {
		var ticket uint64
		buffer := panelNativeBuffer{}
		args := []any{p.session, &ticket, &buffer}
		status := p.invoke("session_next", args...)
		if status == 1 {
			return activations, output, nil
		}
		if status != 2 && status != 0 || buffer.size > panelOwnedFrameLimit {
			return nil, nil, panelNativeStatus("output", status)
		}
		data := make([]byte, buffer.size)
		if len(data) != 0 {
			buffer.data = unsafe.Pointer(&data[0])
		}
		buffer.capacity = uintptr(len(data))
		if status == 2 {
			if status = p.invoke("session_next", args...); status != 0 || buffer.size > buffer.capacity {
				return nil, nil, panelNativeStatus("output copy", status)
			}
		}
		if len(data) != 0 {
			output = append(output, data[:buffer.size])
		}
		runtime.KeepAlive(data)
	}
	return nil, nil, errors.New("Panel native output batch exceeded bound")
}

func (p *panelWindowsModel) Retire() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retired {
		return nil
	}
	p.retired = true
	if p.session != 0 {
		if status := p.invoke("session_retire", p.session); status != 0 {
			return panelNativeStatus("session close", status)
		}
	}
	if p.model != 0 {
		var e panelNativeError
		if status := p.invoke("close", p.model, &e); status != 0 {
			return panelNativeStatus("close", status)
		}
	}
	return nil
}

func (p *panelWindowsModel) Close() error {
	if err := p.Retire(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.model != 0 && p.invoke("pending", p.model) != 0 {
		return panelNativeStatus("destroy", 7)
	}
	if p.session != 0 {
		if status := p.invoke("session_destroy", p.session); status != 0 {
			return panelNativeStatus("session destroy", status)
		}
		p.session = 0
	}
	if p.model != 0 {
		if status := p.invoke("destroy", p.model); status != 0 {
			return panelNativeStatus("destroy", status)
		}
		p.model = 0
	}
	if p.library != 0 {
		if err := windows.FreeLibrary(p.library); err != nil {
			return err
		}
		p.library = 0
	}
	return nil
}

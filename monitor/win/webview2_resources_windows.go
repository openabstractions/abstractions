//go:build windows

package win

// In-process resources for the WebView2 window: every request the page makes
// under one origin raises ICoreWebView2's WebResourceRequested event, and the
// window answers it from Go. No request under that origin reaches a network
// stack, so the window needs no HTTP listener.
//
// The interfaces below are transcribed in declaration order, the order their
// vtables have: ICoreWebView2WebResourceRequestedEventArgs (get_Request,
// get_Response, put_Response, GetDeferral, get_ResourceContext),
// ICoreWebView2WebResourceRequest (get_Uri, put_Uri, get_Method, put_Method,
// get_Content, put_Content, get_Headers), ICoreWebView2HttpRequestHeaders
// (GetHeader, GetHeaders, Contains, SetHeader, RemoveHeader, GetIterator) and
// ICoreWebView2Deferral (Complete), checked against the webview2-sys
// bindgen output of WebView2.h (docs.rs/webview2-sys), the same cross-check
// webview2_windows.go records for its other event arguments. IStream is
// objidl.h's ISequentialStream Read/Write followed by IStream's own slots.
//
// Threading: WebView2 raises the event on the window's thread. The handler
// copies the request out, takes a deferral and returns; the answer is built
// on its own goroutine, and the response is created and the deferral
// completed back on the window's thread (Window.Do), because every
// ICoreWebView2 object belongs to the thread that created it.

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// ResourceRequest is one request the page made under Resources.Origin.
type ResourceRequest struct {
	Method string
	URL    string      // absolute, under Resources.Origin
	Header http.Header // only the names Resources.Headers lists
	Body   []byte
}

// AbortedResponseText is the whole body of an aborted request's 502.
const AbortedResponseText = "This request stopped before it finished."

// ResourceResponse answers one ResourceRequest. Aborted fails the request:
// the page gets a 502 whose body is AbortedResponseText and no part of the
// route's own body. WebView2 offers no way to
// fail an intercepted request other than leaving it to the network stack,
// which this window must never reach.
type ResourceResponse struct {
	Status  int
	Header  http.Header
	Body    []byte
	Aborted bool
}

// Resources answers the window's requests under Origin in process.
type Resources struct {
	// Origin is scheme://host. The window's start page must be under it.
	Origin string
	// Headers names the request headers Serve reads; others are not copied.
	Headers []string
	// Serve answers one request. It runs on its own goroutine and may block,
	// a long poll included; ctx ends when the window closes.
	Serve func(ctx context.Context, r ResourceRequest) ResourceResponse
}

// maxResourceRequestBody bounds the request body copied out of WebView2.
const maxResourceRequestBody = 16 << 20

// webResourceContextAll is COREWEBVIEW2_WEB_RESOURCE_CONTEXT_ALL.
const webResourceContextAll = 0

type coreWebView2WebResourceRequestedEventArgsVtbl struct {
	iUnknownVtbl
	GetRequest         comProc
	GetResponse        comProc
	PutResponse        comProc
	GetDeferral        comProc
	GetResourceContext comProc
}

type coreWebView2WebResourceRequestedEventArgs struct {
	vtbl *coreWebView2WebResourceRequestedEventArgsVtbl
}

type coreWebView2WebResourceRequestVtbl struct {
	iUnknownVtbl
	GetUri     comProc
	PutUri     comProc
	GetMethod  comProc
	PutMethod  comProc
	GetContent comProc
	PutContent comProc
	GetHeaders comProc
}

type coreWebView2WebResourceRequest struct {
	vtbl *coreWebView2WebResourceRequestVtbl
}

type coreWebView2HttpRequestHeadersVtbl struct {
	iUnknownVtbl
	GetHeader    comProc
	GetHeaders   comProc
	Contains     comProc
	SetHeader    comProc
	RemoveHeader comProc
	GetIterator  comProc
}

type coreWebView2HttpRequestHeaders struct {
	vtbl *coreWebView2HttpRequestHeadersVtbl
}

type coreWebView2DeferralVtbl struct {
	iUnknownVtbl
	Complete comProc
}

type coreWebView2Deferral struct {
	vtbl *coreWebView2DeferralVtbl
}

// iStreamVtbl is IStream: ISequentialStream's Read and Write, then IStream's.
type iStreamVtbl struct {
	iUnknownVtbl
	Read         comProc
	Write        comProc
	Seek         comProc
	SetSize      comProc
	CopyTo       comProc
	Commit       comProc
	Revert       comProc
	LockRegion   comProc
	UnlockRegion comProc
	Stat         comProc
	Clone        comProc
}

type iStream struct {
	vtbl *iStreamVtbl
}

// iUnknown is any COM object, for Release.
type iUnknown struct {
	vtbl *iUnknownVtbl
}

type webResourceRequestedHandler struct{ vtbl *handlerVtbl }

// iidWebResourceRequested is ICoreWebView2WebResourceRequestedEventHandler.
var iidWebResourceRequested = syscall.GUID{0xab00b74c, 0x15f1, 0x4646, [8]byte{0x80, 0xe8, 0xe7, 0x63, 0x41, 0xd2, 0x5d, 0x71}}

var (
	shlwapi           = syscall.NewLazyDLL("shlwapi.dll")
	shCreateMemStream = shlwapi.NewProc("SHCreateMemStream")
)

func release(p unsafe.Pointer) {
	if p != nil {
		(*iUnknown)(p).vtbl.Release.call(uintptr(p))
	}
}

// coString reads an [out] LPWSTR and frees it with CoTaskMemFree.
func coString(p *uint16) string {
	if p == nil {
		return ""
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(p))) //unchecked: CoTaskMemFree returns nothing
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), uintptr(n)*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}

// readStream copies a request body out of WebView2, up to limit bytes.
func readStream(s *iStream, limit int) ([]byte, error) {
	var out []byte
	buf := make([]byte, 64<<10)
	for {
		var n uint32
		hr := s.vtbl.Read.call(uintptr(unsafe.Pointer(s)), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))
		if int32(hr) < 0 {
			return nil, fmt.Errorf("IStream::Read: %#x", uint32(hr))
		}
		out = append(out, buf[:n]...)
		if len(out) > limit {
			return nil, fmt.Errorf("request body exceeds %d bytes", limit)
		}
		if n == 0 || hr == 1 { // S_FALSE: the end of the stream
			return out, nil
		}
	}
}

// copyRequest reads method, URI, the named headers and the body.
func copyRequest(args *coreWebView2WebResourceRequestedEventArgs, names []string) (ResourceRequest, error) {
	var req *coreWebView2WebResourceRequest
	if hr := args.vtbl.GetRequest.call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&req))); int32(hr) < 0 || req == nil {
		return ResourceRequest{}, fmt.Errorf("get_Request: %#x", uint32(hr))
	}
	defer release(unsafe.Pointer(req))
	out := ResourceRequest{Header: http.Header{}}
	var text *uint16
	req.vtbl.GetUri.call(uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&text)))
	out.URL = coString(text)
	text = nil
	req.vtbl.GetMethod.call(uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&text)))
	out.Method = coString(text)
	var headers *coreWebView2HttpRequestHeaders
	if hr := req.vtbl.GetHeaders.call(uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&headers))); int32(hr) >= 0 && headers != nil {
		for _, name := range names {
			wname, err := syscall.UTF16PtrFromString(name)
			if err != nil {
				continue
			}
			var has int32
			headers.vtbl.Contains.call(uintptr(unsafe.Pointer(headers)), uintptr(unsafe.Pointer(wname)), uintptr(unsafe.Pointer(&has)))
			if has == 0 {
				continue
			}
			var value *uint16
			if hr := headers.vtbl.GetHeader.call(uintptr(unsafe.Pointer(headers)), uintptr(unsafe.Pointer(wname)), uintptr(unsafe.Pointer(&value))); int32(hr) >= 0 {
				out.Header.Set(name, coString(value))
			}
		}
		release(unsafe.Pointer(headers))
	}
	var content *iStream
	if hr := req.vtbl.GetContent.call(uintptr(unsafe.Pointer(req)), uintptr(unsafe.Pointer(&content))); int32(hr) >= 0 && content != nil {
		body, err := readStream(content, maxResourceRequestBody)
		release(unsafe.Pointer(content))
		if err != nil {
			return ResourceRequest{}, err
		}
		out.Body = body
	}
	return out, nil
}

// responseHeaders is the CRLF-separated header text
// ICoreWebView2Environment::CreateWebResourceResponse takes.
func responseHeaders(h http.Header) string {
	var lines []string
	for name, values := range h {
		for _, v := range values {
			v = strings.NewReplacer("\r", " ", "\n", " ").Replace(v)
			lines = append(lines, name+": "+v)
		}
	}
	return strings.Join(lines, "\r\n")
}

// newWebResourceRequestedHandler answers requests under w.resources.Origin.
func (w *webView2Window) newWebResourceRequestedHandler() *webResourceRequestedHandler {
	invoke := func(this, sender uintptr, args *coreWebView2WebResourceRequestedEventArgs) uintptr {
		if args == nil {
			return 0
		}
		req, err := copyRequest(args, w.resources.Headers)
		if err != nil {
			fmt.Fprintln(os.Stderr, "WebView2 resource request:", err) //unchecked: stderr is the diagnostic
			w.respond(args, nil, ResourceResponse{Status: http.StatusBadRequest})
			return 0
		}
		if !strings.HasPrefix(req.URL, w.resources.Origin+"/") {
			w.respond(args, nil, ResourceResponse{Status: http.StatusForbidden})
			return 0
		}
		var deferral *coreWebView2Deferral
		if hr := args.vtbl.GetDeferral.call(uintptr(unsafe.Pointer(args)), uintptr(unsafe.Pointer(&deferral))); int32(hr) < 0 || deferral == nil {
			w.respond(args, nil, ResourceResponse{Status: http.StatusInternalServerError})
			return 0
		}
		// The args and the deferral outlive this call.
		args.vtbl.AddRef.call(uintptr(unsafe.Pointer(args)))
		ctx := w.ctx
		go func() {
			resp := w.resources.Serve(ctx, req)
			w.win.Do(func() {
				w.respond(args, deferral, resp)
				args.vtbl.Release.call(uintptr(unsafe.Pointer(args)))
			})
		}()
		return 0
	}
	w.resourceHandler = &webResourceRequestedHandler{vtbl: &handlerVtbl{
		iUnknownVtbl: constUnknown(iidWebResourceRequested),
		Invoke:       comProc(syscall.NewCallback(invoke)),
	}}
	return w.resourceHandler
}

// respond sets resp on args and completes deferral when there is one. It runs
// on the window's thread.
func (w *webView2Window) respond(args *coreWebView2WebResourceRequestedEventArgs, deferral *coreWebView2Deferral, resp ResourceResponse) {
	defer func() {
		if deferral != nil {
			deferral.vtbl.Complete.call(uintptr(unsafe.Pointer(deferral)))
			release(unsafe.Pointer(deferral))
		}
	}()
	if w.env == nil {
		return
	}
	if resp.Aborted {
		resp = ResourceResponse{Status: http.StatusBadGateway, Header: http.Header{
			"Cache-Control": {"no-store"}, "Content-Type": {"text/plain; charset=utf-8"}},
			Body: []byte(AbortedResponseText + "\n")}
	}
	var body uintptr
	if len(resp.Body) > 0 {
		body = uintptr(unsafe.Pointer(&resp.Body[0]))
	}
	stream, _, _ := shCreateMemStream.Call(body, uintptr(len(resp.Body))) //unchecked: a nil stream is checked on the next line
	if stream == 0 {
		return
	}
	defer release(unsafe.Pointer(stream))
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	reason, err1 := syscall.UTF16PtrFromString(http.StatusText(status))
	headers, err2 := syscall.UTF16PtrFromString(responseHeaders(resp.Header))
	if err1 != nil || err2 != nil {
		return
	}
	var response unsafe.Pointer
	if hr := w.env.vtbl.CreateWebResourceResponse.call(uintptr(unsafe.Pointer(w.env)), stream, uintptr(status),
		uintptr(unsafe.Pointer(reason)), uintptr(unsafe.Pointer(headers)), uintptr(unsafe.Pointer(&response))); int32(hr) < 0 || response == nil {
		fmt.Fprintf(os.Stderr, "WebView2 CreateWebResourceResponse: %#x\n", uint32(hr)) //unchecked: stderr is the diagnostic
		return
	}
	args.vtbl.PutResponse.call(uintptr(unsafe.Pointer(args)), uintptr(response))
	release(response)
}

// serveResources registers the filter and the handler. It runs in finish,
// before the first navigation.
func (w *webView2Window) serveResources() error {
	filter, err := syscall.UTF16PtrFromString(w.resources.Origin + "/*")
	if err != nil {
		return err
	}
	if hr := w.webview.vtbl.AddWebResourceRequestedFilter.call(uintptr(unsafe.Pointer(w.webview)), uintptr(unsafe.Pointer(filter)), webResourceContextAll); int32(hr) < 0 {
		return fmt.Errorf("AddWebResourceRequestedFilter: %#x", uint32(hr))
	}
	var token eventRegistrationToken
	if hr := w.webview.vtbl.AddWebResourceRequested.call(uintptr(unsafe.Pointer(w.webview)),
		uintptr(unsafe.Pointer(w.newWebResourceRequestedHandler())), uintptr(unsafe.Pointer(&token))); int32(hr) < 0 {
		return fmt.Errorf("add_WebResourceRequested: %#x", uint32(hr))
	}
	return nil
}

//go:build windows

package win

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
	"unsafe"
)

// Each vtable is called by position. These slots must sit where WebView2.h
// declares them: IUnknown's three, then the interface's own in order.
func TestWebResourceVtableSlots(t *testing.T) {
	ptr := unsafe.Sizeof(uintptr(0))
	for _, c := range []struct {
		name string
		got  uintptr
		slot uintptr
	}{
		{"args.get_Request", unsafe.Offsetof(coreWebView2WebResourceRequestedEventArgsVtbl{}.GetRequest), 3},
		{"args.put_Response", unsafe.Offsetof(coreWebView2WebResourceRequestedEventArgsVtbl{}.PutResponse), 5},
		{"args.GetDeferral", unsafe.Offsetof(coreWebView2WebResourceRequestedEventArgsVtbl{}.GetDeferral), 6},
		{"request.get_Uri", unsafe.Offsetof(coreWebView2WebResourceRequestVtbl{}.GetUri), 3},
		{"request.get_Method", unsafe.Offsetof(coreWebView2WebResourceRequestVtbl{}.GetMethod), 5},
		{"request.get_Content", unsafe.Offsetof(coreWebView2WebResourceRequestVtbl{}.GetContent), 7},
		{"request.get_Headers", unsafe.Offsetof(coreWebView2WebResourceRequestVtbl{}.GetHeaders), 9},
		{"headers.GetHeader", unsafe.Offsetof(coreWebView2HttpRequestHeadersVtbl{}.GetHeader), 3},
		{"headers.Contains", unsafe.Offsetof(coreWebView2HttpRequestHeadersVtbl{}.Contains), 5},
		{"deferral.Complete", unsafe.Offsetof(coreWebView2DeferralVtbl{}.Complete), 3},
		{"stream.Read", unsafe.Offsetof(iStreamVtbl{}.Read), 3},
		{"environment.CreateWebResourceResponse", unsafe.Offsetof(coreWebView2EnvironmentVtbl{}.CreateWebResourceResponse), 4},
		{"webview.add_WebResourceRequested", unsafe.Offsetof(coreWebView2Vtbl{}.AddWebResourceRequested), 55},
		{"webview.AddWebResourceRequestedFilter", unsafe.Offsetof(coreWebView2Vtbl{}.AddWebResourceRequestedFilter), 57},
	} {
		if c.got != c.slot*ptr {
			t.Errorf("%s at slot %d, want %d", c.name, c.got/ptr, c.slot)
		}
	}
}

// readStream copies a whole stream and refuses one over its limit; the
// stream here is the same SHCreateMemStream the responses use.
func TestReadStream(t *testing.T) {
	body := bytes.Repeat([]byte("panel"), 30000) // 150 000 bytes: three reads
	stream, _, _ := shCreateMemStream.Call(uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)))
	if stream == 0 {
		t.Fatal("SHCreateMemStream")
	}
	got, err := readStream((*iStream)(unsafe.Pointer(stream)), len(body))
	release(unsafe.Pointer(stream))
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read %d bytes, err %v", len(got), err)
	}
	stream, _, _ = shCreateMemStream.Call(uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)))
	_, err = readStream((*iStream)(unsafe.Pointer(stream)), len(body)-1)
	release(unsafe.Pointer(stream))
	if err == nil {
		t.Fatal("a stream over the limit was read")
	}
}

// Response headers reach WebView2 as CRLF-separated lines, and a value cannot
// add a line of its own.
func TestResponseHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Note", "a\r\nSet-Cookie: x=1")
	text := responseHeaders(h)
	lines := strings.Split(text, "\r\n")
	if len(lines) != 2 {
		t.Fatalf("%d lines: %q", len(lines), text)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "Set-Cookie") {
			t.Fatalf("injected header line: %q", text)
		}
	}
}

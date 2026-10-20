package main

// The Panel's resource mode: a native window hands each request its page
// makes to this process, which runs it through the same routes the HTTP mode
// serves, with no listener in between (research/release-0.4.0/planned/
// resource-only-panel-loading.md). The window's platform code only marshals:
// WebView2's WebResourceRequested on Windows (desktop_windows.go) and the macOS
// launcher's scheme handler over its owned pipes (native_asks_darwin.go). This
// file owns the translation both share.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"html"
	"log"
	"net/http"
	neturl "net/url"
)

// The virtual origins of the native windows. Neither names a host that can
// resolve: ".invalid" is reserved (RFC 6761), and the macOS custom scheme
// never reaches a network stack.
const (
	panelResourceOriginWindows = "https://panel.invalid"
	panelResourceOriginMacOS   = "oa-panel://panel"
)

// panelResourceHeaders are the request headers the Panel's routes read; the
// platform code copies these and no others.
var panelResourceHeaders = []string{"X-Panel-Key", "Content-Type", "Origin"}

// panelResourceRequest is one request from the window, platform-neutral.
type panelResourceRequest struct {
	Method string
	URL    string // absolute, under the window's origin
	Header http.Header
	Body   []byte
}

// panelResourceResponse is its answer. Aborted means the route aborted the
// request (http.ErrAbortHandler): the platform fails the request and sends
// no status or body.
type panelResourceResponse struct {
	Status  int
	Header  http.Header
	Body    []byte
	Aborted bool
}

// maxPanelResourceResponse bounds one buffered answer (decided 2026-10-07).
// Streaming responses are a TODO after 0.4.0.
const maxPanelResourceResponse = 64 << 20

// panelResourceTooLarge is the one sentence a person sees for an answer over
// the cap.
const panelResourceTooLarge = "This result is too large to open in the Panel; save it with openabstractions jobs result <id>."

// errPanelResourceTooLarge stops a route writing past the cap.
var errPanelResourceTooLarge = errors.New("Panel response exceeds 64 MiB")

// panelResources serves one window's requests through routes.
type panelResources struct {
	origin string
	next   http.Handler
}

func newPanelResources(origin string, routes http.Handler) *panelResources {
	return &panelResources{origin: origin, next: panelResourceBoundary(origin, routes)}
}

// serve runs one request. ctx ends when the window closes or the platform
// reports the request abandoned; a long poll returns then. A route that
// panics never takes the Panel down: http.ErrAbortHandler reports the request
// aborted, any other panic answers 500 and logs one line.
func (p *panelResources) serve(ctx context.Context, in panelResourceRequest) (out panelResourceResponse) {
	method := in.Method
	if method == "" {
		method = http.MethodGet
	}
	r, err := http.NewRequestWithContext(ctx, method, in.URL, bytes.NewReader(in.Body))
	if err != nil {
		return panelResourceText(http.StatusBadRequest, "bad request")
	}
	for _, name := range panelResourceHeaders {
		if v := in.Header.Get(name); v != "" {
			r.Header.Set(name, v)
		}
	}
	rec := &panelResourceRecorder{header: http.Header{}}
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		// A route that stops after its answer outgrew the cap (a result
		// download aborting on the refused write) gets the cap's sentence.
		if rec.overflow {
			out = panelResourceText(http.StatusInternalServerError, panelResourceTooLarge)
			return
		}
		if recovered == http.ErrAbortHandler {
			out = panelResourceResponse{Aborted: true}
			return
		}
		log.Printf("Panel resource %s %s: panic: %v", method, r.URL.Path, recovered)
		out = panelResourceText(http.StatusInternalServerError, "The Panel could not answer this request.")
	}()
	p.next.ServeHTTP(rec, r)
	if rec.overflow {
		return panelResourceText(http.StatusInternalServerError, panelResourceTooLarge)
	}
	out = panelResourceResponse{Status: rec.status, Header: rec.header, Body: rec.body.Bytes()}
	if out.Status == 0 {
		out.Status = http.StatusOK
	}
	if out.Status >= 300 && out.Status < 400 {
		return p.redirect(r.URL, out)
	}
	return out
}

// redirect turns a same-origin redirect into a document that replaces itself
// with the target. The macOS scheme handler cannot redirect (measured
// 2026-10-07: neither fetch nor navigation follows a 302 from a
// WKURLSchemeHandler), and one rule keeps both windows alike. Routes redirect
// only navigations (redirectAlias, apiOrPage). A redirect leaving the origin
// is refused.
func (p *panelResources) redirect(from *neturl.URL, out panelResourceResponse) panelResourceResponse {
	location := out.Header.Get("Location")
	target, err := from.Parse(location)
	if location == "" || err != nil || !p.sameOrigin(target) {
		return panelResourceText(http.StatusForbidden, "redirect outside the Panel refused")
	}
	quoted, err := json.Marshal(target.String())
	if err != nil {
		return panelResourceText(http.StatusForbidden, "redirect outside the Panel refused")
	}
	// json.Marshal escapes <, > and & so the target cannot close the script.
	body := "<!doctype html><meta charset=utf-8><title>" + html.EscapeString(target.Path) +
		"</title><script>location.replace(" + string(quoted) + ")</script>"
	h := http.Header{}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	return panelResourceResponse{Status: http.StatusOK, Header: h, Body: []byte(body)}
}

func (p *panelResources) sameOrigin(u *neturl.URL) bool {
	return u != nil && u.User == nil && u.Scheme+"://"+u.Host == p.origin
}

// panelResourceBoundary admits requests for the window's own origin only, the
// counterpart of serviceBrowserBoundary's loopback Host and same-Origin rules.
// The per-run key stays required at each capability handler.
func panelResourceBoundary(origin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.User != nil || r.URL.Scheme+"://"+r.URL.Host != origin {
			http.Error(w, "Panel origin required", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			http.Error(w, "same Origin required", http.StatusForbidden)
			return
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func panelResourceText(status int, text string) panelResourceResponse {
	h := http.Header{}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	return panelResourceResponse{Status: status, Header: h, Body: []byte(text + "\n")}
}

// panelResourceRecorder is the http.ResponseWriter a resource request writes
// into. The routes buffer whole responses; none streams. It holds at most
// maxPanelResourceResponse bytes and refuses every write past that.
type panelResourceRecorder struct {
	header   http.Header
	status   int
	body     bytes.Buffer
	overflow bool
}

func (r *panelResourceRecorder) Header() http.Header { return r.header }

func (r *panelResourceRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *panelResourceRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.overflow || r.body.Len()+len(b) > maxPanelResourceResponse {
		r.overflow = true
		r.body = bytes.Buffer{} // release what was held
		return 0, errPanelResourceTooLarge
	}
	return r.body.Write(b)
}

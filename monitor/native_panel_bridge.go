package main

// The macOS Panel window's requests travel over the launcher's owned pipes
// (native_asks_bridge.go): its WKURLSchemeHandler for oa-panel:// sends one
// "panel-request" line per request, this file answers through the shared
// resource adapter (panel_resource.go) with "panel-response" lines, and a
// "panel-cancel" line ends a request the page abandoned. Nothing listens.

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"sync"
)

// nativePanelChunk bounds one response line's body: base64 of 512 KiB stays
// well inside the channel's 1 MiB line.
const nativePanelChunk = 512 << 10

// maxNativePanelRequests bounds requests in flight from one window.
const maxNativePanelRequests = 64

type nativePanelServer struct {
	ctx       context.Context
	resources *panelResources
	write     func(nativeAsksFrame) error

	mu       sync.Mutex
	inflight map[string]context.CancelFunc
}

func newNativePanelServer(ctx context.Context, resources *panelResources, write func(nativeAsksFrame) error) *nativePanelServer {
	return &nativePanelServer{ctx: ctx, resources: resources, write: write, inflight: map[string]context.CancelFunc{}}
}

// validNativePanelRequest checks one request line's shape. The launcher is
// this child's owner; the check keeps a malformed line from reaching a route.
func validNativePanelRequest(r nativeAsksReply) error {
	if r.ID == "" || len(r.ID) > 64 || r.URL == "" || len(r.URL) > 8192 ||
		r.Option != "" || r.Error != "" || r.Unlocked != nil || r.Page != "" {
		return errors.New("invalid native Panel request")
	}
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead:
	default:
		return errors.New("invalid native Panel request method")
	}
	for name := range r.Headers {
		if !nativePanelHeader(name) {
			return errors.New("invalid native Panel request header")
		}
	}
	return nil
}

func nativePanelHeader(name string) bool {
	for _, allowed := range panelResourceHeaders {
		if http.CanonicalHeaderKey(name) == http.CanonicalHeaderKey(allowed) {
			return true
		}
	}
	return false
}

// request starts one request and returns at once; its answer follows on the
// channel. A request the server cannot take is answered with a refusal.
func (s *nativePanelServer) request(r nativeAsksReply) {
	body, err := base64.StdEncoding.DecodeString(r.Body)
	if err != nil {
		s.respond(r.ID, panelResourceText(http.StatusBadRequest, "bad request body"))
		return
	}
	header := http.Header{}
	for name, value := range r.Headers {
		header.Set(name, value)
	}
	s.mu.Lock()
	if _, busy := s.inflight[r.ID]; busy || len(s.inflight) >= maxNativePanelRequests {
		s.mu.Unlock()
		s.respond(r.ID, panelResourceText(http.StatusServiceUnavailable, "too many Panel requests"))
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.inflight[r.ID] = cancel
	s.mu.Unlock()
	go func() {
		out := s.resources.serve(ctx, panelResourceRequest{Method: r.Method, URL: r.URL, Header: header, Body: body})
		s.mu.Lock()
		_, live := s.inflight[r.ID]
		delete(s.inflight, r.ID)
		s.mu.Unlock()
		cancel()
		if live {
			s.respond(r.ID, out)
		}
	}()
}

// cancel ends a request the page abandoned; its answer is not sent.
func (s *nativePanelServer) cancel(id string) {
	s.mu.Lock()
	stop := s.inflight[id]
	delete(s.inflight, id)
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// respond writes the answer in chunks: the first carries status and headers,
// the last has More false.
func (s *nativePanelServer) respond(id string, out panelResourceResponse) {
	if out.Aborted {
		_ = s.write(nativeAsksFrame{Kind: "panel-response", ID: id, Aborted: true})
		return
	}
	headers := map[string]string{}
	for name, values := range out.Header {
		headers[name] = strings.Join(values, ", ")
	}
	body := out.Body
	for seq := 0; ; seq++ {
		n := min(len(body), nativePanelChunk)
		frame := nativeAsksFrame{Kind: "panel-response", ID: id, Seq: seq, Body: base64.StdEncoding.EncodeToString(body[:n]), More: n < len(body)}
		if seq == 0 {
			frame.Status, frame.Headers = out.Status, headers
		}
		if err := s.write(frame); err != nil {
			return
		}
		body = body[n:]
		if !frame.More {
			return
		}
	}
}

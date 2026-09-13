package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// A panel process retains its selected binding. Failed calls never select a
// different owner; starting a new process requires matching saved recovery data.
type servicePanel struct {
	mu        sync.Mutex
	jobs      *client.JobsClient
	inventory *client.InventoryClient
	history   api.HistoryWindow
}

func (p *servicePanel) binding(ctx context.Context) (*client.JobsClient, *client.InventoryClient, api.HistoryWindow, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobs != nil {
		return p.jobs, p.inventory, p.history, nil
	}
	inventory, err := facade.Discover().ResolveJobInventory(ctx, facade.Requirements{Scope: "local"})
	if err != nil {
		return nil, nil, api.HistoryWindow{}, err
	}
	jobs, err := facade.Discover().ResolveJobs(ctx, facade.Requirements{Scope: "local"})
	if err != nil {
		return nil, nil, api.HistoryWindow{}, err
	}
	history, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		return nil, nil, api.HistoryWindow{}, err
	}
	p.jobs, p.inventory, p.history = jobs, inventory, history
	return jobs, inventory, history, nil
}
func panelCall(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}
func panelJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func panelError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusServiceUnavailable)
}
func (p *servicePanel) serveBinding(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	jobs, _, h, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, struct {
		Endpoint string            `json:"endpoint"`
		History  api.HistoryWindow `json:"history"`
	}{jobs.Endpoint(), h})
}
func (p *servicePanel) serveInventory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	_, inventory, _, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := inventory.ListWork(ctx, r.URL.Query().Get("cursor"), 32)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, page)
}

type serviceAction struct {
	Action   string              `json:"action"`
	Endpoint string              `json:"endpoint"`
	Owner    string              `json:"owner"`
	Identity api.RequestIdentity `json:"identity"`
	URL      string              `json:"url"`
	Digest   string              `json:"digest"`
	Size     int64               `json:"size"`
}

func (p *servicePanel) act(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	var action serviceAction
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&action); err != nil {
		http.Error(w, "invalid action", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one action required", 400)
		return
	}
	if !slices.Contains([]string{"submit", "reconcile", "cancel", "observe"}, action.Action) {
		http.Error(w, "This legacy action is unavailable in service mode. --legacy-local explicitly selects the old provider controls.", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	jobs, _, h, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if action.Endpoint != jobs.Endpoint() || action.Owner != h.LogicalOwner || action.Identity.HistoryEpoch != h.HistoryEpoch {
		http.Error(w, "saved operation belongs to another binding; do not resubmit to a new owner", 409)
		return
	}
	var result any
	switch action.Action {
	case "submit":
		spec := request.Encode(&request.Request{Artifact: request.Artifact{Digest: action.Digest, Size: action.Size}, Sources: []request.Source{{Scheme: "http", Locator: action.URL}}})
		result, err = jobs.Submit(ctx, api.Submission{Identity: action.Identity, Kind: "download", Spec: spec})
	case "reconcile":
		result, err = jobs.Reconcile(ctx, action.Identity)
	case "cancel":
		result, err = jobs.CancelWork(ctx, action.Identity)
	case "observe":
		result, err = jobs.ObserveWork(ctx, action.Identity)
	}
	if err != nil {
		panelError(w, fmt.Errorf("operation outcome may be unresolved; retain this key and use Reconcile: %w", err))
		return
	}
	panelJSON(w, result)
}
func (p *servicePanel) result(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	jobs, _, h, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	q := r.URL.Query()
	id := api.RequestIdentity{Key: q.Get("key"), HistoryEpoch: q.Get("epoch")}
	if q.Get("owner") != h.LogicalOwner || q.Get("endpoint") != jobs.Endpoint() || id.HistoryEpoch != h.HistoryEpoch {
		http.Error(w, "saved operation binding differs", 409)
		return
	}
	first, err := jobs.ReadResult(ctx, id, 0, 65536)
	if err != nil {
		panelError(w, err)
		return
	}
	if first.Outcome != "data" || first.Chunk == nil {
		http.Error(w, "result: "+first.Outcome, 409)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="operation-result.bin"`)
	w.Header().Set("Content-Length", strconv.FormatInt(first.Chunk.Total, 10))
	if _, err = jobs.CopyResult(ctx, id, w); err != nil {
		panic(http.ErrAbortHandler)
	}
}
func (p *servicePanel) settings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	editor, err := facade.Discover().ResolveConfigEditor(ctx, facade.Requirements{Scope: "local"})
	if err != nil {
		panelError(w, err)
		return
	}
	if r.Method == "GET" {
		value, err := editor.ReadUserContext(ctx)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, value)
		return
	}
	if r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var value configwire.UserSnapshot
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&value); err != nil {
		http.Error(w, "invalid settings", 400)
		return
	}
	result, err := editor.ReplaceUserContext(ctx, value.Revision, value.Values)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
func (p *servicePanel) handler(key string) http.Handler {
	guard := &window{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, servicePage)
	})
	mux.HandleFunc("/binding", guard.guard(p.serveBinding))
	mux.HandleFunc("/inventory", guard.guard(p.serveInventory))
	mux.HandleFunc("/action", guard.guard(p.act))
	mux.HandleFunc("/result", guard.guard(p.result))
	mux.HandleFunc("/settings", guard.guard(p.settings))
	mux.HandleFunc("/runtime", guard.guard(guard.serveReadiness))
	return serviceBrowserBoundary(mux)
}
func runServicePanel(address string, open, native bool) error {
	hostname, _, err := net.SplitHostPort(address)
	if err != nil || !loopback(hostname) {
		return errors.New("service panel requires a loopback address")
	}
	p := &servicePanel{}
	key := mint()
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: p.handler(key), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	url := "http://" + listener.Addr().String() + "/?k=" + key
	fmt.Println("service control panel:", url)
	if native {
		go server.Serve(listener)
		return p.desktop(url)
	}
	if open {
		launch(url)
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// The browser UI accepts its loopback host and same-origin requests only.
// The existing per-run bearer key remains required at each capability handler.
func serviceBrowserBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			hostname = r.Host
		}
		if hostname != "localhost" && !loopback(hostname) {
			http.Error(w, "loopback Host required", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := url.Parse(origin)
			if err != nil || parsed.Scheme != "http" || !strings.EqualFold(parsed.Host, r.Host) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				http.Error(w, "same Origin required", 403)
				return
			}
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

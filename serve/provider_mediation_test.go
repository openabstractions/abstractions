package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestProviderMediationClosesAfterLastRetainedOperationExpires(t *testing.T) {
	if !statusTransportProvesProgram(t) || runtime.GOOS == "darwin" {
		t.Skip("mediated Program proof unavailable")
	}
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags", "/api/ps":
			fmt.Fprint(w, `{"models":[{"name":"fixture-model"}]}`)
		case "/v1/chat/completions":
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	route := router.Ollama(upstream.URL)
	route.Name, route.BindingID = "declared", "retained-generation"
	provider, err := inference.New(inference.Config{Router: router.New(route), Retention: time.Second,
		Now:    func() time.Time { return time.Unix(0, clock.Load()) },
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	endpoint := shortMediationEndpoint(t)
	host, err := inferenceservice.ListenMediated(endpoint, provider, inference.ExecutionLocal, inference.ExecutionBinding{Host: route.Name, BindingID: route.BindingID})
	if err != nil {
		t.Fatal(err)
	}
	providers := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}, changes: make(chan struct{})}
	m := &providerMediation{provider: provider, providers: providers, routes: map[string]*providerMediationRoute{route.BindingID: {endpoint: endpoint, host: host}}}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	client := iwire.NewChatClient(listen.FrameClient{Endpoint: endpoint, Timeout: 3 * time.Second, MaxFrame: 1 << 20})
	request := iwire.Request{Model: "fixture-model", Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "retain"}}}}}
	admission, err := client.Start(request)
	if err != nil || admission.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("Start %+v, %v", admission, err)
	}
	var page iwire.DeltaPage
	for !page.AtEnd {
		page, err = client.Observe(admission.Operation, page.Next, 256, 65536, 1000)
		if err != nil || page.Outcome != iwire.PageOutcomePage {
			t.Fatalf("Observe %+v, %v", page, err)
		}
	}
	if err := m.sync(); err != nil {
		t.Fatal(err)
	}
	if !provider.HasBindingOperations(route.BindingID) {
		t.Fatal("ended operation was not retained")
	}
	stale, err := client.Start(request)
	if err != nil || stale.Outcome != iwire.StartOutcomeNoHost {
		t.Fatalf("retained stale Start %+v, %v", stale, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("stale Start dispatched: %d calls", calls.Load())
	}
	clock.Add(int64(2 * time.Second))
	eventually(t, 3*time.Second, "last retained operation expiring", func() bool { return !provider.HasBindingOperations(route.BindingID) })
	eventually(t, 3*time.Second, "expired retired listener closing", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.routes[route.BindingID] == nil
	})
	if _, err := client.Start(request); err == nil {
		t.Fatal("expired listener still accepted a call")
	}
	if calls.Load() != 1 {
		t.Fatalf("expired stale Start dispatched: %d calls", calls.Load())
	}
}

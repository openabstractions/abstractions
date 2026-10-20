package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// lemonadeAttachedFixture is the smallest native API surface Lemonade needs
// for discovery, residency and its unload-all request. The instrument row is
// cleared only when the fake native unload endpoint runs.
type lemonadeAttachedFixture struct {
	loaded  atomic.Bool
	unloads atomic.Int32
	in      *askerFixture
	program string
}

func (f *lemonadeAttachedFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"Qwen3-0.6B-GGUF","downloaded":true}]}`))
	})
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		body := map[string]any{"all_models_loaded": []any{}}
		if f.loaded.Load() {
			body["model_loaded"] = "Qwen3-0.6B-GGUF"
			body["all_models_loaded"] = []map[string]any{{"model_name": "Qwen3-0.6B-GGUF", "loaded": true}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/api/v1/unload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) != 0 {
			http.Error(w, "expected Lemonade's empty-body unload-all request", http.StatusBadRequest)
			return
		}
		f.unloads.Add(1)
		f.loaded.Store(false)
		if f.in != nil && f.program != "" {
			f.in.occupy(f.program, 0)
		}
		w.WriteHeader(http.StatusOK)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// The runtime's attached-host inventory must include Lemonade so a lease
// request routes through its native unload-all adapter and waits for the
// instrument to confirm that its process bytes left.
func TestFixtureLemonadeAttachedHolderIsYieldedByComposedResourceBook(t *testing.T) {
	program := `C:\AI_Bundle\Lemonade\llama-server.exe`
	in := newAskerFixture(200 * askersGiB)
	in.occupy(program, 40*askersGiB)
	table := resourceservice.NewTable(in, nil, time.Nanosecond)
	lemonade := &lemonadeAttachedFixture{in: in, program: program}
	lemonade.loaded.Store(true)
	server := lemonade.server(t)
	r := router.New(router.Lemonade(server.URL))
	r.SetResidency(tableResidency{table: table})
	book, err := resourceservice.OpenBook(resourceservice.BookOptions{
		Table: table, Attached: attachedHosts(r), Settle: time.Second,
		Yield: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	result := book.Acquire(context.Background(), askerSubject("another-engine"), nil,
		instrument.Card0, 180*askersGiB, 2000)
	if result.Outcome != rwire.AcquireOutcomeAcquired || result.Lease == nil {
		t.Fatalf("resource admission returned %+v", result)
	}
	if lemonade.unloads.Load() != 1 {
		t.Fatalf("Lemonade unload-all called %d times, want one", lemonade.unloads.Load())
	}
	if len(result.Asked) != 1 || result.Asked[0].Holder != "host:lemonade" ||
		result.Asked[0].Answer != resourceservice.AnswerYielded || result.Asked[0].Amount != 40*askersGiB {
		t.Fatalf("resource book did not record the measured Lemonade yield: %+v", result.Asked)
	}
	if lemonade.loaded.Load() {
		t.Fatal("Lemonade still reports its model loaded after native unload")
	}
	leases := book.Leases()
	if len(leases) != 1 || leases[0].ID != result.Lease.ID {
		t.Fatalf("book holds %+v after Lemonade yield, want only the asker lease", leases)
	}
}

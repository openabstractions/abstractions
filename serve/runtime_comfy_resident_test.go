package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	router "github.com/openabstractions/abstraction-router/go"
)

// comfyResidentFixture is a fake stock ComfyUI: enough of its API for
// router.ComfyUI's installed() to succeed (Up: true) and for its resident()
// to read a VRAM figure from /system_stats the way the real server.py route
// does.
type comfyResidentFixture struct {
	heldBytes int64 // atomic: what /system_stats reports as vram_total-vram_free
}

func (f *comfyResidentFixture) setHeld(n int64) { atomic.StoreInt64(&f.heldBytes, n) }

func (f *comfyResidentFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/system_stats", func(w http.ResponseWriter, r *http.Request) {
		held := atomic.LoadInt64(&f.heldBytes)
		// One device, vram_total fixed well above whatever held names so
		// vram_total-vram_free always comes out to exactly held, the same
		// shape server.py's system_stats route reports (one entry per torch
		// device, vram_total and vram_free in bytes).
		const vramTotal = 200 * askersGiB
		stats := map[string]any{
			"devices": []map[string]any{{
				"name": "fixture", "type": "cuda", "index": 0,
				"vram_total": vramTotal, "vram_free": vramTotal - held,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// TestFixtureComfyUIResidentReadsSystemStats: a stock ComfyUI host that is
// up reads vram_total-vram_free from /system_stats and reports one resident
// entry once that sum crosses instrument.Threshold, and none below it. Its
// one entry carries the amount, because ComfyUI names no checkpoint anywhere
// in its stock API. The entry is a claim on the table; OA measures a stock
// ComfyUI and never drives it.
func TestFixtureComfyUIResidentReadsSystemStats(t *testing.T) {
	comfy := &comfyResidentFixture{}
	server := comfy.server(t)
	r := router.NewWithHTTPClient(server.Client(), router.ComfyUI(server.URL))

	t.Run("above the floor", func(t *testing.T) {
		comfy.setHeld(40 * askersGiB)
		states, _, _, _, _ := r.Residency(true)
		state, found := comfyState(states)
		if !found {
			t.Fatal("the router reports no comfyui host state")
		}
		if !state.Up {
			t.Fatalf("the fixture ComfyUI reads down: %+v", state)
		}
		if len(state.Resident) != 1 {
			t.Fatalf("resident %v, want exactly one entry once the reading crosses the floor", state.Resident)
		}
	})

	t.Run("below the floor", func(t *testing.T) {
		comfy.setHeld(100 << 20) // 100 MiB, under the 256 MiB floor
		states, _, _, _, _ := r.Residency(true)
		state, found := comfyState(states)
		if !found {
			t.Fatal("the router reports no comfyui host state")
		}
		if !state.Up {
			t.Fatalf("the fixture ComfyUI reads down: %+v", state)
		}
		if state.Resident != nil {
			t.Fatalf("resident %v, want nil below the floor", state.Resident)
		}
	})
}

func comfyState(states []router.HostState) (router.HostState, bool) {
	for _, state := range states {
		if state.Host == "comfyui" {
			return state, true
		}
	}
	return router.HostState{}, false
}

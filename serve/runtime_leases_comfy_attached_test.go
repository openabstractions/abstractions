package main

import (
	"context"
	"encoding/json"
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

// ComfyUI as an attached holder asked to yield (TODO.md resource §6
// remainder; research/resources/COMFY-CARD-2026-09-22.md "What this does not
// prove": "host:comfyui has an unload mechanism in the router and was not
// declared here, so nothing asked this ComfyUI to yield to anyone.").
//
// This is the fixture-level proof composeResources's own pieces compose to:
// a fake ComfyUI server standing in for the real one, wired through
// router.ComfyUI and attachedHosts exactly as an isolated runtime wires them,
// with a fixture instrument reporting ComfyUI's Python as a verified holder
// of card:0 the way the live measurement did (COMFY-CARD-2026-09-22.md
// section 1's 34.62 GiB verified row). No GPU, no ComfyUI install, no render.
//
// router.ComfyUI's own resident func (abstraction-router/go/hosts.go) now
// reads GET /system_stats and sums vram_total-vram_free over every device it
// reports, so attachedHost.Holding (serve/runtime_resources.go) carries a
// real answer for it once that sum crosses instrument.Threshold, and
// Book.order (abstraction-resource/go/service/leases.go) no longer drops it
// before Yield is ever called. The dated section
// research/resources/COMFY-CARD-2026-09-22.md adds records the same finding
// against a live run.

// comfyAttachedFixture is a fake ComfyUI: enough of its stock API for
// router.ComfyUI's installed() to succeed (Up: true), for its resident() to
// read a VRAM figure from /system_stats the way the real server.py route
// does, and for unloadComfyUI (abstraction-router/go/unload.go) to have
// somewhere to POST.
type comfyAttachedFixture struct {
	freed     int32
	heldBytes int64 // atomic: what /system_stats reports as vram_total-vram_free
	in        *askerFixture
	program   string
}

func (f *comfyAttachedFixture) setHeld(n int64) { atomic.StoreInt64(&f.heldBytes, n) }

func (f *comfyAttachedFixture) server(t *testing.T) *httptest.Server {
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
	mux.HandleFunc("/api/free", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		atomic.AddInt32(&f.freed, 1)
		if f.in != nil && f.program != "" {
			// The real /api/free frees the weights behind the hold; the
			// fixture instrument's row for ComfyUI's Python drops the same
			// way (askerFixture.occupy's own doc: "Zero drops the row, the
			// way a yield does"), so Book.askAttached's settled() sees the
			// bytes leave.
			f.in.occupy(f.program, 0)
		}
		w.WriteHeader(http.StatusOK)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (f *comfyAttachedFixture) called() bool { return atomic.LoadInt32(&f.freed) > 0 }

// comfyAttachedTable wires a fixture instrument reporting comfyPython as a
// verified row of card:0 through the same tableResidency a real runtime uses,
// so the router's own residency read comes from the table the way
// composeResources builds it (serve/runtime_inference.go:
// r.SetResidency(tableResidency{table: resources.table})).
func comfyAttachedTable(in *askerFixture, comfyPython string, held int64) *resourceservice.Table {
	in.occupy(comfyPython, held)
	return resourceservice.NewTable(in, nil, time.Nanosecond)
}

// TestFixtureComfyUIAttachedHolderIsAskedWhenOverTheFloor is the finding
// reversed: with a real router.ComfyUI host up, its /system_stats reading
// over instrument.Threshold, and permitted to yield, Acquire now offers it to
// Book.order, calls the fake server's /api/free, and grants the asker once
// the table shows the bytes gone.
func TestFixtureComfyUIAttachedHolderIsAskedWhenOverTheFloor(t *testing.T) {
	comfyPython := `C:\AI_Bundle\ComfyUI\venv\Scripts\python.exe`
	in := newAskerFixture(200 * askersGiB)
	table := comfyAttachedTable(in, comfyPython, 40*askersGiB)

	comfy := &comfyAttachedFixture{in: in, program: comfyPython}
	comfy.setHeld(40 * askersGiB) // over instrument.Threshold (256 MiB)
	server := comfy.server(t)
	r := router.New(router.ComfyUI(server.URL))
	r.SetResidency(tableResidency{table: table})

	// A permit for every attached holder this release has a mechanism for,
	// matching what installation writes (TestInstallationWritesTheLeaseRules):
	// this Yield policy is deliberately permissive, so a grant below can only
	// be the fixture's own /system_stats reading crossing the floor, never a
	// rights decision.
	book, err := resourceservice.OpenBook(resourceservice.BookOptions{
		Table:    table,
		Attached: attachedHosts(r),
		Settle:   time.Second,
		Yield:    func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	asker := askerSubject("someone-else")
	// Pool is capacity - held = 200 - 40 = 160 free; ask for more than that,
	// small enough that ComfyUI's 40 GiB alone covers the gap.
	result := book.Acquire(context.Background(), asker, nil, instrument.Card0, 180*askersGiB, 2000)

	if !comfy.called() {
		t.Fatal("the fixture ComfyUI's /api/free was never called: router.ComfyUI's resident() is not reaching Book.order")
	}
	if result.Outcome != rwire.AcquireOutcomeAcquired {
		t.Fatalf("outcome %s, want acquired once ComfyUI yielded: %+v", result.Outcome, result.Asked)
	}
	if len(result.Asked) != 1 {
		t.Fatalf("asked %+v, want exactly host:comfyui asked once", result.Asked)
	}
	holder := attachedHost{name: "comfyui"}.Holder()
	if result.Asked[0].Holder != holder {
		t.Fatalf("asked %q, want %q", result.Asked[0].Holder, holder)
	}
	if result.Asked[0].Answer != resourceservice.AnswerYielded {
		t.Fatalf("comfyui answered %q, want yielded", result.Asked[0].Answer)
	}
	if result.Asked[0].Amount != 40*askersGiB {
		t.Fatalf("the ask recorded %d bytes freed, not the %d the instrument showed leaving", result.Asked[0].Amount, 40*askersGiB)
	}

	// The table's verified row for ComfyUI is gone: the fixture's /api/free
	// dropped it the way the real unload does.
	state, err := table.Holders(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range state.Holders {
		if row.Program == comfyPython {
			t.Fatalf("ComfyUI's row is still on the table after it yielded: %+v", row)
		}
	}
}

// TestFixtureComfyUIResidentReadsSystemStats pins the one fact the finding
// above rests on, directly against the router's own host, independent of the
// lease book: a ComfyUI host that is up reads vram_total-vram_free from
// /system_stats and reports one resident entry once that sum crosses
// instrument.Threshold, and none below it. This is different from every
// other attached engine this release has a mechanism for only in what it
// names (TestAttachedHostsAreTheEnginesWithAMechanism's lmstudio and ollama
// both read their own resident model list by name; ComfyUI names no
// checkpoint anywhere in its stock API, so its one entry carries the amount
// instead).
func TestFixtureComfyUIResidentReadsSystemStats(t *testing.T) {
	comfy := &comfyAttachedFixture{}
	server := comfy.server(t)
	r := router.New(router.ComfyUI(server.URL))

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

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// fakeInstrument reports the samples a test writes.
type fakeInstrument struct {
	samples []instrument.Sample
}

func (fakeInstrument) Name() string        { return instrument.NameWindowsGPUCounters }
func (fakeInstrument) Resources() []string { return []string{instrument.Card0} }
func (f fakeInstrument) Read(resource string) (instrument.Reading, error) {
	return instrument.Reading{Resource: resource, Samples: f.samples, At: time.Now()}, nil
}

func statesWithResident() []router.HostState {
	return []router.HostState{
		{Host: "lmstudio", Up: true, Resident: []string{"gemma-4-26b", "qwen3.8-27b"}},
		{Host: "ollama", Up: true},
		{Host: "comfyui", Up: false, Resident: []string{"sdxl"}},
	}
}

// Each host's resident model list is one claimed row of card:0 under that
// host's name, with the model in detail and no amount: the hosts measured on
// this machine report none (CONTRACT.md RES-T3).
func TestHostResidentModelsBecomeClaims(t *testing.T) {
	claims := hostClaims(statesWithResident)()
	if len(claims) != 2 {
		t.Fatalf("claims %+v", claims)
	}
	for _, c := range claims {
		if c.Resource != instrument.Card0 || c.Program != claimedHostPrefix+"lmstudio" || c.Account != "" {
			t.Fatalf("claim %+v", c)
		}
	}
	if claims[0].Detail != "gemma-4-26b" || claims[1].Detail != "qwen3.8-27b" {
		t.Fatalf("claims lost the model names: %+v", claims)
	}
}

// A host that is down claims nothing.
func TestADownHostClaimsNothing(t *testing.T) {
	claims := hostClaims(func() []router.HostState {
		return []router.HostState{{Host: "comfyui", Up: false, Resident: []string{"sdxl"}}}
	})()
	if len(claims) != 0 {
		t.Fatalf("a down host claimed %+v", claims)
	}
}

// The router's residency answer is the table's verified rows, and the claims
// the table also carries stay out of it: the router already reports what each
// host says it holds as that host's resident list.
func TestRouterReadsResidencyFromTheTable(t *testing.T) {
	in := fakeInstrument{samples: []instrument.Sample{
		{PID: 28188, Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 21247127552},
		{PID: 50932, Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 18560939827},
	}}
	table := resourceservice.NewTable(in, hostClaims(statesWithResident), time.Millisecond)

	r := router.New()
	r.SetResidency(tableResidency{table: table})
	r.Survey()
	_, holders, why, _, _ := r.Residency(false)
	if why != "" {
		t.Fatalf("the table answered and the router recorded %q", why)
	}
	if len(holders) != 2 {
		t.Fatalf("the router read %d rows from the table: %+v", len(holders), holders)
	}
	if holders[0].Program != `C:\lms\llama-server.exe` || holders[0].Account != "S-1-5-21-7-1001" {
		t.Fatalf("row %+v", holders[0])
	}
	// 21247127552 bytes is 19.79 GiB; the measurement's state (c) row.
	if holders[0].GiB != 19.79 || holders[1].GiB != 17.29 {
		t.Fatalf("rows %+v", holders)
	}
}

// A table whose instrument measures nothing leaves the router with no holder
// and no reason: nothing refused, nothing held.
func TestRouterResidencyIsEmptyWhenNothingIsMeasured(t *testing.T) {
	table := resourceservice.NewTable(instrument.None(), hostClaims(statesWithResident), time.Millisecond)
	r := router.New()
	r.SetResidency(tableResidency{table: table})
	r.Survey()
	_, holders, why, _, _ := r.Residency(false)
	if len(holders) != 0 || why != "" {
		t.Fatalf("holders %+v, why %q", holders, why)
	}
}

// A fake host stands in for LM Studio's GET /api/v0/models: one model,
// resident until a test flips it (simulating a yield the host's own API
// answered), read as many times as the test's survey calls ask.
type fakeLMStudio struct {
	loaded int32 // atomic; 1 while the model reads resident
}

func (f *fakeLMStudio) start(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state := "not-loaded"
		if atomic.LoadInt32(&f.loaded) != 0 {
			state = "loaded"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{
			{"id": "gemma-4-26b", "type": "llm", "state": state},
		}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A claimed host: row comes from the router's last survey (CONTRACT.md
// RES-T3). A caller that does not ask for fresh keeps the row that survey
// found even after the fake host stops reporting the model resident, the
// same way an attached engine's yield would leave the claim standing until
// something surveys again. fresh asks the table's refresh hook for a new
// survey first, so the row is gone the moment that survey sees it gone.
func TestTableFreshResurveysAHostSoAYieldedClaimIsGone(t *testing.T) {
	host := &fakeLMStudio{loaded: 1}
	srv := host.start(t)

	r := router.New(router.LMStudio(srv.URL))
	hosts := func() []router.HostState {
		states, _, _, _, _ := r.Residency(false)
		return states
	}
	table := resourceservice.NewTable(instrument.None(), hostClaims(hosts), time.Hour)
	table.SetRefresh(r.Survey)

	r.Survey() // the startup poll a running runtime would already have done
	state, err := table.Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 || state.Holders[0].Program != claimedHostPrefix+"lmstudio" {
		t.Fatalf("expected the loaded model claimed, got %+v", state.Holders)
	}

	// The model host now says the model let go, the way it would answer
	// after an attached holder yielded it. The router has not surveyed again.
	atomic.StoreInt32(&host.loaded, 0)

	state, err = table.Holders(instrument.Card0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 1 {
		t.Fatalf("a read that did not ask for fresh should still show the last survey's claim: %+v", state.Holders)
	}

	state, err = table.Holders(instrument.Card0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Holders) != 0 {
		t.Fatalf("fresh should have resurveyed and found the claim gone: %+v", state.Holders)
	}
}

// giB rounds to the two decimals the router's residency answer prints.
func TestGiBRoundsToTwoDecimals(t *testing.T) {
	for _, c := range []struct {
		bytes int64
		want  float64
	}{
		{18560939827, 17.29},
		{21247127552, 19.79},
		{0, 0},
		{1 << 30, 1},
	} {
		if got := giB(c.bytes); got != c.want {
			t.Fatalf("giB(%d) = %v, want %v", c.bytes, got, c.want)
		}
	}
}

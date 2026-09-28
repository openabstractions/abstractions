package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// fakeInstrument reports the samples a test writes, the same fixture shape
// serve/runtime_resources_test.go uses for the router's residency read.
type fakeInstrument struct {
	name    string
	offers  []string
	samples []instrument.Sample
}

func (f fakeInstrument) Name() string        { return f.name }
func (f fakeInstrument) Resources() []string { return f.offers }
func (f fakeInstrument) Read(resource string) (instrument.Reading, error) {
	return instrument.Reading{Resource: resource, Capacity: 24 << 30, Samples: f.samples, At: time.Now()}, nil
}

// selfExe is this test binary's own path, as rights names a subject program:
// the bound caller the resource table's own-rows narrowing sees.
func selfExe(t *testing.T) string {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(path)
}

// cardTestTable is a two-row card:0 fixture: one row under another program,
// and one row under this test binary's own path so the own-rows narrowing
// path has a row it must keep.
func cardTestTable(t *testing.T) *resourceservice.Table {
	in := fakeInstrument{name: instrument.NameWindowsGPUCounters, offers: []string{instrument.Card0}, samples: []instrument.Sample{
		{PID: 1, Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 21247127552},
		{PID: os.Getpid(), Program: selfExe(t), Account: "self-account", Amount: 1 << 30},
	}}
	return resourceservice.NewTable(in, func() []resourceservice.Claim {
		return []resourceservice.Claim{{Resource: instrument.Card0, Program: "host:lmstudio", Detail: "gemma-4-26b"}}
	}, time.Second)
}

// panelCardRuntime starts a facade runtime whose resource table is the fixture
// above, over its own endpoint so tests can run in parallel with the other
// panelConfigRuntimeWith callers in this package.
func panelCardRuntime(t *testing.T, policy resourceservice.Policy) {
	t.Helper()
	prefix := fmt.Sprintf("panel-card-%d-%d", os.Getpid(), time.Now().UnixNano())
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.ResourceTable = cardTestTable(t)
		o.ResourceTableEndpoint = listen.Endpoint(prefix + "-resource-table")
		o.ResourceTablePolicy = policy
	})
}

// The card table reads every resource the table reports and forwards each
// row unchanged: a permitted caller sees the machine's rows, verified and
// claimed alike.
func TestCardTableReadsEveryResourceAndRow(t *testing.T) {
	own(t)
	panelCardRuntime(t, func(context.Context, *identity.Peer, string, string) error { return nil })
	h := newTestPanel(t).handler("test-key")

	if r := panelRequest(t, h, "/card?k=wrong", nil); r.Code != 403 {
		t.Fatalf("page without the key: %d", r.Code)
	}
	view := decodePanel[cardTableView](t, 200, panelRequest(t, h, "/card/table", nil).Body.Bytes())
	if len(view.Resources) != 1 {
		t.Fatalf("resources %+v", view.Resources)
	}
	state := view.Resources[0]
	if state.Resource != instrument.Card0 || state.Instrument != instrument.NameWindowsGPUCounters {
		t.Fatalf("resource state %+v", state)
	}
	if len(state.Holders) != 3 {
		t.Fatalf("a permitted caller read %d rows: %+v", len(state.Holders), state.Holders)
	}
	var verified, claimed int
	for _, row := range state.Holders {
		switch row.Evidence {
		case "verified":
			verified++
			if row.Amount == 0 {
				t.Fatalf("a verified row carried no amount: %+v", row)
			}
		case "claimed":
			claimed++
			if row.Amount != 0 {
				t.Fatalf("a claimed row carried an amount: %+v", row)
			}
			if row.Program != "host:lmstudio" || row.Detail != "gemma-4-26b" {
				t.Fatalf("claimed row %+v", row)
			}
		default:
			t.Fatalf("unexpected evidence %q on %+v", row.Evidence, row)
		}
	}
	if verified != 2 || claimed != 1 {
		t.Fatalf("verified %d claimed %d, want 2 and 1: %+v", verified, claimed, state.Holders)
	}
	fresh := decodePanel[cardTableView](t, 200, panelRequest(t, h, "/card/table?fresh=1", nil).Body.Bytes())
	if len(fresh.Resources) != 1 || len(fresh.Resources[0].Holders) != 3 {
		t.Fatalf("a fresh read %+v", fresh)
	}
}

// A caller the policy refuses reads its own rows only (CONTRACT.md RES-T4):
// the claimed row under another program's name is dropped along with the
// other verified row, and the handler forwards the narrowed state unchanged.
func TestCardTableNarrowsToOwnRowsWhenRefused(t *testing.T) {
	own(t)
	panelCardRuntime(t, func(context.Context, *identity.Peer, string, string) error {
		return errors.New("no rule permits this caller")
	})
	h := newTestPanel(t).handler("test-key")

	view := decodePanel[cardTableView](t, 200, panelRequest(t, h, "/card/table", nil).Body.Bytes())
	if len(view.Resources) != 1 {
		t.Fatalf("resources %+v", view.Resources)
	}
	holders := view.Resources[0].Holders
	if len(holders) != 1 {
		t.Fatalf("a refused caller read %d rows, want its own 1: %+v", len(holders), holders)
	}
	if holders[0].Program != selfExe(t) || holders[0].Evidence != "verified" {
		t.Fatalf("the narrowed row was not this caller's own: %+v", holders[0])
	}
}

// Without a runtime the card table reads unavailable rather than falling back
// to anything local.
func TestCardTableUnavailableWithoutARuntime(t *testing.T) {
	own(t)
	h := newTestPanel(t).handler("test-key")
	if r := panelRequest(t, h, "/card/table", nil); r.Code != 503 {
		t.Fatalf("card table with no runtime answered %d: %s", r.Code, r.Body.String())
	}
}

// cardAwakeTestTable is a table whose only claim is one awake lease, so the
// awake section's table-first read has a row to find without disturbing the
// card:0 fixture the other card tests use.
func cardAwakeTestTable(t *testing.T) *resourceservice.Table {
	in := fakeInstrument{name: instrument.NameNone}
	return resourceservice.NewTable(in, func() []resourceservice.Claim {
		return []resourceservice.Claim{{Resource: resourceservice.ResourceAwake, Program: `C:\downloader.exe`,
			Account: "S-1-5-21-7-1001", Grant: resourceservice.AwakeRight, Detail: "a six-hour download",
			Lease: "awake-1", Since: time.Date(2026, 9, 22, 9, 5, 0, 0, time.UTC)}}
	}, time.Second)
}

func panelCardAwakeRuntime(t *testing.T, policy resourceservice.Policy) {
	t.Helper()
	prefix := fmt.Sprintf("panel-card-awake-%d-%d", os.Getpid(), time.Now().UnixNano())
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.ResourceTable = cardAwakeTestTable(t)
		o.ResourceTableEndpoint = listen.Endpoint(prefix + "-resource-table")
		o.ResourceTablePolicy = policy
	})
}

// cardTableWithoutAwake claims nothing at all, so Holders("awake", ...)
// answers a genuinely empty state rather than the card:0 fixture's samples,
// which the fixture's fake instrument returns for any resource name asked.
func cardTableWithoutAwake(t *testing.T) *resourceservice.Table {
	in := fakeInstrument{name: instrument.NameNone}
	return resourceservice.NewTable(in, func() []resourceservice.Claim { return nil }, time.Second)
}

func panelCardRuntimeWithoutAwake(t *testing.T, policy resourceservice.Policy) {
	t.Helper()
	prefix := fmt.Sprintf("panel-card-no-awake-%d-%d", os.Getpid(), time.Now().UnixNano())
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.ResourceTable = cardTableWithoutAwake(t)
		o.ResourceTableEndpoint = listen.Endpoint(prefix + "-resource-table")
		o.ResourceTablePolicy = policy
	})
}

// The awake section reads the resource table's awake rows first (CONTRACT.md
// RES-A1): a wake hold is a claimed lease row like any other, carrying
// program, account, its lease id and why it is held.
func TestCardAwakeReadsTheTableFirst(t *testing.T) {
	own(t)
	panelCardAwakeRuntime(t, func(context.Context, *identity.Peer, string, string) error { return nil })
	h := newTestPanel(t).handler("test-key")

	view := decodePanel[cardAwakeView](t, 200, panelRequest(t, h, "/card/awake", nil).Body.Bytes())
	if view.Outcome != "page" || view.Source != "table" {
		t.Fatalf("awake view %+v", view)
	}
	if len(view.Holds) != 1 {
		t.Fatalf("awake holds %+v", view.Holds)
	}
	hold := view.Holds[0]
	if hold.Program != `C:\downloader.exe` || hold.Account != "S-1-5-21-7-1001" || hold.Lease != "awake-1" ||
		hold.Why != "a six-hour download" || hold.Since == "" {
		t.Fatalf("the table's awake row %+v", hold)
	}
}

// A table with no awake resource falls back to the legacy rights read;
// own(t) leaves no admin.secret, which means no legacy rights service ever
// ran beside this state, so the table's answer stands: a page with nobody
// holding awake, and no path or secret in any reason.
func TestCardAwakeFallsBackWhenTheTableHasNoAwakeResource(t *testing.T) {
	own(t)
	panelCardRuntimeWithoutAwake(t, func(context.Context, *identity.Peer, string, string) error { return nil })
	h := newTestPanel(t).handler("test-key")

	view := decodePanel[cardAwakeView](t, 200, panelRequest(t, h, "/card/awake", nil).Body.Bytes())
	if view.Outcome != "page" || view.Source != "table" || len(view.Holds) != 0 || view.Reason != "" {
		t.Fatalf("awake view %+v", view)
	}
	if strings.Contains(view.Reason, "table:") {
		t.Fatalf("a table that answered with no rows is not a table failure: %q", view.Reason)
	}
}

// Without a runtime the table is unavailable, and that is the whole answer:
// the legacy rights fallback is not consulted, so the reason carries the
// table's resolution failure alone and never a path to an admin secret.
func TestCardAwakeUnavailableWithoutARuntimeNamesTheTableAlone(t *testing.T) {
	own(t)
	h := newTestPanel(t).handler("test-key")
	view := decodePanel[cardAwakeView](t, 200, panelRequest(t, h, "/card/awake", nil).Body.Bytes())
	if view.Outcome != "unavailable" || view.Source != "" || view.Reason == "" {
		t.Fatalf("awake view %+v", view)
	}
	if strings.Contains(view.Reason, "rights fallback") || strings.Contains(view.Reason, "admin.secret") {
		t.Fatalf("the reason should be the table's alone: %q", view.Reason)
	}
}

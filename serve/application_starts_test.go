package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The start log records when a registered application became present, keeps
// it across a restart for seven days, and pages it newest first to the
// operator programs only (APPLICATIONS.md, "Start log"): an application's
// second instance, a renewal and a withdrawal record nothing, and any other
// caller, another account included, reads nothing.
func TestApplicationStartLogPagesToTheOperatorOnly(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "panel.exe")}
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "editor.exe")}
	open := func() *applicationDirectory {
		d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
		if err != nil {
			t.Fatal(err)
		}
		d.operator = func(program string) bool { return program == operator.Program }
		return d
	}
	d := open()
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	for _, name := range []string{"editor", "viewer"} {
		if r := d.register(ctx, operator, wire.ApplicationDescriptor{Name: name, Program: app.Program, Title: "Title " + name, StartGuidance: "Open"}); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(r)
		}
	}
	p := wire.ApplicationPresence{Application: "editor", LeaseMs: 30000}
	first := d.announce(ctx, app, p)
	now = now.Add(time.Second)
	second := d.announce(ctx, app, p) // another live instance: no start
	renewed := p
	renewed.Instance = first.Instance
	third := d.announce(ctx, app, renewed)
	if first.Outcome != wire.ApplicationOutcomeApplied || second.Outcome != wire.ApplicationOutcomeApplied || third.Instance != first.Instance {
		t.Fatal(first, second, third)
	}
	if w := d.withdraw(ctx, app, "editor", second.Instance); w.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(w)
	}
	now = now.Add(time.Second)
	d.announce(ctx, app, wire.ApplicationPresence{Application: "viewer", LeaseMs: 30000})
	// Both editor leases end; the next announcement is a new start.
	now = now.Add(31 * time.Second)
	d.announce(ctx, app, p)
	if len(d.starts) != 3 {
		t.Fatalf("%+v", d.starts)
	}

	restarted := open()
	restarted.now = func() time.Time { return now }
	page := restarted.listStarts(ctx, operator, "", 2)
	if page.Outcome != wire.ApplicationOutcomePage || len(page.Starts) != 2 || page.Complete || page.Cursor == "" || page.CompleteSinceUnixMs != 0 {
		t.Fatalf("%+v", page)
	}
	if s := page.Starts[0]; s.Application != "editor" || s.Program != app.Program || s.Title != "Title editor" || s.Launched || s.StartedUnixMs != now.UnixMilli() {
		t.Fatalf("newest first: %+v", s)
	}
	if page.Starts[1].Application != "viewer" {
		t.Fatalf("%+v", page.Starts)
	}
	rest := restarted.listStarts(ctx, operator, page.Cursor, 64)
	if rest.Outcome != wire.ApplicationOutcomePage || len(rest.Starts) != 1 || !rest.Complete || rest.Cursor != "" || rest.Starts[0].Application != "editor" {
		t.Fatalf("%+v", rest)
	}
	for _, caller := range []rights.Subject{app, {Account: "other", Program: operator.Program}, {}} {
		if refused := restarted.listStarts(ctx, caller, "", 64); refused.Outcome != wire.ApplicationOutcomeForbidden || len(refused.Starts) != 0 {
			t.Fatalf("%+v read %+v", caller, refused)
		}
	}
	if invalid := restarted.listStarts(ctx, operator, "", 65); invalid.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal(invalid)
	}
	// A new start changes the log: an old cursor is stale.
	now = now.Add(2 * time.Second)
	restarted.announce(ctx, app, wire.ApplicationPresence{Application: "viewer", LeaseMs: 1000})
	if stale := restarted.listStarts(ctx, operator, page.Cursor, 64); stale.Outcome != wire.ApplicationOutcomeStale || len(stale.Starts) != 0 {
		t.Fatalf("%+v", stale)
	}
	// Seven days later the old starts are gone.
	now = now.Add(startRetention)
	if aged := restarted.listStarts(ctx, operator, "", 64); aged.Outcome != wire.ApplicationOutcomePage || len(aged.Starts) != 0 {
		t.Fatalf("%+v", aged)
	}
}

// A stale write base (a read that failed at open, a file changed under the
// directory) does not stop the log: the next start reads the base again and
// writes over it.
func TestApplicationStartLogRecoversFromAStaleBase(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "applications", startsFile), []byte(`{"starts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	v := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(state, "editor.exe"), Title: "Editor", StartGuidance: "Open"}
	d.recordStartLocked(ctx, "editor", "test", "", v)
	reopened, err := openApplications(state, "account", d.decide)
	if err != nil || len(reopened.starts) != 1 || reopened.starts[0].Application != "editor" {
		t.Fatalf("the start was not written over a stale base: %+v %v", reopened.starts, err)
	}
}

// The log holds at most maxStarts entries; eviction records the time up to
// which it is incomplete, and an activation the runtime started is marked
// launched.
func TestApplicationStartLogBoundAndLaunchedStarts(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	operator := rights.Subject{Account: "account", Program: filepath.Join(state, "panel.exe")}
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	d.operator = func(program string) bool { return program == operator.Program }
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	d.now = func() time.Time { return now }
	v := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(state, "editor.exe"), Title: "Editor", StartGuidance: "Open"}
	d.descriptors["editor"] = v
	var firstKept int64
	for i := range maxStarts + 2 {
		now = now.Add(time.Second)
		if i == 2 {
			firstKept = now.UnixMilli()
		}
		d.recordStartLocked(ctx, "editor", "test", "", v)
	}
	page := d.listStarts(ctx, operator, "", 64)
	if len(d.starts) != maxStarts || d.starts[0].StartedUnixMs != firstKept || page.CompleteSinceUnixMs != firstKept-1000 {
		t.Fatalf("len=%d since=%d", len(d.starts), page.CompleteSinceUnixMs)
	}
	d.attempts["editor"] = &applicationActivationAttempt{done: make(chan struct{}), session: "test", generation: d.generations["editor"]}
	d.recordStartLocked(ctx, "editor", "test", "", v)
	if last := d.starts[len(d.starts)-1]; !last.Launched {
		t.Fatalf("a start the runtime's activation waited for is launched: %+v", last)
	}
}

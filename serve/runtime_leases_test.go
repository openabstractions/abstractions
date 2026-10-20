package main

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	job "github.com/openabstractions/abstraction-job/go"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// Installation writes the rules leases need: holding the card and the wake.
// Asking a lease back needs no installed rule: the yield switch is on by
// default and the person's deny on one app's program is the off switch
// (RES-L3). No stock engine and no host: name gets a yield rule.
func TestInstallationWritesTheLeaseRules(t *testing.T) {
	rules := resourceRules()
	want := map[string]string{
		resourceservice.ActionHold + " " + instrument.Card0:              "the card",
		resourceservice.ActionHold + " " + resourceservice.ResourceAwake: "the wake",
	}
	for _, rule := range rules {
		delete(want, rule.Action+" "+rule.Resource)
		if rule.Action == resourceservice.ActionYield {
			t.Fatalf("installation writes a yield rule %+v", rule)
		}
	}
	if len(want) != 0 || len(rules) != 2 {
		t.Fatalf("installation writes %+v; missing %v", rules, want)
	}
}

// The person's off switch for one app's memory is enforced by the runtime's
// own lease book: the book the runtime composes (leaseBookOptions) decides
// abstraction.resource/yield through the runtime's rights for its own program
// on the holder's program. A deny on X keeps X's lease and the book asks Y
// instead; with no rule the book asks X, the first planned lease, as before.
func TestTheYieldSwitchKeepsADeniedAppsMemory(t *testing.T) {
	const gib = int64(1) << 30
	x := resourceservice.Subject{Program: filepath.Join(t.TempDir(), "comfyui.exe"), Account: "S-1-5-21-7-1001"}
	y := resourceservice.Subject{Program: filepath.Join(t.TempDir(), "modelhostd.exe"), Account: "S-1-5-21-7-1001"}
	asker := resourceservice.Subject{Program: filepath.Join(t.TempDir(), "krita.exe"), Account: "S-1-5-21-7-1001"}
	for _, deny := range []bool{true, false} {
		state := t.TempDir()
		rights := testRights(t, state)
		if err := rights.policy.RegisterAction(resourceservice.ActionYield); err != nil {
			t.Fatal(err)
		}
		if deny {
			if err := rights.policy.Set(rights.self(), resourceservice.ActionYield, x.Program, false); err != nil {
				t.Fatal(err)
			}
		}
		in := instrument.NewFixed()
		samples := map[string]instrument.Sample{
			x.Program: {PID: 11, Program: x.Program, Account: x.Account, Amount: 20 * gib},
			y.Program: {PID: 12, Program: y.Program, Account: y.Account, Amount: 20 * gib},
		}
		var mu sync.Mutex
		measure := func() {
			mu.Lock()
			defer mu.Unlock()
			var rows []instrument.Sample
			for _, sample := range samples {
				rows = append(rows, sample)
			}
			in.Set(instrument.Card0, instrument.FixedResource{Capacity: 48 * gib, Samples: rows})
		}
		in.Set(instrument.Card0, instrument.FixedResource{Capacity: 48 * gib})
		composed := &runtimeResources{table: resourceservice.NewTable(in, nil, time.Nanosecond), path: filepath.Join(state, "resources")}
		options := leaseBookOptions(composed, nil, rights, job.PlatformGPU)
		// Holding is not under test; the yield switch is the production one.
		options.Hold = func(context.Context, *identity.Peer, string, string) error { return nil }
		options.KeepAwake, options.KeepSystemAwake = nil, nil
		book, err := resourceservice.OpenBook(options)
		if err != nil {
			t.Fatal(err)
		}
		for _, holder := range []resourceservice.Subject{x, y} {
			if out := book.Acquire(context.Background(), holder, &identity.Peer{}, instrument.Card0, 20*gib, 0); out.Lease == nil {
				t.Fatalf("%s: lease read %s", holder.Program, out.Outcome)
			}
			time.Sleep(time.Millisecond) // x's lease is the older one
		}
		measure()
		ctx, cancel := context.WithCancel(context.Background())
		var asked sync.Map
		for _, holder := range []resourceservice.Subject{x, y} {
			go func(holder resourceservice.Subject) {
				page, err := book.Observe(ctx, holder, "", 30000)
				if err != nil || len(page.Requests) == 0 {
					return
				}
				asked.Store(holder.Program, true)
				mu.Lock()
				delete(samples, holder.Program)
				mu.Unlock()
				measure()
				book.Answer(holder, page.Requests[0].ID, rwire.YieldAnswerYielded, "")
			}(holder)
		}
		out := book.Acquire(context.Background(), asker, &identity.Peer{}, instrument.Card0, 20*gib, 20000)
		cancel()
		_, askedX := asked.Load(x.Program)
		_, askedY := asked.Load(y.Program)
		if out.Lease == nil || askedX == deny || askedY != deny {
			t.Fatalf("deny=%v: %s, asked x=%v y=%v, records %+v", deny, out.Outcome, askedX, askedY, out.Asked)
		}
		if deny && (len(out.Asked) != 2 || out.Asked[0].Holder != x.Program ||
			out.Asked[0].Answer != "refused: the person keeps comfyui's memory" || out.Asked[1].Answer != resourceservice.AnswerYielded) {
			t.Fatalf("a kept holder reads %+v", out.Asked)
		}
	}
}

// Without a state directory there is no lease book: a lease that does not
// survive a restart is not a lease, and the table is served read-only.
func TestARuntimeWithoutDurableStateServesTheTableAlone(t *testing.T) {
	composed := composeResources("", router.New(), nil, func(error) {})
	if composed.table == nil {
		t.Fatal("no table")
	}
	if composed.book != nil {
		t.Fatal("a runtime with nowhere to keep leases opened a lease book")
	}
}

// With one, the book and its record live beside the table, and a runtime with
// no rights policy grants nothing: an absent policy is never permission.
func TestTheLeaseBookLivesInTheRuntimeState(t *testing.T) {
	state := t.TempDir()
	var reported []error
	composed := composeResources(state, router.New(), nil, func(err error) { reported = append(reported, err) })
	t.Cleanup(composed.close)
	if len(reported) != 0 {
		t.Fatalf("composing reported %v", reported)
	}
	if composed.book == nil {
		t.Fatal("no lease book")
	}
	for _, name := range []string{leaseBookFile, leaseAuditFile} {
		if got := filepath.Join(state, "resources", name); !strings.HasPrefix(got, state) {
			t.Fatalf("%s is outside the state directory", got)
		}
	}
	out := composed.book.Acquire(context.Background(), resourceservice.Subject{Program: `C:\x.exe`}, nil,
		instrument.Card0, 1<<30, 0)
	if out.Outcome.String() != "unavailable" {
		t.Fatalf("a runtime with no rights policy granted %s", out.Outcome)
	}
	if _, err := resourceservice.ReadAudit(filepath.Join(state, "resources", leaseAuditFile), 8); err != nil {
		t.Fatalf("the record could not be read: %v", err)
	}
}

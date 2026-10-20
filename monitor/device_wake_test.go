package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

// cardRegistry answers registry@1 with one provider declaration per
// program, each naming card:0, and one that names a store only.
type cardRegistry struct{ cards, others []string }

func (r cardRegistry) Declarations() (fwire.DeclarationList, error) {
	list := fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Revision: "providers-v2:r", Declarations: []fwire.DeclarationState{}}
	add := func(program string, resources []string) {
		list.Declarations = append(list.Declarations, fwire.DeclarationState{
			Declaration: fwire.Declaration{Name: "p" + strconv.Itoa(len(list.Declarations)), Program: program, Arguments: []string{}, Endpoint: "p" + strconv.Itoa(len(list.Declarations)) + "-v1",
				Transport: fwire.DeclarationTransportNative, Contracts: []string{"abstraction.inference/chat@1"}, Activation: fwire.ActivationOnDemand,
				Role: fwire.DeclarationRoleProvider, Resources: resources},
			DeclaredBy: "installation", Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleProvider, Described: []fwire.ServiceState{}})
	}
	for _, program := range r.cards {
		add(program, []string{"card:0", "profile:chat"})
	}
	for _, program := range r.others {
		add(program, []string{"store:ollama"})
	}
	return list, nil
}
func (cardRegistry) Declare(string, fwire.Declaration) (fwire.DeclarationChange, error) {
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeForbidden}, nil
}
func (cardRegistry) Withdraw(string, string) (fwire.DeclarationChange, error) {
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeForbidden}, nil
}
func (cardRegistry) Observe(string, int64) (fwire.DeclarationObservation, error) {
	return fwire.DeclarationObservation{Outcome: fwire.DeclarationListOutcomeForbidden, Declarations: []fwire.DeclarationState{}}, nil
}

// serveCardRegistry adds registry to a fixture runtime's options.
func serveCardRegistry(t *testing.T, o *host.Options, registry cardRegistry) {
	t.Helper()
	at := o.ConfigEndpoint + "-registry"
	l, err := listen.Listen(at)
	if err != nil {
		t.Fatal(err)
	}
	o.Registry, o.RegistryEndpoint = registryService{listener: l, handler: registry}, at
}

// wakeFixture is one runtime with a resource table, a rights policy and a
// registry: the model server, a card provider, holds the wake lease and a
// card lease; the downloader holds the wake lease alone; idle, a card
// provider, and other hold nothing.
type wakeFixture struct {
	policy                               *rights.DecisionPolicy
	allow                                *atomic.Bool
	modelServer, downloader, idle, other string
}

const wakeFixtureAccount = "wake-account"

func newWakeFixture(t *testing.T) wakeFixture {
	t.Helper()
	dir := t.TempDir()
	program := func(name string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	f := wakeFixture{allow: &atomic.Bool{}, modelServer: program("lemonaded.exe"), downloader: program("downloader.exe"),
		idle: program("idle.exe"), other: program("other.exe")}
	f.allow.Store(true)
	policy, err := rights.LoadDecisionPolicyWithDefinitions(filepath.Join(dir, "policy.json"), []string{rights.AwakeAction}, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.policy = policy
	since := time.Date(2026, 10, 2, 9, 5, 0, 0, time.UTC)
	table := resourceservice.NewTable(fakeInstrument{name: instrument.NameWindowsGPUCounters, offers: []string{instrument.Card0}}, func() []resourceservice.Claim {
		return []resourceservice.Claim{
			{Resource: instrument.Card0, Program: f.modelServer, Account: wakeFixtureAccount, Lease: "card-1", Amount: 1 << 30, Since: since},
			{Resource: resourceservice.ResourceAwake, Program: f.modelServer, Account: wakeFixtureAccount, Grant: resourceservice.AwakeRight, Lease: "awake-1", Detail: "a chat", Since: since},
			{Resource: resourceservice.ResourceAwake, Program: f.downloader, Account: wakeFixtureAccount, Grant: resourceservice.AwakeRight, Lease: "awake-2", Detail: "a download", Since: since},
		}
	}, time.Second)
	// panelConfigRuntimeWith owns the runtime's lifetime, the Panel's trust
	// in it and the platforms where Program proof is unavailable.
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.RightsPolicy, o.RightsEndpoint = policy, o.ConfigEndpoint+"-rights"
		o.RightsOperator = func(ctx context.Context, peer *identity.Peer) error {
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() || !f.allow.Load() {
				return rightsservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}
		o.ResourceTable, o.ResourceTableEndpoint = table, o.ConfigEndpoint+"-resource-table"
		o.ResourceTablePolicy = func(context.Context, *identity.Peer, string, string) error { return nil }
		serveCardRegistry(t, o, cardRegistry{cards: []string{f.modelServer, f.idle}, others: []string{f.downloader}})
	})
	return f
}

// rule reads one app's wake rule from the fixture's policy file.
func (f wakeFixture) rule(t *testing.T, program string) wire.RuleRead {
	t.Helper()
	return f.policy.ReadRule(wire.Subject{Account: wakeFixtureAccount, Program: program}, rights.AwakeAction, rights.AwakeResource)
}

// The wake list is state on arrival: every app holding the wake lease, the
// display level beside a card lease, and every app with a rule, allowed or
// stopped, whether or not it holds the lease now.
func TestDeviceWakeListsHoldersAndRulesOnArrival(t *testing.T) {
	own(t)
	f := newWakeFixture(t)
	h := (&servicePanel{}).handler("test-key")
	for _, edit := range []wakeEdit{
		{Account: wakeFixtureAccount, Program: f.downloader, Allow: false},
		{Account: wakeFixtureAccount, Program: f.idle, Allow: true},
		{Account: wakeFixtureAccount, Program: f.other, Allow: false},
	} {
		r := panelRequest(t, h, "/card/wake", edit)
		if got := decodePanel[wakeEditResult](t, r.Code, r.Body.Bytes()); got.Outcome != "applied" {
			t.Fatalf("seed %+v: %+v", edit, got)
		}
	}
	r := panelRequest(t, h, "/card/wake", nil)
	view := decodePanel[wakeView](t, r.Code, r.Body.Bytes())
	if view.Outcome != "page" || view.Rules != "read" || len(view.Apps) != 4 {
		t.Fatalf("wake view %+v", view)
	}
	byProgram := map[string]wakeApp{}
	for _, app := range view.Apps {
		byProgram[app.Program] = app
		for _, p := range []string{f.modelServer, f.downloader, f.idle, f.other} {
			if samePanelProgram(app.Program, p) {
				byProgram[p] = app
			}
		}
	}
	if a := byProgram[f.modelServer]; !a.Holding || !a.Display || a.Rule != "none" || a.Why != "a chat" || a.Since == "" || a.Account != wakeFixtureAccount {
		t.Fatalf("model server row %+v", a)
	}
	if a := byProgram[f.downloader]; !a.Holding || a.Display || a.Rule != "stopped" {
		t.Fatalf("downloader row: a wake lease without a card lease keeps only the system awake, and its deny shows %+v", a)
	}
	if a := byProgram[f.idle]; a.Holding || a.Rule != "allowed" {
		t.Fatalf("an allowed app holding nothing %+v", a)
	}
	if a := byProgram[f.other]; a.Holding || a.Rule != "stopped" {
		t.Fatalf("a stopped app holding nothing %+v", a)
	}
	for _, a := range view.Apps {
		if strings.Contains(a.Title, "awake-") || strings.Contains(a.Title, "card-") {
			t.Fatalf("a lease id became a title %+v", a)
		}
	}

	// A Panel that may not read permissions still lists who holds wake now,
	// and says the rules are unread.
	f.allow.Store(false)
	r = panelRequest(t, h, "/card/wake", nil)
	view = decodePanel[wakeView](t, r.Code, r.Body.Bytes())
	if view.Outcome != "page" || view.Rules != "forbidden" || len(view.Apps) != 2 {
		t.Fatalf("holders without rules %+v", view)
	}
}

// Stop writes a deny at the awake level (a revoke would let the runtime's
// provider sweep write its default permit again); allow writes a permit;
// repeating either changes nothing; a Panel without operator authority
// changes nothing and says so.
func TestDeviceWakeAllowAndStopWriteTheAppsRule(t *testing.T) {
	own(t)
	f := newWakeFixture(t)
	h := (&servicePanel{}).handler("test-key")
	set := func(program string, allow bool) string {
		t.Helper()
		r := panelRequest(t, h, "/card/wake", wakeEdit{Account: wakeFixtureAccount, Program: program, Allow: allow})
		return decodePanel[wakeEditResult](t, r.Code, r.Body.Bytes()).Outcome
	}
	if got := set(f.modelServer, false); got != "applied" {
		t.Fatalf("stop: %s", got)
	}
	if read := f.rule(t, f.modelServer); read.Outcome != wire.RuleReadOutcomeFound || read.Record.Rule.Permit {
		t.Fatalf("stop wrote %+v, want a deny on %s for %s", read, rights.AwakeAction, rights.AwakeResource)
	}
	if got := set(f.modelServer, false); got != "unchanged" {
		t.Fatalf("second stop: %s", got)
	}
	if got := set(f.modelServer, true); got != "applied" {
		t.Fatalf("allow: %s", got)
	}
	if read := f.rule(t, f.modelServer); read.Outcome != wire.RuleReadOutcomeFound || !read.Record.Rule.Permit {
		t.Fatalf("allow wrote %+v, want a permit", read)
	}
	other := f.policy.ReadRule(wire.Subject{Account: "another-account", Program: f.modelServer}, rights.AwakeAction, rights.AwakeResource)
	if other.Outcome != wire.RuleReadOutcomeUnknown {
		t.Fatalf("another account's rule changed %+v", other)
	}

	f.allow.Store(false)
	if got := set(f.modelServer, false); got != "forbidden" {
		t.Fatalf("a Panel without operator authority: %s", got)
	}
	if read := f.rule(t, f.modelServer); !read.Record.Rule.Permit {
		t.Fatalf("a refused stop changed the rule %+v", read)
	}

	for _, bad := range []any{
		wakeEdit{Account: wakeFixtureAccount, Program: "relative.exe", Allow: true},
		wakeEdit{Program: f.modelServer, Allow: true},
		map[string]any{"account": wakeFixtureAccount, "program": f.modelServer, "allow": true, "resource": "awake:display"},
	} {
		if r := panelRequest(t, h, "/card/wake", bad); r.Code != 400 {
			t.Fatalf("edit %+v answered %d %s", bad, r.Code, r.Body.String())
		}
	}
}

// Allow writes a permit for every app, a card provider or not: hold on awake
// asks nothing at first use, so removing a deny would leave the app refused.
// Allow after Stop replaces the deny with a permit; the way back to the
// default is Remove rule on Permissions.
func TestDeviceWakeAllowAfterStopWritesAPermitForEveryApp(t *testing.T) {
	own(t)
	f := newWakeFixture(t)
	h := (&servicePanel{}).handler("test-key")
	set := func(program string, allow bool) string {
		t.Helper()
		r := panelRequest(t, h, "/card/wake", wakeEdit{Account: wakeFixtureAccount, Program: program, Allow: allow})
		return decodePanel[wakeEditResult](t, r.Code, r.Body.Bytes()).Outcome
	}
	for _, program := range []string{f.downloader, f.other, f.idle} {
		if got := set(program, false); got != "applied" {
			t.Fatalf("stop: %s", got)
		}
		if read := f.rule(t, program); read.Outcome != wire.RuleReadOutcomeFound || read.Record.Rule.Permit {
			t.Fatalf("stop wrote %+v, want a deny", read)
		}
		if got := set(program, true); got != "applied" {
			t.Fatalf("allow after stop: %s", got)
		}
		if read := f.rule(t, program); read.Outcome != wire.RuleReadOutcomeFound || !read.Record.Rule.Permit {
			t.Fatalf("allow after stop left %+v, want a permit", read)
		}
	}
}

// Without a runtime the wake list is unavailable with its reason, and no
// rule is read.
func TestDeviceWakeUnavailableWithoutARuntime(t *testing.T) {
	own(t)
	h := newTestPanel(t).handler("test-key")
	r := panelRequest(t, h, "/card/wake", nil)
	view := decodePanel[wakeView](t, r.Code, r.Body.Bytes())
	if view.Outcome != "unavailable" || view.Reason == "" || len(view.Apps) != 0 {
		t.Fatalf("wake view %+v", view)
	}
}

// joinWake merges a holder and its rule into one row, keeps two accounts of
// one program apart, and lists a rule nobody holds under it as its own row.
func TestJoinWakeMergesByAccountAndProgram(t *testing.T) {
	program := selfExe(t)
	holds := []cardAwakeHold{{Program: program, Account: "a", Since: "2026-10-02T09:00:00Z"}}
	rules := []wire.PolicyRule{
		{Subject: wire.Subject{Account: "a", Program: program}, Action: rights.AwakeAction, Resource: rights.AwakeResource, Permit: false},
		{Subject: wire.Subject{Account: "b", Program: program}, Action: rights.AwakeAction, Resource: rights.AwakeResource, Permit: true},
	}
	apps := joinWake(holds, nil, rules)
	if len(apps) != 2 || apps[0].Rule != "stopped" || !apps[0].Holding || apps[1].Account != "b" || apps[1].Rule != "allowed" || apps[1].Holding {
		t.Fatalf("joined %+v", apps)
	}
}

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
)

const panelRightsAction = "abstraction.storage/content.read"

// panelRightsRuntime hosts a real decision policy whose operator authorization
// admits this test process only while allow is set and outage is clear.
func panelRightsRuntime(t *testing.T, allow, outage *atomic.Bool) *rights.DecisionPolicy {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "policy.json"), []string{panelRightsAction})
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("panel-rights-%d", time.Now().UnixNano())
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		RightsPolicy: policy, RightsEndpoint: listen.Endpoint(prefix + "r"),
		RightsOperator: func(ctx context.Context, peer *identity.Peer) error {
			if outage.Load() {
				return errors.New("fixture operator decision unavailable")
			}
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() || !allow.Load() {
				return rightsservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)
	return policy
}

func TestServicePanelRightsOperator(t *testing.T) {
	own(t)
	var allow, outage atomic.Bool
	policy := panelRightsRuntime(t, &allow, &outage)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := wire.Subject{Account: "panel-target-account", Program: filepath.Clean(program)}
	h := (&servicePanel{}).handler("test-key")
	list := func() wire.PolicyPage {
		r := panelRequest(t, h, "/rights?cursor=", nil)
		return decodePanel[wire.PolicyPage](t, r.Code, r.Body.Bytes())
	}
	edit := func(e rightsEdit) wire.PolicyEdit {
		r := panelRequest(t, h, "/rights", e)
		return decodePanel[wire.PolicyEdit](t, r.Code, r.Body.Bytes())
	}
	grant := func(revision string, permit bool) rightsEdit {
		return rightsEdit{Edit: "set", Revision: revision, Account: target.Account, Program: target.Program, Action: panelRightsAction, Resource: "sha256:panel", Permit: permit}
	}
	decision := func() string { return policy.Decide(target, panelRightsAction, "sha256:panel").Outcome.String() }

	if page := list(); page.Outcome != wire.PolicyPageOutcomeForbidden || page.Revision != "" || len(page.Catalog) != 0 {
		t.Fatalf("unauthorized list %+v", page)
	}
	if e := edit(grant("any-revision", true)); e.Outcome != wire.PolicyEditOutcomeForbidden || e.Revision != "" || e.Current != nil {
		t.Fatalf("unauthorized grant %+v", e)
	}
	if got := decision(); got != "not_granted" {
		t.Fatalf("refused panel grant changed policy: %s", got)
	}

	allow.Store(true)
	empty := list()
	if empty.Outcome != wire.PolicyPageOutcomePage || !empty.Complete || len(empty.Rules) != 0 || len(empty.Catalog) != 1 || empty.Catalog[0] != panelRightsAction {
		t.Fatalf("authorized list %+v", empty)
	}
	granted := edit(grant(empty.Revision, true))
	if granted.Outcome != wire.PolicyEditOutcomeApplied || granted.Current == nil || !granted.Current.Permit || granted.Revision == empty.Revision {
		t.Fatalf("panel grant %+v", granted)
	}
	if got := decision(); got != "permitted" {
		t.Fatalf("panel grant decision %s", got)
	}
	if stale := edit(grant(empty.Revision, false)); stale.Outcome != wire.PolicyEditOutcomeConflict || stale.Revision != granted.Revision || stale.Current == nil || !stale.Current.Permit {
		t.Fatalf("stale panel deny %+v", stale)
	}
	page := list()
	if page.Outcome != wire.PolicyPageOutcomePage || page.Revision != granted.Revision || len(page.Rules) != 1 || page.Rules[0].Subject != target || !page.Rules[0].Permit {
		t.Fatalf("list after grant %+v", page)
	}
	denied := edit(grant(page.Revision, false))
	if denied.Outcome != wire.PolicyEditOutcomeApplied || denied.Current == nil || denied.Current.Permit {
		t.Fatalf("panel deny %+v", denied)
	}
	if got := decision(); got != "denied" {
		t.Fatalf("panel deny decision %s", got)
	}

	outage.Store(true)
	if page := list(); page.Outcome != wire.PolicyPageOutcomeUnavailable || len(page.Rules) != 0 {
		t.Fatalf("operator outage list %+v", page)
	}
	revoke := rightsEdit{Edit: "revoke", Revision: denied.Revision, Account: target.Account, Program: target.Program, Action: panelRightsAction, Resource: "sha256:panel"}
	if e := edit(revoke); e.Outcome != wire.PolicyEditOutcomeUnavailable || e.Revision != "" {
		t.Fatalf("operator outage revoke %+v", e)
	}
	if got := decision(); got != "denied" {
		t.Fatalf("unavailable revoke changed policy: %s", got)
	}
	outage.Store(false)
	if e := edit(revoke); e.Outcome != wire.PolicyEditOutcomeApplied || e.Current != nil {
		t.Fatalf("panel revoke %+v", e)
	}
	if got := decision(); got != "not_granted" {
		t.Fatalf("panel revoke decision %s", got)
	}
	if e := edit(rightsEdit{Edit: "set", Revision: denied.Revision, Account: target.Account, Program: target.Program, Action: "abstraction.unknown/action", Resource: "x", Permit: true}); e.Outcome != wire.PolicyEditOutcomeInvalid {
		t.Fatalf("uncatalogued panel grant %+v", e)
	}
	if page := list(); page.Outcome != wire.PolicyPageOutcomePage || len(page.Rules) != 0 {
		t.Fatalf("list after revoke %+v", page)
	}

	for _, bad := range []rightsEdit{
		{Edit: "grant", Revision: "r", Account: "a", Program: target.Program, Action: panelRightsAction, Resource: "x"},
		{Edit: "set", Revision: "r", Account: "a", Program: "relative/program", Action: panelRightsAction, Resource: "x"},
		{Edit: "set", Revision: "", Account: "a", Program: target.Program, Action: panelRightsAction, Resource: "x"},
		{Edit: "set", Revision: "r", Account: "bad\naccount", Program: target.Program, Action: panelRightsAction, Resource: "x"},
		{Edit: "revoke", Revision: "r", Account: "a", Program: target.Program, Action: panelRightsAction, Resource: "x", Permit: true},
	} {
		if r := panelRequest(t, h, "/rights", bad); r.Code != 400 {
			t.Fatalf("invalid rights edit %+v reached the service: %d %s", bad, r.Code, r.Body.Bytes())
		}
	}
}

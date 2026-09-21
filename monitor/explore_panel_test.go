package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	client "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/probe"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type exploreReply struct {
	Result probe.Result     `json:"result"`
	Rule   *exploreRuleView `json:"rule"`
}

// panelExploreRuntime gates config editing on a real decision policy. The test
// process is the enforcer, and the operator while operator is set.
func panelExploreRuntime(t *testing.T, operator *atomic.Bool) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "decisions.json"), rwire.ResourceActions)
	if err != nil {
		t.Fatal(err)
	}
	self := func(peer *identity.Peer) bool {
		process, err := peer.Process.AtLeast(listen.Program.Process)
		return err == nil && process.PID == os.Getpid()
	}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.RightsPolicy, o.RightsEndpoint = policy, o.ConfigEndpoint+"-rights"
		o.RightsOperator = func(_ context.Context, peer *identity.Peer) error {
			if !self(peer) || !operator.Load() {
				return rightsservice.ErrOperatorForbidden
			}
			return nil
		}
		o.RightsEnforcer = func(_ context.Context, peer *identity.Peer, _, _ string) bool { return self(peer) }
		o.ConfigEditPolicy = host.ConfigEditPolicyFromRights(rightsclient.New(o.RightsEndpoint))
	})
}

func TestPanelExploreGrantCallRevoke(t *testing.T) {
	own(t)
	var operator atomic.Bool
	operator.Store(true)
	panelExploreRuntime(t, &operator)
	h := newTestPanel(t).handler("test-key")
	exe, _ := os.Executable()

	r := panelRequest(t, h, "/explore", nil)
	catalogue := decodePanel[struct {
		Self   probe.Subject `json:"self"`
		Probes []probe.Probe `json:"probes"`
	}](t, r.Code, r.Body.Bytes())
	if !sameFile(catalogue.Self.Program, exe) || catalogue.Self.Account == "" {
		t.Fatalf("explore catalogue %+v", catalogue)
	}
	// The Panel lists the probes `openabstractions probe` runs: one shared list.
	shared := probe.List()
	if len(catalogue.Probes) != len(shared) {
		t.Fatalf("explore lists %d probes, the shared list %d", len(catalogue.Probes), len(shared))
	}
	for i, listed := range catalogue.Probes {
		want := shared[i]
		if listed.Capability != want.Capability || listed.Operation != want.Operation || listed.Contract != want.Contract ||
			listed.Argument != want.Argument || listed.Writes != want.Writes || listed.Note != want.Note || (listed.Rule == nil) != (want.Rule == nil) {
			t.Fatalf("explore probe %d %+v, shared %+v", i, listed, want)
		}
	}
	call := func(query string) exploreReply {
		t.Helper()
		r := panelRequest(t, h, "/explore?"+query, nil)
		return decodePanel[exploreReply](t, r.Code, r.Body.Bytes())
	}
	// A probe that writes is refused until the page confirms it.
	if r := panelRequest(t, h, "/explore?capability=config&operation=rewrite", nil); r.Code != 400 || !strings.Contains(r.Body.String(), "writes") {
		t.Fatalf("unconfirmed rewrite answered %d %s", r.Code, r.Body.String())
	}
	rewrite := "capability=config&operation=rewrite&confirm=write"

	before := call(rewrite)
	if before.Result.Outcome != "forbidden" || before.Result.Rule == nil || before.Result.Rule.Action != host.ConfigEditAction || before.Result.Subject != catalogue.Self {
		t.Fatalf("rewrite before a grant %+v", before.Result)
	}
	if before.Rule == nil || before.Rule.Outcome != "unknown" || before.Rule.Permit != nil || before.Rule.Revision == "" {
		t.Fatalf("rule before a grant %+v", before.Rule)
	}

	edit := func(e rightsEdit) {
		t.Helper()
		page := decodePanel[rwire.PolicyPage](t, 200, panelRequest(t, h, "/rights?cursor=", nil).Body.Bytes())
		e.Revision, e.Account, e.Program, e.Action, e.Resource = page.Revision, catalogue.Self.Account, catalogue.Self.Program, host.ConfigEditAction, host.ConfigEditResource
		r := panelRequest(t, h, "/rights", e)
		if applied := decodePanel[rwire.PolicyEdit](t, r.Code, r.Body.Bytes()); applied.Outcome != rwire.PolicyEditOutcomeApplied {
			t.Fatalf("rights %s %+v", e.Edit, applied)
		}
	}
	edit(rightsEdit{Edit: "set", Permit: true})
	after := call(rewrite)
	if after.Result.Outcome != "applied" || after.Rule == nil || after.Rule.Outcome != "found" || after.Rule.Permit == nil || !*after.Rule.Permit || after.Rule.SetBy == nil || !sameFile(after.Rule.SetBy.Program, exe) {
		t.Fatalf("rewrite after a grant %+v rule %+v", after.Result, after.Rule)
	}
	edit(rightsEdit{Edit: "revoke"})
	revoked := call(rewrite)
	if revoked.Result.Outcome != "forbidden" || revoked.Rule.Outcome != "unknown" {
		t.Fatalf("rewrite after a revoke %+v rule %+v", revoked.Result, revoked.Rule)
	}

	other := call(rewrite + "&program=" + url.QueryEscape(filepath.Join(filepath.Dir(exe), "other-program.exe")))
	if other.Rule == nil || other.Rule.Outcome != "unknown" || other.Rule.Subject.Program == catalogue.Self.Program || other.Result.Subject.Program != catalogue.Self.Program {
		t.Fatalf("rule for another subject %+v %+v", other.Result, other.Rule)
	}
	if read := call("capability=config&operation=read"); read.Result.Outcome != "read" || read.Rule != nil {
		t.Fatalf("config read %+v", read)
	}
	if decide := call("capability=rights&operation=decide&argument=" + url.QueryEscape(host.ConfigEditAction+" "+host.ConfigEditResource)); decide.Result.Outcome != "not_granted" || decide.Rule == nil || decide.Rule.Outcome != "unknown" {
		t.Fatalf("rights decide %+v %+v", decide.Result, decide.Rule)
	}
	if jobs := call("capability=jobs&operation=inventory"); jobs.Result.Resolution != "unavailable" {
		t.Fatalf("jobs without a job host %+v", jobs.Result)
	}

	operator.Store(false)
	if refused := call(rewrite); refused.Result.Outcome != "forbidden" || refused.Rule == nil || refused.Rule.Outcome != "forbidden" {
		t.Fatalf("rule read refused to a non-operator %+v", refused.Rule)
	}

	for _, bad := range []string{"capability=config&operation=delete", "capability=model&operation=resolve", "capability=config&operation=read&argument=x",
		"capability=config&operation=rewrite&program=relative", "capability=router&operation=pick&argument=" + url.QueryEscape("bad\nmodel")} {
		if r := panelRequest(t, h, "/explore?"+bad, nil); r.Code != 400 {
			t.Fatalf("invalid explore %s answered %d", bad, r.Code)
		}
	}
}

func TestPanelExploreWithNoRuntime(t *testing.T) {
	own(t)
	principal, _, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	absent := client.NewVerified(listen.Endpoint(fmt.Sprintf("absent-panel-explore-%d", time.Now().UnixNano())), listen.ServerExpectation{Principal: principal, Program: exe})
	previous := panelMachine
	panelMachine = func() *facade.Machine { return absent }
	t.Cleanup(func() { panelMachine = previous })
	h := newTestPanel(t).handler("test-key")
	for _, p := range probe.List() {
		query := url.Values{"capability": {p.Capability}, "operation": {p.Operation}}
		if p.Argument != "" {
			query.Set("argument", "ollama:llama3.2 x")
		}
		if p.Argument == "URL" {
			query.Set("argument", "http://127.0.0.1:9/probe")
		}
		if p.Writes {
			query.Set("confirm", "write")
		}
		r := panelRequest(t, h, "/explore?"+query.Encode(), nil)
		reply := decodePanel[exploreReply](t, r.Code, r.Body.Bytes())
		if reply.Result.Outcome != "runtime_unavailable" && reply.Result.Outcome != "invalid" {
			t.Fatalf("%s %s with no runtime %+v", p.Capability, p.Operation, reply.Result)
		}
		if reply.Rule != nil && reply.Rule.Outcome != "runtime_unavailable" {
			t.Fatalf("%s %s rule with no runtime %+v", p.Capability, p.Operation, reply.Rule)
		}
	}
}

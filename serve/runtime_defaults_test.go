package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-asks/go/scripthostcases"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// turnDefault turns one default of the runtime at endpoint through
// `openabstractions rights defaults`, as this test's operator program.
func turnDefault(t *testing.T, endpoint, name string, on bool) rightsDefaultsReply {
	t.Helper()
	flag := "--off"
	if on {
		flag = "--on"
	}
	var out bytes.Buffer
	err := rightsCommand([]string{"defaults", flag, name, "--endpoint", endpoint, "--json"}, &out, io.Discard)
	var reply rightsDefaultsReply
	if decodeErr := json.Unmarshal(out.Bytes(), &reply); err != nil || decodeErr != nil || reply.Outcome != "applied" || reply.Current == nil || reply.Current.On != on {
		t.Fatalf("rights defaults %s %s: %v %v %s", flag, name, err, decodeErr, out.String())
	}
	return reply
}

// rights RIGHTS-R22 through the shipped runtime: a second program with no rule
// of its own submits a download under the default; `rights defaults --off
// download` refuses it with not_granted; a Block refuses it while the default
// is on again, and removing the Block returns it to the default. Listing shows
// the five defaults.
func TestShippedRuntimeDownloadDefault(t *testing.T) {
	options := gatedRuntime(t)
	bin := probeBinary(t)
	payload := []byte("downloaded under the default\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(payload) }))
	defer server.Close()
	submit := []string{"jobs", "submit", server.URL + "/default.bin"}
	var out bytes.Buffer
	if err := rightsCommand([]string{"defaults", "--endpoint", options.endpoint, "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err, out.String())
	}
	var listed rightsDefaultsReply
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil || listed.Outcome != "page" || len(listed.Defaults) != 5 {
		t.Fatalf("defaults listing %v %s", err, out.String())
	}
	if accepted, _ := probeAs(t, bin, options, submit...); accepted.Outcome != "accepted" {
		t.Fatalf("submit under the download default %+v", accepted)
	}
	turnDefault(t, options.endpoint, rights.DefaultDownload, false)
	if refused, code := probeAs(t, bin, options, submit...); refused.Outcome != "forbidden" || code != exitRefusedCall {
		t.Fatalf("submit with the default off: exit %d %+v", code, refused)
	}
	if d, _ := probeAs(t, bin, options, "rights", "decide", host.JobSubmitAction, host.JobKindResource("download")); d.Outcome != "not_granted" {
		t.Fatalf("decision with the default off %+v", d)
	}
	turnDefault(t, options.endpoint, rights.DefaultDownload, true)
	block := []string{"--endpoint", options.endpoint, "--program", bin, "--action", host.JobSubmitAction, "--resource", host.JobKindResource("download")}
	if reply, err := runRightsCommand(t, append([]string{"grant", "--deny"}, block...)...); err != nil || reply.Outcome != "applied" {
		t.Fatalf("Block %v %+v", err, reply)
	}
	if refused, _ := probeAs(t, bin, options, submit...); refused.Outcome != "forbidden" {
		t.Fatalf("submit under a Block %+v", refused)
	}
	if reply, err := runRightsCommand(t, append([]string{"revoke"}, block...)...); err != nil || reply.Outcome != "applied" {
		t.Fatalf("remove the Block %v %+v", err, reply)
	}
	if accepted, _ := probeAs(t, bin, options, submit...); accepted.Outcome != "accepted" {
		t.Fatalf("submit after the Block was removed %+v", accepted)
	}
}

// defaultActions registers every action the runtime's defaults name.
var defaultActions = []string{rights.ActionApplicationRegisterSelf, rights.ActionApplicationAnnounce, rights.ActionQuestionAsk, grants.NotificationPostAction}

// rights RIGHTS-R22, VISION 2026-10-07: the composed runtime gives every
// program of its own account the five defaults; a script host and another
// account get none, a Block for one program refuses that program alone, and a
// switch turned off refuses everyone at the next decision.
func TestRuntimeAccountDefaults(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install(defaultActions, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	app := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "app.exe")}
	other := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "other.exe")}
	script := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "python.exe")}
	remote := rwire.Subject{Account: "remote-namespace", Program: app.Program}
	// PowerShell from the Microsoft Store is decided as its package family,
	// and a renamed copy of a signed host is named by its signature.
	scriptHosts := []rwire.Subject{script, {Account: r.owner, Program: "msix:Microsoft.PowerShell_8wekyb3d8bbwe"}}
	for _, c := range scripthostcases.Cases(t) {
		if c.Host && c.Evidence == (asks.Publisher{}) && strings.HasPrefix(c.Name, "a renamed copy of signed") {
			scriptHosts = append(scriptHosts, rwire.Subject{Account: r.owner, Program: c.Program})
		}
	}
	targets := []struct{ action, resource string }{
		{rights.ActionApplicationRegisterSelf, "account"},
		{rights.ActionQuestionAsk, "account"},
		{rights.ActionApplicationAnnounce, "app:editor"},
		{grants.NotificationPostAction, "account"},
		{rights.ActionJobSubmit, "job:download"},
	}
	for _, c := range targets {
		if got := r.decide(ctx, app, c.action, c.resource).Outcome; got != rwire.DecisionOutcomePermitted {
			t.Errorf("app %s on %s = %s, want permitted", c.action, c.resource, got)
		}
		for _, host := range scriptHosts {
			if got := r.decide(ctx, host, c.action, c.resource).Outcome; got != rwire.DecisionOutcomeNotGranted {
				t.Errorf("script host %s: %s on %s = %s, want not_granted", host.Program, c.action, c.resource, got)
			}
		}
		if got := r.decide(ctx, remote, c.action, c.resource).Outcome; got != rwire.DecisionOutcomeNotGranted {
			t.Errorf("remote %s on %s = %s, want not_granted", c.action, c.resource, got)
		}
	}
	if got := r.decide(ctx, app, rights.ActionJobSubmit, "job:inference").Outcome; got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("the download default covered another job kind", got)
	}
	if err := r.policy.Set(app, rights.ActionQuestionAsk, "account", false); err != nil {
		t.Fatal(err)
	}
	if got := r.decide(ctx, app, rights.ActionQuestionAsk, "account").Outcome; got != rwire.DecisionOutcomeDenied {
		t.Fatal("Block lost to the default", got)
	}
	if got := r.decide(ctx, other, rights.ActionQuestionAsk, "account").Outcome; got != rwire.DecisionOutcomePermitted {
		t.Fatal("one app's Block refused another", got)
	}
	revision, _, err := r.policy.DefaultsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	off, err := r.policy.SetDefault(revision, rights.DefaultAnnounce, false, r.self(), func() error { return nil })
	if err != nil || off.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(off, err)
	}
	if got := r.decide(ctx, other, rights.ActionApplicationAnnounce, "app:editor").Outcome; got != rwire.DecisionOutcomeNotGranted {
		t.Fatal("switched-off announce default", got)
	}
	if _, err := r.policy.SetDefault(off.Revision, rights.DefaultAnnounce, true, r.self(), func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if got := r.decide(ctx, other, rights.ActionApplicationAnnounce, "app:editor").Outcome; got != rwire.DecisionOutcomePermitted {
		t.Fatal("switched-on announce default", got)
	}
}

// APPLICATIONS.md: a self-registration lets each operator program read and
// start the entry, with why "registered by the app", and never overwrites a
// rule already there, a Block included.
func TestSelfRegistrationPermitsOperatorsWithoutOverwriting(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionApplicationRead, ActionApplicationActivate}, nil); err != nil {
		t.Fatal(err)
	}
	exe, panel := r.operators[0], r.operators[1]
	blocked := rwire.Subject{Account: r.owner, Program: panel}
	if err := r.policy.Set(blocked, ActionApplicationRead, "app:editor", false); err != nil {
		t.Fatal(err)
	}
	rules := []installationRule{{ActionApplicationRead, "app:editor"}, {ActionApplicationActivate, "app:editor"}}
	if err := r.permitOperators(rules, registeredSelfWhy); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		program, action string
		permit          bool
		why             string
	}{
		{exe, ActionApplicationRead, true, registeredSelfWhy},
		{exe, ActionApplicationActivate, true, registeredSelfWhy},
		{panel, ActionApplicationRead, false, ""},
		{panel, ActionApplicationActivate, true, registeredSelfWhy},
	} {
		read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: c.program}, c.action, "app:editor")
		if read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit != c.permit || read.Record.Why != c.why {
			t.Errorf("%s %s: %+v", filepath.Base(c.program), c.action, read)
		}
	}
}

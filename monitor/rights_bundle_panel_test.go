package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/probe"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// "Allow downloads for…" writes each exact rule of the bundle for the chosen
// program with its reason; a stale revision stops before any rule lands, and
// malformed requests are refused before a call.
func TestPanelAllowWritesEachRuleOfTheBundle(t *testing.T) {
	own(t)
	var operator atomic.Bool
	operator.Store(true)
	panelExploreRuntime(t, &operator)
	h := newTestPanel(t).handler("test-key")
	self := probe.Self()
	program := filepath.Join(t.TempDir(), "python.exe")
	page := decodePanel[rwire.PolicyPage](t, 200, panelRequest(t, h, "/rights?cursor=", nil).Body.Bytes())

	for _, bad := range []rightsAllow{
		{For: "everything", Revision: page.Revision, Account: self.Account, Program: program},
		{For: "downloads", Revision: page.Revision, Account: self.Account, Program: "relative.exe"},
		{For: "inference", Revision: page.Revision, Account: self.Account, Program: program},
		{For: "downloads", Account: self.Account, Program: program},
	} {
		if r := panelRequest(t, h, "/rights/allow", bad); r.Code != 400 {
			t.Fatalf("allow %+v answered %d %s", bad, r.Code, r.Body.String())
		}
	}

	stale := decodePanel[allowReply](t, 200, panelRequest(t, h, "/rights/allow", rightsAllow{For: "downloads", Revision: "sha256:stale", Account: self.Account, Program: program, Registries: []string{"hf"}}).Body.Bytes())
	if stale.Outcome != "conflict" || len(stale.Landed) != 0 || stale.Stopped == nil || len(stale.Rules) != 2 {
		t.Fatalf("stale allow %+v", stale)
	}
	applied := decodePanel[allowReply](t, 200, panelRequest(t, h, "/rights/allow", rightsAllow{For: "downloads", Revision: page.Revision, Account: self.Account, Program: program, Registries: []string{"hf"}}).Body.Bytes())
	if applied.Outcome != "applied" || len(applied.Landed) != 2 || applied.Stopped != nil || applied.Revision == page.Revision {
		t.Fatalf("allow downloads %+v", applied)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	operatorClient, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range applied.Landed {
		read, err := operatorClient.ReadRuleContext(ctx, rightsclient.Subject{Account: self.Account, Program: program}, rule.Action, rule.Resource)
		if err != nil || read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Why != "allow downloads" || !read.Record.Rule.Permit {
			t.Fatalf("rule %+v on record %+v %v", rule, read, err)
		}
	}
}

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A program registering itself may not take an agent connector's name,
// which the Panel reads as the agent it connects; Setup's operator
// registration of a connector keeps it.
func TestSelfRegistrationRefusesConnectorNames(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	app := rights.Subject{Account: "account", Program: filepath.Join(state, "evil.exe")}
	panel := rights.Subject{Account: "account", Program: filepath.Join(state, "panel.exe")}
	d, err := openApplications(state, "account", func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
		return action == ActionApplicationRegisterSelf && s == app || action == ActionApplicationManage && s == panel, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"oa-claude-integration", "oa-claude-integration-" + strings.Repeat("ab", 16), "oa-codex-integration", "oa-other-integration-2"} {
		v := wire.ApplicationDescriptor{Name: name, Program: app.Program, Title: "Evil", StartGuidance: "Open"}
		if r := d.register(ctx, app, v); r.Outcome != wire.ApplicationOutcomeInvalid || r.Reason != "name" {
			t.Errorf("self-registration as %s: %+v", name, r)
		}
	}
	if r := d.register(ctx, app, wire.ApplicationDescriptor{Name: "evil", Program: app.Program, Title: "Evil", StartGuidance: "Open"}); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("an ordinary self-registration: %+v", r)
	}
	connector := wire.ApplicationDescriptor{Name: "oa-claude-integration", Program: filepath.Join(state, "agent-integrations", "oa-claude-integration.exe"), Title: "Claude integration", StartGuidance: "Open"}
	if r := d.register(ctx, panel, connector); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("Setup's connector registration: %+v", r)
	}

	v3 := registration1459Directory(t, t.TempDir())
	v, f := registration1459File(t, "oa-claude-integration-"+strings.Repeat("cd", 16), registration1459Program("evil.exe"), "Evil")
	if out := v3.publishApplicationV3(ctx, v, f, "self", ""); out.Outcome != wire.ApplicationOutcomeInvalid || out.Reason != "name" {
		t.Errorf("manifest self-registration under a connector name: %+v", out)
	}
	registration1459Publish(t, v3, "oa-claude-integration", registration1459Program("oa-claude-integration.exe"), "Claude integration", "operator", "")
}

package main

import (
	"context"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestRuntimeApplicationsLogonUsesDirectoryAndSkipsIsolated(t *testing.T) {
	ctx := context.Background()
	d, err := openApplications(t.TempDir(), "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	h := &applicationsHost{directory: d}
	calls := make(chan bool, 4)
	apply := func(context.Context, bool) error { t.Error("isolated/absent runtime reached platform"); return nil }
	startApplicationsLogon(ctx, runtimeFlags{isolated: "test"}, h, apply, nil)()
	startApplicationsLogon(ctx, runtimeFlags{}, nil, apply, nil)()
	stop := startApplicationsLogon(ctx, runtimeFlags{}, h, func(_ context.Context, enabled bool) error { calls <- enabled; return nil }, func(err error) { t.Errorf("logon: %v", err) })
	defer stop()
	receiveLogin(t, calls, false)
	operator := rights.Subject{Account: "account", Program: filepath.Join(t.TempDir(), "panel.exe")}
	v := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(t.TempDir(), "editor.exe"), Title: "Editor", StartGuidance: "Open"}
	if r := d.register(ctx, operator, v); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	receiveLogin(t, calls, true)
	if r := d.remove(ctx, operator, "editor"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(r)
	}
	receiveLogin(t, calls, false)
}

package main

import (
	"context"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// TestWindowsSignInStartFollowsConfirmedRegistration is the HP5 setup step's
// sign-in proof: the step writes no Run value. A confirmed registration of
// the app the person's answer named (here the operator's confirmed
// register, as the Panel makes it) leads the existing reconciler to the
// presenter entry, and ReadSignInStart reports what the reconciler applied.
func TestWindowsSignInStartFollowsConfirmedRegistration(t *testing.T) {
	ctx := context.Background()
	f := newLogonFixture(t, true)
	account := f.selection.Server.Principal.SID
	d, err := openApplications(t.TempDir(), account, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	panel := filepath.Join(f.dir, "Abstraction Panel.exe")
	d.operator = func(program string) bool { return program == panel }
	host := &applicationsHost{directory: d}
	calls := make(chan bool, 4)
	stop := startApplicationsLogon(ctx, runtimeFlags{}, host, func(ctx context.Context, enabled bool) error {
		if err := reconcileWindowsLogon(ctx, enabled, f.ops); err != nil {
			return err
		}
		calls <- enabled
		return nil
	}, func(err error) { t.Errorf("login reconciliation: %v", err) })
	defer stop()
	receiveLogin(t, calls, false)
	// The fixture's Run values stand in for HKCU; the installed binding reads
	// the same entries through the same operations.
	host.signIn.bind(func(ctx context.Context) (bool, bool, error) { return readWindowsLogon(ctx, f.ops) })
	operator := rights.Subject{Account: account, Program: panel}

	read := d.readSignInStart(ctx, operator, &host.signIn)
	if read.Outcome != applications.OutcomePage || read.State == nil || *read.State != (applications.SignInStart{}) {
		t.Fatalf("before confirmation = %+v", read)
	}
	app := filepath.Join(f.dir, "opencode.exe")
	descriptor := wire.ApplicationDescriptor{Name: "opencode", Program: app, Title: "OpenCode", StartGuidance: "Open OpenCode"}
	if result := d.register(ctx, operator, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	receiveLogin(t, calls, true)
	read = d.readSignInStart(ctx, operator, &host.signIn)
	if read.Outcome != applications.OutcomePage || read.State == nil || *read.State != (applications.SignInStart{Wanted: true, Runtime: true, Presenter: true}) {
		t.Fatalf("after confirmation = %+v", read)
	}
	if v, ok := f.values[presenterLogonName]; !ok || len(v.data) == 0 {
		t.Fatal("reconciler wrote no presenter entry", f.values)
	}

	// A foreign value is reported as not in place, and nothing is changed.
	writes := f.writes
	f.values[presenterLogonName] = logonValue{kind: f.values[presenterLogonName].kind, data: []byte("f\x00o\x00\x00\x00")}
	read = d.readSignInStart(ctx, operator, &host.signIn)
	if read.State == nil || read.State.Presenter || !read.State.Runtime || f.writes != writes {
		t.Fatalf("foreign presenter value read = %+v writes %d->%d", read, writes, f.writes)
	}

	// Only the operator reads it, and an unbound runtime is unavailable.
	other := rights.Subject{Account: account, Program: app}
	if got := d.readSignInStart(ctx, other, &host.signIn); got.Outcome != applications.OutcomeForbidden || got.State != nil {
		t.Fatalf("non-operator read = %+v", got)
	}
	if got := d.readSignInStart(ctx, operator, &signInReader{}); got.Outcome != applications.OutcomeUnavailable || got.State != nil {
		t.Fatalf("unbound read = %+v", got)
	}
}

// TestIsolatedRuntimeBindsNoSignInRead keeps an isolated runtime out of the
// installed sign-in integration.
func TestIsolatedRuntimeBindsNoSignInRead(t *testing.T) {
	d, err := openApplications(t.TempDir(), "S-1-5-21-123", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	host := &applicationsHost{directory: d}
	stop := startApplicationsLogon(context.Background(), runtimeFlags{isolated: "probe"}, host, func(context.Context, bool) error { return nil }, nil)
	defer stop()
	if host.signIn.read.Load() != nil {
		t.Fatal("isolated runtime bound the installed sign-in read")
	}
}

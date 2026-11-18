package main

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// settleRegistrations moves every registrations file's write time a minute
// back, as on a runtime whose applications were registered earlier.
func settleRegistrations(t *testing.T, d *applicationDirectory) {
	t.Helper()
	entries, err := os.ReadDir(d.v3.dir)
	if err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Minute)
	for _, e := range entries {
		if err := os.Chtimes(filepath.Join(d.v3.dir, e.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
}

// countingRegistrationDirectory is registration1459Directory with the
// manifest decoder and the role decoder counted: readLocked decodes every
// retained manifest, and each fresh role projection decodes every file.
func countingRegistrationDirectory(t *testing.T, state string) (*applicationDirectory, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var manifests, roles atomic.Int64
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	decode := func(raw []byte, program string, held *heldApplicationManifest) (wire.ApplicationDescriptor, string, error) {
		manifests.Add(1)
		return registration1459Decoder(raw, program, held)
	}
	if err := d.attachApplicationV3(context.Background(), decode); err != nil {
		t.Fatal(err)
	}
	d.roleDecode = func([]byte, string) (bool, error) { roles.Add(1); return false, nil }
	d.operator = func(p string) bool { return p == "operator" }
	return d, &manifests, &roles
}

// The enrollment read every message progress pass and client call makes takes
// the descriptors CAS and a read-only registration transaction. An unlisted
// name keeps the count to readLocked's own decodes. With the
// registrations directory unchanged, those transactions reuse the state
// already decoded. A write by another owner, or by this one, is read again.
func TestIdleRegistrationReadsReuseDecodedState(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d, manifests, roles := countingRegistrationDirectory(t, state)
	for _, name := range []string{"alpha", "beta", "gamma"} {
		registration1459Publish(t, d, name, registration1459Program(name+".exe"), name, "operator", "")
	}
	settleRegistrations(t, d)

	manifests.Store(0)
	roles.Store(0)
	for range 20 {
		d.currentAgentEnrollment(ctx, "absent")
	}
	if got := manifests.Load(); got != 3 {
		t.Fatalf("20 idle enrollment reads decoded %d manifests, want one read of 3", got)
	}
	if got := roles.Load(); got != 3 {
		t.Fatalf("20 idle enrollment reads projected roles from %d files, want one projection of 3", got)
	}

	// Another owner on the same state registers an application.
	other := registration1459Directory(t, state)
	registration1459Publish(t, other, "delta", registration1459Program("delta.exe"), "delta", "operator", "")
	manifests.Store(0)
	d.currentAgentEnrollment(ctx, "absent")
	if got := manifests.Load(); got != 4 {
		t.Fatalf("read after another owner's write decoded %d manifests, want 4", got)
	}
	if !d.lock(ctx) {
		t.Fatal("lock")
	}
	_, listed := d.v3.entries["delta"]
	d.unlock()
	if !listed {
		t.Fatal("read-only transaction missed another owner's registration")
	}

	// This owner's own write transaction is never reused.
	settleRegistrations(t, d)
	d.currentAgentEnrollment(ctx, "absent")
	manifests.Store(0)
	if out := d.addDescriptor(ctx, wire.ApplicationDescriptor{Name: "legacy", Program: registration1459Program("legacy.exe"), Title: "Legacy"}, nil); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	writes := manifests.Load()
	if writes == 0 {
		t.Fatal("write transaction reused the read-only state")
	}
	d.currentAgentEnrollment(ctx, "absent")
	if got := manifests.Load() - writes; got != 4 {
		t.Fatalf("read after a write decoded %d manifests, want 4", got)
	}
}

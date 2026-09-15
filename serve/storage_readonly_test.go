package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func managedStore(t *testing.T) (state, root string) {
	t.Helper()
	state = t.TempDir()
	root = filepath.Join(state, "jobs")
	if _, err := acceptanceprovider.OpenManaged(root, downloadserve.HTTPExecution{}); err != nil {
		t.Fatal(err)
	}
	return state, root
}

func TestStorageReadOnlyWritesNothing(t *testing.T) {
	state, root := managedStore(t)
	before := storageTree(t, state)
	var out bytes.Buffer
	if err := storageCommand([]string{"check", "--state-dir", state, "--read-only"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, storageTree(t, state)) {
		t.Fatal("read-only check changed the store")
	}
	if _, err := os.Stat(filepath.Join(root, "acceptance", "host.lock")); !os.IsNotExist(err) {
		t.Fatalf("read-only check created the host guard: %v", err)
	}
	if !strings.Contains(out.String(), "(read-only)") || !strings.Contains(out.String(), "no host guard") {
		t.Fatalf("output does not say what it did not take: %q", out.String())
	}

	// The guarded check on the same store adds exactly its guard.
	if err := storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	after := storageTree(t, state)
	delete(after, filepath.Join("jobs", "acceptance", "host.lock"))
	if !reflect.DeepEqual(before, after) {
		t.Fatal("guarded check changed more than its guard")
	}
}

func TestStorageReadOnlyEmptyRootStaysEmpty(t *testing.T) {
	state := t.TempDir()
	if err := os.Mkdir(filepath.Join(state, "jobs"), 0700); err != nil {
		t.Fatal(err)
	}
	before := storageTree(t, state)
	if err := storageCommand([]string{"check", "--state-dir", state, "--read-only"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, storageTree(t, state)) {
		t.Fatal("read-only check wrote into an empty root")
	}
}

func TestStorageReadOnlyRefusesUnknownFeatureByName(t *testing.T) {
	state, root := managedStore(t)
	owner := filepath.Join(root, "acceptance", "owner.json")
	raw, err := os.ReadFile(owner)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	fields["Features"] = []string{"abstraction.job/journal-future@9"}
	if raw, err = json.Marshal(fields); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(owner, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before := storageTree(t, state)
	err = storageCommand([]string{"check", "--read-only", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, acceptanceprovider.ErrIncompatibleStorage) || !strings.Contains(err.Error(), "abstraction.job/journal-future@9") {
		t.Fatalf("unknown feature: %v", err)
	}
	if !reflect.DeepEqual(before, storageTree(t, state)) {
		t.Fatal("refusal changed the store")
	}
}

// Read-only mode takes no guard and probes the runtime's own: held, released
// and never created are three answers, and none of them writes.
func TestStorageReadOnlyReportsALiveHost(t *testing.T) {
	state, root := managedStore(t)
	// Windows refuses to read a locked byte, so the snapshot is taken with the
	// guard's file created and released.
	guard, err := acceptanceprovider.AcquireHost(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}
	before := storageTree(t, state)
	if guard, err = acceptanceprovider.AcquireHost(root); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = storageCommand([]string{"check", "--state-dir", state, "--read-only"}, &out, &bytes.Buffer{})
	if !errors.Is(err, acceptanceprovider.ErrHostActive) {
		guard.Close()
		t.Fatalf("read-only check against a held store: %v", err)
	}
	if !strings.Contains(out.String(), "metadata passed (read-only)") {
		guard.Close()
		t.Fatalf("held store output does not report its metadata: %q", out.String())
	}
	if err := storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{}); !errors.Is(err, acceptanceprovider.ErrHostActive) {
		guard.Close()
		t.Fatalf("guarded check against a held store: %v", err)
	}
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}

	out.Reset()
	if err := storageCommand([]string{"check", "--state-dir", state, "--read-only"}, &out, &bytes.Buffer{}); err != nil {
		t.Fatalf("read-only check against a released store: %v", err)
	}
	if !strings.Contains(out.String(), "no runtime held one when probed") {
		t.Fatalf("released store output: %q", out.String())
	}
	if !reflect.DeepEqual(before, storageTree(t, state)) {
		t.Fatal("read-only probe changed the store")
	}
	again, err := acceptanceprovider.AcquireHost(root)
	if err != nil {
		t.Fatal("read-only probe left the guard taken:", err)
	}
	again.Close()
}

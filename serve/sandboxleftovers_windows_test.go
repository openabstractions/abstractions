package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
)

func writeLeftover(t *testing.T, state, app string) string {
	t.Helper()
	dir := filepath.Join(state, "apps", app)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "sandbox.json")
	if err := os.WriteFile(manifest, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// TestRemoveLeftoverSandboxesReadsEveryAppManifest: each app's manifest goes
// to the kit's Remove once; one a session still runs in is left and named;
// another failure is returned after the rest are removed.
func TestRemoveLeftoverSandboxesReadsEveryAppManifest(t *testing.T) {
	state := t.TempDir()
	a := writeLeftover(t, state, "a")
	b := writeLeftover(t, state, "b")
	c := writeLeftover(t, state, "c")
	var called []string
	remove := func(manifest string, keepProfile bool) (*sandbox.Removal, error) {
		if keepProfile {
			t.Fatal("the profile is kept")
		}
		called = append(called, manifest)
		switch manifest {
		case b:
			return nil, sandbox.ErrInUse
		case c:
			return nil, errors.New("access list changed identity")
		}
		return &sandbox.Removal{Profile: "oa.app.a", Revoked: []string{"x", "y"}, ProfileDeleted: true}, nil
	}
	var out bytes.Buffer
	err := removeLeftoverSandboxes(state, &out, unelevated, remove)
	if err == nil || !strings.Contains(err.Error(), "access list changed identity") {
		t.Fatalf("error %v", err)
	}
	if strings.Join(called, "|") != strings.Join([]string{a, b, c}, "|") {
		t.Fatalf("removed %v", called)
	}
	if !strings.Contains(out.String(), a+": 2 grants revoked, profile oa.app.a deleted") || !strings.Contains(out.String(), b+": a session still runs") {
		t.Fatalf("output %q", out.String())
	}
	out.Reset()
	if err := removeLeftoverSandboxes(t.TempDir(), &out, unelevated, remove); err != nil || !strings.Contains(out.String(), "no sandbox left") {
		t.Fatalf("empty state: %v %q", err, out.String())
	}
}

func unelevated() bool { return false }

// TestRemoveLeftoverSandboxesRefusesAnElevatedToken: under an elevated token
// the command prints one line and reads no manifest, so a forged tree entry
// can never be revoked with an administrator's rights.
func TestRemoveLeftoverSandboxesRefusesAnElevatedToken(t *testing.T) {
	state := t.TempDir()
	writeLeftover(t, state, "forged")
	remove := func(string, bool) (*sandbox.Removal, error) {
		t.Fatal("an elevated run read a manifest")
		return nil, nil
	}
	var out bytes.Buffer
	if err := removeLeftoverSandboxes(state, &out, func() bool { return true }, remove); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Count(got, "\n") != 1 || !strings.Contains(got, "refused with an elevated token") {
		t.Fatalf("output %q", got)
	}
}

// TestRemoveLeftoverSandboxesRestoresACrashedSession: a sandbox a session
// recorded and never committed, as a crash leaves it, is taken back whole.
func TestRemoveLeftoverSandboxesRestoresACrashedSession(t *testing.T) {
	state, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(state, "apps", "leftover")
	project := filepath.Join(state, "project")
	for _, d := range []string{dir, project} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := sandbox.ApplicationProfileName("leftover-test", strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.DeleteProfile(profile) })
	setup, err := sandbox.Begin(filepath.Join(dir, "sandbox.json"), profile)
	if err != nil {
		t.Fatal(err)
	}
	sid := setup.SID()
	err = setup.Grant(sandbox.Grant{Path: project, Access: sandbox.Modify})
	setup.Close()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := removeLeftoverSandboxes(state, &out, unelevated, sandbox.Remove); err != nil {
		t.Fatalf("%v: %s", err, out.String())
	}
	if _, err := os.Lstat(filepath.Join(dir, "sandbox.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the manifest is left: %v", err)
	}
	if holders, err := sandbox.Holders(project, sid); err != nil || len(holders) > 0 {
		t.Fatalf("the project still names the container: %v %v", holders, err)
	}
	if sandbox.ProfileExists(sid) {
		t.Fatal("the profile is left")
	}
}

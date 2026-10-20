//go:build windows

package main

import (
	"path/filepath"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestRegistryV3NativeMissingImageRefusesBeforePublication(t *testing.T) {
	r, caller := registryObserverFixture(t)
	d := registration1459Directory(t, t.TempDir())
	r.operator.credentials.runtimeRights.applications = d
	program := filepath.Join(t.TempDir(), "missing.exe")
	candidate := registry3Candidate(t, "untrusted")
	candidate.Spec.Program = program
	out := r.registerProgram(t.Context(), caller, "", true, candidate.Spec, fileid.Self)
	if out.Outcome != registration.OutcomeForbidden || out.Reason != "identity:probe" || out.Refusal == nil {
		t.Fatal("untrusted image admitted", out)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || len(rows) != 0 || len(d.programSelfEntries()) != 0 {
		t.Fatal("refusal published image or Keep", rows, err)
	}
}

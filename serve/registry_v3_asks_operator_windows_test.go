package main

import (
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// A program registration another program makes asks nothing: it waits pending
// until the person reviews it in the Panel (VISION 2026-10-06, amended
// 2026-11-10). The Panel's review, an operator's Replace, asks the person to
// keep it.
func TestRegistryV3RegistrationAsksOnlyOnAnOperatorsAction(t *testing.T) {
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	rights := r.operator.credentials.runtimeRights
	saved := rights.operators
	defer func() { rights.operators = saved }()
	rights.operators = nil
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	if _, _, mark, ok := run.d.programSelfEntry(record.Key); !ok || mark.Question != "" {
		t.Fatal("another program's registration asked", ok, mark)
	}
	run.k.resume()
	if _, _, mark, ok := run.d.programSelfEntry(record.Key); !ok || mark.Question != "" {
		t.Fatal("a restart asked about an unreviewed program", ok, mark)
	}
	rights.operators = []string{caller.Program}
	out := r.registerProgram(run.ctx, caller, record.Revision, false, record.Spec, fileid.Self)
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal("the Panel's review", out)
	}
	if _, _, mark, ok := run.d.programSelfEntry(record.Key); !ok || mark.Question == "" {
		t.Fatal("the Panel's review asked nothing", ok, mark)
	}
}

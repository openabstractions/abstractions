//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Owner-hit on R27 (2026-11-11): Setup re-pinned ComfyUI from cu\0.8.0-r17
// to cu\0.8.0-r26, which moves the entry's program through the production
// message reservation. The registry index kept the entry's facet on the
// earlier program's record as well, and the entry could take that record's
// revision. The reservation's finish then refused ("unavailable:
// message_binding"), and the applications@3 listing refused the mismatched
// record, so the Panel could not Remove the entry either. Any entry whose
// program changes took this path. These tests run it with the runtime's own
// message permits, on manifests that carry acceptance times and therefore
// program records, as the owner's do.

// repinMessageRun is newUnsignedRun with Setup as the operator and the
// runtime's message permits composed: every program change runs the real
// reservation and its Rights invalidation.
func repinMessageRun(t *testing.T) *unsignedRun {
	t.Helper()
	run := newUnsignedRun(t, "ComfyUI")
	r := run.f.a.rights
	if err := r.policy.RegisterAction(ActionApplicationMessage); err != nil {
		t.Fatal(err)
	}
	// Retained state may name the app from before; no re-pin carries it.
	if err := r.policy.Set(run.f.answerer, ActionApplicationMessage, "app:comfyui", true); err != nil {
		t.Fatal(err)
	}
	r.applications = run.d
	run.f.a.keepAgentEnrollments(run.ctx, run.d)
	if err := r.configureMessagePermits(run.d); err != nil {
		t.Fatal(err)
	}
	run.d.operator = func(program string) bool { return identity.SameSubjectProgram(program, run.f.panel.Program) }
	return run
}

// comfyHolders lists the program records that hold the comfyui facet, with
// their revisions.
func comfyHolders(run *unsignedRun) map[string]string {
	run.d.mu.Lock()
	defer run.d.mu.Unlock()
	out := map[string]string{}
	for key, v := range run.d.v3.programs {
		for _, a := range v.Spec.Applications {
			if a.Name == "comfyui" {
				out[key] = v.Revision
			}
		}
	}
	return out
}

func requireOneHolder(t *testing.T, run *unsignedRun, program string) {
	t.Helper()
	e, f, ok := run.entry()
	holders := comfyHolders(run)
	if !ok || len(holders) != 1 || holders[identity.NormalizeSubjectProgram(program)] != e.Revision || !identity.SameSubjectProgram(f.Source.Program, program) {
		t.Fatalf("the entry is not on %s alone: %+v %v", program, e, holders)
	}
}

func repinUpdate(t *testing.T, run *unsignedRun, version string) string {
	t.Helper()
	updated := filepath.Join(filepath.Dir(filepath.Dir(run.program)), version, "launch-helper.exe")
	if err := os.MkdirAll(filepath.Dir(updated), 0o700); err != nil {
		t.Fatal(err)
	}
	writeUnsignedImage(t, updated, "ComfyUI", true)
	return updated
}

func requireNoComfyMessagePermit(t *testing.T, run *unsignedRun) {
	t.Helper()
	if got := run.f.a.rights.policy.Decide(run.f.answerer, ActionApplicationMessage, "app:comfyui"); got.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("a message permit followed the entry to its new program", got)
	}
}

func TestSetupRepinThroughTheMessageReservationMovesTheProgramRecord(t *testing.T) {
	run := repinMessageRun(t)
	run.register(t)
	kept := run.keep(t)
	requireOneHolder(t, run, run.program)
	updated := repinUpdate(t, run, "0.8.0-r26")

	out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, kept.Revision)
	if out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Setup's re-pin:", out)
	}
	if e, _, _ := run.entry(); e.State != "enabled" || e.Source != "self" || e.Question != "" || e.Revision != out.Revision {
		t.Fatalf("the re-pinned entry: %+v", e)
	}
	requireOneHolder(t, run, updated)
	requireNoComfyMessagePermit(t, run)

	// The next update re-pins again under the revision Setup reads.
	again := repinUpdate(t, run, "0.8.0-r27")
	e, _, _ := run.entry()
	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, again, e.Revision); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("the second re-pin:", out)
	}
	requireOneHolder(t, run, again)

	// The person's Remove of the app entry applies.
	e, _, _ = run.entry()
	if out := run.d.changeApplicationV3(run.ctx, run.f.panel, "comfyui", e.Revision, "remove"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Remove:", out)
	}
	if _, _, ok := run.entry(); ok || len(comfyHolders(run)) != 0 {
		t.Fatal("Remove left the entry or a program record", comfyHolders(run))
	}
}

// stuckRepin leaves the state a failed re-pin finish leaves: the entry on the
// update's program, reserved ("removing") under its message question, here
// because the Rights invalidation's reply was lost.
func stuckRepin(t *testing.T) (*unsignedRun, string) {
	t.Helper()
	run := repinMessageRun(t)
	run.register(t)
	kept := run.keep(t)
	updated := repinUpdate(t, run, "0.8.0-r26")
	block := run.d.messageBlock
	run.d.messageBlock = func(ctx context.Context, name string) error {
		if err := block(ctx, name); err != nil {
			return err
		}
		return errors.New("lost invalidation reply")
	}
	if out := run.d.registerApplicationV3(run.ctx, run.f.panel, fileid.Self, updated, kept.Revision); out.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal("the interrupted re-pin:", out)
	}
	// The next runtime start reads the stuck state.
	run.restart(t)
	run.f.a.rights.applications = run.d
	run.d.operator = func(program string) bool { return identity.SameSubjectProgram(program, run.f.panel.Program) }
	run.f.a.keepAgentEnrollments(run.ctx, run.d)
	run.d.messageBlock = block
	e, f, _ := run.entry()
	if e.State != "removing" || !identity.SameSubjectProgram(f.Source.Program, updated) {
		t.Fatalf("not the stuck state: %+v %s", e, f.Source.Program)
	}
	return run, updated
}

// A person's Remove applies to an entry a failed re-pin left reserved.
func TestPersonRemovesAStuckRepin(t *testing.T) {
	run, updated := stuckRepin(t)
	// The listing reads one record, under the revision Remove names.
	requireOneHolder(t, run, updated)
	e, _, _ := run.entry()
	if out := run.d.changeApplicationV3(run.ctx, run.f.panel, "comfyui", e.Revision, "remove"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("Remove:", out)
	}
	if _, _, ok := run.entry(); ok || len(comfyHolders(run)) != 0 {
		t.Fatal("Remove left the entry or a program record", comfyHolders(run))
	}
}

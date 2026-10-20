package main

import (
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// An operator declaration whose program was deleted reads as Off: its
// registration's verification is missing, which projects it as disabled.
// Withdraw removes it all the same; the program is gone, and a withdrawal
// needs nothing from it (owner report R20c, comfy-image-1828, 2026-11-10).
func TestRegistryWithdrawOperatorDeclarationWithMissingProgram(t *testing.T) {
	p, d, _ := registry3LegacyFixture(t)
	ctx := t.Context()
	file := registry3LegacyFile("comfy-image-1828", filepath.Join(t.TempDir(), "gone", "pytorchd.exe"))
	file.DeclaredBy = `C:\Program Files\OpenAbstractions\tools\openabstractions.exe`
	registry3WriteLegacy(t, p.dir, file)
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _, _, _, _, err := d.programRegistrations(ctx)
	if err != nil || len(rows) != 1 || rows[0].Source != registration.SourceOperator || rows[0].Verification != registration.VerificationMissing {
		t.Fatal("fixture is not an adopted operator declaration with a missing program", rows, err)
	}
	files, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	if i := slicesIndexProvider(files, "comfy-image-1828"); i < 0 || !files[i].Disabled || files[i].Source != declarationSourceOperator {
		t.Fatal("the missing program's declaration does not read as an inert operator declaration", files)
	}
	if out := p.removeAdmitted(ctx, revision, "comfy-image-1828", nil); out.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("withdraw of a declaration whose program is missing: %v %q", out.Outcome, out.Reason)
	}
	// Reload through the strict store: the removal holds and the original
	// declaration file does not come back as a new input.
	d = registration1459Directory(t, filepath.Dir(d.dir))
	p.registrationDirectory = func() *applicationDirectory { return d }
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, _, _, _, _, err := d.programRegistrations(ctx); err != nil || len(rows) != 0 {
		t.Fatal("withdrawn registration remains", rows, err)
	}
	if files, _, err := p.readCached(); err != nil || slicesIndexProvider(files, "comfy-image-1828") >= 0 {
		t.Fatal("withdrawn declaration still listed", files, err)
	}
	if list, _, err := p.list(); err != nil || len(list) != 0 {
		t.Fatal("withdrawn declaration still on the Registry", list, err)
	}
}

// A name that is not declared at all still answers unknown.
func TestRegistryWithdrawUndeclaredNameStaysUnknown(t *testing.T) {
	p, _, program := registry3LegacyFixture(t)
	ctx := t.Context()
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("present", program))
	if err := p.adoptProgramRegistrations(ctx); err != nil {
		t.Fatal(err)
	}
	_, revision, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	if out := p.removeAdmitted(ctx, revision, "absent", nil); out.Outcome != wire.DeclarationEditOutcomeUnknown {
		t.Fatalf("withdraw of an undeclared name: %v %q", out.Outcome, out.Reason)
	}
}

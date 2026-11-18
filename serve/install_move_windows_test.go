//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// movedInstall lays out a per-machine tools folder and a per-user one, both
// holding copies of this test's executable as OA's runtime and two tools,
// and makes them this test's OA install folders, with every file in the
// per-user folder signed by the runtime's signer. It returns both folders
// and the shipped-image record of the per-user tools.
func movedInstall(t *testing.T) (machine, user, record string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	machine = filepath.Join(t.TempDir(), "Program Files", "OpenAbstractions", "tools")
	user = filepath.Join(t.TempDir(), "Programs", "OpenAbstractions", "tools")
	digest := sha256.Sum256(data)
	images := map[string]string{}
	for _, dir := range []string{machine, user} {
		if err := os.MkdirAll(filepath.Join(dir, installationDeclarationsDir), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"openabstractions.exe", "provider.exe", "picture.exe", "inventory.exe"} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o700); err != nil {
				t.Fatal(err)
			}
			images[name] = hex.EncodeToString(digest[:])
		}
	}
	savedMachine, savedUser, savedSigner := standingInstallFolders, standingUserInstallFolder, standingSameSigner
	t.Cleanup(func() {
		standingInstallFolders, standingUserInstallFolder, standingSameSigner = savedMachine, savedUser, savedSigner
	})
	standingInstallFolders = func() []string { return []string{machine} }
	standingUserInstallFolder = func() string { return user }
	standingSameSigner = func(program string) bool { return filepath.Dir(program) == user }
	return machine, user, shippedTestRecord(images)
}

// removeInstall deletes a tools folder the way an uninstall does.
func removeInstall(t *testing.T, tools string) {
	t.Helper()
	if err := os.RemoveAll(tools); err != nil {
		t.Fatal(err)
	}
}

// The owner's 2026-11-22 move from Program Files to the per-user folder, with
// the three records the owner's PC held:
//   - modelhost, a registration with the person's Keep and no declaration
//     file, re-keys to the per-user tool and keeps that Keep;
//   - pytorch-image, a declaration Setup wrote with OA tool paths in its
//     arguments, is rewritten on disk and re-keyed;
//   - local-stores, an installation declaration on inventory.exe, joins the
//     per-user inventory.exe registration Setup already made for
//     oa-setup-picture, which stays served as it was, with no name conflict.
//
// After the per-machine folder is removed and the runtime starts from the
// per-user folder, every one serves at the per-user tools and each repair
// writes one log line.
func TestInstallMoveRepairsDeclarationsAndRegistrations(t *testing.T) {
	machine, user, record := movedInstall(t)
	p, d, _ := registry3LegacyFixture(t)
	var logged []string
	p.report = func(err error) { logged = append(logged, err.Error()) }
	p.installation = filepath.Join(machine, installationDeclarationsDir)

	registry3WriteLegacy(t, p.dir, registry3LegacyFile("modelhost", filepath.Join(machine, "provider.exe")))
	pytorch := registry3LegacyFile("pytorch-image", filepath.Join(machine, "picture.exe"))
	pytorch.Declaration.Arguments = []string{"serve", "--runtime-program", filepath.Join(machine, "openabstractions.exe"), "--runtime=" + filepath.Join(machine, "openabstractions.exe")}
	pytorch.DeclaredBy = filepath.Join(machine, "openabstractions.exe")
	registry3WriteLegacy(t, p.dir, pytorch)
	registry3WriteLegacy(t, p.dir, registry3LegacyFile("oa-setup-picture", filepath.Join(user, "inventory.exe")))
	shipped := registry3LegacyFile("local-stores", "inventory.exe")
	for _, dir := range []string{machine, user} {
		registry3WriteLegacyRaw(t, filepath.Join(dir, installationDeclarationsDir), shipped)
	}
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	if rows := registry3Programs(t, d); len(rows) != 4 {
		t.Fatalf("per-machine adoption: %v", rows)
	}
	// modelhost now lives only as a registration, kept by the person.
	if err := os.Remove(filepath.Join(p.dir, "modelhost.json")); err != nil {
		t.Fatal(err)
	}
	oldModelhost := identity.NormalizeSubjectProgram(filepath.Join(machine, "provider.exe"))
	if out := d.editProgramRegistration(t.Context(), func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		s.programApprovals[oldModelhost] = registrationProgramApproval{KeptGeneration: s.programs[oldModelhost].Epoch, ServedBy: filepath.Join(machine, "openabstractions.exe")}
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	}); out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	before := servedDeclarations(t, p)

	removeInstall(t, machine)
	p.installation = filepath.Join(user, installationDeclarationsDir)
	p.observeShippedImage = func(ctx context.Context, program string, prior *applicationV3Source) (registration.Verification, *applicationV3Source, func() error, func() error) {
		return observeShippedRegistrationImageAt(ctx, program, prior, filepath.Join(user, "openabstractions.exe"), record)
	}
	logged = nil
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(p.dir, "pytorch-image.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk providerFile
	if err := json.Unmarshal(raw, &onDisk); err != nil {
		t.Fatal(err)
	}
	runtimeProgram := filepath.Join(user, "openabstractions.exe")
	if onDisk.Declaration.Program != filepath.Join(user, "picture.exe") || onDisk.DeclaredBy != runtimeProgram ||
		onDisk.Declaration.Arguments[2] != runtimeProgram || onDisk.Declaration.Arguments[3] != "--runtime="+runtimeProgram {
		t.Fatalf("operator declaration not repaired: %+v", onDisk)
	}
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil || len(rows) != 3 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if !strings.HasPrefix(row.Spec.Program, user) || row.Key != identity.NormalizeSubjectProgram(row.Spec.Program) {
			t.Fatalf("registration not moved: %s %s", row.Key, row.Spec.Program)
		}
		if row.Verification != registration.VerificationVerified && row.Verification != registration.VerificationUnverified {
			t.Fatalf("moved image not proven: %s %v %s", row.Spec.Program, row.Verification, row.VerificationReason)
		}
		if row.State != registration.StateEnabled {
			t.Fatalf("moved registration not enabled: %s %v", row.Spec.Program, row.State)
		}
	}
	modelhost := identity.NormalizeSubjectProgram(filepath.Join(user, "provider.exe"))
	if approval := d.v3.programApprovals[modelhost]; approval.KeptGeneration == "" || approval.KeptGeneration != d.v3.programs[modelhost].Epoch || approval.ServedBy != runtimeProgram {
		t.Fatalf("the person's Keep did not move with modelhost: %+v epoch %s", approval, d.v3.programs[modelhost].Epoch)
	}
	if _, stayed := d.v3.programApprovals[oldModelhost]; stayed {
		t.Fatal("the removed folder's approval stayed")
	}
	after := servedDeclarations(t, p)
	for name, program := range map[string]string{"modelhost": "provider.exe", "pytorch-image": "picture.exe", "local-stores": "inventory.exe", "oa-setup-picture": "inventory.exe"} {
		if after[name].Declaration.Program != filepath.Join(user, program) {
			t.Fatalf("%s serves %q", name, after[name].Declaration.Program)
		}
	}
	if !reflect.DeepEqual(before["oa-setup-picture"].Declaration, after["oa-setup-picture"].Declaration) {
		t.Fatalf("Setup's per-user registration changed: %+v -> %+v", before["oa-setup-picture"].Declaration, after["oa-setup-picture"].Declaration)
	}
	repairs := 0
	for _, line := range logged {
		if strings.Contains(line, "installation moved; repaired") || strings.Contains(line, "installation moved; joined") {
			repairs++
		}
		if strings.Contains(line, "precedence") || strings.Contains(line, "facet:name") {
			t.Fatalf("name conflict after the move: %q", logged)
		}
	}
	if repairs != 4 {
		t.Fatalf("want one line for the declaration file and each of three registrations, got %q", logged)
	}

	logged = nil
	if err := p.adoptProgramRegistrations(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, line := range logged {
		if strings.Contains(line, "installation moved") {
			t.Fatalf("repeat start repaired again: %q", logged)
		}
	}
}

// servedDeclarations is each declaration the runtime serves by name; every
// one must be enabled with its program present.
func servedDeclarations(t *testing.T, p *runtimeProviders) map[string]providerFile {
	t.Helper()
	files, _, err := p.readCached()
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]providerFile{}
	for _, f := range servingView(files) {
		if f.Disabled || programImageMissing(f.Declaration.Program) {
			t.Fatalf("%s does not serve: disabled %v (%s), program %s", f.Declaration.Name, f.Disabled, f.disabledWhy, f.Declaration.Program)
		}
		served[f.Declaration.Name] = f
	}
	return served
}

// Only a missing OA tool moves, only into the running installation's tools
// folder, and only to a file there with product standing.
func TestInstallMoveRule(t *testing.T) {
	machine, user, _ := movedInstall(t)
	p := &runtimeProviders{installation: filepath.Join(user, installationDeclarationsDir)}
	m, ok := p.installMove()
	if !ok {
		t.Fatal("per-user runtime has no repair rule")
	}
	gone := filepath.Join(machine, "provider.exe")
	if _, ok := m.path(gone); ok {
		t.Fatal("a tool that still exists moved")
	}
	removeInstall(t, machine)
	if moved, ok := m.path(gone); !ok || moved != filepath.Join(user, "provider.exe") {
		t.Fatal("removed tool did not move", moved)
	}
	if _, ok := m.path(filepath.Join(machine, "absent-from-user.exe")); ok {
		t.Fatal("a name the running tools folder lacks moved")
	}
	elsewhere := filepath.Join(t.TempDir(), "tools", "provider.exe")
	if _, ok := m.path(elsewhere); ok {
		t.Fatal("a program outside OA's install folders moved")
	}
	standingSameSigner = func(string) bool { return false }
	if _, ok := m.path(gone); ok {
		t.Fatal("moved into a per-user file without the runtime's signer")
	}
	dev := &runtimeProviders{installation: filepath.Join(t.TempDir(), installationDeclarationsDir)}
	if _, ok := dev.installMove(); ok {
		t.Fatal("a runtime outside OA's install folders repairs records")
	}
}

// A rule the person or Setup gave an OA tool at the removed folder holds at
// the moved tool; a rule at the moved tool and other programs' rules stay.
func TestInstallMoveCarriesRightsRules(t *testing.T) {
	machine, user, _ := movedInstall(t)
	r := testRights(t, t.TempDir())
	if err := r.policy.RegisterAction(ActionInventoryProvide); err != nil {
		t.Fatal(err)
	}
	old := rwire.Subject{Account: r.owner, Program: filepath.Join(machine, "inventory.exe")}
	set := func(s rwire.Subject, resource string, permit bool) {
		t.Helper()
		if err := r.policy.Set(s, ActionInventoryProvide, resource, permit); err != nil {
			t.Fatal(err)
		}
	}
	set(old, "store:chat", true)
	set(old, "store:denied", false)
	set(old, "store:kept", true)
	moved := rwire.Subject{Account: r.owner, Program: filepath.Join(user, "inventory.exe")}
	set(moved, "store:kept", false)
	other := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "tools", "inventory.exe")}
	set(other, "store:other", true)
	removeInstall(t, machine)

	m, ok := (&runtimeProviders{installation: filepath.Join(user, installationDeclarationsDir)}).installMove()
	if !ok {
		t.Fatal("no repair rule")
	}
	var logged []string
	r.repairMovedInstall(m, func(err error) { logged = append(logged, err.Error()) })
	decide := func(s rwire.Subject, resource string) rwire.DecisionOutcome {
		return r.policy.Decide(s, ActionInventoryProvide, resource).Outcome
	}
	if decide(moved, "store:chat") != rwire.DecisionOutcomePermitted || decide(moved, "store:denied") != rwire.DecisionOutcomeDenied {
		t.Fatal("rules not carried to the moved tool", logged)
	}
	if decide(moved, "store:kept") != rwire.DecisionOutcomeDenied {
		t.Fatal("an existing rule at the moved tool was replaced")
	}
	movedOther := rwire.Subject{Account: r.owner, Program: filepath.Join(user, "inventory.exe")}
	if decide(movedOther, "store:other") == rwire.DecisionOutcomePermitted {
		t.Fatal("a program outside OA's folders lent its rule")
	}
	if len(logged) != 2 {
		t.Fatalf("want one line per carried rule: %q", logged)
	}
	logged = nil
	r.repairMovedInstall(m, func(err error) { logged = append(logged, err.Error()) })
	for _, line := range logged {
		if strings.Contains(line, "carried") {
			t.Fatalf("repeat carried again: %q", logged)
		}
	}
}

// registry3WriteLegacyRaw writes an installation declaration with a bare
// program name, which only the installation reading resolves.
func registry3WriteLegacyRaw(t *testing.T, dir string, file providerFile) {
	t.Helper()
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file.Declaration.Name+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func registry3Programs(t *testing.T, d *applicationDirectory) []string {
	t.Helper()
	rows, _, _, _, _, err := d.programRegistrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Spec.Program)
	}
	return out
}

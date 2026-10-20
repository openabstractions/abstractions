package setup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/supervision"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/placetest"
)

func TestNativeArchiveShortPlacementKeepsVersionAndLegacyAssociation(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	_, a, _, _ := nativeArchiveFixture(t)
	home, err := CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	first, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if first != filepath.Join(home.Data, "OA", "a", "cu", a.Version) {
		t.Fatal("native placement differs", first)
	}
	next := a
	next.Version = "0.8.1"
	second, err := installPath(next)
	if err != nil || first == second || filepath.Dir(first) != filepath.Dir(second) {
		t.Fatal("version isolation lost", second, err)
	}
	legacy := lemonadeArtifact(t)
	legacy.InstallIdentity = "comfyui"
	old, err := installPath(legacy)
	if err != nil || old != filepath.Join(home.Data, "OpenAbstractions", "apps", "comfyui", legacy.Version) {
		t.Fatal("legacy placement changed", old, err)
	}
	// Repeated receipt/removal lookup resolves the same canonical version root.
	again, err := installPath(a)
	if err != nil || again != first {
		t.Fatal("placement association changed", again, err)
	}
}

func TestNativeArchiveCompleteMemberBudgetUsesUTF16AndCanonicalRoot(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "future-version", "oa-backend")
	pending, err := fileid.Pending(ctx, fileid.Self, root)
	if err != nil {
		t.Fatal(err)
	}
	room := supervision.MaxLibraryPath - supervision.UTF16Len(pending.Path()) - 1
	if room < 4 {
		t.Fatal("fixture root leaves no member budget")
	}
	for _, prefix := range []string{"", "界", "🦊"} {
		member := prefix + strings.Repeat("x", room-supervision.UTF16Len(prefix))
		if err := nativeArchivePathBudget(ctx, root, []archiveFileRecord{{Path: "manifest.json"}, {Path: member}}); err != nil {
			t.Fatal("259 refused", err)
		}
		if err := nativeArchivePathBudget(ctx, root, []archiveFileRecord{{Path: member + "x"}}); err == nil {
			t.Fatal("260 accepted")
		}
	}
	// The longest actual selected M member has 197 UTF-16 units. Pair's
	// independently pinned complete61241 inventory establishes this maximum.
	if supervision.LongestOpen(strings.Repeat("r", 54), 0, []string{strings.Repeat("x", 197)}) != 252 ||
		supervision.LongestOpen(strings.Repeat("r", 86), 0, []string{strings.Repeat("x", 185)}) != 272 {
		t.Fatal("selected material arithmetic changed")
	}
	for _, extra := range []string{strings.Repeat("p", 80), strings.Repeat("界", 80), strings.Repeat("🦊", 40)} {
		if err := nativeArchivePathBudget(ctx, filepath.Join(root, extra), []archiveFileRecord{{Path: strings.Repeat("x", room)}}); err == nil {
			t.Fatal("long profile/version escaped budget")
		}
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("budget created target", err)
	}
}

func TestNativeArchiveBudgetCannotUseAShortAliasToEscapeActualLength(t *testing.T) {
	ctx := context.Background()
	// TempDir adds the test name to the worktree's scratch path. Leave room
	// for the archive member that crosses the canonical path budget.
	parent, err := os.MkdirTemp(testPerson, "archive-path-")
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(parent, "Long canonical application data directory for the native path fixture")
	if err := os.Mkdir(base, 0700); err != nil {
		t.Fatal(err)
	}
	short, err := placetest.ShortName(base)
	if err != nil {
		t.Skip(err)
	}
	root := filepath.Join(short, "future-version")
	pending, err := fileid.Pending(ctx, fileid.Self, root)
	if err != nil {
		t.Fatal(err)
	}
	if supervision.UTF16Len(root) >= supervision.UTF16Len(pending.Path()) {
		t.Skip("filesystem supplies no shorter alias")
	}
	room := supervision.MaxLibraryPath - supervision.UTF16Len(pending.Path()) - 1
	if room < 1 {
		t.Fatal("fixture canonical root is already too deep")
	}
	member := strings.Repeat("x", room+1)
	if supervision.UTF16Len(filepath.Join(root, member)) > supervision.MaxLibraryPath {
		t.Fatal("fixture does not expose spelling shortcut")
	}
	if err := nativeArchivePathBudget(ctx, root, []archiveFileRecord{{Path: member}}); err == nil {
		t.Fatal("short alias escaped actual resource budget")
	}
}

func TestNativeArchiveHostBudgetRefusesBeforePlacementOrNativeCommand(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	_, a, _, _ := nativeArchiveFixture(t)
	source, pin := archiveFixture(t, archiveEntry{name: "launch-helper.exe", data: "inert native image"}, archiveEntry{name: strings.Repeat("x", 240), data: "inert"})
	a.Bytes, a.SHA256 = pin.Bytes, pin.SHA256
	a.PeakBytes += pin.Bytes + 4096
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == fixtureComponent {
			c.Components[i].Artifact = &a
		}
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	ctx := context.Background()
	if _, err := j.SavePlan(ctx, mustPlan(t, j.Catalog(), fixtureApp)); err != nil {
		t.Fatal(err)
	}
	state, err := j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldCommand := nativeArchiveCommand
	defer func() { nativeArchiveCommand = oldCommand }()
	nativeArchiveCommand = func(context.Context, sevenZipPin, nativeArchiveOperation) ([]byte, error) {
		t.Fatal("native command before path admission")
		return nil, nil
	}
	r := NewRunner(j, &fakeTransfer{})
	_, err = r.advanceNativeArchive(ctx, state, fixtureComponent, a, Effect{Phase: Verified, StagedPath: source})
	if err == nil || !strings.Contains(err.Error(), "UTF-16") {
		t.Fatal("host budget not refused", err)
	}
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("host placed before admission", err)
	}
}

func TestNativeArchiveBackendBudgetRefusesBeforeExtractAndSeal(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := InstallArchive(ctx, a, source, root); err != nil {
		t.Fatal(err)
	}
	plan.Files = append(plan.Files, archiveFileRecord{Path: strings.Repeat("x", 240), SHA256: strings.Repeat("a", 64)})
	oldCommand, oldExtract := nativeArchiveCommand, nativeArchiveExtract
	defer func() { nativeArchiveCommand, nativeArchiveExtract = oldCommand, oldExtract }()
	nativeArchiveCommand = func(_ context.Context, _ sevenZipPin, op nativeArchiveOperation) ([]byte, error) {
		if op != nativeArchivePlan {
			t.Fatal("seal/check before path admission")
		}
		return json.Marshal(plan)
	}
	nativeArchiveExtract = func(context.Context, sevenZipArchive) error {
		t.Fatal("backend extracted before path admission")
		return nil
	}
	err = installNativeResources(ctx, a, root, source, nil)
	if err == nil || !strings.Contains(err.Error(), "UTF-16") {
		t.Fatal("backend budget not refused", err)
	}
	if _, err = os.Stat(filepath.Join(root, plan.Directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("backend created before admission", err)
	}
}

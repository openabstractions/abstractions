package setup

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func TestSetAsideAgentImageName(t *testing.T) {
	for name, want := range map[string]bool{
		".oa-helper-old-2821748900":            true,
		".oa-helper-old-7":                     true,
		".oa-helper-old-":                      false,
		".oa-helper-old-12a":                   false,
		".oa-helper-old-12.exe":                false,
		".OA-HELPER-OLD-12":                    false,
		"x.oa-helper-old-12":                   false,
		"oa-helper-old-12":                     false,
		".oa-helper-old-123456789012345678901": false,
	} {
		if got := setAsideAgentImageName(name); got != want {
			t.Errorf("setAsideAgentImageName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestRemoveSetAsideAgentImagesKeepsCurrentHeldAndUnrelatedFiles(t *testing.T) {
	root := t.TempDir()
	old := []string{".oa-helper-old-2821748900", ".oa-helper-old-695398237", ".oa-helper-old-1"}
	stays := []string{
		"oa-claude-integration.exe",
		"oa-codex-integration.exe",
		"oa-claude-integration.exe.lock",
		".oa-agent-update-batch.lock",
		"oa-codex-integration.exe.f08f.tmp.previous",
		".oa-helper-old-held",
		".oa-helper-old-12.exe",
		".oa-helper-old-3244746243",
		".oa-helper-old-55",
	}
	writeAgentFixtureFiles(t, root, append(slices.Clone(old), stays...)...)
	// A copy another process still holds cannot be deleted; it stays and the
	// sweep reports no failure.
	held, err := openArchiveSource(context.Background(), filepath.Join(root, ".oa-helper-old-3244746243"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := os.Mkdir(filepath.Join(root, ".oa-helper-old-99"), 0700); err != nil {
		t.Fatal(err)
	}
	stays = append(stays, ".oa-helper-old-99")

	removed := removeSetAsideAgentImages(t.Context(), root, nil, filepath.Join(root, ".oa-helper-old-55"))
	slices.Sort(removed)
	want := slices.Clone(old)
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	for _, name := range old {
		if agentFixtureExists(t, root, name) {
			t.Errorf("set-aside copy %s remains", name)
		}
	}
	for _, name := range stays {
		if !agentFixtureExists(t, root, name) {
			t.Errorf("%s was removed", name)
		}
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	if removed := removeSetAsideAgentImages(t.Context(), root, nil); !slices.Equal(removed, []string{".oa-helper-old-3244746243", ".oa-helper-old-55"}) {
		t.Fatalf("released copies removed %v", removed)
	}
}

func TestRemoveSetAsideAgentImagesDoesNotFollowLinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeAgentFixtureFiles(t, outside, "victim.exe")
	link := filepath.Join(root, ".oa-helper-old-404")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if removed := removeSetAsideAgentImages(t.Context(), root, nil); len(removed) != 0 {
		t.Fatalf("removed %v through a link", removed)
	}
	if !agentFixtureExists(t, root, ".oa-helper-old-404") || !agentFixtureExists(t, outside, "victim.exe") {
		t.Fatal("sweep followed or removed the junction")
	}
}

// A helper that is still running when an update moves it aside keeps its copy
// until it exits; the next sweep removes it.
func TestRemoveSetAsideAgentImagesWaitsForRunningHelper(t *testing.T) {
	if os.Getenv("OA_SET_ASIDE_HELPER_CHILD") == "1" {
		// The child helper runs until its parent closes its input.
		io.Copy(io.Discard, os.Stdin)
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	helper := filepath.Join(root, "oa-claude-integration.exe")
	copyTestFile(t, self, helper)
	cmd := exec.Command(helper, "-test.run=^TestRemoveSetAsideAgentImagesWaitsForRunningHelper$")
	cmd.Env = append(os.Environ(), "OA_SET_ASIDE_HELPER_CHILD=1")
	stop, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := false
	defer func() {
		if !exited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	}()
	aside := filepath.Join(root, ".oa-helper-old-31337")
	if err := os.Rename(helper, aside); err != nil {
		t.Fatal(err)
	}
	copyTestFile(t, self, helper)
	if removed := removeSetAsideAgentImages(t.Context(), root, nil); len(removed) != 0 {
		t.Fatalf("running helper image removed: %v", removed)
	}
	if !agentFixtureExists(t, root, ".oa-helper-old-31337") || !agentFixtureExists(t, root, "oa-claude-integration.exe") {
		t.Fatal("running or current helper removed")
	}
	stop.Close()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper child: %v", err)
	}
	exited = true
	var removed []string
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if removed = removeSetAsideAgentImages(t.Context(), root, nil); len(removed) != 0 {
			break
		}
	}
	if !slices.Equal(removed, []string{".oa-helper-old-31337"}) {
		t.Fatalf("exited helper copy removed %v", removed)
	}
	if !agentFixtureExists(t, root, "oa-claude-integration.exe") {
		t.Fatal("current helper removed")
	}
}

func copyTestFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveSetAsideAgentImagesChecksPrivateDirectoryUnderHold(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "agent-integrations")
	moved := filepath.Join(parent, "moved")
	if err := privatedir.Make(root); err != nil {
		t.Fatal(err)
	}
	const stale = ".oa-helper-old-101"
	writeAgentFixtureFiles(t, root, stale)
	// The test account can move the fixture before cleanup takes its hold.
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(moved, root); err != nil {
		t.Fatal(err)
	}
	checked := false
	removed := removeSetAsideAgentImages(t.Context(), root, func(path string) error {
		checked = true
		if err := os.Rename(path, moved); err == nil {
			t.Fatal("the cleanup root could be replaced during the privacy check")
		}
		return privatedir.CheckDir(path)
	})
	if !checked || !slices.Equal(removed, []string{stale}) {
		t.Fatalf("private root checked = %t, removed = %v", checked, removed)
	}
}

func TestRemoveAgentImageCopiesHonorsCancellationDuringSelection(t *testing.T) {
	root := t.TempDir()
	const stale = ".oa-helper-old-101"
	writeAgentFixtureFiles(t, root, stale)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	considered := false
	removed := removeAgentImageCopies(ctx, root, nil, func(name string) bool {
		considered = true
		cancel()
		return setAsideAgentImageName(name)
	})
	if !considered || len(removed) != 0 || !agentFixtureExists(t, root, stale) {
		t.Fatalf("candidate considered = %t, removed = %v", considered, removed)
	}
}

func TestRemoveSetAsideAgentImagesReadsFurtherBatches(t *testing.T) {
	root := t.TempDir()
	var want []string
	for i := 100; i < 165; i++ {
		want = append(want, fmt.Sprintf(".oa-helper-old-%d", i))
	}
	writeAgentFixtureFiles(t, root, want...)
	if removed := removeSetAsideAgentImages(t.Context(), root, nil); !slices.Equal(removed, want) {
		t.Fatalf("removed %d candidates, want %d", len(removed), len(want))
	}
}

func TestRemoveSetAsideAgentImagesSweepsSetupConnectorFolder(t *testing.T) {
	home, err := CurrentHome()
	if err != nil || home.Isolated {
		t.Fatalf("home %+v: %v", home, err)
	}
	root := filepath.Join(home.Config, "agent-integrations")
	if err := privatedir.Ensure(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	writeAgentFixtureFiles(t, root, ".oa-helper-old-2098074858", "oa-codex-integration.exe")
	if removed := RemoveSetAsideAgentImages(context.Background()); !slices.Equal(removed, []string{".oa-helper-old-2098074858"}) {
		t.Fatalf("removed %v", removed)
	}
	if !agentFixtureExists(t, root, "oa-codex-integration.exe") {
		t.Fatal("current helper removed")
	}
}

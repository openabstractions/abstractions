package nativecli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// claudeCLIFixture writes claude.exe at <root>\Claude-Data\<family>\claude-code\<version>\<build>.
func claudeCLIFixture(t *testing.T, root, version, build string) string {
	t.Helper()
	directory := filepath.Join(root, "Claude-Data", "claude_pzs8sxrjxfjjc", "claude-code", version, build)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(directory, "claude.exe")
	if err := os.WriteFile(program, []byte("inert "+version+" fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return program
}

func TestFollowClaudeCLIFindsNewestVersionAfterSelfUpdate(t *testing.T) {
	root := t.TempDir()
	saved := claudeCLIFixture(t, root, "2.1.309", "9078b9c1d466")
	claudeCLIFixture(t, root, "2.1.99", "aaaa")
	claudeCLIFixture(t, root, "2.1.317", "bbbb")
	newest := claudeCLIFixture(t, root, "2.1.321", "d020ce1b5d30")
	// A version still being installed has no CLI yet.
	if err := os.MkdirAll(filepath.Join(filepath.Dir(filepath.Dir(newest)), "..", "2.1.400", "cccc"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(saved))); err != nil {
		t.Fatal(err)
	}
	got, err := FollowClaudeCLI(context.Background(), saved)
	if err != nil || got != newest {
		t.Fatal("saved CLI did not follow the update", got, err)
	}
	if !SameClaudeCLIInstall(saved, newest) {
		t.Fatal("two versions of one installation compared different")
	}
}

func TestFollowClaudeCLIKeepsPathsOutsideClaudePackageData(t *testing.T) {
	root := t.TempDir()
	for _, program := range []string{
		filepath.Join(root, "bin", "claude.exe"),
		filepath.Join(root, "Claude-Data", "Other_family", "claude-code", "2.1.309", "abc", "claude.exe"),
		filepath.Join(root, "Other-Data", "claude_pzs8sxrjxfjjc", "claude-code", "2.1.309", "abc", "claude.exe"),
		filepath.Join(root, "Claude-Data", "claude_pzs8sxrjxfjjc", "claude-code", "latest", "abc", "claude.exe"),
		filepath.Join(root, "Claude-Data", "claude_pzs8sxrjxfjjc", "claude-code", "2.1.309", "abc", "node.exe"),
	} {
		if _, ok := ClaudeCLIInstallRoot(program); ok {
			t.Fatal("foreign path treated as Claude's package data", program)
		}
		if got, err := FollowClaudeCLI(context.Background(), program); err != nil || got != program {
			t.Fatal("foreign path was redirected", program, got, err)
		}
	}
	other := filepath.Join(t.TempDir(), "Claude-Data", "claude_pzs8sxrjxfjjc", "claude-code", "2.1.309", "abc", "claude.exe")
	if SameClaudeCLIInstall(claudeCLIFixture(t, root, "2.1.309", "abc"), other) {
		t.Fatal("two installations compared the same")
	}
}

func TestSameClaudeCLIInstallDistinguishesNativeRootTwins(t *testing.T) {
	root := t.TempDir()
	left := claudeCLIFixture(t, filepath.Join(root, "InstallK"), "2.1.309", "old")
	right := claudeCLIFixture(t, filepath.Join(root, "InstallK"), "2.1.321", "new")
	leftRoot, _ := ClaudeCLIInstallRoot(left)
	rightRoot, _ := ClaudeCLIInstallRoot(right)
	same, err := fileid.SameFile(leftRoot, rightRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := SameClaudeCLIInstall(left, right); got != same {
		t.Fatalf("install match = %t, volume identity = %t", got, same)
	}
}

func TestFollowClaudeCLIRefusesAmbiguousAndAbsentVersions(t *testing.T) {
	root := t.TempDir()
	saved := claudeCLIFixture(t, root, "2.1.321", "aaaa")
	claudeCLIFixture(t, root, "2.1.321", "bbbb")
	if _, err := FollowClaudeCLI(context.Background(), saved); err == nil {
		t.Fatal("two builds of the newest version were not refused")
	}
	empty := filepath.Join(t.TempDir(), "Claude-Data", "claude_pzs8sxrjxfjjc", "claude-code", "2.1.309", "abc", "claude.exe")
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(filepath.Dir(empty))), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := FollowClaudeCLI(context.Background(), empty); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("an installation without a CLI was not reported absent", err)
	}
}

func TestClaudeCLICurrentRefusesAKeptOlderVersion(t *testing.T) {
	root := t.TempDir()
	older := claudeCLIFixture(t, root, "2.1.317", "aaaa")
	newest := claudeCLIFixture(t, root, "2.1.321", "bbbb")
	ctx := context.Background()
	if ClaudeCLICurrent(ctx, older) || !ClaudeCLICurrent(ctx, newest) {
		t.Fatal("current version misjudged")
	}
	if ClaudeCLICurrent(ctx, filepath.Join(root, "missing", "claude.exe")) {
		t.Fatal("missing file counted current")
	}
	if err := os.Remove(older); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(newest, older); err != nil {
		t.Fatal(err)
	}
	if ClaudeCLICurrent(ctx, older) {
		t.Fatal("a hard link in an older version counted current")
	}
}

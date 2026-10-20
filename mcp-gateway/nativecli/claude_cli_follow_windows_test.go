package nativecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Owner report 2026-11-10: Claude Code updated itself from 2.1.309 to
// 2.1.321 and removed 2.1.309; the saved connection read "Needs attention".
func TestClaudeSavedCLIFollowsSelfUpdateAndAuthenticatesNewVersion(t *testing.T) {
	root := t.TempDir()
	saved := claudeCLIFixture(t, root, "2.1.309", "9078b9c1d466")
	locator := claudeSavedLocator(t, saved)
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(saved))); err != nil {
		t.Fatal(err)
	}
	claudeCLIFixture(t, root, "2.1.317", "bbbb")
	newest := claudeCLIFixture(t, root, "2.1.321", "d020ce1b5d30")
	closed := 0
	var authenticated []string
	hold := followingClaudeImage(func(_ context.Context, path string) (nativeProductImage, error) {
		authenticated = append(authenticated, path)
		return &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closed}, nil
	})
	cli, err := selectClaudeSavedCLI(context.Background(), []string{locator}, func(_ context.Context, path string) ([]byte, error) {
		if path != newest {
			t.Fatal("version probe ran another image", path)
		}
		return []byte("2.1.321 (Claude Code)\n"), nil
	}, hold)
	if err != nil {
		t.Fatal("saved connection did not follow the Claude Code update", err)
	}
	defer cli.Close()
	if cli.Program() != newest || cli.Version() != "2.1.321" || len(authenticated) != 1 || authenticated[0] != newest {
		t.Fatal(cli.Program(), cli.Version(), authenticated)
	}
}

func TestClaudeSavedCLIRefusesUnverifiableNewerVersion(t *testing.T) {
	root := t.TempDir()
	saved := claudeCLIFixture(t, root, "2.1.317", "bbbb")
	locator := claudeSavedLocator(t, saved)
	newest := claudeCLIFixture(t, root, "2.1.321", "d020ce1b5d30")
	probes := 0
	cli, err := selectClaudeSavedCLI(context.Background(), []string{locator}, func(context.Context, string) ([]byte, error) {
		probes++
		return []byte("2.1.321 (Claude Code)\n"), nil
	}, followingClaudeImage(func(_ context.Context, path string) (nativeProductImage, error) {
		if path != newest {
			t.Fatal("an older version replaced the refused newest one", path)
		}
		return nil, errors.New("Code publisher or description differs from its registered product")
	}))
	if err == nil || cli != nil || probes != 0 {
		t.Fatal("unverifiable Claude Code was admitted", err, probes)
	}
}

// A followed CLI must report the version its folder names.
func TestClaudeSavedCLIFollowedVersionMustMatchItsFolder(t *testing.T) {
	root := t.TempDir()
	saved := claudeCLIFixture(t, root, "2.1.317", "bbbb")
	locator := claudeSavedLocator(t, saved)
	claudeCLIFixture(t, root, "2.1.321", "d020ce1b5d30")
	closed := 0
	hold := followingClaudeImage(func(_ context.Context, path string) (nativeProductImage, error) {
		return &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closed}, nil
	})
	cli, err := selectClaudeSavedCLI(context.Background(), []string{locator}, func(context.Context, string) ([]byte, error) {
		return []byte("2.1.317 (Claude Code)\n"), nil
	}, hold)
	if err == nil || cli != nil || closed != 1 {
		t.Fatal("followed CLI with a mismatched version admitted", err, closed)
	}
}

func TestClaudeSavedLocatorRecognizesAHeldFileThroughAnotherName(t *testing.T) {
	dir := t.TempDir()
	saved := filepath.Join(dir, "claude.exe")
	if err := os.WriteFile(saved, []byte("saved CLI"), 0600); err != nil {
		t.Fatal(err)
	}
	locator := claudeSavedLocator(t, saved)
	alias := "\\\\?\\" + saved
	cli, err := selectClaudeSavedCLI(t.Context(), []string{locator}, func(context.Context, string) ([]byte, error) {
		return []byte("2.1.317 (Claude Code)\n"), nil
	}, func(context.Context, string) (nativeProductImage, error) {
		return claudeCLIImageFixture(t, alias), nil
	})
	if err != nil {
		t.Fatalf("a second name for the held image was treated as a followed update: %v", err)
	}
	if err := cli.Close(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "moved-claude.exe")
	if err := os.Rename(saved, moved); err != nil {
		t.Fatal(err)
	}
	image := claudeCLIImageFixture(t, moved)
	defer image.Close()
	if !claudeCLIFollowed(t.Context(), []string{locator}, image.FileKey()) {
		t.Fatal("the historical identity made an absent locator current")
	}
}

func TestClaudeSavedLocatorDistinguishesNativeFilesWithFoldedNames(t *testing.T) {
	root := t.TempDir()
	saved := filepath.Join(root, "ClaudeK.exe")
	other := filepath.Join(root, "Claude\u212a.exe")
	if err := os.WriteFile(saved, []byte("saved CLI"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(other, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		t.Skip("this volume identifies the two names as one entry")
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("another CLI"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	image := claudeCLIImageFixture(t, other)
	defer image.Close()
	if !claudeCLIFollowed(t.Context(), []string{claudeSavedLocator(t, saved)}, image.FileKey()) {
		t.Fatal("folded path names matched two different files")
	}
}

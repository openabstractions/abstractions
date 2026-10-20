package nativecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexSavedCLIWithoutDesktopProcess(t *testing.T) {
	_, program := codexCLIFixture(t)
	locator := claudeSavedLocator(t, program)
	probes, closes := 0, 0
	selected, err := selectCodexSavedCLI(context.Background(), []string{locator, locator}, func(_ context.Context, path string) ([]byte, error) {
		probes++
		if path != program {
			t.Fatal(path)
		}
		return []byte("codex-cli 0.164.0\n"), nil
	}, func(_ context.Context, path string) (nativeProductImage, error) {
		return &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closes}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if probes != 1 || closes != 1 || selected.Version() != "0.164.0" || selected.Program() != program {
		t.Fatal(probes, closes, selected)
	}
	if err := selected.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil || closes != 2 {
		t.Fatal(err, closes)
	}
}

func TestCodexSavedCLIFencesAmbiguityAndFailedProbe(t *testing.T) {
	_, a := codexCLIFixture(t)
	_, b := codexCLIFixture(t)
	for _, mode := range []string{"ambiguous", "probe", "version", "changed"} {
		t.Run(mode, func(t *testing.T) {
			stored := []string{claudeSavedLocator(t, a)}
			if mode == "ambiguous" {
				stored = append(stored, claudeSavedLocator(t, b))
			}
			probes, holds, closes := 0, 0, 0
			_, err := selectCodexSavedCLI(context.Background(), stored, func(context.Context, string) ([]byte, error) {
				probes++
				if mode == "probe" {
					return nil, errors.New("probe unavailable")
				}
				return []byte("wrong vendor\n"), nil
			}, func(_ context.Context, path string) (nativeProductImage, error) {
				holds++
				return &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closes, changed: mode == "changed"}, nil
			})
			if err == nil || holds != closes || (mode == "ambiguous" || mode == "changed") && probes != 0 {
				t.Fatal(err, holds, closes, probes)
			}
		})
	}
}

func codexPackageFixture(t *testing.T, root, version string) string {
	t.Helper()
	program := codexPackageCLI(root, "OpenAI.Codex_"+version+"_x64__2p2nqsd0c76g0")
	if err := os.MkdirAll(filepath.Dir(program), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("inert CLI fixture "+version), 0600); err != nil {
		t.Fatal(err)
	}
	return program
}

// A saved Codex connection follows a Codex Desktop update to the registered
// package and authenticates that image before any version probe; a refused
// image is never admitted.
func TestCodexSavedCLIFollowsDesktopUpdateAndAuthenticates(t *testing.T) {
	root := t.TempDir()
	saved := codexPackageFixture(t, root, "26.1027.3384.0")
	locator := claudeSavedLocator(t, saved)
	if err := os.RemoveAll(filepath.Join(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0")); err != nil {
		t.Fatal(err)
	}
	current := codexPackageFixture(t, root, "26.1103.7773.0")
	follow := func(ctx context.Context, program string) (string, error) {
		return followCodexCLI(ctx, program, func(context.Context) (string, bool, error) { return current, true, nil })
	}
	closes := 0
	var authenticated []string
	selected, err := selectCodexSavedCLI(context.Background(), []string{locator}, func(_ context.Context, path string) ([]byte, error) {
		if path != current {
			t.Fatal("version probe ran another image", path)
		}
		return []byte("codex-cli 0.170.0\n"), nil
	}, followingImage(follow, func(_ context.Context, path string) (nativeProductImage, error) {
		authenticated = append(authenticated, path)
		return &claudeSavedImageFixture{nativeProductImage: claudeCLIImageFixture(t, path), closes: &closes}, nil
	}))
	if err != nil {
		t.Fatal("saved Codex connection did not follow the update", err)
	}
	if selected.Program() != current || len(authenticated) != 1 || authenticated[0] != current {
		t.Fatal(selected.Program(), authenticated)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	probes := 0
	refused, err := selectCodexSavedCLI(context.Background(), []string{locator}, func(context.Context, string) ([]byte, error) {
		probes++
		return []byte("codex-cli 0.170.0\n"), nil
	}, followingImage(follow, func(context.Context, string) (nativeProductImage, error) {
		return nil, errors.New("registered package publisher differs")
	}))
	if err == nil || refused != nil || probes != 0 {
		t.Fatal("an unverified Codex image was admitted", err, probes)
	}
}

// On a machine with Codex Desktop, a removed package version follows to the
// registered package, whose image the saved hold verifies. Read-only.
func TestCodexSavedCLIFollowsRegisteredDesktopPackage(t *testing.T) {
	current, ok, err := registeredCodexCLI(context.Background())
	if err != nil || !ok {
		t.Skip("Codex Desktop is not registered for this account", err)
	}
	root, inLayout := CodexCLIInstallRoot(current)
	if !inLayout {
		t.Fatal("registered Codex CLI is outside its package layout", current)
	}
	removed := codexPackageCLI(root, "OpenAI.Codex_1.0.0.0_x64__2p2nqsd0c76g0")
	got, err := FollowCodexCLI(context.Background(), removed)
	if err != nil || got != current {
		t.Fatal(got, err)
	}
	image, err := followingImage(FollowCodexCLI, holdSavedCodexImage)(context.Background(), removed)
	if err != nil {
		t.Fatal("registered Codex image did not verify", err)
	}
	if image.Program() != current {
		t.Fatal(image.Program())
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
}

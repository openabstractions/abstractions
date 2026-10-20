package nativecli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func codexPackageCLI(root, folder string) string {
	return filepath.Join(root, folder, "app", "resources", "codex.exe")
}

// Owner state 2026-11-10: the saved Codex connection named
// OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0, which Codex Desktop's update
// to 26.1103.7773.0 had removed.
func TestFollowCodexCLIMovesToTheRegisteredPackage(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WindowsApps")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	saved := codexPackageCLI(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0")
	current := codexPackageCLI(root, "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0")
	if got, ok := CodexCLIInstallRoot(saved); !ok || got != root {
		t.Fatal("saved package path is outside the Codex layout", got, ok)
	}
	if !SameCodexCLIInstall(saved, current) {
		t.Fatal("two Codex package versions are one installation")
	}
	registered := func(context.Context) (string, bool, error) { return current, true, nil }
	got, err := followCodexCLI(context.Background(), saved, registered)
	if err != nil || got != current {
		t.Fatal("saved Codex path did not follow the update", got, err)
	}
}

func TestSameCodexCLIInstallKeepsOnlyExactProtectedRegisteredRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "WindowsApps")
	saved := codexPackageCLI(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0")
	current := codexPackageCLI(root, "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0")
	otherRoot := codexPackageCLI(root+"-other", "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0")
	foreign := codexPackageCLI(root, "OpenAI.Codex_26.1103.7773.0_x64__0000000000000")
	for _, test := range []struct {
		name    string
		current string
		err     error
		check   bool
		want    bool
	}{
		{"access denied and registered", current, fs.ErrPermission, true, true},
		{"access denied and unregistered", current, fs.ErrPermission, true, false},
		{"denied other root", otherRoot, fs.ErrPermission, false, false},
		{"denied foreign package", foreign, fs.ErrPermission, false, false},
		{"root missing", current, fs.ErrNotExist, false, false},
		{"different filesystem identities", current, nil, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			checked := false
			got := sameCodexCLIInstall(saved, test.current, func(a, b string) (bool, error) {
				if test.err == nil {
					return false, nil
				}
				return false, &fs.PathError{Op: "open", Path: a, Err: test.err}
			}, func(path string) bool {
				checked = true
				return path == current && test.want
			})
			if got != test.want || checked != test.check {
				t.Fatalf("matched = %t, registration checked = %t", got, checked)
			}
		})
	}
}

func TestFollowCodexCLIKeepsOtherPaths(t *testing.T) {
	root := t.TempDir()
	calls := 0
	registered := func(context.Context) (string, bool, error) {
		calls++
		return codexPackageCLI(root, "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0"), true, nil
	}
	for _, program := range []string{
		filepath.Join(root, "npm", "codex.exe"),
		codexPackageCLI(root, "OpenAI.Codex_26.1027.3384.0_x64__0000000000000"),
		codexPackageCLI(root, "Other.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0"),
		filepath.Join(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0", "app", "codex.exe"),
		filepath.Join(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0", "app", "resources", "evil.exe"),
		"OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0/app/resources/codex.exe",
	} {
		if _, ok := CodexCLIInstallRoot(program); ok {
			t.Fatal("accepted outside the Codex package layout", program)
		}
		if got, err := followCodexCLI(context.Background(), program, registered); err != nil || got != program {
			t.Fatal("moved a path outside the layout", program, got, err)
		}
	}
	if calls != 0 {
		t.Fatal("a path outside the layout consulted the registration")
	}
	if SameCodexCLIInstall(filepath.Join(root, "npm", "codex.exe"), codexPackageCLI(root, "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0")) {
		t.Fatal("an npm CLI is not the Codex Desktop installation")
	}
}

func TestFollowCodexCLIWithoutRegistrationOrWithError(t *testing.T) {
	saved := codexPackageCLI(t.TempDir(), "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0")
	got, err := followCodexCLI(context.Background(), saved, func(context.Context) (string, bool, error) { return "", false, nil })
	if err != nil || got != saved {
		t.Fatal("an unregistered Codex Desktop keeps the saved path for the hold to refuse", got, err)
	}
	if _, err := followCodexCLI(context.Background(), saved, func(context.Context) (string, bool, error) { return "", false, errors.New("ambiguous") }); err == nil {
		t.Fatal("a registration failure was ignored")
	}
}

package nativecli

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"regexp"

	"github.com/openabstractions/abstraction-facade/go/runtime/packaged"
	"github.com/openabstractions/abstraction-identity/fileid"
)

const codexProductFamily = "OpenAI.Codex_2p2nqsd0c76g0"

// Codex Desktop installs from its registered package. Every update installs a
// new package folder <WindowsApps>\OpenAI.Codex_<version>_<arch>__<publisher>
// and removes the earlier one; the CLI is app\resources\codex.exe inside it.
// The publisher ID is the one in codexProductFamily.
var (
	codexPackageDir        = regexp.MustCompile(`(?i)^OpenAI\.Codex_[0-9]{1,5}(?:\.[0-9]{1,5}){3}_[a-z0-9]{1,16}__2p2nqsd0c76g0$`)
	codexCLIProgram        = regexp.MustCompile(`(?i)^codex\.exe$`)
	codexCLIResourceLayout = regexp.MustCompile(`(?i)^app[/\\]resources$`)
)

// CodexCLIInstallRoot returns the folder holding Codex Desktop's package
// folders for a CLI path in that layout. Any other path has no install root.
func CodexCLIInstallRoot(program string) (string, bool) {
	if !filepath.IsAbs(program) || !codexCLIProgram.MatchString(filepath.Base(program)) {
		return "", false
	}
	resources := filepath.Dir(program)
	app := filepath.Dir(resources)
	pkg := filepath.Dir(app)
	if !codexCLIResourceLayout.MatchString(filepath.Join(filepath.Base(app), filepath.Base(resources))) || !codexPackageDir.MatchString(filepath.Base(pkg)) {
		return "", false
	}
	return filepath.Dir(pkg), true
}

// SameCodexCLIInstall reports whether two saved CLI locators name the same
// Codex Desktop installation: the same path, or two of its package versions.
// It is a locator comparison and grants no execution authority.
func SameCodexCLIInstall(saved, current string) bool {
	return sameCodexCLIInstall(saved, current, fileid.SameFile, currentCodexRegistration)
}

func sameCodexCLIInstall(saved, current string, sameFile func(string, string) (bool, error), registered func(string) bool) bool {
	return sameCLIInstall(CodexCLIInstallRoot, saved, current, func(savedRoot, currentRoot string) bool {
		same, err := sameFile(savedRoot, currentRoot)
		if err == nil {
			return same
		}
		// WindowsApps can deny opening the parent of an installed package.
		// Keep the exact saved locator only when the held current CLI also
		// names the OS-registered product.
		return errors.Is(err, fs.ErrPermission) && savedRoot == currentRoot && registered(current)
	})
}

func currentCodexRegistration(program string) bool {
	rows, err := packaged.Registrations(context.Background(), codexProductFamily)
	if err != nil || len(rows) != 1 {
		return false
	}
	same, err := fileid.SameFile(filepath.Join(rows[0].Path, "app", "resources", "codex.exe"), program)
	return err == nil && same
}

// followCodexCLI maps a saved CLI locator in Codex Desktop's package layout to
// the CLI of the package the OS registers now for that publisher. A path
// outside the layout, or one with no registered package, is returned
// unchanged. The result is a location only.
func followCodexCLI(ctx context.Context, program string, registered func(context.Context) (string, bool, error)) (string, error) {
	if _, ok := CodexCLIInstallRoot(program); !ok {
		return program, nil
	}
	current, ok, err := registered(ctx)
	if err != nil {
		return "", err
	}
	if !ok {
		return program, nil
	}
	return current, nil
}

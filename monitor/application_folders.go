package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
)

// An application manifest file names the folders that exist only on the
// installing machine by placeholder (VISION 2026-10-06): {app} is the folder
// the installer put the manifest in, {local_app_data} the account's local
// application data and {pictures} its Pictures folder. The Panel resolves
// them before the Facade's own check and refuses any path that resolves
// outside them, asking the file system by identity (identity.Within).

// manifestFolders are the folders a manifest's paths may name.
type manifestFolders map[string]string

// manifestPlaceholder is one value that begins with a placeholder: the
// placeholder's name and the rest below it.
var manifestPlaceholder = regexp.MustCompile(`^\{([a-z_]+)\}(?:[\\/](.*))?$`)

// resolve returns the value the descriptor carries. A value beginning with a
// placeholder is the named folder joined with the rest, which must stay
// strictly inside that folder; a placeholder anywhere else, or an unknown
// one, is refused. An absolute value without one must lie inside one of the
// folders. The program must lie inside {app}. Any other value is literal.
func (f manifestFolders) resolve(value string, program bool) (string, error) {
	path, root, literal, err := f.place(value, program)
	if err != nil || literal {
		return path, err
	}
	roots := []string{root}
	switch {
	case program:
		roots = []string{f["app"]}
	case root == "":
		roots = []string{f["app"], f["local_app_data"], f["pictures"]}
	}
	for _, folder := range roots {
		if folder == "" {
			continue
		}
		if inside, err := identity.Within(path, folder); err == nil && inside {
			if program {
				if err := refuseSharedInterpreter(path); err != nil {
					return "", err
				}
			}
			return path, nil
		}
	}
	if program {
		return "", fmt.Errorf("%s is outside the application's folder", path)
	}
	return "", fmt.Errorf("%s is outside the application's folders", path)
}

// place is the path a value names before any folder check: a placeholder's
// folder joined with the rest (root that folder), an absolute path (root ""),
// or a literal value that names no path (literal), refused for the program.
func (f manifestFolders) place(value string, program bool) (path, root string, literal bool, err error) {
	path = value
	if strings.Contains(value, "{") {
		m := manifestPlaceholder.FindStringSubmatch(value)
		if m == nil {
			return "", "", false, errors.New("a placeholder must begin the value and name a folder: {app}, {local_app_data} or {pictures}")
		}
		folder, known := f[m[1]]
		if !known {
			return "", "", false, fmt.Errorf("unknown placeholder {%s}", m[1])
		}
		if m[2] == "" {
			return "", "", false, fmt.Errorf("{%s} must be followed by a path inside it", m[1])
		}
		// A manifest is portable text: the rest separates with \ or / on
		// every OS and joins with the native separator.
		rest := filepath.FromSlash(strings.ReplaceAll(m[2], `\`, "/"))
		path, root = filepath.Join(folder, rest), folder
	} else if !filepath.IsAbs(value) {
		if program {
			return "", "", false, errors.New("the program must be {app}/... or an absolute path inside the application's folder")
		}
		return value, "", true, nil
	}
	if err := identity.LocalPath(path); err != nil {
		return "", "", false, err
	}
	return path, root, false, nil
}

// officialProgram is the official program OA's compiled Setup catalog names
// for the app a manifest registers (setup.OfficialProgramOf): its path for
// this account and the signer its file must carry. It is the only trust
// anchor for a program outside {app}; an install record is no anchor, since
// any process of the account can write one.
type officialProgram struct {
	Program string
	Signer  string
}

// officialSigner is the Authenticode signer the Panel verifies on program
// itself (identity.VerifyFile, asks.CodePublisher); ok is false for a file
// without a trusted signature. Tests stand in for it.
var officialSigner = func(program string) (subject string, ok bool) {
	code, _, err := identity.VerifyFile(program, nil)
	if err != nil {
		return "", false
	}
	publisher := asks.CodePublisher(code)
	return publisher.Subject, publisher.Trusted && publisher.Subject != ""
}

// resolveProgram resolves the manifest's program with the one exception to
// "the program lies inside {app}" (P-J1a, coordinator ruling 2026-11-01): an
// integration manifest may name the official app's program outside {app}
// only when OA's compiled catalog names that official program for the app,
// the manifest's program is that program (identity.SameSubjectProgram), and
// its file carries the catalog's signer, verified here. A placeholder path
// must still stay inside its own folder, and a script host is never an app.
// Any other program outside {app} is refused.
func (f manifestFolders) resolveProgram(value string, official officialProgram) (string, error) {
	path, err := f.resolve(value, true)
	if err == nil || official.Program == "" || official.Signer == "" {
		return path, err
	}
	outside, root, _, placeErr := f.place(value, true)
	if placeErr != nil {
		return "", placeErr
	}
	if root != "" {
		if inside, withinErr := identity.Within(outside, root); withinErr != nil || !inside {
			return "", err
		}
	}
	if !identity.SameSubjectProgram(outside, official.Program) {
		return "", err
	}
	if signer, ok := officialSigner(outside); !ok || signer != official.Signer {
		return "", fmt.Errorf("%s does not carry its official publisher's signature", outside)
	}
	if shared := refuseSharedInterpreter(outside); shared != nil {
		return "", shared
	}
	return outside, nil
}

// refuseSharedInterpreter refuses a script host as an app's program: one
// identity reads as a shared interpreter by its names or signature (identity
// ID-I1, SharedInterpreterProgram: PowerShell, cmd, Node, Python and the
// rest), or by the pyvenv.cfg rule (SharedInterpreterVenv). Registering one
// would make one grant cover every script it runs; an application's own
// interpreter is a copy inside its folder (VISION 2026-10-07: interpreters
// are not programs). A config, home or program that cannot be verified is
// refused the same way.
func refuseSharedInterpreter(program string) error {
	if err := identity.SharedInterpreterVenv(program); err != nil {
		return err
	}
	if identity.SharedInterpreterProgram(program, identity.Code{}) {
		return fmt.Errorf("%s runs scripts for any program, so it cannot be registered as an app", program)
	}
	return nil
}

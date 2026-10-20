package main

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	"golang.org/x/sys/windows"
)

// `openabstractions host sandbox-roots grant|revoke|show` writes, removes and
// reports the one entry for OA's container capability on the system drive's
// root and the profiles folder (provider kit KIT-S7, facade FAC-P3). The
// per-machine installer runs grant at install and revoke at removal, as
// administrator; show needs no administrator.
//
// TEMPORARY EXCEPTION (TODO.md "Temporary exception: sandbox root
// attributes"): remove this command and the installer's custom actions with
// the capability once the application that needs them resolves its folders
// natively in a released, pinned version.

const sandboxRootsUsage = `Usage: openabstractions host sandbox-roots grant|revoke|show

  grant   add one non-inherited entry letting every OA sandbox read the
          attributes of the system drive's root and the profiles folder,
          and nothing they hold. An entry already there is kept.
  revoke  remove exactly that entry from both folders, by SID, rights and
          flags.
  show    print whether each folder holds the entry.

grant and revoke change each folder's own access list without propagating
to what it holds, and need an administrator token. A failure is also
appended to %ProgramData%\abstraction\upgrade-v1\installer-actions.txt.
This is a temporary exception; it goes once the application that needs it
reads its folders natively.
`

// sandboxRootOps is the kit's root entry API, replaced by tests.
type sandboxRootOps struct {
	roots         func() ([]string, error)
	grant, revoke func(string) (bool, error)
	present       func(string) (bool, error)
	recordFailure func(command string, err error)
}

func systemSandboxRootOps() sandboxRootOps {
	return sandboxRootOps{roots: sandbox.AttributeRoots, grant: sandbox.GrantRootAttributes, revoke: sandbox.RevokeRootAttributes,
		present: sandbox.RootAttributesPresent, recordFailure: func(command string, err error) {
			// An unelevated run would create %ProgramData%\abstraction owned by
			// this account, which the machine log then refuses for good.
			if windows.GetCurrentProcessToken().IsElevated() {
				recordInstallerFailure(installerActionsPath, "machine", command, err, time.Now())
			}
		}}
}

func sandboxRootsCommand(args []string, output, diagnostics io.Writer, ops sandboxRootOps) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, sandboxRootsUsage)
		return err
	}
	if len(args) != 1 || args[0] != "grant" && args[0] != "revoke" && args[0] != "show" {
		return commandMistake(diagnostics, "host sandbox-roots: grant, revoke or show is required", "openabstractions host sandbox-roots --help")
	}
	roots, err := ops.roots()
	if err != nil {
		return err
	}
	var problems []error
	for _, root := range roots {
		var line string
		switch args[0] {
		case "grant":
			changed, err := ops.grant(root)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", root, err))
				continue
			}
			line = map[bool]string{true: "entry added", false: "entry already present"}[changed]
		case "revoke":
			changed, err := ops.revoke(root)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", root, err))
				continue
			}
			line = map[bool]string{true: "entry removed", false: "no entry to remove"}[changed]
		case "show":
			present, err := ops.present(root)
			if err != nil {
				problems = append(problems, fmt.Errorf("%s: %w", root, err))
				continue
			}
			line = map[bool]string{true: "entry present", false: "no entry"}[present]
		}
		if _, err := fmt.Fprintf(output, "%s: %s\n", root, line); err != nil {
			return err
		}
	}
	err = errors.Join(problems...)
	if err != nil && args[0] != "show" {
		ops.recordFailure("host sandbox-roots "+args[0], err)
	}
	return err
}

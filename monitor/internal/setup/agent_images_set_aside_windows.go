package setup

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// setAsideAgentImagePrefix starts the name replaceAgentImageAtBackup gives a
// replaced helper it moves aside; os.CreateTemp completes it with decimal
// digits. Windows cannot delete a running image, so the move happens first and
// the copy stays until a sweep finds it unused.
const setAsideAgentImagePrefix = ".oa-helper-old-"

func setAsideAgentImageName(name string) bool {
	digits, ok := strings.CutPrefix(name, setAsideAgentImagePrefix)
	return ok && digits != "" && len(digits) <= 20 && strings.Trim(digits, "0123456789") == ""
}

// RemoveSetAsideAgentImages sweeps Setup's connector folder at Panel and
// background-presenter start. It does nothing for an isolated runtime or a
// folder that is missing, not private or reached through a link.
func RemoveSetAsideAgentImages(ctx context.Context) []string {
	home, err := CurrentHome()
	if err != nil || home.Isolated {
		return nil
	}
	root := filepath.Join(home.Config, "agent-integrations")
	return removeSetAsideAgentImages(ctx, root, privatedir.CheckDir)
}

// removeSetAsideAgentImages deletes the regular files in root named
// ".oa-helper-old-<digits>" except those keep names. A copy a running helper
// still holds cannot be deleted; it stays for the next sweep and the sweep
// reports no failure. Current helpers, other names, directories and links stay;
// a link is never followed.
func removeSetAsideAgentImages(ctx context.Context, root string, checkDirectory func(string) error, keep ...string) []string {
	ctx, cancel := context.WithTimeout(ctx, agentImageCleanupTimeout)
	defer cancel()
	return removeAgentImageCopies(ctx, root, checkDirectory, setAsideAgentImageName, keep...)
}

package setup

import (
	"context"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
)

func agentImageName(party AgentParty) string { return "oa-" + string(party) + "-integration" }
func agentGatewayPath(server string) string {
	return filepath.Join(filepath.Dir(server), "openabstractions-mcp")
}

// The new file is private to this person. POSIX holds do not exclude writers
// or renames; AgentImage.Check revalidates its named identity before use.
func agentImageMode(file *os.File) error { return file.Chmod(0700) }

func openAgentImage(ctx context.Context, path string) (*os.File, string, error) {
	held, err := fileid.Open(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return nil, "", err
	}
	// Ownership of this same descriptor passes to AgentImage/the copy caller.
	return held.File(), held.FileID().Key(), nil
}

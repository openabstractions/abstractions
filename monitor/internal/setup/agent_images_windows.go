package setup

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
)

func agentImageName(party AgentParty) string { return "oa-" + string(party) + "-integration.exe" }
func agentGatewayPath(server string) string {
	return filepath.Join(filepath.Dir(filepath.Dir(server)), "tools", "openabstractions-mcp.exe")
}
func agentImageMode(*os.File) error { return nil }

func openAgentImage(ctx context.Context, path string) (*os.File, string, error) {
	file, err := openArchiveSource(ctx, path)
	if err != nil {
		return nil, "", err
	}
	key, err := fileid.FileKeyOf(file)
	if err != nil {
		return nil, "", errors.Join(err, file.Close())
	}
	return file, key, nil
}

package setupio

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"path/filepath"
	"time"
)

type CLIResult struct {
	Output, Diagnostic []byte
	Code               int
	NotStarted         bool
}
type Output struct{ bytes.Buffer }

func (b *Output) Write(p []byte) (int, error) {
	if len(p) > 32<<10-b.Len() {
		return 0, errors.New("vendor output bound")
	}
	return b.Buffer.Write(p)
}
func RunClaude(ctx context.Context, executable, project string, args []string) ([]byte, error) {
	cmd := childproc.Hidden(exec.CommandContext(ctx, executable, args...))
	cmd.Dir = project
	// Documented vendor controls apply only to these configuration subprocesses.
	// They request no updates, nonessential traffic or cloud MCP discovery;
	// normal interactive Claude settings and profiles are left unchanged.
	cmd.Env = append(os.Environ(), "DISABLE_UPDATES=1", "DISABLE_TELEMETRY=1",
		"DISABLE_ERROR_REPORTING=1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ENABLE_CLAUDEAI_MCP_SERVERS=false")
	cmd.WaitDelay = time.Second
	var output Output
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.Bytes(), err
}
func RunCodex(ctx context.Context, program, directory string, args []string) (CLIResult, error) {
	command := childproc.Hidden(exec.CommandContext(ctx, program, args...))
	command.Dir = directory
	command.WaitDelay = time.Second
	var output, diagnostic Output
	command.Stdout = &output
	command.Stderr = &diagnostic
	err := command.Run()
	result := CLIResult{Output: output.Bytes(), Diagnostic: diagnostic.Bytes(), NotStarted: command.Process == nil}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			return result, err
		}
		result.Code = exit.ExitCode()
	}
	return result, nil
}
func CodexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		if !filepath.IsAbs(home) {
			return "", errors.New("Codex home must be absolute")
		}
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

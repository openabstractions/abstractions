// Package fixture holds helpers shared by conformance fixture tests: child
// processes that report an expired budget by name, and executable comparison
// on the file Program proof names.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Output runs cmd, which the caller built with exec.CommandContext(ctx, ...),
// and returns its combined output. A child killed because ctx expired leaves
// only a bare exit status; Output then returns an error that names the
// timeout, the executable and the deadline, and wraps context.DeadlineExceeded.
func Output(ctx context.Context, cmd *exec.Cmd) ([]byte, error) {
	out, err := cmd.CombinedOutput()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		deadline := "its deadline"
		if d, ok := ctx.Deadline(); ok {
			deadline = "the fixture deadline " + d.Format(time.RFC3339)
		}
		return out, fmt.Errorf("timeout: %s exceeded %s (child ended with %v): %w", filepath.Base(cmd.Path), deadline, err, context.DeadlineExceeded)
	}
	return out, err
}

// Executable resolves a command name or path through PATH, relative
// components and symlinks to the file a process proof names.
func Executable(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}
	if path, err = filepath.Abs(path); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(path)
}

// SameExecutable reports whether two names resolve to one executable file.
// Windows paths compare case-insensitively. Unresolvable names never match.
func SameExecutable(a, b string) bool {
	ra, err := Executable(a)
	if err != nil {
		return false
	}
	rb, err := Executable(b)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}

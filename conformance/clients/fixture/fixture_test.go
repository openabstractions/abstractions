package fixture

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A re-executed test binary serves as the child process.
func TestMain(m *testing.M) {
	switch os.Getenv("OA_FIXTURE_CHILD") {
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "fail":
		os.Stdout.WriteString("child failed on its own\n")
		os.Exit(3)
	}
	os.Exit(m.Run())
}

func child(ctx context.Context, t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, self)
	cmd.Env = append(os.Environ(), "OA_FIXTURE_CHILD="+mode)
	return cmd
}

func TestOutputNamesExpiredBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Output(ctx, child(ctx, t, "sleep"))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.HasPrefix(err.Error(), "timeout: ") || !strings.Contains(err.Error(), "fixture deadline") {
		t.Fatalf("expired budget reported as %v", err)
	}
	// Control: the bare child error names no timeout.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, bare := child(ctx2, t, "sleep").CombinedOutput(); bare == nil || strings.Contains(bare.Error(), "timeout") {
		t.Fatalf("bare child error already named the timeout: %v", bare)
	}
}

func TestOutputKeepsChildFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := Output(ctx, child(ctx, t, "fail"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 || errors.Is(err, context.DeadlineExceeded) || !strings.Contains(string(out), "failed on its own") {
		t.Fatalf("child failure became %v with %q", err, out)
	}
}

func TestSameExecutableResolvesLinksAndCase(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "probe.exe")
	if err := os.WriteFile(real, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !SameExecutable(real, filepath.Join(dir, ".", "probe.exe")) {
		t.Fatal("relative component changed identity")
	}
	if runtime.GOOS == "windows" && !SameExecutable(real, strings.ToUpper(real)) {
		t.Fatal("Windows case changed identity")
	}
	other := filepath.Join(dir, "other.exe")
	if err := os.WriteFile(other, []byte("y"), 0o755); err != nil {
		t.Fatal(err)
	}
	if SameExecutable(real, other) || SameExecutable(real, filepath.Join(dir, "absent.exe")) {
		t.Fatal("different or absent executables matched")
	}
	link := filepath.Join(dir, "python3")
	if err := os.Symlink(real, link); err != nil {
		t.Logf("symlink unavailable here (%v); link resolution runs where symlinks are permitted", err)
		return
	}
	if !SameExecutable(link, real) {
		t.Fatal("symlinked executable did not resolve to its target")
	}
}

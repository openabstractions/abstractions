package nativecli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexCLIFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	program := filepath.Join(dir, "codex.exe")
	if err := os.WriteFile(program, []byte("inert CLI fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	return dir, program
}

func TestCodexCLISelectionHoldsDeduplicatesAndRechecks(t *testing.T) {
	dir, program := codexCLIFixture(t)
	calls := 0
	selected, err := selectCodexPaths(context.Background(), dir+string(os.PathListSeparator)+dir, func(ctx context.Context, path string) ([]byte, error) {
		calls++
		if path != program {
			t.Fatal(path)
		}
		if err := os.WriteFile(path, []byte("replaced"), 0600); err == nil {
			t.Fatal("probe could replace held image")
		}
		if err := os.Rename(path, path+".moved"); err == nil {
			t.Fatal("probe could rename held image")
		}
		return []byte("codex-cli 0.164.0-alpha.11.1\r\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer selected.Close()
	if calls != 1 || selected.Program() != program || selected.FileKey() == "" || selected.Version() != "0.164.0-alpha.11.1" {
		t.Fatal(selected, calls)
	}
	if err := selected.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := selected.Close(); err != nil {
		t.Fatal(err)
	}
	if err := selected.Check(context.Background()); err == nil {
		t.Fatal("closed selection executable")
	}
	if err := os.WriteFile(program, []byte("after join"), 0600); err != nil {
		t.Fatal("hold leaked", err)
	}
}

func TestCodexCLISelectionRefusesAmbiguityBeforeProbe(t *testing.T) {
	a, first := codexCLIFixture(t)
	b, second := codexCLIFixture(t)
	called := false
	if _, err := selectCodexPaths(context.Background(), a+string(os.PathListSeparator)+b, func(context.Context, string) ([]byte, error) { called = true; return nil, nil }); err == nil {
		t.Fatal("ambiguous images accepted")
	}
	if called {
		t.Fatal("ambiguous executable ran")
	}
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("released"), 0600); err != nil {
			t.Fatal("hold leaked", err)
		}
	}
}

func TestCodexCLISelectionFailureReleasesAndDoesNotFallback(t *testing.T) {
	for _, output := range []string{"", "other-cli 1.2.3\n", "codex-cli 1.2.3\nextra", "codex-cli 1.2.3\x00", strings.Repeat("x", 257)} {
		t.Run(output[:min(len(output), 20)], func(t *testing.T) {
			dir, program := codexCLIFixture(t)
			if _, err := selectCodexPaths(context.Background(), dir, func(context.Context, string) ([]byte, error) { return []byte(output), nil }); err == nil {
				t.Fatal("invalid version accepted")
			}
			if err := os.WriteFile(program, []byte("released"), 0600); err != nil {
				t.Fatal("hold leaked", err)
			}
		})
	}
	dir, program := codexCLIFixture(t)
	if _, err := selectCodexPaths(context.Background(), dir, func(context.Context, string) ([]byte, error) { return nil, errors.New("probe unavailable") }); err == nil {
		t.Fatal("probe failure accepted")
	}
	if err := os.WriteFile(program, []byte("released"), 0600); err != nil {
		t.Fatal("hold leaked", err)
	}
}

func TestCodexCLISelectionBoundedCancellationMissingAndLinked(t *testing.T) {
	called := false
	probe := func(context.Context, string) ([]byte, error) { called = true; return []byte("codex-cli 1.2.3"), nil }
	for _, path := range []string{"", t.TempDir(), "relative", strings.Repeat(t.TempDir()+string(os.PathListSeparator), 257)} {
		if _, err := selectCodexPaths(context.Background(), path, probe); err == nil {
			t.Fatal("unsupported search accepted", path)
		}
	}
	dir, program := codexCLIFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := selectCodexPaths(ctx, dir, probe); err == nil {
		t.Fatal("cancel accepted")
	}
	if err := os.Link(program, program+".alias"); err != nil {
		t.Fatal(err)
	}
	if _, err := selectCodexPaths(context.Background(), dir, probe); err == nil {
		t.Fatal("multiply named image accepted")
	}
	if called {
		t.Fatal("unadmitted executable probed")
	}
}

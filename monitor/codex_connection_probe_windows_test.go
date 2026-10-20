package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// These opt-in tests use actual native images and the bounded process owner.
// TestMain still redirects per-user folders: these are not normal installed
// profile or GUI-readiness qualifications.
func TestCodexConnectionActualBoundedVersion(t *testing.T) {
	if os.Getenv("OA_TEST_CODEX_REGISTERED_PACKAGE") != "1" {
		t.Skip("requires explicitly selected current-user Codex package")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	called := false
	cli, err := setup.SelectCodexCLI(ctx, func(ctx context.Context, program string) ([]byte, error) {
		called = true
		return runCodexPanelCommand(ctx, program, "--version")
	})
	if err != nil {
		t.Fatalf("bounded version called=%t error=%q", called, err.Error())
	}
	defer cli.Close()
	if err := cli.Check(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("isolated-home bounded version called=%t valid=%t", called, cli.Version() != "")
}

func TestCodexConnectionActualReadOnlyFactory(t *testing.T) {
	if os.Getenv("OA_TEST_CODEX_REGISTERED_PACKAGE") != "1" {
		t.Skip("requires explicitly selected current-user Codex package")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	panel := new(servicePanel)
	view, err := panel.codexConnectionsForPanel().read(ctx)
	if err != nil {
		t.Fatalf("read reason=%s error=%q", view.Reason, err.Error())
	}
	t.Logf("isolated-home supported=%t available=%t outcome=%s", view.Supported, view.Available, view.Outcome)
}

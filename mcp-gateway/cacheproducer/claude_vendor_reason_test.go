package cacheproducer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

// claudeVendorFailures are failures Claude Code prints for plugin commands,
// each with the closed reason the gateway reports for it.
var claudeVendorFailures = map[string]string{
	"folder_missing":      `Error: ENOENT: no such file or directory, scandir 'C:\Program Files\OpenAbstractions\claude-plugin-user'`,
	"marketplace_missing": `Error: Marketplace "oa-local-1f2e" not found`,
	"plugin_missing":      `Error: Plugin "openabstractions@oa-local-1f2e" not found in marketplace "oa-local-1f2e"`,
	"name_conflict":       `Error: Marketplace 'oa-local-1f2e' already exists with a different source`,
	"invalid_plugin":      `Plugin validation failed: .claude-plugin/plugin.json: invalid manifest`,
	"access_denied":       `Error: EPERM: operation not permitted, rename 'C:\Users\x\.claude\plugins\known_marketplaces.json'`,
	"exit":                `Something new went wrong`,
}

// TestFakeClaudeChild is the fake Claude Code: it prints one vendor failure
// and exits 1.
func TestFakeClaudeChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-2] != "oa-fake-claude" {
		return
	}
	fmt.Fprintln(os.Stderr, claudeVendorFailures[os.Args[len(os.Args)-1]])
	os.Exit(1)
}

// A failed vendor plugin command reaches the Panel as one closed reason; the
// vendor's own text stays in the gateway.
func TestClaudeVendorFailureBecomesClosedReason(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for want, text := range claudeVendorFailures {
		t.Run(want, func(t *testing.T) {
			ctx := context.Background()
			output, runErr := setupio.RunClaude(ctx, program, t.TempDir(), []string{"-test.run=^TestFakeClaudeChild$", "--", "oa-fake-claude", want})
			if runErr == nil {
				t.Fatal("fake Claude Code exited 0")
			}
			err := claudeInvocationFailureFrom(ctx, "marketplace_add", true, runErr, output)
			var invocation *ClaudeInvocationError
			if !errors.As(err, &invocation) || invocation.Operation() != "marketplace_add" || invocation.Reason() != want {
				t.Fatalf("reason for %q = %#v", text, err)
			}
			if strings.Contains(err.Error(), "Program Files") || strings.Contains(err.Error(), "oa-local") {
				t.Fatal("vendor text crossed the diagnostic boundary", err)
			}
		})
	}
	if reason := ClaudeVendorReason([]byte("plugin list printed nothing useful")); reason != "" {
		t.Fatal("unmatched output classified", reason)
	}
}

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These exact default outputs are also asserted by the actual gateway C1
// output tests with nonempty lifecycle metadata present in its owned state.
func TestHelperCauseC1DefaultOutputsReachClosedPanelConsumers(t *testing.T) {
	claude, err := os.ReadFile(filepath.Join("..", "mcp-gateway", "testdata", "helper-cause-claude-default.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := &claudePanelConnections{state: func() (string, error) { return t.TempDir(), nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "--helper-lifecycle" {
				t.Fatal("Panel requested diagnostic output")
			}
		}
		return claude, nil
	}}
	view, err := c.read(context.Background())
	if err != nil || view.Outcome != "page" || len(view.Connections) != 0 {
		t.Fatal(view, err)
	}
	codex, err := os.ReadFile(filepath.Join("..", "mcp-gateway", "testdata", "helper-cause-codex-default.json"))
	if err != nil {
		t.Fatal(err)
	}
	if view, err := decodeCodexView(codex); err != nil || view.Outcome != "page" || view.Scope != "user" || view.Registration != "absent" {
		t.Fatal(view, err)
	}
	cp := codexPanelFixture(t)
	cp.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		for _, arg := range args {
			if arg == "--helper-lifecycle" {
				t.Fatal("Panel requested diagnostic output")
			}
		}
		return codex, nil
	}
	if view, err := cp.read(context.Background()); err != nil || view.Outcome != "page" || view.Registration != "absent" {
		t.Fatal(view, err)
	}
}

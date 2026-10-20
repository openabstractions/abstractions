package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestAgentCLIConcreteSelectorFailureIsNilAtProductionPanelBoundary(t *testing.T) {
	selection, cancel := context.WithCancel(context.Background())
	cancel()
	p := new(servicePanel)
	claude := p.claudeConnectionsForPanel()
	codex := p.codexConnectionsForPanel()
	for name, factory := range map[string]func(context.Context) (claudePanelCLI, error){"claude": claude.cli, "codex": codex.cli} {
		t.Run(name, func(t *testing.T) {
			cli, err := factory(selection)
			if err == nil || cli != nil {
				t.Fatal("failed concrete selector became a nonnil Panel interface", cli, err)
			}
		})
	}
	if image, err := panelGatewayImage(selection); err == nil || image != nil {
		t.Fatal("failed gateway image became nonnil Panel interface", image, err)
	}
	if image, _, _, err := p.updateAgentHelper(selection, setup.AgentClaude, nil); err == nil || image != nil {
		t.Fatal("failed helper update became nonnil Panel interface", image, err)
	}
	// Reuse the actual production closure, forcing its concrete Windows
	// selector failure while the read-only inventory still has a live context.
	production := claude.cli
	claude.cli = func(context.Context) (claudePanelCLI, error) { return production(selection) }
	claude.state = func() (string, error) { return t.TempDir(), nil }
	claude.image = func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }
	claude.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 0 || args[0] != "connections" {
			t.Fatal("selector failure ran a child or setup", args)
		}
		return []byte(`{"outcome":"page","connections":[]}`), nil
	}
	claude.picker = func(context.Context) (string, error) { return "", nil }
	claude.rows = []claudeSavedConnection{{Name: "stale"}}
	view, err := claude.read(context.Background())
	if err != nil || view.Outcome != "page" || view.CLIAvailable || view.CLIReasonCode != "cli_selection" || !view.PickerAvailable || len(view.Connections) != 0 || len(claude.rows) != 0 {
		t.Fatal(view, err)
	}
	claude.close()
	productionCodex := codex.cli
	codex.cli = func(context.Context) (claudePanelCLI, error) { return productionCodex(selection) }
	viewCodex, err := codex.read(context.Background())
	if err == nil || viewCodex.Available || viewCodex.Reason != "cli_selection" {
		t.Fatal(viewCodex, err)
	}
	codex.close()
}

func TestClaudeCLIClosedAppUsesFreshlyAuthenticatedOwnedLocator(t *testing.T) {
	for _, mode := range []string{"closed", "missing", "foreign", "no-saved-entry", "invalid-page"} {
		t.Run(mode, func(t *testing.T) {
			raw := []byte(`{"outcome":"page","connections":[{"kind":"user","registration":"saved","cli_identity":"owned-location-hint"}]}`)
			if mode == "no-saved-entry" {
				raw = []byte(`{"outcome":"page","connections":[]}`)
			}
			if mode == "invalid-page" {
				raw = []byte(`{"outcome":"unknown","connections":[]}`)
			}
			live, authenticated := 0, 0
			selectCLI := func(ctx context.Context) (claudePanelCLI, error) {
				return selectClaudeManagementPage(ctx, raw, nil, func(context.Context, []string, func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
					authenticated++
					if mode == "missing" || mode == "foreign" {
						return nil, errors.New("native file authentication refused")
					}
					return &claudePanelFixtureCLI{}, nil
				}, func(context.Context, func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
					live++
					return nil, errors.New("Claude is closed")
				})
			}
			c := &claudePanelConnections{cli: selectCLI, state: func() (string, error) { return t.TempDir(), nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, command: func(context.Context, string, ...string) ([]byte, error) {
				return []byte(`{"outcome":"page","connections":[]}`), nil
			}}
			view, err := c.read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if mode == "closed" {
				if !view.CLIAvailable || live != 0 || authenticated != 1 {
					t.Fatal("closed app falsely disabled management", view, live, authenticated)
				}
			} else if view.CLIAvailable {
				t.Fatal("unverified command enabled", view)
			}
			if mode == "missing" || mode == "foreign" {
				if live != 0 || view.CLIReasonCode != "cli_saved_verification" || strings.Contains(view.CLIReason, "blank") {
					t.Fatal(view, live)
				}
			}
			if mode == "no-saved-entry" && live != 1 {
				t.Fatal("first connection lost existing native discovery", live)
			}
		})
	}
}

func TestClaudeCLIUnavailableReasonPreservesInventoryAndPicker(t *testing.T) {
	for _, mode := range []string{"private", "virtualized", "saved-virtualized", "check", "native-access"} {
		t.Run(mode, func(t *testing.T) {
			c := &claudePanelConnections{state: func() (string, error) { return t.TempDir(), nil }, image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, picker: func(context.Context) (string, error) { return "", nil }, command: func(context.Context, string, ...string) ([]byte, error) {
				return []byte(`{"outcome":"page","connections":[]}`), nil
			}}
			c.cli = func(context.Context) (claudePanelCLI, error) {
				if mode == "native-access" {
					return nil, os.ErrPermission
				}
				if mode == "virtualized" || mode == "saved-virtualized" {
					refusal := &panelConfigurationError{stage: "command_layout", cause: &layout.Refusal{Reason: layout.VirtualizedProfile, Path: "private-path", Detail: "private-detail"}}
					if mode == "saved-virtualized" {
						return nil, &claudeSavedCLIError{refusal}
					}
					return nil, refusal
				}
				if mode == "check" {
					return &selectionCheckFailureCLI{}, nil
				}
				return nil, errors.New("private-path private-detail")
			}
			view, err := c.read(context.Background())
			if err != nil || view.Outcome != "page" || view.CLIAvailable || view.CLIReasonCode == "" || view.CLIReason == "" || !view.PickerAvailable {
				t.Fatal(view, err)
			}
			raw, _ := json.Marshal(view)
			if strings.Contains(string(raw), "private-path") || strings.Contains(string(raw), "private-detail") {
				t.Fatal("raw selector error exposed", string(raw))
			}
			if (mode == "virtualized" || mode == "saved-virtualized") && (view.CLIReasonCode != "command_virtualized_profile" || !strings.Contains(view.CLIReason, "Start menu")) {
				t.Fatal(view)
			}
			if mode == "native-access" && view.CLIReasonCode != "cli_native_access" {
				t.Fatal(view)
			}
		})
	}
}

type selectionCheckFailureCLI struct{ claudePanelFixtureCLI }

func (*selectionCheckFailureCLI) Check(context.Context) error {
	return errors.New("private-path changed")
}

type selectionCloseFailureCLI struct{ claudePanelFixtureCLI }

func (*selectionCloseFailureCLI) Close() error { return errors.New("private cleanup failure") }
func TestClaudeCLISelectionCleanupFailureClearsAvailability(t *testing.T) {
	c := &claudePanelConnections{rows: []claudeSavedConnection{{Name: "stale"}}, cli: func(context.Context) (claudePanelCLI, error) { return &selectionCloseFailureCLI{}, nil }}
	view, err := c.read(context.Background())
	if err == nil || view.CLIAvailable || view.CLIVersion != "" || view.CLIReasonCode != "cli_cleanup" || len(c.rows) != 0 {
		t.Fatal(view, err, c.rows)
	}
}

func TestAgentSelection1784ActualReadOnlyCLI(t *testing.T) {
	if os.Getenv("OA_AGENT_SELECTION1784_DIAG") != "1" && os.Getenv("OA_CLAUDE_MOD_VALIDATION_FIXTURE") == "" {
		t.Skip("explicit read-only native CLI receiving")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	called := false
	probe := func(ctx context.Context, program string) ([]byte, error) {
		called = true
		return runClaudePanelCommand(ctx, program, "--version")
	}
	selectCLI := selectClaudePanelCLI
	if hint := os.Getenv("OA_CLAUDE_MOD_VALIDATION_CLI"); hint != "" {
		id, err := fileid.Resolve(ctx, fileid.Self, hint, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			t.Fatal(err)
		}
		stored, err := id.Stored()
		if err != nil {
			t.Fatal(err)
		}
		selectCLI = func(ctx context.Context, probe func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
			selected, err := setup.SelectClaudeSavedCLI(ctx, []string{stored.String()}, probe)
			if selected == nil {
				return nil, err
			}
			return selected, err
		}
	}
	cli, err := selectCLI(ctx, probe)
	if err != nil {
		if os.Getenv("OA_CLAUDE_MOD_VALIDATION_FIXTURE") != "" {
			t.Fatal("explicit native mod validation requires an authenticated CLI", err)
		}
		code, _ := claudeCLISelectionFailure(err)
		t.Logf("native selector available=false bounded_version_called=%t reason=%s error=%v; TestMain redirects user folders, normal desktop readiness remains separate", called, code, err)
		if cli != nil {
			t.Fatal("failed diagnostic has nonnil interface")
		}
		return
	}
	defer cli.Close()
	if err := cli.Check(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("native selector available=true bounded_version_called=%t version=%s; isolated-home receiving only", called, cli.Version())
	if fixture := os.Getenv("OA_CLAUDE_MOD_VALIDATION_FIXTURE"); fixture != "" {
		if !filepath.IsAbs(fixture) {
			t.Fatal("native mod fixture must be absolute")
		}
		if err := cli.Check(ctx); err != nil {
			t.Fatal(err)
		}
		raw, err := runClaudePanelCommand(ctx, cli.Program(), "plugin", "validate", fixture, "--json")
		if err != nil {
			t.Fatalf("native mod validation failed: %v %s", err, raw)
		}
		if err := cli.Check(ctx); err != nil {
			t.Fatal(err)
		}
		t.Logf("native mod validator version=%s result=%s", cli.Version(), raw)
	}
}

func TestAgentCLINilConcreteCleanup(t *testing.T) {
	var claude *setup.ClaudeCLI
	var codex *setup.CodexCLI
	for name, close := range map[string]func() error{"claude": claude.Close, "codex": codex.Close} {
		if err := close(); err != nil {
			t.Fatal(name, err)
		}
	}
}

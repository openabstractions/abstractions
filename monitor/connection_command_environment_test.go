package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/supervision"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

func TestPanelConfigurationFailureExplainsStageWithoutVendorData(t *testing.T) {
	for _, reason := range []struct {
		cause error
		want  string
	}{
		{context.DeadlineExceeded, "timed out"},
		{context.Canceled, "was canceled"},
		{fmt.Errorf("private/profile API_KEY=do-not-expose"), "could not be completed"},
	} {
		err := &panelConfigurationError{stage: "command_exit", cause: reason.cause, exitCode: 1, pluginOperation: "validate", pluginReason: "exit"}
		text := err.Error()
		for _, want := range []string{reason.want, "command_exit", "exit: 1", "validate", "reason: exit"} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "private") || strings.Contains(text, "API_KEY") {
			t.Fatal("vendor data exposed", text)
		}
	}
}

func TestPanelPluginDiagnosticRefusesUntrustedOrAmbiguousValues(t *testing.T) {
	for _, tc := range []struct{ raw, operation, reason string }{
		{"vendor output contains a secret\nOA_CLAUDE_PLUGIN_ERROR validate exit\nmore private output", "validate", "exit"},
		{"OA_CLAUDE_PLUGIN_ERROR update timeout", "update", "timeout"},
		{"OA_CLAUDE_PLUGIN_ERROR private/profile exit", "", ""},
		{"OA_CLAUDE_PLUGIN_ERROR validate API_KEY=secret", "", ""},
		{"OA_CLAUDE_PLUGIN_ERROR validate exit secret", "", ""},
		{"OA_CLAUDE_PLUGIN_ERROR validate exit\nOA_CLAUDE_PLUGIN_ERROR update timeout", "", ""},
	} {
		operation, reason := panelPluginDiagnostic([]byte(tc.raw))
		if operation != tc.operation || reason != tc.reason {
			t.Fatalf("got %q/%q, want %q/%q", operation, reason, tc.operation, tc.reason)
		}
	}
}

func TestPanelConfigurationBudgetAllowsPluginOwnerToReport(t *testing.T) {
	for _, action := range []string{"install", "update", "remove"} {
		if got := panelConfigurationBudget([]string{"plugin", "claude", action}); got <= 30*time.Second {
			t.Fatal("Panel preempts the thirty-second plugin owner", action, got)
		}
	}
	for _, args := range [][]string{nil, {"--version"}, {"plugin", "claude", "status"}, {"watch-permission", "claude", "status"}, {"plugin", "other", "install"}} {
		if got := panelConfigurationBudget(args); got != 25*time.Second {
			t.Fatal("ordinary command budget changed", args, got)
		}
	}
}

func TestPanelConfigurationEnvironmentChild(t *testing.T) {
	if len(os.Args) < 2 || os.Args[len(os.Args)-1] != "oa-env-child" {
		return
	}
	value, _ := json.Marshal(map[string]string{"codex": os.Getenv("CODEX_HOME"), "claude": os.Getenv("CLAUDE_CONFIG_DIR")})
	fmt.Print(string(value))
	os.Exit(0)
}

func TestPanelConfigurationRunnerPreservesOnlySelectedVendorHome(t *testing.T) {
	codex, claude := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("CLAUDE_CONFIG_DIR", claude)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, isCodex := range []bool{true, false} {
		args := []string{"-test.run=^TestPanelConfigurationEnvironmentChild$", "--", "oa-env-child"}
		raw, err := runPanelConfigurationCommand(t.Context(), program, args, isCodex)
		if runtime.GOOS != "windows" {
			if !errors.Is(err, supervision.ErrUnsupported) {
				t.Fatalf("unsupported command runner returned %v", err)
			}
			// Verify projection through the same child without Windows process supervision.
			cmd := exec.CommandContext(t.Context(), program, args...)
			cmd.Env, err = panelConfigurationEnvironment(t.TempDir(), isCodex)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = cmd.Output()
		}
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatal(err, string(raw))
		}
		if isCodex && (got["codex"] != codex || got["claude"] != "") {
			t.Fatal(got)
		}
		if !isCodex && (got["claude"] != claude || got["codex"] != "") {
			t.Fatal(got)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "relative-config")
	if _, err := panelConfigurationEnvironment(t.TempDir(), false); err == nil {
		t.Fatal("relative Claude configuration directory admitted")
	}
}

func TestCodexConnectionFailureReasonIsBoundedAndActionable(t *testing.T) {
	c := codexPanelFixture(t)
	c.cli = func(context.Context) (claudePanelCLI, error) {
		return nil, fmt.Errorf("private/path/secret: selection failed")
	}
	view, err := c.read(context.Background())
	if err == nil || view.Available || view.Reason != "cli_selection" || view.Outcome != "unavailable" {
		t.Fatal(view, err)
	}
	raw, _ := json.Marshal(view)
	if strings.Contains(string(raw), "secret") || strings.Contains(string(raw), "private/path") {
		t.Fatal("raw selection error exposed", string(raw))
	}
	c.cli = func(context.Context) (claudePanelCLI, error) {
		return nil, &panelConfigurationError{stage: "command_tmp_root", cause: fmt.Errorf("private/path/secret")}
	}
	view, err = c.read(context.Background())
	if err == nil || view.Reason != "command_tmp_root" || view.Available {
		t.Fatal(view, err)
	}
	var output bytes.Buffer
	if code := reportCodexConnectionAvailability(context.Background(), &output, c.read); code != 1 || !strings.Contains(output.String(), "command_tmp_root") || strings.Contains(output.String(), "private") {
		t.Fatal(code, output.String())
	}
	c.cli = func(context.Context) (claudePanelCLI, error) {
		return nil, &panelConfigurationError{stage: "command_layout", cause: &layout.Refusal{Reason: layout.VirtualizedProfile, Path: "private/path", Detail: "private package"}}
	}
	output.Reset()
	if code := reportCodexConnectionAvailability(context.Background(), &output, c.read); code != 1 || !strings.Contains(output.String(), "command_virtualized_profile") || strings.Contains(output.String(), "private") {
		t.Fatal(code, output.String())
	}
}

func TestCodexConnectionDiagnosticUsesUIReadOnlyPath(t *testing.T) {
	c := codexPanelFixture(t)
	calls := 0
	c.prepare = func(context.Context) (claudePanelImage, error) { t.Fatal("diagnostic prepared image"); return nil, nil }
	c.enroll = func(context.Context, claudePanelImage) (string, error) {
		t.Fatal("diagnostic requested permission")
		return "", nil
	}
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		calls++
		if args[0] != "connections" {
			t.Fatal("mutating command", args)
		}
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Registration: "absent", Program: "private vendor record"})
	}
	var output bytes.Buffer
	if code := reportCodexConnectionAvailability(context.Background(), &output, c.read); code != 0 || calls != 1 {
		t.Fatal(code, calls, output.String())
	}
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "program") {
		t.Fatal("vendor record exposed", output.String())
	}
	if !strings.Contains(output.String(), `"available":true`) {
		t.Fatal(output.String())
	}
	c.cli = func(context.Context) (claudePanelCLI, error) { return nil, fmt.Errorf("private failure") }
	output.Reset()
	if code := reportCodexConnectionAvailability(context.Background(), &output, c.read); code != 1 || calls != 1 || !strings.Contains(output.String(), "cli_selection") || strings.Contains(output.String(), "private") {
		t.Fatal(code, calls, output.String())
	}
	output.Reset()
	if code := reportCodexConnectionAvailability(context.Background(), &output, func(context.Context) (codexConnectionView, error) {
		return codexConnectionView{Outcome: "private result", Reason: "private path"}, nil
	}); code != 1 || strings.Contains(output.String(), "private") {
		t.Fatal(code, output.String())
	}
}

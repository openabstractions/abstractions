package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// codexPanelFixture exercises the Panel's Codex flow on every platform; its
// seams stand in for the Windows-only native steps.
func codexPanelFixture(t *testing.T) *codexPanelConnections {
	t.Helper()
	return &codexPanelConnections{revision: 1, platform: func() bool { return true }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, manager: func(context.Context) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: "manager", key: "manager-key"}, nil
	}, prepare: func(context.Context) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: "codex-agent", key: "agent-key"}, nil
	}, enroll: func(context.Context, claudePanelImage) (string, error) { return "enrolled", nil }, admit: func(_ context.Context, image claudePanelImage) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: image.Program(), key: image.FileKey()}, nil
	}, state: func() (string, error) { return t.TempDir(), nil }}
}
func TestCodexPanelAddAndRemovalUseDifferentAdmittedImages(t *testing.T) {
	c := codexPanelFixture(t)
	calls := 0
	c.command = func(_ context.Context, program string, args ...string) ([]byte, error) {
		calls++
		if args[1] != "codex" {
			t.Fatal(args)
		}
		registration := "saved"
		if args[0] == "connect" {
			if program != "codex-agent" {
				t.Fatal(program)
			}
		} else {
			if program != "manager" || args[len(args)-2] != "--registered-program" {
				t.Fatal(program, args)
			}
			registration = "absent"
		}
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Registration: registration})
	}
	view, err := c.change(context.Background(), false, "1")
	if err != nil || view.Enrollment != "enrolled" {
		t.Fatal(view, err)
	}
	c.current = codexConnectionView{Owned: true, Registration: "saved", Program: "revoked-or-deleted-agent"}
	if _, err := c.change(context.Background(), true, "1"); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
	c.close()
}
func TestCodexPanelStaleAndUnownedRemovalCannotDispatch(t *testing.T) {
	c := codexPanelFixture(t)
	called := false
	c.command = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	if _, err := c.change(context.Background(), false, "old"); err == nil || called {
		t.Fatal("stale action dispatched")
	}
	if _, err := c.change(context.Background(), true, "1"); err == nil || called {
		t.Fatal("unowned removal dispatched")
	}
	c.enroll = func(context.Context, claudePanelImage) (string, error) { return "unknown", errors.New("refused") }
	if _, err := c.change(context.Background(), false, "1"); err == nil || called {
		t.Fatal("enrollment refusal dispatched")
	}
}
func TestCodexPanelScopeIsOrdinaryAndSavedIsNotConnectionProof(t *testing.T) {
	for _, text := range []string{"This account", "desktop, CLI and IDE"} {
		if !strings.Contains(codexConnectionsPage, text) {
			t.Fatal(text)
		}
	}
	if strings.Contains(codexConnectionsPage, "Connected.") {
		t.Fatal("saved configuration claimed a connection")
	}
	if _, err := decodeCodexView([]byte(`{"outcome":"page","scope":"project","registration":"saved"}`)); err == nil {
		t.Fatal("project scope accepted")
	}
}

func TestCodexPanelHookStatusPreservesMCPView(t *testing.T) {
	for _, status := range []string{"absent", "installed", "unavailable", "preserved"} {
		raw := []byte(`{"outcome":"page","scope":"user","registration":"saved","owned":true,"session_start":"` + status + `"}`)
		view, err := decodeCodexView(raw)
		if err != nil || view.Registration != "saved" || !view.Owned || view.SessionStart != status {
			t.Fatal(view, err)
		}
	}
	if _, err := decodeCodexView([]byte(`{"outcome":"page","scope":"user","registration":"saved","session_start":"unknown"}`)); err == nil {
		t.Fatal("unknown hook status accepted")
	}
}

func TestCodexPanelCurrencyRequiresOwnedSavedRegistration(t *testing.T) {
	for _, owned := range []bool{false, true} {
		c := codexPanelFixture(t)
		calls := 0
		c.helperState = func(_ context.Context, program, key string) agentHelperReading {
			calls++
			if program != "owned-helper" || key != "" {
				t.Fatal(program, key)
			}
			return agentHelperReading{State: "update"}
		}
		c.command = func(context.Context, string, ...string) ([]byte, error) {
			return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Owned: owned, Registration: "saved", Program: "owned-helper", HelperState: "current"})
		}
		view, err := c.read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if owned && (calls != 1 || view.HelperState != "update") {
			t.Fatal(view, calls)
		}
		if !owned && (calls != 0 || view.HelperState != "") {
			t.Fatal("unowned registration supplied currency", view, calls)
		}
	}
}

func TestCodexPanelPreservesSelectedHomeAndRefusesUnsupportedPlatform(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	codex, err := panelConfigurationEnvironment(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	claude, err := panelConfigurationEnvironment(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	contains := func(values []string) bool {
		for _, value := range values {
			if value == "CODEX_HOME="+home {
				return true
			}
		}
		return false
	}
	if !contains(codex) || contains(claude) {
		t.Fatal("Codex scope dropped or Claude environment changed")
	}
	c := codexPanelFixture(t)
	c.platform = func() bool { return false }
	called := false
	c.cli = func(context.Context) (claudePanelCLI, error) { called = true; return nil, nil }
	view, err := c.read(context.Background())
	if err == nil || view.Supported || view.Available || called {
		t.Fatal(view, err, called)
	}
	if _, err := c.change(context.Background(), false, "1"); err == nil || called {
		t.Fatal("unsupported setup dispatched")
	}
}

func TestCodexPanelCancellationDoesNotDispatchOrPublishLate(t *testing.T) {
	c := codexPanelFixture(t)
	entered := make(chan struct{})
	done := make(chan error, 1)
	called := false
	c.enroll = func(ctx context.Context, _ claudePanelImage) (string, error) {
		close(entered)
		<-ctx.Done()
		return "unknown", ctx.Err()
	}
	c.command = func(context.Context, string, ...string) ([]byte, error) { called = true; return nil, nil }
	go func() { _, err := c.change(context.Background(), false, "1"); done <- err }()
	<-entered
	c.cancelAction()
	if err := <-done; !errors.Is(err, context.Canceled) || called {
		t.Fatal(err, called)
	}
	c.close()
}

type codexCloseFailure struct{ claudePanelFixtureImage }

func (*codexCloseFailure) Close() error { return errors.New("close unknown") }

func TestCodexPanelCloseFailureClearsSavedClaims(t *testing.T) {
	c := codexPanelFixture(t)
	c.manager = func(context.Context) (claudePanelImage, error) { return &codexCloseFailure{}, nil }
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Owned: true, Registration: "saved", Program: "native-agent"})
	}
	view, err := c.read(context.Background())
	if err == nil || view.Owned || view.Registration != "" || c.current.Owned {
		t.Fatal("unknown retained saved claims", view, err, c.current)
	}
}

func TestCodexPanelPageSuppressesStaleAndClosedResults(t *testing.T) {
	testAgentConnectionController(t, "codex", codexConnectionsPage)
}

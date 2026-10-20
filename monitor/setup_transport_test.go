package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestSetupTransportMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transport.json")
	mode := setupTransportMode{NativeExplicit: true, Page: "setup", Endpoint: "fixture-endpoint", Program: filepath.Join(root, "runtime.exe"), StateDir: root, Flags: []string{"native", "open", "page", "setup-transport", "runtime-endpoint", "runtime-program", "runtime-state-dir"}}
	if err := validateSetupTransportMode(path, mode); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*setupTransportMode){
		"native":   func(m *setupTransportMode) { m.Native = true },
		"default":  func(m *setupTransportMode) { m.NativeExplicit = false },
		"browser":  func(m *setupTransportMode) { m.Open = true },
		"review":   func(m *setupTransportMode) { m.Page = "permission-review" },
		"args":     func(m *setupTransportMode) { m.Args = 1 },
		"endpoint": func(m *setupTransportMode) { m.Endpoint = "" },
		"program":  func(m *setupTransportMode) { m.Program = "" },
		"state":    func(m *setupTransportMode) { m.StateDir = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := mode
			change(&bad)
			if validateSetupTransportMode(path, bad) == nil {
				t.Fatal("unsupported mode accepted")
			}
		})
	}
	installed := setupTransportMode{NativeExplicit: true, Page: "setup", Flags: []string{"native", "open", "page", "setup-transport"}}
	if err := validateSetupTransportMode(path, installed); err != nil {
		t.Fatal(err)
	}
	installed.Flags = append(installed.Flags, "runtime-state-dir")
	if err := validateSetupTransportMode(path, installed); err == nil {
		t.Fatal("explicit runtime flag accepted for installed qualification")
	}
	for _, flag := range []string{"owned-host", "background", "tray", "native-asks", "native-owned-child", "register-application", "remove-application", "review-feature", "operation", "check-webview2"} {
		bad := mode
		bad.Flags = append(append([]string{}, mode.Flags...), flag)
		if validateSetupTransportMode(path, bad) == nil {
			t.Fatal("foreign mode flag accepted: " + flag)
		}
	}
	if err := validateSetupTransportMode("", setupTransportMode{Native: true, Open: true}); err != nil {
		t.Fatal("ordinary startup changed")
	}
}

func TestSetupTransportReadAndRuntimeBinding(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "transport.json")
	input := setup.QualificationInput{RuntimeEndpoint: "fixture-endpoint", RuntimeProgram: filepath.Join(root, "runtime.exe"), RuntimeStateDir: root, Expires: time.Now().Add(time.Minute), Mirrors: []setup.QualificationMirror{}}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readSetupTransport(path)
	if err != nil || got.RuntimeEndpoint != input.RuntimeEndpoint {
		t.Fatalf("bounded input: %+v %v", got, err)
	}
	explicit := &explicitRuntime{Endpoint: input.RuntimeEndpoint, Program: input.RuntimeProgram, StateDir: input.RuntimeStateDir}
	machine := client.New("unserved-fixture")
	for _, field := range []string{"endpoint", "program", "state"} {
		bad := input
		switch field {
		case "endpoint":
			bad.RuntimeEndpoint += "other"
		case "program":
			bad.RuntimeProgram += "other"
		case "state":
			bad.RuntimeStateDir += "other"
		}
		if _, err := setupTransportForPanel(bad, explicit, machine); err == nil {
			t.Fatal("runtime mismatch accepted")
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "transport.json" {
		t.Fatal("startup DATA read performed setup work")
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", setup.QualificationInputMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSetupTransport(path); err == nil {
		t.Fatal("oversize startup input accepted")
	}
}

func TestSetupTransportPerInstanceStillNeedsConfirmation(t *testing.T) {
	root := t.TempDir()
	journal := setup.Journal{Path: filepath.Join(root, "plan.json")}
	machine := client.New("unserved-exact-fixture-runtime")
	transfer := &setup.PublicTransfer{Machine: machine, LocalOnly: true}
	panel, server, listener, _, err := bindPanelPageWithSetupTransfer("127.0.0.1:0", "setup", transfer)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer listener.Close()
	panel.setupJournal = &journal
	runner, _, err := panel.setupRunnerForPanel()
	if err != nil {
		t.Fatal(err)
	}
	if got := runner.Transfer.(setup.PublicTransfer); got.Machine != machine || !got.LocalOnly {
		t.Fatal("per-instance explicit runtime lost")
	}
	ordinary, _, err := (&servicePanel{setupJournal: &journal}).setupRunnerForPanel()
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Transfer.(setup.PublicTransfer).Machine != nil {
		t.Fatal("qualification selection leaked to default Panel")
	}
	defaultRun, err := newPanelRun("setup")
	if err != nil || defaultRun.panel.setupTransfer != nil {
		t.Fatal("default/native construction acquired qualification transport")
	}
	handler := panel.handler("fixture-key")
	for _, tc := range []struct {
		body, key string
		code      int
	}{
		{`{"selected":["comfyui"],"plan_id":"wrong","confirm":false}`, "fixture-key", http.StatusBadRequest},
		{`{"selected":["comfyui"],"plan_id":"wrong","confirm":true}`, "fixture-key", http.StatusConflict},
		{`{"selected":["comfyui"],"plan_id":"wrong","confirm":true}`, "foreign-key", http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodPost, "/setup/install", strings.NewReader(tc.body))
		request.Host = "127.0.0.1"
		request.Header.Set("X-Panel-Key", tc.key)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != tc.code {
			t.Fatalf("normal confirmation bypass: %d %s", response.Code, response.Body.String())
		}
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatal("configuration or refused confirmation created durable setup work")
	}
}

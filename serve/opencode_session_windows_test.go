//go:build windows

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	projectwire "github.com/openabstractions/abstractions/serve/go/opencode/session"
)

func TestOpenCodeGuestUsesInstalledAdapterAndIndependentRuntimeProof(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "verified-plugin")
	if err := os.Mkdir(plugin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"package.json": `{"name":"@openabstractions/opencode","exports":{"./server":"./plugin.js"}}`, "index.js": "verified provider fixture", "plugin.js": "verified startup fixture"} {
		if err := os.WriteFile(filepath.Join(plugin, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := filepath.Join(root, "original-config.json")
	original := []byte(`{"model":"my-oa/qwen","provider":{"my-oa":{"npm":"@openabstractions/opencode@1.0.0","options":{"credential":"chosen-model","runtimeEndpoint":"old endpoint","server":{"program":"old program"}}},"other":{"npm":"@other/provider"}},"plugin":[["@openabstractions/opencode@1.0.0",{"providerId":"my-oa"}]]}`)
	if err := os.WriteFile(config, original, 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "mutable")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeProgram := filepath.Join(root, "selected-runtime.exe")
	selection := bootstrap.Selection{Endpoint: `\\.\pipe\selected-runtime`, Server: listen.ServerExpectation{Principal: identity.User{Kind: "windows", SID: "S-1-5-21-123"}, Program: runtimeProgram}}
	t.Setenv("OA_UNRELATED_SECRET", "never enter guest")
	env, err := openCodeGuestEnvironment(&installedOpenCode{Config: config, Plugin: plugin}, data, `\\.\pipe\session`, filepath.Join(root, "owner.exe"), "S-1-5-21-123", "native-launch", selection)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, item := range env {
		key, value, _ := strings.Cut(item, "=")
		values[key] = value
	}
	if values["PATH"] != os.Getenv("PATH") {
		t.Fatalf("the guest PATH differs from the owner's: %q", values["PATH"])
	}
	if _, found := values["OA_UNRELATED_SECRET"]; found {
		t.Fatal("unrelated secret entered guest")
	}
	for key, want := range map[string]string{"OA_OPENCODE_RUNTIME_PROGRAM": runtimeProgram, "OA_OPENCODE_RUNTIME_ACCOUNT": "S-1-5-21-123", "OA_OPENCODE_SESSION_ID": "native-launch", "LOCALAPPDATA": filepath.Join(data, "local"), "XDG_STATE_HOME": filepath.Join(data, "state")} {
		if values[key] != want {
			t.Fatalf("%s = %q want %q", key, values[key], want)
		}
	}
	raw, err := os.ReadFile(values["OPENCODE_CONFIG"])
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Provider map[string]struct {
			NPM     string `json:"npm"`
			Options struct {
				RuntimeEndpoint string `json:"runtimeEndpoint"`
				Credential      string `json:"credential"`
				Server          struct {
					PrincipalKind int    `json:"principalKind"`
					Principal     string `json:"principal"`
					Program       string `json:"program"`
				} `json:"server"`
			} `json:"options"`
		} `json:"provider"`
		Plugin []json.RawMessage `json:"plugin"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	oa := result.Provider["my-oa"]
	if oa.NPM != openCodeFileURL(filepath.Join(plugin, "index.js")) || oa.Options.RuntimeEndpoint != selection.Endpoint ||
		oa.Options.Server.PrincipalKind != 1 || oa.Options.Server.Principal != "S-1-5-21-123" || oa.Options.Server.Program != runtimeProgram || oa.Options.Credential != "chosen-model" {
		t.Fatalf("configured provider lost verified package/proof or selected model: %+v", oa)
	}
	if len(result.Plugin) != 1 {
		t.Fatalf("unexpected startup plugins: %s", raw)
	}
	registration, options, err := openCodePluginEntry(result.Plugin[0])
	if err != nil || registration != plugin || string(options["providerId"]) != `"my-oa"` {
		t.Fatalf("startup registration not bound to installed adapter/provider: %s %v", raw, err)
	}
	untouched, err := os.ReadFile(config)
	if err != nil || string(untouched) != string(original) {
		t.Fatalf("original config was changed: %v", err)
	}
}

func TestOpenCodeFreshResumeRequiresClosedJobExactNativeBindingsAndUnusedReceipt(t *testing.T) {
	now := time.Now().UTC()
	request := projectwire.ProjectRequest{RequestKey: "request", Folder: "selected", SessionID: "actual-session", MessageID: "actual-message"}
	completion := projectwire.ProjectCompletion{RequestKey: request.RequestKey, SessionID: request.SessionID, MessageID: request.MessageID, Receipt: "receipt", PartID: "actual-part", ToolCallID: "actual-call", CompletedAt: now.UnixMilli()}
	approval := openCodeFolderApproval{Request: request, Receipt: "receipt", Approved: true, ManualAnswer: true, Created: now.Add(-time.Minute), Expires: now.Add(time.Minute), Completion: &completion}
	record := openCodeLaunchRecord{Format: 1, Phase: "closed", ClosedJob: true, ChildPID: 123, ChildStart: now.Add(-time.Hour), Account: "account", OwnerProgramID: "owner-id", StateID: "state-id", RuntimeEndpoint: "endpoint", RuntimeProgramID: "runtime-id", GuestProgramID: "guest-id", PackageRootID: "root-id", Initial: &openCodeFolderApproval{}, Requests: map[string]*openCodeFolderApproval{"request": &approval}}
	check := func(r openCodeLaunchRecord) error {
		_, err := openCodeResumeApproval(r, "receipt", "account", "owner-id", "state-id", "endpoint", "runtime-id", "guest-id", "root-id", now)
		return err
	}
	if err := check(record); err != nil {
		t.Fatalf("known closed completed receipt refused: %v", err)
	}
	for _, alter := range []func(*openCodeLaunchRecord){
		func(r *openCodeLaunchRecord) { r.ClosedJob = false }, func(r *openCodeLaunchRecord) { r.Phase = "running" }, func(r *openCodeLaunchRecord) { r.Phase = "exit-unknown" },
		func(r *openCodeLaunchRecord) { r.Account = "other" }, func(r *openCodeLaunchRecord) { r.OwnerProgramID = "other" }, func(r *openCodeLaunchRecord) { r.StateID = "other" },
		func(r *openCodeLaunchRecord) { r.RuntimeEndpoint = "other" }, func(r *openCodeLaunchRecord) { r.RuntimeProgramID = "other" }, func(r *openCodeLaunchRecord) { r.GuestProgramID = "other" }, func(r *openCodeLaunchRecord) { r.PackageRootID = "other" },
		func(r *openCodeLaunchRecord) { r.Requests["request"].Consumed = true }, func(r *openCodeLaunchRecord) { r.Requests["request"].Completion = nil },
		func(r *openCodeLaunchRecord) { r.Requests["request"].Expires = now }, func(r *openCodeLaunchRecord) { r.Requests["request"].ManualAnswer = false },
	} {
		changed := record
		copyApproval := approval
		changed.Requests = map[string]*openCodeFolderApproval{"request": &copyApproval}
		alter(&changed)
		if check(changed) == nil {
			t.Fatalf("unsafe fresh action accepted: %+v", changed)
		}
	}
}

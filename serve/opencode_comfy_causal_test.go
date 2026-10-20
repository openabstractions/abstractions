package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// verifyOpenCodeComfyImage binds the actual attachment to the one saved output;
// equal host/model labels alone carry no causal output evidence.
func verifyOpenCodeComfyImage(saved, attached []byte, digest string) error {
	if !bytes.Equal(saved, attached) || digest != fmt.Sprintf("sha256:%x", sha256.Sum256(saved)) {
		return fmt.Errorf("Comfy saved output, delivery digest and OpenCode attachment differ")
	}
	return nil
}

func TestOpenCodeComfyImageCorrelationRefusesDifferentOutput(t *testing.T) {
	saved := []byte("saved PNG fixture")
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(saved))
	if err := verifyOpenCodeComfyImage(saved, saved, digest); err != nil {
		t.Fatal(err)
	}
	if err := verifyOpenCodeComfyImage(saved, []byte("another output from the same host/model"), digest); err == nil {
		t.Fatal("accepted independent output with matching route labels")
	}
	if err := verifyOpenCodeComfyImage(saved, saved, fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("other")))); err == nil {
		t.Fatal("accepted a different terminal digest")
	}
}

func TestOpenCodeStagedImageToolControls(t *testing.T) {
	if _, err := findOpenCodeProviderSources(filepath.Clean("..")); err != nil {
		if !privateWorkspace(t) && strings.Contains(err.Error(), "generated JavaScript packages absent") {
			t.Skip("OpenCode image integration requires the generated JavaScript package checkouts")
		}
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is unavailable for the staged image-tool controls")
	}
	provider := stageOpenCodeProvider(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, "--test", "--test-name-pattern", "interrupted operation|terminal refusal|not_granted refusal|edit inputs are refused|successful image attachment remains", filepath.Join(filepath.Dir(provider), "test", "image-tool.test.mjs"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("staged existing image controls: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

func TestOpenCodeStagedImageToolPublicAbsenceSkips(t *testing.T) {
	charter := t.TempDir()
	provider := filepath.Join(charter, "adopters", "opencode")
	if err := os.MkdirAll(provider, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(provider, "index.js"), []byte("// public provider fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serve := filepath.Join(charter, "serve")
	if err := os.Mkdir(serve, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(serve)
	skipped := false
	t.Run("without-generated-siblings", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		TestOpenCodeStagedImageToolControls(t)
	})
	if !skipped {
		t.Fatal("public checkout without generated siblings did not skip the optional integration")
	}
}

func TestOpenCodeNativeImageUsesRegisteredComfyRecipeOnce(t *testing.T) {
	runOpenCodeNativeComfyImage(t, false)
}

// This is a source-derived synthetic terminal error, not a captured R3 reply.
func TestOpenCodeNativeImageReceivesSyntheticTerminalError(t *testing.T) {
	runOpenCodeNativeComfyImage(t, true)
}

func nativeComfyEnvironment(t *testing.T, root, bootstrap, addon, library string) ([]string, error) {
	t.Helper()
	if !filepath.IsAbs(bootstrap) {
		return nil, fmt.Errorf("native image proof requires the reviewed offline bootstrap fixture")
	}
	for _, name := range []string{"config", "cache"} {
		info, err := os.Lstat(filepath.Join(bootstrap, name))
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("native image bootstrap %s is unavailable", name)
		}
		copyTree(t, filepath.Join(bootstrap, name), filepath.Join(root, "xdg-"+name, "opencode"))
	}
	return isolatedOpenCodeEnvironment(t, root, addon, library), nil
}

func TestOpenCodeNativeImageBootstrapReachesBothBranches(t *testing.T) {
	for _, branch := range []string{"success", "error"} {
		t.Run(branch, func(t *testing.T) {
			seed, root := t.TempDir(), t.TempDir()
			for _, name := range []string{"config", "cache"} {
				if err := os.Mkdir(filepath.Join(seed, name), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(seed, name, "pinned"), []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			environment, err := nativeComfyEnvironment(t, root, seed, "", "fixture.dll")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"config", "cache"} {
				data, err := os.ReadFile(filepath.Join(root, "xdg-"+name, "opencode", "pinned"))
				if err != nil || string(data) != name || !slices.Contains(environment, "XDG_"+strings.ToUpper(name)+"_HOME="+filepath.Join(root, "xdg-"+name)) {
					t.Fatalf("bootstrap/XDG %s: %q %v", name, data, err)
				}
			}
		})
	}
	for _, missing := range []string{"", "relative", t.TempDir()} {
		if _, err := nativeComfyEnvironment(t, t.TempDir(), missing, "", "fixture.dll"); err == nil {
			t.Fatal("accepted missing bootstrap before native opt-in")
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "opencode_comfy_causal_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls int
	for _, declaration := range file.Decls {
		fn, ok := declaration.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runOpenCodeNativeComfyImage" {
			continue
		}
		for _, statement := range fn.Body.List {
			assignment, ok := statement.(*ast.AssignStmt)
			if !ok {
				continue
			}
			for _, expression := range assignment.Rhs {
				call, ok := expression.(*ast.CallExpr)
				if !ok {
					continue
				}
				if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "nativeComfyEnvironment" {
					calls++
				}
			}
		}
	}
	if calls != 1 {
		t.Fatal("both native image branches must prepare bootstrap without a terminalError conditional")
	}
}

func runOpenCodeNativeComfyImage(t *testing.T, terminalError bool) {
	t.Helper()
	opencode, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_LIBRARY")
	if opencode == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY and OA_IPC_LIBRARY for the native causal image proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	raw, err := os.ReadFile("testdata/comfy_recipe_bf16.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != "e71f5dc126d130c9f64c0c43cbef1d806c69634eec53ed8f8dc4f0431fb9fcbb" {
		t.Fatalf("pinned recipe: %v", err)
	}
	var recipe map[string]any
	if err := json.Unmarshal(raw, &recipe); err != nil {
		t.Fatal(err)
	}
	const prompt = "A watercolor fox reading a book under a tree"
	const promptID = "oa-causal-comfy-prompt"
	expectedGraph := recipe["graph"].(map[string]any)
	expectedGraph["74"].(map[string]any)["inputs"].(map[string]any)["text"] = prompt
	assets := recipe["components"].([]any)
	for _, asset := range assets {
		asset.(map[string]any)["state"] = "ready"
	}
	manifest := map[string]any{"id": recipe["id"], "variant": recipe["variant"], "manifest_sha256": recipe["manifest_sha256"], "ready": true, "assets": assets}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(t.TempDir(), "out.png")
	var posts, histories, views atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			fmt.Fprint(w, `["checkpoints","diffusion_models","text_encoders","vae"]`)
		case "/models/checkpoints":
			fmt.Fprint(w, `[]`)
		case "/system_stats":
			fmt.Fprint(w, `{"devices":[]}`)
		case "/abstraction/recipes":
			_ = json.NewEncoder(w).Encode(map[string]any{"schema": "oa.comfy.recipes@1", "recipes": []any{map[string]any{"recipe": json.RawMessage(raw), "manifest_path": "/fixture-manifest"}}})
		case "/fixture-manifest":
			_ = json.NewEncoder(w).Encode(manifest)
		case "/prompt":
			if r.Method != http.MethodPost || posts.Add(1) != 1 {
				t.Error("expected exactly one prompt POST")
				http.Error(w, "duplicate", http.StatusConflict)
				return
			}
			var body struct {
				Prompt map[string]any `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body.Prompt, expectedGraph) {
				t.Errorf("request did not use the exact pinned recipe and prompt: %v", err)
			}
			if !terminalError {
				if err := os.WriteFile(saved, encoded.Bytes(), 0o600); err != nil {
					t.Error(err)
				}
			}
			fmt.Fprintf(w, `{"prompt_id":%q}`, promptID)
		case "/history/" + promptID:
			histories.Add(1)
			if terminalError {
				_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{
					"status": map[string]any{"completed": false, "status_str": "error",
						"messages": []any{"synthetic private exception details must remain upstream"}},
					"outputs": map[string]any{},
				}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{promptID: map[string]any{"status": map[string]any{"completed": true}, "outputs": map[string]any{recipe["output_node"].(string): map[string]any{"images": []any{map[string]any{"filename": "out.png"}}}}}})
		case "/view":
			views.Add(1)
			if r.URL.Query().Get("filename") != "out.png" || posts.Load() != 1 {
				t.Error("view did not select the submitted prompt's saved image")
			}
			data, err := os.ReadFile(saved)
			if err != nil {
				t.Error(err)
			}
			_, _ = w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	if out, err := runInference(t, append([]string{"host", "add", "comfyui", "--base", server.URL}, endpoint...)...); err != nil {
		t.Fatalf("register Comfy host: %v\n%s", err, out)
	}
	providerProgram := copyTestBinary(t, "oa-opencode-causal-provider")
	providerState, pidDir := t.TempDir(), t.TempDir()
	args, _ := json.Marshal(map[string]string{"model": recipe["model_alias"].(string), "prompt": prompt, "size": "512x512"})
	if err := os.WriteFile(filepath.Join(providerState, "image-call.json"), args, 0o600); err != nil {
		t.Fatal(err)
	}
	self, _ := os.Executable()
	declare := []string{"add", "opencode-causal", "--program", providerProgram, "--provider-endpoint", "opencode-causal", "--contract", inference.Contract,
		"--guarantee", fixtureGuarantee, "--guarantee", "abstraction.inference/local-only@1", "--model", "fixture-model",
		"--arg", openCodeProviderFixtureArg, "--arg", "{endpoint}", "--arg", providerState, "--arg", "image", "--arg", pidDir, "--arg", self}
	if out, err := runProvider(t, append(declare, endpoint...)...); err != nil {
		t.Fatalf("declare chat fixture: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = runProvider(t, append([]string{"remove", "opencode-causal"}, endpoint...)...) })
	setCompleteRule(t, options.endpoint, filepath.Clean(opencode), "opencode-causal", true)
	setCompleteRule(t, options.endpoint, filepath.Clean(opencode), "comfyui", true)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(options.endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	account, _ := user.Current()
	for _, action := range []string{contentReadAction, contentWriteAction} {
		page, err := operator.ListPolicyContext(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		edit, err := operator.SetRuleContext(ctx, page.Revision, rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(opencode)}, Action: action, Resource: "output:image", Permit: true})
		if err != nil || edit.Outcome.String() != "applied" {
			t.Fatalf("output grant: %+v %v", edit, err)
		}
	}
	providerIndex := stageOpenCodeProvider(t)
	project := t.TempDir()
	config := map[string]any{"agent": map[string]any{"title": map[string]any{"disable": true}}, "plugin": []any{[]any{filepath.Dir(providerIndex), map[string]any{"tools": map[string]any{"generate_image": true}}}},
		"provider": map[string]any{"openabstractions": map[string]any{"name": "OpenAbstractions", "npm": fileURL(providerIndex),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "local", "guarantees": []string{fixtureGuarantee},
				"server": map[string]any{"principalKind": 1, "principal": account.Uid, "program": self}},
			"models": map[string]any{"fixture-model": map[string]any{"name": "OA fixture", "tool_call": true, "attachment": false, "modalities": map[string]any{"input": []string{"text"}, "output": []string{"text"}}, "limit": map[string]any{"context": 32768, "output": 4096}}}}}, "model": "openabstractions/fixture-model"}
	configBytes, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	call, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	command := exec.CommandContext(call, opencode, "run", "--auto", "--format", "json", "--model", "openabstractions/fixture-model", "--dir", project, "Use the available image tool once and report its result.")
	envRoot := t.TempDir()
	environment, err := nativeComfyEnvironment(t, envRoot, os.Getenv("OA_OPENCODE_NATIVE_BOOTSTRAP"), os.Getenv("OA_IPC_NODE"), library)
	if err != nil {
		t.Fatal(err)
	}
	overrides := map[string]string{"OPENCODE_CONFIG": filepath.Join(project, "opencode.json"), "OPENCODE_DISABLE_AUTOUPDATE": "1", "OPENCODE_DISABLE_MODELS_FETCH": "1"}
	for _, key := range []string{"HOME", "USERPROFILE", "LOCALAPPDATA", "APPDATA", "TEMP", "TMP"} {
		path := filepath.Join(envRoot, strings.ToLower(key))
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		overrides[key] = path
	}
	command.Dir = project
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			command.Env = append(command.Env, entry)
		}
	}
	for key, value := range overrides {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenCode image: %v\n%s", err, output)
	}
	var attachments [][]byte
	var walk func(any)
	walk = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if v["type"] == "file" && v["mime"] == "image/png" {
				if url, ok := v["url"].(string); ok && strings.HasPrefix(url, "data:image/png;base64,") {
					data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/png;base64,"))
					if err != nil {
						t.Error(err)
					} else {
						attachments = append(attachments, data)
					}
				}
			}
			for _, child := range v {
				walk(child)
			}
		case []any:
			for _, child := range v {
				walk(child)
			}
		}
	}
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event any
		if json.Unmarshal(line, &event) == nil {
			walk(event)
		}
	}
	if terminalError {
		requests, err := os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
		if _, statErr := os.Stat(saved); !os.IsNotExist(statErr) {
			t.Fatalf("synthetic error created an image: %v", statErr)
		}
		if err != nil || posts.Load() != 1 || histories.Load() != 1 || views.Load() != 0 || len(attachments) != 0 ||
			!bytes.Contains(requests, []byte(`"Kind":"tool_result"`)) ||
			!bytes.Contains(requests, []byte("Image generation unavailable: upstream:status.")) ||
			bytes.Contains(requests, []byte("synthetic private exception")) {
			t.Fatalf("synthetic terminal result err=%v posts=%d histories=%d views=%d attachments=%d\n%s\n%s", err, posts.Load(), histories.Load(), views.Load(), len(attachments), output, requests)
		}
		t.Log("synthetic terminal image error delivered as tool_result; no image or native crash")
		return
	}
	data, err := os.ReadFile(saved)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(data))
	if err != nil || posts.Load() != 1 || histories.Load() < 1 || views.Load() != 1 || len(attachments) != 1 || !bytes.Contains(output, []byte(digest)) {
		t.Fatalf("causal result saved=%v posts=%d histories=%d views=%d attachments=%d\n%s", err, posts.Load(), histories.Load(), views.Load(), len(attachments), output)
	}
	if err := verifyOpenCodeComfyImage(data, attachments[0], digest); err != nil {
		t.Fatal(err)
	}
	requests, err := os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
	if err != nil || !bytes.Contains(requests, []byte(`"Kind":"tool_result"`)) ||
		!bytes.Contains(requests, []byte(digest)) ||
		!bytes.Contains(requests, []byte("this model does not support image input")) ||
		bytes.Contains(requests, []byte(base64.StdEncoding.EncodeToString(data))) {
		t.Fatalf("text-only follow-up lost its result or received image pixels: %v\n%s", err, requests)
	}
	t.Logf("one prompt %s, model %s, attachment %s (%d bytes)", promptID, recipe["model_alias"], digest, len(data))
}

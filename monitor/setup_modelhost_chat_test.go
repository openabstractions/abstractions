package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type codingPanelTransfer struct {
	*panelTransfer
	failure  error
	submits  int
	reserves int
}

func (f *codingPanelTransfer) Reserve(ctx context.Context, pin setup.PinnedDownload, key string) (setup.WorkIdentity, error) {
	f.reserves++
	return f.panelTransfer.Reserve(ctx, pin, key)
}

func (f *codingPanelTransfer) Submit(context.Context, setup.PinnedDownload, setup.WorkIdentity) error {
	f.submits++
	return f.failure
}

func TestCodingModelHandlerRequiresExactConsentAndAcceptedWork(t *testing.T) {
	plan := setup.CodingModelPlan()
	root := t.TempDir()
	transfer := &codingPanelTransfer{panelTransfer: &panelTransfer{}}
	c := setup.ModelController{Plan: &plan, JournalPath: filepath.Join(root, "coding.json"), StageDir: root, ProviderRoot: root, Transfer: transfer, LocalResults: true, FreeBytes: func(string) (uint64, error) { return 1 << 50, nil }}
	p := &servicePanel{codingController: &c}
	h := p.handler("setup-test-key")
	call := installHandlerFor(h)
	a := setup.ModelApproval{PlanID: plan.ID, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Licenses: plan.Licenses}
	body := func(action string, approval setup.ModelApproval) string {
		raw, _ := json.Marshal(map[string]any{"action": action, "confirm": true, "approval": approval})
		return string(raw)
	}
	if got := call("GET", "/setup/modelhost/coding", ""); got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	bad := a
	bad.PlanID = "stale"
	if got := call("POST", "/setup/modelhost/coding", body("start", bad)); got.Code != 400 || transfer.submits != 0 {
		t.Fatalf("stale plan reached submit: %d", got.Code)
	}
	transfer.failure = errors.New("acceptance uncertain")
	if got := call("POST", "/setup/modelhost/coding", body("start", a)); got.Code != 409 || strings.Contains(got.Body.String(), `"accepted":true`) {
		t.Fatalf("uncertain accepted: %d %s", got.Code, got.Body.String())
	}
	first := transfer.work
	transfer.failure = nil
	// A new Panel/controller reads the same intent after the window closes.
	reopened := c
	h = (&servicePanel{codingController: &reopened}).handler("setup-test-key")
	call = installHandlerFor(h)
	got := call("POST", "/setup/modelhost/coding", body("start", a))
	if got.Code != 200 || !strings.Contains(got.Body.String(), `"accepted":true`) || transfer.work.Key != first.Key {
		t.Fatalf("resume failed: %d %s", got.Code, got.Body.String())
	}
	before := transfer.submits
	if got := call("GET", "/setup/modelhost/coding", ""); got.Code != 200 || transfer.submits != before {
		t.Fatal("status dispatched new work")
	}
	if got := call("POST", "/setup/modelhost/coding", `{"confirm":true,"foreign":true}`); got.Code != 400 {
		t.Fatal("unknown request field accepted")
	}
}

func TestModelHostEngineFormRemovesTempArchiveWhenNextPartIsMalformed(t *testing.T) {
	tempDir := t.TempDir()
	var createdPath string
	originalCreate := createModelHostEngineTemp
	createModelHostEngineTemp = func(_, pattern string) (*os.File, error) {
		file, err := os.CreateTemp(tempDir, pattern)
		if err == nil {
			createdPath = file.Name()
		}
		return file, err
	}
	t.Cleanup(func() { createModelHostEngineTemp = originalCreate })

	body := "--engine-boundary\r\n" +
		"Content-Disposition: form-data; name=\"archive\"; filename=\"engine.zip\"\r\n" +
		"Content-Type: application/zip\r\n\r\n" +
		"zip bytes\r\n" +
		"--engine-boundary\r\n" +
		"malformed-header-without-colon\r\n\r\n" +
		"--engine-boundary--\r\n"
	request := httptest.NewRequest(http.MethodPost, "/setup/modelhost/engine", strings.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=engine-boundary")
	_, _, _, err := modelHostEngineForm(httptest.NewRecorder(), request, int64(len(body))+1024)
	if err == nil {
		t.Fatal("malformed follow-up multipart part was accepted")
	}
	if createdPath == "" {
		t.Fatal("archive temp file was not created")
	}
	if _, statErr := os.Stat(createdPath); !os.IsNotExist(statErr) {
		t.Fatalf("archive temp file survived parse failure: %v", statErr)
	}
}

func TestModelHostEngineFormRefusesNetworkTempDirectoryBeforeArchiveWrite(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows local-drive identity rules")
	}
	originalTempDir := modelHostSystemTempDir
	modelHostSystemTempDir = func() string { return `\\server\share\temp` }
	t.Cleanup(func() { modelHostSystemTempDir = originalTempDir })
	createCalled := false
	originalCreate := createModelHostEngineTemp
	createModelHostEngineTemp = func(string, string) (*os.File, error) {
		createCalled = true
		return nil, errors.New("unexpected archive creation")
	}
	t.Cleanup(func() { createModelHostEngineTemp = originalCreate })

	body := "--engine-boundary\r\n" +
		"Content-Disposition: form-data; name=\"archive\"; filename=\"engine.zip\"\r\n\r\n" +
		"zip bytes\r\n--engine-boundary--\r\n"
	request := httptest.NewRequest(http.MethodPost, "/setup/modelhost/engine", strings.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary=engine-boundary")
	_, _, _, err := modelHostEngineForm(httptest.NewRecorder(), request, int64(len(body))+1024)
	if err == nil || !strings.Contains(err.Error(), "local drive") || createCalled {
		t.Fatalf("network temp path was not refused before writing: created=%v err=%v", createCalled, err)
	}
}

func TestModelHostMutationPlanRequiresTheCurrentPlanID(t *testing.T) {
	p := &servicePanel{}
	if _, _, err := p.modelHostMutationPlan(context.Background(), ""); err == nil || !strings.Contains(err.Error(), "plan ID") {
		t.Fatalf("missing plan ID was accepted: %v", err)
	}
}

// R16: the model host facts read without an approved current plan (an
// earlier release's plan, or none) and name no plan; a change sent with that
// reply's empty plan ID is refused.
func TestModelHostFactsReadWithoutAPlanAndChangesNeedOne(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	feature := setup.FeatureStatus{Available: true, Reason: "Add the ModelHost feature."}
	p := &servicePanel{setupJournal: &journal, modelHostFeatureCheck: func() (setup.FeatureStatus, error) { return feature, nil }}
	h := p.handler("setup-test-key")
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+"?k=setup-test-key", strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		h.ServeHTTP(got, req)
		return got
	}
	got := send(http.MethodGet, "/setup/modelhost/chat", "")
	var view modelHostChatView
	if err := json.Unmarshal(got.Body.Bytes(), &view); got.Code != http.StatusOK || err != nil {
		t.Fatalf("facts read: %d %s", got.Code, got.Body.String())
	}
	if view.PlanID != "" || !view.Available || !view.Feature.Available || view.Feature.Installed || view.Reason != feature.Reason {
		t.Fatalf("the facts did not come from the feature check: %+v", view)
	}
	body, _ := json.Marshal(modelHostPlanRequest{PlanID: view.PlanID, Confirm: true})
	if got := send(http.MethodPost, "/setup/modelhost/feature/add", string(body)); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "plan ID") {
		t.Fatalf("a change without the current plan was not refused: %d %s", got.Code, got.Body.String())
	}
}

func TestModelHostInventoryResolutionIsLocalOnly(t *testing.T) {
	var resolved client.Requirements
	p := &servicePanel{modelHostInventoryResolve: func(_ context.Context, requirements client.Requirements) (modelHostInventoryReader, error) {
		resolved = requirements
		return nil, nil
	}}
	if _, err := p.modelHostReadInventory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolved.Scope != client.ScopeLocal {
		t.Fatalf("inventory resolution scope = %v, want local", resolved.Scope)
	}
}

func TestModelHostEngineStateRejectsReceiptOutsidePinnedCacheFolder(t *testing.T) {
	base := t.TempDir()
	pin := modelHostPin{
		Release: "b10825", Archive: "llama-b10825-bin-win-vulkan-x64.zip",
		SHA256:    strings.Repeat("a", 64),
		EngineDir: filepath.Join(base, "cache", "OpenAbstractions", "engines", "llama.cpp-b10825"),
	}
	outside := filepath.Join(base, "outside")
	for _, dir := range []string{pin.EngineDir, outside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := setup.Home{Config: filepath.Join(base, "config")}
	receipt := modelHostEngineReceipt{
		Format: 1, Release: pin.Release, Archive: pin.Archive, SHA256: pin.SHA256,
		EngineDir: pin.EngineDir, Engine: filepath.Join(outside, "llama-server.exe"),
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModelHostRecord(modelHostEngineReceiptPath(home), data); err != nil {
		t.Fatal(err)
	}
	commandCalled := false
	p := &servicePanel{modelHostCLI: func(context.Context, string, ...string) ([]byte, error) {
		commandCalled = true
		return nil, nil
	}}
	state := p.modelHostEngineState(context.Background(), home, "modelhostd.exe", pin)
	if state.Staged || !strings.Contains(state.Reason, "outside the pinned cache folder") || commandCalled {
		t.Fatalf("receipt outside pinned engine folder was not refused before command validation: state=%+v commandCalled=%v", state, commandCalled)
	}
}

func TestModelHostRootRegistrationSlotPreservesExistingFolder(t *testing.T) {
	base := t.TempDir()
	configRoot := filepath.Join(base, "profile")
	firstPath := filepath.Join(base, "first models")
	secondPath := filepath.Join(base, "second models")
	for _, dir := range []string{firstPath, secondPath} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := setup.Home{Config: configRoot}
	first, err := setup.NewModelHostChatRoot(firstPath, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	config, err := setup.ModelHostChatInventoryConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	first.ConfigSHA256 = modelHostConfigDigest(config)
	if err := writeModelHostRecord(first.ConfigPath, config); err != nil {
		t.Fatal(err)
	}
	receipt := modelHostInventoryReceipt{Format: 1, Root: first, ConfigSHA256: first.ConfigSHA256,
		Program: "inventoryd.exe", Arguments: modelHostProviderArguments(first, "inventoryd.exe"),
		Contract: modelHostInventoryContract, Permission: "inventory.provide store:" + first.Store}
	receiptData, err := modelHostInventoryReceiptBytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModelHostRecord(first.ReceiptPath, receiptData); err != nil {
		t.Fatal(err)
	}
	if err := modelHostRootRegistrationSlot(home, first); err != nil {
		t.Fatalf("the existing folder could not be reused: %v", err)
	}
	second, err := setup.NewModelHostChatRoot(secondPath, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := modelHostRootRegistrationSlot(home, second); err == nil || !strings.Contains(err.Error(), "already has a saved model folder") {
		t.Fatalf("a second folder was not refused before registration: %v", err)
	}
	currentReceipt, err := os.ReadFile(first.ReceiptPath)
	if err != nil || !bytes.Equal(currentReceipt, receiptData) {
		t.Fatalf("the working folder receipt changed: err=%v", err)
	}
	if _, err := os.Stat(second.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the second folder config was created despite refusal: %v", err)
	}
}

func TestModelHostRootRegistrationSlotRetriesExactUnfinishedConfig(t *testing.T) {
	base := t.TempDir()
	configRoot := filepath.Join(base, "profile")
	firstPath := filepath.Join(base, "first models")
	secondPath := filepath.Join(base, "second models")
	for _, dir := range []string{firstPath, secondPath} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := setup.Home{Config: configRoot}
	first, err := setup.NewModelHostChatRoot(firstPath, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	config, err := setup.ModelHostChatInventoryConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModelHostRecord(first.ConfigPath, config); err != nil {
		t.Fatal(err)
	}
	if err := modelHostRootRegistrationSlot(home, first); err != nil {
		t.Fatalf("an exact unfinished config could not be retried: %v", err)
	}
	second, err := setup.NewModelHostChatRoot(secondPath, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := modelHostRootRegistrationSlot(home, second); err == nil || !strings.Contains(err.Error(), "unfinished model-folder registration") {
		t.Fatalf("a different root bypassed unfinished-registration recovery: %v", err)
	}
	if _, err := os.Stat(second.ConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second config was created during recovery: %v", err)
	}
}

func TestModelHostSavedRootIdentityMatchRetainsConfigDigest(t *testing.T) {
	base := t.TempDir()
	selected := filepath.Join(base, "chosen models")
	if err := os.Mkdir(selected, 0o700); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(base, "profile")
	root, err := setup.NewModelHostChatRoot(selected, configRoot)
	if err != nil {
		t.Fatal(err)
	}
	root.ConfigSHA256 = strings.Repeat("a", 64)
	if !modelHostSavedRootMatches(root, configRoot) {
		t.Fatal("a valid receipt with its config digest was rejected during refresh or host revalidation")
	}
	root.Provider += "-changed"
	if modelHostSavedRootMatches(root, configRoot) {
		t.Fatal("a changed receipt was accepted")
	}
}

func TestParseModelHostPinJSON(t *testing.T) {
	const archive = "llama-b10825-bin-win-vulkan-x64.zip"
	const archiveDigest = "8907e5f3b74cb57d93fd94e187a87d85be24dc64e3b0ff2bc1b287da17b31446"
	const bundle = "llama-b10825-oa-win-vulkan-x64.zip"
	const bundleDigest = "bfed6c062e89fec3ba594da6d826703258079ca57fcea10c834e4014b9bafceb"
	pinJSON := []byte(`{"schema":1,"release":"b10825","program":"llama-server.exe","archive":{"name":"` + archive + `","url":"https://github.com/ggml-org/llama.cpp/releases/download/b10825/` + archive + `","source_commit":"9e0e220594af405a62835dc3a27495729fd8506b","size_bytes":35227898,"sha256":"` + archiveDigest + `"},"bundle":{"name":"` + bundle + `","size_bytes":96902073,"sha256":"` + bundleDigest + `"},"files":[{"path":"llama-server.exe","size_bytes":1,"sha256":"` + strings.Repeat("a", 64) + `"}],"runtime":{"name":"Microsoft Visual C++ 2015-2026 Redistributable (x64)","version":"14.51.36231","folder":"Microsoft.VC145.CRT","files":[]}}`)
	cache := filepath.Join(t.TempDir(), "Local", "OpenAbstractions")
	pin, err := parseModelHostPin(pinJSON, cache)
	if err != nil {
		t.Fatal(err)
	}
	if pin.Release != "b10825" || pin.Program != "llama-server.exe" || pin.Archive != archive || pin.Size != 35227898 || pin.SHA256 != archiveDigest ||
		pin.Bundle != (modelHostBundlePin{Name: bundle, Size: 96902073, SHA256: bundleDigest}) ||
		pin.URL != "https://github.com/ggml-org/llama.cpp/releases/download/b10825/"+archive ||
		pin.EngineDir != filepath.Join(cache, "OpenAbstractions", "engines", "llama.cpp-b10825") {
		t.Fatalf("parsed installed ModelHost pin: %+v", pin)
	}
	if !modelHostSameDigest(strings.ToUpper(archiveDigest), archiveDigest) || modelHostSameDigest("not-a-digest", archiveDigest) {
		t.Fatal("archive digest comparison did not accept the same hexadecimal digest and reject malformed input")
	}
	// CQ-12 reads both sides through digest.Normalize: a labelled or padded
	// spelling of the same sha256 matches, and anything that is not 64 hex
	// digits matches nothing, the empty pair and a 128-digit value included.
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"", "", false},
		{"sha256:" + archiveDigest, archiveDigest, true},
		{"sha256-" + archiveDigest, archiveDigest, true},
		{" " + archiveDigest + "\n", archiveDigest, true},
		{archiveDigest + archiveDigest, archiveDigest + archiveDigest, false},
		{archiveDigest[:62], archiveDigest[:62], false},
		{archiveDigest, bundleDigest, false},
	} {
		if got := modelHostSameDigest(c.a, c.b); got != c.same {
			t.Errorf("modelHostSameDigest(%q, %q) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
	if pin.ArchiveRefused || modelHostRestage(pin) != "choose the pinned archive again" {
		t.Fatal("a pin without runtime files retired the publisher archive")
	}
	// A pin whose folder holds app-local runtime files retires the archive
	// route (MODELHOST-L3): the Panel offers the included engine instead.
	withRuntime := strings.Replace(string(pinJSON), `"files":[]}`, `"files":["vcruntime140.dll"]}`, 1)
	retired, err := parseModelHostPin([]byte(withRuntime), cache)
	if err != nil || !retired.ArchiveRefused || modelHostRestage(retired) != "stage the included engine again" {
		t.Fatalf("a pin with runtime files: %+v %v", retired, err)
	}
	if _, err := parseModelHostPin([]byte(strings.ReplaceAll(string(pinJSON), archive, `../outside.zip`)), cache); err == nil {
		t.Fatal("a path-bearing archive name was accepted from ModelHost JSON")
	}
	if _, err := parseModelHostPin(append(append([]byte(nil), pinJSON...), []byte(" {}")...), cache); err == nil {
		t.Fatal("trailing JSON was accepted from ModelHost")
	}
	called := false
	panel := &servicePanel{modelHostCLI: func(_ context.Context, program string, args ...string) ([]byte, error) {
		called = program == "modelhostd.exe" && slices.Equal(args, []string{"engine", "pin", "--json"})
		return pinJSON, nil
	}}
	if _, err := panel.modelHostPin(context.Background(), modelHostPrograms{ModelHost: "modelhostd.exe"}, setup.Home{Data: cache}); err != nil || !called {
		t.Fatalf("Setup did not use the provider's JSON pin command: called=%v err=%v", called, err)
	}
}

func TestModelHostBundledEngineStagesWithBundlePinAndState(t *testing.T) {
	base := t.TempDir()
	engineDir := filepath.Join(base, "cache", "OpenAbstractions", "engines", "llama.cpp-b10825")
	if err := os.MkdirAll(engineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	enginePath := filepath.Join(engineDir, "llama-server.exe")
	home := setup.Home{Config: filepath.Join(base, "config"), Data: filepath.Join(base, "cache")}
	pin := modelHostPin{Release: "b10825", Archive: "llama-b10825-bin-win-vulkan-x64.zip", SHA256: strings.Repeat("a", 64),
		Bundle: modelHostBundlePin{Name: "llama-b10825-oa-win-vulkan-x64.zip", Size: 123, SHA256: strings.Repeat("b", 64)}, EngineDir: engineDir}
	var commands [][]string
	p := &servicePanel{modelHostCLI: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		commands = append(commands, append([]string(nil), args...))
		if len(commands) == 1 {
			return []byte("installed llama.cpp b10825; pass --engine " + enginePath + "\n"), nil
		}
		return []byte("engine is pinned\n"), nil
	}}
	receipt, err := p.stageModelHostBundledEngine(context.Background(), home,
		modelHostPrograms{ModelHost: filepath.Join(base, "tools", "modelhostd.exe")}, pin,
		filepath.Join(base, "tools", "engines", pin.Bundle.Name))
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(home.Data, "OpenAbstractions", "providers", "modelhost")
	wantInstall := []string{"engine", "install", "--bundle", filepath.Join(base, "tools", "engines", pin.Bundle.Name), "--dir", engineDir, "--state", statePath}
	wantCheck := []string{"engine", "check", "--engine", enginePath, "--state", statePath}
	if len(commands) != 2 || !slices.Equal(commands[0], wantInstall) || !slices.Equal(commands[1], wantCheck) {
		t.Fatalf("bundled engine commands = %#v, want install/check %#v / %#v", commands, wantInstall, wantCheck)
	}
	if receipt.Source != "bundle" || receipt.Archive != pin.Bundle.Name || receipt.SHA256 != pin.Bundle.SHA256 || receipt.Engine != enginePath {
		t.Fatalf("saved receipt does not describe the reduced bundle: %+v", receipt)
	}
	var saved modelHostEngineReceipt
	if err := readModelHostRecord(modelHostEngineReceiptPath(home), &saved); err != nil || saved != receipt {
		t.Fatalf("bundle receipt was not written exactly: saved=%+v err=%v", saved, err)
	}
	state := p.modelHostEngineState(context.Background(), home, filepath.Join(base, "tools", "modelhostd.exe"), pin)
	if !state.Staged || state.Path != enginePath {
		t.Fatalf("a valid bundled engine receipt was not recognized: %+v", state)
	}
}

func TestModelHostBundledArchiveUsesReducedBundleBytes(t *testing.T) {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		t.Skip("the pinned reduced engine bundle is qualified for Windows x64")
	}
	base := t.TempDir()
	tools := filepath.Join(base, "tools")
	engines := filepath.Join(tools, "engines")
	if err := os.MkdirAll(engines, 0o700); err != nil {
		t.Fatal(err)
	}
	programs := modelHostPrograms{ModelHost: filepath.Join(tools, "modelhostd.exe")}
	payload := []byte("reduced-bundle-fixture")
	path := filepath.Join(engines, "llama-b10825-oa-win-vulkan-x64.zip")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	pin := modelHostPin{Release: "b10825", Archive: "upstream.zip", Size: 99, SHA256: strings.Repeat("a", 64),
		Bundle: modelHostBundlePin{Name: filepath.Base(path), Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}}
	if err := verifyModelHostPinnedArchive(context.Background(), path, pin.Bundle); err != nil {
		t.Fatalf("the exact reduced bundle was rejected: %v", err)
	}
	state := (&servicePanel{}).modelHostBundledArchive(context.Background(), programs, pin)
	if !state.Available || state.Name != pin.Bundle.Name || state.Size != pin.Bundle.Size || state.SHA256 != pin.Bundle.SHA256 ||
		!strings.Contains(state.Approval, pin.Bundle.Name) || !strings.Contains(state.Approval, pin.Bundle.SHA256) {
		t.Fatalf("Setup did not present the reduced bundle pin for approval: %+v", state)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyModelHostPinnedArchive(context.Background(), path, pin.Bundle); err == nil {
		t.Fatal("modified bundled bytes were accepted")
	}
}

func TestModelHostProviderDeclarationUsesOnlyTheSetupConfigRoot(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "chosen local models")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := setup.NewModelHostChatRoot(rootPath, filepath.Join(base, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{"serve", "--endpoint", "{endpoint}", "--config", root.ConfigPath, "--config-roots-only"}
	if got := modelHostProviderArguments(root, program); !slices.Equal(got, wantArgs) {
		t.Fatalf("provider args %q, want %q", got, wantArgs)
	}
	list := fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{{
		Declaration: fwire.Declaration{
			Name: root.Provider, Program: program, Arguments: wantArgs, Endpoint: root.Endpoint,
			Transport: fwire.DeclarationTransportNative, Contracts: []string{modelHostInventoryContract},
			Resources: []string{"store:" + root.Store}, Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider,
		},
		DeclaredBy: program, Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleProvider,
		Described: []fwire.ServiceState{},
	}}}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if exact, err := modelHostProviderDeclaration(raw, root, program); err != nil || !exact {
		t.Fatalf("exact declaration: %v %v", exact, err)
	}
	list.Declarations[0].Declaration.Arguments = []string{"serve", "--endpoint", "{endpoint}", "--config", root.ConfigPath, "--ignore-environment-roots"}
	raw, err = json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if exact, err := modelHostProviderDeclaration(raw, root, program); err != nil || exact {
		t.Fatalf("provider with other inventory adapters enabled was accepted: %v %v", exact, err)
	}
}

func TestModelHostEngineConsentBindsArchiveAndDisplayedOperation(t *testing.T) {
	pin := modelHostPin{
		Release: "b10825", Archive: "llama-b10825-bin-win-vulkan-x64.zip", Size: 35227898,
		SHA256:    "8907e5f3b74cb57d93fd94e187a87d85be24dc64e3b0ff2bc1b287da17b31446",
		URL:       "https://github.com/ggml-org/llama.cpp/releases/download/b10825/llama-b10825-bin-win-vulkan-x64.zip",
		EngineDir: filepath.Join(t.TempDir(), "engines", "llama.cpp-b10825"),
	}
	approval := modelHostEngineApproval(pin)
	digest := modelHostEngineApprovalDigest(pin)
	if !strings.Contains(approval, pin.Archive) || !strings.Contains(approval, pin.URL) || !strings.Contains(approval, pin.SHA256) || !strings.Contains(approval, pin.EngineDir) ||
		!modelHostSameDigest(digest, setup.ModelHostChatApprovalDigest(approval)) {
		t.Fatalf("engine consent does not bind the pinned operation: %q (%s)", approval, digest)
	}
}

func TestModelHostProviderListUsesPublicCommandSeam(t *testing.T) {
	root := setup.ModelHostChatRoot{ID: "0123456789abcdef01234567", Store: "oa-chat-0123456789abcdef01234567",
		Provider: "oa-setup-chat-0123456789abcdef01234567", Endpoint: "oa_setup_chat_0123456789abcdef01234567",
		ConfigPath: filepath.Join(t.TempDir(), "inventoryd.json")}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	list := fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{{
		Declaration: fwire.Declaration{Name: root.Provider, Program: program, Arguments: modelHostProviderArguments(root, program),
			Endpoint: root.Endpoint, Transport: fwire.DeclarationTransportNative, Contracts: []string{modelHostInventoryContract},
			Resources: []string{"store:" + root.Store}, Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider},
		DeclaredBy: program, Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleProvider,
		Described: []fwire.ServiceState{},
	}}}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	p := &servicePanel{modelHostOA: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		return raw, nil
	}}
	registered, err := p.listModelHostProvider(context.Background(), root, program)
	if err != nil || !registered || len(calls) != 1 || !slices.Equal(calls[0], []string{"provider", "list", "--json", "--timeout", "30s"}) {
		t.Fatalf("public provider verification: registered=%v calls=%q err=%v", registered, calls, err)
	}
}

func TestModelHostProviderAddAndListBindTheConfigRootsOnlyMode(t *testing.T) {
	base := t.TempDir()
	rootPath := filepath.Join(base, "chosen local models")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := setup.NewModelHostChatRoot(rootPath, filepath.Join(base, "profile"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	declaration := fwire.Declaration{Name: root.Provider, Program: program, Arguments: modelHostProviderArguments(root, program),
		Endpoint: root.Endpoint, Transport: fwire.DeclarationTransportNative, Contracts: []string{modelHostInventoryContract},
		Resources: []string{"store:" + root.Store}, Activation: fwire.ActivationOnDemand, Role: fwire.DeclarationRoleProvider}
	list := fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{{
		Declaration: declaration, DeclaredBy: program, Readiness: fwire.DeclarationReadinessReady, Role: fwire.DeclarationRoleProvider,
		Described: []fwire.ServiceState{},
	}}}
	raw, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	p := &servicePanel{modelHostOA: func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, slices.Clone(args))
		if slices.Equal(args[:2], []string{"provider", "list"}) {
			return raw, nil
		}
		return []byte(`{"outcome":"applied"}`), nil
	}}
	if err := p.addModelHostInventoryProvider(context.Background(), root, program); err != nil {
		t.Fatal(err)
	}
	wantAdd := []string{"provider", "add", root.Provider, "--program", program, "--provider-endpoint", root.Endpoint,
		"--contract", modelHostInventoryContract, "--arg=serve", "--arg=--endpoint", "--arg={endpoint}",
		"--arg=--config", "--arg=" + root.ConfigPath, "--arg=--config-roots-only", "--store", root.Store,
		"--json", "--timeout", "30s"}
	wantList := []string{"provider", "list", "--json", "--timeout", "30s"}
	if len(calls) != 2 || !slices.Equal(calls[0], wantAdd) || !slices.Equal(calls[1], wantList) {
		t.Fatalf("provider public verbs: %q, want [%q %q]", calls, wantAdd, wantList)
	}
}

func TestModelHostModelApprovalNamesTheExactInventoryObject(t *testing.T) {
	model := modelHostChatModel{ID: "inventory-object-id", Store: "oa-chat-0123456789abcdef01234567", Name: "Qwen GGUF", Size: 1024}
	approval := modelHostModelApproval(setup.ModelHostChatRoot{Path: `C:\chosen`}, model)
	for _, want := range []string{model.Name, model.ID, model.Store, "1024 bytes", "pinned llama.cpp engine"} {
		if !strings.Contains(approval, want) {
			t.Fatalf("model approval %q does not identify %q", approval, want)
		}
	}
}

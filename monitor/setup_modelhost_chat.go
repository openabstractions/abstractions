package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	contentdigest "github.com/openabstractions/abstraction-storage/go/digest"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

const modelHostInventoryContract = "abstraction.storage/inventory-source@1"

// codingModelController reuses the public download journal. Its result folder
// is also the model folder: publishing a local job result adds a hard link.
func (p *servicePanel) codingModelController() (setup.ModelController, error) {
	if p.codingController != nil {
		return *p.codingController, nil
	}
	home, err := setup.CurrentHome()
	if err != nil {
		return setup.ModelController{}, err
	}
	if home.Isolated {
		return setup.ModelController{}, errors.New("coding-model Setup requires the installed runtime's user state")
	}
	plan := setup.CodingModelPlan()
	root := filepath.Join(home.Data, "models", "coding")
	return setup.ModelController{Plan: &plan, JournalPath: filepath.Join(home.Config, "coding-model.json"),
		StageDir: root, ProviderRoot: root, LocalResults: true, Transfer: setup.PublicTransfer{LocalOnly: true}}, nil
}

func (p *servicePanel) setupCodingModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost {
		if !p.setupTryStart(w) {
			return
		}
		defer p.setupActionMu.Unlock()
	}
	c, err := p.codingModelController()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	status := setup.ModelStatus{}
	accepted := false
	if r.Method == http.MethodPost {
		var request struct {
			Action      string              `json:"action"`
			SetupPlanID string              `json:"setup_plan_id,omitempty"`
			Confirm     bool                `json:"confirm"`
			Approval    setup.ModelApproval `json:"approval"`
		}
		if !decodeModelHostJSON(w, r, &request, 4096) {
			return
		}
		// Every mutation binds the displayed source, bytes and license identity.
		plan := setup.CodingModelPlan()
		if !request.Confirm || request.Approval.PlanID != plan.ID || request.Approval.DownloadBytes != plan.DownloadBytes || request.Approval.PeakBytes != plan.PeakBytes || !slices.Equal(request.Approval.Licenses, plan.Licenses) {
			http.Error(w, "approve the exact coding-model download first", http.StatusBadRequest)
			return
		}
		if request.SetupPlanID != "" {
			_, record, admissionErr := p.approvedInstall(ctx, request.SetupPlanID)
			if admissionErr != nil || record.CodingModelPlanID != plan.ID {
				http.Error(w, "the installation has no matching coding-model approval", http.StatusConflict)
				return
			}
		}
		switch request.Action {
		case "start":
			status, err = c.StartAccepted(ctx, request.Approval)
			accepted = err == nil
		case "check":
			status, err = c.Advance(ctx, "coding")
		case "retry":
			status, err = c.Retry(ctx, "coding")
		default:
			http.Error(w, "unknown coding-model action", http.StatusBadRequest)
			return
		}
	} else {
		status, err = c.Status(ctx)
	}
	if err != nil {
		http.Error(w, "Download not confirmed: "+err.Error()+". Reopen or check again to reconcile the same saved work.", http.StatusConflict)
		return
	}
	panelJSON(w, struct {
		Status   setup.ModelStatus `json:"status"`
		Accepted bool              `json:"accepted"`
		Folder   string            `json:"folder"`
	}{status, accepted, c.StageDir})
}

type modelHostPrograms struct {
	ModelHost string
	Inventory string
}

type modelHostInventoryReader interface {
	ListStore(context.Context, string, string, int64) (content.InventoryPage, error)
}

type modelHostPin struct {
	Release   string `json:"release"`
	Archive   string `json:"archive"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	URL       string `json:"url"`
	EngineDir string `json:"engine_dir"`
	// ArchiveRefused is a pin whose engine folder holds app-local Visual
	// C++ runtime files: only the reduced bundle carries them, so the
	// publisher archive cannot stage this engine (MODELHOST-L3) and the
	// Panel offers the included engine or a Repair instead.
	ArchiveRefused bool               `json:"archive_refused"`
	Bundle         modelHostBundlePin `json:"-"`
	Program        string             `json:"-"`
}

type modelHostBundlePin struct {
	Name   string
	Size   int64
	SHA256 string
}

type modelHostEnginePinDocument struct {
	Schema  int    `json:"schema"`
	Release string `json:"release"`
	Program string `json:"program"`
	Archive struct {
		Name         string `json:"name"`
		URL          string `json:"url"`
		SourceCommit string `json:"source_commit"`
		SizeBytes    int64  `json:"size_bytes"`
		SHA256       string `json:"sha256"`
	} `json:"archive"`
	Bundle struct {
		Name      string `json:"name"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	} `json:"bundle"`
	Files []struct {
		Path      string `json:"path"`
		SizeBytes int64  `json:"size_bytes"`
		SHA256    string `json:"sha256"`
	} `json:"files"`
	// Runtime names the app-local Visual C++ runtime files among Files that
	// the reduced bundle adds; the engine folder holds them like the rest.
	Runtime struct {
		Name    string   `json:"name"`
		Version string   `json:"version"`
		Folder  string   `json:"folder"`
		Files   []string `json:"files"`
	} `json:"runtime"`
}

type modelHostEngineState struct {
	Staged         bool                    `json:"staged"`
	Path           string                  `json:"path,omitempty"`
	Reason         string                  `json:"reason,omitempty"`
	Approval       string                  `json:"approval,omitempty"`
	ApprovalDigest string                  `json:"approval_digest,omitempty"`
	Bundled        modelHostBundledArchive `json:"bundled"`
}

type modelHostBundledArchive struct {
	Available      bool   `json:"available"`
	Name           string `json:"name,omitempty"`
	Size           int64  `json:"size,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	Approval       string `json:"approval,omitempty"`
	ApprovalDigest string `json:"approval_digest,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

type modelHostChatModel struct {
	ID     string `json:"id"`
	Store  string `json:"store"`
	Name   string `json:"name"`
	Family string `json:"family,omitempty"`
	Quant  string `json:"quant,omitempty"`
	Size   int64  `json:"size"`
	// Digest is the weights entry digest when the inventory names one.
	Digest string `json:"digest,omitempty"`
	Hosted bool   `json:"hosted"`
}

type modelHostRootState struct {
	setup.ModelHostChatRoot
	Registered     bool                 `json:"registered"`
	Reason         string               `json:"reason,omitempty"`
	Models         []modelHostChatModel `json:"models,omitempty"`
	Approval       string               `json:"approval,omitempty"`
	ApprovalDigest string               `json:"approval_digest,omitempty"`
}

type modelHostChatView struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	// Unavailable names why local models are off: "platform" for a computer
	// that has no local model host, "isolated" for an isolated home. Empty
	// with Available false is a read failure.
	Unavailable string               `json:"unavailable,omitempty"`
	PlanID      string               `json:"plan_id,omitempty"`
	Feature     setup.FeatureStatus  `json:"feature"`
	Pin         *modelHostPin        `json:"pin,omitempty"`
	Engine      modelHostEngineState `json:"engine"`
	Root        *modelHostRootState  `json:"root,omitempty"`
}

type modelHostPlanRequest struct {
	PlanID  string `json:"plan_id"`
	Confirm bool   `json:"confirm"`
}

type modelHostBundledEngineRequest struct {
	PlanID         string `json:"plan_id"`
	ApprovalDigest string `json:"approval_digest"`
	Confirm        bool   `json:"confirm"`
}

type modelHostRootRequest struct {
	PlanID         string `json:"plan_id"`
	Path           string `json:"path"`
	ApprovalDigest string `json:"approval_digest"`
	Confirm        bool   `json:"confirm"`
}

type modelHostEngineReceipt struct {
	Format    int    `json:"format"`
	Release   string `json:"release"`
	Archive   string `json:"archive"`
	SHA256    string `json:"sha256"`
	EngineDir string `json:"engine_dir"`
	Engine    string `json:"engine"`
	Source    string `json:"source,omitempty"`
}

type modelHostInventoryReceipt struct {
	Format       int                     `json:"format"`
	Root         setup.ModelHostChatRoot `json:"root"`
	ConfigSHA256 string                  `json:"config_sha256"`
	Program      string                  `json:"program"`
	Arguments    []string                `json:"arguments"`
	Contract     string                  `json:"contract"`
	Permission   string                  `json:"permission"`
}

func (p *servicePanel) modelHostPrograms() (modelHostPrograms, error) {
	if p.modelHostProgramPaths != nil {
		return p.modelHostProgramPaths()
	}
	check := p.modelHostFeatureCheck
	if check == nil {
		check = setup.ModelHostFeatureStatus
	}
	status, err := check()
	if err != nil {
		return modelHostPrograms{}, err
	}
	if !status.Installed {
		if status.Reason != "" {
			return modelHostPrograms{}, errors.New(status.Reason)
		}
		return modelHostPrograms{}, errors.New("add the OpenAbstractions ModelHost feature first")
	}
	executable, err := os.Executable()
	if err != nil {
		return modelHostPrograms{}, err
	}
	tools := filepath.Dir(executable)
	if !identity.SamePathName(filepath.Base(tools), "tools") || !identity.SamePathName(filepath.Base(executable), "Abstraction Panel.exe") {
		return modelHostPrograms{}, errors.New("open the installed OpenAbstractions Panel to use ModelHost Setup")
	}
	programs := modelHostPrograms{ModelHost: filepath.Join(tools, "modelhostd.exe"), Inventory: filepath.Join(tools, "inventoryd.exe")}
	for _, path := range []string{programs.ModelHost, programs.Inventory} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return modelHostPrograms{}, fmt.Errorf("the installed ModelHost support file is missing: %s", filepath.Base(path))
		}
		if err := identity.LocalPath(path); err != nil {
			return modelHostPrograms{}, err
		}
	}
	return programs, nil
}

func (p *servicePanel) modelHostCommand(ctx context.Context, program string, args ...string) ([]byte, error) {
	if p.modelHostCLI != nil {
		return p.modelHostCLI(ctx, program, args...)
	}
	command := childproc.Hidden(exec.CommandContext(ctx, program, args...))
	var output, failure bytes.Buffer
	command.Stdout, command.Stderr = &output, &failure
	if err := command.Run(); err != nil {
		reason := strings.TrimSpace(failure.String())
		if reason == "" {
			reason = strings.TrimSpace(output.String())
		}
		if reason == "" {
			reason = err.Error()
		}
		return nil, errors.New(reason)
	}
	return output.Bytes(), nil
}

// modelHostOpenAbstractions runs one openabstractions command against the
// runtime this Panel serves. Every ModelHost and provider command the Panel
// runs goes through here.
func (p *servicePanel) modelHostOpenAbstractions(ctx context.Context, args ...string) ([]byte, error) {
	args = servedRuntimeArgs(args)
	if p.modelHostOA != nil {
		return p.modelHostOA(ctx, args...)
	}
	return openAbstractionsCommand(ctx, args...)
}

// servedRuntimeArgs names the Panel's explicit runtime on a command line:
// the command otherwise dials ABSTRACTION_RUNTIME_ENDPOINT or the installed
// runtime. --runtime-program verifies that runtime's program and account as
// the Panel's own binding does. The installed runtime needs nothing added.
func servedRuntimeArgs(args []string) []string {
	if panelExplicit == nil {
		return args
	}
	return append(slices.Clone(args), "--endpoint", panelExplicit.Endpoint, "--runtime-program", panelExplicit.Program)
}

// openAbstractionsCommand runs the openabstractions command installed beside
// this Panel.
func openAbstractionsCommand(ctx context.Context, args ...string) ([]byte, error) {
	panel, err := os.Executable()
	if err != nil {
		return nil, err
	}
	name := "openabstractions"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := childproc.Hidden(exec.CommandContext(ctx, filepath.Join(filepath.Dir(panel), name), args...))
	var output, failure bytes.Buffer
	command.Stdout, command.Stderr = &output, &failure
	if err := command.Run(); err != nil {
		reason := strings.TrimSpace(failure.String())
		if reason == "" {
			reason = strings.TrimSpace(output.String())
		}
		if reason == "" {
			reason = err.Error()
		}
		return nil, errors.New(reason)
	}
	return output.Bytes(), nil
}

func parseModelHostPin(raw []byte, cache string) (modelHostPin, error) {
	if !filepath.IsAbs(cache) || filepath.Clean(cache) != cache || identity.LocalPath(cache) != nil {
		return modelHostPin{}, errors.New("Setup has no absolute local engine cache folder")
	}
	var document modelHostEnginePinDocument
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return modelHostPin{}, fmt.Errorf("the installed ModelHost engine pin is not valid JSON: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return modelHostPin{}, errors.New("the installed ModelHost engine pin has trailing data")
	}
	if document.Schema != 1 || document.Release == "" || strings.ContainsAny(document.Release, `/\\:`) ||
		document.Program == "" || filepath.Base(document.Program) != document.Program || strings.ContainsAny(document.Program, `/\\:`) ||
		document.Archive.SizeBytes <= 0 || document.Archive.SizeBytes > 1<<30 || document.Bundle.SizeBytes <= 0 || document.Bundle.SizeBytes > 1<<30 ||
		!modelHostArchiveBasename(document.Archive.Name) || !modelHostArchiveBasename(document.Bundle.Name) ||
		!modelHostLowerSHA256(document.Archive.SHA256) || !modelHostLowerSHA256(document.Bundle.SHA256) ||
		!modelHostLowerHex(document.Archive.SourceCommit, 40) ||
		document.Archive.URL != "https://github.com/ggml-org/llama.cpp/releases/download/"+document.Release+"/"+document.Archive.Name ||
		len(document.Files) == 0 || len(document.Files) > 128 {
		return modelHostPin{}, errors.New("the installed ModelHost does not publish a usable Windows x64 engine pin and reduced bundle")
	}
	seen := map[string]bool{}
	programListed := false
	for _, file := range document.Files {
		nameKey := identity.PathNameKey(file.Path)
		if file.Path == "" || filepath.Base(file.Path) != file.Path || strings.ContainsAny(file.Path, `/\\:`) ||
			file.SizeBytes <= 0 || !modelHostLowerSHA256(file.SHA256) || seen[nameKey] {
			return modelHostPin{}, errors.New("the installed ModelHost engine pin has an invalid file list")
		}
		seen[nameKey] = true
		programListed = programListed || identity.SamePathName(file.Path, document.Program)
	}
	if !programListed {
		return modelHostPin{}, errors.New("the installed ModelHost engine pin does not list its engine program")
	}
	return modelHostPin{
		Release: document.Release, Program: document.Program, Archive: document.Archive.Name,
		Size: document.Archive.SizeBytes, SHA256: document.Archive.SHA256, URL: document.Archive.URL,
		Bundle:         modelHostBundlePin{Name: document.Bundle.Name, Size: document.Bundle.SizeBytes, SHA256: document.Bundle.SHA256},
		EngineDir:      layout.EngineCacheDir(cache, "llama.cpp", document.Release),
		ArchiveRefused: len(document.Runtime.Files) > 0,
	}, nil
}

// modelHostArchiveRefused is the refusal of the publisher-archive upload for a pin
// that carries the app-local runtime, in one sentence that names the route
// that works.
const modelHostArchiveRefused = "The publisher's engine ZIP lacks the Visual C++ runtime this engine needs; stage the engine included with OpenAbstractions, or repair OpenAbstractions in Windows Settings if it is missing."

// modelHostRestage names how to stage the engine again under this pin.
func modelHostRestage(pin modelHostPin) string {
	if pin.ArchiveRefused {
		return "stage the included engine again"
	}
	return "choose the pinned archive again"
}

func modelHostArchiveBasename(name string) bool {
	return name != "" && filepath.Base(name) == name && !strings.ContainsAny(name, `/\\:`) && strings.HasSuffix(name, ".zip")
}

func modelHostLowerSHA256(value string) bool { return modelHostLowerHex(value, 64) }

func modelHostLowerHex(value string, length int) bool {
	if len(value) != length || strings.Trim(value, "0123456789abcdef") != "" {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func modelHostEngineApproval(pin modelHostPin) string {
	return fmt.Sprintf("Stage the pinned llama.cpp %s engine from %s (%s; %d bytes, SHA-256 %s) in %s. Setup verifies the archive and writes only the pinned engine files. It will not start the engine or load a model.",
		pin.Release, pin.Archive, pin.URL, pin.Size, pin.SHA256, pin.EngineDir)
}

func modelHostEngineApprovalDigest(pin modelHostPin) string {
	return setup.ModelHostChatApprovalDigest(modelHostEngineApproval(pin))
}

func modelHostBundledArchivePath(programs modelHostPrograms, pin modelHostPin) string {
	return filepath.Join(filepath.Dir(programs.ModelHost), "engines", pin.Bundle.Name)
}

func modelHostBundledEngineApproval(pin modelHostPin, archivePath string) string {
	return fmt.Sprintf("Stage the pinned llama.cpp %s engine from the bundled local archive at %s (%s; %d bytes, SHA-256 %s) in %s. Setup verifies the archive and writes only the pinned engine files. It will not start the engine or load a model.",
		pin.Release, archivePath, pin.Bundle.Name, pin.Bundle.Size, pin.Bundle.SHA256, pin.EngineDir)
}

func modelHostBundledEngineApprovalDigest(pin modelHostPin, archivePath string) string {
	return setup.ModelHostChatApprovalDigest(modelHostBundledEngineApproval(pin, archivePath))
}

func verifyModelHostPinnedArchive(ctx context.Context, path string, pin modelHostBundlePin) error {
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		return errors.New("the bundled llama.cpp archive is currently qualified only for Windows x64")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || identity.LocalPath(path) != nil {
		return errors.New("the bundled engine archive must be at its local installed-package path")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() != pin.Size {
		return errors.New("the installed bundled archive size differs from the pinned engine")
	}
	archive, err := os.Open(path)
	if err != nil {
		return err
	}
	defer archive.Close()
	opened, err := archive.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || opened.Size() != pin.Size {
		return errors.New("the bundled engine archive changed while Setup opened it")
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := archive.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > pin.Size {
				return errors.New("the bundled engine archive exceeds its pinned size")
			}
			_, _ = h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	final, err := archive.Stat()
	if err != nil || !os.SameFile(opened, final) || final.Size() != pin.Size || total != pin.Size ||
		!modelHostSameDigest(hex.EncodeToString(h.Sum(nil)), pin.SHA256) {
		return errors.New("the installed bundled archive does not match the pinned engine bytes")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(final, current) || !current.Mode().IsRegular() {
		return errors.New("the bundled engine archive changed during verification")
	}
	return nil
}

func (p *servicePanel) modelHostBundledArchive(ctx context.Context, programs modelHostPrograms, pin modelHostPin) modelHostBundledArchive {
	state := modelHostBundledArchive{Name: pin.Bundle.Name, Size: pin.Bundle.Size, SHA256: pin.Bundle.SHA256}
	path := modelHostBundledArchivePath(programs, pin)
	if err := verifyModelHostPinnedArchive(ctx, path, pin.Bundle); err != nil {
		state.Reason = "The bundled archive is unavailable or does not match the installed pin: " + err.Error()
		return state
	}
	state.Approval = modelHostBundledEngineApproval(pin, path)
	state.ApprovalDigest = modelHostBundledEngineApprovalDigest(pin, path)
	state.Available = true
	return state
}

func modelHostSameDigest(a, b string) bool {
	left := contentdigest.Normalize(a)
	return left != "" && subtle.ConstantTimeCompare([]byte(left), []byte(contentdigest.Normalize(b))) == 1
}

func readModelHostRecord(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("Setup record has trailing data")
	}
	return nil
}

func writeModelHostRecord(path string, data []byte) error {
	if err := identity.LocalPath(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func modelHostBase(home setup.Home) string { return filepath.Join(home.Config, "modelhost-chat") }

func modelHostEngineReceiptPath(home setup.Home) string {
	return filepath.Join(modelHostBase(home), "engine.json")
}

func modelHostProviderState(home setup.Home) string {
	return filepath.Join(home.Data, "OpenAbstractions", "providers", "modelhost")
}

func (p *servicePanel) modelHostPlan(ctx context.Context, planID string) (setup.Journal, setup.State, error) {
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		return setup.Journal{}, setup.State{}, err
	}
	status, err := setupStatus(ctx, runner, journal)
	if err != nil {
		return setup.Journal{}, setup.State{}, err
	}
	if !status.Approved || !status.CurrentCatalog || (planID != "" && status.Plan.ID != planID) {
		return setup.Journal{}, setup.State{}, errors.New("review and approve the current setup plan first")
	}
	state, err := journal.Load(ctx)
	if err != nil {
		return setup.Journal{}, setup.State{}, err
	}
	return journal, state, nil
}

func (p *servicePanel) modelHostMutationPlan(ctx context.Context, planID string) (setup.Journal, setup.State, error) {
	if planID == "" {
		return setup.Journal{}, setup.State{}, errors.New("refresh Setup and include its current plan ID before continuing")
	}
	return p.modelHostPlan(ctx, planID)
}

func (p *servicePanel) modelHostFeatureStatus() (setup.FeatureStatus, error) {
	if p.modelHostFeatureCheck != nil {
		return p.modelHostFeatureCheck()
	}
	return setup.ModelHostFeatureStatus()
}

func (p *servicePanel) modelHostPin(ctx context.Context, programs modelHostPrograms, home setup.Home) (modelHostPin, error) {
	raw, err := p.modelHostCommand(ctx, programs.ModelHost, "engine", "pin", "--json")
	if err != nil {
		return modelHostPin{}, fmt.Errorf("ModelHost engine pin: %w", err)
	}
	return parseModelHostPin(raw, home.Data)
}

func (p *servicePanel) modelHostEngineState(ctx context.Context, home setup.Home, program string, pin modelHostPin) modelHostEngineState {
	state := modelHostEngineState{Approval: modelHostEngineApproval(pin), ApprovalDigest: modelHostEngineApprovalDigest(pin)}
	var receipt modelHostEngineReceipt
	if err := readModelHostRecord(modelHostEngineReceiptPath(home), &receipt); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			state.Reason = "The saved engine receipt cannot be read; " + modelHostRestage(pin) + " to reconcile it."
		}
		return state
	}
	if receipt.Format != 1 || receipt.Release != pin.Release || receipt.EngineDir != pin.EngineDir || !filepath.IsAbs(receipt.Engine) {
		state.Reason = "The saved engine receipt does not match the installed ModelHost pin."
		return state
	}
	switch receipt.Source {
	case "", "archive":
		if receipt.Archive != pin.Archive || !modelHostSameDigest(receipt.SHA256, pin.SHA256) {
			state.Reason = "The saved publisher archive receipt does not match the installed ModelHost pin."
			return state
		}
	case "bundle":
		if receipt.Archive != pin.Bundle.Name || !modelHostSameDigest(receipt.SHA256, pin.Bundle.SHA256) {
			state.Reason = "The saved bundled archive receipt does not match the installed ModelHost pin."
			return state
		}
	default:
		state.Reason = "The saved engine receipt names an unknown archive source."
		return state
	}
	if err := identity.LocalPath(receipt.Engine); err != nil {
		state.Reason = "The saved engine path is not on a local drive."
		return state
	}
	same, err := identity.SameFile(filepath.Dir(receipt.Engine), pin.EngineDir)
	if err != nil || !same {
		state.Reason = "The saved engine path is outside the pinned cache folder."
		return state
	}
	if _, err := p.modelHostCommand(ctx, program, "engine", "check", "--engine", receipt.Engine, "--state", modelHostProviderState(home)); err != nil {
		state.Reason = "The staged engine no longer matches the installed pin: " + err.Error()
		return state
	}
	state.Staged, state.Path = true, receipt.Engine
	return state
}

func (p *servicePanel) setupModelHostChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	reply := modelHostChatView{}
	// The facts are read without a plan: an earlier release's plan or none
	// still shows what is installed. Changes need the approved current plan
	// (modelHostMutationPlan), and the reply names it only when there is one.
	if _, state, planErr := p.modelHostPlan(ctx, ""); planErr == nil {
		reply.PlanID = state.Plan.ID
	}
	var err error
	reply.Feature, err = p.modelHostFeatureStatus()
	if err != nil {
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	if !reply.Feature.Available {
		reply.Unavailable = "platform"
		reply.Reason = reply.Feature.Reason
		panelJSON(w, reply)
		return
	}
	if !reply.Feature.Installed {
		reply.Available = true
		reply.Reason = reply.Feature.Reason
		panelJSON(w, reply)
		return
	}
	programs, err := p.modelHostPrograms()
	if err != nil {
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		if err == nil {
			reply.Unavailable = "isolated"
			err = errors.New("ModelHost Setup requires the installed runtime's user state")
		}
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	pin, err := p.modelHostPin(ctx, programs, home)
	if err != nil {
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	if runtime.GOOS != "windows" || runtime.GOARCH != "amd64" {
		reply.Available = true
		reply.Reason = "The reviewed bundled llama.cpp engine is currently available only for Windows x64; this installation has no qualified local engine archive."
		reply.Engine.Reason = reply.Reason
		reply.Engine.Bundled.Reason = reply.Reason
		panelJSON(w, reply)
		return
	}
	reply.Pin = &pin
	reply.Engine = p.modelHostEngineState(ctx, home, programs.ModelHost, pin)
	if !reply.Engine.Staged {
		reply.Engine.Bundled = p.modelHostBundledArchive(ctx, programs, pin)
	}
	reply.Available = true
	if reply.Engine.Staged {
		root, rootErr := p.modelHostSavedRoot(ctx, home, programs, pin, reply.Engine.Path)
		if rootErr != nil {
			reply.Reason = rootErr.Error()
		} else if root != nil {
			reply.Root = root
		}
	}
	panelJSON(w, reply)
}

func decodeModelHostJSON(w http.ResponseWriter, r *http.Request, target any, max int64) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, max))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid ModelHost Setup request", http.StatusBadRequest)
		return false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one ModelHost Setup request required", http.StatusBadRequest)
		return false
	}
	return true
}

func (p *servicePanel) setupModelHostFeatureAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request modelHostPlanRequest
	if !decodeModelHostJSON(w, r, &request, 2048) {
		return
	}
	if !request.Confirm {
		http.Error(w, "explicit permission to add ModelHost is required", http.StatusBadRequest)
		return
	}
	if !p.setupTryStart(w) {
		return
	}
	defer p.setupActionMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if _, _, err := p.modelHostMutationPlan(ctx, request.PlanID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	runner, _, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	feature, err := runner.AddModelHostFeature(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if !feature.Installed {
		http.Error(w, feature.Reason, http.StatusConflict)
		return
	}
	panelJSON(w, struct {
		Feature setup.FeatureStatus `json:"feature"`
	}{Feature: feature})
}

var createModelHostEngineTemp = os.CreateTemp
var modelHostSystemTempDir = os.TempDir

func modelHostEngineTempDirectory() (string, error) {
	tempDir := modelHostSystemTempDir()
	if tempDir == "" || !filepath.IsAbs(tempDir) || identity.LocalPath(tempDir) != nil {
		return "", errors.New("the system temporary folder must be on a local drive to stage the selected engine archive")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(tempDir))
	if err != nil || identity.LocalPath(resolved) != nil {
		return "", errors.New("the system temporary folder must resolve to a local drive to stage the selected engine archive")
	}
	return resolved, nil
}

func modelHostEngineForm(w http.ResponseWriter, r *http.Request, maxBytes int64) (values map[string]string, filename, tempPath string, resultErr error) {
	createdTempPath := ""
	defer func() {
		if resultErr != nil && createdTempPath != "" {
			_ = os.Remove(createdTempPath)
		}
	}()
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return nil, "", "", errors.New("choose the pinned engine archive")
	}
	tempDir, err := modelHostEngineTempDirectory()
	if err != nil {
		return nil, "", "", err
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	mr := multipart.NewReader(r.Body, params["boundary"])
	values = map[string]string{}
	for {
		part, nextErr := mr.NextPart()
		if nextErr == io.EOF {
			break
		}
		if nextErr != nil {
			return nil, "", "", nextErr
		}
		if part.FormName() == "archive" {
			if filename != "" || part.FileName() == "" {
				part.Close()
				return nil, "", "", errors.New("select one engine archive")
			}
			filename = filepath.Base(part.FileName())
			file, err := createModelHostEngineTemp(tempDir, "oa-modelhost-engine-*.zip")
			if err != nil {
				part.Close()
				return nil, "", "", err
			}
			tempPath = file.Name()
			createdTempPath = tempPath
			written, copyErr := io.Copy(file, io.LimitReader(part, maxBytes+1))
			closePartErr := part.Close()
			closeFileErr := file.Close()
			if err := errors.Join(copyErr, closePartErr, closeFileErr); err != nil {
				_ = os.Remove(tempPath)
				return nil, "", "", err
			}
			if written > maxBytes {
				_ = os.Remove(tempPath)
				return nil, "", "", errors.New("engine archive exceeds the pinned size")
			}
			continue
		}
		if !slices.Contains([]string{"plan_id", "pin_digest", "approval_digest", "confirm"}, part.FormName()) {
			part.Close()
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
			return nil, "", "", errors.New("unexpected engine form field")
		}
		value, err := io.ReadAll(io.LimitReader(part, 2049))
		part.Close()
		_, duplicate := values[part.FormName()]
		if err != nil || len(value) > 2048 || duplicate {
			if tempPath != "" {
				_ = os.Remove(tempPath)
			}
			return nil, "", "", errors.New("invalid engine form fields")
		}
		values[part.FormName()] = string(value)
	}
	if filename == "" || tempPath == "" {
		return nil, "", "", errors.New("choose one local engine archive")
	}
	return values, filename, tempPath, nil
}

func (p *servicePanel) stageModelHostEngine(ctx context.Context, home setup.Home, programs modelHostPrograms, pin modelHostPin, archivePath string) (modelHostEngineReceipt, error) {
	return p.stageModelHostEngineSource(ctx, home, programs, pin, archivePath, "archive")
}

func (p *servicePanel) stageModelHostBundledEngine(ctx context.Context, home setup.Home, programs modelHostPrograms, pin modelHostPin, archivePath string) (modelHostEngineReceipt, error) {
	return p.stageModelHostEngineSource(ctx, home, programs, pin, archivePath, "bundle")
}

func (p *servicePanel) stageModelHostEngineSource(ctx context.Context, home setup.Home, programs modelHostPrograms, pin modelHostPin, archivePath, source string) (modelHostEngineReceipt, error) {
	archiveFlag, archiveName, archiveSHA := "--archive", pin.Archive, pin.SHA256
	statePath := modelHostProviderState(home)
	if err := identity.LocalPath(statePath); err != nil {
		return modelHostEngineReceipt{}, fmt.Errorf("ModelHost private state must be on a local drive: %w", err)
	}
	if source == "bundle" {
		archiveFlag, archiveName, archiveSHA = "--bundle", pin.Bundle.Name, pin.Bundle.SHA256
	} else if source != "archive" {
		return modelHostEngineReceipt{}, errors.New("unknown ModelHost engine archive source")
	}
	output, err := p.modelHostCommand(ctx, programs.ModelHost, "engine", "install", archiveFlag, archivePath, "--dir", pin.EngineDir, "--state", statePath)
	if err != nil {
		return modelHostEngineReceipt{}, fmt.Errorf("the pinned engine %s was not staged: %w", source, err)
	}
	const marker = "pass --engine "
	text := string(output)
	start := strings.LastIndex(text, marker)
	if start < 0 {
		return modelHostEngineReceipt{}, errors.New("ModelHost staged the engine but did not return its program path; " + modelHostRestage(pin) + " to reconcile")
	}
	enginePath := strings.TrimSpace(strings.SplitN(text[start+len(marker):], "\n", 2)[0])
	if !filepath.IsAbs(enginePath) {
		return modelHostEngineReceipt{}, errors.New("ModelHost returned a non-absolute engine path")
	}
	same, err := identity.SameFile(filepath.Dir(enginePath), pin.EngineDir)
	if err != nil || !same {
		return modelHostEngineReceipt{}, errors.New("ModelHost returned an engine path outside the pinned cache folder")
	}
	if _, err := p.modelHostCommand(ctx, programs.ModelHost, "engine", "check", "--engine", enginePath, "--state", statePath); err != nil {
		return modelHostEngineReceipt{}, fmt.Errorf("the staged engine did not pass its pin check: %w", err)
	}
	receipt := modelHostEngineReceipt{Format: 1, Release: pin.Release, Archive: archiveName, SHA256: archiveSHA, EngineDir: pin.EngineDir, Engine: enginePath, Source: source}
	receiptData, _ := json.MarshalIndent(receipt, "", "  ")
	receiptData = append(receiptData, '\n')
	receiptPath := modelHostEngineReceiptPath(home)
	if err := writeModelHostRecord(receiptPath, receiptData); err != nil {
		var existing modelHostEngineReceipt
		if readModelHostRecord(receiptPath, &existing) != nil || existing != receipt {
			return modelHostEngineReceipt{}, fmt.Errorf("the engine passed its pin check but Setup could not save its exact receipt: %w", err)
		}
	}
	return receipt, nil
}

func (p *servicePanel) setupModelHostEngine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if !p.setupTryStart(w) {
		return
	}
	defer p.setupActionMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	// The fixed cap accepts the currently pinned Windows archive plus multipart
	// framing; the pinned byte count is checked again before the public installer.
	values, filename, archivePath, err := modelHostEngineForm(w, r, 64<<20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer os.Remove(archivePath)
	if values["confirm"] != "true" {
		http.Error(w, "explicit approval of the pinned engine archive is required", http.StatusBadRequest)
		return
	}
	if _, _, err := p.modelHostMutationPlan(ctx, values["plan_id"]); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	feature, err := p.modelHostFeatureStatus()
	if err != nil || !feature.Installed {
		if err == nil {
			err = errors.New("add the OpenAbstractions ModelHost feature first")
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	programs, err := p.modelHostPrograms()
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		if err == nil {
			err = errors.New("ModelHost Setup requires the installed runtime's user state")
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	pin, err := p.modelHostPin(ctx, programs, home)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if pin.ArchiveRefused {
		http.Error(w, modelHostArchiveRefused, http.StatusConflict)
		return
	}
	if filename != pin.Archive || !modelHostSameDigest(values["pin_digest"], pin.SHA256) ||
		subtle.ConstantTimeCompare([]byte(values["approval_digest"]), []byte(modelHostEngineApprovalDigest(pin))) != 1 {
		http.Error(w, "the selected archive or pin changed; review the installed pin again", http.StatusConflict)
		return
	}
	info, err := os.Stat(archivePath)
	if err != nil || info.Size() != pin.Size {
		http.Error(w, "the selected archive size does not match the installed pin", http.StatusBadRequest)
		return
	}
	receipt, err := p.stageModelHostEngine(ctx, home, programs, pin, archivePath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	panelJSON(w, struct {
		Staged  bool   `json:"staged"`
		Path    string `json:"path"`
		Release string `json:"release"`
	}{Staged: true, Path: receipt.Engine, Release: pin.Release})
}

func (p *servicePanel) setupModelHostBundledEngine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request modelHostBundledEngineRequest
	if !decodeModelHostJSON(w, r, &request, 2048) {
		return
	}
	if !request.Confirm || request.ApprovalDigest == "" {
		http.Error(w, "explicit approval of the exact bundled engine operation is required", http.StatusBadRequest)
		return
	}
	if !p.setupTryStart(w) {
		return
	}
	defer p.setupActionMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	if _, _, err := p.modelHostMutationPlan(ctx, request.PlanID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	receipt, pin, err := p.modelHostStageBundled(ctx, modelHostApprovedBy(request.ApprovalDigest))
	if err != nil {
		writeModelHostError(w, err)
		return
	}
	panelJSON(w, struct {
		Staged  bool   `json:"staged"`
		Path    string `json:"path"`
		Release string `json:"release"`
	}{Staged: true, Path: receipt.Engine, Release: pin.Release})
}

// modelHostStatusError carries the HTTP status a ModelHost Setup operation's
// refusal has on the Panel's routes.
type modelHostStatusError struct {
	status int
	err    error
}

func (e *modelHostStatusError) Error() string { return e.err.Error() }
func (e *modelHostStatusError) Unwrap() error { return e.err }

func modelHostFail(status int, err error) error {
	return &modelHostStatusError{status: status, err: err}
}

func writeModelHostError(w http.ResponseWriter, err error) {
	var status *modelHostStatusError
	if errors.As(err, &status) {
		http.Error(w, status.err.Error(), status.status)
		return
	}
	http.Error(w, err.Error(), http.StatusConflict)
}

// modelHostApprovedBy admits the operation whose approval digest the person
// reviewed on the Setup page.
func modelHostApprovedBy(reviewed string) func(string) bool {
	return func(digest string) bool { return subtle.ConstantTimeCompare([]byte(reviewed), []byte(digest)) == 1 }
}

// modelHostInstalled is the installed runtime's user state, the ModelHost
// feature and its programs, which every ModelHost Setup operation needs.
func (p *servicePanel) modelHostInstalled(ctx context.Context, needFeature bool) (setup.Home, modelHostPrograms, modelHostPin, error) {
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		if err == nil {
			err = errors.New("ModelHost Setup requires the installed runtime's user state")
		}
		return setup.Home{}, modelHostPrograms{}, modelHostPin{}, modelHostFail(http.StatusConflict, err)
	}
	if needFeature {
		feature, err := p.modelHostFeatureStatus()
		if err != nil || !feature.Installed {
			if err == nil {
				err = errors.New("add the OpenAbstractions ModelHost feature first")
			}
			return setup.Home{}, modelHostPrograms{}, modelHostPin{}, modelHostFail(http.StatusConflict, err)
		}
	}
	programs, err := p.modelHostPrograms()
	if err != nil {
		return setup.Home{}, modelHostPrograms{}, modelHostPin{}, modelHostFail(http.StatusConflict, err)
	}
	pin, err := p.modelHostPin(ctx, programs, home)
	if err != nil {
		return setup.Home{}, modelHostPrograms{}, modelHostPin{}, modelHostFail(http.StatusConflict, err)
	}
	return home, programs, pin, nil
}

// modelHostStageBundled stages the engine archive the installation bundles.
// approve admits the operation's approval digest: the Setup page's reviewed
// one, or the setup step's one answer.
func (p *servicePanel) modelHostStageBundled(ctx context.Context, approve func(string) bool) (modelHostEngineReceipt, modelHostPin, error) {
	home, programs, pin, err := p.modelHostInstalled(ctx, true)
	if err != nil {
		return modelHostEngineReceipt{}, modelHostPin{}, err
	}
	bundled := p.modelHostBundledArchive(ctx, programs, pin)
	if !bundled.Available {
		return modelHostEngineReceipt{}, modelHostPin{}, modelHostFail(http.StatusConflict, errors.New(bundled.Reason))
	}
	if !approve(bundled.ApprovalDigest) {
		return modelHostEngineReceipt{}, modelHostPin{}, modelHostFail(http.StatusConflict, errors.New("the bundled archive, destination or installed pin changed; review the operation again"))
	}
	archivePath := modelHostBundledArchivePath(programs, pin)
	receipt, err := p.stageModelHostBundledEngine(ctx, home, programs, pin, archivePath)
	if err != nil {
		return modelHostEngineReceipt{}, modelHostPin{}, modelHostFail(http.StatusConflict, err)
	}
	return receipt, pin, nil
}

func (p *servicePanel) modelHostRoot(path string, home setup.Home) (setup.ModelHostChatRoot, string, string, error) {
	root, err := setup.NewModelHostChatRoot(path, home.Config)
	if err != nil {
		return setup.ModelHostChatRoot{}, "", "", err
	}
	approval := setup.ModelHostChatApproval(root)
	root.ConfigSHA256 = ""
	return root, approval, setup.ModelHostChatApprovalDigest(approval), nil
}

func (p *servicePanel) setupModelHostRootPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		Path string `json:"path"`
	}
	if !decodeModelHostJSON(w, r, &request, 4096) {
		return
	}
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		if err == nil {
			err = errors.New("ModelHost Setup requires the installed runtime's user state")
		}
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	root, approval, digest, err := p.modelHostRoot(request.Path, home)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	panelJSON(w, struct {
		Root           setup.ModelHostChatRoot `json:"root"`
		Approval       string                  `json:"approval"`
		ApprovalDigest string                  `json:"approval_digest"`
	}{root, approval, digest})
}

func modelHostProviderArguments(root setup.ModelHostChatRoot, program string) []string {
	return []string{"serve", "--endpoint", "{endpoint}", "--config", root.ConfigPath, "--config-roots-only"}
}

func modelHostProviderDeclaration(raw []byte, root setup.ModelHostChatRoot, program string) (bool, error) {
	var list fwire.DeclarationList
	if err := json.Unmarshal(raw, &list); err != nil {
		return false, err
	}
	if list.Outcome != fwire.DeclarationListOutcomePage {
		return false, fmt.Errorf("provider registry is %s", list.Outcome.String())
	}
	for _, state := range list.Declarations {
		d := state.Declaration
		if d.Name != root.Provider {
			continue
		}
		sameProgram := identity.SameSubjectProgram(d.Program, program)
		resources := []string{"store:" + root.Store}
		contracts := []string{modelHostInventoryContract}
		return sameProgram && d.Role == fwire.DeclarationRoleProvider && d.Transport == fwire.DeclarationTransportNative &&
			d.Endpoint == root.Endpoint && slices.Equal(d.Arguments, modelHostProviderArguments(root, program)) &&
			slices.Equal(d.Contracts, contracts) && len(d.Guarantees) == 0 && slices.Equal(d.Resources, resources) &&
			len(d.Models) == 0 && d.Remote == nil && d.Host == nil && d.Activation == fwire.ActivationOnDemand, nil
	}
	return false, nil
}

func (p *servicePanel) listModelHostProvider(ctx context.Context, root setup.ModelHostChatRoot, program string) (bool, error) {
	raw, err := p.modelHostOpenAbstractions(ctx, "provider", "list", "--json", "--timeout", "30s")
	if err != nil {
		return false, err
	}
	return modelHostProviderDeclaration(raw, root, program)
}

func (p *servicePanel) addModelHostInventoryProvider(ctx context.Context, root setup.ModelHostChatRoot, program string) error {
	args := []string{"provider", "add", root.Provider, "--program", program, "--provider-endpoint", root.Endpoint,
		"--contract", modelHostInventoryContract, "--arg=serve", "--arg=--endpoint", "--arg={endpoint}",
		"--arg=--config", "--arg=" + root.ConfigPath, "--arg=--config-roots-only", "--store", root.Store, "--json", "--timeout", "30s"}
	_, addErr := p.modelHostOpenAbstractions(ctx, args...)
	exact, listErr := p.listModelHostProvider(ctx, root, program)
	if exact {
		return nil
	}
	if listErr != nil {
		return errors.Join(addErr, listErr)
	}
	if addErr != nil {
		return addErr
	}
	return errors.New("the runtime did not retain the Setup-owned inventory provider declaration")
}

func (p *servicePanel) modelHostReadInventory(ctx context.Context) (modelHostInventoryReader, error) {
	if p.modelHostInventory != nil {
		return p.modelHostInventory, nil
	}
	requirements := client.Requirements{Scope: client.ScopeLocal}
	if p.modelHostInventoryResolve != nil {
		return p.modelHostInventoryResolve(ctx, requirements)
	}
	return panelMachine().ResolveStorageInventory(ctx, requirements)
}

func modelHostModelApproval(root setup.ModelHostChatRoot, model modelHostChatModel) string {
	return fmt.Sprintf("Allow OpenAbstractions ModelHost to load %s (%d bytes; inventory ID %s) from the selected store %s. Setup declares this exact inventory object with the pinned llama.cpp engine; the original model stays where it is. The engine starts only when an approved application asks the runtime to use it.",
		model.Name, model.Size, model.ID, model.Store)
}

func (p *servicePanel) modelHostModels(ctx context.Context, root setup.ModelHostChatRoot, enginePath string) ([]modelHostChatModel, error) {
	inventory, err := p.modelHostReadInventory(ctx)
	if err != nil {
		return nil, err
	}
	var manifests []content.ManifestHolders
	continuation := ""
	for pages := 0; pages < 512; pages++ {
		page, err := inventory.ListStore(ctx, root.Store, continuation, 256)
		if err != nil {
			return nil, err
		}
		if page.Outcome != content.InventoryOutcomePage {
			return nil, fmt.Errorf("storage inventory is %s", page.Outcome.String())
		}
		for _, item := range page.Manifests {
			m := item.Manifest
			if m.Store == root.Store && m.Kind == "abstraction.model/descriptor@1" && m.Descriptor["format"] == "gguf" {
				manifests = append(manifests, item)
			}
		}
		if page.Complete {
			break
		}
		if page.Continuation == "" || page.Continuation == continuation {
			return nil, errors.New("storage inventory returned an invalid continuation")
		}
		continuation = page.Continuation
		if pages == 511 {
			return nil, errors.New("storage inventory exceeded the Setup model list limit")
		}
	}
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		return nil, err
	}
	models := make([]modelHostChatModel, 0, len(manifests))
	for _, item := range manifests {
		m := item.Manifest
		var size int64
		var digests []string
		for _, entry := range m.Entries {
			if entry.Role == "weights" && entry.Size > 0 && entry.Locator != "" {
				size += entry.Size
				digests = append(digests, entry.Digest)
			}
		}
		if size == 0 {
			continue
		}
		name := m.ID
		for _, n := range m.Names {
			if n.Scheme == root.Store {
				name = n.Name
				break
			}
		}
		model := modelHostChatModel{ID: m.ID, Store: m.Store, Name: name, Family: m.Descriptor["family"], Quant: m.Descriptor["quant"], Size: size}
		if len(digests) == 1 {
			model.Digest = digests[0]
		}
		for _, saved := range hosted {
			if saved.Store == model.Store && saved.Object == model.ID {
				model.Hosted = true
			}
		}
		models = append(models, model)
	}
	slices.SortFunc(models, func(a, b modelHostChatModel) int { return strings.Compare(a.Name, b.Name) })
	_ = enginePath
	return models, nil
}

func (p *servicePanel) modelHostHosted(ctx context.Context) ([]struct {
	Name   string `json:"Name"`
	Store  string `json:"Store"`
	Object string `json:"Object"`
}, error) {
	raw, err := p.modelHostOpenAbstractions(ctx, "models", "hosts", "--json", "--timeout", "30s")
	if err != nil {
		return nil, err
	}
	var models []struct {
		Name   string `json:"Name"`
		Store  string `json:"Store"`
		Object string `json:"Object"`
	}
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, fmt.Errorf("models hosts response: %w", err)
	}
	return models, nil
}

func modelHostConfigDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func modelHostInventoryReceiptBytes(receipt modelHostInventoryReceipt) ([]byte, error) {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (p *servicePanel) modelHostSavedRoot(ctx context.Context, home setup.Home, programs modelHostPrograms, pin modelHostPin, enginePath string) (*modelHostRootState, error) {
	entries, err := os.ReadDir(modelHostBase(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved *modelHostRootState
	for _, entry := range entries {
		if !entry.IsDir() || !modelHostRootID(entry.Name()) {
			continue
		}
		path := filepath.Join(modelHostBase(home), entry.Name(), "receipt.json")
		var receipt modelHostInventoryReceipt
		if err := readModelHostRecord(path, &receipt); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		if saved != nil {
			return nil, errors.New("Setup has more than one saved model root; choose one from the Models page")
		}
		if !modelHostSavedRootMatches(receipt.Root, home.Config) {
			return nil, errors.New("the saved model folder identity or Setup receipt changed; select the folder again")
		}
		data, err := setup.ModelHostChatInventoryConfig(receipt.Root)
		if err != nil || receipt.Format != 1 || receipt.ConfigSHA256 != modelHostConfigDigest(data) || receipt.Program != programs.Inventory ||
			receipt.Contract != modelHostInventoryContract || receipt.Permission != "inventory.provide store:"+receipt.Root.Store {
			return nil, errors.New("the saved inventory receipt does not match its explicit-root config")
		}
		config, err := os.ReadFile(receipt.Root.ConfigPath)
		if err != nil || !bytes.Equal(config, data) {
			return nil, errors.New("the saved inventory config changed; review the selected model folder again")
		}
		exact, err := p.listModelHostProvider(ctx, receipt.Root, programs.Inventory)
		if err != nil {
			return nil, err
		}
		state := &modelHostRootState{ModelHostChatRoot: receipt.Root, Registered: exact, Approval: setup.ModelHostChatApproval(receipt.Root)}
		state.ApprovalDigest = setup.ModelHostChatApprovalDigest(state.Approval)
		if !exact {
			state.Reason = "The runtime no longer has the exact Setup inventory declaration. Re-register this folder after reviewing its permission."
		} else {
			state.Models, err = p.modelHostModels(ctx, receipt.Root, enginePath)
			if err != nil {
				state.Reason = "The provider is registered, but its inventory is not available yet: " + err.Error()
			}
		}
		saved = state
	}
	return saved, nil
}

func modelHostRootID(value string) bool {
	if len(value) != 24 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// Setup currently presents one selected model folder. Keep a second approved
// root from making both roots unusable when the page has no root picker.
func modelHostRootRegistrationSlot(home setup.Home, selected setup.ModelHostChatRoot) error {
	base := modelHostBase(home)
	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !modelHostRootID(entry.Name()) {
			continue
		}
		rootDir := filepath.Join(base, entry.Name())
		var receipt modelHostInventoryReceipt
		err := readModelHostRecord(filepath.Join(rootDir, "receipt.json"), &receipt)
		if errors.Is(err, os.ErrNotExist) {
			config, configErr := os.ReadFile(filepath.Join(rootDir, "inventoryd.json"))
			if errors.Is(configErr, os.ErrNotExist) {
				continue
			}
			if configErr != nil {
				return configErr
			}
			if entry.Name() != selected.ID {
				return errors.New("Setup has an unfinished model-folder registration; refresh or reconcile that folder before choosing another")
			}
			approvedConfig, approvedErr := setup.ModelHostChatInventoryConfig(selected)
			if approvedErr != nil || !bytes.Equal(config, approvedConfig) {
				return errors.New("the unfinished Setup inventory config does not match this selected folder; review the folder again")
			}
			// The exact selected folder and config match. Let registration retry
			// complete the provider add and its receipt after an interrupted run.
			continue
		}
		if err != nil {
			return fmt.Errorf("the saved model-folder receipt cannot be checked: %w", err)
		}
		if receipt.Format != 1 || receipt.Root.ID != entry.Name() || !modelHostSavedRootMatches(receipt.Root, home.Config) {
			return errors.New("the saved model-folder identity or Setup receipt changed; refresh it before choosing another folder")
		}
		if receipt.Root.ID != selected.ID || receipt.Root.Path != selected.Path {
			return fmt.Errorf("Setup already has a saved model folder at %s; refresh to continue with it before choosing another folder", receipt.Root.Path)
		}
	}
	return nil
}

func modelHostSavedRootMatches(root setup.ModelHostChatRoot, configRoot string) bool {
	expected, err := setup.NewModelHostChatRoot(root.Path, configRoot)
	if err != nil {
		return false
	}
	expected.ConfigSHA256 = root.ConfigSHA256
	return expected == root
}

func (p *servicePanel) setupModelHostRootRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request modelHostRootRequest
	if !decodeModelHostJSON(w, r, &request, 8192) {
		return
	}
	if !request.Confirm {
		http.Error(w, "explicit permission to register this model folder is required", http.StatusBadRequest)
		return
	}
	if !p.setupTryStart(w) {
		return
	}
	defer p.setupActionMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if _, _, err := p.modelHostMutationPlan(ctx, request.PlanID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	rootState, err := p.modelHostRegisterRoot(ctx, request.Path, modelHostApprovedBy(request.ApprovalDigest))
	if err != nil {
		writeModelHostError(w, err)
		return
	}
	panelJSON(w, rootState)
}

// modelHostRegisterRoot registers one model folder as a Setup-owned
// inventory store. approve admits the folder approval's digest.
func (p *servicePanel) modelHostRegisterRoot(ctx context.Context, path string, approve func(string) bool) (modelHostRootState, error) {
	home, programs, pin, err := p.modelHostInstalled(ctx, true)
	if err != nil {
		return modelHostRootState{}, err
	}
	engine := p.modelHostEngineState(ctx, home, programs.ModelHost, pin)
	if !engine.Staged {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("stage and verify the pinned local engine archive first"))
	}
	root, approval, digest, err := p.modelHostRoot(path, home)
	if err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusBadRequest, err)
	}
	if !approve(digest) {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("the folder or its approval text changed; review it again"))
	}
	if err := modelHostRootRegistrationSlot(home, root); err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, err)
	}
	config, err := setup.ModelHostChatInventoryConfig(root)
	if err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusBadRequest, err)
	}
	root.ConfigSHA256 = modelHostConfigDigest(config)
	if err := os.MkdirAll(filepath.Dir(root.ConfigPath), 0o700); err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, err)
	}
	if existing, err := os.ReadFile(root.ConfigPath); err == nil {
		if !bytes.Equal(existing, config) {
			return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("a different config already occupies this Setup-owned path"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, err)
	}
	if _, err := p.modelHostOpenAbstractions(ctx, "provider", "list", "--json", "--timeout", "30s"); err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusServiceUnavailable, errors.New("could not check the runtime provider registry: "+err.Error()))
	}
	exact, err := p.listModelHostProvider(ctx, root, programs.Inventory)
	if err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusServiceUnavailable, err)
	}
	if !exact {
		// The registry may contain our name with another program or contract.
		// provider add refuses that name instead of replacing it.
		raw, listErr := p.modelHostOpenAbstractions(ctx, "provider", "list", "--json", "--timeout", "30s")
		if listErr != nil {
			return modelHostRootState{}, modelHostFail(http.StatusServiceUnavailable, listErr)
		}
		var declarationList fwire.DeclarationList
		if json.Unmarshal(raw, &declarationList) != nil {
			return modelHostRootState{}, modelHostFail(http.StatusServiceUnavailable, errors.New("invalid provider registry response"))
		}
		for _, declaration := range declarationList.Declarations {
			if declaration.Declaration.Name == root.Provider {
				return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("the Setup provider name is already used by a different declaration"))
			}
		}
	}
	if existing, err := os.ReadFile(root.ConfigPath); errors.Is(err, os.ErrNotExist) {
		if err := writeModelHostRecord(root.ConfigPath, config); err != nil {
			return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("could not write the approved inventory config: "+err.Error()))
		}
	} else if err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, err)
	} else if !bytes.Equal(existing, config) {
		return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("the approved inventory config changed before registration"))
	}
	if !exact {
		if err := p.addModelHostInventoryProvider(ctx, root, programs.Inventory); err != nil {
			return modelHostRootState{}, modelHostFail(http.StatusServiceUnavailable, errors.New("the approved config is saved; provider registration is unresolved and can be retried: "+err.Error()))
		}
	}
	receipt := modelHostInventoryReceipt{Format: 1, Root: root, ConfigSHA256: root.ConfigSHA256, Program: programs.Inventory,
		Arguments: modelHostProviderArguments(root, programs.Inventory), Contract: modelHostInventoryContract,
		Permission: "inventory.provide store:" + root.Store}
	receiptData, err := modelHostInventoryReceiptBytes(receipt)
	if err != nil {
		return modelHostRootState{}, modelHostFail(http.StatusInternalServerError, err)
	}
	if err := writeModelHostRecord(root.ReceiptPath, receiptData); err != nil {
		var existing modelHostInventoryReceipt
		if readModelHostRecord(root.ReceiptPath, &existing) != nil || existing.Format != receipt.Format || existing.Root.ID != receipt.Root.ID || existing.Root.Path != receipt.Root.Path ||
			existing.ConfigSHA256 != receipt.ConfigSHA256 || existing.Program != receipt.Program || !slices.Equal(existing.Arguments, receipt.Arguments) {
			return modelHostRootState{}, modelHostFail(http.StatusConflict, errors.New("provider registration succeeded but Setup could not save its exact receipt: "+err.Error()))
		}
	}
	registered, err := p.listModelHostProvider(ctx, root, programs.Inventory)
	if err != nil || !registered {
		if err == nil {
			err = errors.New("runtime did not retain the exact Setup declaration")
		}
		return modelHostRootState{}, modelHostFail(http.StatusServiceUnavailable, errors.New("provider registration needs reconciliation: "+err.Error()))
	}
	models, modelErr := p.modelHostModels(ctx, root, engine.Path)
	rootState := modelHostRootState{ModelHostChatRoot: root, Registered: true, Models: models, Approval: approval, ApprovalDigest: digest}
	if modelErr != nil {
		rootState.Reason = "The inventory provider is registered; its first inventory read is still pending: " + modelErr.Error()
	}
	return rootState, nil
}

// modelHostUnhostRequest names one hosted inventory object.
type modelHostUnhostRequest struct {
	Store   string `json:"store"`
	ID      string `json:"id"`
	Confirm bool   `json:"confirm"`
}

// setupModelHostUnhost is the Models page's Remove from OA: the model host
// stops hosting one model (openabstractions models unhost). The file stays
// where it is; the last model withdraws the model host and stops its
// engine. The answer is verified against the hosted list.
func (p *servicePanel) setupModelHostUnhost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request modelHostUnhostRequest
	if !decodeModelHostJSON(w, r, &request, 4096) {
		return
	}
	if !request.Confirm {
		http.Error(w, "explicit permission to stop hosting this exact model is required", http.StatusBadRequest)
		return
	}
	if !modelHostInventoryName(request.Store) || !modelHostInventoryName(request.ID) {
		http.Error(w, "invalid model", http.StatusBadRequest)
		return
	}
	if !p.setupTryStart(w) {
		return
	}
	defer p.setupActionMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	p.logAction(ctx, "modelhost.unhost", map[string]string{"model.store": request.Store, "model.object": request.ID})
	// Setup's step keeps the person's removal of its model before the model
	// host forgets it (recordStepRemoval).
	if err := p.recordStepRemoval(ctx, request.Store, request.ID); err != nil {
		writeModelHostError(w, modelHostFail(http.StatusServiceUnavailable, errors.New("OpenAbstractions could not record the removal for Setup, so the model is still hosted: "+err.Error())))
		return
	}
	if _, err := p.modelHostOpenAbstractions(ctx, "models", "unhost", request.Store+"/"+request.ID, "--json", "--timeout", "30s"); err != nil {
		writeModelHostError(w, modelHostFail(http.StatusConflict, errors.New("the model host kept the model: "+err.Error())))
		return
	}
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		writeModelHostError(w, modelHostFail(http.StatusServiceUnavailable, errors.New("the unhost command returned, but the Panel could not verify it: "+err.Error())))
		return
	}
	for _, item := range hosted {
		if item.Store == request.Store && item.Object == request.ID {
			writeModelHostError(w, modelHostFail(http.StatusServiceUnavailable, errors.New("the runtime still hosts the model")))
			return
		}
	}
	panelJSON(w, struct {
		Unhosted bool `json:"unhosted"`
	}{Unhosted: true})
}

// modelHostInventoryName admits a store name or inventory object id: no
// separator the command line's <store>/<id> form would split on.
func modelHostInventoryName(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "/\\\x00") && strings.TrimSpace(value) == value && !strings.HasPrefix(value, "-")
}

// modelHostHostModel declares one inventory object of the Setup-approved
// store as a hosted model. approve admits the model approval's digest.
func (p *servicePanel) modelHostHostModel(ctx context.Context, store, id string, approve func(string) bool, engineArgs []string, name string, content ...string) error {
	home, programs, pin, err := p.modelHostInstalled(ctx, false)
	if err != nil {
		return err
	}
	engine := p.modelHostEngineState(ctx, home, programs.ModelHost, pin)
	if !engine.Staged {
		return modelHostFail(http.StatusConflict, errors.New("stage and verify the pinned local engine archive first"))
	}
	root, err := p.modelHostSavedRoot(ctx, home, programs, pin, engine.Path)
	if err != nil || root == nil || !root.Registered {
		if err == nil {
			err = errors.New("register an approved model folder first")
		}
		return modelHostFail(http.StatusConflict, err)
	}
	if store != root.Store {
		return modelHostFail(http.StatusConflict, errors.New("the selected model is outside the Setup-approved store"))
	}
	model, err := p.findModelHostModel(ctx, *root, engine.Path, id)
	if err != nil {
		return modelHostFail(http.StatusConflict, err)
	}
	if !approve(setup.ModelHostChatApprovalDigest(modelHostModelApproval(root.ModelHostChatRoot, model))) {
		return modelHostFail(http.StatusConflict, errors.New("the selected model changed; review its approval again"))
	}
	_, err = p.modelHostOpenAbstractions(ctx, modelHostHostArgs(store, id, engine.Path, programs.ModelHost, engineArgs, name, content...)...)
	if err != nil {
		return modelHostFail(http.StatusConflict, errors.New("the model host declaration was not written: "+err.Error()))
	}
	hosted, err := p.modelHostHosted(ctx)
	if err != nil {
		return modelHostFail(http.StatusServiceUnavailable, errors.New("the host command returned, but Setup could not verify the declaration: "+err.Error()))
	}
	for _, item := range hosted {
		if item.Store == store && item.Object == id {
			return nil
		}
	}
	return modelHostFail(http.StatusServiceUnavailable, errors.New("the runtime did not retain the hosted model declaration"))
}

// modelHostHostArgs is the openabstractions models host command for one
// inventory object, with each engine argument as its own --engine-arg; the
// command's engine check refuses any outside ModelHost's reviewed list
// (MODELHOST-L5). A name is the one callers ask for (--as); empty keeps
// the store's own name for the object.
func modelHostHostArgs(store, id, enginePath, program string, engineArgs []string, name string, content ...string) []string {
	args := []string{"models", "host", store + "/" + id, "--engine", enginePath, "--program", program}
	if name != "" {
		args = append(args, "--as", name)
	}
	if len(content) == 1 && content[0] != "" {
		args = append(args, "--content", content[0])
	}
	for _, arg := range engineArgs {
		args = append(args, "--engine-arg="+arg)
	}
	return append(args, "--json", "--timeout", "30s")
}

func (p *servicePanel) findModelHostModel(ctx context.Context, root modelHostRootState, enginePath, id string) (modelHostChatModel, error) {
	models, err := p.modelHostModels(ctx, root.ModelHostChatRoot, enginePath)
	if err != nil {
		return modelHostChatModel{}, err
	}
	for _, model := range models {
		if model.ID == id {
			return model, nil
		}
	}
	return modelHostChatModel{}, errors.New("that model is no longer in the selected store; refresh the inventory")
}

var _ = multipart.ErrMessageTooLarge

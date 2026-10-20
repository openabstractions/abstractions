package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"

	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The full legacy descriptor remains the conditional edit input. The @3
// manifest display projection cannot reconstruct that descriptor.
func readClaudeStartup(ctx context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
	return readClaudeStartupSelected(ctx, claudeIntegrationApplication, "")
}

func readClaudeStartupSelected(ctx context.Context, application, program string) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
	directory, err := panelMachine().ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, err
	}
	page, err := directory.Observe(ctx, "", 0)
	if err != nil || page.Outcome != wire.ApplicationOutcomePage {
		return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, errors.New("current connection descriptor unavailable")
	}
	descriptor, err := claudeStartupDescriptorSelected(page, application, program)
	if err != nil {
		return descriptor, applications.AgentEnrollmentResult{}, err
	}
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return descriptor, applications.AgentEnrollmentResult{}, err
	}
	enrollment, err := applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReadAgentEnrollment(descriptor.Name)
	return descriptor, enrollment, err
}

func claudeStartupDescriptor(page wire.ApplicationPage) (wire.ApplicationDescriptor, error) {
	return claudeStartupDescriptorSelected(page, claudeIntegrationApplication, "")
}

func claudeStartupDescriptorSelected(page wire.ApplicationPage, application, program string) (wire.ApplicationDescriptor, error) {
	if page.Outcome != wire.ApplicationOutcomePage {
		return wire.ApplicationDescriptor{}, errors.New("current connection descriptor unavailable")
	}
	var descriptor wire.ApplicationDescriptor
	for _, entry := range page.Applications {
		if entry.Scope == wire.ScopeLocal && entry.Descriptor.Name == application {
			if program != "" && entry.Descriptor.Program != program {
				return descriptor, errors.New("saved connection program changed")
			}
			if descriptor.Name != "" {
				return descriptor, errors.New("connection descriptor ambiguous")
			}
			descriptor = entry.Descriptor
		}
	}
	if descriptor.Name == "" {
		return descriptor, errors.New("saved connection unavailable")
	}
	return descriptor, nil
}

func setClaudeStartup(ctx context.Context, request applications.ActivationConfiguration) (wire.ApplicationChange, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return wire.ApplicationChange{}, err
	}
	return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).SetActivation(request)
}

func finishClaudeStartup(ctx context.Context, expected wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return wire.ApplicationChange{}, err
	}
	return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReconcileActivation(expected)
}

type claudeStartupCapture struct {
	descriptor     wire.ApplicationDescriptor
	enrollment     applications.AgentEnrollmentResult
	folder         fileid.FileID
	folderRevision uint64
}

type claudeStartupView struct {
	Outcome     string `json:"outcome"`
	Revision    string `json:"revision"`
	Configured  bool   `json:"configured"`
	Recovery    bool   `json:"recovery"`
	Model       string `json:"model"`
	Folder      string `json:"folder"`
	TimeoutMS   uint64 `json:"timeout_ms"`
	BudgetCents uint64 `json:"budget_cents"`
	OutputBytes uint64 `json:"output_bytes"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

type claudeStartupEdit struct {
	Revision    string `json:"revision"`
	Action      string `json:"action"`
	Model       string `json:"model"`
	TimeoutMS   uint64 `json:"timeout_ms"`
	BudgetCents uint64 `json:"budget_cents"`
	OutputBytes uint64 `json:"output_bytes"`
	MemoryBytes uint64 `json:"memory_bytes"`
}

func validClaudeStartupEnrollment(d wire.ApplicationDescriptor, r applications.AgentEnrollmentResult) bool {
	return r.Outcome == applications.OutcomePage && r.Enrollment != nil && r.Enrollment.State == applications.AgentEnrollmentStateEnrolled && r.Enrollment.Application == d.Name && r.Enrollment.Program == d.Program
}

func (c *claudePanelConnections) readStartup(ctx context.Context, revision string) (view claudeStartupView, err error) {
	if !c.actionMu.TryLock() {
		return view, errors.New("connection setup is already in progress")
	}
	defer c.actionMu.Unlock()
	view = claudeStartupView{Outcome: "unavailable", Revision: revision, OutputBytes: startprofile.MaxOutputBytes, MemoryBytes: startprofile.MaxMemoryBytes}
	c.mu.Lock()
	valid := !c.closed && revision == strconv.FormatUint(c.revision, 10)

	if valid {
		c.startupCapture = nil
	}
	c.mu.Unlock()
	if !valid || c.startupRead == nil {
		return view, errors.New("connection view changed; check again")
	}
	d, enrollment, err := c.startupRead(ctx)
	if err != nil {
		return view, err
	}
	view.Recovery = !validClaudeStartupEnrollment(d, enrollment)
	capture := &claudeStartupCapture{descriptor: d, enrollment: enrollment}
	if d.Activation != nil {
		profile, err := startprofile.Parse(d.Activation.Arguments)
		if err != nil || profile.Name != "claude-v1" {
			return view, errors.New("existing startup recipe is unsupported; preserve it")
		}
		view.Configured, view.Model, view.Folder = true, profile.Model, filepath.Base(profile.Folder)
		view.TimeoutMS, view.BudgetCents, view.OutputBytes, view.MemoryBytes = profile.TimeoutMS, profile.BudgetCents, profile.OutputBytes, profile.MemoryBytes
		capture.folder, err = fileid.Resolve(ctx, fileid.Self, profile.Folder, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !capture.folder.IsDir() {
			capture.folder = fileid.FileID{}
			view.Folder = ""
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || revision != strconv.FormatUint(c.revision, 10) || ctx.Err() != nil {
		return view, errors.New("connection view changed; check again")
	}
	c.startupCapture = capture
	view.Outcome = "page"
	return view, nil
}

func (c *claudePanelConnections) chooseStartupFolder(ctx context.Context, revision string) (string, error) {
	if !c.actionMu.TryLock() {
		return "", errors.New("connection setup is already in progress")
	}
	defer c.actionMu.Unlock()
	if c.picker == nil {
		return "", errors.New("folder selection unavailable")
	}
	c.mu.Lock()
	capture := c.startupCapture
	valid := !c.closed && capture != nil && revision == strconv.FormatUint(c.revision, 10)
	var folderRevision uint64
	if valid {
		capture.folderRevision++
		folderRevision = capture.folderRevision
	}
	c.mu.Unlock()
	if !valid {
		return "", errors.New("startup settings changed; check again")
	}
	path, err := c.picker(ctx)
	if err != nil || path == "" {
		return "", err
	}
	folder, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !folder.IsDir() {
		return "", errors.New("working folder unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.startupCapture != capture || capture.folderRevision != folderRevision || revision != strconv.FormatUint(c.revision, 10) || ctx.Err() != nil {
		return "", errors.New("folder selection belongs to an earlier view")
	}
	capture.folder = folder
	return filepath.Base(folder.Path()), nil
}

func (c *claudePanelConnections) changeStartup(ctx context.Context, edit claudeStartupEdit) (resultErr error) {
	if edit.Action != "configure" && edit.Action != "disable" && edit.Action != "finish" {
		return errors.New("startup action unavailable")
	}
	if !c.actionMu.TryLock() {
		return errors.New("connection setup is already in progress")
	}
	defer c.actionMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	capture := c.startupCapture
	valid := !c.closed && capture != nil && edit.Revision == strconv.FormatUint(c.revision, 10)
	if valid {
		c.activeCancel = cancel
	}
	c.mu.Unlock()
	if !valid || c.startupSet == nil {
		return errors.New("startup settings changed; check again")
	}
	if edit.Action == "finish" {
		if c.startupFinish == nil {
			return errors.New("startup recovery unavailable")
		}
		changed, err := c.startupFinish(ctx, capture.descriptor)
		if err != nil {
			return fmt.Errorf("finish saving outcome unknown; check saved settings: %w", err)
		}
		if changed.Outcome != wire.ApplicationOutcomeApplied {
			return fmt.Errorf("startup recovery unavailable (%s)", changed.Outcome)
		}
		c.mu.Lock()
		if c.startupCapture == capture {
			c.startupCapture = nil
		}
		c.mu.Unlock()
		return ctx.Err()
	}

	if c.startupRead == nil || c.startupImage == nil {
		return errors.New("startup settings unavailable")
	}
	image, err := c.startupImage(ctx)
	if image != nil {
		defer func() { resultErr = errors.Join(resultErr, image.Close()) }()
	}
	if err != nil || image == nil {
		return errors.New("kept connection image unavailable")
	}
	if image.Program() != capture.descriptor.Program || image.Check(ctx) != nil {
		return errors.New("kept connection image changed")
	}
	var activation *wire.ApplicationActivationRecipe
	guard := func(context.Context) error { return nil }
	if edit.Action == "configure" {
		// Proposed operational defaults for optional Advanced overrides. The
		// runtime approval resolver remains disabled pending C59 receiving.
		if edit.OutputBytes == 0 {
			edit.OutputBytes = startprofile.MaxOutputBytes
		}
		if edit.MemoryBytes == 0 {
			edit.MemoryBytes = startprofile.MaxMemoryBytes
		}
		if c.startupCLI == nil || c.state == nil || !capture.folder.IsDir() {
			return errors.New("choose a working folder and explicit limits")
		}
		cli, err := c.startupCLI(ctx)
		if cli != nil {
			defer func() { resultErr = errors.Join(resultErr, cli.Close()) }()
		}
		if err != nil || cli == nil {
			return errors.New("saved Code identity unavailable")
		}
		id, err := fileid.Resolve(ctx, fileid.Self, cli.Program(), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || id.IsDir() || id.Key() != cli.FileKey() {
			return errors.New("saved Code identity changed")
		}
		stored, err := id.Stored()
		if err != nil {
			return err
		}
		state, err := c.state()
		if err != nil {
			return err
		}
		stateID, err := fileid.Resolve(ctx, fileid.Self, state, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !stateID.IsDir() {
			return errors.New("connection state folder unavailable")
		}
		folder, err := fileid.Resolve(ctx, fileid.Self, capture.folder.Path(), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !folder.Equal(capture.folder) {
			return errors.New("working folder changed; choose it again")
		}
		contextName := ""
		if capture.descriptor.Activation != nil {
			prior, err := startprofile.Parse(capture.descriptor.Activation.Arguments)
			if err != nil {
				return err
			}
			contextName = prior.Context
		}
		if contextName == "" {
			contextName, err = applications.NewRequestKey()
			if err != nil {
				return err
			}
			contextName = "receiver-" + contextName
		}
		profile := startprofile.Profile{Name: "claude-v1", Context: contextName, Folder: folder.Path(), StateDir: stateID.Path(), CLIFileID: stored.String(), Model: edit.Model, TimeoutMS: edit.TimeoutMS, BudgetCents: edit.BudgetCents, OutputBytes: edit.OutputBytes, MemoryBytes: edit.MemoryBytes}
		if _, err := startprofile.Parse(profile.Arguments()); err != nil {
			return err
		}
		activation = &wire.ApplicationActivationRecipe{Arguments: profile.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 15000}
		guard = func(ctx context.Context) error {
			if err := cli.Check(ctx); err != nil {
				return err
			}
			for _, prior := range []fileid.FileID{id, folder, stateID} {
				current, err := fileid.Resolve(ctx, fileid.Self, prior.Path(), fileid.Options{Links: fileid.RefuseLinks})
				if err != nil || !current.Equal(prior) {
					return errors.New("startup identity changed; check again")
				}
			}
			return ctx.Err()
		}
	}
	current, enrollment, err := c.startupRead(ctx)
	if err != nil || !reflect.DeepEqual(current, capture.descriptor) || !reflect.DeepEqual(enrollment, capture.enrollment) || !validClaudeStartupEnrollment(current, enrollment) {
		return errors.New("connection or enrollment changed; check again")
	}
	if err := image.Check(ctx); err != nil {
		return err
	}
	if err := guard(ctx); err != nil {
		return err
	}
	c.mu.Lock()
	valid = !c.closed && c.startupCapture == capture && edit.Revision == strconv.FormatUint(c.revision, 10) && ctx.Err() == nil
	c.mu.Unlock()
	if !valid {
		return errors.New("startup edit belongs to an earlier view")
	}
	changed, err := c.startupSet(ctx, applications.ActivationConfiguration{Expected: capture.descriptor, Activation: activation})
	c.mu.Lock()
	if c.startupCapture == capture {
		c.startupCapture = nil
	}
	c.mu.Unlock()
	if err != nil {
		return fmt.Errorf("startup outcome unknown; check saved settings before changing them: %w", err)
	}
	if changed.Outcome != wire.ApplicationOutcomeApplied {
		return fmt.Errorf("startup settings were not saved (%s); check saved settings", changed.Outcome)
	}
	return ctx.Err()
}

func (p *servicePanel) claudeStartupRoutes(mux *http.ServeMux, key string) {
	for _, action := range []string{"state", "folder", "configure", "disable", "finish"} {
		mux.HandleFunc("/connections/claude/startup/"+action, guard(key, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "POST required", 405)
				return
			}
			var edit claudeStartupEdit
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&edit) != nil || decoder.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid startup settings", 400)
				return
			}
			control := p.claudeConnectionsForPanel()
			if action == "state" {
				view, err := control.readStartup(r.Context(), edit.Revision)
				if err != nil {
					panelError(w, err)
					return
				}
				panelJSON(w, view)
				return
			}
			if action == "folder" {
				folder, err := control.chooseStartupFolder(r.Context(), edit.Revision)
				if err != nil {
					panelError(w, err)
					return
				}
				panelJSON(w, map[string]string{"folder": folder})
				return
			}
			edit.Action = action
			if err := control.changeStartup(r.Context(), edit); err != nil {
				panelError(w, err)
				return
			}
			panelJSON(w, map[string]string{"outcome": "saved"})
		}))
	}
}

func configureClaudeStartup(c *claudePanelConnections) {
	c.startupRead, c.startupSet = readClaudeStartup, setClaudeStartup
	if runtime.GOOS == "darwin" {
		c.startupRead = func(ctx context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
			program, err := c.selectedUserHelperProgram()
			if err != nil {
				return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, err
			}
			application := nativecli.AgentApplicationName(filepath.Base(program))
			if nativecli.AgentApplicationBase(application) != claudeIntegrationApplication {
				return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, errors.New("saved connection application unavailable")
			}
			return readClaudeStartupSelected(ctx, application, program)
		}
	}
	c.startupFinish = finishClaudeStartup
	c.startupImage = func(ctx context.Context) (claudePanelImage, error) {
		return selectClaudeStartupImage(ctx, func(ctx context.Context) (*setup.EnrolledAgentImage, error) {
			if runtime.GOOS == "darwin" {
				program, err := c.selectedUserHelperProgram()
				if err != nil {
					return nil, err
				}
				return setup.HoldEnrolledAgentImageFrom(ctx, panelMachine(), setup.AgentClaude, program)
			}
			return setup.HoldEnrolledAgentImage(ctx, panelMachine(), setup.AgentClaude)
		})
	}
	c.startupCLI = c.selectStartupCLI
}

func (c *claudePanelConnections) selectedUserHelperProgram() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	program := ""
	for _, row := range c.rows {
		if row.Kind == "user" && row.Registration == "saved" {
			if program != "" {
				return "", errors.New("saved connection ambiguous")
			}
			program = row.Program
		}
	}
	if program == "" {
		return "", errors.New("saved connection unavailable")
	}
	return program, nil
}

func selectClaudeStartupImage(ctx context.Context, hold func(context.Context) (*setup.EnrolledAgentImage, error)) (claudePanelImage, error) {
	image, err := hold(ctx)
	if err != nil || image == nil {
		return nil, err
	}
	return image, nil
}

func (c *claudePanelConnections) selectStartupCLI(ctx context.Context) (selected claudePanelImage, err error) {
	if c.state == nil || c.image == nil || c.command == nil {
		return nil, errors.New("saved Code identity unavailable")
	}
	state, err := c.state()
	if err != nil {
		return nil, err
	}
	manager, err := c.image(ctx)
	if err != nil || manager == nil {
		return nil, errors.New("connection manager unavailable")
	}
	defer func() {
		if closeErr := manager.Close(); closeErr != nil {
			if selected != nil {
				closeErr = errors.Join(closeErr, selected.Close())
				selected = nil
			}
			err = errors.Join(err, closeErr)
		}
	}()
	if err := manager.Check(ctx); err != nil {
		return nil, err
	}
	raw, err := c.command(ctx, manager.Program(), "connections", "claude", "--state-dir", state, "--cli-locator")
	if err != nil {
		return nil, err
	}
	if err := manager.Check(ctx); err != nil {
		return nil, err
	}
	return selectClaudeManagementPage(ctx, raw, nil, func(ctx context.Context, stored []string, _ func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
		return nativecli.HoldClaudeSavedCLI(ctx, stored)
	}, func(context.Context, func(context.Context, string) ([]byte, error)) (claudePanelCLI, error) {
		return nil, errors.New("save a Claude connection before configuring startup")
	})
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type codexConnectionView struct {
	Previous     *setup.AgentEnrollmentCapture `json:"previous,omitempty"`
	Supported    bool                          `json:"supported"`
	Outcome      string                        `json:"outcome"`
	Scope        string                        `json:"scope"`
	Registration string                        `json:"registration"`
	Name         string                        `json:"name"`
	Program      string                        `json:"program"`
	Owned        bool                          `json:"owned"`
	Revision     string                        `json:"revision"`
	Available    bool                          `json:"available"`
	Enrollment   string                        `json:"enrollment,omitempty"`
	Reason       string                        `json:"reason,omitempty"`
	// Guidance says in one plain sentence what stops the connection and
	// what to do next; Reason is its closed stage code.
	Guidance          string   `json:"guidance,omitempty"`
	PermissionOutcome string   `json:"permission_outcome,omitempty"`
	PermissionReason  string   `json:"permission_reason,omitempty"`
	HelperState       string   `json:"helper_state,omitempty"`
	RestartHosts      []string `json:"restart_hosts,omitempty"`
	Failure           string   `json:"update_failure,omitempty"`
	SessionStart      string   `json:"session_start,omitempty"`
}
type codexPanelConnections struct {
	platform    func() bool
	mu          sync.Mutex
	action      sync.Mutex
	startRecipe sync.Mutex // one start recipe registration at a time
	revision    uint64
	current     codexConnectionView
	closed      bool
	cancel      context.CancelFunc
	image       claudePanelImage
	cli         func(context.Context) (claudePanelCLI, error)
	manager     func(context.Context) (claudePanelImage, error)
	prepare     func(context.Context) (claudePanelImage, error)
	enroll      func(context.Context, claudePanelImage) (string, error)
	declare     func(context.Context, claudePanelImage) error
	admit       func(context.Context, claudePanelImage) (claudePanelImage, error)
	command     func(context.Context, string, ...string) ([]byte, error)
	state       func() (string, error)
	update      agentHelperUpdater
	helperState func(context.Context, string, string) agentHelperReading
	updateFrom  agentHelperUpdaterFrom
	retire      func(context.Context, setup.AgentEnrollmentCapture) error
	// startupRead and startupSet read and save the start recipe on the
	// connection's registration.
	startupRead func(context.Context, string, string) (fwire.ApplicationDescriptor, applications.AgentEnrollmentResult, error)
	startupSet  func(context.Context, applications.ActivationConfiguration) (fwire.ApplicationChange, error)
}

func (p *servicePanel) codexConnectionsForPanel() *codexPanelConnections {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.codexConnections != nil {
		return p.codexConnections
	}
	c := &codexPanelConnections{command: runCodexPanelCommand, manager: panelGatewayImage, startupRead: readClaudeStartupSelected, startupSet: setClaudeStartup, state: func() (string, error) {
		home, err := setup.CurrentHome()
		if err != nil {
			return "", err
		}
		if home.Isolated {
			return "", errors.New("Codex setup requires the installed OpenAbstractions")
		}
		return filepath.Join(home.Config, "codex-connections"), nil
	}}
	c.cli = func(ctx context.Context) (claudePanelCLI, error) {
		return selectCodexPanelCLI(ctx, func(ctx context.Context, program string) ([]byte, error) { return c.command(ctx, program, "--version") })
	}
	c.prepare = func(ctx context.Context) (claudePanelImage, error) {
		if runtime.GOOS == "darwin" {
			c.mu.Lock()
			view := c.current
			c.mu.Unlock()
			if view.Owned && view.Registration == "saved" {
				return setup.HoldEnrolledAgentImageFrom(ctx, panelMachine(), setup.AgentCodex, view.Program)
			}
		}
		kept, err := setup.HoldEnrolledAgentImage(ctx, panelMachine(), setup.AgentCodex)
		if err == nil {
			return kept, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		created, createErr := setup.PrepareAgentImage(ctx, setup.AgentCodex)
		if createErr == nil {
			return created, nil
		}
		review, reviewErr := setup.HoldAgentImageForReview(ctx, setup.AgentCodex)
		if reviewErr == nil {
			return review, nil
		}
		return nil, errors.Join(err, createErr, reviewErr)
	}
	c.enroll = p.enrollCodexImage
	c.declare = c.declareCompanion
	c.helperState = func(ctx context.Context, program, key string) agentHelperReading {
		return panelAgentHelperReading(ctx, setup.AgentCodex, program, key)
	}
	c.update = func(ctx context.Context, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
		return p.updateAgentHelper(ctx, setup.AgentCodex, verify)
	}
	c.updateFrom = func(ctx context.Context, program string, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
		return p.updateAgentHelperFrom(ctx, setup.AgentCodex, program, verify)
	}
	c.retire = func(ctx context.Context, capture setup.AgentEnrollmentCapture) error {
		return setup.RemoveAgentEnrollment(ctx, panelMachine(), setup.AgentCodex, capture)
	}
	c.admit = func(ctx context.Context, image claudePanelImage) (claudePanelImage, error) {
		return admitAgentImageForConnection(ctx, image, setup.AgentCodex)
	}
	p.codexConnections = c
	return c
}

func (p *servicePanel) enrollCodexImage(ctx context.Context, image claudePanelImage) (string, error) {
	native, err := p.agentEnrollmentForPanel(ctx)
	if err != nil {
		return "unknown", err
	}
	client := claudeContextEnrollmentClient{
		read: native.ReadAgentEnrollment,
		set: func(request applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
			return native.SetAgentEnrollment(ctx, request)
		},
	}
	return agentConnectionSetup(ctx, image, client, func(ctx context.Context, descriptor fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
		directory, err := panelMachine().ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return fwire.ApplicationChange{}, err
		}
		return directory.Register(ctx, descriptor)
	}, "oa-codex-integration", "Codex integration", true, codexEnrollmentFailureFor)
}

// Only the original live Add waits. Reads never create or renew a question.
func waitCodexEnrollment(ctx context.Context, pending applications.AgentEnrollment, read func(context.Context) (applications.AgentEnrollmentResult, error)) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		result, err := read(ctx)
		if err != nil {
			return err
		}
		if result.Outcome != applications.OutcomePage || result.Enrollment == nil {
			return codexEnrollmentFailureFor(result)
		}
		current := result.Enrollment
		if current.Application != pending.Application || current.Program != pending.Program || current.Revision != pending.Revision {
			return errors.New("Codex permission request changed; check again")
		}
		switch current.State {
		case applications.AgentEnrollmentStateEnrolled:
			return ctx.Err()
		case applications.AgentEnrollmentStatePending:
		default:
			return errors.New("Codex permission was not kept; connection preserved")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (c *codexPanelConnections) cancelAction() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revision++
	c.current = codexConnectionView{}
	if c.cancel != nil {
		c.cancel()
	}
}
func (c *codexPanelConnections) close() {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	c.action.Lock()
	defer c.action.Unlock()
	if c.image != nil {
		_ = c.image.Close()
		c.image = nil
	}
}

func decodeCodexView(raw []byte) (codexConnectionView, error) {
	var view codexConnectionView
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if len(raw) > 32<<10 || d.Decode(&view) != nil || d.Decode(new(any)) != io.EOF || view.Outcome != "page" || view.Scope != "user" || (view.Registration != "saved" && view.Registration != "changed" && view.Registration != "absent") {
		return view, errors.New("Codex setup could not be checked")
	}
	switch view.SessionStart {
	case "", "absent", "installed", "unavailable", "preserved":
	default:
		return view, errors.New("Codex hook setup could not be checked")
	}
	return view, nil
}

func (c *codexPanelConnections) supported() bool {
	if c.platform != nil {
		return c.platform()
	}
	return runtime.GOOS == "windows"
}

func (c *codexPanelConnections) read(ctx context.Context) (view codexConnectionView, err error) {
	c.mu.Lock()
	c.revision++
	revision := c.revision
	c.current = codexConnectionView{}
	closed := c.closed
	c.mu.Unlock()
	view = codexConnectionView{Supported: c.supported(), Outcome: "unavailable", Scope: "user", Revision: strconv.FormatUint(revision, 10)}
	defer func() {
		if err != nil {
			var commandFailure *panelConfigurationError
			if errors.As(err, &commandFailure) {
				view.Reason = commandFailure.reason()
			}
			view.Outcome = "unavailable"
			view.Available = false
			view.Owned = false
			view.Registration = ""
			view.Program = ""
			c.mu.Lock()
			if c.revision == revision {
				c.current = codexConnectionView{}
			}
			c.mu.Unlock()
		}
	}()
	if !view.Supported {
		view.Reason = "unsupported_platform"
		return view, errors.New("guided Codex setup is unavailable on this platform")
	}
	if closed {
		view.Reason = "window_closed"
		return view, errors.New("window closed")
	}
	view.Reason = "cli_selection"
	cli, err := c.cli(ctx)
	if err != nil {
		return view, err
	}
	if cli == nil {
		return view, errors.New("Start Codex, then check again")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	if err := cli.Check(ctx); err != nil {
		return view, err
	}
	view.Available = true
	view.Reason = "installed_gateway"
	manager, err := c.manager(ctx)
	if err != nil {
		return view, err
	}
	if manager == nil {
		return view, fmt.Errorf("installed MCP tool unavailable: %w", os.ErrNotExist)
	}
	defer func() { err = errors.Join(err, manager.Close()) }()
	view.Reason = "setup_state"
	state, err := c.state()
	if err != nil {
		return view, err
	}
	if err := manager.Check(ctx); err != nil {
		return view, err
	}
	view.Reason = "configuration_check"
	raw, err := c.command(ctx, manager.Program(), "connections", "codex", "--state-dir", state, "--codex", cli.Program())
	if err != nil {
		return view, err
	}
	observed, err := decodeCodexView(raw)
	if err != nil {
		return view, err
	}
	if err := errors.Join(manager.Check(ctx), cli.Check(ctx)); err != nil {
		return view, err
	}
	observed.HelperState, observed.RestartHosts, observed.Failure = "", nil, ""
	if observed.Owned && observed.Registration == "saved" {
		observed.HelperState = "unknown"
		if c.helperState != nil {
			reading := c.helperState(ctx, observed.Program, "")
			observed.HelperState, observed.RestartHosts, observed.Failure = reading.State, reading.Restart, reading.Failure
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || ctx.Err() != nil || c.revision != revision {
		return view, errors.New("Codex setup view changed")
	}
	observed.Revision = view.Revision
	observed.Supported = true
	observed.Available = true
	c.current = observed
	return observed, nil
}

func (c *codexPanelConnections) change(ctx context.Context, remove bool, revision string) (view codexConnectionView, err error) {
	defer func() {
		var refused *codexEnrollmentFailure
		if errors.As(err, &refused) {
			view = codexConnectionView{Supported: c.supported(), Outcome: "permission_error", PermissionOutcome: refused.outcome, PermissionReason: refused.reason}
		}
	}()
	if !c.supported() {
		return view, errors.New("guided Codex setup is unavailable on this platform")
	}
	if !c.action.TryLock() {
		return view, errors.New("Codex setup is in progress")
	}
	defer c.action.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	snapshot := c.current
	if c.closed || revision != strconv.FormatUint(c.revision, 10) {
		c.mu.Unlock()
		return view, errors.New("Codex setup changed; check again")
	}
	c.cancel = cancel
	c.mu.Unlock()
	cli, err := c.cli(ctx)
	if err != nil {
		return view, err
	}
	if cli == nil {
		return view, errors.New("Codex CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	if err := cli.Check(ctx); err != nil {
		return view, err
	}
	var image claudePanelImage
	action := "connect"
	enrollment := "unknown"
	if remove {
		if !snapshot.Owned || snapshot.Registration != "saved" || snapshot.Program == "" {
			return view, errors.New("Codex setup is changed or unowned; preserved")
		}
		action = "disconnect"
		image, err = c.manager(ctx)
		if err != nil {
			return view, err
		}
		if image == nil {
			return view, errors.New("installed management tool unavailable")
		}
		defer func() { err = errors.Join(err, image.Close()) }()
	} else {
		if c.image != nil && c.image.Check(ctx) != nil {
			if ctx.Err() != nil {
				return view, ctx.Err()
			}
			_ = c.image.Close()
			c.image = nil
		}
		if c.image == nil {
			c.image, err = c.prepare(ctx)
			if err != nil {
				return view, err
			}
		}
		image = c.image
		if image == nil {
			return view, errors.New("Codex integration image unavailable")
		}
		if c.declare != nil {
			if err := c.declare(ctx, image); err != nil {
				return view, err
			}
		}
		enrollment, err = c.enroll(ctx, image)
		if err != nil {
			return view, err
		}
		if enrollment != "enrolled" {
			return codexConnectionView{Outcome: "permission_pending", Enrollment: enrollment, Supported: true, Scope: "user"}, ctx.Err()
		}
		if c.admit == nil {
			return view, errors.New("native integration admission unavailable")
		}
		admitted, admitErr := c.admit(ctx, image)
		if admitErr != nil {
			return view, admitErr
		}
		if admitted == nil {
			return view, errors.New("native integration admission unavailable")
		}
		if admitted.FileKey() != image.FileKey() {
			_ = admitted.Close()
			return view, errors.New("integration changed during permission review")
		}
		if closeErr := image.Close(); closeErr != nil {
			_ = admitted.Close()
			c.image = nil
			return view, closeErr
		}
		image = admitted
		c.image = image
		// Only an unfinished Keep retains the candidate between actions. Once
		// admitted, release it when this command joins so other windows can update.
		defer func() {
			err = errors.Join(err, image.Close())
			c.image = nil
		}()
	}
	if err := image.Check(ctx); err != nil {
		return view, err
	}
	state, err := c.state()
	if err != nil {
		return view, err
	}
	args := []string{action, "codex", "--state-dir", state, "--codex", cli.Program()}
	if remove {
		args = append(args, "--registered-program", snapshot.Program)
	}
	raw, err := c.command(ctx, image.Program(), args...)
	if err != nil {
		return view, err
	}
	view, err = decodeCodexView(raw)
	if err != nil {
		return view, err
	}
	if err := errors.Join(image.Check(ctx), cli.Check(ctx)); err != nil {
		return view, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil || c.closed || revision != strconv.FormatUint(c.revision, 10) {
		return codexConnectionView{}, errors.New("earlier Codex setup outcome; check again")
	}
	view.Enrollment = enrollment
	return view, nil
}

func (p *servicePanel) codexConnectionRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/connections/codex", panelHTMLPage(key, "codex-connections", codexConnectionTitle+" connection", codexConnectionsPage))
	mux.HandleFunc("/connections/codex/state", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "GET required", 405)
			return
		}
		view, err := p.codexConnectionsForPanel().read(r.Context())
		if err != nil {
			view.Outcome = "unavailable"
			view.Guidance = codexUnavailableSentence(view.Reason, err)
		}
		noteAgentAttention("codex", view.Guidance)
		panelJSON(w, view)
	}))
	for _, action := range []string{"add", "remove"} {
		mux.HandleFunc("/connections/codex/"+action, guard(key, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				http.Error(w, "POST required", 405)
				return
			}
			var edit struct {
				Revision string `json:"revision"`
			}
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
			d.DisallowUnknownFields()
			if d.Decode(&edit) != nil || d.Decode(new(any)) != io.EOF {
				http.Error(w, "invalid setup request", 400)
				return
			}
			control := p.codexConnectionsForPanel()
			view, err := control.change(r.Context(), action == "remove", edit.Revision)
			if err == nil && action == "add" && view.Owned && view.Registration == "saved" {
				// The saved connection gets its start recipe.
				program := view.Program
				ensureStartRecipeLater(func(ctx context.Context) error { return control.ensureStartRecipe(ctx, program) })
			}
			if err != nil {
				var refused *codexEnrollmentFailure
				if errors.As(err, &refused) {
					panelJSON(w, view)
					return
				}
				if action == "add" {
					sentence := agentSetupBlocker(err, codexConnectionTitle)
					if sentence == "" {
						sentence = "OpenAbstractions couldn't connect Codex. Select Check again."
					}
					p.connectFailed("codex", codexConnectionTitle, sentence)
				}
				panelError(w, err)
				return
			}
			panelJSON(w, view)
		}))
	}
	mux.HandleFunc("/connections/codex/cancel", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "POST required", 405)
			return
		}
		p.codexConnectionsForPanel().cancelAction()
		panelJSON(w, map[string]string{"outcome": "cancelled"})
	}))
}

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

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type claudePanelImage interface {
	Program() string
	FileKey() string
	Check(context.Context) error
	Close() error
}

type claudePanelCLI interface {
	claudePanelImage
	Version() string
}

type claudeSavedConnection struct {
	Previous     *setup.AgentEnrollmentCapture `json:"previous,omitempty"`
	Project      string                        `json:"project"`
	ProjectKey   string                        `json:"project_key"`
	Program      string                        `json:"program"`
	ProgramKey   string                        `json:"program_key"`
	Name         string                        `json:"name"`
	Registration string                        `json:"registration"`
	Kind         string                        `json:"kind,omitempty"`
	HelperState  string                        `json:"helper_state,omitempty"`
	RestartHosts []string                      `json:"restart_hosts,omitempty"`
	Failure      string                        `json:"update_failure,omitempty"`
	ModState     string                        `json:"mod_state,omitempty"`
}

type claudePanelView struct {
	Supported             bool                    `json:"supported"`
	Outcome               string                  `json:"outcome"`
	Revision              string                  `json:"revision"`
	Connections           []claudeSavedConnection `json:"connections"`
	PickerAvailable       bool                    `json:"picker_available"`
	RegistrationAvailable bool                    `json:"registration_available"`
	ProjectRequired       bool                    `json:"project_required"`
	CLIAvailable          bool                    `json:"cli_available"`
	CLIVersion            string                  `json:"cli_version"`
	CLIReason             string                  `json:"cli_reason,omitempty"`
	CLIReasonCode         string                  `json:"cli_reason_code,omitempty"`
	OpenAvailable         bool                    `json:"open_available"`
	Background            claudeBackgroundState   `json:"background"`
	BackgroundAsks        claudeBackgroundState   `json:"background_asks"`
	BackgroundAvailable   bool                    `json:"background_available"`

	// Reason says in one plain sentence why the saved connections could not
	// be read; ReasonCode is its closed token.
	Reason     string `json:"reason,omitempty"`
	ReasonCode string `json:"reason_code,omitempty"`

	// ReconnectAvailable: the saved Code command could not be verified and
	// the running Claude Code verifies; Connect adopts the running one.
	ReconnectAvailable bool `json:"reconnect_available,omitempty"`
}

// This is one Panel adapter. The gateway remains the configuration/marker
// owner; native application enrollment and OA consent retain their owners.
type claudePanelConnections struct {
	mu                   sync.Mutex
	actionMu             sync.Mutex
	startRecipe          sync.Mutex // one start recipe registration at a time
	revision             uint64
	rows                 []claudeSavedConnection
	selection            uint64
	project              fileid.FileID
	activeCancel         context.CancelFunc
	window               context.Context
	closed               bool
	image                func(context.Context) (claudePanelImage, error)
	cli                  func(context.Context) (claudePanelCLI, error)
	open                 func(context.Context) error
	command              func(context.Context, string, ...string) ([]byte, error)
	state                func() (string, error)
	picker               func(context.Context) (string, error)
	prepare              func(context.Context) (claudePanelImage, error)
	enroll               func(context.Context, claudePanelImage) (string, error)
	declare              func(context.Context, claudePanelImage, string) error
	admit                func(context.Context, claudePanelImage) (claudePanelImage, error)
	heldImage            claudePanelImage // serialized by actionMu; retained for this Panel lifetime
	register             func(context.Context, fileid.FileID) (claudeConnectionSaved, error)
	projectRequired      bool
	update               agentHelperUpdater
	helperState          func(context.Context, string, string) agentHelperReading
	userMod              bool
	backgroundReview     func(context.Context, claudePanelCLI, string) (bool, error)
	asksBackgroundReview func(context.Context, claudePanelCLI, string) (bool, error)
	startupRead          func(context.Context) (fwire.ApplicationDescriptor, applications.AgentEnrollmentResult, error)
	startupSet           func(context.Context, applications.ActivationConfiguration) (fwire.ApplicationChange, error)
	startupImage         func(context.Context) (claudePanelImage, error)
	startupCLI           func(context.Context) (claudePanelImage, error)
	startupCapture       *claudeStartupCapture
	startupFinish        func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error)
	updateFrom           agentHelperUpdaterFrom
	retire               func(context.Context, setup.AgentEnrollmentCapture) error

	// liveCLI selects the running, authenticated Claude Code. Only the
	// person's Connect uses it in place of an unverifiable saved command.
	liveCLI func(context.Context) (claudePanelCLI, error)
}

func (p *servicePanel) claudeConnectionsForPanel() *claudePanelConnections {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.claudeConnections == nil {
		p.claudeConnections = &claudePanelConnections{
			image:   panelGatewayImage,
			command: runClaudePanelCommand,
			state: func() (string, error) {
				home, err := setup.CurrentHome()
				if err != nil {
					return "", err
				}
				if home.Isolated {
					return "", errors.New("connections require the installed OpenAbstractions")
				}
				return filepath.Join(home.Config, "claude-connections"), nil
			},
			picker: p.directoryPicker,
		}
		control := p.claudeConnections
		control.declare = control.declareCompanion
		control.cli = func(ctx context.Context) (claudePanelCLI, error) {
			return control.selectManagementCLI(ctx)
		}
		control.liveCLI = func(ctx context.Context) (claudePanelCLI, error) {
			return selectClaudePanelCLI(ctx, func(ctx context.Context, program string) ([]byte, error) {
				return control.command(ctx, program, "--version")
			})
		}
		control.open = setup.OpenClaudeCode
		control.prepare = func(ctx context.Context) (claudePanelImage, error) {
			if runtime.GOOS == "darwin" {
				control.mu.Lock()
				program := ""
				for _, row := range control.rows {
					if row.Kind == "user" && row.Registration == "saved" {
						if program != "" {
							control.mu.Unlock()
							return nil, errors.New("multiple user connection images; check ownership")
						}
						program = row.Program
					}
				}
				control.mu.Unlock()
				if program != "" {
					return setup.HoldEnrolledAgentImageFrom(ctx, panelMachine(), setup.AgentClaude, program)
				}
			}
			return prepareClaudePanelImage(ctx)
		}
		control.enroll = p.registerClaudeImage
		control.helperState = func(ctx context.Context, program, key string) agentHelperReading {
			return panelAgentHelperReading(ctx, setup.AgentClaude, program, key)
		}
		control.update = func(ctx context.Context, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
			return p.updateAgentHelper(ctx, setup.AgentClaude, verify)
		}
		control.updateFrom = func(ctx context.Context, program string, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
			return p.updateAgentHelperFrom(ctx, setup.AgentClaude, program, verify)
		}
		control.retire = func(ctx context.Context, capture setup.AgentEnrollmentCapture) error {
			return setup.RemoveAgentEnrollment(ctx, panelMachine(), setup.AgentClaude, capture)
		}
		control.admit = func(ctx context.Context, image claudePanelImage) (claudePanelImage, error) {
			return admitAgentImageForConnection(ctx, image, setup.AgentClaude)
		}
		control.register = control.saveProject
		control.projectRequired = false
		control.userMod = true
		control.backgroundReview = p.reviewClaudeBackground
		control.asksBackgroundReview = p.reviewClaudeAsksBackground
		configureClaudeStartup(control)
	}
	return p.claudeConnections
}

func (c *claudePanelConnections) cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selection++
	c.project = fileid.FileID{}
	c.revision++
	c.rows = nil
	if c.activeCancel != nil {
		c.activeCancel()
	}
}

func (c *claudePanelConnections) close() {
	c.mu.Lock()
	c.closed = true
	c.selection++
	c.project = fileid.FileID{}
	c.rows = nil
	if c.activeCancel != nil {
		c.activeCancel()
	}
	c.mu.Unlock()
	c.actionMu.Lock()
	defer c.actionMu.Unlock()
	if c.heldImage != nil {
		_ = c.heldImage.Close()
		c.heldImage = nil
	}
}

func (c *claudePanelConnections) read(ctx context.Context) (view claudePanelView, err error) {
	c.mu.Lock()
	c.revision++
	revision := c.revision
	c.rows = nil
	view = claudePanelView{Supported: runtime.GOOS == "windows", Outcome: "unavailable", Revision: strconv.FormatUint(revision, 10), Connections: []claudeSavedConnection{}, PickerAvailable: c.picker != nil, RegistrationAvailable: c.register != nil && runtime.GOOS == "windows", ProjectRequired: c.projectRequired}
	view.OpenAvailable = runtime.GOOS == "windows" && c.open != nil
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return view, errors.New("connection window is closed")
	}
	defer func() {
		if err != nil {
			c.mu.Lock()
			if c.revision == revision {
				c.rows = nil
			}
			c.mu.Unlock()
		}
	}()
	if c.cli != nil {
		cli, cliErr := c.cli(ctx)
		if cli != nil {
			if cliErr == nil {
				cliErr = cli.Check(ctx)
			}
			if cliErr == nil {
				view.CLIAvailable = true
				view.CLIVersion = cli.Version()
				view.CLIReason = ""
			}
			if closeErr := cli.Close(); closeErr != nil {
				view.CLIAvailable = false
				view.CLIVersion = ""
				view.CLIReasonCode = "cli_cleanup"
				view.CLIReason = "Code settings could not be checked. Check again."
				return view, closeErr
			}
		}
		if !view.CLIAvailable {
			view.CLIReasonCode, view.CLIReason = claudeCLISelectionFailure(cliErr)
			view.ReconnectAvailable = c.reconnectAvailable(ctx, cliErr)
		}
	}
	if c.state == nil || c.image == nil || c.command == nil {
		return view, errors.New("saved connection reader unavailable")
	}
	state, err := c.state()
	if err != nil {
		return view, err
	}
	image, err := c.image(ctx)
	if err != nil {
		return view, err
	}
	if image == nil {
		return view, fmt.Errorf("installed connection helper unavailable: %w", os.ErrNotExist)
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	if err = image.Check(ctx); err != nil {
		return view, err
	}
	raw, err := c.command(ctx, image.Program(), "connections", "claude", "--state-dir", state)
	if err != nil {
		return view, err
	}
	if err = image.Check(ctx); err != nil {
		return view, err
	}
	var page struct {
		Outcome     string                  `json:"outcome"`
		Connections []claudeSavedConnection `json:"connections"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 1<<20 || decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF || page.Outcome != "page" || page.Connections == nil || len(page.Connections) > 64 {
		return view, errors.New("saved connection inventory unavailable")
	}
	seen := make(map[string]bool)
	for _, row := range page.Connections {
		key := row.Kind + "\x00" + row.ProjectKey + "\x00" + row.ProgramKey + "\x00" + row.Name
		if (row.Kind != "" && row.Kind != "plugin" && row.Kind != "user") || row.ProjectKey == "" || !filepath.IsAbs(row.Project) || !filepath.IsAbs(row.Program) || row.Name == "" || len(row.Name) > 64 || seen[key] || (row.Registration != "saved" && row.Registration != "changed" && row.Registration != "absent") {
			return view, errors.New("saved connection inventory invalid")
		}
		seen[key] = true
	}
	currency := make(map[string]agentHelperReading)
	for i := range page.Connections {
		row := &page.Connections[i]
		row.HelperState = "unknown"
		if row.Registration == "saved" && c.helperState != nil {
			key := row.Program + "\x00" + row.ProgramKey
			reading, found := currency[key]
			if !found {
				reading = c.helperState(ctx, row.Program, row.ProgramKey)
				currency[key] = reading
			}
			row.HelperState, row.RestartHosts, row.Failure = reading.State, reading.Restart, reading.Failure
		}
		if c.userMod && row.Kind == "user" && row.Registration == "saved" {
			row.ModState = c.readUserModState(ctx, image, state, row.Program)
		}
	}
	if c.userMod {
		for _, row := range page.Connections {
			if row.Kind == "user" && row.Registration == "saved" {
				view.Background = c.readBackground(ctx)
				view.BackgroundAsks = c.readBackgroundScope(ctx, "asks")
				view.BackgroundAvailable = view.CLIAvailable
				break
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil || c.revision != revision {
		return view, errors.New("saved connection view changed")
	}
	c.rows = append([]claudeSavedConnection(nil), page.Connections...)
	view.Outcome = "page"
	view.Connections = page.Connections
	return view, nil
}

func (c *claudePanelConnections) chooseProject(ctx context.Context) (string, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", "", errors.New("connection window is closed")
	}
	if c.activeCancel != nil {
		c.activeCancel()
	}
	c.activeCancel = cancel
	c.selection++
	selection := c.selection
	c.project = fileid.FileID{}
	pick := c.picker
	c.mu.Unlock()
	if pick == nil {
		return "", "", errors.New("native project picker unavailable")
	}
	path, err := pick(ctx)
	if err != nil || ctx.Err() != nil {
		return "", "", errors.Join(err, ctx.Err())
	}
	if path == "" {
		return "", "", nil
	}
	project, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !project.IsDir() {
		return "", "", errors.New("selected project folder unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil || c.selection != selection {
		return "", "", errors.New("project selection changed")
	}
	c.project = project
	return project.Path(), strconv.FormatUint(selection, 10), nil
}

func (c *claudePanelConnections) connect(ctx context.Context, selection string) (claudeConnectionSaved, error) {
	unknown := claudeConnectionSaved{Enrollment: "unknown"}
	if !c.actionMu.TryLock() {
		return unknown, errors.New("connection setup is already in progress")
	}
	defer c.actionMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	register, project, revision, required := c.register, c.project, c.selection, c.projectRequired
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return unknown, errors.New("connection window is closed")
	}
	if register == nil {
		return unknown, errors.New("connection setup is unavailable in this build")
	}
	if required {
		if selection != strconv.FormatUint(revision, 10) || project.IsZero() {
			return unknown, errors.New("choose the project again")
		}
		current, err := fileid.Resolve(ctx, fileid.Self, project.Path(), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !current.Equal(project) {
			return unknown, errors.New("project folder changed; choose it again")
		}
	}
	if err := ctx.Err(); err != nil {
		return unknown, err
	}
	c.mu.Lock()
	if c.closed || (required && c.selection != revision) {
		c.mu.Unlock()
		return unknown, errors.New("project selection changed")
	}
	c.activeCancel = cancel
	c.mu.Unlock()
	result, err := register(ctx, project)
	if err != nil {
		return result, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil || (required && c.selection != revision) {
		return unknown, errors.New("connection outcome belongs to an earlier selection; refresh saved connections")
	}
	return result, nil
}

func (p *servicePanel) claudeConnectionRoutes(mux *http.ServeMux, key string) {
	p.installedAgentConnectionRoutes(mux, key)
	p.claudeBackgroundRoutes(mux, key)
	p.claudeStartupRoutes(mux, key)
	mux.HandleFunc("/connections/claude", panelHTMLPage(key, "claude-connections", claudeConnectionTitle+" connection", claudeConnectionsPage))
	mux.HandleFunc("/connections/claude/state", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET required", 405)
			return
		}
		connections := p.claudeConnectionsForPanel()
		view, err := connections.read(r.Context())
		if err != nil {
			view.Outcome = "unavailable"
			view.Connections = []claudeSavedConnection{}
			view.ReasonCode, view.Reason = claudeConnectionFailure(err)
		}
		noteAgentAttention("claude", view.Reason)
		panelJSON(w, view)
	}))
	mux.HandleFunc("/connections/claude/project", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		var edit struct{}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&edit) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid project selection request", 400)
			return
		}
		path, selection, err := p.claudeConnectionsForPanel().chooseProject(r.Context())
		if err != nil {
			panelError(w, err)
			return
		}
		outcome := "selected"
		if path == "" {
			outcome = "cancelled"
		}
		panelJSON(w, map[string]string{"outcome": outcome, "project": path, "selection": selection})
	}))
	mux.HandleFunc("/connections/claude/connect", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		var edit struct {
			Selection string `json:"selection"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&edit) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid connection request", 400)
			return
		}
		control := p.claudeConnectionsForPanel()
		result, err := control.connect(r.Context(), edit.Selection)
		if err != nil {
			// The page shows the first line when it is a plain sentence; the
			// whole error is its Technical details.
			sentence := panelConfigurationSentence(err)
			if sentence == "" {
				sentence = agentSetupBlocker(err, claudeConnectionTitle)
			}
			if sentence != "" {
				p.connectFailed("claude", claudeConnectionTitle, sentence)
				http.Error(w, sentence+"\n"+err.Error(), http.StatusServiceUnavailable)
				return
			}
			p.connectFailed("claude", claudeConnectionTitle, "OpenAbstractions couldn't connect Claude. Select Check again.")
			panelError(w, err)
			return
		}
		// The saved connection gets its start recipe.
		ensureStartRecipeLater(control.ensureStartRecipe)
		panelJSON(w, result)
	}))
	mux.HandleFunc("/connections/claude/cancel", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		p.claudeConnectionsForPanel().cancel()
		panelJSON(w, map[string]string{"outcome": "cancelled"})
	}))
	mux.HandleFunc("/connections/claude/disconnect", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		var edit claudeDisconnectRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&edit) != nil || decoder.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid saved connection request", 400)
			return
		}
		if err := p.claudeConnectionsForPanel().disconnect(r.Context(), edit); err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, map[string]string{"outcome": "removed"})
	}))
	mux.HandleFunc("/connections/claude/open", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST required", 405)
			return
		}
		control := p.claudeConnectionsForPanel()
		control.mu.Lock()
		open, closed := control.open, control.closed
		control.mu.Unlock()
		if closed {
			panelError(w, errors.New("connection window is closed"))
			return
		}
		if open == nil {
			panelError(w, errors.New("registered Claude activation unavailable"))
			return
		}
		if err := open(r.Context()); err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, map[string]string{"outcome": "requested"})
	}))
}

// claudeConnectionFailure is the closed code and plain sentence for a saved
// connection read that failed.
func claudeConnectionFailure(err error) (string, string) {
	if sentence := agentSetupBlocker(err, claudeConnectionTitle); sentence != "" {
		if agentHomeIsolated() {
			return "installed_runtime", sentence
		}
		return "helper_missing", sentence
	}
	var command *panelConfigurationError
	if sentence := panelConfigurationSentence(err); sentence != "" && errors.As(err, &command) {
		return command.pluginOperation + "_" + command.pluginReason, sentence
	}
	return "unavailable", "Connection setup is unavailable. Check again."
}

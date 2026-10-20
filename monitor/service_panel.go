package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// A panel process retains its selected binding. Failed calls never select a
// different owner; starting a new process requires matching saved recovery data.
type servicePanel struct {
	windowTitle string
	mu          sync.Mutex
	jobs        *client.JobsClient
	inventory   *client.InventoryClient
	history     api.HistoryWindow
	// appRequest is the registration or removal an application's installer
	// started this Panel run for (application_register.go).
	appRequest applicationRequest
	// appReadRules writes the Panel's read rule on what the person registered
	// (application_read.go); nil uses the runtime's rights operator.
	appReadRules applicationReadRules
	// permissionReview resolves personal feature proposals through the same
	// native account, inventory and Rights clients; tests provide inert owners.
	permissionReview          *permissionReviewSources
	permissionReviewMu        sync.Mutex
	permissionReviewActionMu  sync.Mutex
	permissionReviewNonce     string
	permissionReviewStarted   string
	permissionReviewDecisions map[permissionReviewContext]permissionReviewDecision
	// notifications reaches the notification and Rights services for the
	// Activity page's Notifications group; the zero value uses the runtime's.
	notifications     panelNotifications
	setupJournal      *setup.Journal
	setupActionMu     sync.Mutex
	modelController   setupModelController
	engineControllers map[setup.AppID]setupModelController
	// choiceControllers are the offered picture model plans' controllers,
	// keyed by plan ID; built on first use, set by tests.
	choiceControllers map[string]setupModelController
	modelProgressMu   sync.Mutex
	modelProgress     map[string]modelProgress
	setupFeatureCheck func() (setup.FeatureStatus, error)
	modelControllerMu sync.Mutex
	comfyPathsMu      sync.Mutex
	comfyPaths        *setup.ComfyPaths
	// comfyActivationOpen is a test seam for exercising explicit launch
	// revalidation. Product handlers always use setup.OpenInstalledComfy.
	comfyActivationOpen       func(context.Context, setup.Journal) (setup.ComfyPaths, error)
	comfyAppsMu               sync.Mutex
	comfyApps                 setupApplications
	comfyRegisteredDescriptor *fwire.ApplicationDescriptor
	comfyActivationStarted    bool
	// ModelHost setup seams keep the guided local-model flow on the same
	// public commands and inventory client used by the installed Panel.
	modelHostCLI              func(context.Context, string, ...string) ([]byte, error)
	modelHostOA               func(context.Context, ...string) ([]byte, error)
	modelHostProgramPaths     func() (modelHostPrograms, error)
	modelHostInventory        modelHostInventoryReader
	modelHostInventoryResolve func(context.Context, client.Requirements) (modelHostInventoryReader, error)
	modelHostFeatureCheck     func() (setup.FeatureStatus, error)
	// log is the Panel's own logging.
	log panelLog
}

func (p *servicePanel) binding(ctx context.Context) (*client.JobsClient, *client.InventoryClient, api.HistoryWindow, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobs != nil {
		return p.jobs, p.inventory, p.history, nil
	}
	inventory, err := panelMachine().ResolveJobInventory(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, nil, api.HistoryWindow{}, err
	}
	jobs, err := panelMachine().ResolveJobs(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, nil, api.HistoryWindow{}, err
	}
	history, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		return nil, nil, api.HistoryWindow{}, err
	}
	p.jobs, p.inventory, p.history = jobs, inventory, history
	return jobs, inventory, history, nil
}
func panelCall(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}
func panelJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func panelError(w http.ResponseWriter, err error) {
	http.Error(w, err.Error(), http.StatusServiceUnavailable)
}
func (p *servicePanel) serveBinding(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	jobs, _, h, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, struct {
		Endpoint string            `json:"endpoint"`
		History  api.HistoryWindow `json:"history"`
	}{jobs.Endpoint(), h})
}
func (p *servicePanel) serveInventory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	_, inventory, _, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := inventory.ListWork(ctx, r.URL.Query().Get("cursor"), 32)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, page)
}

type serviceAction struct {
	Action   string              `json:"action"`
	Endpoint string              `json:"endpoint"`
	Owner    string              `json:"owner"`
	Identity api.RequestIdentity `json:"identity"`
	URL      string              `json:"url"`
	Digest   string              `json:"digest"`
	Size     int64               `json:"size"`
}

func (p *servicePanel) act(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	var action serviceAction
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&action); err != nil {
		http.Error(w, "invalid action", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one action required", 400)
		return
	}
	if !slices.Contains([]string{"submit", "reconcile", "cancel", "observe"}, action.Action) {
		http.Error(w, "This action is unavailable: the panel offers submit, reconcile, cancel and observe.", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	jobs, _, h, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if action.Endpoint != jobs.Endpoint() || action.Owner != h.LogicalOwner || action.Identity.HistoryEpoch != h.HistoryEpoch {
		http.Error(w, "saved operation belongs to another binding; do not resubmit to a new owner", 409)
		return
	}
	if action.Action != "observe" {
		p.logAction(ctx, action.Action, map[string]string{"request.key": action.Identity.Key})
	}
	var result any
	switch action.Action {
	case "submit":
		spec := request.Encode(&request.Request{Artifact: request.Artifact{Digest: action.Digest, Size: action.Size}, Sources: []request.Source{{Scheme: "http", Locator: action.URL}}})
		result, err = jobs.Submit(ctx, api.Submission{Identity: action.Identity, Kind: "download", Spec: spec})
	case "reconcile":
		result, err = jobs.Reconcile(ctx, action.Identity)
	case "cancel":
		result, err = jobs.CancelWork(ctx, action.Identity)
	case "observe":
		result, err = jobs.ObserveWork(ctx, action.Identity)
	}
	if err != nil {
		panelError(w, fmt.Errorf("operation outcome may be unresolved; retain this key and use Reconcile: %w", err))
		return
	}
	panelJSON(w, result)
}
func (p *servicePanel) result(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	jobs, _, h, err := p.binding(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	q := r.URL.Query()
	id := api.RequestIdentity{Key: q.Get("key"), HistoryEpoch: q.Get("epoch")}
	if q.Get("owner") != h.LogicalOwner || q.Get("endpoint") != jobs.Endpoint() || id.HistoryEpoch != h.HistoryEpoch {
		http.Error(w, "saved operation binding differs", 409)
		return
	}
	first, err := jobs.ReadResult(ctx, id, 0, 65536)
	if err != nil {
		panelError(w, err)
		return
	}
	if first.Outcome != api.ResultOutcomeData || first.Chunk == nil {
		http.Error(w, "result: "+first.Outcome.String(), 409)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="operation-result.bin"`)
	w.Header().Set("Content-Length", strconv.FormatInt(first.Chunk.Total, 10))
	if _, err = jobs.CopyResult(ctx, id, w); err != nil {
		panic(http.ErrAbortHandler)
	}
}
func (p *servicePanel) settings(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	editor, err := panelMachine().ResolveConfigEditor(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if r.Method == "GET" {
		value, err := editor.ReadUserContext(ctx)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, value)
		return
	}
	var value configwire.UserSnapshot
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		http.Error(w, "invalid settings", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one settings replacement required", 400)
		return
	}
	p.logAction(ctx, "settings.replace", map[string]string{"settings.revision": value.Revision})
	result, err := editor.ReplaceUserContext(ctx, value.Revision, value.Values)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}

// panelHTMLPage serves one page's document at its own path, keyed the way /
// is: the per-run key in the query string, not the header guard() expects
// from that page's own fetch calls.
func panelHTMLPage(key, active, title, fragment string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		//unchecked: a failed write means the client is already gone; there is no response left to recover
		_, _ = io.WriteString(w, panelPage(r, key, active, title, fragment))
	}
}

// pageOrAPI serves one path two ways, for the five paths that already carried
// a guarded JSON call before this page existed (rights, settings, logging,
// identity, explore) and now also carry a page of their own at that same
// path (task 2026-09-23, requirement 1: "the handlers stay"). Every one of
// those calls, including this very page's own fetch() calls, sends the
// X-Panel-Key header (guard's own requirement); a plain browser navigation —
// the nav pane's <a>, a reload, an address typed in — never does. That
// header's presence is what tells the two apart.
func pageOrAPI(key, active, title, fragment string, api http.HandlerFunc) http.HandlerFunc {
	page := panelHTMLPage(key, active, title, fragment)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Panel-Key") != "" {
			guard(key, api)(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		page(w, r)
	}
}

// handler is the HTTP mode's handler: the routes behind the loopback
// boundary.
func (p *servicePanel) handler(key string) http.Handler {
	return serviceBrowserBoundary(p.routes(key))
}

// routes is every Panel route, shared by the HTTP mode (handler) and the
// native windows' resource mode (panel_resource.go). Neither boundary is
// part of it.
func (p *servicePanel) routes(key string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		panelHTMLPage(key, "status", "OpenAbstractions apps", servicePage)(w, r)
	})
	mux.HandleFunc("/static/", servePanelStatic)
	mux.HandleFunc("/binding", guard(key, p.serveBinding))
	mux.HandleFunc("/inventory", guard(key, p.serveInventory))
	mux.HandleFunc("/action", guard(key, p.act))
	mux.HandleFunc("/account-work", guard(key, p.accountWork))
	mux.HandleFunc("/notifications", guard(key, p.notificationsHandler))
	mux.HandleFunc("/notifications/act", guard(key, p.notificationsAct))
	mux.HandleFunc("/result", guard(key, p.result))
	mux.HandleFunc("/work", panelHTMLPage(key, "work", "OpenAbstractions activity", workPage))
	mux.HandleFunc("/setup", panelHTMLPage(key, "setup", "OpenAbstractions Setup", setupPage))
	for alias, target := range navAliases {
		mux.HandleFunc(alias, redirectAlias(target))
	}
	mux.HandleFunc("/setup/plan", guard(key, p.setupPlan))
	mux.HandleFunc("/setup/other-copies", guard(key, p.setupOtherCopies))
	mux.HandleFunc("/setup/install", guard(key, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			p.setupInstallState(w, r)
			return
		}
		p.setupInstall(w, r)
	}))
	mux.HandleFunc("/setup/install/grants", guard(key, p.setupInstallGrants))
	mux.HandleFunc("/setup/install/open", guard(key, p.setupInstallOpen))
	mux.HandleFunc("/setup/intent", guard(key, p.setupIntent))
	mux.HandleFunc("/setup/state", guard(key, p.setupState))
	mux.HandleFunc("/setup/advance", guard(key, p.setupAdvance))
	mux.HandleFunc("/setup/retry", guard(key, p.setupRetry))
	mux.HandleFunc("/setup/recover", guard(key, p.setupRecover))
	mux.HandleFunc("/setup/feature", guard(key, p.setupFeatureStatus))
	mux.HandleFunc("/setup/feature/add", guard(key, p.setupAddFeature))
	mux.HandleFunc("/setup/model", guard(key, p.setupModelStatus))
	mux.HandleFunc("/setup/model/start", guard(key, func(w http.ResponseWriter, r *http.Request) { p.setupModelAction(w, r, "start") }))
	mux.HandleFunc("/setup/model/advance", guard(key, func(w http.ResponseWriter, r *http.Request) { p.setupModelAction(w, r, "advance") }))
	mux.HandleFunc("/setup/model/retry", guard(key, func(w http.ResponseWriter, r *http.Request) { p.setupModelAction(w, r, "retry") }))
	mux.HandleFunc("/setup/model/configure", guard(key, func(w http.ResponseWriter, r *http.Request) { p.setupModelAction(w, r, "configure") }))
	mux.HandleFunc("/setup/model/reconcile", guard(key, func(w http.ResponseWriter, r *http.Request) { p.setupModelAction(w, r, "reconcile") }))
	mux.HandleFunc("/setup/engine/remove", guard(key, p.setupRemoveEngine))
	mux.HandleFunc("/setup/comfy/status", guard(key, p.setupComfyStatus))
	mux.HandleFunc("/setup/comfy/register", guard(key, p.setupComfyRegister))
	mux.HandleFunc("/setup/comfy/activate", guard(key, p.setupComfyActivate))
	mux.HandleFunc("/setup/comfy/remove", guard(key, p.setupComfyRemove))
	mux.HandleFunc("/setup/modelhost/chat", guard(key, p.setupModelHostChat))
	mux.HandleFunc("/setup/modelhost/feature/add", guard(key, p.setupModelHostFeatureAdd))
	mux.HandleFunc("/setup/modelhost/engine", guard(key, p.setupModelHostEngine))
	mux.HandleFunc("/setup/modelhost/engine/bundled", guard(key, p.setupModelHostBundledEngine))
	mux.HandleFunc("/setup/modelhost/root/preview", guard(key, p.setupModelHostRootPreview))
	mux.HandleFunc("/setup/modelhost/root", guard(key, p.setupModelHostRootRegister))
	mux.HandleFunc("/setup/modelhost/host", guard(key, p.setupModelHostHost))
	mux.HandleFunc("/settings", pageOrAPI(key, "settings", "OpenAbstractions settings", settingsPage, p.settings))
	mux.HandleFunc("/settings/observe", guard(key, p.observeSettings))
	mux.HandleFunc("/questions", apiOrPage(key, p.questions, "/rights"))
	mux.HandleFunc("/choices", guard(key, p.choices))
	mux.HandleFunc("/rights/requests", guard(key, p.permissions))
	mux.HandleFunc("/rights", pageOrAPI(key, "rights", "OpenAbstractions requests and permissions", rightsPage, p.rights))
	p.permissionReviewRoutes(mux, key)
	mux.HandleFunc("/rights/observe", guard(key, p.observeRights))
	mux.HandleFunc("/rights/defaults", guard(key, p.rightsDefaults))
	mux.HandleFunc("/rights/delegation", guard(key, p.questionDelegation))
	mux.HandleFunc("/rights/assistant", guard(key, p.assistant))
	mux.HandleFunc("/rights/answered", guard(key, p.answered))
	p.activityRoutes(mux, key)
	mux.HandleFunc("/credentials-page", p.credentialsPageHandler(key))
	mux.HandleFunc("/credentials", guard(key, p.credentials))
	mux.HandleFunc("/runtime", guard(key, serveReadiness))
	p.applicationRoutes(mux, key)
	mux.HandleFunc("/logging", apiOrPage(key, p.logging, "/settings"))
	mux.HandleFunc("/identity", apiOrPage(key, p.identity, "/settings"))
	mux.HandleFunc("/explore", apiOrPage(key, p.explore, "/settings"))
	p.inferenceRoutes(mux, key)
	p.registryRoutes(mux, key)
	mux.HandleFunc("/apps/registrations", guard(key, p.appsRegistrations))
	p.appPageRoutes(mux, key)
	p.cardRoutes(mux, key)
	p.navRoutes(mux, key)
	return mux
}

// bindPanel starts listening on address and builds this run's key and URL.
// Every front end this program offers — the browser page, the native
// inventory window, the macOS notification launcher's window — shares this HTTP
// server and per-run bearer key.
func bindPanel(address string) (p *servicePanel, server *http.Server, listener net.Listener, url string, err error) {
	return bindPanelPage(address, "overview")
}

type panelStartPage struct {
	path        string
	windowTitle string
}

// panelAccount is the runtime's observation of this Panel's native IPC
// caller's account, or "" when the runtime did not name it. The browser
// cannot select another account.
func panelAccount(ctx context.Context) string {
	observed, err := panelMachine().ObserveCaller(ctx)
	if err != nil || observed.Outcome != fwire.CallerOutcomeObserved || observed.Account == "" {
		return ""
	}
	return observed.Account
}

// parsePanelStartPage deliberately accepts a small set of semantic page names.
// Callers cannot choose an arbitrary path or URL for this keyed local server.
func parsePanelStartPage(name string) (panelStartPage, error) {
	switch name {
	case "overview":
		// /start opens Get started while setup is unfinished, then Apps
		// (panel_nav.go, landingPath).
		return panelStartPage{path: "/start", windowTitle: "OpenAbstractions Panel"}, nil
	case "questions":
		return panelStartPage{path: "/rights", windowTitle: "OpenAbstractions Panel"}, nil
	case "permission-review":
		return panelStartPage{path: "/permission-review", windowTitle: "OpenAbstractions Panel"}, nil
	case "credentials":
		return panelStartPage{path: "/credentials-page", windowTitle: "OpenAbstractions Panel"}, nil
	case "connect-deepseek":
		// A launcher asking for the retired hosted-key page gets Credentials,
		// where keys are added.
		return panelStartPage{path: "/credentials-page", windowTitle: "OpenAbstractions Panel"}, nil
	case "setup":
		return panelStartPage{path: "/setup", windowTitle: "OpenAbstractions Setup"}, nil
	case "activity":
		return panelStartPage{path: "/work", windowTitle: "OpenAbstractions Panel"}, nil
	case "register-application":
		return panelStartPage{path: "/application/register", windowTitle: "OpenAbstractions Panel"}, nil
	case "remove-application":
		return panelStartPage{path: "/application/remove", windowTitle: "OpenAbstractions Panel"}, nil
	default:
		return panelStartPage{}, fmt.Errorf("unknown Panel page %q; choose overview, questions, credentials, setup or activity", name)
	}
}

func keyedPanelURL(address, path, key string) string {
	u := neturl.URL{Scheme: "http", Host: address, Path: path}
	query := u.Query()
	query.Set("k", key)
	u.RawQuery = query.Encode()
	return u.String()
}

// announcePanelURL prints this run's keyed URL unless quiet. A run an
// application's installer started is always quiet: the installer owns this
// process's stdout, and the key in the URL would let it confirm its own
// registration without the person's click.
func announcePanelURL(out io.Writer, panelURL string, quiet bool, request applicationStart) error {
	return writePanelURL(out, panelURL, quiet || request != (applicationStart{}))
}

func writePanelURL(out io.Writer, panelURL string, quiet bool) error {
	if quiet {
		return nil
	}
	_, err := fmt.Fprintln(out, "service control panel:", panelURL)
	return err
}

// panelRun is one Panel run before it is served: the panel, this run's key
// and its start page. The HTTP mode binds it to a loopback listener
// (bindPanelPage); a native window serves it as resources (resources).
type panelRun struct {
	panel *servicePanel
	key   string
	start panelStartPage
}

func newPanelRun(pageName string) (*panelRun, error) {
	startPage, err := parsePanelStartPage(pageName)
	if err != nil {
		return nil, err
	}
	return &panelRun{panel: &servicePanel{windowTitle: startPage.windowTitle}, key: mint(), start: startPage}, nil
}

// resources serves this run in process under origin, and the keyed start
// URL a native window opens. Nothing listens.
func (run *panelRun) resources(origin string) (*panelResources, string) {
	u, err := neturl.Parse(origin)
	if err != nil {
		u = &neturl.URL{}
	}
	u.Path = run.start.path
	query := neturl.Values{}
	query.Set("k", run.key)
	u.RawQuery = query.Encode()
	return newPanelResources(origin, run.panel.routes(run.key)), withStartOperation(u.String(), run.start)
}

// panelListen is net.Listen. Tests replace it to prove that a native window's
// resource mode binds no listener.
var panelListen = net.Listen

func bindPanelPage(address, pageName string) (p *servicePanel, server *http.Server, listener net.Listener, url string, err error) {
	run, err := newPanelRun(pageName)
	if err != nil {
		return nil, nil, nil, "", err
	}
	hostname, _, err := net.SplitHostPort(address)
	if err != nil || !loopback(hostname) {
		return nil, nil, nil, "", errors.New("service panel requires a loopback address")
	}
	listener, err = panelListen("tcp", address)
	if err != nil {
		return nil, nil, nil, "", err
	}
	server = &http.Server{Handler: run.panel.handler(run.key), ReadHeaderTimeout: 5 * time.Second}
	url = withStartOperation(keyedPanelURL(listener.Addr().String(), run.start.path, run.key), run.start)
	return run.panel, server, listener, url, nil
}

// startOperation is the operation the Activity page opens at, from
// -operation; a notification's Open Activity action sets it (ASK-N1).
var startOperation string

// withStartOperation adds that operation to an Activity start URL.
func withStartOperation(url string, start panelStartPage) string {
	if start.path != "/work" || startOperation == "" {
		return url
	}
	return url + "&operation=" + neturl.QueryEscape(startOperation)
}

// panelWindowAnnouncement is the readiness line's value for a native window:
// it opened, and there is no URL to visit.
const panelWindowAnnouncement = "window"

func runServicePanel(address string, open, native bool, pageName string, quiet bool, request applicationStart) error {
	if native {
		// The native window is served as resources and binds nothing
		// (panel_resource.go). The loopback HTTP below is the developer and
		// automation mode, -native=false.
		run, err := newPanelRun(pageName)
		if err != nil {
			return err
		}
		run.panel.appRequest.register, run.panel.appRequest.remove = request.register, request.remove
		return runResourceWindow(run, quiet)
	}
	p, server, listener, panelURL, err := bindPanelPage(address, pageName)
	if err != nil {
		return err
	}
	p.appRequest.register, p.appRequest.remove = request.register, request.remove
	if err := announcePanelURL(os.Stdout, panelURL, quiet, request); err != nil {
		server.Close()
		return err
	}
	defer server.Close()
	if open {
		launch(panelURL)
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// The browser UI accepts its loopback host and same-origin requests only.
// The existing per-run bearer key remains required at each capability handler.
func serviceBrowserBoundary(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostname, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			hostname = r.Host
		}
		if hostname != "localhost" && !loopback(hostname) {
			http.Error(w, "loopback Host required", 403)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			parsed, err := neturl.Parse(origin)
			if err != nil || parsed.Scheme != "http" || !strings.EqualFold(parsed.Host, r.Host) || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				http.Error(w, "same Origin required", 403)
				return
			}
		}
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

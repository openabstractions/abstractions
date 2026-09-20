package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	core "github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	loggingwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

// platformDeclaration is docs/platforms.json, copied beside the Panel so the
// published Panel carries the declaration it was built with.
// TestPanelPlatformDeclarationIsTheRepositoryCopy keeps the copy exact.
//
//go:embed platforms.json
var platformDeclaration []byte

// selectInstalled is the SDK's installed-runtime selection; tests replace it.
var selectInstalled = core.SelectInstalled

type selectionView struct {
	Status   string `json:"status"`
	Endpoint string `json:"endpoint,omitempty"`
	Account  string `json:"account,omitempty"`
	Program  string `json:"program,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type declarationView struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Limits []string `json:"limits"`
}

type ceilingView struct {
	Platform  string            `json:"platform"`
	Transport string            `json:"transport"`
	Best      map[string]string `json:"best"`
	Why       map[string]string `json:"why"`
	Bindable  bool              `json:"bindable"`
	Binding   string            `json:"binding"`
	Stronger  string            `json:"stronger,omitempty"`
}

type callerAttributeView struct {
	Attribute string `json:"attribute"`
	Proof     string `json:"proof"`
	Ceiling   string `json:"ceiling"`
}

// callerView is the runtime's abstraction.facade/caller@1 answer, or why there
// is none.
type callerView struct {
	Absent     bool                  `json:"absent"`
	Error      string                `json:"error,omitempty"`
	Outcome    string                `json:"outcome,omitempty"`
	Mechanism  string                `json:"mechanism,omitempty"`
	Account    string                `json:"account,omitempty"`
	Program    string                `json:"program,omitempty"`
	PID        int64                 `json:"pid"`
	Attributes []callerAttributeView `json:"attributes"`
	Platform   string                `json:"platform,omitempty"`
	Transport  string                `json:"transport,omitempty"`
	Bindable   bool                  `json:"bindable"`
	Stronger   string                `json:"stronger,omitempty"`
}

func presentCaller(o fwire.CallerObservation) callerView {
	view := callerView{Outcome: o.Outcome.String(), Mechanism: o.Mechanism, Account: o.Account, Program: o.Program, PID: o.PID,
		Attributes: []callerAttributeView{}, Platform: o.Platform, Transport: o.Transport, Bindable: o.Bindable, Stronger: o.Stronger}
	for _, a := range o.Attributes {
		view.Attributes = append(view.Attributes, callerAttributeView{Attribute: a.Attribute, Proof: a.Proof, Ceiling: a.Ceiling})
	}
	return view
}

type capabilityView struct {
	Name     string `json:"name"`
	Contract string `json:"contract"`
	Status   string `json:"status"`
	Label    string `json:"label"`
}

type loggingIdentityView struct {
	Absent  bool           `json:"absent"`
	Error   string         `json:"error,omitempty"`
	Outcome string         `json:"outcome"`
	Record  *logRecordView `json:"record,omitempty"`
	Stamp   *hopView       `json:"stamp,omitempty"`
	Claim   *hopView       `json:"claim,omitempty"`
	Agrees  *bool          `json:"agreesWithRuntime,omitempty"`
}

type identityView struct {
	Selection       selectionView       `json:"selection"`
	Explicit        *explicitRuntime    `json:"explicit,omitempty"`
	Platform        string              `json:"platform"`
	Declarations    []declarationView   `json:"declarations"`
	Ceiling         ceilingView         `json:"ceiling"`
	Runtime         callerView          `json:"runtime"`
	Capabilities    []capabilityView    `json:"capabilities"`
	CapabilityError string              `json:"capabilityError,omitempty"`
	Logging         loggingIdentityView `json:"logging"`
	Limits          []string            `json:"limits"`
	CheckedAt       string              `json:"checkedAt"`
}

// selectionStatus names a selection result with the native C ABI's words.
func selectionStatus(selected core.Selection, err error) selectionView {
	switch {
	case err == nil:
		account := selected.Server.Principal.SID
		if selected.Server.Principal.Kind != "windows" {
			account = itoa(selected.Server.Principal.UID)
		}
		return selectionView{Status: "TRUSTED", Endpoint: selected.Endpoint, Account: account, Program: selected.Server.Program}
	case errors.Is(err, context.DeadlineExceeded):
		return selectionView{Status: "TIMEOUT", Detail: err.Error()}
	case errors.Is(err, context.Canceled):
		return selectionView{Status: "CANCELLED", Detail: err.Error()}
	case errors.Is(err, core.ErrUnsupportedSelection):
		return selectionView{Status: "PROOF_UNAVAILABLE", Detail: err.Error()}
	}
	return selectionView{Status: "UNTRUSTED", Detail: err.Error()}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// declarationsFor returns the platform declaration entries for goos.
func declarationsFor(goos string) ([]declarationView, error) {
	var document struct {
		Platforms []struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Status string   `json:"status"`
			Limits []string `json:"limits"`
		} `json:"platforms"`
	}
	if err := json.Unmarshal(platformDeclaration, &document); err != nil {
		return nil, err
	}
	prefix := map[string]string{"windows": "windows-", "linux": "linux-", "darwin": "macos", "android": "android"}[goos]
	views := []declarationView{}
	for _, p := range document.Platforms {
		if prefix != "" && strings.HasPrefix(p.ID, prefix) {
			views = append(views, declarationView{ID: p.ID, Name: p.Name, Status: p.Status, Limits: p.Limits})
		}
	}
	return views, nil
}

func ceilingOf(limits identity.Limits) ceilingView {
	best := map[string]string{"user": limits.Best.User.String(), "process": limits.Best.Process.String(), "path": limits.Best.Path.String(),
		"package": limits.Best.Package.String(), "code": limits.Best.Code.String()}
	return ceilingView{Platform: limits.Platform, Transport: limits.Transport, Best: best, Why: limits.Why,
		Bindable: limits.Bindable, Binding: limits.Binding, Stronger: limits.Stronger}
}

// panelContracts are the services the Panel calls, each observed as a
// resolution for this caller.
var panelContracts = []struct{ name, contract string }{
	{"Logging", "abstraction.logging/sink@1"}, {"Logging history", "abstraction.logging/reader@1"}, {"Logging observation", "abstraction.logging/observer@1"},
	{"Configuration changes", "abstraction.config/editor@1"}, {"Work submission", "abstraction.job/acceptance@1"},
	{"Work status and results", "abstraction.job/operations@1"}, {"Accepted work inventory", "abstraction.job/inventory@1"},
	{"Questions", "abstraction.asks/operator@1"}, {"Rights decisions", "abstraction.rights/authorization@1"}, {"Rights administration", "abstraction.rights/operator@1"},
}

func isAbsent(err error) bool {
	var resolution *client.ResolutionError
	return errors.As(err, &resolution) && resolution.Status == client.RuntimeUnavailable
}

// identity reports how this machine's runtime was selected, how the runtime
// and its logging service bound this Panel, and what the platform can prove.
// Every runtime fact comes from a resolved service: the caller echo
// (abstraction.facade/caller@1), per-contract resolution for this caller, and
// the logging service's stamp on a record the Panel wrote.
func (p *servicePanel) identity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	view := identityView{Platform: runtime.GOOS, Explicit: panelExplicit, Capabilities: []capabilityView{}, Limits: []string{}}

	step, stop := context.WithTimeout(ctx, 2*time.Second)
	view.Selection = selectionStatus(selectInstalled(step))
	stop()

	declarations, err := declarationsFor(runtime.GOOS)
	if err != nil {
		panelError(w, err)
		return
	}
	view.Declarations = declarations
	limits := identity.Ceiling()
	view.Ceiling = ceilingOf(limits)

	step, stop = context.WithTimeout(ctx, 2*time.Second)
	observed, err := panelMachine().ObserveCaller(step)
	stop()
	if err != nil {
		view.Runtime = callerView{Absent: isAbsent(err), Error: err.Error(), PID: -1, Attributes: []callerAttributeView{}}
	} else {
		view.Runtime = presentCaller(observed)
	}

	requests := make([]fwire.ResolveRequest, len(panelContracts))
	for i, item := range panelContracts {
		capability, _, _ := strings.Cut(item.contract, "/")
		requests[i] = fwire.ResolveRequest{Capability: capability, Contracts: []string{item.contract}, Guarantees: []string{}, Scope: fwire.ScopeLocal}
	}
	step, stop = context.WithTimeout(ctx, 3*time.Second)
	observation, err := panelMachine().Observe(step, requests)
	stop()
	if err != nil {
		view.CapabilityError = err.Error()
	}
	for i, item := range observation.Capabilities {
		row := capabilityView{Name: panelContracts[i].name, Contract: panelContracts[i].contract, Status: "unobserved"}
		if item.Result != nil {
			row.Status = item.Result.Status.String()
		}
		row.Label = readinessLabel(row.Status)
		view.Capabilities = append(view.Capabilities, row)
	}

	view.Logging = p.loggingIdentity(ctx, view.Runtime)
	view.Limits = identityLimits(runtime.GOOS, declarations, limits)
	view.CheckedAt = time.Now().Format(time.RFC3339)
	w.Header().Set("Cache-Control", "no-store")
	panelJSON(w, view)
}

// identityLimits states what the platform's identity cannot do, from the
// platform declaration and the identity ceiling.
func identityLimits(goos string, declarations []declarationView, limits identity.Limits) []string {
	statements := []string{}
	if goos == "darwin" {
		statements = append(statements, "On macOS, protected calls are refused: a socket peer cannot reach the Program proof (user kernel, process kernel, path bound) every runtime service requires, so readiness, resolution and history are refused on this platform.")
	}
	if platform := core.UnsupportedPlatform(goos); platform != "" {
		statements = append(statements, "The platform declaration lists "+platform+" as unsupported: no application can use a runtime service here.")
	}
	for _, d := range declarations {
		for _, limit := range d.Limits {
			statements = append(statements, d.Name+": "+limit)
		}
	}
	if !limits.Bindable {
		statements = append(statements, "No binding pins the caller's process on this machine: "+limits.Binding)
	}
	if limits.Stronger != "" {
		statements = append(statements, "A stronger transport exists: "+limits.Stronger)
	}
	statements = append(statements, "A program rule is only as strong as the path proof: two programs running as one account are not isolated from each other, so a per-program grant describes the caller and does not contain it.")
	return statements
}

// loggingIdentity writes one marked record through the Panel's own sink and
// reads it back through history, so the logging service's stamp shows how that
// service bound the Panel. It starts at the current history end (the reader@1
// end cursor), so retained history costs the check nothing.
func (p *servicePanel) loggingIdentity(ctx context.Context, runtimeView callerView) loggingIdentityView {
	failed := func(err error) loggingIdentityView {
		return loggingIdentityView{Absent: isAbsent(err), Error: failureText(err), Outcome: "error"}
	}
	reader, err := panelMachine().ResolveLogReader(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return failed(err)
	}
	end, err := reader.ReadContext(ctx, logging.EndCursor, 1, logPageBytes)
	if err != nil {
		return failed(err)
	}
	if end.Outcome != loggingwire.PageOutcomePage {
		return loggingIdentityView{Outcome: end.Outcome.String()}
	}
	cursor := end.Next
	nonce := mint()[:16]
	if err := p.log.record(ctx, logging.LevelInfo, "panel identity check", map[string]string{"panel.probe": nonce}); err != nil {
		return failed(err)
	}
	observer, err := panelMachine().ResolveLogObserver(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return failed(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		page, err := observer.ObserveContext(ctx, cursor, logPageRecords, logPageBytes, 1000)
		if err != nil {
			return failed(err)
		}
		if page.Outcome != loggingwire.PageOutcomePage {
			return loggingIdentityView{Outcome: page.Outcome.String()}
		}
		cursor = page.Next
		for _, record := range page.Records {
			if record.Attrs["panel.probe"] != nonce {
				continue
			}
			presented := presentRecord(record)
			result := loggingIdentityView{Outcome: "found", Record: &presented}
			for i := range presented.Hops {
				hop := presented.Hops[i]
				if hop.Hop == 0 && hop.By == logging.BySelf {
					result.Claim = &hop
				}
				if hop.Hop == 1 {
					result.Stamp = &hop
				}
			}
			if result.Stamp != nil && runtimeView.Outcome == fwire.CallerOutcomeObserved.String() {
				agrees := strings.EqualFold(result.Stamp.Exe, runtimeView.Program) && int64(result.Stamp.PID) == runtimeView.PID &&
					(result.Stamp.User == runtimeView.Account || itoa(result.Stamp.UID) == runtimeView.Account)
				result.Agrees = &agrees
			}
			return result
		}
	}
	state := p.log.state()
	return loggingIdentityView{Outcome: "not_found", Error: "the Panel's record did not reach history within four seconds; its sink is " + state.State}
}

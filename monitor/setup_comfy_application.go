package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type setupApplications interface {
	Register(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error)
	Remove(context.Context, string) (fwire.ApplicationChange, error)
	Observe(context.Context, string, int64) (fwire.ApplicationPage, error)
	Activate(context.Context, string) (fwire.ApplicationActivationResult, error)
}

type comfyApplicationReply struct {
	Available  bool   `json:"available"`
	Registered bool   `json:"registered"`
	Readable   bool   `json:"readable"`
	Matching   bool   `json:"matching"`
	Ready      bool   `json:"ready"`
	WebReady   bool   `json:"web_ready"`
	WebURL     string `json:"web_url,omitempty"`
	Starting   bool   `json:"starting"`
	Reason     string `json:"reason,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	Started    bool   `json:"started,omitempty"`
	// Conflict marks a registered entry that differs from this build's recipe;
	// the page offers Remove beside Reason and keeps Command in its details.
	Conflict bool `json:"conflict,omitempty"`
	// Foreign marks a conflicting entry registered by another program; Name
	// is the entry's title or its program's file name.
	Foreign bool   `json:"foreign,omitempty"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command,omitempty"`
	// Message is the one sentence a completed Remove writes.
	Message string `json:"message,omitempty"`
}

type comfyApplicationRequest struct {
	SetupPlanID string `json:"setup_plan_id"`
	Confirm     bool   `json:"confirm"`
}

type comfyRemoveBody struct {
	Confirm bool `json:"confirm"`
}

func (p *servicePanel) setupApplicationsForPanel(ctx context.Context) (setupApplications, error) {
	p.comfyAppsMu.Lock()
	defer p.comfyAppsMu.Unlock()
	if p.comfyApps != nil {
		return p.comfyApps, nil
	}
	apps, err := panelMachine().ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	p.comfyApps = apps
	return apps, nil
}

func (p *servicePanel) openComfyForActivation(ctx context.Context, journal setup.Journal) (setup.ComfyPaths, error) {
	if p.comfyActivationOpen != nil {
		return p.comfyActivationOpen(ctx, journal)
	}
	return setup.OpenInstalledComfy(ctx, journal)
}

func comfyDescriptorEqual(a, b fwire.ApplicationDescriptor) bool {
	return reflect.DeepEqual(a, b)
}

// comfyRemoveCommand is the command line equivalent of the Panel's Remove
// action, shown in the details.
const comfyRemoveCommand = "openabstractions applications remove " + setup.ComfyApplicationName

// comfyConflictSummary is the one visible sentence for a registered
// picture workspace entry that differs from this build's recipe. The entry's
// application id appears only in the details' command.
func comfyConflictSummary(registered, current fwire.ApplicationDescriptor) string {
	if registered.Program == current.Program {
		return "The registered picture workspace entry is from an earlier OpenAbstractions build; it was preserved."
	}
	return "A picture workspace entry for another program is already registered; it was preserved."
}

// comfyDescriptorConflict explains a registered entry that differs from this
// build's recipe. The entry is preserved; the Panel's Remove action deletes
// it, after which this build registers its own.
func comfyDescriptorConflict(registered, current fwire.ApplicationDescriptor) string {
	return comfyConflictSummary(registered, current)
}

// comfyConflictReply is the status the setup page shows for a differing
// entry: the summary sentence, the registered entry's name, the Remove action
// and the command in the details.
func comfyConflictReply(registered, current fwire.ApplicationDescriptor) comfyApplicationReply {
	return comfyApplicationReply{Available: true, Registered: true, Conflict: true,
		Foreign: registered.Program != current.Program, Name: comfyEntryName(registered),
		Reason: comfyConflictSummary(registered, current), Command: comfyRemoveCommand}
}

// comfyEntryName names a registered entry in the Panel's sentences: its
// title, else its program's stem and "(registered app)", never its file name
// and never the registered name, which would read as this workspace for another
// program's entry. A reserved title reads as appPageTitle reads it.
func comfyEntryName(descriptor fwire.ApplicationDescriptor) string {
	if strings.TrimSpace(descriptor.Title) == "" {
		return programStem(descriptor.Program) + " (registered app)"
	}
	return appPageTitle(descriptor)
}

func findComfyDescriptor(page fwire.ApplicationPage) (fwire.ApplicationDescriptor, bool, error) {
	if page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return fwire.ApplicationDescriptor{}, false, fmt.Errorf("application list is %s", page.Outcome.String())
	}
	for _, entry := range page.Applications {
		if entry.Descriptor.Name == setup.ComfyApplicationName {
			return entry.Descriptor, true, nil
		}
	}
	return fwire.ApplicationDescriptor{}, false, nil
}

func comfyReadyPresence(page fwire.ApplicationPage) bool {
	for _, entry := range page.Applications {
		if entry.Descriptor.Name != setup.ComfyApplicationName {
			continue
		}
		for _, instance := range entry.Instances {
			for _, iface := range instance.Interfaces {
				if iface.Name == "oa-integration-loaded" && iface.Protocol == "oa-comfyui" && iface.Contract == "openabstractions.comfyui/integration-ready@1" {
					return true
				}
			}
		}
	}
	return false
}

const comfyWebURL = "http://127.0.0.1:8188/"

// checkComfyWebUI is a short fixed loopback availability probe, used only
// after the public Applications service observes the exact OA integration
// interface. Its result only controls whether the browser link is shown; it
// never establishes caller identity, permissions, or inference availability.
func checkComfyWebUI(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, comfyWebURL+"system_stats", nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Timeout: 1500 * time.Millisecond,
		Transport: &http.Transport{
			Proxy:       nil,
			DialContext: (&net.Dialer{Timeout: 750 * time.Millisecond}).DialContext,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	return response.StatusCode == http.StatusOK
}

// registerSetupEntry is Setup's confirmed registration of its fixed entry. The
// Panel's read and activate rules on the name are written first, and
// rewriting them is idempotent: the Panel then sees whatever entry holds the
// name. A registration refused as a conflict, from an entry the Panel could
// not read before, is checked once more against the now readable entry, so a
// matching entry reads as registered and a different one as a reviewable
// conflict.
func registerSetupEntry(ctx context.Context, apps setupApplications, reads applicationReadRules, descriptor fwire.ApplicationDescriptor, earlier []fwire.ApplicationDescriptor) (bool, error) {
	if err := reads.allowPanelRead(ctx, descriptor.Name); err != nil {
		return false, fmt.Errorf("the Panel could not save its permission to list and open this entry: %w", err)
	}
	for retried := false; ; retried = true {
		created, err := ensureComfyDescriptorRegistered(ctx, apps, descriptor, earlier)
		if retried || err == nil || !strings.Contains(err.Error(), "("+fwire.ApplicationOutcomeConflict.String()+")") {
			return created, err
		}
	}
}

// ensureComfyDescriptorRegistered registers this build's fixed entry. An
// entry equal to one Setup built for a version an update replaced is Setup's
// own earlier registration: it is removed and the updated entry registered,
// with the same application.manage right the first registration used. Any
// other differing entry is preserved.
func ensureComfyDescriptorRegistered(ctx context.Context, apps setupApplications, descriptor fwire.ApplicationDescriptor, earlier []fwire.ApplicationDescriptor) (bool, error) {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return false, err
	}
	registered, found, err := findComfyDescriptor(page)
	if err != nil {
		return false, err
	}
	if found {
		if comfyDescriptorEqual(registered, descriptor) {
			return false, nil
		}
		if !slices.ContainsFunc(earlier, func(own fwire.ApplicationDescriptor) bool { return comfyDescriptorEqual(registered, own) }) {
			return false, errors.New(comfyDescriptorConflict(registered, descriptor))
		}
		change, err := apps.Remove(ctx, setup.ComfyApplicationName)
		if err != nil {
			return false, err
		}
		if change.Outcome != fwire.ApplicationOutcomeApplied {
			return false, fmt.Errorf("the earlier ComfyUI entry was not removed (%s); it was preserved", change.Outcome.String())
		}
	}
	change, err := apps.Register(ctx, descriptor)
	if err != nil {
		return false, err
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return false, fmt.Errorf("ComfyUI descriptor was not registered (%s); refresh and inspect the current entry", change.Outcome.String())
	}
	return true, nil
}

func (p *servicePanel) setupComfyPlan(ctx context.Context, setupPlanID string, requireCurrent bool) (setup.Journal, error) {
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		return setup.Journal{}, err
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		return setup.Journal{}, err
	}
	if setupPlanID == "" || state.Plan.ID != setupPlanID || !slices.Contains(state.Plan.Selected, setup.ComfyUI) {
		return setup.Journal{}, errors.New("the approved picture setup plan changed; review and approve it again")
	}
	if requireCurrent && !state.Approved {
		return setup.Journal{}, errors.New("the picture setup catalog changed; review and approve the current plan")
	}
	if !requireCurrent {
		prior, err := journal.Load(ctx)
		if err != nil || prior.ApprovedPlanID != setupPlanID {
			return setup.Journal{}, errors.New("the installed picture setup has no approved plan")
		}
	}
	return journal, nil
}

func decodeComfyApplicationRequest(w http.ResponseWriter, r *http.Request) (comfyApplicationRequest, bool) {
	var request comfyApplicationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid ComfyUI setup request", http.StatusBadRequest)
		return comfyApplicationRequest{}, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one ComfyUI setup request required", http.StatusBadRequest)
		return comfyApplicationRequest{}, false
	}
	if !request.Confirm || request.SetupPlanID == "" {
		http.Error(w, "explicit confirmation of the approved picture setup is required", http.StatusBadRequest)
		return comfyApplicationRequest{}, false
	}
	return request, true
}

func (p *servicePanel) setupComfyRegister(w http.ResponseWriter, r *http.Request) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeComfyApplicationRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	journal, err := p.setupComfyPlan(ctx, request.SetupPlanID, true)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	paths, err := p.openInstalledComfy(ctx, journal)
	if err != nil {
		http.Error(w, "ComfyUI installation did not pass its pinned receipt check", http.StatusConflict)
		return
	}
	if err := paths.PrepareDataDirectories(); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := p.ensureComfyImagePolicy(ctx, paths); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	controller, err := p.setupModelForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	model, err := controller.Status(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if !model.Ready {
		http.Error(w, "the reviewed picture model and exact app policy are not ready yet", http.StatusConflict)
		return
	}
	descriptor, err := setup.ComfyApplicationDescriptor(paths)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	earlier, err := setup.EarlierComfyDescriptors(ctx, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	created, err := registerSetupEntry(ctx, apps, p.applicationReads(), descriptor, earlier)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			panelError(w, err)
		} else {
			http.Error(w, err.Error(), http.StatusConflict)
		}
		return
	}
	p.comfyAppsMu.Lock()
	p.comfyRegisteredDescriptor = &descriptor
	p.comfyAppsMu.Unlock()
	reply := comfyApplicationReply{Available: true, Registered: true, Readable: !created, Matching: true}
	// The registration no longer starts an earlier version; remove it now.
	if state, err := journal.Load(ctx); err == nil && len(state.Effects["comfyui-app"].Retired) > 0 {
		runner, _, err := p.setupRunnerForPanel()
		if err == nil {
			_, err = runner.Advance(ctx, "comfyui-app")
		}
		if err != nil {
			reply.Reason = "The updated ComfyUI is registered. The earlier version stays for now: " + err.Error()
		}
	}
	panelJSON(w, reply)
}

// setupComfyRemove deletes the registered comfyui entry through the same
// Applications client Register and Activate use. The entry's name is fixed
// here; the request carries only the person's confirmation.
func (p *servicePanel) setupComfyRemove(w http.ResponseWriter, r *http.Request) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request comfyRemoveBody
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid ComfyUI removal request", http.StatusBadRequest)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one ComfyUI removal request required", http.StatusBadRequest)
		return
	}
	if !request.Confirm {
		http.Error(w, "explicit confirmation is required to remove the ComfyUI entry", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	// The sentence names the entry as registered. A Panel without the read
	// permission still removes it and names it plainly.
	name := "the picture workspace entry"
	if page, err := apps.Observe(ctx, "", 0); err == nil {
		if registered, found, _ := findComfyDescriptor(page); found {
			if entryName := comfyEntryName(registered); entryName != "" {
				name = entryName
			}
		}
	}
	change, err := apps.Remove(ctx, setup.ComfyApplicationName)
	if err != nil {
		panelError(w, err)
		return
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		reason := change.Reason
		if reason == "" {
			reason = "the application entry was not removed (" + change.Outcome.String() + ")"
		}
		http.Error(w, reason, http.StatusConflict)
		return
	}
	p.comfyAppsMu.Lock()
	p.comfyRegisteredDescriptor = nil
	p.comfyActivationStarted = false
	p.comfyAppsMu.Unlock()
	panelJSON(w, comfyApplicationReply{Available: true, Message: "Removed " + name + "."})
}

func (p *servicePanel) setupComfyActivate(w http.ResponseWriter, r *http.Request) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeComfyApplicationRequest(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	journal, err := p.setupComfyPlan(ctx, request.SetupPlanID, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// Activation is an explicit launch boundary. Reopen through the shared
	// receipt verifier here so a changed installation cannot reuse paths that
	// were validated earlier during setup or status polling.
	paths, err := p.openComfyForActivation(ctx, journal)
	if err != nil {
		http.Error(w, "ComfyUI installation did not pass its pinned receipt check", http.StatusConflict)
		return
	}
	p.comfyPathsMu.Lock()
	p.comfyPaths = &paths
	p.comfyPathsMu.Unlock()
	descriptor, err := setup.ComfyApplicationDescriptor(paths)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		panelError(w, err)
		return
	}
	registered, found, err := findComfyDescriptor(page)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if !found || !comfyDescriptorEqual(registered, descriptor) {
		http.Error(w, "the exact reviewed ComfyUI descriptor is not registered yet", http.StatusConflict)
		return
	}
	controller, err := p.setupModelForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	model, err := controller.Status(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if !model.Ready {
		http.Error(w, "the reviewed picture model and exact app policy are not ready yet", http.StatusConflict)
		return
	}
	if comfyReadyPresence(page) {
		webReady := checkComfyWebUI(ctx)
		p.comfyAppsMu.Lock()
		p.comfyActivationStarted = false
		p.comfyAppsMu.Unlock()
		reply := comfyApplicationReply{Available: true, Registered: true, Readable: true, Matching: true, Ready: true, WebReady: webReady, Outcome: "ready"}
		if webReady {
			reply.WebURL = comfyWebURL
		}
		panelJSON(w, reply)
		return
	}
	p.comfyAppsMu.Lock()
	if p.comfyActivationStarted {
		p.comfyAppsMu.Unlock()
		panelJSON(w, comfyApplicationReply{Available: true, Registered: true, Readable: true, Matching: true, Starting: true, Outcome: "not_ready", Started: true, Reason: "ComfyUI is still starting; setup is checking its OpenAbstractions integration."})
		return
	}
	p.comfyAppsMu.Unlock()
	result, err := apps.Activate(ctx, setup.ComfyApplicationName)
	if err != nil {
		panelError(w, err)
		return
	}
	if result.Outcome == fwire.ApplicationActivationOutcomeReady || result.Outcome == fwire.ApplicationActivationOutcomeNotReady {
		p.comfyAppsMu.Lock()
		p.comfyActivationStarted = true
		p.comfyAppsMu.Unlock()
	}
	webReady := result.Outcome == fwire.ApplicationActivationOutcomeReady && checkComfyWebUI(ctx)
	if result.Outcome == fwire.ApplicationActivationOutcomeReady {
		p.comfyAppsMu.Lock()
		p.comfyActivationStarted = false
		p.comfyAppsMu.Unlock()
	}
	reply := comfyApplicationReply{Available: true, Registered: true, Matching: true,
		Ready:    result.Outcome == fwire.ApplicationActivationOutcomeReady,
		Starting: result.Outcome == fwire.ApplicationActivationOutcomeNotReady,
		Outcome:  result.Outcome.String(), Started: result.Started, Reason: result.Reason, WebReady: webReady}
	if webReady {
		reply.WebURL = comfyWebURL
	}
	panelJSON(w, reply)
}

func (p *servicePanel) setupComfyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	p.comfyPathsMu.Lock()
	paths := p.comfyPaths
	p.comfyPathsMu.Unlock()
	if paths == nil {
		panelJSON(w, comfyApplicationReply{Reason: "Use the explicit setup step to verify the installed ComfyUI package."})
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	descriptor, err := setup.ComfyApplicationDescriptor(*paths)
	if err != nil {
		panelJSON(w, comfyApplicationReply{Reason: err.Error()})
		return
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelJSON(w, comfyApplicationReply{Available: true, Reason: err.Error()})
		return
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		panelJSON(w, comfyApplicationReply{Available: true, Reason: err.Error()})
		return
	}
	registered, found, err := findComfyDescriptor(page)
	if err != nil {
		panelJSON(w, comfyApplicationReply{Available: true, Reason: err.Error()})
		return
	}
	if !found {
		p.comfyAppsMu.Lock()
		registeredByPanel := p.comfyRegisteredDescriptor != nil && comfyDescriptorEqual(*p.comfyRegisteredDescriptor, descriptor)
		p.comfyAppsMu.Unlock()
		if registeredByPanel {
			panelJSON(w, comfyApplicationReply{Available: true, Registered: true, Matching: true, Reason: "The Panel registered the picture workspace but cannot check it. Restore the Panel's permissions on the Permissions page."})
			return
		}
		panelJSON(w, comfyApplicationReply{Available: true, Reason: "The picture workspace is not registered yet. Prepare it, or restore the Panel's permissions on the Permissions page to check an existing one."})
		return
	}
	if !comfyDescriptorEqual(registered, descriptor) {
		panelJSON(w, comfyConflictReply(registered, descriptor))
		return
	}
	ready := comfyReadyPresence(page)
	webReady := false
	if ready {
		webReady = checkComfyWebUI(ctx)
	}
	p.comfyAppsMu.Lock()
	starting := p.comfyActivationStarted && !ready
	if ready {
		p.comfyActivationStarted = false
	}
	p.comfyAppsMu.Unlock()
	reply := comfyApplicationReply{Available: true, Registered: true, Readable: true, Matching: true, Ready: ready, Starting: starting, WebReady: webReady}
	if webReady {
		reply.WebURL = comfyWebURL
	}
	panelJSON(w, reply)
}

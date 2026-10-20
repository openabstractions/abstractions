package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func (pkg *officialPackagePanel) retainedShortcut() extensionShortcut {
	const description = " with OpenAbstractions image tools"
	registration := pkg.archive.Registration()
	if shortcut, ok := declaredExtensionShortcut(registration, description); ok && shortcut.application == registration {
		return shortcut
	}
	// The retained workspace routes keep their historical registration.
	title := pkg.retainedTitle
	return extensionShortcut{application: registration, title: title, description: title + description}
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

func (p *servicePanel) cachedSetupApplications() setupApplications {
	p.comfyAppsMu.Lock()
	defer p.comfyAppsMu.Unlock()
	return p.comfyApps
}

func (p *servicePanel) openComfyForActivation(ctx context.Context, journal setup.Journal) (setup.ComfyPaths, error) {
	if p.comfyActivationOpen != nil {
		return p.comfyActivationOpen(ctx, journal)
	}
	return setup.OpenInstalledComfy(ctx, journal)
}

func (pkg *officialPackagePanel) findWorkspaceDescriptor(page fwire.ApplicationPage) (fwire.ApplicationDescriptor, bool, error) {
	registration := pkg.archive.Registration()
	return findRegistrationDescriptor(page, registration)
}

func (pkg *officialPackagePanel) retainedWorkspaceRegistration() workspaceRegistrationPolicy {
	registration := pkg.archive.Registration()
	return workspaceRegistrationPolicy{
		registration: registration, title: pkg.retainedShortcut().title,
		replaceable: comfyEntryReplaceable, nativeRootOwns: setup.ComfyNativeRootHolds,
	}
}

// registerWorkspaceEntry is Setup's confirmed registration of its fixed entry. The
// Panel's read and activate rules on the name are written first, and
// rewriting them is idempotent: the Panel then sees whatever entry holds the
// name. A registration refused as a conflict, from an entry the Panel could
// not read before, is checked once more against the now readable entry, so a
// matching entry reads as registered and a different one as a reviewable
// conflict.
func (pkg *officialPackagePanel) registerWorkspaceEntry(ctx context.Context, apps setupApplications, reads applicationReadRules, descriptor fwire.ApplicationDescriptor, earlier []fwire.ApplicationDescriptor, native *workspaceNative) (workspaceRegistration, error) {
	workspace := pkg.retainedWorkspaceRegistration()
	if err := workspace.checkTarget(descriptor); err != nil {
		return workspaceUnchanged, err
	}
	if err := reads.allowPanelRead(ctx, descriptor.Name); err != nil {
		return workspaceUnchanged, fmt.Errorf("the Panel could not save its permission to list and open this entry: %w", err)
	}
	for retried := false; ; retried = true {
		done, err := workspace.ensureRegistered(ctx, apps, descriptor, earlier, native)
		if retried || err == nil || !strings.Contains(err.Error(), "("+fwire.ApplicationOutcomeConflict.String()+")") {
			return done, err
		}
	}
}

// completeWorkspace is the workspace step's work: the installed build's
// entry in Apps (registerWorkspaceEntry), then the Start-menu shortcut that opens
// it through the same owned launch the app page's Open uses.
func (pkg *officialPackagePanel) completeWorkspace(ctx context.Context, apps setupApplications, reads applicationReadRules, descriptor fwire.ApplicationDescriptor, earlier []fwire.ApplicationDescriptor, native *workspaceNative) (workspaceRegistration, error) {
	done, err := pkg.registerWorkspaceEntry(ctx, apps, reads, descriptor, earlier, native)
	if err != nil {
		return done, err
	}
	shortcut := pkg.retainedShortcut()
	if err := shortcut.ensure(); err != nil {
		return done, fmt.Errorf("%s is in Apps, but its Start menu shortcut was not saved: %w", shortcut.title, err)
	}
	return done, nil
}

// comfyEntryReplaceable reports whether a registered comfyui entry for
// another program is one Setup replaces with the installed build: its
// program is gone, it lies in a folder Setup installs versions into, or it is
// an OpenAbstractions build of ComfyUI registered before Setup installed one.
// A signed Panel recognises that build by its own signer (oaSameSigner). An
// unsigned preview Panel has no signer to compare, and recognises it by the
// title OpenAbstractions' builds register under (setup.ComfyApplicationTitle):
// an entry claiming that title gives up the name to the build the person
// installed, and removing it changes no file and grants nothing. Every other
// program's entry belongs to a third party and is preserved. Tests replace it.
var comfyEntryReplaceable = func(registered fwire.ApplicationDescriptor) bool {
	program := registered.Program
	if !filepath.IsAbs(program) || filepath.Clean(program) != program || identity.LocalPath(program) != nil {
		return false
	}
	if setup.ComfyInstallFolderHolds(program) {
		return true
	}
	registration := officialPicture.archive.Registration()
	if registered.Name == registration && registered.Title == setup.ComfyApplicationTitle && !panelSigned() {
		return true
	}
	if _, err := os.Lstat(program); errors.Is(err, fs.ErrNotExist) {
		return true
	}
	return oaSameSigner(program)
}

// panelSigned reports whether this Panel's own file carries a signer that
// oaSameSigner can compare others with. Tests replace it.
var panelSigned = func() bool {
	self, err := panelExecutable()
	return err == nil && oaSameSigner(self)
}

// comfyWorkspaceState reads whether the installed ComfyUI an approved plan
// selected is in Apps and in the Start menu: "ready"; "unregistered" with no
// entry; "stale" with an entry Setup replaces (comfyEntryReplaceable);
// "other" with another program's entry, which stays, and note its sentence;
// "no_shortcut" with the entry in place and no current shortcut. It answers ""
// when the plan has no installed ComfyUI or the runtime cannot be read, and
// the page then decides from its other steps. The read opens no installed
// file.
func (pkg *officialPackagePanel) workspaceState(p *servicePanel, ctx context.Context, journal setup.Journal, plan setup.Plan) (state, note string) {
	if !slices.Contains(plan.Selected, pkg.retainedApp) {
		return "", ""
	}
	archive := pkg.archive
	component, registration := archive.Component(), archive.Registration()
	recorded, err := journal.Load(ctx)
	if err != nil || recorded.Effects[component].Phase != setup.Installed {
		return "", ""
	}
	program, err := comfyInstalledProgram(ctx, journal)
	if err != nil {
		return "", ""
	}
	apps, err := p.nativeApplicationsForPanel(ctx, registration)
	if err != nil {
		return "", ""
	}
	return pkg.retainedWorkspaceRegistration().installedState(ctx, apps, program, pkg.retainedShortcut().current)
}

// comfyInstalledProgram is setup.InstalledComfyProgram. Tests replace it.
var comfyInstalledProgram = setup.InstalledComfyProgram

// approvedWorkspacePlan checks that the plan the request names is the installed,
// approved plan that selected ComfyUI. An update that changed the catalogue
// leaves it in force: registration installs nothing.
func (pkg *officialPackagePanel) approvedWorkspacePlan(p *servicePanel, ctx context.Context, setupPlanID string) (setup.Journal, error) {
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		return setup.Journal{}, err
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		return setup.Journal{}, err
	}
	if setupPlanID == "" || state.Plan.ID != setupPlanID || !slices.Contains(state.Plan.Selected, pkg.retainedApp) {
		return setup.Journal{}, errors.New("the approved picture setup plan changed; review and approve it again")
	}
	prior, err := journal.Load(ctx)
	if err != nil || prior.ApprovedPlanID != setupPlanID {
		return setup.Journal{}, errors.New("the installed picture setup has no approved plan")
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

// settleWorkspaceRegistration records what Setup's registration did: the entry
// it registered, the Activity line for a re-pinned update,
// and for an app registration the service refused for its image, the Not
// connected request Apps lists with Connect. A completed registration ends
// any such request.
func (pkg *officialPackagePanel) settleWorkspaceRegistration(p *servicePanel, descriptor fwire.ApplicationDescriptor, done workspaceRegistration, err error) {
	if done != workspaceUnchanged || err == nil {
		p.comfyAppsMu.Lock()
		p.comfyRegisteredDescriptor = &descriptor
		p.comfyAppsMu.Unlock()
	}
	if done == workspaceUpdated {
		title := pkg.retainedShortcut().title
		p.appActivity.add(applicationActivity{App: title, Program: descriptor.Program, What: "Updated " + title, Outcome: "Updated"})
	}
	switch {
	case errors.Is(err, setup.ErrRegistrationIdentity):
		p.recordNativeIdentityRequest(descriptor.Name, descriptor.Program)
	case err == nil:
		p.removeApplicationRequest(descriptor.Name)
	}
}

// retainedWorkspaceNative is the installed native package's registration:
// its launch helper's register-self, and the hold of that helper after the
// package receipt check.
func retainedWorkspaceNative(archive setup.RetainedArchive, journal setup.Journal) *workspaceNative {
	return &workspaceNative{
		register: func(ctx context.Context) error { return archive.RegisterSelf(ctx, journal) },
		hold:     func(ctx context.Context) (setup.HeldProgram, error) { return archive.HoldProgram(ctx, journal) },
	}
}

func (pkg *officialPackagePanel) serveWorkspaceRegister(p *servicePanel, w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
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
	// The approved plan that installed the package and its receipt check are
	// the authority (approvedWorkspacePlan).
	journal, err := pkg.approvedWorkspacePlan(p, ctx, request.SetupPlanID)
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
	// Registration publishes the receipt-bound app entry. Model selection,
	// provider readiness and inference consent belong to their explicit steps.
	descriptor, err := setup.ComfyApplicationDescriptor(paths)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	apps, err := p.nativeApplicationsForPanel(ctx, descriptor.Name)
	if err != nil {
		panelError(w, err)
		return
	}
	earlier, err := setup.EarlierComfyDescriptors(ctx, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	archive := pkg.archive
	var native *workspaceNative
	if isNative, err := archive.UsesNativeResources(ctx, journal); err != nil {
		panelError(w, err)
		return
	} else if isNative {
		native = retainedWorkspaceNative(archive, journal)
	}
	done, err := pkg.completeWorkspace(ctx, apps, p.applicationReads(), descriptor, earlier, native)
	created := done == workspaceCreated
	pkg.settleWorkspaceRegistration(p, descriptor, done, err)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			panelError(w, err)
		} else {
			http.Error(w, err.Error(), http.StatusConflict)
		}
		return
	}
	shortcut := true
	reply := workspaceApplicationReply{Available: true, Registered: true, Readable: !created, Matching: true, Shortcut: &shortcut, Application: descriptor.Name}
	// The registration no longer starts an earlier version; remove it now.
	component := archive.Component()
	if state, err := journal.Load(ctx); err == nil && len(state.Effects[component].Retired) > 0 {
		runner, _, err := p.setupRunnerForPanel()
		if err == nil {
			_, err = runner.Advance(ctx, component)
		}
		if err != nil {
			reply.Reason = "The updated ComfyUI is registered. The earlier version stays for now: " + err.Error()
		}
	}
	panelJSON(w, reply)
}

// serveWorkspaceRemove removes the route's fixed registration. The shortcut
// opens that entry, and the request carries only the person's confirmation.
func (pkg *officialPackagePanel) serveWorkspaceRemove(p *servicePanel, w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
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
	shortcut := pkg.retainedShortcut()
	apps, err := p.nativeApplicationsForPanel(ctx, shortcut.application)
	if err != nil {
		panelError(w, err)
		return
	}
	// The sentence names the entry as registered. A Panel without the read
	// permission still removes it and names it plainly.
	name := "the picture workspace entry"
	if registered, found, err := observeRegisteredApplication(ctx, apps, shortcut.application); err == nil && found {
		if entryName := workspaceEntryName(registered); entryName != "" {
			name = entryName
		}
	}
	change, err := apps.Remove(ctx, shortcut.application)
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
	// The shortcut opens the entry just removed.
	if err := shortcut.remove(); err != nil {
		log.Printf("setup: %s Start menu shortcut: %v", shortcut.title, err)
	}
	panelJSON(w, workspaceApplicationReply{Available: true, Message: "Removed " + name + "."})
}

func (pkg *officialPackagePanel) serveWorkspaceActivate(p *servicePanel, w http.ResponseWriter, r *http.Request) {
	http.Error(w, "Use the application's page in Apps to send its startup request.", http.StatusGone)
}

func (pkg *officialPackagePanel) serveWorkspaceStatus(p *servicePanel, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	p.comfyPathsMu.Lock()
	paths := p.comfyPaths
	p.comfyPathsMu.Unlock()
	if paths == nil {
		panelJSON(w, workspaceApplicationReply{Available: true, Reason: "Register the installed application; Setup verifies its package receipt before changing the app entry."})
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	descriptor, err := setup.ComfyApplicationDescriptor(*paths)
	if err != nil {
		panelJSON(w, workspaceApplicationReply{Reason: err.Error()})
		return
	}
	workspace := pkg.retainedWorkspaceRegistration()
	if err := workspace.checkTarget(descriptor); err != nil {
		panelJSON(w, workspaceApplicationReply{Reason: err.Error()})
		return
	}
	apps, err := p.nativeApplicationsForPanel(ctx, workspace.registration)
	if err != nil {
		panelJSON(w, workspaceApplicationReply{Available: true, Reason: err.Error()})
		return
	}
	panelJSON(w, workspace.status(ctx, apps, descriptor, pkg.retainedShortcut().current))
}

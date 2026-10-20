package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"

	manifestowner "github.com/openabstractions/abstraction-facade/go/manifest"
	identity "github.com/openabstractions/abstraction-identity"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	facadeclient "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type setupApplications interface {
	Register(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error)
	Remove(context.Context, string) (fwire.ApplicationChange, error)
	Observe(context.Context, string, int64) (fwire.ApplicationPage, error)
	Activate(context.Context, string) (fwire.ApplicationActivationResult, error)
}

type workspaceApplicationReply struct {
	Application string `json:"application,omitempty"`
	Available   bool   `json:"available"`
	Registered  bool   `json:"registered"`
	Readable    bool   `json:"readable"`
	Matching    bool   `json:"matching"`
	Ready       bool   `json:"ready"`
	WebReady    bool   `json:"web_ready"`
	WebURL      string `json:"web_url,omitempty"`
	Starting    bool   `json:"starting"`
	Reason      string `json:"reason,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Started     bool   `json:"started,omitempty"`
	// Shortcut reports whether the Start menu holds Setup's current workspace
	// shortcut; absent when the reply did not read it.
	Shortcut *bool `json:"shortcut,omitempty"`
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

// Native application pages use the existing signed-manifest contract.
// The runtime extracts the manifest; this client supplies no recipe authority.
type nativeApplications struct {
	transport   func(context.Context) applications.ApplicationsTransport
	revision    string
	application string
	owned       bool
	canOpen     bool
	disabled    bool
	scriptHost  bool                   // private @3 metadata; the closed @2 descriptor stays unchanged
	requestKey  func() (string, error) // focused failure-control seam
}

// applicationDisplayEqual compares the listed entry with Setup's own: the
// service stores the normalized, handle-resolved program, which may differ in
// spelling (case, short names) from the path Setup builds from its root.
func applicationDisplayEqual(a, b fwire.ApplicationDescriptor) bool {
	return a.Name == b.Name && identity.SameSubjectProgram(a.Program, b.Program) && a.Title == b.Title
}

func isNativeApplications(apps setupApplications) bool {
	_, ok := apps.(*nativeApplications)
	return ok
}

func (n *nativeApplications) client(ctx context.Context) *applications.ApplicationsClient {
	return applications.NewApplicationsClient(n.transport(ctx))
}

func (p *servicePanel) nativeApplicationsForPanel(ctx context.Context, application string) (setupApplications, error) {
	if fixture := p.cachedSetupApplications(); fixture != nil {
		// Existing injected fixture seam; an actual cached @2 client never
		// substitutes for the native registration/activation contract.
		if _, legacy := fixture.(*facadeclient.Applications); !legacy {
			return fixture, nil
		}
	}
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	transport := panelApplicationsTransport(binding.Transport())
	return &nativeApplications{application: application, transport: func(ctx context.Context) applications.ApplicationsTransport { return transport.WithContext(ctx) }}, nil
}

// panelApplicationsCallBudget is the longest one applications@3 call may take
// from the Panel. An Activate waits for the person's Allow (the wait policy's
// 40 s) and then for the app's start (its manifest's readiness, at most 30
// s); the transport's default call budget of 5 s cut the Panel's Open, and
// the Start-menu shortcut's, before either finished. Each call's own context
// still decides its deadline.
const panelApplicationsCallBudget = 2 * time.Minute

func panelApplicationsTransport(transport listen.FrameClient) listen.FrameClient {
	return transport.WithDefaults(panelApplicationsCallBudget, 0)
}

func (n *nativeApplications) Register(ctx context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	r, err := n.client(ctx).Register(applications.RegisterRequest{Program: d.Program})
	if err != nil {
		return fwire.ApplicationChange{}, err
	}
	o, ok := fwire.ParseApplicationOutcome(r.Outcome.String())
	if !ok {
		return fwire.ApplicationChange{}, fmt.Errorf("native registration outcome %s", r.Outcome)
	}
	if o == fwire.ApplicationOutcomeApplied {
		page, err := n.Observe(ctx, "", 0)
		if err != nil {
			return fwire.ApplicationChange{}, err
		}
		matched := false
		for _, entry := range page.Applications {
			if entry.Descriptor.Name == n.application && identity.SameSubjectProgram(entry.Descriptor.Program, d.Program) && (d.Title == "" || entry.Descriptor.Title == d.Title) {
				matched = true
			}
		}
		if page.Outcome != fwire.ApplicationOutcomePage || !matched {
			return fwire.ApplicationChange{}, fmt.Errorf("native registration readback differs from expected own image")
		}
	}
	return fwire.ApplicationChange{Outcome: o, Reason: r.Reason}, nil
}

// setupReplacer re-pins the entry Observe read to program: the operator's
// Register naming that entry's revision (applicationV3Repin in serve). A
// stale revision meets conflict and changes nothing.
type setupReplacer interface {
	Replace(ctx context.Context, program string) (fwire.ApplicationChange, error)
}

func (n *nativeApplications) observedRevision() string { return n.revision }

// Replace re-pins the observed entry to program and reads it back: the entry
// lists program afterwards.
func (n *nativeApplications) Replace(ctx context.Context, program string) (fwire.ApplicationChange, error) {
	change, _, err := n.replaceRevision(ctx, program, false)
	if err != nil {
		return fwire.ApplicationChange{}, err
	}
	return change, nil
}

func (n *nativeApplications) replaceAndVerifyRevision(ctx context.Context, program string) (fwire.ApplicationChange, string, error) {
	return n.replaceRevision(ctx, program, true)
}

func (n *nativeApplications) replaceRevision(ctx context.Context, program string, exact bool) (fwire.ApplicationChange, string, error) {
	if n.revision == "" {
		return fwire.ApplicationChange{}, "", fmt.Errorf("native replacement has no observed revision")
	}
	r, err := n.client(ctx).Register(applications.RegisterRequest{Program: program, ExpectedRevision: n.revision})
	if err != nil {
		return fwire.ApplicationChange{}, "", err
	}
	o, ok := fwire.ParseApplicationOutcome(r.Outcome.String())
	if !ok {
		return fwire.ApplicationChange{}, "", fmt.Errorf("native replacement outcome %s", r.Outcome)
	}
	change := fwire.ApplicationChange{Outcome: o, Reason: r.Reason}
	if o == fwire.ApplicationOutcomeApplied {
		page, err := n.Observe(ctx, "", 0)
		if err != nil {
			return change, r.Revision, err
		}
		if page.Outcome != fwire.ApplicationOutcomePage || len(page.Applications) != 1 || !identity.SameSubjectProgram(page.Applications[0].Descriptor.Program, program) {
			return change, r.Revision, fmt.Errorf("native replacement readback differs from the installed program")
		}
		if exact && (r.Revision == "" || r.Revision != n.revision) {
			return change, r.Revision, fmt.Errorf("native replacement readback differs from the applied revision")
		}
	}
	return change, r.Revision, nil
}

// ExpectConnect records the person's Connect click for name: its program
// and the SHA-256 of the image the Panel installed (architect ruling
// 2026-11-11). It calls the operator-only abstraction.applications/operator@1
// on the same runtime endpoint as applications@3.
func (n *nativeApplications) ExpectConnect(ctx context.Context, name, program, sha256 string) (fwire.ApplicationChange, error) {
	return applications.NewApplicationsOperatorClient(n.transport(ctx)).ExpectConnect(applications.ConnectExpectation{Application: name, Program: program, SHA256: sha256})
}

func (n *nativeApplications) AskConfirmation(ctx context.Context, name string) (fwire.ApplicationChange, error) {
	return n.client(ctx).AskConfirmation(name)
}

func (n *nativeApplications) Remove(ctx context.Context, name string) (fwire.ApplicationChange, error) {
	if name != n.application || n.revision == "" {
		return fwire.ApplicationChange{}, fmt.Errorf("native removal has no observed revision")
	}
	r, err := n.client(ctx).Remove(applications.ChangeRequest{Application: name, ExpectedRevision: n.revision})
	if err != nil {
		return fwire.ApplicationChange{}, err
	}
	o, ok := fwire.ParseApplicationOutcome(r.Outcome.String())
	if !ok {
		return fwire.ApplicationChange{}, fmt.Errorf("native remove outcome %s", r.Outcome)
	}
	return fwire.ApplicationChange{Outcome: o, Reason: r.Reason}, nil
}

func (n *nativeApplications) Observe(ctx context.Context, cursor string, wait int64) (fwire.ApplicationPage, error) {
	if wait < 0 || wait > 30000 {
		return fwire.ApplicationPage{}, fmt.Errorf("native observe wait outside bound")
	}
	n.revision, n.owned, n.canOpen, n.scriptHost, n.disabled = "", false, false, false, false
	r, err := n.client(ctx).Observe(applications.Query{Application: n.application, Cursor: cursor, Limit: 1, WaitMs: int32(wait)})
	if err == nil && r.Outcome == applications.OutcomeStale && cursor != "" {
		// One fresh read honors the existing @3 restart contract. Never inspect
		// the opaque cursor or carry the stale page into the @2 display view.
		r, err = n.client(ctx).Observe(applications.Query{Application: n.application, Limit: 1})
	}
	if err != nil {
		return fwire.ApplicationPage{}, err
	}
	o, ok := fwire.ParseApplicationOutcome(r.Outcome.String())
	if !ok {
		return fwire.ApplicationPage{}, fmt.Errorf("native observe outcome %s", r.Outcome)
	}
	page := fwire.ApplicationPage{Outcome: o, Cursor: r.Cursor}
	n.revision, n.owned, n.canOpen, n.scriptHost, n.disabled = "", false, false, false, false
	for _, row := range r.Registrations {
		manifest, reading, found := nativeApplicationFacet(&row, n.application)
		if !found || row.Spec.Program == "" {
			return fwire.ApplicationPage{}, fmt.Errorf("unexpected native registration")
		}
		n.revision = row.Revision
		n.disabled = row.State == registration.StateDisabled
		var instances []fwire.ApplicationInstance
		if reading != nil {
			n.owned = len(reading.OwnedInstances) != 0
			instances = reading.Instances
		}
		n.scriptHost = manifest.ScriptHost
		display := fwire.ApplicationDescriptor{Name: manifest.Name, Program: row.Spec.Program, Title: manifest.Title}
		if manifestowner.ValidateApplicationActivationPayload(manifest, &applications.ActivateRequest{Application: manifest.Name, Verb: "open", Payload: applications.Raw(`{}`)}) == nil {
			n.canOpen = true
			display.Activation = &fwire.ApplicationActivationRecipe{}
		}
		page.Applications = append(page.Applications, fwire.ApplicationEntry{Scope: fwire.ScopeLocal, Descriptor: display, Instances: instances})
	}
	return page, nil
}

func (n *nativeApplications) Activate(ctx context.Context, name string) (fwire.ApplicationActivationResult, error) {
	if name != n.application || !n.canOpen {
		return fwire.ApplicationActivationResult{}, fmt.Errorf("registered native application has no supported empty-object open verb")
	}
	keyOwner := applications.NewRequestKey
	if n.requestKey != nil {
		keyOwner = n.requestKey
	}
	key, err := keyOwner()
	if err != nil {
		return fwire.ApplicationActivationResult{}, err
	}
	r, err := n.client(ctx).Activate(key, applications.ActivateRequest{Application: name, Verb: "open", Payload: applications.Raw(`{}`)})
	if err != nil {
		return fwire.ApplicationActivationResult{}, err
	}
	o, ok := fwire.ParseApplicationActivationOutcome(r.Outcome.String())
	if !ok {
		return fwire.ApplicationActivationResult{}, fmt.Errorf("native activation outcome %s", r.Outcome)
	}
	return fwire.ApplicationActivationResult{Outcome: o, Instance: r.Instance, Started: r.Started, Reason: r.Reason}, nil
}

// workspaceConflictSummary is the one visible sentence for a registered
// picture workspace entry that differs from this build's recipe. The entry's
// application id appears only in the details' command.
func workspaceConflictSummary(registered, current fwire.ApplicationDescriptor) string {
	if registered.Program == current.Program {
		return "The registered picture workspace entry is from an earlier OpenAbstractions build; it was preserved."
	}
	return "A picture workspace entry for another program is already registered; it was preserved."
}

func observeRegisteredApplication(ctx context.Context, apps setupApplications, registration string) (fwire.ApplicationDescriptor, bool, error) {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return fwire.ApplicationDescriptor{}, false, err
	}
	return findRegistrationDescriptor(page, registration)
}

type workspaceRegistrationPolicy struct {
	registration   string
	title          string
	replaceable    func(fwire.ApplicationDescriptor) bool
	nativeRootOwns func(string) bool
}

func (w workspaceRegistrationPolicy) checkTarget(descriptor fwire.ApplicationDescriptor) error {
	if w.registration == "" || descriptor.Name != w.registration {
		return errors.New("the installed workspace does not match its registration")
	}
	return nil
}

func findRegistrationDescriptor(page fwire.ApplicationPage, registration string) (fwire.ApplicationDescriptor, bool, error) {
	if page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return fwire.ApplicationDescriptor{}, false, fmt.Errorf("application list is %s", page.Outcome.String())
	}
	for _, entry := range page.Applications {
		if entry.Descriptor.Name == registration {
			return entry.Descriptor, true, nil
		}
	}
	return fwire.ApplicationDescriptor{}, false, nil
}

// workspaceNative is the native package's registration: the app's own
// register-self, and hold, the installed program checked against its package
// receipt and held while Setup re-pins an updated entry to it. nil is a
// package registered through its descriptor.
type workspaceNative struct {
	register func(context.Context) error
	hold     func(context.Context) (setup.HeldProgram, error)
}

// workspaceRegistration is what Setup's registration did to the entry.
type workspaceRegistration uint8

const (
	workspaceUnchanged workspaceRegistration = iota
	workspaceCreated
	// workspaceUpdated: Setup re-pinned its earlier entry to the update it
	// installed, and the app's own registration updated it silently.
	workspaceUpdated
)

// ensureRegistered registers a receipt-bound workspace. Older entries are
// replaced only under the supplied package owner's policy.
func (w workspaceRegistrationPolicy) ensureRegistered(ctx context.Context, apps setupApplications, descriptor fwire.ApplicationDescriptor, earlier []fwire.ApplicationDescriptor, native *workspaceNative) (workspaceRegistration, error) {
	if err := w.checkTarget(descriptor); err != nil {
		return workspaceUnchanged, err
	}
	registered, found, err := observeRegisteredApplication(ctx, apps, w.registration)
	if err != nil {
		return workspaceUnchanged, err
	}
	if found {
		same := reflect.DeepEqual(registered, descriptor) || isNativeApplications(apps) && applicationDisplayEqual(registered, descriptor)
		if same && !workspaceMayResumeRepin(apps, native) {
			return workspaceUnchanged, nil
		}
		if native != nil && native.hold != nil {
			repinned, err := w.repin(ctx, apps, registered, descriptor, native)
			if err != nil {
				return workspaceUnchanged, err
			}
			if repinned {
				return workspaceUpdated, nil
			}
		}
		if same {
			return workspaceUnchanged, nil
		}
		if !slices.ContainsFunc(earlier, func(own fwire.ApplicationDescriptor) bool { return reflect.DeepEqual(registered, own) }) &&
			(registered.Program == descriptor.Program || !w.replaceable(registered)) {
			return workspaceUnchanged, errors.New(workspaceConflictSummary(registered, descriptor))
		}
		change, err := apps.Remove(ctx, w.registration)
		if err != nil {
			return workspaceUnchanged, err
		}
		if change.Outcome != fwire.ApplicationOutcomeApplied {
			return workspaceUnchanged, fmt.Errorf("the earlier %s entry was not removed (%s); it was preserved", w.title, change.Outcome.String())
		}
	}
	if native != nil {
		if err := w.registerSelf(ctx, apps, descriptor, native); err != nil {
			return workspaceUnchanged, err
		}
		return workspaceCreated, nil
	}
	change, err := apps.Register(ctx, descriptor)
	if err != nil {
		return workspaceUnchanged, err
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return workspaceUnchanged, fmt.Errorf("%s descriptor was not registered (%s); refresh and inspect the current entry", w.title, change.Outcome.String())
	}
	return workspaceCreated, nil
}

// registerSelf runs the native app's own register-self and reads the
// entry back: the entry now listed is this installation's.
func (w workspaceRegistrationPolicy) registerSelf(ctx context.Context, apps setupApplications, descriptor fwire.ApplicationDescriptor, native *workspaceNative) error {
	if err := native.register(ctx); err != nil {
		return fmt.Errorf("%s did not register itself with OpenAbstractions; register again to retry: %w", w.title, err)
	}
	listed, found, err := observeRegisteredApplication(ctx, apps, w.registration)
	if err != nil {
		return err
	}
	if !found || !applicationDisplayEqual(listed, descriptor) {
		return fmt.Errorf("%s asked to register itself, but OpenAbstractions does not list this installation; register again to retry", w.title)
	}
	return nil
}

// repin updates an archive owner's own earlier registration while holding
// the new receipt-verified image. A pending or disabled registration stays
// with the person when the runtime refuses the replacement.
func (w workspaceRegistrationPolicy) repin(ctx context.Context, apps setupApplications, registered, descriptor fwire.ApplicationDescriptor, native *workspaceNative) (repinned bool, err error) {
	replacer, ok := apps.(setupReplacer)
	if !ok || identity.SameSubjectProgram(registered.Program, descriptor.Program) && !workspaceMayResumeRepin(apps, native) ||
		!w.nativeRootOwns(registered.Program) || !w.nativeRootOwns(descriptor.Program) {
		return false, nil
	}
	held, err := native.hold(ctx)
	if err != nil {
		return false, fmt.Errorf("the updated %s does not match its package, so Setup kept the earlier entry: %w", w.title, err)
	}
	if !identity.SameSubjectProgram(held.Path, descriptor.Program) {
		return false, errors.Join(fmt.Errorf("the updated %s's program is not the one its package names, so Setup kept the earlier entry", w.title), held.Close())
	}
	if held.SHA256 != "" {
		expecting, ok := apps.(expectingApplications)
		if !ok {
			return false, errors.Join(fmt.Errorf("the runtime cannot verify this %s update pin", w.title), held.Close())
		}
		change, expectErr := expecting.ExpectConnect(ctx, w.registration, held.Path, held.SHA256)
		if expectErr != nil || change.Outcome != fwire.ApplicationOutcomeApplied {
			return false, errors.Join(fmt.Errorf("the runtime did not accept this %s update pin (%s %s)", w.title, change.Outcome.String(), change.Reason), expectErr, held.Close())
		}
	}
	change, err := replacer.Replace(ctx, held.Path)
	if err = errors.Join(err, held.Close()); err != nil {
		return false, err
	}
	switch {
	case change.Outcome == fwire.ApplicationOutcomeApplied:
	case change.Outcome == fwire.ApplicationOutcomeConflict && change.Reason == "installer_expectation_expired":
		return false, fmt.Errorf("the earlier %s update expired; remove %s in Apps, then Connect again", w.title, w.title)
	case change.Outcome == fwire.ApplicationOutcomeConflict && change.Reason == "state":
		return false, nil
	default:
		reason := change.Outcome.String()
		if change.Reason != "" {
			reason += ": " + change.Reason
		}
		return false, fmt.Errorf("the earlier %s entry was not updated (%s); it was kept", w.title, reason)
	}
	return true, w.registerSelf(ctx, apps, descriptor, native)
}

// A staged native replacement displays the new program but is disabled
// until the registry publishes it. Retry through the held package path.
func workspaceMayResumeRepin(apps setupApplications, native *workspaceNative) bool {
	client, ok := apps.(*nativeApplications)
	return ok && client.disabled && native != nil && native.hold != nil
}

// conflictReply is the status the setup page shows for a differing
// entry: the summary sentence, the registered entry's name, the Remove action
// and the command in the details.
func (w workspaceRegistrationPolicy) conflictReply(registered, current fwire.ApplicationDescriptor) workspaceApplicationReply {
	return workspaceApplicationReply{Available: true, Registered: true, Conflict: true,
		Foreign: registered.Program != current.Program, Name: workspaceEntryName(registered),
		Reason: workspaceConflictSummary(registered, current), Command: "openabstractions applications remove " + w.registration}
}

// workspaceEntryName names a registered entry in the Panel's sentences: its
// title, else its program's stem and "(registered app)", never its file name
// and never the registered name, which would read as this workspace for another
// program's entry. A reserved title reads as appPageTitle reads it.
func workspaceEntryName(descriptor fwire.ApplicationDescriptor) string {
	if strings.TrimSpace(descriptor.Title) == "" {
		return programStem(descriptor.Program) + " (registered app)"
	}
	return appPageTitle(descriptor)
}

// installedState checks a receipt-bound program and its workspace shortcut.
// Historical plans identify the installation by native program identity.
func (w workspaceRegistrationPolicy) installedState(ctx context.Context, apps setupApplications, program string, shortcutCurrent func() bool) (state, note string) {
	registered, found, err := observeRegisteredApplication(ctx, apps, w.registration)
	switch {
	case err != nil:
		return "", ""
	case !found:
		return "unregistered", ""
	case !identity.SameSubjectProgram(registered.Program, program):
		if w.replaceable(registered) {
			return "stale", ""
		}
		return "other", workspaceConflictSummary(registered, fwire.ApplicationDescriptor{Name: w.registration, Program: program})
	case !shortcutCurrent():
		return "no_shortcut", ""
	}
	return "ready", ""
}

// status is the explicit Registry confirmation used after the workspace
// route opens the installed recipe. It matches the normalized descriptor.
func (w workspaceRegistrationPolicy) status(ctx context.Context, apps setupApplications, descriptor fwire.ApplicationDescriptor, shortcutCurrent func() bool) workspaceApplicationReply {
	registered, found, err := observeRegisteredApplication(ctx, apps, w.registration)
	if err != nil {
		return workspaceApplicationReply{Available: true, Reason: err.Error()}
	}
	if found && !applicationDisplayEqual(registered, descriptor) {
		if registered.Program != descriptor.Program && w.replaceable(registered) {
			// Register replaces Setup's own or a stale OpenAbstractions entry.
			return workspaceApplicationReply{Available: true, Reason: "Register the installed application; it replaces the earlier OpenAbstractions entry."}
		}
		return w.conflictReply(registered, descriptor)
	}
	shortcut := shortcutCurrent()
	return workspaceApplicationReply{Available: true, Registered: found, Readable: true, Matching: found, Shortcut: &shortcut, Application: descriptor.Name, Reason: "Open this application from Apps. Registration does not establish engine or integration readiness."}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// An official package binds the Panel to a selected Setup operation owner
// and its compiled compatibility declarations.
type officialPackagePanel struct {
	extension      setup.OfficialExtension
	spec           product.PackageSpec
	legacy         product.LegacyPackageSpec
	download       string
	afterInstall   func(context.Context, setup.Journal) error
	retainedApp    setup.AppID
	retainedTitle  string
	retainedLegacy product.LegacyPackageSpec
	archive        setup.RetainedArchive

	discover            func() []setup.ExtensionHost
	desktopToFinish     func() (setup.ExtensionHost, bool)
	processes           func(setup.ExtensionHost) ([]uint32, error)
	entryKept           func(*servicePanel, context.Context, string) (bool, bool)
	connectPackage      func(context.Context, setup.Catalog, setup.ExtensionHost, setup.PackageConnection) (bool, error)
	reconnect           func(context.Context, setup.Catalog, setup.ExtensionHost) error
	register            func(context.Context, string) error
	hold                func(context.Context, setup.Catalog, string) (setup.HeldProgram, error)
	open                func(setup.ExtensionHost) error
	claimName           string
	claim               func() (func(), bool, error)
	recordFile          func() (string, error)
	installPinned       func(context.Context, setup.Catalog, string, func() error) (func(context.Context) error, error)
	installPackage      func(context.Context, string, string) error
	installSavedPackage func(context.Context, string, string, func() error) error
	publishPinned       func(context.Context, setup.Catalog) error
	keepPrevious        func(context.Context) (string, string, error)
}

func newOfficialPackagePanel(id setup.AppID, retained product.PackageSpec, download string, afterInstall func(context.Context, setup.Journal) error) *officialPackagePanel {
	spec, ok := product.PackageDeclaration(string(id))
	if !ok {
		spec = product.PackageSpec{ID: string(id), Registration: string(id), Title: setup.ApplicationName(id)}
	}
	legacy, ok := product.LegacyPackageDeclaration(spec.ID)
	if !ok {
		panic("the compiled picture package lacks its required compatibility declaration")
	}
	retainedApp := setup.AppID(retained.ID)
	retainedLegacy, ok := product.LegacyPackageDeclaration(retained.ID)
	if !ok {
		panic("the compiled picture workspace lacks its required compatibility declaration")
	}
	pkg := &officialPackagePanel{
		extension: setup.OfficialExtensionFor(id), spec: spec, legacy: legacy, download: download,
		afterInstall: afterInstall, claimName: legacy.MaintenanceMutex,
		retainedApp: retainedApp, retainedTitle: retained.Title, retainedLegacy: retainedLegacy, archive: setup.RetainedArchiveFor(retainedApp),
	}
	pkg.discover = func() []setup.ExtensionHost { return pkg.extension.Find(pkg.extension.AccountFolders()) }
	pkg.desktopToFinish = func() (setup.ExtensionHost, bool) {
		return pkg.extension.DesktopToFinish(pkg.extension.AccountFolders())
	}
	pkg.processes = pkg.extension.Processes
	pkg.entryKept = func(p *servicePanel, ctx context.Context, program string) (bool, bool) {
		return pkg.target(program).kept(ctx, p)
	}
	pkg.connectPackage = pkg.extension.Connect
	pkg.reconnect = pkg.extension.Reconnect
	pkg.register = pkg.extension.Register
	pkg.hold = pkg.extension.HoldConnector
	pkg.open = func(host setup.ExtensionHost) error { return setup.OpenExtensionHost(host, pkg.spec.Title) }
	pkg.claim = func() (func(), bool, error) { return claimNamed(pkg.claimName) }
	pkg.recordFile = func() (string, error) { return setup.PackageMaintenancePath(pkg.spec.ID) }
	pkg.installPinned = pkg.extension.InstallPinnedPackage
	pkg.installPackage = pkg.extension.InstallPackage
	pkg.installSavedPackage = pkg.extension.InstallSavedPackage
	pkg.publishPinned = pkg.extension.PublishPinnedPackage
	pkg.keepPrevious = pkg.extension.KeepPreviousPackage
	return pkg
}

var officialPicture = func() *officialPackagePanel {
	retained, ok := product.RetainedArchiveDeclaration()
	if !ok {
		panic("the compiled picture workspace lacks its retained archive")
	}
	id := setup.AppID(product.ExtensionSetupFacts().PackageID)
	return newOfficialPackagePanel(id, retained, product.ExtensionSetupDownloadPage, setup.SyncRetainedArchiveModelPaths)
}()
var officialPictureAppTitle = officialPicture.spec.Title

type setupRetainedPicture struct {
	ID           string `json:"id"`
	Component    string `json:"component"`
	Title        string `json:"title"`
	RemovalTitle string `json:"removal_title"`
}

type setupPictureApplication struct {
	ID          setup.AppID          `json:"id"`
	Title       string               `json:"title"`
	Actions     map[string]string    `json:"actions"`
	ConnectStep string               `json:"connect_step"`
	Retained    setupRetainedPicture `json:"retained"`
	Publisher   string               `json:"publisher"`
	License     string               `json:"license"`
	Source      string               `json:"source"`
	Finish      string               `json:"finish"`
	setupPackageCardFacts
}

func officialPictureCatalogID() setup.AppID {
	return setup.AppID(officialPicture.spec.ID)
}

// pictureWorkspaceScript keeps historical plan execution independent of the
// live discovery read. Its paths are the same ones the adapter registers.
func pictureWorkspaceScript() string {
	facts := struct {
		Application setup.AppID `json:"application"`
		Title       string      `json:"title"`
		Status      string      `json:"status"`
		Register    string      `json:"register"`
		Download    string      `json:"download"`
	}{
		Application: officialPicture.retainedApp, Title: officialPicture.retainedTitle,
		Status: officialPicture.retainedLegacy.Routes.Status, Register: officialPicture.retainedLegacy.Routes.Register, Download: officialPicture.download,
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		panic(err)
	}
	return "<script>var oaPictureWorkspace=" + string(raw) + ";</script>"
}

func (p *servicePanel) readPictureApplication(ctx context.Context, catalog setup.Catalog, journal setup.Journal) setupPictureApplication {
	return officialPicture.applicationCard(p, ctx, catalog, journal)
}

func (pkg *officialPackagePanel) applicationCard(p *servicePanel, ctx context.Context, catalog setup.Catalog, journal setup.Journal) setupPictureApplication {
	row := setupPictureApplication{ID: setup.AppID(pkg.spec.ID), Title: pkg.spec.Title, setupPackageCardFacts: pkg.cardFacts(p, ctx, catalog, journal)}
	component, registration := pkg.archive.Component(), pkg.archive.Registration()
	display := pkg.legacy.Display
	retainedDisplay := pkg.retainedLegacy.Display
	row.Retained = setupRetainedPicture{ID: registration + "-oa", Component: component, Title: retainedDisplay.RetainedTitle, RemovalTitle: retainedDisplay.RetainedRemovalTitle}
	row.Actions = map[string]string{
		"connect": pkg.legacy.Routes.Connect, "update": pkg.legacy.Routes.Connect, "open": pkg.legacy.Routes.Open, "folder": pkg.legacy.Routes.Folder,
		"remove-oa": pkg.retainedLegacy.Routes.RemoveRetained,
	}
	row.ConnectStep = pkg.legacy.ConnectStep
	row.Publisher, row.License, row.Source, row.Finish = display.Publisher, display.License, display.Source, display.Finish
	return row
}

// outdated reports whether found's integration came from an earlier
// OpenAbstractions. A connector it cannot read counts as current, with a
// log line.
func (pkg *officialPackagePanel) outdated(catalog setup.Catalog, found setup.ExtensionHost) bool {
	if !found.Connected {
		return false
	}
	outdated, err := pkg.extension.Outdated(catalog, found.Base)
	if err != nil {
		log.Printf("setup: %s's integration could not be compared with this build's: %v", pkg.spec.Title, err)
		return false
	}
	return outdated
}

// restartPending reports whether a host process that ran during
// Update still runs, and forgets the processes once none does.
func (pkg *officialPackagePanel) restartPending(p *servicePanel, found setup.ExtensionHost) bool {
	p.mu.Lock()
	ran := p.packageRestarts[pkg.spec.ID]
	p.mu.Unlock()
	if len(ran) == 0 {
		return false
	}
	running, err := pkg.processes(found)
	if err != nil {
		log.Printf("setup: %s's programs: %v", pkg.spec.Title, err)
		return false
	}
	for _, pid := range running {
		if slices.Contains(ran, pid) {
			return true
		}
	}
	p.mu.Lock()
	delete(p.packageRestarts, pkg.spec.ID)
	p.mu.Unlock()
	return false
}

// cardFacts adapts the account's existing discovery and Setup receipts
// to the shared Catalog card. It performs no Connect or Open operation.
func (pkg *officialPackagePanel) cardFacts(p *servicePanel, ctx context.Context, catalog setup.Catalog, journal setup.Journal) setupPackageCardFacts {
	_, pinned, pinErr := pkg.extension.Revision(catalog)
	found, present := pkg.primary()
	host := packageCardHost{
		kind: found.Kind, folder: found.Base, canOpen: found.Program != "", integrated: found.Connected,
		kept:           func() (bool, bool) { return pkg.entryKept(p, ctx, pkg.extension.Connector(found.Base)) },
		changed:        func() bool { return pkg.readRecord().Changed },
		outdated:       func() bool { return pkg.outdated(catalog, found) },
		updateBlocked:  func() string { return pkg.extension.PackageMissing(catalog) },
		restartPending: func() bool { return pkg.restartPending(p, found) },
	}
	pending := func() (string, bool) {
		desktop, ok := pkg.desktopToFinish()
		return desktop.Kind, ok
	}
	retained := func() (installed, removing bool) {
		state, err := journal.Load(ctx)
		if err != nil {
			log.Printf("setup: %s's OA build: %v", pkg.spec.Title, err)
			return false, false
		}
		_, effect, ok := pkg.archive.Installed(state)
		return ok, ok && effect.Removing
	}
	return presentPackageCard(pkg.target(""), pkg.download, pinned, pinErr == nil, host, present, pending, retained)
}

// extensionConnectTimeout bounds Connect: the MSI's own setup script ends
// within a minute.
var extensionConnectTimeout = 10 * time.Minute

// picturePackageIdentity gives registry callers the same registration and
// title the Catalog card presents.
func picturePackageIdentity() (registration, title string) {
	return officialPicture.spec.Registration, officialPicture.spec.Title
}

func (pkg *officialPackagePanel) primary() (setup.ExtensionHost, bool) {
	return setup.PrimaryExtensionHost(pkg.discover())
}

func (pkg *officialPackagePanel) target(program string) packageConnectTarget {
	return packageConnectTarget{registration: pkg.spec.Registration, program: program, title: pkg.spec.Title}
}

func (p *servicePanel) packageUpdateUndo() packageUpdateUndo {
	return officialPicture.updateUndo(p)
}

func (pkg *officialPackagePanel) updateUndo(p *servicePanel) packageUpdateUndo {
	return packageUpdateUndo{
		registration: pkg.spec.Registration,
		read:         pkg.readRecord,
		restore: func(w http.ResponseWriter, ctx context.Context, at int64, line applicationActivity) {
			pkg.undoUpdate(p, w, ctx, at, line)
		},
	}
}

func (pkg *officialPackagePanel) expectConnect(p *servicePanel, ctx context.Context, catalog setup.Catalog, connector string) {
	digest, _, err := pkg.extension.Revision(catalog)
	if err != nil {
		log.Printf("setup: %s integration connect: %v", pkg.spec.Title, err)
		return
	}
	p.recordPackageConnectExpectation(ctx, pkg.target(connector), digest)
}

func (pkg *officialPackagePanel) connect(p *servicePanel, ctx context.Context, catalog setup.Catalog, found setup.ExtensionHost) (bool, error) {
	digest, _, err := pkg.extension.Revision(catalog)
	if err != nil {
		return true, err
	}
	connector := pkg.extension.Connector(found.Base)
	return p.connectCatalogPackage(ctx, catalogPackageConnect{
		target: pkg.target(connector),
		digest: digest,
		kept: func(ctx context.Context) (bool, bool) {
			return pkg.entryKept(p, ctx, connector)
		},
		install: func(ctx context.Context, ops setup.PackageConnection) (bool, error) {
			return pkg.connectPackage(ctx, catalog, found, ops)
		},
		connectLegacy: func(ctx context.Context) error { return pkg.reconnect(ctx, catalog, found) },
		register: func(ctx context.Context, program string) error {
			return pkg.register(ctx, program)
		},
		hold: func(ctx context.Context) (setup.HeldProgram, error) {
			return pkg.hold(ctx, catalog, found.Base)
		},
		receipt: setup.PackageConnection{
			ExistingReceipt: pkg.readRecord().Connection,
			Save:            pkg.saveConnection,
			Publish:         pkg.publishConnection,
			Commit:          pkg.commitConnection,
		},
	})
}

// serveConnect starts the selected integration MSI as a Setup step.
func (pkg *officialPackagePanel) serveConnect(p *servicePanel, w http.ResponseWriter, r *http.Request) {
	if !decodeSetupConfirm(w, r) || !p.setupTryStart(w) {
		return
	}
	started := false
	defer func() {
		if !started {
			p.setupActionMu.Unlock()
		}
	}()
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	found, ok := pkg.primary()
	if !ok {
		http.Error(w, pkg.legacy.Copy.HostMissing, http.StatusConflict)
		return
	}
	release, acquired, err := pkg.claim()
	if err != nil || !acquired {
		http.Error(w, "An integration change is already in progress. Try again.", http.StatusConflict)
		return
	}
	defer func() {
		if !started {
			release()
		}
	}()
	if pkg.readRecord().Undo != nil {
		http.Error(w, "Finish the pending Undo before connecting the integration.", http.StatusConflict)
		return
	}
	catalog := journal.Catalog()
	// Connect updates a kept integration from an earlier release.
	kind := "connect"
	if pkg.outdated(catalog, found) {
		if kept, known := pkg.entryKept(p, r.Context(), pkg.extension.Connector(found.Base)); kept || !known {
			kind = "update"
		}
	}
	stepID := p.startPackageStep(pkg.legacy.ConnectStep, kind, "connecting stopped unexpectedly", extensionConnectTimeout, false, release,
		func(ctx context.Context, _ func(int, int)) error {
			if handled, err := pkg.connectKept(p, ctx, journal, found); handled {
				return err
			}
			transaction, err := pkg.connect(p, ctx, catalog, found)
			if err != nil {
				return err
			}
			// The integration adds OA's prepared picture model folders at its
			// next start.
			pkg.finishInstall(ctx, journal)
			if kind == "update" {
				pkg.tellUpdated(p, found, 0, false)
			}
			if !transaction {
				// The record follows the connector Connect installed and kept.
				if digest, _, err := pkg.extension.Revision(catalog); err == nil && digest != "" {
					if outdated, err := pkg.extension.Outdated(catalog, found.Base); err == nil && !outdated {
						pkg.changeRecord(ctx, func(r *setup.PackageMaintenanceRecord) {
							r.RecordKeptConnector(digest)
							r.Declined = ""
						})
					}
				}
			}
			return nil
		},
		func(_ context.Context, failure error, outcome *setupOutcome) {
			if failure != nil {
				log.Printf("setup: %s %s: %v", kind, pkg.spec.Title, failure)
				outcome.Failure, outcome.Detail = removalReason(failure), failure.Error()
			}
		},
	)
	started = true
	panelJSON(w, map[string]any{"actions": p.setupActionsNow(), "outcomes": p.setupOutcomesNow(), "step_id": stepID})
}

// tellUpdated adds Update's one Activity line. A running host
// keeps the earlier integration until it restarts: the line says so, and its
// row says so while those processes run.
func (pkg *officialPackagePanel) tellUpdated(p *servicePanel, found setup.ExtensionHost, at int64, restores bool) {
	running, err := pkg.processes(found)
	if err != nil {
		log.Printf("setup: %s's programs: %v", pkg.spec.Title, err)
	}
	p.mu.Lock()
	if p.packageRestarts == nil {
		p.packageRestarts = make(map[string][]uint32)
	}
	p.packageRestarts[pkg.spec.ID] = running
	p.mu.Unlock()
	var undoRegistration string
	if restores {
		undoRegistration = pkg.spec.Registration
	}
	target := pkg.target(pkg.extension.Connector(found.Base))
	p.tellPackageUpdated(target, at, len(running) > 0, undoRegistration)
}

// oaBuildRemoval is the removal step; tests replace it.
var oaBuildRemoval = (*officialPackagePanel).removeRetainedArchive

// setupRemoveOABuild starts the OA build's removal as a Setup step with
// Cancel: its registration, its files and its Start menu shortcut. The application's
// user folder, its models and the person's own files stay.
func (pkg *officialPackagePanel) serveRemoveRetained(p *servicePanel, w http.ResponseWriter, r *http.Request) {
	if !decodeSetupConfirm(w, r) || !p.setupTryStart(w) {
		return
	}
	started := false
	defer func() {
		if !started {
			p.setupActionMu.Unlock()
		}
	}()
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	state, err := journal.Load(r.Context())
	if err != nil {
		panelError(w, err)
		return
	}
	archive := pkg.archive
	if _, _, ok := archive.Installed(state); !ok {
		http.Error(w, pkg.retainedLegacy.Copy.RetainedNotInstalled, http.StatusConflict)
		return
	}
	component, name := archive.Component(), archive.Registration()
	stepID := p.startPackageStep(component, "remove", "the removal stopped unexpectedly", nativeUninstallTimeout, true, nil,
		func(ctx context.Context, progress func(int, int)) error {
			return oaBuildRemoval(pkg, p, setup.WithProgress(ctx, progress), runner)
		},
		func(ctx context.Context, failure error, outcome *setupOutcome) {
			switch {
			case failure == nil:
				clearRemovalFailure(journal, name)
			case errors.Is(failure, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
				outcome.Cancelled = true
			default:
				log.Printf("setup: remove the OA build of %s: %v", pkg.retainedTitle, failure)
				outcome.Failure, outcome.Detail = removalReason(failure), failure.Error()
				recordRemovalFailure(journal, name, pkg.retainedLegacy.Display.RetainedRemovalTitle, failure)
			}
		},
	)
	started = true
	panelJSON(w, map[string]any{"actions": p.setupActionsNow(), "outcomes": p.setupOutcomesNow(), "step_id": stepID})
}

func (pkg *officialPackagePanel) removeRetainedArchive(p *servicePanel, ctx context.Context, runner setup.Runner) error {
	archive := pkg.archive
	if err := archive.Remove(ctx, runner.Journal, func(ctx context.Context, a setup.Artifact, root string) error {
		return p.unregisterNativeInstallation(ctx, archive.Registration(), a, root)
	}); err != nil {
		return err
	}
	if err := pkg.retainedShortcut().remove(); err != nil {
		log.Printf("setup: %s Start menu shortcut: %v", pkg.retainedTitle, err)
	}
	return nil
}

// settled reports this build's integration record settled:
// nothing to update or record, or a build that pins no integration.
func (pkg *officialPackagePanel) settled(journal setup.Journal) bool {
	r := pkg.readRecord()
	if r.Undo != nil && r.Undo.Replacement.InstallComplete && r.Undo.Replacement.After != "" {
		return false
	}
	build, _, err := pkg.extension.Revision(journal.Catalog())
	if err != nil {
		return false
	}
	if build == "" {
		return true
	}
	if r.Seen != build || r.PendingUpdate != nil || r.Recorded != build {
		return false
	}
	if r.KeptPackageSHA256 != "" {
		digest, err := pkg.extension.PackageDigest(journal.Catalog())
		if err != nil || digest != r.KeptPackageSHA256 {
			return false
		}
	}
	// A connector at this build's pin whose entry OA has not re-pinned to it
	// waits for healPin.
	return r.Kept == build || r.Declined == build || !pkg.connectorAtPin(journal.Catalog())
}

// connectorAtPin reports the selected host's connector on disk at
// this build's pin.
func (pkg *officialPackagePanel) connectorAtPin(catalog setup.Catalog) bool {
	found, ok := pkg.primary()
	if !ok || !found.Connected {
		return false
	}
	outdated, err := pkg.extension.Outdated(catalog, found.Base)
	return err == nil && !outdated
}

// closed reports no program of the selected host running.
func (pkg *officialPackagePanel) closed() bool {
	found, ok := pkg.primary()
	if !ok {
		return true
	}
	running, err := pkg.processes(found)
	return err == nil && len(running) == 0
}

func (pkg *officialPackagePanel) maintenance(journal setup.Journal, claim func() (func(), bool, error), pass func(context.Context, setup.Journal)) extensionMaintenance {
	return extensionMaintenance{
		title:   pkg.spec.Title,
		claim:   claim,
		settled: func() bool { return pkg.settled(journal) },
		idle:    pkg.closed,
		pass:    func(ctx context.Context) { pass(ctx, journal) },
	}
}

// maintainSetupExtensions runs the configured extension maintenance once
// after the Setup step, using the same claim as the presenter.
func (p *servicePanel) maintainSetupExtensions(ctx context.Context) {
	pkg := officialPicture
	journal, err := p.setupJournalForPanel()
	if err != nil {
		log.Printf("setup: %s integration update: %v", pkg.spec.Title, err)
		return
	}
	pkg.maintenance(journal, pkg.claim, func(ctx context.Context, journal setup.Journal) { pkg.maintain(p, ctx, journal) }).tryPass(ctx, false)
}

// The historical routes stay bound to the original workspace and archive.
func officialPictureSetupExtension() setupExtensionAdapter {
	pkg := officialPicture
	routes := pkg.legacy.Routes
	workspace := pkg.retainedLegacy.Routes
	return setupExtensionAdapter{
		maintenance: pkg.backgroundMaintenance,
		routes: []setupExtensionRoute{
			{workspace.Status, pkg.serveWorkspaceStatus},
			{workspace.Register, pkg.serveWorkspaceRegister},
			{workspace.Activate, pkg.serveWorkspaceActivate},
			{workspace.Remove, pkg.serveWorkspaceRemove},
			{routes.Connect, pkg.serveConnect},
			catalogPackageOpenRoute(routes.Open, pkg.target(""), pkg.primary,
				func() (setup.ExtensionHost, bool) { return pkg.desktopToFinish() },
				func(host setup.ExtensionHost) error { return pkg.open(host) }),
			catalogPackageFolderRoute(routes.Folder, pkg.extension.SaveChosenFolder),
			{workspace.RemoveRetained, pkg.serveRemoveRetained},
		},
	}
}

func (pkg *officialPackagePanel) finishInstall(ctx context.Context, journal setup.Journal) {
	if pkg.afterInstall != nil {
		if err := pkg.afterInstall(ctx, journal); err != nil {
			log.Printf("setup: %s model folders: %v", pkg.spec.Title, err)
		}
	}
}

func (pkg *officialPackagePanel) backgroundMaintenance(journal setup.Journal) extensionMaintenance {
	return pkg.maintenance(journal, pkg.claim, func(ctx context.Context, journal setup.Journal) {
		p := new(servicePanel)
		pkg.maintain(p, ctx, journal)
		drain, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		p.log.close(drain)
	})
}

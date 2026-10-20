//go:build windows

package main

import (
	"context"
	"errors"
	"log"
	"os"
	"path/filepath"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

const (
	desktopUpgradeSettled = 0
	desktopUpgradeRunning = 20
	desktopUpgradeLater   = 21
	desktopUpgradeRefused = 22
)

type desktopUpgradeMode uint8

const (
	desktopUpgradeApply desktopUpgradeMode = iota
	desktopUpgradeDefer
	desktopUpgradeResume
)

const desktopUpgradeClaim = "Local\\OpenAbstractions.SetupDesktopIntegration"

func claimDesktopUpgrade() (func(), bool, error) { return claimNamed(desktopUpgradeClaim) }

var desktopUpgradeKept = func(ctx context.Context, p *servicePanel, app setup.AppID) (bool, error) {
	host, ok := setup.OfficialProgramOf(app)
	if !ok {
		return false, errors.New("the desktop app has no official host")
	}
	local, err := layout.LocalAppData()
	if err != nil {
		return false, err
	}
	expected := filepath.Join(local, filepath.FromSlash(host.LocalAppData))
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return false, err
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return false, err
	}
	if page.Outcome != fwire.ApplicationOutcomePage {
		return false, errors.New("the installed integration could not be checked")
	}
	for _, entry := range page.Applications {
		if entry.Descriptor.Name != string(app) || entry.SelfRegistered || entry.Scope != fwire.ScopeLocal {
			continue
		}
		same, err := identity.SameFile(expected, entry.Descriptor.Program)
		if err != nil || !same {
			return false, err
		}
		state, present, known := readPackageRegistrationState(ctx, packageConnectTarget{registration: string(app)})
		if !known {
			return false, errors.New("the integration's registration state could not be checked")
		}
		return !present || state == registration.StateEnabled, nil
	}
	return false, nil
}

var desktopUpgradeBusy = setup.DesktopIntegration.Running
var stageDesktopUpgrade = setup.DesktopIntegration.Stage
var applyKeptDesktopUpgrade = setup.DesktopIntegration.ApplyKeptUpdate
var applyStagedDesktopUpgrade = setup.DesktopIntegration.ApplyStaged

func runInstallerDesktopUpgrade(later bool) int {
	if elevated, err := processElevated(); err != nil || elevated {
		return desktopUpgradeRefused
	}
	home, err := setup.CurrentHome()
	if err != nil || home.Isolated {
		return desktopUpgradeRefused
	}
	ctx, cancel := context.WithTimeout(context.Background(), desktopIntegrationTimeout)
	defer cancel()
	release, acquired, err := claimDesktopUpgrade()
	if err != nil || !acquired {
		return desktopUpgradeRefused
	}
	defer release()
	p := new(servicePanel)
	journal, err := p.setupJournalForPanel()
	if err != nil {
		log.Printf("setup: maintain desktop integrations: %v", err)
		return desktopUpgradeRefused
	}
	mode := desktopUpgradeApply
	if later {
		mode = desktopUpgradeDefer
	}
	code := p.upgradeDesktopIntegration(ctx, journal.Catalog().DesktopIntegration, home.Config, mode)
	if code == desktopUpgradeRunning {
		return code
	}
	return p.finishIntegrationUpgrades(ctx, journal, code)
}

// The staged MSI itself preserves the exact approved digest across Setup and
// the presenter. A later build derives a different directory from its pin.
func (p *servicePanel) upgradeDesktopIntegration(ctx context.Context, pin *setup.DesktopIntegration, config string, mode desktopUpgradeMode) int {
	if pin == nil {
		return desktopUpgradeSettled
	}
	dir := filepath.Join(config, "deferred-desktop-integration-"+pin.SHA256)
	staged := filepath.Join(dir, pin.File)
	attempted := false
	clear := func() { _ = pin.ClearStaged(dir) }
	fail := func(err error) int {
		log.Printf("setup: %s integration maintenance: %v", pin.Title, err)
		if attempted {
			clear()
		}
		if mode != desktopUpgradeResume {
			p.appActivity.add(applicationActivity{App: pin.Title, What: "The OpenAbstractions integration update did not finish.", Outcome: "Refused"})
		}
		return desktopUpgradeRefused
	}
	if mode == desktopUpgradeResume {
		if _, err := os.Lstat(staged); errors.Is(err, os.ErrNotExist) {
			return desktopUpgradeSettled
		} else if err != nil {
			return fail(err)
		}
	}
	action, err := readCatalogDesktopPackage(*pin)
	if err != nil {
		return fail(err)
	}
	if action != "update" {
		clear()
		return desktopUpgradeSettled
	}
	if err := verifyCatalogDesktopHost(pin.App); err != nil {
		return fail(err)
	}
	kept, err := desktopUpgradeKept(ctx, p, pin.App)
	if err != nil {
		return fail(err)
	}
	if !kept {
		clear()
		return desktopUpgradeSettled
	}
	transfer := setup.PublicTransfer{Machine: panelMachine(), LocalOnly: true}
	if mode == desktopUpgradeDefer {
		if err := stageDesktopUpgrade(*pin, ctx, dir, transfer); err != nil {
			return fail(err)
		}
		p.appActivity.add(applicationActivity{App: pin.Title, What: "The integration will update after you close the app.", Outcome: "Deferred"})
		return desktopUpgradeLater
	}
	running, err := desktopUpgradeBusy(*pin)
	if err != nil {
		return fail(err)
	}
	if running {
		return desktopUpgradeRunning
	}
	keep := func(ctx context.Context) error {
		if err := verifyCatalogDesktopHost(pin.App); err != nil {
			return err
		}
		kept, err := desktopUpgradeKept(ctx, p, pin.App)
		if err != nil {
			return err
		}
		if !kept {
			return errors.New("the owner disconnected this integration before the update")
		}
		return nil
	}
	if mode == desktopUpgradeResume {
		attempted = true
		err = applyStagedDesktopUpgrade(*pin, ctx, dir, keep)
	} else {
		err = applyKeptDesktopUpgrade(*pin, ctx, transfer, keep)
	}
	if errors.Is(err, setup.ErrDesktopIntegrationRunning) {
		return desktopUpgradeRunning
	}
	if err != nil {
		return fail(err)
	}
	clear()
	p.appActivity.add(applicationActivity{App: pin.Title, What: "Updated the OpenAbstractions integration.", Outcome: "Updated"})
	return desktopUpgradeSettled
}

// Use the same picture integration maintenance as normal Finish and the presenter.
// A pending, running package is Deferred. Unknown state and replacement
// errors remain visible before Setup closes.
func (p *servicePanel) finishIntegrationUpgrades(ctx context.Context, journal setup.Journal, desktopResult int) int {
	pkg := officialPicture
	catalog := journal.Catalog()
	build, _, err := pkg.extension.Revision(catalog)
	fail := func(detail error) int {
		log.Printf("setup: %s integration maintenance: %v", pkg.spec.Title, detail)
		p.appActivity.add(applicationActivity{App: pkg.spec.Title, What: "The OpenAbstractions integration update could not be confirmed.", Outcome: "Refused"})
		return desktopUpgradeRefused
	}
	if err != nil {
		return fail(err)
	}
	if build == "" {
		return desktopResult
	}
	digest, err := pkg.extension.PackageDigest(catalog)
	if err != nil {
		return fail(err)
	}
	// The presenter may have started the same maintenance as Setup. Give it
	// a short chance to settle before judging the common receipt.
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		release, acquired, err := pkg.claim()
		if err != nil {
			return fail(err)
		}
		if acquired {
			defer release()
			break
		}
		select {
		case <-wait.Done():
			return fail(errors.New("another application update is still running"))
		case <-tick.C:
		}
	}
	found, exists := pkg.primary()
	observed := pkg.observeScheduledUpdate(p, ctx, catalog, found, exists)
	if !observed.Known {
		return fail(errors.New("the kept integration could not be checked"))
	}
	if !observed.Kept {
		return desktopResult
	}
	pkg.maintain(p, ctx, journal)
	record := pkg.readRecord()
	found, exists = pkg.primary()
	observed = pkg.observeScheduledUpdate(p, ctx, catalog, found, exists)
	if !observed.Known {
		return fail(errors.New("the integration's final registration could not be checked"))
	}
	if !observed.Kept || record.Declined == build {
		return desktopResult
	}
	packageCurrent := record.KeptPackageSHA256 == "" || record.KeptPackageSHA256 == digest
	if !observed.Outdated && packageCurrent && record.PendingUpdate == nil && record.Kept == build {
		return desktopResult
	}
	if pending := record.PendingUpdate; pending != nil && pending.SHA256 == build && pending.Replacement == nil && pending.MatchesProgram(observed.Program) {
		processes, err := pkg.processes(found)
		if err == nil && len(processes) != 0 {
			if err := pkg.stageMaintainedPackage(p, ctx, catalog); err != nil {
				return fail(err)
			}
			p.appActivity.add(applicationActivity{App: pkg.spec.Title, What: "The integration will update after you close the app.", Outcome: "Deferred"})
			if desktopResult == desktopUpgradeRefused {
				return desktopResult
			}
			return desktopUpgradeLater
		}
	}
	return fail(errors.New("the kept integration update did not finish"))
}

func startDesktopIntegrationDuty(lifetime context.Context) {
	go func() {
		home, err := setup.CurrentHome()
		if err != nil || home.Isolated {
			return
		}
		journal, err := setup.DefaultJournal()
		if err != nil {
			return
		}
		pin := journal.Catalog().DesktopIntegration
		if pin == nil {
			return
		}
		p := new(servicePanel)
		refused := false
		duty := extensionMaintenance{title: pin.Title, claim: claimDesktopUpgrade,
			settled: func() bool { return lifetime.Err() != nil },
			idle:    func() bool { return refused },
			pass: func(ctx context.Context) {
				ctx, cancel := context.WithTimeout(ctx, desktopIntegrationTimeout)
				defer cancel()
				result := p.upgradeDesktopIntegration(ctx, pin, home.Config, desktopUpgradeResume)
				if result == desktopUpgradeRefused && !refused {
					p.appActivity.add(applicationActivity{App: pin.Title, What: "The deferred integration update could not be completed.", Outcome: "Refused"})
				}
				refused = result == desktopUpgradeRefused
			},
		}
		duty.duty(lifetime)
	}()
}

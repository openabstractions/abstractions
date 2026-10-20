package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// A kept integration updates with OpenAbstractions (owner decisions
// 2026-11-03 and 2026-11-11 point 4, confirmed by the architect on
// 2026-11-12). The person's Keep of the integration authorizes OA's
// same-source, non-widening updates of it within six bounds:
//
//  1. The same application, account and binding: the connector at the kept
//     entry's own program path in this account's installation.
//  2. The same authenticated OA source: the extension MSI this build pins,
//     checked by its SHA-256, and the connector bytes this build pins after
//     it installs. A connector pin OA installed before a later one is a
//     downgrade and refuses.
//  3. No rights wider than kept: the update is the installing operator's
//     re-pin (architect ruling 2026-11-10), which keeps the entry's state,
//     title and grants; a new right still goes to the person.
//  4. A person's disconnect wins: an entry removed, waiting or turned off
//     gets no expectation and its folder is left alone.
//  5. Act, tell, Undo: one Activity line, whose Undo installs the earlier
//     integration again and re-pins the entry to it while it is kept.
//  6. An application without a prior connection stays unconnected.
//
// The Setup step writes the expectation: the step the installer's Finish
// runs after an OA upgrade, and the one each sign-in runs. The expectation
// names the app, its connector path and the connector SHA-256 this build
// pins. It lives until the integration registers its new connector, which
// the step completes once the application is closed, or until the next OA
// upgrade replaces the pin. It is used once. The step leaves the folder
// alone while the application runs.

func (pkg *officialPackagePanel) readRecord() setup.PackageMaintenanceRecord {
	path, err := pkg.recordFile()
	if err == nil {
		var record setup.PackageMaintenanceRecord
		record, err = (setup.PackageMaintenanceStore{Path: path}).Load()
		if err == nil {
			return record
		}
	}
	log.Printf("setup: %s integration record: %v", pkg.spec.Title, err)
	return setup.PackageMaintenanceRecord{}
}

func (pkg *officialPackagePanel) changeRecord(ctx context.Context, change func(*setup.PackageMaintenanceRecord)) error {
	return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
		change(r)
		return nil
	})
}

func (pkg *officialPackagePanel) editRecord(ctx context.Context, change func(*setup.PackageMaintenanceRecord) error) error {
	path, err := pkg.recordFile()
	if err == nil {
		err = (setup.PackageMaintenanceStore{Path: path}).Edit(ctx, change)
	}
	if err != nil {
		log.Printf("setup: %s integration record: %v", pkg.spec.Title, err)
	}
	return err
}

func (pkg *officialPackagePanel) saveConnection(ctx context.Context, before, after *setup.PackageConnectionReceipt) error {
	return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
		return r.SaveConnection(before, after)
	})
}

func (pkg *officialPackagePanel) publishConnection(ctx context.Context, prepared setup.PackageConnectionReceipt, publish func(context.Context) error) error {
	return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
		return r.PublishConnection(ctx, prepared, publish)
	})
}

func (pkg *officialPackagePanel) commitConnection(ctx context.Context, installed setup.PackageConnectionReceipt, publish func(context.Context) error) error {
	return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
		return r.CommitConnection(ctx, installed, publish)
	})
}

// An older expectation can leave Application unspecified. Its existing
// program, digest and revision checks still apply. A named expectation
// belongs only to the registration it records.
func (pkg *officialPackagePanel) ownsUpdate(pending *setup.PackageUpdateExpectation) bool {
	return pending == nil || pending.Application == "" || pending.Application == pkg.spec.Registration
}

// Read the selected package's local registration even when its image is
// missing. Unknown replies leave update and repair admission undecided.
var readPackageRegistrationState = func(ctx context.Context, target packageConnectTarget) (registrationwire.State, bool, bool) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return 0, false, false
	}
	rows, err := selectionRegistrationRows(applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).Observe)
	if err != nil {
		return 0, false, false
	}
	for i := range rows {
		if _, _, ok := nativeApplicationFacet(&rows[i], target.registration); ok {
			return rows[i].State, true, true
		}
	}
	return 0, false, true
}

// extensionMaintenanceTimeout bounds one update: the MSI's own setup script
// ends within a minute.
const extensionMaintenanceTimeout = 10 * time.Minute

var maintainedPackageTransfer = func(p *servicePanel) setup.Transfer {
	if p.setupTransfer != nil {
		return *p.setupTransfer
	}
	return setup.PublicTransfer{Machine: panelMachine(), LocalOnly: true}
}

func (pkg *officialPackagePanel) stageMaintainedPackage(p *servicePanel, ctx context.Context, catalog setup.Catalog) error {
	stage, cancel := context.WithTimeout(ctx, extensionMaintenanceTimeout)
	defer cancel()
	return pkg.extension.StagePackage(stage, catalog, maintainedPackageTransfer(p))
}

func (pkg *officialPackagePanel) maintain(p *servicePanel, ctx context.Context, journal setup.Journal) {
	record := pkg.readRecord()
	if record.Connection != nil || !pkg.ownsUpdate(record.PendingUpdate) {
		return
	}
	if record.Undo != nil {
		if previous := record.Previous; previous != nil && record.Undo.Replacement.InstallComplete && record.Undo.Replacement.After != "" {
			found, ok := pkg.primary()
			if ok && found.Connected && pkg.extension.Connector(found.Base) == previous.Program {
				ctx, cancel := context.WithTimeout(ctx, extensionMaintenanceTimeout)
				defer cancel()
				if err := pkg.restorePrevious(p, ctx, found, record, record.Undo.At); err != nil {
					log.Printf("setup: resume Undo of the %s integration update: %v", pkg.spec.Title, err)
					return
				}
				p.appActivity.clearUndo(record.Undo.At)
				p.logAction(ctx, "application.integration_update_undo", map[string]string{"application.name": pkg.spec.Registration})
			}
		}
		return
	}
	catalog := journal.Catalog()
	build, _, err := pkg.extension.Revision(catalog)
	if err != nil {
		log.Printf("setup: %s integration update: %v", pkg.spec.Title, err)
		return
	}
	if build == "" {
		return
	}
	packageDigest, err := pkg.extension.PackageDigest(catalog)
	if err != nil {
		log.Printf("setup: %s integration update: %v", pkg.spec.Title, err)
		return
	}
	if pending := record.PendingUpdate; pending != nil && pending.Replacement != nil {
		found, ok := pkg.primary()
		if pending.SHA256 == build && pending.Replacement.Candidate == packageDigest {
			if !record.Changed && ok && pending.MatchesProgram(pkg.extension.Connector(found.Base)) {
				pkg.applyUpdate(p, ctx, journal, found, pending, record.KeptPackageSHA256, nil)
			}
			return
		}
		if !ok || !pkg.retireSupersededReplacement(p, ctx, found, pending) {
			log.Printf("setup: %s integration update waits: an earlier replacement still requires recovery", pkg.spec.Title)
			return
		}
		record = pkg.readRecord()
		if !pkg.ownsUpdate(record.PendingUpdate) {
			return
		}
	}
	original := record
	record = record.ScheduledUpdateSnapshot(build)
	packageChanged := record.KeptPackageSHA256 != "" && record.KeptPackageSHA256 != packageDigest
	if !packageChanged && record.Seen == build && record.PendingUpdate == nil && record.Recorded == build {
		if record.Kept != build && record.Declined != build {
			pkg.healPin(p, ctx, catalog, build)
		}
		return
	}
	found, ok := pkg.primary()
	if ok && record.PendingUpdate != nil && !record.PendingUpdate.MatchesProgram(pkg.extension.Connector(found.Base)) {
		return
	}
	observation := pkg.observeScheduledUpdate(p, ctx, catalog, found, ok)
	observation.Outdated = observation.Outdated || observation.Kept && packageChanged
	next, done, downgrade := record.PlanScheduledUpdate(build, observation, func() (string, int64) {
		version, _ := setup.ReleaseVersion()
		return version, time.Now().UnixMilli()
	})
	if downgrade {
		log.Printf("setup: %s integration update refused: this OpenAbstractions pins an earlier integration", pkg.spec.Title)
	}
	start := func(ctx context.Context, pending *setup.PackageUpdateExpectation) error {
		return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
			return r.AdmitScheduledUpdate(original, next, pending)
		})
	}
	if record.KeptPackageSHA256 == "" {
		if err := start(ctx, next.PendingUpdate); err != nil {
			return
		}
		start = nil
	}
	saveDecision := func() error {
		if start != nil {
			return start(ctx, next.PendingUpdate)
		}
		return nil
	}
	if !done {
		_ = saveDecision()
		return
	}
	if next.PendingUpdate == nil {
		if err := saveDecision(); err != nil {
			return
		}
		if next.Recorded != build {
			pkg.recordIntegration(p, ctx, catalog, build)
		}
		return
	}
	found, ok = pkg.primary()
	if !ok || !next.PendingUpdate.MatchesProgram(pkg.extension.Connector(found.Base)) {
		_ = saveDecision()
		return
	}
	found.Base = filepath.Clean(found.Base)
	if running, err := pkg.processes(found); err != nil || len(running) > 0 {
		// The host loads its integration on start. Wait for the next step
		// with the host closed.
		if err != nil {
			log.Printf("setup: %s's programs: %v", pkg.spec.Title, err)
		}
		_ = saveDecision()
		return
	}
	if why := pkg.extension.PackageMissing(catalog); why != "" {
		if err := pkg.stageMaintainedPackage(p, ctx, catalog); err != nil {
			log.Printf("setup: %s integration update: %v", pkg.spec.Title, err)
			_ = saveDecision()
			return
		}
	}
	pkg.applyUpdate(p, ctx, journal, found, next.PendingUpdate, record.KeptPackageSHA256, start)
}

func (pkg *officialPackagePanel) retireSupersededReplacement(p *servicePanel, ctx context.Context, found setup.ExtensionHost, pending *setup.PackageUpdateExpectation) bool {
	path, err := pkg.recordFile()
	if err != nil {
		return false
	}
	store := setup.PackageMaintenanceStore{Path: path}
	program := pkg.extension.Connector(found.Base)
	return store.ForgetUntouchedReplacement(ctx, pending, program, func(ctx context.Context) (string, error) {
		_, revision, err := pkg.target(pending.Program).observeMaintainedRevision(ctx, p)
		return revision, err
	}) == nil
}

func (pkg *officialPackagePanel) observeScheduledUpdate(p *servicePanel, ctx context.Context, catalog setup.Catalog, found setup.ExtensionHost, ok bool) setup.PackageUpdateObservation {
	if !ok || !found.Connected {
		return setup.PackageUpdateObservation{Known: true}
	}
	connector := pkg.extension.Connector(found.Base)
	kept, known := pkg.entryKept(p, ctx, connector)
	if !known {
		return setup.PackageUpdateObservation{}
	}
	state, present, known := readPackageRegistrationState(ctx, pkg.target(connector))
	if !known {
		return setup.PackageUpdateObservation{}
	}
	if !kept || !present || state != registrationwire.StateEnabled {
		return setup.PackageUpdateObservation{Known: true}
	}
	outdated, err := pkg.extension.Outdated(catalog, found.Base)
	if err != nil {
		log.Printf("setup: %s integration update: %v", pkg.spec.Title, err)
		return setup.PackageUpdateObservation{}
	}
	return setup.PackageUpdateObservation{Known: true, Kept: true, Outdated: outdated,
		Application: pkg.spec.Registration, Program: connector}
}

// connectKept uses the replacement receipt for an explicit Update.
// First-time and unkept registrations still use the native Connect path.
func (pkg *officialPackagePanel) connectKept(p *servicePanel, ctx context.Context, journal setup.Journal, found setup.ExtensionHost) (bool, error) {
	catalog := journal.Catalog()
	build, _, err := pkg.extension.Revision(catalog)
	if err != nil {
		return true, err
	}
	record := pkg.readRecord()
	connector := pkg.extension.Connector(found.Base)
	pending, resume, err := record.PlanExplicitUpdate(ctx, setup.ExplicitPackageUpdate{
		Application: pkg.spec.Registration, Program: connector, Pin: build, Connected: found.Connected,
		Available: func() bool { return pkg.extension.PackageMissing(catalog) == "" },
		Outdated:  func() (bool, error) { return pkg.extension.Outdated(catalog, found.Base) },
		Image:     func() (string, int64, error) { return pkg.extension.Image(found.Base) },
		EntryKept: func(ctx context.Context) (bool, bool) { return pkg.entryKept(p, ctx, connector) },
		RegistrationAbsent: func(ctx context.Context) bool {
			_, present, known := readPackageRegistrationState(ctx, pkg.target(connector))
			return known && !present
		},
		CheckMissingCompanion: func(ctx context.Context, digest string, size int64) error {
			return pkg.extension.CheckMissingCompanion(ctx, found.Base, digest, size)
		},
		Stamp: func() (string, int64) {
			version, _ := setup.ReleaseVersion()
			return version, time.Now().UnixMilli()
		},
	})
	if err != nil || pending == nil {
		return err != nil, err
	}
	var start func(context.Context, *setup.PackageUpdateExpectation) error
	if !resume {
		start = func(ctx context.Context, pending *setup.PackageUpdateExpectation) error {
			return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
				return r.AdmitExplicitUpdate(record, pending, !found.Connected)
			})
		}
	}
	return true, pkg.replaceKept(p, ctx, journal, found, pending, record.KeptPackageSHA256, true, start)
}

// applyUpdate installs this build's integration into the selected host
// and re-pins the kept entry to its connector. A connector whose bytes
// differ from the pin after the install leaves the entry on its earlier
// image, and the row says it changed.
func (pkg *officialPackagePanel) applyUpdate(p *servicePanel, parent context.Context, journal setup.Journal, found setup.ExtensionHost, pending *setup.PackageUpdateExpectation, priorDigest string, start func(context.Context, *setup.PackageUpdateExpectation) error) {
	if err := pkg.replaceKept(p, parent, journal, found, pending, priorDigest, false, start); err != nil {
		log.Printf("setup: %s integration update: %v", pkg.spec.Title, err)
	}
}

func (pkg *officialPackagePanel) replaceKept(p *servicePanel, parent context.Context, journal setup.Journal, found setup.ExtensionHost, pending *setup.PackageUpdateExpectation, priorDigest string, explicit bool, start func(context.Context, *setup.PackageUpdateExpectation) error) error {
	if pending == nil || !pkg.ownsUpdate(pending) {
		return errors.New("the pending replacement does not name this integration")
	}
	catalog := journal.Catalog()
	found.Base = filepath.Clean(found.Base)
	connector, build := pending.Program, pending.SHA256
	return pkg.extension.ReplacePackage(parent, catalog, found.Base, setup.KeptPackageTransaction{
		Program: connector, KeptPackageSHA256: priorDigest,
		Timeout: extensionMaintenanceTimeout, ExistingReplacement: pending.Replacement,
		Image: func() (string, int64, error) { return pkg.extension.Image(found.Base) },
		Keep:  pkg.keepPrevious,
		Ready: func() error {
			if explicit {
				return nil
			}
			running, err := pkg.processes(found)
			if err != nil {
				return err
			}
			if len(running) > 0 {
				return errors.New("the picture application is running")
			}
			return nil
		},
		Install: func(ctx context.Context, before func() error) (func(context.Context) error, error) {
			if found.Connected {
				return pkg.installPinned(ctx, catalog, found.Base, before)
			}
			return pkg.installPinned(ctx, catalog, found.Base, func() error {
				prior := pending.Replacement.Previous
				if err := pkg.extension.CheckMissingCompanion(ctx, found.Base, prior.SHA256, prior.Bytes); err != nil {
					return err
				}
				return before()
			})
		},
		Restore: func(ctx context.Context, path string) error {
			return pkg.installPackage(ctx, path, found.Base)
		},
		Hold: func(ctx context.Context) (setup.HeldProgram, error) {
			return pkg.hold(ctx, catalog, found.Base)
		},
		Observe: func(ctx context.Context) (string, func(context.Context, string) (fwire.ApplicationChange, string, error), error) {
			apps, revision, err := pkg.target(connector).observeMaintainedRevision(ctx, p)
			if err != nil {
				return "", nil, err
			}
			return revision, apps.replaceAndVerifyRevision, nil
		},
		Expect: func(ctx context.Context) { pkg.expectConnect(p, ctx, catalog, connector) },
		Save: func(ctx context.Context, step *setup.PackageReplacement) error {
			if start != nil {
				next := *pending
				next.Replacement = step
				if err := start(ctx, &next); err != nil {
					return err
				}
				pending, start = &next, nil
				return nil
			}
			next, err := pkg.saveReplacement(ctx, pending, step)
			if err == nil {
				pending = next
			}
			return err
		},
		MarkChanged: func(ctx context.Context) {
			_ = pkg.editPendingUpdate(ctx, pending, func(r *setup.PackageMaintenanceRecord) { r.Changed, r.Recorded = true, build })
		},
		Publish: func(ctx context.Context) error { return pkg.publishPinned(ctx, catalog) },
		Commit: func(ctx context.Context, _ setup.PackageReplacement) error {
			at := p.appActivity.timestamp()
			var receipt setup.PreviousIntegration
			if err := pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
				var err error
				receipt, err = r.CommitReplacement(pending, at)
				return err
			}); err != nil {
				return err
			}
			pkg.presentKeptUpdate(p, ctx, journal, found, receipt, explicit)
			return nil
		},
	})
}

func (pkg *officialPackagePanel) presentKeptUpdate(p *servicePanel, ctx context.Context, journal setup.Journal, found setup.ExtensionHost, receipt setup.PreviousIntegration, explicit bool) {
	pkg.finishInstall(ctx, journal)
	if explicit {
		pkg.tellUpdated(p, found, receipt.ActivityAt, true)
		return
	}
	version, _ := setup.ReleaseVersion()
	what := pkg.spec.Title + "'s OpenAbstractions integration was updated with OpenAbstractions"
	if version != "" {
		what += " " + version
	}
	line := applicationActivity{At: receipt.ActivityAt, App: pkg.spec.Title, Program: receipt.Program, What: what, Outcome: "Updated"}
	line.Undo, line.UndoRestores = []string{pkg.spec.Registration}, true
	p.appActivity.add(line)
	p.logAction(ctx, "application.integration_update", map[string]string{"application.name": pkg.spec.Registration, "application.program": receipt.Program})
}

type keptPackageRevision interface {
	observedRevision() string
	replaceAndVerifyRevision(context.Context, string) (fwire.ApplicationChange, string, error)
}

// observeMaintainedRevision proves the declared registration and selected
// program before granting the replacement path a Registry revision.
func (target packageConnectTarget) observeMaintainedRevision(ctx context.Context, p *servicePanel) (keptPackageRevision, string, error) {
	apps, err := p.nativeApplicationsForPanel(ctx, target.registration)
	if err != nil {
		return nil, "", err
	}
	replacer, ok := apps.(keptPackageRevision)
	if !ok {
		return nil, "", errors.New("this runtime cannot read the installed app revision")
	}
	if _, err := target.registrationEntry(ctx, apps); err != nil {
		return nil, "", err
	}
	state, present, known := readPackageRegistrationState(ctx, target)
	revision := replacer.observedRevision()
	if !known || !present || state != registrationwire.StateEnabled || revision == "" {
		return nil, "", errors.New("the kept registration was not confirmed")
	}
	return replacer, revision, nil
}

func (pkg *officialPackagePanel) editPendingUpdate(ctx context.Context, pending *setup.PackageUpdateExpectation, change func(*setup.PackageMaintenanceRecord)) error {
	return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
		if err := r.CheckUpdate(pending); err != nil {
			return err
		}
		change(r)
		return nil
	})
}

func (pkg *officialPackagePanel) saveReplacement(ctx context.Context, pending *setup.PackageUpdateExpectation, step *setup.PackageReplacement) (*setup.PackageUpdateExpectation, error) {
	next := *pending
	next.Replacement = step
	return &next, pkg.editPendingUpdate(ctx, pending, func(r *setup.PackageMaintenanceRecord) { r.PendingUpdate = &next })
}

// healPin re-pins a kept entry whose pinned image is an earlier one
// while the connector on disk is this build's pin, as an install outside the
// step leaves it (owner's PC, R36: the integration reinstalled by hand, the
// entry still pinned to R35's connector, every agent call refused). The
// re-pin is the installing operator's, as applyUpdate's, and keeps the
// entry's state, title and grants. A running host is no reason to wait:
// its connector is the image the re-pin names.
func (pkg *officialPackagePanel) healPin(p *servicePanel, ctx context.Context, catalog setup.Catalog, build string) {
	found, ok := pkg.primary()
	if !ok || !found.Connected {
		return
	}
	if outdated, err := pkg.extension.Outdated(catalog, found.Base); err != nil || outdated {
		return
	}
	connector := pkg.extension.Connector(found.Base)
	kept, known := pkg.entryKept(p, ctx, connector)
	if !known || !kept {
		return
	}
	if state, present, known := readPackageRegistrationState(ctx, pkg.target(connector)); !known || !present || state != registrationwire.StateEnabled {
		return
	}
	held, err := pkg.hold(ctx, catalog, found.Base)
	if err != nil {
		log.Printf("setup: %s integration pin: the connector is not this build's: %v", pkg.spec.Title, err)
		return
	}
	pkg.expectConnect(p, ctx, catalog, connector)
	change, err := pkg.target(connector).replaceHeldProgram(ctx, p, held)
	if err != nil || change.Outcome != fwire.ApplicationOutcomeApplied {
		log.Printf("setup: %s integration pin: the entry was not re-pinned (%s %s): %v", pkg.spec.Title, change.Outcome.String(), change.Reason, err)
		return
	}
	pkg.changeRecord(ctx, func(r *setup.PackageMaintenanceRecord) { r.RecordKeptConnector(build) })
	log.Printf("setup: %s integration pin: the kept entry was re-pinned to this build's connector", pkg.spec.Title)
	p.logAction(ctx, "application.integration_record", map[string]string{"application.name": pkg.spec.Registration, "application.program": connector})
}

// recordIntegration is the R35 migration (architect R35): a kept,
// turned-on integration whose connector is already this build's pin, kept
// before the runtime recorded integrations or through the Keep question,
// gets its runtime record once. The step holds the connector against the
// pin, names the pin through ExpectConnect and re-pins the entry to the same
// image as the installing operator; the runtime writes installer_update and
// the integration shares the card with no click. The host's programs stay
// undisturbed: while they run, the migration waits for a later step.
func (pkg *officialPackagePanel) recordIntegration(p *servicePanel, ctx context.Context, catalog setup.Catalog, build string) {
	found, ok := pkg.primary()
	if !ok || !found.Connected {
		return
	}
	if running, err := pkg.processes(found); err != nil || len(running) > 0 {
		if err != nil {
			log.Printf("setup: %s's programs: %v", pkg.spec.Title, err)
		}
		return
	}
	connector := pkg.extension.Connector(found.Base)
	held, err := pkg.hold(ctx, catalog, found.Base)
	if err != nil {
		log.Printf("setup: %s integration record: the connector is not this build's: %v", pkg.spec.Title, err)
		pkg.changeRecord(ctx, func(r *setup.PackageMaintenanceRecord) { r.Recorded = build })
		return
	}
	pkg.expectConnect(p, ctx, catalog, connector)
	change, err := pkg.target(connector).replaceHeldProgram(ctx, p, held)
	if err != nil || change.Outcome != fwire.ApplicationOutcomeApplied {
		log.Printf("setup: %s integration record: the entry was not re-pinned (%s %s): %v", pkg.spec.Title, change.Outcome.String(), change.Reason, err)
		return
	}
	pkg.changeRecord(ctx, func(r *setup.PackageMaintenanceRecord) {
		if r.Kept != build {
			r.KeptPackageSHA256 = ""
		}
		r.Kept, r.Recorded = build, build
	})
	p.logAction(ctx, "application.integration_record", map[string]string{"application.name": pkg.spec.Registration, "application.program": connector})
}

// replaceHeldProgram re-pins the selected kept registration as the installing
// operator, holding the verified program through the native call.
func (target packageConnectTarget) replaceHeldProgram(ctx context.Context, p *servicePanel, held setup.HeldProgram) (fwire.ApplicationChange, error) {
	apps, err := p.nativeApplicationsForPanel(ctx, target.registration)
	if err != nil {
		return fwire.ApplicationChange{}, errors.Join(err, held.Close())
	}
	replacer, ok := apps.(setupReplacer)
	if !ok {
		return fwire.ApplicationChange{}, errors.Join(errors.New("this runtime cannot re-pin an app"), held.Close())
	}
	if _, err := target.registrationEntry(ctx, apps); err != nil {
		return fwire.ApplicationChange{}, errors.Join(err, held.Close())
	}
	change, err := replacer.Replace(ctx, held.Path)
	return change, errors.Join(err, held.Close())
}

// undoUpdate is the Undo of an update's Activity line: it installs the
// earlier integration again and re-pins the kept entry to its connector, the
// image the entry had before the update. The pin this build carries is then
// not applied again on its own; the Catalog offers Update.
func (pkg *officialPackagePanel) undoUpdate(p *servicePanel, w http.ResponseWriter, ctx context.Context, at int64, line applicationActivity) {
	unlock, acquired, err := pkg.claim()
	if err != nil || !acquired {
		http.Error(w, "OpenAbstractions could not restore the earlier integration. Try again.", http.StatusConflict)
		return
	}
	defer unlock()

	record := pkg.readRecord()
	if record.Connection != nil {
		http.Error(w, "Finish the pending integration installation before Undo.", http.StatusConflict)
		return
	}
	previous := record.Previous
	found, ok := pkg.primary()
	pending := record.Undo
	if !pkg.ownsUpdate(record.PendingUpdate) || previous == nil || !ok || pkg.extension.Connector(found.Base) != previous.Program ||
		(record.PendingUpdate != nil && record.PendingUpdate.Replacement != nil) ||
		(pending == nil && (!found.Connected || !p.latestPackageUndo(line, previous))) ||
		(pending != nil && pending.At != at) {
		http.Error(w, "This update can no longer be undone.", http.StatusConflict)
		return
	}
	if kept, known := pkg.entryKept(p, ctx, previous.Program); !known || !kept {
		http.Error(w, "This update can no longer be undone.", http.StatusConflict)
		return
	}
	if running, err := pkg.processes(found); err != nil || len(running) > 0 {
		http.Error(w, "Close "+pkg.spec.Title+", then Undo.", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), extensionMaintenanceTimeout)
	defer cancel()
	if err := pkg.restorePrevious(p, ctx, found, record, at); err != nil {
		log.Printf("setup: undo the %s integration update: %v", pkg.spec.Title, err)
		http.Error(w, "OpenAbstractions could not restore the earlier integration. Try again.", http.StatusConflict)
		return
	}
	p.appActivity.clearUndo(at)
	p.logAction(ctx, "application.integration_update_undo", map[string]string{"application.name": pkg.spec.Registration})
	panelJSON(w, struct {
		Message string `json:"message"`
	}{"Undone: " + pkg.spec.Title + " uses its earlier OpenAbstractions integration again."})
}

// Older receipts omit ActivityAt. Only the newest restore line for their
// connector can use them. New receipts name that line before it is appended.
func (p *servicePanel) latestPackageUndo(line applicationActivity, previous *setup.PreviousIntegration) bool {
	if line.Program != previous.Program || (previous.ActivityAt != 0 && line.At != previous.ActivityAt) {
		return false
	}
	var latest int64
	matches := 0
	for _, candidate := range p.appActivity.lines() {
		if candidate.At == line.At {
			matches++
		}
		if candidate.UndoRestores && candidate.Program == previous.Program {
			latest = candidate.At
		}
	}
	return matches == 1 && latest == line.At
}

func (pkg *officialPackagePanel) restorePrevious(p *servicePanel, ctx context.Context, found setup.ExtensionHost, record setup.PackageMaintenanceRecord, at int64) error {
	return setup.RestoreKeptPackage(ctx, record, at, setup.PackageUndoTransaction{
		Timeout:      extensionMaintenanceTimeout,
		HoldPrevious: pkg.extension.HoldUndoPackage,
		Observe: func(ctx context.Context, program string) (string, func(context.Context, string) (fwire.ApplicationChange, string, error), error) {
			apps, revision, err := pkg.target(program).observeMaintainedRevision(ctx, p)
			if err != nil {
				return "", nil, err
			}
			return revision, apps.replaceAndVerifyRevision, nil
		},
		Image: func() (string, int64, error) { return pkg.extension.Image(found.Base) },
		LegacyDigest: func(image string, size int64) (string, error) {
			journal, err := p.setupJournalForPanel()
			if err != nil {
				return "", err
			}
			return pkg.extension.PackageDigestForConnector(journal.Catalog(), image, size)
		},
		KeepCurrent: pkg.extension.KeepPreviousPackage,
		ReadyToRestore: func() error {
			if running, err := pkg.processes(found); err != nil || len(running) > 0 {
				return errors.Join(errors.New("the host must be closed before restoring the saved-current package"), err)
			}
			return nil
		},
		Install: func(ctx context.Context, path string, before func() error) error {
			return pkg.installSavedPackage(ctx, path, found.Base, before)
		},
		HoldConnector: func(ctx context.Context, digest string, size int64) (setup.HeldProgram, error) {
			return pkg.extension.HoldImage(ctx, found.Base, digest, size)
		},
		Save: func(ctx context.Context, before setup.PackageMaintenanceRecord, next *setup.PackageUndoProof, complete bool) error {
			return pkg.editRecord(ctx, func(r *setup.PackageMaintenanceRecord) error {
				if !pkg.ownsUpdate(r.PendingUpdate) {
					return errors.New("the pending Undo changed")
				}
				return r.SaveUndo(before, next, complete)
			})
		},
		ReleaseError: func(what string, err error) {
			log.Printf("setup: undo the %s integration update: release %s: %v", pkg.spec.Title, what, err)
		},
	})
}

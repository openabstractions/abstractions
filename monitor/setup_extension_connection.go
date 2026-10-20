package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type packageConnectTarget struct {
	registration string
	program      string
	title        string
}

// kept reads the ordinary Apps directory, which carries the person's Keep
// state. An unavailable directory is distinct from a known absent entry.
func (target packageConnectTarget) kept(ctx context.Context, panel *servicePanel) (kept, known bool) {
	apps, err := panel.setupApplicationsForPanel(ctx)
	if err != nil {
		return false, false
	}
	entry, readable := appPageEntry(ctx, apps, target.registration)
	if !readable {
		return false, false
	}
	return entry != nil && !entry.SelfRegistered && identity.SameSubjectProgram(entry.Descriptor.Program, target.program), true
}

func (target packageConnectTarget) otherProgram() error {
	return fmt.Errorf("Another program is registered as %s in Apps. Remove it there, then Connect again", target.title)
}

func (target packageConnectTarget) notReplaced() error {
	return fmt.Errorf("An earlier %s connector is in Apps, and OpenAbstractions could not replace it. Remove %s in Apps, then Connect again",
		target.title, target.title)
}

type catalogPackageConnect struct {
	target        packageConnectTarget
	digest        string
	kept          func(context.Context) (bool, bool)
	install       func(context.Context, setup.PackageConnection) (bool, error)
	connectLegacy func(context.Context) error
	register      func(context.Context, string) error
	hold          func(context.Context) (setup.HeldProgram, error)
	receipt       setup.PackageConnection
}

// connectCatalogPackage turns the person's Connect click into the native
// expectation and follows registration with Activity or a native Keep ask.
func (p *servicePanel) connectCatalogPackage(ctx context.Context, action catalogPackageConnect) (bool, error) {
	target := action.target
	keptBefore, _ := action.kept(ctx)
	var keptAfter bool
	ops := action.receipt
	ops.Admit = p.admitPackageConnect(target)
	ops.Expect = func(ctx context.Context, registration, program, digest string) {
		if registration == target.registration && identity.SameSubjectProgram(program, target.program) {
			expected := target
			expected.program = program
			p.recordPackageConnectExpectation(ctx, expected, digest)
		}
	}
	ops.Confirm = func(ctx context.Context, registration, program string) error {
		if registration != target.registration || !identity.SameSubjectProgram(program, target.program) {
			return errors.New("the connector proof differs from the selected package")
		}
		err := action.register(ctx, program)
		if keptBefore {
			err = p.resolvePackageConnectConflict(ctx, action, err, false)
		}
		if err != nil {
			return err
		}
		apps, err := p.nativeApplicationsForPanel(ctx, target.registration)
		if err != nil {
			return err
		}
		if _, err := target.registrationEntry(ctx, apps); err != nil {
			return err
		}
		keptAfter, _ = action.kept(ctx)
		if !keptAfter {
			return p.askPackageKeep(ctx, target)
		}
		return nil
	}
	ops.ConfirmLegacy = func(ctx context.Context, registration, program string) error {
		if registration != target.registration || !identity.SameSubjectProgram(program, target.program) {
			return errors.New("the connector proof differs from the selected package")
		}
		if err := p.resolvePackageConnectConflict(ctx, action, action.register(ctx, program), true); err != nil {
			return err
		}
		kept, known := action.kept(ctx)
		keptAfter = known && kept
		if !keptAfter {
			return p.askPackageKeep(ctx, target)
		}
		return nil
	}
	if handled, err := action.install(ctx, ops); handled {
		if err == nil && keptAfter && (!keptBefore || ops.ExistingReceipt != nil) {
			p.tellPackageConnected(target)
		}
		return true, err
	}
	if ops.ExistingReceipt != nil {
		return true, errors.New("the pending integration installation requires its original release")
	}
	p.recordPackageConnectExpectation(ctx, target, action.digest)
	if err := p.resolvePackageConnectConflict(ctx, action, action.connectLegacy(ctx), true); err != nil {
		return false, err
	}
	if kept, known := action.kept(ctx); known && kept {
		if !keptBefore {
			p.tellPackageConnected(target)
		}
		return false, nil
	}
	return false, p.askPackageKeep(ctx, target)
}

func (p *servicePanel) admitPackageConnect(target packageConnectTarget) func(context.Context, string, string, bool) error {
	return func(ctx context.Context, registration, program string, installing bool) error {
		if registration != target.registration || !identity.SameSubjectProgram(program, target.program) {
			return errors.New("the connector target differs from the selected package")
		}
		apps, err := p.nativeApplicationsForPanel(ctx, registration)
		if err != nil {
			return err
		}
		page, err := apps.Observe(ctx, "", 0)
		if err != nil {
			return err
		}
		if page.Outcome != fwire.ApplicationOutcomePage {
			return fmt.Errorf("the current integration registration could not be read (%s)", page.Outcome.String())
		}
		registered, present, err := findRegistrationDescriptor(page, registration)
		if err != nil {
			return err
		}
		if present && !identity.SameSubjectProgram(registered.Program, program) {
			return target.otherProgram()
		}
		if present && installing {
			return errors.New("the existing integration registration must be resolved before installing")
		}
		return nil
	}
}

func (p *servicePanel) resolvePackageConnectConflict(ctx context.Context, action catalogPackageConnect, err error, allowLegacyReregister bool) error {
	var conflict *setup.ExtensionRegistrationConflict
	if errors.As(err, &conflict) && conflict.Reason == "identity" {
		apps, appsErr := p.nativeApplicationsForPanel(ctx, action.target.registration)
		if appsErr != nil {
			return errors.Join(err, appsErr)
		}
		return replacePackageConnector(ctx, apps, action, allowLegacyReregister)
	}
	return err
}

// expectingApplications is the applications@3 client's ExpectConnect.
type expectingApplications interface {
	ExpectConnect(ctx context.Context, name, program, sha256 string) (fwire.ApplicationChange, error)
}

func (p *servicePanel) recordPackageConnectExpectation(ctx context.Context, target packageConnectTarget, digest string) {
	if digest == "" {
		log.Printf("setup: connect %s: this build pins no connector; the person is asked to keep it", target.title)
		return
	}
	apps, err := p.nativeApplicationsForPanel(ctx, target.registration)
	if err != nil {
		log.Printf("setup: connect %s: the Connect click was not recorded: %v", target.title, err)
		return
	}
	expecting, ok := apps.(expectingApplications)
	if !ok {
		log.Printf("setup: connect %s: the Connect click was not recorded: no applications@3 client", target.title)
		return
	}
	p.logAction(ctx, "application.expect_connect", map[string]string{"application.name": target.registration, "application.program": target.program})
	change, err := expecting.ExpectConnect(ctx, target.registration, target.program, digest)
	if err != nil || change.Outcome != fwire.ApplicationOutcomeApplied {
		log.Printf("setup: connect %s: the Connect click was not recorded (%s %s): %v",
			target.title, change.Outcome.String(), change.Reason, err)
	}
}

func (p *servicePanel) tellPackageConnected(target packageConnectTarget) {
	p.appActivity.add(applicationActivity{App: target.title, Program: target.program,
		What: target.title + " connected to OpenAbstractions", Outcome: "Connected",
		Undo: []string{target.registration}, UndoRemoves: true})
}

func (p *servicePanel) tellPackageUpdated(target packageConnectTarget, at int64, restart bool, undoRegistration string) {
	what := "Updated the OpenAbstractions integration in " + target.title
	if restart {
		what += "; restart " + target.title + " to use it"
	}
	line := applicationActivity{At: at, App: target.title, Program: target.program, What: what, Outcome: "Updated"}
	if undoRegistration != "" {
		line.Undo, line.UndoRestores = []string{undoRegistration}, true
	}
	p.appActivity.add(line)
}

func (p *servicePanel) askPackageKeep(ctx context.Context, target packageConnectTarget) error {
	notAsked := fmt.Errorf("%s is connected. OpenAbstractions could not ask whether to keep it. Connect again", target.title)
	apps, err := p.nativeApplicationsForPanel(ctx, target.registration)
	if err != nil {
		return errors.Join(notAsked, err)
	}
	asking, ok := apps.(confirmingApplications)
	if !ok {
		return notAsked
	}
	p.logAction(ctx, "application.ask_confirmation", map[string]string{"application.name": target.registration})
	change, err := asking.AskConfirmation(ctx, target.registration)
	if err != nil {
		return errors.Join(notAsked, err)
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return fmt.Errorf("%w (%s %s)", notAsked, change.Outcome.String(), change.Reason)
	}
	return nil
}

func replacePackageConnector(ctx context.Context, apps setupApplications, action catalogPackageConnect, allowLegacyReregister bool) error {
	target := action.target
	registered, err := target.registrationEntry(ctx, apps)
	if err != nil {
		return err
	}
	notReplaced := target.notReplaced()
	if replacer, ok := apps.(setupReplacer); ok {
		held, holdErr := action.hold(ctx)
		if holdErr != nil {
			log.Printf("setup: connect %s: earlier connector not re-pinned: %v", target.title, holdErr)
		} else {
			if !identity.SameSubjectProgram(held.Path, target.program) {
				return errors.Join(notReplaced, held.Close())
			}
			change, err := replacer.Replace(ctx, held.Path)
			if err = errors.Join(err, held.Close()); err != nil {
				return errors.Join(notReplaced, err)
			}
			switch {
			case change.Outcome == fwire.ApplicationOutcomeApplied:
				return nil
			case change.Outcome == fwire.ApplicationOutcomeConflict && change.Reason == "state":
			default:
				return fmt.Errorf("%w (%s %s)", notReplaced, change.Outcome.String(), change.Reason)
			}
			if registered, err = target.registrationEntry(ctx, apps); err != nil {
				return err
			}
		}
	}
	if !allowLegacyReregister {
		return notReplaced
	}
	change, err := apps.Remove(ctx, registered.Name)
	if err != nil {
		return errors.Join(notReplaced, err)
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return fmt.Errorf("%w (%s %s)", notReplaced, change.Outcome.String(), change.Reason)
	}
	if err := action.register(ctx, target.program); err != nil {
		return fmt.Errorf("OpenAbstractions removed the earlier %s connector. Connect again to add this one: %w", target.title, err)
	}
	return nil
}

func (target packageConnectTarget) registrationEntry(ctx context.Context, apps setupApplications) (fwire.ApplicationDescriptor, error) {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return fwire.ApplicationDescriptor{}, errors.Join(target.notReplaced(), err)
	}
	registered, found, err := findRegistrationDescriptor(page, target.registration)
	if err != nil {
		return fwire.ApplicationDescriptor{}, errors.Join(target.notReplaced(), err)
	}
	if !found || !identity.SameSubjectProgram(registered.Program, target.program) {
		return fwire.ApplicationDescriptor{}, target.otherProgram()
	}
	return registered, nil
}

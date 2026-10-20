package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
)

// An installer's --register-application or --remove-application run shows
// nothing (VISION 2026-10-06, amended 2026-11-10). Registration grants no
// permission, so no person is asked: the registry owner decides it and Apps
// lists the result.
//   - A registration the Applications service applies is connected: a new
//     name, or the same program registering its name again, which the service
//     updates in place and keeps as kept.
//   - A refused registration waits in Apps as Not connected with Connect: a
//     name another program holds (conflict, the entry stays as it is), or a
//     runtime that could not be reached. Connect replaces only on the click.
//   - A title that reads as OpenAbstractions is refused, logged and listed
//     in Activity.
//   - An uninstaller removes its own program's entry and the removal is
//     listed in Activity; an entry of another program is kept and the refusal
//     logged.

// errApplicationNotChanged ends a run whose request left the entry as it was.
var errApplicationNotChanged = errors.New("the application entry was not changed")

// errNativeOwned is a request whose name the app's own applications@3
// registration holds (application_native_owner.go).
var errNativeOwned = errors.New("the app's own registration holds this name")

// errApplicationIdentity is a request refused with conflict "identity": the
// same path holds an image without the entry's recorded signer or image pin.
// It waits in Apps as Not connected, and Connect replaces the entry.
var errApplicationIdentity = errors.New("the program image changed")

// applyApplicationRequest applies an installer's one request without asking.
func (p *servicePanel) applyApplicationRequest(ctx context.Context, request applicationStart) error {
	if request.register != nil {
		return p.applyApplicationRegistration(ctx, request)
	}
	if request.remove != "" {
		return p.applyApplicationRemoval(ctx, request.remove, request.recorded)
	}
	return nil
}

// applyApplicationRegistration registers the installer's manifest through
// the Panel's application.manage, or records it as not connected.
func (p *servicePanel) applyApplicationRegistration(ctx context.Context, request applicationStart) error {
	pending := *request.register
	if reservedTitle(pending.Title) {
		log.Printf("apps: %s is not connected: %s", pending.Name, reservedTitleRefusal)
		p.appActivity.add(applicationActivity{App: pending.Title, Program: pending.Program,
			What: "Its installer asked to connect it", Outcome: "Not connected: the title is a name OpenAbstractions uses for itself"})
		return fmt.Errorf("%w: %s", errApplicationNotChanged, reservedTitleRefusal)
	}
	message, err := p.connectApplication(ctx, pending)
	if err == nil {
		log.Print(message)
		return nil
	}
	if errors.Is(err, errNativeOwned) {
		// The app's own applications@3 registration is its one entry.
		p.removeApplicationRequest(pending.Name)
		log.Printf("apps: %s is connected through its own registration", pending.Name)
		return nil
	}
	// Not connected: the request waits in Apps with Connect.
	p.recordApplicationRequestAs(request, errors.Is(err, errApplicationIdentity))
	log.Printf("apps: %s is not connected and waits in Apps: %v", pending.Name, err)
	return fmt.Errorf("%w: %w", errApplicationNotChanged, err)
}

// connectApplication is the create-only Register of a manifest that holds no
// question: applied for a free name and for the same program's own name,
// conflict for a name another program holds.
func (p *servicePanel) connectApplication(ctx context.Context, pending fwire.ApplicationDescriptor) (string, error) {
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return "", err
	}
	change, err := apps.Register(ctx, pending)
	if err != nil {
		return "", err
	}
	switch change.Outcome {
	case fwire.ApplicationOutcomeApplied:
		p.removeApplicationRequest(pending.Name)
		message := pending.Title + " is connected to OpenAbstractions."
		if err := p.applicationReads().allowPanelRead(ctx, pending.Name); err != nil {
			message += " The Panel could not record that it may list it, so Apps does not show it: " + err.Error()
		}
		return message, nil
	case fwire.ApplicationOutcomeConflict:
		if nativeOwns(ctx, pending.Name) {
			return "", errNativeOwned
		}
		if change.Reason == "identity" {
			return "", errors.Join(errApplicationIdentity, applicationConflict("the program at this path changed since it was connected; the entry was kept"))
		}
		return "", applicationConflict("another program is connected under this name; the entry was kept")
	}
	return "", applicationConflict("the application was not registered (" + change.Outcome.String() + ")")
}

// applyApplicationRemoval removes the named entry when it belongs to the
// program the uninstaller installed; recorded is the program the
// installer's record gave it at intake. Any other entry stays, and the
// refusal is logged.
func (p *servicePanel) applyApplicationRemoval(ctx context.Context, name, recorded string) error {
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		log.Printf("apps: the removal of %s could not reach the runtime: %v", name, err)
		return fmt.Errorf("%w: %w", errApplicationNotChanged, err)
	}
	existing, readable := registeredApplication(ctx, apps, name)
	switch {
	case !readable:
		log.Printf("apps: the removal of %s could not read the connected apps", name)
		return fmt.Errorf("%w: the connected apps could not be read", errApplicationNotChanged)
	case existing == nil:
		p.removeApplicationRequest(name)
		return nil
	case !p.uninstallerProgram(ctx, name, existing.Program, recorded):
		log.Printf("apps: the removal of %s is refused: its entry names %s, which its uninstaller did not install", name, existing.Program)
		return fmt.Errorf("%w: the entry belongs to another program", errApplicationNotChanged)
	}
	change, err := apps.Remove(ctx, name)
	if err != nil {
		log.Printf("apps: the removal of %s failed: %v", name, err)
		return fmt.Errorf("%w: %w", errApplicationNotChanged, err)
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied && change.Outcome != fwire.ApplicationOutcomeUnknown {
		log.Printf("apps: the removal of %s was not applied (%s)", name, change.Outcome)
		return fmt.Errorf("%w: %s", errApplicationNotChanged, change.Outcome)
	}
	p.removeApplicationRequest(name)
	if err := p.applicationReads().clearPanelRead(ctx, name); err != nil {
		log.Printf("apps: the Panel's permission to list %s could not be removed: %v", name, err)
	}
	p.appActivity.add(applicationActivity{App: existing.Title, Program: existing.Program,
		What: "Its uninstaller removed it", Outcome: "Removed"})
	log.Printf("apps: removed %s", name)
	return nil
}

// uninstallerProgram reports whether program is the one name's installer
// put in place: inside the folder Setup installs name into, the program
// Setup's install record gives name, or installed, the program of the
// manifest the installer recorded for name (installedRecords), read when
// the request arrived. An extension's program lies in its host's folder;
// its installer's Applications record names it.
func (p *servicePanel) uninstallerProgram(ctx context.Context, name, program, installed string) bool {
	r := &p.appRequests
	folder := r.appFolder
	if folder == nil {
		folder = setupAppFolder
	}
	if root := folder(name); root != "" && identity.Inside(program, root) {
		return true
	}
	recorded := ""
	if r.setupProgram != nil {
		recorded = r.setupProgram(name)
	} else {
		recorded = p.stepRecordedProgram(ctx, name)
	}
	if recorded != "" && identity.SameSubjectProgram(program, recorded) {
		return true
	}
	return installed != "" && identity.SameSubjectProgram(program, installed)
}

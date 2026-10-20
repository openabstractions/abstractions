package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// An application's own installer starts the Panel with the application's
// Facade application manifest (`--register-application <file>`) or its name
// (`--remove-application <name>`). The run asks nothing and opens no window
// (application_native.go): the Panel registers or removes the entry with its
// own application.manage (VISION 2026-10-06, amended 2026-11-10). A
// registration the service refuses waits in Apps as Not connected, where
// Connect replaces an entry only on the person's click
// (application_pending.go). Every text shown comes from the manifest file or
// the registered entry, as untrusted text.

// readApplicationManifest reads and checks the installer's manifest file
// before anything is shown.
func readApplicationManifest(path string) (fwire.ApplicationDescriptor, error) {
	file, err := os.Open(path)
	if err != nil {
		return fwire.ApplicationDescriptor{}, fmt.Errorf("the application manifest could not be read: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, client.MaxApplicationDescriptorBytes+1))
	if err != nil {
		return fwire.ApplicationDescriptor{}, fmt.Errorf("the application manifest could not be read: %w", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fwire.ApplicationDescriptor{}, err
	}
	localAppData, pictures, err := manifestAccountFolders()
	if err != nil {
		return fwire.ApplicationDescriptor{}, fmt.Errorf("this account's folders could not be read: %w", err)
	}
	folders := manifestFolders{"app": filepath.Dir(absolute), "local_app_data": localAppData, "pictures": pictures}
	official := manifestOfficialProgram(data, localAppData)
	return client.ParseApplicationDescriptorResolving(data, func(value string, program bool) (string, error) {
		if program {
			return folders.resolveProgram(value, official)
		}
		return folders.resolve(value, false)
	})
}

// manifestOfficialProgram is the official program OA's compiled catalog names
// for the app this manifest registers, placed in this account's local
// application data folder; none for any other app.
func manifestOfficialProgram(data []byte, localAppData string) officialProgram {
	var named struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(data, &named) != nil || !validApplicationName(named.Name) || localAppData == "" {
		return officialProgram{}
	}
	official, ok := catalogOfficialProgram(setup.AppID(named.Name))
	if !ok || official.LocalAppData == "" || official.Signer == "" {
		return officialProgram{}
	}
	return officialProgram{Program: filepath.Join(localAppData, filepath.FromSlash(official.LocalAppData)), Signer: official.Signer}
}

// catalogOfficialProgram is setup.OfficialProgramOf; tests stand in a
// fixture app.
var catalogOfficialProgram = setup.OfficialProgramOf

// manifestAccountFolders is accountFolders; a test names its own folders.
var manifestAccountFolders = accountFolders

// reservedTitleRefusal is the one sentence the Panel gives an installer's
// manifest whose title is a product name (reservedTitle): an app may not
// read as OpenAbstractions in the Panel. The refusal is logged and listed in
// Activity.
const reservedTitleRefusal = "This app's title is a name OpenAbstractions uses for itself, so the Panel does not register it."

// applicationDigest identifies one manifest the person saw; "" is none.
func applicationDigest(descriptor *fwire.ApplicationDescriptor) string {
	if descriptor == nil {
		return ""
	}
	raw, _ := json.Marshal(descriptor)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// registeredApplication reads the entry registered under name. readable is
// false when the Panel cannot list the directory.
func registeredApplication(ctx context.Context, apps setupApplications, name string) (entry *fwire.ApplicationDescriptor, readable bool) {
	page, err := apps.Observe(ctx, "", 0)
	if err != nil || page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return nil, false
	}
	for _, listed := range page.Applications {
		if listed.Descriptor.Name == name {
			found := listed.Descriptor
			return &found, true
		}
	}
	return nil, true
}

type applicationSettled struct {
	Message string `json:"message"`
}

// applicationConflict is a confirmation the Panel refused, in one sentence:
// the request stays as it was. Any other error is a service failure.
type applicationConflict string

func (c applicationConflict) Error() string { return string(c) }

type applicationUnknown string

func (u applicationUnknown) Error() string { return string(u) }

// applicationReviewAgain is the conflict that keeps the registration waiting
// for a fresh review of an entry the Panel could not list before.
const applicationReviewAgain applicationConflict = "an application is already registered under this name; reload to review it before replacing it"

// applicationChanged refuses a confirmation whose registered entry changed
// after the person saw it; the request waits for a fresh review.
const applicationChanged applicationConflict = "the registered entry changed since this page was shown; reload and review it again"

// writeApplicationOutcome answers one confirmation over HTTP.
func writeApplicationOutcome(w http.ResponseWriter, settled applicationSettled, err error) {
	var conflict applicationConflict
	switch {
	case errors.As(err, &conflict):
		http.Error(w, string(conflict), http.StatusConflict)
	case err != nil:
		panelError(w, err)
	default:
		panelJSON(w, settled)
	}
}

// registerApplication is the registration a Not connected row's Connect
// makes: the Panel's application.manage Register of the manifest the person
// saw, through the existing replace. shown is the digest of the entry
// registered under its name when the person saw it ("" for none); a
// replacement removes that entry first. A registration clears the Panel's
// record of the request (application_pending.go).
// Legacy applications@2 has no conditional replace. A replacement removes the
// entry the person saw, then registers the new one. A definite refusal registers
// the earlier entry again. A lost reply is reconciled by one read of the name,
// and the result names the entry registered now. When that read fails, the
// outcome is unknown and nothing further changes.
func (p *servicePanel) registerApplication(ctx context.Context, pending fwire.ApplicationDescriptor, shown string) (string, error) {
	if reservedTitle(pending.Title) {
		return "", applicationConflict(reservedTitleRefusal)
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return "", err
	}
	existing, _ := registeredApplication(ctx, apps, pending.Name)
	if applicationDigest(existing) != shown {
		return "", applicationChanged
	}
	if existing != nil && applicationDigest(existing) == applicationDigest(&pending) {
		p.removeApplicationRequest(pending.Name)
		return pending.Title + " is already connected to OpenAbstractions.", nil
	}
	connected := func() (string, error) {
		p.removeApplicationRequest(pending.Name)
		message := pending.Title + " is connected to OpenAbstractions."
		if err := p.applicationReads().allowPanelRead(ctx, pending.Name); err != nil {
			message += " The Panel could not record that it may list it, so Apps does not show it: " + err.Error()
		}
		return message, nil
	}
	if existing != nil {
		change, err := apps.Remove(ctx, pending.Name)
		if err != nil {
			now, readable := registeredApplication(ctx, apps, pending.Name)
			if !readable {
				return "", applicationUnknown("the replacement outcome is unknown; check Apps: " + err.Error())
			}
			if applicationDigest(now) == shown {
				return "", applicationConflict("the earlier entry was kept")
			}
			if now != nil {
				return "", applicationUnknown("the replacement outcome is unknown; check Apps: " + err.Error())
			}
			// The lost Remove reply is reconciled: the name is empty now.
		} else if change.Outcome != fwire.ApplicationOutcomeApplied {
			return "", applicationConflict("the registered entry was not replaced (" + change.Outcome.String() + "); it was kept")
		}
	}
	change, err := apps.Register(ctx, pending)
	if err == nil && change.Outcome == fwire.ApplicationOutcomeApplied {
		return connected()
	}
	if err != nil {
		now, readable := registeredApplication(ctx, apps, pending.Name)
		if !readable {
			return "", applicationUnknown("the replacement outcome is unknown; check Apps: " + err.Error())
		}
		switch {
		case applicationDigest(now) == applicationDigest(&pending):
			return connected()
		case now != nil && applicationDigest(now) == shown:
			return "", applicationConflict("the earlier entry was kept")
		case now != nil:
			return "", applicationConflict("the entry changed by another program; review Apps")
		}
	}
	reason := "the application was not registered"
	if err != nil {
		reason += ": " + err.Error()
	} else {
		reason += " (" + change.Outcome.String() + ")"
	}
	if existing == nil {
		if err == nil && change.Outcome == fwire.ApplicationOutcomeConflict {
			// An entry the Panel may not list holds the name: one registered
			// before the Panel wrote read rules, or one whose read the person
			// revoked. The person's click asked for this name in the Panel, so
			// the Panel lets itself list it and keeps the request waiting for
			// a review of both entries.
			if p.applicationReads().allowPanelRead(ctx, pending.Name) == nil {
				return "", applicationReviewAgain
			}
			reason = "another application is registered under this name and the Panel cannot read it; nothing was changed"
		}
		return "", applicationConflict(reason)
	}
	// The earlier entry was removed for the replacement; put it back.
	restored, restoreErr := apps.Register(ctx, *existing)
	if restoreErr == nil && restored.Outcome == fwire.ApplicationOutcomeApplied {
		return "", applicationConflict(reason + "; the earlier entry " + existing.Title + " is registered again")
	}
	return "", applicationConflict(reason + "; the earlier entry " + existing.Title + " was removed and could not be registered again")
}

func (p *servicePanel) applicationRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/apps/pending", guard(key, p.appsPending))
	mux.HandleFunc("/apps/pending/connect", guard(key, p.appsPendingConnect))
}

// applicationStart is what the command line asked of this run: a manifest to
// register (and the file it came from) or a name to remove, already checked.
// recorded is the program the installer's own record gave the name to remove
// when the run started (removalRecordedProgram).
type applicationStart struct {
	register *fwire.ApplicationDescriptor
	manifest string
	remove   string
	recorded string
}

// parseApplicationStart checks --register-application and
// --remove-application before anything is applied, and names the request
// they select. A malformed manifest is refused here and never applied.
func parseApplicationStart(manifest, remove, page string) (applicationStart, string, error) {
	switch {
	case manifest == "" && remove == "":
		if page == "register-application" || page == "remove-application" {
			return applicationStart{}, "", errors.New("the application page needs --register-application <file> or --remove-application <name>")
		}
		return applicationStart{}, page, nil
	case manifest != "" && remove != "":
		return applicationStart{}, "", errors.New("give --register-application or --remove-application, not both")
	case manifest != "":
		descriptor, err := readApplicationManifest(manifest)
		if err != nil {
			return applicationStart{}, "", err
		}
		absolute, err := filepath.Abs(manifest)
		if err != nil {
			return applicationStart{}, "", err
		}
		return applicationStart{register: &descriptor, manifest: absolute}, "register-application", nil
	default:
		if !validApplicationName(remove) {
			return applicationStart{}, "", errors.New("the application name to remove is not a registered-name shape")
		}
		return applicationStart{remove: remove, recorded: removalRecordedProgram(remove)}, "remove-application", nil
	}
}

// removalRecordedProgram reads, at request intake, the program the
// installer's Applications record gives name: the record names its manifest,
// and the manifest its program. An uninstaller starts this run without
// waiting and goes on to delete that record and the manifest, so the
// identity is read first and the removal matches against it
// (uninstallerProgram). "" is no record, or one that does not read.
func removalRecordedProgram(name string) string {
	manifest := installedRecords()[name]
	if manifest == "" || !filepath.IsAbs(manifest) {
		return ""
	}
	descriptor, err := readApplicationManifest(manifest)
	if err != nil || descriptor.Name != name {
		return ""
	}
	return descriptor.Program
}

// applicationProbeProgram is a clean absolute path on this platform, used only
// to check a name against the manifest rule.
var applicationProbeProgram = func() string {
	if runtime.GOOS == "windows" {
		return `C:\probe.exe`
	}
	return "/probe"
}()

// validApplicationName holds a removal's name to the manifest's name rule.
func validApplicationName(name string) bool {
	probe := fwire.ApplicationDescriptor{Name: name, Program: applicationProbeProgram, Title: "x"}
	return client.ValidApplicationDescriptor(probe) == nil
}

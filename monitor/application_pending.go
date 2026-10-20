package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// A registration the Applications service did not apply waits in Apps as a
// Not connected row with Connect (VISION 2026-10-06, amended 2026-11-10). Two
// records name such a request, each only a manifest file to read again: the
// record an OpenAbstractions Setup package writes at install
// (installedApplicationRecords), and the Panel's own record of a
// --register-application run the service refused (applicationRequestRecords),
// which also covers an extension's installer and an app's own Connect. Any
// process of this account can write the installer's record, so it counts only
// under Setup's trust rule (trustedInstalledManifest). A name registered with
// the same manifest or by the same program waits for nothing. A name another
// program holds waits with the title it would replace; Apps shows that title
// inline on the Connect click, and Connect replaces through
// registerApplication. Listing the rows asks nothing and changes no entry.

// applicationRequestRecords are the Panel's records of registration requests,
// one small file per application name holding the manifest's path.
type applicationRequestRecords struct {
	// dir is the records' folder; "" is this account's
	// (<setup home>/application-requests).
	dir string
	// installed reads the installers' records, name to manifest file; nil
	// reads the platform's (installedApplicationRecords).
	installed func() map[string]string
	// appFolder and setupProgram are Setup's trust facts for an installer's
	// record (trustedInstalledManifest): the folder the package installs
	// name into and the program Setup's install record gives it; nil reads
	// setupAppFolder and Journal.RecordedProgram.
	appFolder    func(name string) string
	setupProgram func(name string) string
	// publisher words a program's signature; nil verifies the file
	// (applicationPublisher).
	publisher func(program string) string
	mu        sync.Mutex
	// signatures keeps each program's publisher sentence by the file's size
	// and time, so a reread of Apps does not verify an unchanged file again.
	signatures map[string]signatureSeen
	// ignored holds the installer records already logged as untrusted.
	ignored map[string]bool
}

type signatureSeen struct {
	size     int64
	modified time.Time
	sentence string
}

type applicationRequestRecord struct {
	Manifest string `json:"manifest"`
	// Identity marks a request refused with conflict "identity": the same
	// path holding an image without the entry's signer or image pin.
	Identity bool `json:"identity,omitempty"`
	// Program, with no Manifest, is an applications@3 app's own
	// registration refused with conflict "identity": the app's manifest is
	// inside its program, and Connect re-pins the entry to that program
	// (recordNative).
	Program string `json:"program,omitempty"`
}

// valid requires one request: a manifest file, or an applications@3
// program refused for its identity.
func (r applicationRequestRecord) valid() bool {
	if r.Program != "" {
		return r.Manifest == "" && r.Identity && filepath.IsAbs(r.Program)
	}
	return filepath.IsAbs(r.Manifest)
}

const applicationRequestRecordLimit = 4096

// installedRecords is installedApplicationRecords; TestMain keeps tests away
// from this account's records.
var installedRecords = installedApplicationRecords

func (r *applicationRequestRecords) folder() (string, error) {
	if r.dir != "" {
		return r.dir, nil
	}
	home, err := setup.CurrentHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(home.Config, "application-requests"), nil
}

// recordFile is name's record, or "" for a name that is no registered-name
// shape or no plain file name.
func (r *applicationRequestRecords) recordFile(name string) (string, error) {
	if !validApplicationName(name) || filepath.Base(name) != name || strings.ContainsAny(name, `/\:`) {
		return "", fmt.Errorf("%q is no application name", name)
	}
	dir, err := r.folder()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+".json"), nil
}

// record keeps the request for name; a later request for the name replaces
// it.
func (r *applicationRequestRecords) record(name, manifest string) error {
	return r.recordAs(name, manifest, false)
}

// recordAs is record, marking a request refused for its program's identity.
func (r *applicationRequestRecords) recordAs(name, manifest string, identity bool) error {
	path, err := r.recordFile(name)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(manifest) {
		return fmt.Errorf("the manifest path %q is not absolute", manifest)
	}
	return r.write(path, applicationRequestRecord{Manifest: manifest, Identity: identity})
}

// recordNative keeps an applications@3 app's own registration the service
// refused for its program's identity: program is the path whose image the
// entry's pin does not name.
func (r *applicationRequestRecords) recordNative(name, program string) error {
	path, err := r.recordFile(name)
	if err != nil {
		return err
	}
	record := applicationRequestRecord{Program: program, Identity: true}
	if !record.valid() {
		return fmt.Errorf("the program path %q is not absolute", program)
	}
	return r.write(path, record)
}

func (r *applicationRequestRecords) write(path string, record applicationRequestRecord) error {
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return cas.ChangeLimit(path, applicationRequestRecordLimit, func([]byte) ([]byte, error) { return raw, nil })
}

// remove ends the Panel's record for name and its lock; a missing record is
// no error.
func (r *applicationRequestRecords) remove(name string) error {
	path, err := r.recordFile(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = os.Remove(path + ".lock")
	return nil
}

// recorded reads the Panel's records, name to manifest file. A record that
// cannot be read is left out.
func (r *applicationRequestRecords) recorded() map[string]string {
	found := map[string]string{}
	for name, record := range r.records() {
		if record.Manifest != "" {
			found[name] = record.Manifest
		}
	}
	return found
}

// nativeRefused reads the Panel's records of applications@3 registrations
// refused for their program's identity, name to program.
func (r *applicationRequestRecords) nativeRefused() map[string]string {
	found := map[string]string{}
	for name, record := range r.records() {
		if record.Program != "" {
			found[name] = record.Program
		}
	}
	return found
}

// identityRefused names the Panel's records refused for their program's
// identity (applicationRequestRecord.Identity).
func (r *applicationRequestRecords) identityRefused() map[string]bool {
	found := map[string]bool{}
	for name, record := range r.records() {
		if record.Identity {
			found[name] = true
		}
	}
	return found
}

// records reads the Panel's records by name. A record that cannot be read
// is left out.
func (r *applicationRequestRecords) records() map[string]applicationRequestRecord {
	found := map[string]applicationRequestRecord{}
	dir, err := r.folder()
	if err != nil {
		return found
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return found
	}
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !entry.Type().IsRegular() {
			continue
		}
		path, err := r.recordFile(name)
		if err != nil {
			continue
		}
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		raw, err := io.ReadAll(io.LimitReader(file, applicationRequestRecordLimit+1))
		file.Close()
		var record applicationRequestRecord
		if err != nil || len(raw) > applicationRequestRecordLimit || json.Unmarshal(raw, &record) != nil || !record.valid() {
			continue
		}
		found[name] = record
	}
	return found
}

// sources are every request's manifest file by name: the Panel's records,
// and the installers' own records.
func (r *applicationRequestRecords) sources() (recorded, installed map[string]string) {
	read := r.installed
	if read == nil {
		read = installedRecords
	}
	installed = map[string]string{}
	for name, manifest := range read() {
		if validApplicationName(name) && filepath.IsAbs(manifest) {
			installed[name] = manifest
		}
	}
	return r.recorded(), installed
}

// installedManifest reads an installer's record for name under Setup's trust
// rule (trustedInstalledManifest). A record that fails it is ignored and
// logged once per manifest file.
func (p *servicePanel) installedManifest(ctx context.Context, name, manifest string) (fwire.ApplicationDescriptor, bool) {
	r := &p.appRequests
	folder := r.appFolder
	if folder == nil {
		folder = setupAppFolder
	}
	program := func(name string) string {
		if r.setupProgram != nil {
			return r.setupProgram(name)
		}
		return p.stepRecordedProgram(ctx, name)
	}
	descriptor, err := readApplicationManifest(manifest)
	reason := ""
	switch {
	case err != nil:
		reason = "its manifest cannot be read: " + err.Error()
	case descriptor.Name != name:
		reason = "its manifest names another app"
	case !trustedInstalledManifest(name, manifest, descriptor, folder, program):
		reason = "its manifest lies outside the app's Setup folder and names another program"
	default:
		return descriptor, true
	}
	r.mu.Lock()
	logged := r.ignored[name+"\x00"+manifest]
	if r.ignored == nil {
		r.ignored = map[string]bool{}
	}
	r.ignored[name+"\x00"+manifest] = true
	r.mu.Unlock()
	if !logged {
		log.Printf("apps: the installer record for %s is ignored: %s", name, reason)
	}
	return fwire.ApplicationDescriptor{}, false
}

// ownApplicationPublisher is "" for one of OpenAbstractions' own programs,
// which show no Publisher line, else program's publisher sentence. The
// program decides; a connector name a request asks for vouches for
// nothing.
func (r *applicationRequestRecords) ownApplicationPublisher(program string) string {
	if (*programTitler)(nil).oaOwnProgram(program) {
		return ""
	}
	return r.publisherOf(program)
}

// publisherOf is program's publisher sentence, verified once per file
// version.
func (r *applicationRequestRecords) publisherOf(program string) string {
	if r.publisher != nil {
		return r.publisher(program)
	}
	info, err := os.Stat(program)
	if err != nil {
		return applicationPublisher(program)
	}
	r.mu.Lock()
	seen, ok := r.signatures[program]
	r.mu.Unlock()
	if ok && seen.size == info.Size() && seen.modified.Equal(info.ModTime()) {
		return seen.sentence
	}
	sentence := applicationPublisher(program)
	r.mu.Lock()
	if r.signatures == nil {
		r.signatures = map[string]signatureSeen{}
	}
	r.signatures[program] = signatureSeen{size: info.Size(), modified: info.ModTime(), sentence: sentence}
	r.mu.Unlock()
	return sentence
}

// applicationPublisher words the signature on program's file: its verified
// signer, or that none was verified.
func applicationPublisher(program string) string {
	code, _, err := identity.VerifyFile(program, nil)
	if err != nil {
		return "Publisher: not verified."
	}
	publisher := asks.CodePublisher(code)
	switch {
	case publisher.Trusted && publisher.Subject != "":
		return "Publisher: " + publisher.Subject + ", verified by its signature."
	case publisher.Trusted:
		return "Publisher: signature verified; the signer's name is unavailable."
	default:
		return "Publisher: not verified."
	}
}

// pendingApplication is one Not connected app as Apps shows it. Name and
// Digest identify the request for Connect; neither is page text. Replaces is
// the title of the entry another program holds under the name, and Shown its
// digest; both are empty when the name is free.
type pendingApplication struct {
	Name       string                      `json:"name"`
	Title      string                      `json:"title"`
	Program    string                      `json:"program"`
	Publisher  string                      `json:"publisher"`
	Digest     string                      `json:"digest"`
	Replaces   string                      `json:"replaces,omitempty"`
	Shown      string                      `json:"shown,omitempty"`
	descriptor fwire.ApplicationDescriptor `json:"-"`
	// native is an applications@3 app's own registration: Connect re-pins
	// its entry (connectNativeApplication).
	native bool
}

// pendingApplications reads every Not connected request: one whose manifest
// still reads and names its record's name, and whose name is free or held by
// another program. An installer's record counts only under Setup's trust
// rule (installedManifest); otherwise the Panel's record of the same name,
// if any, stands. It asks nothing and changes no entry.
func (p *servicePanel) pendingApplications(ctx context.Context) ([]pendingApplication, error) {
	recorded, installed := p.appRequests.sources()
	refused, native := p.appRequests.identityRefused(), p.appRequests.nativeRefused()
	found := []pendingApplication{}
	if len(recorded) == 0 && len(installed) == 0 && len(native) == 0 {
		return found, nil
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return nil, err
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return nil, err
	}
	if page.Outcome != fwire.ApplicationOutcomePage && page.Outcome != fwire.ApplicationOutcomeStale {
		return nil, fmt.Errorf("the registered apps could not be read (%s)", page.Outcome)
	}
	registered := map[string]fwire.ApplicationDescriptor{}
	for _, entry := range page.Applications {
		registered[entry.Descriptor.Name] = entry.Descriptor
	}
	names := make([]string, 0, len(recorded)+len(installed)+len(native))
	for name := range recorded {
		names = append(names, name)
	}
	for name := range native {
		names = append(names, name)
	}
	for name := range installed {
		names = append(names, name)
	}
	slices.Sort(names)
	names = slices.Compact(names)
	for _, name := range names {
		if program, has := native[name]; has {
			if row, waits := p.pendingNativeApplication(name, program, registered); waits {
				found = append(found, row)
			}
			continue
		}
		var descriptor fwire.ApplicationDescriptor
		ok := false
		if manifest, has := installed[name]; has {
			descriptor, ok = p.installedManifest(ctx, name, manifest)
		}
		if manifest, has := recorded[name]; !ok && has {
			read, err := readApplicationManifest(manifest)
			descriptor, ok = read, err == nil && read.Name == name
		}
		if !ok || reservedTitle(descriptor.Title) {
			continue
		}
		row := pendingApplication{Name: name, Title: descriptor.Title, Program: programStem(descriptor.Program),
			Publisher: p.appRequests.ownApplicationPublisher(descriptor.Program), Digest: applicationDigest(&descriptor), descriptor: descriptor}
		if existing, held := registered[name]; held {
			if applicationDigest(&existing) == row.Digest || !refused[name] && identity.SameSubjectProgram(existing.Program, descriptor.Program) {
				// Connected: the same manifest, or the same program, which
				// the Applications service updates in place when it carries
				// the entry's signer or image pin. A request the service
				// refused for its program's identity waits here as Not
				// connected, and Connect replaces the entry.
				p.removeApplicationRequest(name)
				continue
			}
			if nativeOwns(ctx, name) {
				// The app's own applications@3 registration is its one
				// entry (application_native_owner.go).
				p.removeApplicationRequest(name)
				continue
			}
			row.Replaces, row.Shown = existing.Title, applicationDigest(&existing)
		}
		found = append(found, row)
	}
	return found, nil
}

// pendingNativeApplication is the Not connected row of an applications@3
// app's own registration refused for its program's identity: the entry under
// name stays, and Connect re-pins it to program (connectNativeApplication).
// With no entry under the name the request has nothing to replace and ends;
// the app's next registration of its own creates the entry. A conflict
// "identity" names the entry's own path, so an entry naming another program
// ends the request too: Connect never moves an entry to another path.
func (p *servicePanel) pendingNativeApplication(name, program string, registered map[string]fwire.ApplicationDescriptor) (pendingApplication, bool) {
	existing, held := registered[name]
	if !held || !identity.SameSubjectProgram(existing.Program, program) {
		p.removeApplicationRequest(name)
		return pendingApplication{}, false
	}
	descriptor := fwire.ApplicationDescriptor{Name: name, Program: program, Title: existing.Title}
	row := pendingApplication{Name: name, Title: appPageTitle(descriptor), Program: programStem(program),
		Publisher: p.appRequests.ownApplicationPublisher(program), Digest: nativeRequestDigest(descriptor),
		Replaces: appPageTitle(existing), Shown: applicationDigest(&existing), descriptor: descriptor, native: true}
	return row, true
}

// nativeRequestDigest identifies a native request for Connect apart from the
// entry it replaces, which may name the same program and title.
func nativeRequestDigest(descriptor fwire.ApplicationDescriptor) string {
	marked := descriptor
	marked.Name = "applications@3:" + descriptor.Name
	return applicationDigest(&marked)
}

// recordNativeIdentityRequest keeps an applications@3 app's own
// registration the service refused for its program's identity, so Apps
// lists it as Not connected with Connect.
func (p *servicePanel) recordNativeIdentityRequest(name, program string) {
	if err := p.appRequests.recordNative(name, program); err != nil {
		log.Printf("record the registration request: %v", err)
	}
}

// recordApplicationRequest keeps a run's registration request the service
// did not apply, so Apps lists it as Not connected with Connect.
func (p *servicePanel) recordApplicationRequest(request applicationStart) {
	p.recordApplicationRequestAs(request, false)
}

// recordApplicationRequestAs is recordApplicationRequest, marking a request
// refused for its program's identity.
func (p *servicePanel) recordApplicationRequestAs(request applicationStart, identity bool) {
	if request.register == nil || request.manifest == "" {
		return
	}
	if err := p.appRequests.recordAs(request.register.Name, request.manifest, identity); err != nil {
		log.Printf("record the registration request: %v", err)
	}
}

// removeApplicationRequest ends the Panel's record of name's request.
func (p *servicePanel) removeApplicationRequest(name string) {
	if err := p.appRequests.remove(name); err != nil {
		log.Printf("remove the registration request: %v", err)
	}
}

type appsPendingPage struct {
	Outcome string               `json:"outcome"`
	Entries []pendingApplication `json:"entries"`
	Error   string               `json:"error,omitempty"`
}

// appsPending is the Apps page's read of the Not connected apps.
func (p *servicePanel) appsPending(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	view := appsPendingPage{Outcome: "page", Entries: []pendingApplication{}}
	if found, err := p.pendingApplications(ctx); err != nil {
		view.Outcome, view.Error = "unavailable", err.Error()
	} else {
		view.Entries = found
	}
	w.Header().Set("Cache-Control", "no-store")
	panelJSON(w, view)
}

// appsPendingConnectRequest is one Connect click: the request the row showed
// and the digest of the entry it replaces ("" for none).
type appsPendingConnectRequest struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Shown  string `json:"shown"`
}

// appsPendingConnect is a row's Connect: it registers the request the row
// showed through registerApplication, replacing the entry the row named only
// when that entry is still the one registered.
func (p *servicePanel) appsPendingConnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var body appsPendingConnectRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&body) != nil || decoder.Decode(&extra) != io.EOF || body.Name == "" || body.Digest == "" {
		http.Error(w, "one Connect is required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	found, err := p.pendingApplications(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	at := slices.IndexFunc(found, func(a pendingApplication) bool { return a.Name == body.Name })
	if at < 0 {
		http.Error(w, "this app is no longer waiting; Apps shows its current state", http.StatusConflict)
		return
	}
	if found[at].Digest != body.Digest || found[at].Shown != body.Shown {
		http.Error(w, "this app's request or the app it replaces changed since Apps showed it; review it again", http.StatusConflict)
		return
	}
	var message string
	if found[at].native {
		message, err = p.connectNativeApplication(ctx, found[at].descriptor)
	} else {
		message, err = p.registerApplication(ctx, found[at].descriptor, found[at].Shown)
	}
	writeApplicationOutcome(w, applicationSettled{Message: message}, err)
}

// connectNativeApplication is the Connect click on an applications@3 app's
// refused registration: the person re-pins its entry to the program at the
// request's path, under the revision the Panel reads now. appsPendingConnect
// has checked that the entry is still the one the row showed. The service
// pins the image the path holds at the click.
func (p *servicePanel) connectNativeApplication(ctx context.Context, pending fwire.ApplicationDescriptor) (string, error) {
	apps, err := p.nativeApplicationsForPanel(ctx, pending.Name)
	if err != nil {
		return "", err
	}
	replacer, ok := apps.(setupReplacer)
	if !ok {
		return "", applicationConflict("this app's entry cannot be replaced from here")
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return "", err
	}
	if page.Outcome != fwire.ApplicationOutcomePage || len(page.Applications) != 1 || page.Applications[0].Descriptor.Name != pending.Name {
		return "", applicationChanged
	}
	change, err := replacer.Replace(ctx, pending.Program)
	if err != nil {
		return "", err
	}
	switch {
	case change.Outcome == fwire.ApplicationOutcomeApplied:
	case change.Outcome == fwire.ApplicationOutcomeConflict && change.Reason == "revision":
		return "", applicationChanged
	default:
		return "", applicationConflict("the registered entry was not replaced (" + change.Outcome.String() + "); it was kept")
	}
	p.removeApplicationRequest(pending.Name)
	return appPageTitle(pending) + " is connected to OpenAbstractions.", nil
}

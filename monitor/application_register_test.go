package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// applicationDirectoryFake is an in-memory applications directory: Register
// adds an entry under a free name and updates the same program's entry, as
// the Applications service does; another program's name is a conflict.
// Remove deletes one.
type applicationDirectoryFake struct {
	entries   map[string]fwire.ApplicationDescriptor
	registers int
	removes   int
	// identity names entries whose program image changed: the same path
	// registering again meets conflict "identity" (serve's pin rule).
	identity map[string]bool
}

func (f *applicationDirectoryFake) Register(_ context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	f.registers++
	if existing, ok := f.entries[d.Name]; ok && existing.Program != d.Program {
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict}, nil
	}
	if _, ok := f.entries[d.Name]; ok && f.identity[d.Name] {
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict, Reason: "identity"}, nil
	}
	f.entries[d.Name] = d
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

func (f *applicationDirectoryFake) Remove(_ context.Context, name string) (fwire.ApplicationChange, error) {
	f.removes++
	if _, ok := f.entries[name]; !ok {
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeUnknown}, nil
	}
	delete(f.entries, name)
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

func (f *applicationDirectoryFake) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	page := fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}
	for _, d := range f.entries {
		page.Applications = append(page.Applications, fwire.ApplicationEntry{Descriptor: d})
	}
	return page, nil
}

func (f *applicationDirectoryFake) Activate(context.Context, string) (fwire.ApplicationActivationResult, error) {
	return fwire.ApplicationActivationResult{}, nil
}

const applicationTestKey = "app-test-key"

// applicationTestPublisher stands in for the program's signature check.
const applicationTestPublisher = "Publisher: Example Corp, verified by its signature."

// applicationTestDirs holds each test's application folder: the folder its
// manifest is written to, which the manifest's program must lie in ({app}).
var applicationTestDirs sync.Map

func applicationTestDir(t *testing.T) string {
	if dir, ok := applicationTestDirs.Load(t); ok {
		return dir.(string)
	}
	dir := t.TempDir()
	applicationTestDirs.Store(t, dir)
	t.Cleanup(func() { applicationTestDirs.Delete(t) })
	return dir
}

func applicationTestProgram(t *testing.T) string {
	return filepath.Join(applicationTestDir(t), "example", "example.exe")
}

func writeApplicationManifest(t *testing.T, text string) string {
	path := filepath.Join(applicationTestDir(t), "manifest.json")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func manifestText(program, title string) string {
	raw, _ := json.Marshal(map[string]any{"name": "example", "program": program, "title": title,
		"activation": map[string]any{"arguments": []string{"--serve", "<b>"}, "readiness_timeout_ms": 1000,
			"readiness": map[string]string{"name": "ready", "protocol": "example", "contract": "example/ready@1"}}})
	return string(raw)
}

// applicationRequestsFor are a test's own request records: a fresh folder,
// the installer records given (none by default), no Setup folder or Setup
// program for any app, and a fixed publisher.
func applicationRequestsFor(t *testing.T, installed map[string]string) applicationRequestRecords {
	return applicationRequestRecords{dir: filepath.Join(t.TempDir(), "requests"),
		installed:    func() map[string]string { return installed },
		appFolder:    func(string) string { return "" },
		setupProgram: func(string) string { return "" },
		publisher:    func(string) string { return applicationTestPublisher }}
}

// applicationPanel is a Panel whose Apps lists manifest as a Not connected
// request, as a refused installer's run records it.
func applicationPanel(t *testing.T, apps setupApplications, manifest string) (*servicePanel, http.Handler) {
	t.Helper()
	request, page, err := parseApplicationStart(writeApplicationManifest(t, manifest), "", "overview")
	if err != nil || page != "register-application" {
		t.Fatalf("start: %q %v", page, err)
	}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}, appRequests: applicationRequestsFor(t, nil)}
	p.recordApplicationRequest(request)
	return p, p.handler(applicationTestKey)
}

func applicationCall(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path+"?k="+applicationTestKey, strings.NewReader(body))
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// pendingRows reads Apps' waiting registrations.
func pendingRows(t *testing.T, h http.Handler) []pendingApplication {
	t.Helper()
	rec := applicationCall(t, h, "GET", "/apps/pending", "")
	var page appsPendingPage
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &page) != nil || page.Outcome != "page" {
		t.Fatalf("pending %d: %s", rec.Code, rec.Body.String())
	}
	return page.Entries
}

// connectRow is a row's Connect click with the request and the replaced
// entry the row showed.
func connectRow(t *testing.T, h http.Handler, row pendingApplication) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(appsPendingConnectRequest{Name: row.Name, Digest: row.Digest, Shown: row.Shown})
	return applicationCall(t, h, "POST", "/apps/pending/connect", string(body))
}

// recordedRequest is the Not connected request Apps lists for example.
func recordedRequest(t *testing.T, p *servicePanel) fwire.ApplicationDescriptor {
	t.Helper()
	d, err := readApplicationManifest(p.appRequests.recorded()["example"])
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// connectAsShown is a Connect click on the row as Apps shows it now: it
// replaces the entry registered under the name.
func connectAsShown(t *testing.T, p *servicePanel) (string, error) {
	t.Helper()
	ctx := context.Background()
	d := recordedRequest(t, p)
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		t.Fatal(err)
	}
	existing, _ := registeredApplication(ctx, apps, d.Name)
	return p.registerApplication(ctx, d, applicationDigest(existing))
}

// A Not connected request waits as one row in Apps: its title, program and
// publisher. Showing it registers nothing; Connect registers it once through
// the Applications service, and the row and its record end.
func TestANotConnectedRequestShowsInAppsAndConnectRegistersIt(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	program := applicationTestProgram(t)
	p, h := applicationPanel(t, apps, manifestText(program, "Example"))
	rows := pendingRows(t, h)
	if len(rows) != 1 || apps.registers != 0 {
		t.Fatalf("rows %+v registers %d", rows, apps.registers)
	}
	row := rows[0]
	if row.Name != "example" || row.Title != "Example" || row.Replaces != "" || row.Shown != "" ||
		row.Program != "example" || row.Publisher != applicationTestPublisher || row.Digest == "" {
		t.Fatalf("row %+v", row)
	}
	rec := connectRow(t, h, row)
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"message":"Example is connected to OpenAbstractions."}` {
		t.Fatalf("allow %d: %s", rec.Code, rec.Body.String())
	}
	if got := apps.entries["example"]; got.Program != program || got.Activation == nil || got.Activation.Arguments[1] != "<b>" {
		t.Fatalf("registered %+v", got)
	}
	if rows := pendingRows(t, h); len(rows) != 0 {
		t.Fatalf("a connected app still waits: %+v", rows)
	}
	if recorded := p.appRequests.recorded(); len(recorded) != 0 {
		t.Fatalf("the request record outlived Connect: %v", recorded)
	}
	if again := connectRow(t, h, row); again.Code != http.StatusConflict || apps.registers != 1 {
		t.Fatalf("a second click registered again: %d", again.Code)
	}
}

// A Connect for a request that changed since Apps showed it registers nothing.
func TestConnectRefusesARequestThatChanged(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	program := applicationTestProgram(t)
	_, h := applicationPanel(t, apps, manifestText(program, "Example"))
	row := pendingRows(t, h)[0]
	writeApplicationManifest(t, manifestText(program, "Example 2"))
	if rec := connectRow(t, h, row); rec.Code != http.StatusConflict || apps.registers != 0 {
		t.Fatalf("a changed request was registered: %d %s", rec.Code, rec.Body.String())
	}
	if rows := pendingRows(t, h); len(rows) != 1 || rows[0].Title != "Example 2" {
		t.Fatalf("rows %+v", rows)
	}
}

// The installer's own record (Setup's HKCU record) waits in Apps without a
// Panel record, and an app registered by another path waits for nothing.
func TestAnInstallersRecordWaitsAndARegisteredNameDoesNot(t *testing.T) {
	program := applicationTestProgram(t)
	manifest := writeApplicationManifest(t, manifestText(program, "Example"))
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}, appRequests: applicationRequestsFor(t, map[string]string{"example": manifest, "other": manifest})}
	p.appRequests.appFolder = func(string) string { return applicationTestDir(t) }
	h := p.handler(applicationTestKey)
	rows := pendingRows(t, h)
	if len(rows) != 1 || rows[0].Name != "example" {
		t.Fatalf("an installer's record: %+v (a record whose manifest names another app is left out)", rows)
	}
	if err := p.appRequests.record("example", manifest); err != nil {
		t.Fatal(err)
	}
	apps.entries["example"] = fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example"}
	if rows := pendingRows(t, h); len(rows) != 0 {
		t.Fatalf("a registered app waits: %+v", rows)
	}
	if recorded := p.appRequests.recorded(); len(recorded) != 0 {
		t.Fatalf("the Panel's record of a registered app was kept: %v", recorded)
	}
}

// An installer's record counts under Setup's trust rule only: its manifest
// lies inside the app's Setup folder, or names the program Setup's install
// record gives the app. Any other record is ignored and logged once, and the
// Panel's own record of a --register-application run for the name stands.
func TestAnInstallersRecordWaitsOnlyUnderSetupsTrustRule(t *testing.T) {
	root := t.TempDir()
	place := func(folder, title string) (manifest, program string) {
		if err := os.MkdirAll(folder, 0o700); err != nil {
			t.Fatal(err)
		}
		program = filepath.Join(folder, "example.exe")
		if err := os.WriteFile(program, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		manifest = filepath.Join(folder, "application-manifest.json")
		if err := os.WriteFile(manifest, []byte(manifestText(program, title)), 0o600); err != nil {
			t.Fatal(err)
		}
		return manifest, program
	}
	appFolder := filepath.Join(root, "Programs", "OpenAbstractions", "apps", "example")
	insideManifest, insideProgram := place(appFolder, "Inside")
	outsideManifest, outsideProgram := place(filepath.Join(root, "elsewhere"), "Outside")
	panelManifest, _ := place(filepath.Join(root, "requested"), "Requested")
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	for _, test := range []struct {
		name, manifest, setupProgram, panelRecord, want string
	}{
		{"inside", insideManifest, "", "", "Inside"},
		{"outside, same program", outsideManifest, outsideProgram, "", "Outside"},
		{"outside, other program", outsideManifest, insideProgram, "", ""},
		{"outside, no Setup record", outsideManifest, "", "", ""},
		{"outside, other program, with a Panel record", outsideManifest, insideProgram, panelManifest, "Requested"},
	} {
		t.Run(test.name, func(t *testing.T) {
			logs.Reset()
			apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
			p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}, appRequests: applicationRequestsFor(t, map[string]string{"example": test.manifest})}
			p.appRequests.appFolder = func(name string) string {
				if name != "example" {
					t.Errorf("folder asked for %q", name)
				}
				return appFolder
			}
			p.appRequests.setupProgram = func(string) string { return test.setupProgram }
			if test.panelRecord != "" {
				if err := p.appRequests.record("example", test.panelRecord); err != nil {
					t.Fatal(err)
				}
			}
			h := p.handler(applicationTestKey)
			rows := pendingRows(t, h)
			if test.want == "" {
				if len(rows) != 0 {
					t.Fatalf("an untrusted record waits: %+v", rows)
				}
			} else if len(rows) != 1 || rows[0].Title != test.want {
				t.Fatalf("rows %+v, want %s", rows, test.want)
			}
			ignored := strings.Contains(logs.String(), "the installer record for example is ignored")
			if trusted := test.want == "Inside" || test.want == "Outside"; ignored == trusted {
				t.Fatalf("logged %q", logs.String())
			}
			// A reread logs nothing more.
			logs.Reset()
			pendingRows(t, h)
			if logs.Len() != 0 {
				t.Fatalf("the reread logged again: %q", logs.String())
			}
			if test.want == "" {
				return
			}
			if rec := connectRow(t, h, rows[0]); rec.Code != 200 || apps.entries["example"].Title != test.want {
				t.Fatalf("allow %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// A manifest whose title is a product name, in any case or spacing, waits in
// no row and is never registered.
func TestRegistrationRefusesAReservedTitle(t *testing.T) {
	for _, title := range []string{"OpenAbstractions", "openabstractions panel"} {
		apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
		p, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), title))
		if rows := pendingRows(t, h); len(rows) != 0 {
			t.Fatalf("%q: a reserved title waits in Apps: %+v", title, rows)
		}
		d, err := readApplicationManifest(writeApplicationManifest(t, manifestText(applicationTestProgram(t), title)))
		if err != nil {
			t.Fatal(err)
		}
		_, err = p.registerApplication(context.Background(), d, "")
		var conflict applicationConflict
		if !errors.As(err, &conflict) || string(conflict) != reservedTitleRefusal || apps.registers != 0 {
			t.Fatalf("%q: confirm %v registers %d", title, err, apps.registers)
		}
	}
}

func TestMalformedManifestIsRefusedBeforeAnyPage(t *testing.T) {
	program := applicationTestProgram(t)
	for name, text := range map[string]string{
		"unknown field":   strings.TrimSuffix(manifestText(program, "Example"), "}") + `,"icon":"x"}`,
		"relative":        manifestText("example.exe", "Example"),
		"not json":        "{",
		"empty title":     manifestText(program, ""),
		"duplicate title": strings.TrimSuffix(manifestText(program, "Example"), "}") + `,"title":"Other"}`,
	} {
		if request, page, err := parseApplicationStart(writeApplicationManifest(t, text), "", "overview"); err == nil {
			t.Errorf("%s: accepted as %q %+v", name, page, request)
		}
	}
	if _, _, err := parseApplicationStart(filepath.Join(t.TempDir(), "absent.json"), "", "overview"); err == nil {
		t.Error("a missing manifest file was accepted")
	}
	if _, _, err := parseApplicationStart("", "Not A Name", "overview"); err == nil {
		t.Error("a malformed removal name was accepted")
	}
	if _, _, err := parseApplicationStart("", "", "register-application"); err == nil {
		t.Error("the application page opened with nothing to show")
	}
}

// A page named for an installer's request opens Apps; the registration and
// removal pages are gone.
func TestARequestPageOpensApps(t *testing.T) {
	for _, name := range []string{"register-application", "remove-application"} {
		if start, err := parsePanelStartPage(name); err != nil || start.path != "/" {
			t.Fatalf("%s: start %+v %v", name, start, err)
		}
	}
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	for _, path := range []string{"/application/register", "/application/register/confirm", "/application/register/cancel",
		"/application/remove", "/application/remove/confirm", "/application/remove/cancel", "/apps/pending/allow"} {
		if rec := applicationCall(t, h, "GET", path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s answers %d", path, rec.Code)
		}
	}
}

func TestReplacementNeedsTheEntryThePersonSaw(t *testing.T) {
	program := applicationTestProgram(t)
	old := fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example 1"}
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": old}}
	p, _ := applicationPanel(t, apps, manifestText(program, "Example 2"))
	// A Connect that saw no entry does not replace one.
	if _, err := p.registerApplication(context.Background(), recordedRequest(t, p), ""); !errors.Is(err, applicationChanged) || apps.entries["example"].Title != "Example 1" {
		t.Fatalf("a stale Connect replaced the entry: %v", err)
	}
	message, err := connectAsShown(t, p)
	if err != nil || apps.entries["example"].Title != "Example 2" || apps.removes != 1 || message != "Example 2 is connected to OpenAbstractions." {
		t.Fatalf("replace %q %v %+v", message, err, apps.entries)
	}
}

// An installer owns the stdout of the Panel it starts; the keyed URL never
// reaches it, or the installer could confirm its own registration.
func TestAnInstallersPanelPrintsNoKey(t *testing.T) {
	program := applicationTestProgram(t)
	register, _, err := parseApplicationStart(writeApplicationManifest(t, manifestText(program, "Example")), "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	remove, _, err := parseApplicationStart("", "example", "overview")
	if err != nil {
		t.Fatal(err)
	}
	for name, request := range map[string]applicationStart{"register": register, "remove": remove} {
		var out strings.Builder
		if err := announcePanelURL(&out, "http://127.0.0.1:50000/?k=private-key", false, request); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Errorf("%s: stdout carries %q", name, out.String())
		}
	}
	var out strings.Builder
	if err := announcePanelURL(&out, "http://127.0.0.1:8734/?k=private-key", false, applicationStart{}); err != nil || !strings.Contains(out.String(), "private-key") {
		t.Fatalf("an ordinary run no longer prints its URL: %q %v", out.String(), err)
	}
}

// failingRegistration refuses the replacement and accepts the earlier entry
// back.
type failingRegistration struct {
	*applicationDirectoryFake
	refuse string
}

func (f *failingRegistration) Register(ctx context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	if d.Title == f.refuse {
		f.registers++
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeUnavailable}, nil
	}
	return f.applicationDirectoryFake.Register(ctx, d)
}

func TestAFailedReplacementRegistersTheEarlierEntryAgain(t *testing.T) {
	program := applicationTestProgram(t)
	old := fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example 1"}
	apps := &failingRegistration{applicationDirectoryFake: &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": old}}, refuse: "Example 2"}
	p, _ := applicationPanel(t, apps, manifestText(program, "Example 2"))
	_, err := connectAsShown(t, p)
	var conflict applicationConflict
	if !errors.As(err, &conflict) || !strings.Contains(string(conflict), "Example 1 is registered again") {
		t.Fatalf("failed replacement: %v", err)
	}
	if got := apps.entries["example"]; got.Title != "Example 1" || apps.removes != 1 {
		t.Fatalf("the earlier entry was not restored: %+v removes %d", apps.entries, apps.removes)
	}
}

type uncertainApplicationReplacement struct {
	*applicationDirectoryFake
	mode      string
	changed   bool
	reads     int
	old, next fwire.ApplicationDescriptor
}

func (f *uncertainApplicationReplacement) Observe(ctx context.Context, cursor string, wait int64) (fwire.ApplicationPage, error) {
	f.reads++
	if f.changed && strings.HasSuffix(f.mode, "read-error") {
		return fwire.ApplicationPage{}, errors.New("read failed")
	}
	return f.applicationDirectoryFake.Observe(ctx, cursor, wait)
}

func (f *uncertainApplicationReplacement) Remove(ctx context.Context, name string) (fwire.ApplicationChange, error) {
	if strings.HasPrefix(f.mode, "remove-") {
		f.changed = true
		f.removes++
		switch f.mode {
		case "remove-gone":
			delete(f.entries, name)
		case "remove-other":
			f.entries[name] = fwire.ApplicationDescriptor{Name: name, Title: "Other", Program: "other"}
		}
		return fwire.ApplicationChange{}, errors.New("Remove reply lost")
	}
	return f.applicationDirectoryFake.Remove(ctx, name)
}

func (f *uncertainApplicationReplacement) Register(ctx context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	if d.Title == f.next.Title {
		f.changed = true
		f.registers++
		switch f.mode {
		case "register-new":
			f.entries[d.Name] = d
		case "register-shown":
			f.entries[d.Name] = f.old
		case "register-other":
			f.entries[d.Name] = fwire.ApplicationDescriptor{Name: d.Name, Title: "Other", Program: "other"}
		default:
			if strings.HasPrefix(f.mode, "remove-") {
				f.entries[d.Name] = d
				return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
			}
		}
		return fwire.ApplicationChange{}, errors.New("Register reply lost")
	}
	return f.applicationDirectoryFake.Register(ctx, d)
}

func TestApplicationReplacementReconcilesLostRepliesWithoutBlindRestore(t *testing.T) {
	for _, mode := range []string{"register-new", "register-gone", "register-shown", "register-other", "register-read-error", "remove-gone", "remove-shown", "remove-other", "remove-read-error"} {
		t.Run(mode, func(t *testing.T) {
			program := applicationTestProgram(t)
			old := fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example 1"}
			apps := &uncertainApplicationReplacement{applicationDirectoryFake: &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": old}}, mode: mode, old: old}
			p, _ := applicationPanel(t, apps, manifestText(program, "Example 2"))
			apps.next = recordedRequest(t, p)
			_, err := p.registerApplication(t.Context(), apps.next, applicationDigest(&old))
			var unknown applicationUnknown
			switch mode {
			case "register-new", "remove-gone":
				if err != nil || apps.entries["example"].Title != "Example 2" || apps.registers != 1 {
					t.Fatal(err, apps.entries, apps.registers)
				}
			case "register-gone":
				if err == nil || apps.entries["example"].Title != old.Title || apps.registers != 2 {
					t.Fatal(err, apps.entries, apps.registers)
				}
			case "register-shown", "remove-shown":
				if err == nil || apps.entries["example"].Title != old.Title || apps.registers != map[bool]int{true: 1, false: 0}[mode == "register-shown"] {
					t.Fatal(err, apps.entries, apps.registers)
				}
			case "register-other":
				if err == nil || !strings.Contains(err.Error(), "review Apps") || apps.entries["example"].Title != "Other" || apps.registers != 1 {
					t.Fatal(err, apps.entries, apps.registers)
				}
			default:
				if !errors.As(err, &unknown) {
					t.Fatal("expected unknown", err)
				}
				want := 0
				if strings.HasPrefix(mode, "register-") {
					want = 1
				}
				if apps.registers != want {
					t.Fatal("unknown mutated again", apps.registers)
				}
			}
			if apps.reads != 2 {
				t.Fatal("reconciliation must read once", apps.reads)
			}
		})
	}
}

func TestAConnectWithoutAShownEntryRemovesNothing(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	if rec := connectRow(t, h, pendingRows(t, h)[0]); rec.Code != 200 || apps.removes != 0 {
		t.Fatalf("a first registration called Remove: %d removes %d", rec.Code, apps.removes)
	}
}

func TestApplicationRoutesNeedTheKey(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	for _, path := range []string{"/apps/pending", "/apps/pending/connect", "/activity/app-requests"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "127.0.0.1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without the key: %d", path, rec.Code)
		}
	}
	if apps.registers != 0 {
		t.Fatal("a call without the key registered")
	}
}

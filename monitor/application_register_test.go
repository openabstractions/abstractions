package main

import (
	"context"
	"encoding/json"
	"html"
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
// adds an entry under a free name, Remove deletes one.
type applicationDirectoryFake struct {
	entries   map[string]fwire.ApplicationDescriptor
	registers int
	removes   int
}

func (f *applicationDirectoryFake) Register(_ context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	f.registers++
	if _, ok := f.entries[d.Name]; ok {
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict}, nil
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

func applicationPanel(t *testing.T, apps *applicationDirectoryFake, manifest string) (*servicePanel, http.Handler) {
	t.Helper()
	request, page, err := parseApplicationStart(writeApplicationManifest(t, manifest), "", "overview")
	if err != nil || page != "register-application" {
		t.Fatalf("start: %q %v", page, err)
	}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}}
	p.appRequest.register = request.register
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

// shownDigest reads the digest the page's Register button sends back.
func shownDigest(t *testing.T, page string) string {
	t.Helper()
	return buttonDigest(t, page, "/application/register/confirm")
}

func buttonDigest(t *testing.T, page, path string) string {
	t.Helper()
	marker := `data-path="` + path + `" data-confirm="true" data-shown="`
	start := strings.Index(page, marker)
	if start < 0 {
		t.Fatalf("no confirm button for %s on the page: %s", path, page)
	}
	rest := page[start+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

func TestRegisterApplicationAfterTheConfirmation(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	program := applicationTestProgram(t)
	_, h := applicationPanel(t, apps, manifestText(program, "Example"))
	page := applicationCall(t, h, "GET", "/application/register", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Example") || apps.registers != 0 {
		t.Fatalf("page %d registers %d: %s", page.Code, apps.registers, page.Body.String())
	}
	shown := shownDigest(t, page.Body.String())
	rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Registered Example.") {
		t.Fatalf("confirm %d: %s", rec.Code, rec.Body.String())
	}
	if got := apps.entries["example"]; got.Program != program || got.Activation == nil || got.Activation.Arguments[1] != "<b>" {
		t.Fatalf("registered %+v", got)
	}
	again := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`)
	if again.Code != http.StatusConflict || apps.registers != 1 {
		t.Fatalf("a second click registered again: %d", again.Code)
	}
}

// A manifest whose title is a product name, in any case or spacing, is
// refused: the page says so in one sentence and offers no Register, and a
// confirm sent anyway registers nothing.
func TestRegistrationRefusesAReservedTitle(t *testing.T) {
	for _, title := range []string{"OpenAbstractions", "openabstractions panel"} {
		apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
		_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), title))
		page := applicationCall(t, h, "GET", "/application/register", "").Body.String()
		if !strings.Contains(page, html.EscapeString(reservedTitleRefusal)) || strings.Contains(page, `data-path="/application/register/confirm"`) {
			t.Fatalf("%q: the page offers Register or no refusal: %s", title, page)
		}
		rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), reservedTitleRefusal) || apps.registers != 0 {
			t.Fatalf("%q: confirm %d registers %d: %s", title, rec.Code, apps.registers, rec.Body.String())
		}
	}
}

func TestCancelRegistersNothing(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	rec := applicationCall(t, h, "POST", "/application/register/cancel", `{"confirm":false,"shown":""}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Nothing was registered.") {
		t.Fatalf("cancel %d: %s", rec.Code, rec.Body.String())
	}
	late := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`)
	if late.Code != http.StatusConflict || apps.registers != 0 || len(apps.entries) != 0 {
		t.Fatalf("a cancelled registration was written: %d %v", late.Code, apps.entries)
	}
	if rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":false,"shown":""}`); rec.Code != http.StatusBadRequest && rec.Code != http.StatusConflict {
		t.Fatalf("an unconfirmed click: %d", rec.Code)
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

func TestReplacementShowsBothAndNeedsItsOwnClick(t *testing.T) {
	program := applicationTestProgram(t)
	old := fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example 1"}
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": old}}
	_, h := applicationPanel(t, apps, manifestText(program, "Example 2"))
	page := applicationCall(t, h, "GET", "/application/register", "").Body.String()
	if !strings.Contains(page, "Registered now") || !strings.Contains(page, "Example 1") || !strings.Contains(page, "Replacement") || !strings.Contains(page, "Example 2") {
		t.Fatalf("the replacement page does not show both entries: %s", page)
	}
	// A click that saw no entry does not replace one.
	if rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`); rec.Code != http.StatusConflict || apps.entries["example"].Title != "Example 1" {
		t.Fatalf("a click on a stale page replaced the entry: %d", rec.Code)
	}
	shown := shownDigest(t, page)
	rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != 200 || apps.entries["example"].Title != "Example 2" || apps.removes != 1 {
		t.Fatalf("replace %d: %s %+v", rec.Code, rec.Body.String(), apps.entries)
	}
}

func TestRemoveAfterTheConfirmation(t *testing.T) {
	program := applicationTestProgram(t)
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": {Name: "example", Program: program, Title: "Example"}}}
	request, page, err := parseApplicationStart("", "example", "overview")
	if err != nil || page != "remove-application" {
		t.Fatal(page, err)
	}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}}
	p.appRequest.remove = request.remove
	h := p.handler(applicationTestKey)
	body := applicationCall(t, h, "GET", "/application/remove", "").Body.String()
	shown := buttonDigest(t, body, "/application/remove/confirm")
	if shown == "" || !strings.Contains(body, "Example") || len(apps.entries) != 1 {
		t.Fatalf("remove page: %s", body)
	}
	if rec := applicationCall(t, h, "POST", "/application/remove/cancel", `{"confirm":false,"shown":""}`); rec.Code != 200 || len(apps.entries) != 1 {
		t.Fatalf("cancel removed: %d", rec.Code)
	}
	p.appRequest.remove, p.appRequest.done = "example", ""
	rec := applicationCall(t, h, "POST", "/application/remove/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != 200 || len(apps.entries) != 0 || !strings.Contains(rec.Body.String(), "Removed Example.") {
		t.Fatalf("remove %d: %s", rec.Code, rec.Body.String())
	}
}

func TestTheConfirmationPageEscapesManifestText(t *testing.T) {
	program := filepath.Join(applicationTestDir(t), `a&b'c`, "a.exe")
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(program, `<script>alert(1)</script>`))
	page := applicationCall(t, h, "GET", "/application/register", "").Body.String()
	for _, raw := range []string{"<script>alert(1)", "a&b'c", "<b>"} {
		if strings.Contains(page, raw) {
			t.Errorf("the page carries raw manifest text %q", raw)
		}
	}
	for _, escaped := range []string{"&lt;script&gt;alert(1)&lt;/script&gt;", "a&amp;b&#39;c", "&lt;b&gt;"} {
		if !strings.Contains(page, escaped) {
			t.Errorf("the page lacks the escaped text %q", escaped)
		}
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
		if err := announcePanelURL(&out, "http://127.0.0.1:50000/application/register?k=private-key", false, request); err != nil {
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
	request, _, err := parseApplicationStart(writeApplicationManifest(t, manifestText(program, "Example 2")), "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}}
	p.appRequest.register = request.register
	h := p.handler(applicationTestKey)
	shown := shownDigest(t, applicationCall(t, h, "GET", "/application/register", "").Body.String())
	rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":"`+shown+`"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Example 1 is registered again") {
		t.Fatalf("failed replacement %d: %s", rec.Code, rec.Body.String())
	}
	if got := apps.entries["example"]; got.Title != "Example 1" || apps.removes != 1 {
		t.Fatalf("the earlier entry was not restored: %+v removes %d", apps.entries, apps.removes)
	}
}

func TestAConfirmationWithoutAShownEntryRemovesNothing(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	if rec := applicationCall(t, h, "POST", "/application/register/confirm", `{"confirm":true,"shown":""}`); rec.Code != 200 || apps.removes != 0 {
		t.Fatalf("a first registration called Remove: %d removes %d", rec.Code, apps.removes)
	}
}

func TestApplicationPagesNeedTheKey(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	_, h := applicationPanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	for _, path := range []string{"/application/register", "/application/remove"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "127.0.0.1"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s without the key: %d", path, rec.Code)
		}
	}
}

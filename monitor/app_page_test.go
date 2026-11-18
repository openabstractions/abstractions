package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
)

// appPageDirectory is an in-memory registry for the app page: Observe lists
// its local entries, Activate and Remove record the name they were given.
type appPageDirectory struct {
	entries   []fwire.ApplicationEntry
	activated []string
	removed   []string
	activate  fwire.ApplicationActivationOutcome
	outcome   fwire.ApplicationOutcome
}

func (f *appPageDirectory) Register(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeForbidden}, nil
}

func (f *appPageDirectory) Remove(_ context.Context, name string) (fwire.ApplicationChange, error) {
	f.removed = append(f.removed, name)
	for i, e := range f.entries {
		if e.Descriptor.Name == name {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
		}
	}
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeUnknown}, nil
}

func (f *appPageDirectory) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	outcome := f.outcome
	if outcome == 0 {
		outcome = fwire.ApplicationOutcomePage
	}
	return fwire.ApplicationPage{Outcome: outcome, Applications: f.entries}, nil
}

func (f *appPageDirectory) Activate(_ context.Context, name string) (fwire.ApplicationActivationResult, error) {
	f.activated = append(f.activated, name)
	return fwire.ApplicationActivationResult{Outcome: f.activate, Started: true}, nil
}

const appPageKey = "app-page-key"

// appPagePanel serves the Panel with an in-memory registry holding one app
// whose title is markup, and no runtime: the caller's account stays unknown.
func appPagePanel(t *testing.T, activation bool) (*appPageDirectory, *servicePanel) {
	t.Helper()
	own(t)
	principal, _, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	absent := client.NewVerified(listen.Endpoint(fmt.Sprintf("absent-app-page-%d", time.Now().UnixNano())), listen.ServerExpectation{Principal: principal, Program: exe})
	previous := panelMachine
	panelMachine = func() *facade.Machine { return absent }
	t.Cleanup(func() { panelMachine = previous })
	d := fwire.ApplicationDescriptor{Name: "example", Program: filepath.Join(t.TempDir(), "Example", "example.exe"), Title: "Example <b>"}
	if activation {
		d.Activation = &fwire.ApplicationActivationRecipe{Readiness: fwire.ApplicationInterface{Name: "ready", Protocol: "example", Contract: "example/ready@1"}, ReadinessTimeoutMs: 1000}
	} else {
		d.StartGuidance = "Open it from the Start menu."
	}
	apps := &appPageDirectory{entries: []fwire.ApplicationEntry{{Descriptor: d, Instances: []fwire.ApplicationInstance{{Instance: "one"}}, Scope: fwire.ScopeLocal}}, activate: fwire.ApplicationActivationOutcomeReady}
	p := newTestPanel(t)
	p.comfyApps = apps
	return apps, p
}

func appPageGet(t *testing.T, p *servicePanel, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	req.Host = "127.0.0.1"
	rec := httptest.NewRecorder()
	p.handler(appPageKey).ServeHTTP(rec, req)
	return rec
}

func appPagePost(t *testing.T, p *servicePanel, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Host = "127.0.0.1"
	req.Header.Set("X-Panel-Key", appPageKey)
	rec := httptest.NewRecorder()
	p.handler(appPageKey).ServeHTTP(rec, req)
	return rec
}

// The app page shows one registered app on arrival: its breadcrumb title,
// its sections, Start from its recipe and Remove, with every app-supplied
// text escaped and no program path on the page. It passes the Panel rules.
func TestAppPageShowsTheRegisteredApp(t *testing.T) {
	apps, p := appPagePanel(t, true)
	rec := appPageGet(t, p, "/app/example?k="+appPageKey+"&platform=windows")
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	doc := rec.Body.String()
	for _, want := range []string{
		`<h1 class="oa-pagehead-title"><a href="/?k=` + appPageKey + `">Apps</a> › Example &lt;b&gt;</h1>`,
		`document.title="Apps › Example \u003cb\u003e";`,
		`<section id="app-models" class="oa-group" hidden><h2>Models it uses</h2>`,
		`function appNeedsInference(){`,
		`<section id="app-permissions" class="oa-group"><h2>Permissions</h2>`,
		`<section id="app-answering" class="oa-group" hidden><h2>Answering for you</h2>`,
		`<section id="app-activity" class="oa-group"><h2>Recent activity</h2>`,
		`</h1></div><div class="oa-pagehead-action"><button id="app-open" data-status="app-open-status" disabled>Running</button></div></header>`,
		`<p class="oa-status" id="app-open-status" role="status" aria-live="polite" data-state="idle"></p>`,
		`<button id="app-remove">Remove…</button>`,
		`class="oa-nav-item active" href="/?k=` + appPageKey + `"`,
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	if order := []string{"app-open", "app-open-status", "app-models", "app-permissions", "app-answering", "app-activity", "app-remove-section"}; !inOrder(doc, order) {
		t.Errorf("sections out of order %v", order)
	}
	if strings.Contains(doc, "<b>") {
		t.Error("an app-supplied title reached the page as markup")
	}
	if found := lintPanelPage(doc, loadRawValueShapes(t)); len(found) != 0 {
		t.Errorf("the app page breaks the Panel rules: %v", found)
	}
	if len(apps.activated)+len(apps.removed) != 0 {
		t.Error("opening the page started or removed the app")
	}

	mac := appPageGet(t, p, "/app/example?k="+appPageKey+"&platform=macos&shell=native").Body.String()
	if !strings.Contains(mac, `document.title="Example \u003cb\u003e";`) || strings.Contains(mac, "<h1") || strings.Contains(mac, `class="oa-nav"`) {
		t.Error("a native macOS host takes the app's name as its title and draws its own navigation")
	}
}

func TestAppPageGeneralApplicationHasRegisteredStateWithoutAnnouncements(t *testing.T) {
	apps, panel := appPagePanel(t, true)
	apps.entries[0].Descriptor.Name = "private-notes-key"
	apps.entries[0].Descriptor.Title = "Plain Notes"
	apps.entries[0].Instances = nil
	response := appPageGet(t, panel, "/app/private-notes-key?k="+appPageKey+"&platform=windows")
	if response.Code != http.StatusOK {
		t.Fatalf("status %d: %s", response.Code, response.Body)
	}
	doc := response.Body.String()
	for _, want := range []string{`Apps › Plain Notes`, `<div class="oa-pagehead-action"><button id="app-open" class="oa-primary" data-status="app-open-status">Open</button></div></header>`, `<section id="app-models" class="oa-group" hidden>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("general app page lacks %q", want)
		}
	}
	if strings.Contains(doc, "Not running") || strings.Contains(doc, ">private-notes-key<") {
		t.Error("announcement absence became a process claim or a registration key became visible text")
	}
	if len(apps.activated)+len(apps.removed) != 0 {
		t.Error("opening a general app page mutated the application")
	}
}

func TestAppPageOffersPicturePermissionReviewForTheDeclaredRegistration(t *testing.T) {
	apps, panel := appPagePanel(t, true)
	const linkText = "Review picture workflow permissions"
	ordinary := appPageGet(t, panel, "/app/example?k="+appPageKey)
	if ordinary.Code != http.StatusOK || strings.Contains(ordinary.Body.String(), linkText) {
		t.Fatalf("ordinary app permission review: status %d", ordinary.Code)
	}
	apps.entries[0].Descriptor.Name = "comfyui"
	picture := appPageGet(t, panel, "/app/comfyui?k="+appPageKey)
	if picture.Code != http.StatusOK || !strings.Contains(picture.Body.String(), linkText) {
		t.Fatalf("declared picture app permission review: status %d", picture.Code)
	}
}

// withoutOwnPrograms has the program titles know no OpenAbstractions program
// for one test, whose asking program is the test binary.
func withoutOwnPrograms(t *testing.T) {
	saved := ownProgramTitles
	ownProgramTitles = func() []oaProgramTitle { return nil }
	t.Cleanup(func() { ownProgramTitles = saved })
}

func inOrder(doc string, ids []string) bool {
	at := 0
	for _, id := range ids {
		i := strings.Index(doc[at:], `id="`+id+`"`)
		if i < 0 {
			return false
		}
		at += i
	}
	return true
}

// An app without a start recipe shows its own start guidance and no Start
// button; a name no app is registered under shows one sentence; a malformed
// name is not a page, and the page needs the Panel key.
func TestAppPageWithoutRecipeMissingAppAndKey(t *testing.T) {
	_, p := appPagePanel(t, false)
	doc := appPageGet(t, p, "/app/example?k="+appPageKey).Body.String()
	if !strings.Contains(doc, `<span class="oa-row-caption">Open it from the Start menu.</span>`) || strings.Contains(doc, `id="app-open"`) {
		t.Error("an app without an open verb shows its guidance and no Open")
	}
	missing := appPageGet(t, p, "/app/nothing?k="+appPageKey)
	if missing.Code != 200 || !strings.Contains(missing.Body.String(), `<p class="oa-empty">This app is no longer registered.</p>`) {
		t.Errorf("missing app: %d %s", missing.Code, missing.Body)
	}
	if found := lintPanelPage(missing.Body.String(), loadRawValueShapes(t)); len(found) != 0 {
		t.Errorf("the missing-app page breaks the Panel rules: %v", found)
	}
	if rec := appPageGet(t, p, "/app/example"); rec.Code != 403 {
		t.Errorf("no key: %d", rec.Code)
	}
	if rec := appPageGet(t, p, "/app/Not%20A%20Name?k="+appPageKey); rec.Code != 404 {
		t.Errorf("malformed name: %d", rec.Code)
	}
	if rec := appPageGet(t, p, "/apps/registrations?source=applications&wait_ms=0"); rec.Code != 403 {
		t.Errorf("the registry read keeps its own guarded route: %d", rec.Code)
	}
	if rec := appPageGet(t, p, "/app/registrations?k="+appPageKey); rec.Code != 200 || !strings.Contains(rec.Body.String(), "This app is no longer registered.") {
		t.Errorf("an app named like the registry route still has its own page: %d", rec.Code)
	}
	if item, ok := navItemByPath("/app/example"); !ok || item.ID != "status" {
		t.Errorf("an app page belongs to Apps in the navigation: %+v", item)
	}
}

// Apps, the app page and Permissions call oaReady themselves, so each
// declares data-ready-owner="page" on an element of its served markup, in
// the browser and the native shell alike; no script sets it.
func TestPagesThatCallOaReadyOwnTheirReadiness(t *testing.T) {
	_, p := appPagePanel(t, true)
	for _, path := range []string{"/", "/rights", "/app/example"} {
		for _, shell := range []string{"", "&shell=native"} {
			req := httptest.NewRequest("GET", path+"?k="+appPageKey+shell, nil)
			req.Host = "127.0.0.1"
			rec := httptest.NewRecorder()
			p.handler(appPageKey).ServeHTTP(rec, req)
			doc := rec.Body.String()
			if !strings.Contains(doc, `<header class="oa-pagehead" data-ready-owner="page">`) || !strings.Contains(doc, "oaReady()") {
				t.Errorf("%s%s: the served head does not own readiness", path, shell)
			}
			if strings.Contains(doc, "readyOwner") || strings.Contains(doc, "setAttribute('data-ready-owner'") {
				t.Errorf("%s%s: a script sets data-ready-owner", path, shell)
			}
		}
	}
}

// Every sentence names a program by one rule (program_titles.go):
// OpenAbstractions' own programs by product name, a registered app by its
// title, any other program by its file stem and "(not registered)". A
// program named openabstractions.exe in some other folder is no
// OpenAbstractions program. The runtime beside this Panel counts when this
// Panel's signer signed it (product standing).
func TestProgramTitlesNameOwnProgramsAndRegisteredApps(t *testing.T) {
	apps, p := appPagePanel(t, false)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	runtimeProgram := filepath.Join(filepath.Dir(exe), "openabstractions"+suffix)
	impostor := filepath.Join(t.TempDir(), "openabstractions"+suffix)
	stranger := filepath.Join(t.TempDir(), "stranger.exe")
	signedByPanelSigner(t, runtimeProgram)
	names := p.programTitler(context.Background())
	want := map[string]string{exe: "OpenAbstractions Panel", runtimeProgram: "OpenAbstractions", apps.entries[0].Descriptor.Program: "Example <b>",
		impostor: "openabstractions (not registered)", stranger: "stranger (not registered)"}
	got := names.names([]string{exe, runtimeProgram, apps.entries[0].Descriptor.Program, impostor, stranger})
	for program, title := range want {
		if got[program] != title {
			t.Errorf("%s named %q, want %q", program, got[program], title)
		}
	}
	if _, ok := names.title(impostor); ok {
		t.Error("an openabstractions program in another folder took a product title")
	}
	if got := firstUseSentence(trayPublisher{Names: names}, impostor, "abstraction.model/lookup", "hf"); !strings.HasPrefix(got, "openabstractions (not registered) wants to ") {
		t.Errorf("the native prompt's sentence reads %q", got)
	}
	if got := firstUseSentence(trayPublisher{Names: names}, apps.entries[0].Descriptor.Program, "abstraction.model/lookup", "hf"); !strings.HasPrefix(got, "Example <b> wants to ") {
		t.Errorf("a registered app's prompt sentence reads %q", got)
	}
}

// The native prompt names a program by its verified signed description
// first and by its registered title only when it is unsigned; Panel pages
// put the registered title first.
func TestPromptPutsTheSignedDescriptionFirstAndPagesTheTitle(t *testing.T) {
	program := filepath.Join(t.TempDir(), "coder.exe")
	names := newProgramTitler([]fwire.ApplicationEntry{{Descriptor: fwire.ApplicationDescriptor{Name: "coder", Program: program, Title: "Coder"}, Scope: fwire.ScopeLocal}})
	signed := trayPublisher{Trusted: true, Subject: "Example Corp", Description: "Coder Studio"}
	if got := programDisplayName(signed.forPrompt(names), program); got != "Coder Studio" {
		t.Errorf("the prompt names a signed program %q", got)
	}
	if got := programDisplayName(signed.withNames(names), program); got != "Coder" {
		t.Errorf("a page names a signed registered program %q", got)
	}
	unsigned := trayPublisher{Description: "Forged Name"}
	if got := programDisplayName(unsigned.forPrompt(names), program); got != "Coder" {
		t.Errorf("the prompt names an unsigned registered program %q", got)
	}
	if got := programDisplayName(unsigned.forPrompt(nil), program); got != "coder" {
		t.Errorf("the prompt names an unsigned unregistered program %q", got)
	}
	view := trayQuestionViewForAsker(askclient.RecordMetadata{ID: "q", Asker: "coder.exe", Text: "coder.exe asks x", Options: []string{"once"}}, trayPublisher{}, nil, "")
	if view.Instruction != "coder asks x" {
		t.Errorf("an asker without a program reads by its stem: %q", view.Instruction)
	}
}

// A registered title that is a product name, in any case or spacing, never
// reads as OpenAbstractions: it reads as the program's stem and
// "(registered app)".
func TestReservedTitlesReadAsTheRegisteredProgram(t *testing.T) {
	// Look-alikes: a zero-width space, a full-width letter, a mathematical
	// bold letter, a Cyrillic о, a small capital, a circled and a superscript
	// letter, the digit zero, and a title that only starts with the name.
	for _, title := range []string{"OpenAbstractions", "openabstractions panel", " Open Abstractions ", "OPENABSTRACTIONSPANEL",
		"Open\u200bAbstractions", "Ｏpen Abstractions", "\U0001D40Epen Abstractions", "\u043epen Abstractions", "OpenAbstractions Helper", "Open-Abstractions!",
		"\u1d0fpen Abstractions", "\u24c4pen Abstractions", "\u1d52pen Abstractions", "0penAbstractions"} {
		d := fwire.ApplicationDescriptor{Name: "x", Program: filepath.Join(t.TempDir(), "helper.exe"), Title: title}
		if got := appPageTitle(d); got != "helper (registered app)" {
			t.Errorf("title %q reads %q", title, got)
		}
	}
	if got := appPageTitle(fwire.ApplicationDescriptor{Name: "x", Program: "C:\\x\\notes.exe", Title: "Notes for OpenAbstractions"}); got != "Notes for OpenAbstractions" {
		t.Errorf("a title that only names the product reads %q", got)
	}
}

// Every expander on Permissions, served or built by its script, carries
// the shared chevron in its summary, as oaExpander draws it.
func TestPermissionsExpandersCarryTheChevron(t *testing.T) {
	summaries := regexp.MustCompile(`(?s)<summary[^>]*>(.*?)</summary>`).FindAllStringSubmatch(rightsPage, -1)
	if len(summaries) == 0 {
		t.Fatal("no expander on Permissions")
	}
	for _, m := range summaries {
		if !strings.HasSuffix(m[1], `<span class="oa-row-chevron" aria-hidden="true"></span>`) {
			t.Errorf("summary without the chevron: %s", m[1])
		}
	}
	if strings.Contains(rightsPage, "summary.textContent=") {
		t.Error("a script-built summary is written without rightsSummary")
	}
}

// Answering access names a registered app by its title, and any other
// program by its file stem and "(not registered)", never its file name.
func TestAssistantNameReadsTheRegisteredTitle(t *testing.T) {
	apps, p := appPagePanel(t, false)
	if got := p.assistantName(context.Background(), apps.entries[0].Descriptor.Program); got != "Example <b>" {
		t.Errorf("registered program named %q", got)
	}
	other := filepath.Join(t.TempDir(), "other.exe")
	if got := p.assistantName(context.Background(), other); got != "other (not registered)" {
		t.Errorf("unregistered program named %q", got)
	}
}

// Start and Remove act only on the entry the page showed, after a confirmed
// request, and answer one sentence.
func TestAppPageStartAndRemove(t *testing.T) {
	apps, p := appPagePanel(t, true)
	shown := applicationDigest(&apps.entries[0].Descriptor)
	body := func(confirm bool, digest string) string {
		raw, _ := json.Marshal(appActionBody{Confirm: confirm, Shown: digest})
		return string(raw)
	}
	if rec := appPagePost(t, p, "/app/example/start", body(false, shown)); rec.Code != 400 {
		t.Errorf("unconfirmed start: %d", rec.Code)
	}
	if rec := appPagePost(t, p, "/app/example/start", body(true, "other")); rec.Code != 409 || len(apps.activated) != 0 {
		t.Errorf("start of a changed entry: %d %v", rec.Code, apps.activated)
	}
	rec := appPagePost(t, p, "/app/example/start", body(true, shown))
	var reply appActionReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Outcome != "ready" || reply.Message != "Example <b> is open." || len(apps.activated) != 1 || apps.activated[0] != "example" {
		t.Errorf("start: %d %s %v", rec.Code, rec.Body, apps.activated)
	}
	if rec := appPageGet(t, p, "/app/example/start?k="+appPageKey); rec.Code != 405 {
		t.Errorf("a GET starts nothing: %d", rec.Code)
	}
	rec = appPagePost(t, p, "/app/example/remove", body(true, shown))
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || reply.Message != "Removed Example <b> from OpenAbstractions." || len(apps.removed) != 1 {
		t.Errorf("remove: %d %s", rec.Code, rec.Body)
	}
	if rec := appPagePost(t, p, "/app/example/remove", body(true, shown)); rec.Code != 409 || len(apps.removed) != 1 {
		t.Errorf("a second remove of a gone entry: %d", rec.Code)
	}
}

// An app registered through the facade directory (pretend-lemonade
// --register) is listed by Apps from applications@2 while the native
// applications@3 owner holds no entry for it. Its page shows it, and Start
// and Remove act on the directory entry (panel audit 2026-11-01: "This app
// is no longer registered." on a row Apps listed).
func TestAppPageFindsAnAppTheApplicationsDirectoryLists(t *testing.T) {
	directory, p := appPagePanel(t, true)
	native := &appPageDirectory{}
	saved := appPageNative
	appPageNative = func(*servicePanel, context.Context, string) (setupApplications, error) { return native, nil }
	t.Cleanup(func() { appPageNative = saved })
	doc := appPageGet(t, p, "/app/example?k="+appPageKey+"&platform=windows").Body.String()
	if strings.Contains(doc, `<p class="oa-empty">This app is no longer registered.</p>`) || !strings.Contains(doc, `<button id="app-open" data-status="app-open-status" disabled>Running</button>`) {
		t.Fatalf("a directory-registered app has no page:\n%s", doc)
	}
	shown := applicationDigest(&directory.entries[0].Descriptor)
	program := directory.entries[0].Descriptor.Program
	raw, _ := json.Marshal(appActionBody{Confirm: true, Shown: shown})
	if rec := appPagePost(t, p, "/app/example/start", string(raw)); rec.Code != 200 || len(directory.activated) != 1 || len(native.activated) != 0 {
		t.Errorf("start of a directory entry: %d %s", rec.Code, rec.Body)
	}
	if rec := appPagePost(t, p, "/app/example/remove", string(raw)); rec.Code != 200 || len(directory.removed) != 1 || len(native.removed) != 0 {
		t.Errorf("remove of a directory entry: %d %s", rec.Code, rec.Body)
	}

	// An entry the native owner lists is still started through it.
	native.entries = []fwire.ApplicationEntry{{Descriptor: fwire.ApplicationDescriptor{Name: "native-app", Program: program, Title: "Native app", Activation: &fwire.ApplicationActivationRecipe{}}, Scope: fwire.ScopeLocal}}
	native.activate = fwire.ApplicationActivationOutcomeReady
	raw, _ = json.Marshal(appActionBody{Confirm: true, Shown: applicationDigest(&native.entries[0].Descriptor)})
	if rec := appPagePost(t, p, "/app/native-app/start", string(raw)); rec.Code != 200 || len(native.activated) != 1 {
		t.Errorf("start of a native entry: %d %s", rec.Code, rec.Body)
	}
}

func TestAppPageDoesNotCallAnUnreadableDirectoryEntryRemoved(t *testing.T) {
	directory, panel := appPagePanel(t, true)
	saved := appPageNative
	appPageNative = func(*servicePanel, context.Context, string) (setupApplications, error) {
		return nil, fmt.Errorf("native service unavailable")
	}
	t.Cleanup(func() { appPageNative = saved })
	shown := applicationDigest(&directory.entries[0].Descriptor)
	raw, _ := json.Marshal(appActionBody{Confirm: true, Shown: shown})
	for _, outcome := range []fwire.ApplicationOutcome{fwire.ApplicationOutcomeStale, fwire.ApplicationOutcomeForbidden, fwire.ApplicationOutcomeUnavailable} {
		directory.outcome = outcome
		doc := appPageGet(t, panel, "/app/example?k="+appPageKey).Body.String()
		if !strings.Contains(doc, "The list of apps is unavailable right now.") || strings.Contains(doc, "This app is no longer registered.") {
			t.Errorf("%s was presented as absence: %s", outcome, doc)
		}
		for _, action := range []string{"start", "remove"} {
			if reply := appPagePost(t, panel, "/app/example/"+action, string(raw)); reply.Code != http.StatusConflict {
				t.Errorf("%s on %s: %d", action, outcome, reply.Code)
			}
		}
	}
	if len(directory.activated) != 0 || len(directory.removed) != 0 {
		t.Fatal("unreadable state allowed an action")
	}
	directory.outcome, directory.entries = fwire.ApplicationOutcomePage, nil
	doc := appPageGet(t, panel, "/app/example?k="+appPageKey).Body.String()
	if !strings.Contains(doc, "The list of apps is unavailable right now.") {
		t.Fatal("unreadable native owner became proof of removal")
	}
}

// appPageSlowOpen is a registry whose Activate waits until its caller's
// deadline: the person has not answered, or the app is still starting.
type appPageSlowOpen struct{ *appPageDirectory }

func (f appPageSlowOpen) Activate(ctx context.Context, name string) (fwire.ApplicationActivationResult, error) {
	f.activated = append(f.activated, name)
	<-ctx.Done()
	return fwire.ApplicationActivationResult{}, ctx.Err()
}

// Owner report on R20c, 2026-11-10: Open showed an Allow, then "This didn't
// work." with the details "context deadline exceeded". An Open whose wait
// runs out now answers not_ready with a sentence, and the budget covers the
// wait policy's 40 s answer and the app's 30 s start.
func TestAppPageOpenThatOutwaitsItsBudgetSaysWhatIsPending(t *testing.T) {
	if appOpenBudget < 70*time.Second {
		t.Fatalf("Open waits %v, less than the 40 s answer and the 30 s start", appOpenBudget)
	}
	apps, p := appPagePanel(t, true)
	p.comfyApps = appPageSlowOpen{apps}
	saved := appOpenBudget
	appOpenBudget = 50 * time.Millisecond
	t.Cleanup(func() { appOpenBudget = saved })
	raw, _ := json.Marshal(appActionBody{Confirm: true, Shown: applicationDigest(&apps.entries[0].Descriptor)})
	rec := appPagePost(t, p, "/app/example/start", string(raw))
	var reply appActionReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != 200 || reply.Outcome != "not_ready" ||
		reply.Message != "Example <b> is still opening. If OpenAbstractions asked you to allow it, answer, then select Open again." {
		t.Fatalf("an Open that outwaited its budget: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("the reply names the bare deadline: %s", rec.Body)
	}
}

// Each runtime answer to Open reads as one sentence.
func TestAppOpenSentences(t *testing.T) {
	for _, c := range []struct {
		result fwire.ApplicationActivationResult
		want   string
	}{
		{fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeReady}, "ComfyUI is open."},
		{fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeNotReady, Reason: "consent"}, "OpenAbstractions is waiting for your answer to open ComfyUI. Answer its question, then select Open again."},
		{fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeNotReady, Reason: "owned_host"}, "ComfyUI is opening."},
		{fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeForbidden, Reason: "consent"}, "You answered Not now. ComfyUI stays closed."},
		{fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeForbidden, Reason: "rights"}, "OpenAbstractions did not let the Panel open ComfyUI."},
		{fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeIdentityRefused, Reason: "owned_host"}, "OpenAbstractions could not start ComfyUI's program."},
	} {
		if got := appOpenSentence("ComfyUI", c.result); got != c.want {
			t.Errorf("%+v: %q, want %q", c.result, got, c.want)
		}
	}
}

// The Panel's applications@3 calls carry a budget that outlasts an Open;
// the transport's 5 s default cut the Open while the person read the Allow.
func TestPanelApplicationsTransportOutlastsAnOpen(t *testing.T) {
	if got := panelApplicationsTransport(listen.FrameClient{Endpoint: "x"}).Timeout; got < appOpenBudget {
		t.Fatalf("applications@3 call budget %v is shorter than an Open's %v", got, appOpenBudget)
	}
	if got := panelApplicationsTransport(listen.FrameClient{Endpoint: "x", Timeout: time.Second}).Timeout; got != time.Second {
		t.Fatalf("an explicit budget was replaced: %v", got)
	}
}

// A refused Remove logs the service's outcome and reason and the contract the
// Panel used (owner R27, 2026-11-11: the log held no reason).
func TestAppRemoveFailureNamesTheRefusal(t *testing.T) {
	refused := appRemoveFailure(&appPageDirectory{}, "comfyui", fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict, Reason: "applications_v3"})
	want := map[string]string{"application.name": "comfyui", "application.outcome": "conflict", "application.reason": "applications_v3", "application.client": "legacy"}
	if !reflect.DeepEqual(refused, want) {
		t.Fatalf("refused Remove logged %v", refused)
	}
	if got := appRemoveFailure(&nativeApplications{}, "comfyui", fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeUnavailable, Reason: "message_binding"}); got["application.client"] != "native" || got["application.reason"] != "message_binding" {
		t.Fatalf("native refusal logged %v", got)
	}
	for _, outcome := range []fwire.ApplicationOutcome{fwire.ApplicationOutcomeApplied, fwire.ApplicationOutcomeUnknown} {
		if got := appRemoveFailure(&appPageDirectory{}, "comfyui", fwire.ApplicationChange{Outcome: outcome}); got != nil {
			t.Fatalf("%v logged as a failure: %v", outcome, got)
		}
	}
}

// Agent-ux 2026-11-22: a failed Open said only "OpenAbstractions could not
// start ComfyUI's program." It now says what the Panel can see, whether the
// program is still on this computer, and the next step; ComfyUI's step
// links to its Catalog row, where Connect restores the connector.
func TestAppOpenRefusedStartSaysReasonAndNextStep(t *testing.T) {
	apps, p := appPagePanel(t, true)
	apps.activate = fwire.ApplicationActivationOutcomeIdentityRefused
	raw, _ := json.Marshal(appActionBody{Confirm: true, Shown: applicationDigest(&apps.entries[0].Descriptor)})
	open := func() appActionReply {
		rec := appPagePost(t, p, "/app/example/start", string(raw))
		var reply appActionReply
		if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil || rec.Code != 200 {
			t.Fatalf("open: %d %s", rec.Code, rec.Body)
		}
		return reply
	}
	if reply := open(); reply.Message != "Example <b>'s program is no longer on this computer. Reinstall Example <b>, then select Open again." || reply.Next != "" {
		t.Fatalf("missing program: %+v", reply)
	}
	program := apps.entries[0].Descriptor.Program
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("inert"), 0o600); err != nil {
		t.Fatal(err)
	}
	if reply := open(); reply.Message != "Example <b>'s program did not start. Reinstall Example <b>, then select Open again." {
		t.Fatalf("present program: %+v", reply)
	}
	picture, _ := picturePackageIdentity()
	message, next := appStartFailure("ComfyUI", picture, filepath.Join(t.TempDir(), "comfyui-connector.exe"))
	if message != "ComfyUI's OpenAbstractions connector is no longer on this computer. Select Connect for ComfyUI in the Catalog to restore it." || next != "/setup#setup-row-"+string(officialPictureCatalogID()) {
		t.Fatalf("ComfyUI: %q %q", message, next)
	}
	doc := appPageGet(t, p, "/app/example?k="+appPageKey).Body.String()
	if !strings.Contains(doc, `<a id="app-open-next" href="#">Open Catalog</a>`) || !strings.Contains(doc, `id="app-open-next-line" hidden`) {
		t.Fatal("the page has no hidden Catalog link for the next step")
	}
}

package main

import (
	"context"
	"errors"
	facade "github.com/openabstractions/abstraction-facade/go"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestWorkspaceRegistrationRefusesANameOutsideTheRetainedArchive(t *testing.T) {
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}, registerReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
	var log []string
	descriptor := fwire.ApplicationDescriptor{Name: "unrelated-workspace", Program: filepath.Join(t.TempDir(), "launch-helper.exe")}
	done, err := officialPicture.registerWorkspaceEntry(context.Background(), apps, &orderedReads{log: &log}, descriptor, nil, nil)
	if err == nil || done != workspaceUnchanged || len(log) != 0 || apps.registerCalls != 0 || apps.removeCalls != 0 {
		t.Fatalf("foreign workspace affected registration: done=%v err=%v log=%v", done, err, log)
	}
}

func TestComfyRegistrationDoesNotRequireAnUnselectedPictureProvider(t *testing.T) {
	// Native package admission and receipt-bound paths are exercised by the
	// Setup native archive owners. Registration's existing owner consumes only
	// the native descriptor; it has no model/provider discovery dependency.
	previous := panelMachine
	panelMachine = func() *facade.Machine {
		t.Fatal("registration attempted engine discovery or policy access")
		return nil
	}
	t.Cleanup(func() { panelMachine = previous })
	descriptor := fwire.ApplicationDescriptor{Name: setup.ComfyApplicationName, Title: "ComfyUI", Program: filepath.Join(t.TempDir(), "launch-helper.exe")}
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}, registerReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
	var log []string
	created, err := createdOf(officialPicture.registerWorkspaceEntry(context.Background(), apps, &orderedReads{log: &log}, descriptor, nil, nil))
	if err != nil || !created || apps.registerCalls != 1 || apps.registered.Program != descriptor.Program {
		t.Fatalf("native registration depended on an unrelated provider: created=%v calls=%d error=%v", created, apps.registerCalls, err)
	}
}

type setupApplicationsFake struct {
	page          fwire.ApplicationPage
	registerCalls int
	registered    fwire.ApplicationDescriptor
	registerReply fwire.ApplicationChange
	removeCalls   int
	removeReply   fwire.ApplicationChange
	activateCalls int
	removed       []string
}

func (f *setupApplicationsFake) Register(_ context.Context, descriptor fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	f.registerCalls++
	f.registered = descriptor
	return f.registerReply, nil
}

func TestComfyRetireGuardKeepsAVersionTheEntryStarts(t *testing.T) {
	apps := filepath.Join(t.TempDir(), "OpenAbstractions", "apps", "comfyui")
	root := filepath.Join(apps, "1.0.0")
	entry := func(program string) *setupApplicationsFake {
		return &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage,
			Applications: []fwire.ApplicationEntry{{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Program: program}}}}}
	}
	// The installed version exists, and the entry names its program in
	// another spelling the volume resolves to the same file: in upper case on
	// Windows, whose volumes ignore case. File identity decides.
	python := filepath.Join(root, "launch-helper.exe")
	if err := os.MkdirAll(filepath.Dir(python), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(python, []byte("python"), 0o755); err != nil {
		t.Fatal(err)
	}
	spelled := python
	if runtime.GOOS == "windows" {
		spelled = strings.ToUpper(python)
	}
	p := &servicePanel{comfyApps: entry(spelled)}
	if err := p.comfyRetireGuard(context.Background(), "comfyui-app", root); err == nil {
		t.Fatal("the version the registered entry starts was released for removal")
	}
	p = &servicePanel{comfyApps: entry(filepath.Join(apps, "2.0.0", "python.exe"))}
	if err := p.comfyRetireGuard(context.Background(), "comfyui-app", root); err != nil {
		t.Fatalf("a version no entry starts was kept: %v", err)
	}
	p = &servicePanel{comfyApps: &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}}}
	if err := p.comfyRetireGuard(context.Background(), "comfyui-app", root); err == nil {
		t.Fatal("an unreadable entry released the version for removal")
	}
	if err := p.comfyRetireGuard(context.Background(), "lemonade-engine", root); err != nil {
		t.Fatalf("the ComfyUI guard held another package: %v", err)
	}
}
func (f *setupApplicationsFake) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	return f.page, nil
}
func (f *setupApplicationsFake) Remove(_ context.Context, name string) (fwire.ApplicationChange, error) {
	f.removed = append(f.removed, name)
	f.removeCalls++
	return f.removeReply, nil
}
func (f *setupApplicationsFake) Activate(context.Context, string) (fwire.ApplicationActivationResult, error) {
	f.activateCalls++
	return fwire.ApplicationActivationResult{Outcome: fwire.ApplicationActivationOutcomeNotReady}, nil
}

func TestComfyLegacyActivationRouteCannotStartOrInspectPrograms(t *testing.T) {
	ctx := context.Background()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}

	installed := filepath.Join(t.TempDir(), "python.exe")
	if err := os.WriteFile(installed, []byte("registered package"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installed, []byte("changed after registration"), 0o600); err != nil {
		t.Fatal(err)
	}

	apps := &setupApplicationsFake{}
	openCalls := 0
	p := &servicePanel{
		setupJournal: &journal,
		comfyPaths:   &setup.ComfyPaths{}, // cached paths from the earlier registration step
		comfyApps:    apps,
		comfyActivationOpen: func(context.Context, setup.Journal) (setup.ComfyPaths, error) {
			openCalls++
			data, err := os.ReadFile(installed)
			if err != nil {
				return setup.ComfyPaths{}, err
			}
			if string(data) != "registered package" {
				return setup.ComfyPaths{}, os.ErrInvalid
			}
			return setup.ComfyPaths{}, nil
		},
	}
	requestBody := `{"setup_plan_id":"` + plan.ID + `","confirm":true}`
	req := httptest.NewRequest("POST", "/setup/comfy/activate", strings.NewReader(requestBody))
	rec := httptest.NewRecorder()
	officialPicture.serveWorkspaceActivate(p, rec, req)
	if rec.Code != http.StatusGone {
		t.Fatalf("activation status = %d, want retired route; body=%s", rec.Code, rec.Body.String())
	}
	if openCalls != 0 {
		t.Fatalf("fresh install verifier calls = %d, want 0", openCalls)
	}
	if apps.activateCalls != 0 {
		t.Fatalf("public Applications.Activate called %d times after package mutation", apps.activateCalls)
	}
}

func TestPanelExplicitComfyActionDoesNotTrustCachedPaths(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	p := &servicePanel{comfyPaths: &setup.ComfyPaths{}}
	if _, err := p.openInstalledComfy(context.Background(), journal); err == nil {
		t.Fatal("cached paths bypassed the installed-package receipt check")
	}
}

func TestComfyRegistrationPreservesExistingDescriptors(t *testing.T) {
	want := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\OA\python.exe`, Title: "ComfyUI — OA build"}
	// Another program's entry names a program that exists: an entry whose
	// program is gone is stale, and Setup replaces it.
	existing := filepath.Join(t.TempDir(), "Other", "python.exe")
	if err := os.MkdirAll(filepath.Dir(existing), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("another program"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Run("same descriptor is idempotent", func(t *testing.T) {
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: want}}}}
		created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, nil))
		if err != nil || created || apps.registerCalls != 0 {
			t.Fatalf("same descriptor: created=%v calls=%d err=%v", created, apps.registerCalls, err)
		}
	})
	t.Run("different descriptor is not overwritten", func(t *testing.T) {
		other := want
		other.Program, other.Title = existing, "ComfyUI"
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: other}}}}
		_, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, nil)
		if err == nil || err.Error() != "A picture workspace entry for another program is already registered; it was preserved." {
			t.Fatalf("conflicting descriptor: %v", err)
		}
		if apps.registerCalls != 0 {
			t.Fatal("conflicting descriptor was overwritten")
		}
	})
	t.Run("an earlier OA build's descriptor is named and preserved", func(t *testing.T) {
		// The recipe before the launch check: same receipt-bound program, other arguments.
		earlier := want
		earlier.Activation = &fwire.ApplicationActivationRecipe{Arguments: []string{"-B", "-s", `C:\OA\ComfyUI\main.py`}}
		current := want
		current.Activation = &fwire.ApplicationActivationRecipe{Arguments: []string{"-B", "-s", `C:\OA\oa_comfyui_launch.py`}}
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: earlier}}}}
		_, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, current, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "from an earlier OpenAbstractions build; it was preserved") ||
			strings.Contains(err.Error(), "comfyui") {
			t.Fatalf("earlier descriptor: %v", err)
		}
		if apps.registerCalls != 0 {
			t.Fatal("earlier descriptor was overwritten")
		}
	})
	t.Run("an updated version replaces only Setup's own earlier entry", func(t *testing.T) {
		replaced := want
		replaced.Program = `C:\OA\apps\comfyui\1.0.0\python.exe`
		updated := want
		updated.Program = `C:\OA\apps\comfyui\2.0.0\python.exe`
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: replaced}}},
			registerReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
		created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, updated, []fwire.ApplicationDescriptor{replaced}, nil))
		if err != nil || !created || apps.removeCalls != 1 || apps.registerCalls != 1 || apps.registered.Program != updated.Program {
			t.Fatalf("earlier own entry not replaced: created=%v remove=%d register=%d err=%v", created, apps.removeCalls, apps.registerCalls, err)
		}
		foreign := replaced
		foreign.Program = existing
		foreign.Title = "someone else's entry"
		apps = &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: foreign}}}}
		if _, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, updated, []fwire.ApplicationDescriptor{replaced}, nil); err == nil || apps.removeCalls != 0 {
			t.Fatalf("an entry Setup did not build was removed: remove=%d err=%v", apps.removeCalls, err)
		}
		apps = &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: replaced}}},
			removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeForbidden}}
		if _, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, updated, []fwire.ApplicationDescriptor{replaced}, nil); err == nil || apps.registerCalls != 0 {
			t.Fatalf("a refused removal still registered the update: register=%d err=%v", apps.registerCalls, err)
		}
	})
	t.Run("registers only when missing", func(t *testing.T) {
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}, registerReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
		created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, nil))
		if err != nil || !created || apps.registerCalls != 1 || apps.registered.Program != want.Program {
			t.Fatalf("missing descriptor: created=%v calls=%d err=%v", created, apps.registerCalls, err)
		}
	})
}

func comfyRemoveRequest(p *servicePanel, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	officialPicture.serveWorkspaceRemove(p, rec, httptest.NewRequest("POST", "/setup/comfy/remove", strings.NewReader(body)))
	return rec
}

func TestComfyRemoveDeletesOnlyTheFixedEntryAfterConfirmation(t *testing.T) {
	entry := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\OA\python.exe`, Title: "ComfyUI for OpenAbstractions"}
	unrelated := fwire.ApplicationDescriptor{Name: "other-workspace", Program: `C:\OA\other.exe`, Title: "Other workspace"}
	newApps := func() *setupApplicationsFake {
		return &setupApplicationsFake{
			page:        fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: unrelated}, {Descriptor: entry}}},
			removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied},
		}
	}
	t.Run("an unconfirmed request removes nothing", func(t *testing.T) {
		apps := newApps()
		p := &servicePanel{comfyApps: apps}
		if rec := comfyRemoveRequest(p, `{}`); rec.Code != 400 || len(apps.removed) != 0 {
			t.Fatalf("unconfirmed: status=%d removed=%v", rec.Code, apps.removed)
		}
	})
	t.Run("the request cannot replace the declared target", func(t *testing.T) {
		apps := newApps()
		p := &servicePanel{comfyApps: apps}
		rec := comfyRemoveRequest(p, `{"confirm":true,"application":"other-workspace"}`)
		if rec.Code != http.StatusBadRequest || len(apps.removed) != 0 {
			t.Fatalf("caller-selected removal: status=%d removed=%v", rec.Code, apps.removed)
		}
	})
	t.Run("a confirmed request removes comfyui and writes one sentence", func(t *testing.T) {
		apps := newApps()
		p := &servicePanel{comfyApps: apps, comfyRegisteredDescriptor: &entry, comfyActivationStarted: true}
		rec := comfyRemoveRequest(p, `{"confirm":true}`)
		if rec.Code != 200 || len(apps.removed) != 1 || apps.removed[0] != "comfyui" {
			t.Fatalf("confirmed: status=%d removed=%v body=%s", rec.Code, apps.removed, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"message":"Removed ComfyUI for OpenAbstractions."`) {
			t.Fatalf("the reply does not name the removed application: %s", rec.Body.String())
		}
		if p.comfyRegisteredDescriptor != nil || p.comfyActivationStarted {
			t.Fatal("the Panel kept its memory of the removed entry")
		}
	})
	t.Run("a different live package cannot redirect the historical route", func(t *testing.T) {
		selected := *officialPicture
		selected.spec.Registration = unrelated.Name
		apps := newApps()
		p := &servicePanel{comfyApps: apps}
		rec := httptest.NewRecorder()
		selected.serveWorkspaceRemove(p, rec, httptest.NewRequest("POST", selected.retainedLegacy.Routes.Remove, strings.NewReader(`{"confirm":true}`)))
		if rec.Code != http.StatusOK || len(apps.removed) != 1 || apps.removed[0] != entry.Name {
			t.Fatalf("historical removal: status=%d removed=%v", rec.Code, apps.removed)
		}
	})

	t.Run("a refused removal is a conflict that names the reason", func(t *testing.T) {
		apps := newApps()
		apps.removeReply = fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeForbidden, Reason: "needs application.manage"}
		p := &servicePanel{comfyApps: apps}
		rec := comfyRemoveRequest(p, `{"confirm":true}`)
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "needs application.manage") {
			t.Fatalf("refused: status=%d body=%s", rec.Code, rec.Body.String())
		}
	})
	t.Run("GET is refused", func(t *testing.T) {
		rec := httptest.NewRecorder()
		officialPicture.serveWorkspaceRemove(&servicePanel{comfyApps: newApps()}, rec, httptest.NewRequest("GET", "/setup/comfy/remove", nil))
		if rec.Code != 405 {
			t.Fatalf("GET status = %d", rec.Code)
		}
	})
}

func TestWorkspaceStatusRetainsHistoricalProgramAndExplicitDescriptorChecks(t *testing.T) {
	ctx := context.Background()
	workspace := officialPicture.retainedWorkspaceRegistration()
	program := filepath.Join(t.TempDir(), "launch-helper.exe")
	descriptor := fwire.ApplicationDescriptor{Name: workspace.registration, Program: program, Title: "Current workspace"}
	listed := descriptor
	listed.Title = "Earlier workspace"
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{
		Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: listed}},
	}}
	shortcutReads := 0
	shortcut := func() bool { shortcutReads++; return false }
	if state, note := workspace.installedState(ctx, apps, program, shortcut); state != "no_shortcut" || note != "" {
		t.Fatalf("historical program readback = %q %q", state, note)
	}
	if status := workspace.status(ctx, apps, descriptor, shortcut); !status.Conflict || status.Foreign || status.Matching || status.Shortcut != nil {
		t.Fatalf("explicit descriptor status = %+v", status)
	}
	if shortcutReads != 1 || apps.removeCalls != 0 || apps.registerCalls != 0 {
		t.Fatalf("status affected the workspace: shortcut reads=%d removes=%d registers=%d", shortcutReads, apps.removeCalls, apps.registerCalls)
	}
}

func TestComfyConflictReplyOffersRemoveAndKeepsTheCommandInTheDetails(t *testing.T) {
	registered := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\OA\python.exe`}
	current := registered
	current.Activation = &fwire.ApplicationActivationRecipe{Arguments: []string{"-B"}}
	reply := officialPicture.retainedWorkspaceRegistration().conflictReply(registered, current)
	if !reply.Registered || reply.Matching || !reply.Conflict {
		t.Fatalf("conflict reply flags: %+v", reply)
	}
	if strings.Contains(reply.Reason, "comfyui") || !strings.Contains(reply.Reason, "picture workspace entry is from an earlier OpenAbstractions build") {
		t.Fatalf("the visible reason is one sentence in words, with no id or command: %q", reply.Reason)
	}
	if reply.Command != `openabstractions applications remove comfyui` {
		t.Fatalf("the details carry the command: %q", reply.Command)
	}
	if reply.Foreign || reply.Name != "python (registered app)" {
		t.Fatalf("an untitled earlier-build entry is named by its program file: %+v", reply)
	}
	other := registered
	other.Program = `C:\Other\tool.exe`
	if reply := officialPicture.retainedWorkspaceRegistration().conflictReply(other, current); !reply.Conflict || !reply.Foreign || reply.Command == "" || reply.Name != "tool (registered app)" || strings.Contains(reply.Reason, "comfyui") {
		t.Fatalf("another program's entry offers Remove under its own name: %+v", reply)
	}
	other.Title = "Tool Studio"
	if reply := officialPicture.retainedWorkspaceRegistration().conflictReply(other, current); reply.Name != "Tool Studio" {
		t.Fatalf("a titled entry is named by its title: %+v", reply)
	}
}

func TestComfyRemoveNamesAnotherProgramsEntryByItself(t *testing.T) {
	other := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\Other\tool.exe`}
	apps := &setupApplicationsFake{
		page:        fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: other}}},
		removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied},
	}
	rec := comfyRemoveRequest(&servicePanel{comfyApps: apps}, `{"confirm":true}`)
	if !strings.Contains(rec.Body.String(), `"message":"Removed tool (registered app)."`) {
		t.Fatalf("an untitled entry of another program is named by its file, not as ComfyUI: %s", rec.Body.String())
	}
}

// Native ComfyUI registers through its own register-self, which the service
// admits through the person's Keep for an unsigned helper. A failed first
// request leaves nothing registered, and the next workspace call retries.
func TestComfyNativeSelfRegistrationRetriesFromTheWorkspaceStep(t *testing.T) {
	want := fwire.ApplicationDescriptor{Name: "comfyui", Program: platformProgram(`C:\OA\a\cu\0.8.0\launch-helper.exe`), Title: "ComfyUI — OA build"}
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}, registerReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
	calls, stored := 0, want
	self := func(context.Context) error {
		calls++
		if calls == 1 {
			return errors.New("the runtime did not answer")
		}
		// The service stores the entry the helper's own request names.
		apps.page.Applications = []fwire.ApplicationEntry{{Descriptor: stored}}
		return nil
	}
	if _, err := officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, &workspaceNative{register: self}); err == nil || !strings.Contains(err.Error(), "register again to retry") {
		t.Fatalf("first register-self: %v", err)
	}
	created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, &workspaceNative{register: self}))
	if err != nil || !created || calls != 2 || apps.registerCalls != 0 {
		t.Fatalf("second workspace call: created=%v self=%d operator=%d err=%v", created, calls, apps.registerCalls, err)
	}
	// The readback refuses an entry that is not this installation's.
	apps.page.Applications = nil
	stored.Program = platformProgram(`C:\Other\launch-helper.exe`)
	if created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, &workspaceNative{register: self})); err == nil || created ||
		!strings.Contains(err.Error(), "does not list this installation") {
		t.Fatalf("readback of another entry: created=%v err=%v", created, err)
	}
}

// The service lists the normalized program the helper's handle resolves to;
// a spelling that differs from Setup's own only in case is the same entry.
func TestComfyReadbackMatchesTheProgramAcrossSpelling(t *testing.T) {
	root := t.TempDir()
	want := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(root, "OA", "a", "cu", "0.8.0", "launch-helper.exe"), Title: "ComfyUI"}
	listed := want
	listed.Program = strings.ToUpper(want.Program[:1]) + strings.ToLower(want.Program[1:])
	if listed.Program == want.Program {
		listed.Program = strings.ToLower(want.Program)
	}
	if runtime.GOOS == "windows" && !applicationDisplayEqual(listed, want) {
		t.Fatalf("a case difference failed the readback: %q %q", listed.Program, want.Program)
	}
	other := want
	other.Program = filepath.Join(root, "other", "launch-helper.exe")
	if applicationDisplayEqual(other, want) {
		t.Fatal("another program matched")
	}
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}}
	self := func(context.Context) error {
		apps.page.Applications = []fwire.ApplicationEntry{{Descriptor: listed}}
		return nil
	}
	if runtime.GOOS == "windows" {
		if created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, want, nil, &workspaceNative{register: self})); err != nil || !created {
			t.Fatalf("readback across spelling: created=%v err=%v", created, err)
		}
	}
}

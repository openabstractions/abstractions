package main

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

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
	python := filepath.Join(root, "ComfyUI_windows_portable", "python_embeded", "python.exe")
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

func TestComfyActivationRevalidatesAfterRegistration(t *testing.T) {
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
	p.setupComfyActivate(rec, req)
	if rec.Code != 409 {
		t.Fatalf("activation status = %d, want conflict; body=%s", rec.Code, rec.Body.String())
	}
	if openCalls != 1 {
		t.Fatalf("fresh install verifier calls = %d, want 1", openCalls)
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
	t.Run("same descriptor is idempotent", func(t *testing.T) {
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: want}}}}
		created, err := ensureComfyDescriptorRegistered(context.Background(), apps, want, nil)
		if err != nil || created || apps.registerCalls != 0 {
			t.Fatalf("same descriptor: created=%v calls=%d err=%v", created, apps.registerCalls, err)
		}
	})
	t.Run("different descriptor is not overwritten", func(t *testing.T) {
		other := want
		other.Program = `C:\Other\python.exe`
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: other}}}}
		_, err := ensureComfyDescriptorRegistered(context.Background(), apps, want, nil)
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
		_, err := ensureComfyDescriptorRegistered(context.Background(), apps, current, nil)
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
		created, err := ensureComfyDescriptorRegistered(context.Background(), apps, updated, []fwire.ApplicationDescriptor{replaced})
		if err != nil || !created || apps.removeCalls != 1 || apps.registerCalls != 1 || apps.registered.Program != updated.Program {
			t.Fatalf("earlier own entry not replaced: created=%v remove=%d register=%d err=%v", created, apps.removeCalls, apps.registerCalls, err)
		}
		foreign := replaced
		foreign.Title = "someone else's entry"
		apps = &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: foreign}}}}
		if _, err := ensureComfyDescriptorRegistered(context.Background(), apps, updated, []fwire.ApplicationDescriptor{replaced}); err == nil || apps.removeCalls != 0 {
			t.Fatalf("an entry Setup did not build was removed: remove=%d err=%v", apps.removeCalls, err)
		}
		apps = &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: replaced}}},
			removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeForbidden}}
		if _, err := ensureComfyDescriptorRegistered(context.Background(), apps, updated, []fwire.ApplicationDescriptor{replaced}); err == nil || apps.registerCalls != 0 {
			t.Fatalf("a refused removal still registered the update: register=%d err=%v", apps.registerCalls, err)
		}
	})
	t.Run("registers only when missing", func(t *testing.T) {
		apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}, registerReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
		created, err := ensureComfyDescriptorRegistered(context.Background(), apps, want, nil)
		if err != nil || !created || apps.registerCalls != 1 || apps.registered.Program != want.Program {
			t.Fatalf("missing descriptor: created=%v calls=%d err=%v", created, apps.registerCalls, err)
		}
	})
}

func TestComfyReadyPresenceRequiresTheFixedOAInterface(t *testing.T) {
	entry := fwire.ApplicationEntry{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui"}, Instances: []fwire.ApplicationInstance{{Interfaces: []fwire.ApplicationInterface{{Name: "oa-integration-loaded", Protocol: "oa-comfyui", Contract: "openabstractions.comfyui/integration-ready@1"}}}}}
	if !comfyReadyPresence(fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{entry}}) {
		t.Fatal("exact Comfy integration presence was not observed")
	}
	entry.Instances[0].Interfaces[0].Contract = "other"
	if comfyReadyPresence(fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{entry}}) {
		t.Fatal("unrelated interface was treated as Comfy readiness")
	}
}

func comfyRemoveRequest(p *servicePanel, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	p.setupComfyRemove(rec, httptest.NewRequest("POST", "/setup/comfy/remove", strings.NewReader(body)))
	return rec
}

func TestComfyRemoveDeletesOnlyTheFixedEntryAfterConfirmation(t *testing.T) {
	entry := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\OA\python.exe`, Title: "ComfyUI for OpenAbstractions"}
	newApps := func() *setupApplicationsFake {
		return &setupApplicationsFake{
			page:        fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: entry}}},
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
		(&servicePanel{comfyApps: newApps()}).setupComfyRemove(rec, httptest.NewRequest("GET", "/setup/comfy/remove", nil))
		if rec.Code != 405 {
			t.Fatalf("GET status = %d", rec.Code)
		}
	})
}

func TestComfyConflictReplyOffersRemoveAndKeepsTheCommandInTheDetails(t *testing.T) {
	registered := fwire.ApplicationDescriptor{Name: "comfyui", Program: `C:\OA\python.exe`}
	current := registered
	current.Activation = &fwire.ApplicationActivationRecipe{Arguments: []string{"-B"}}
	reply := comfyConflictReply(registered, current)
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
	if reply := comfyConflictReply(other, current); !reply.Conflict || !reply.Foreign || reply.Command == "" || reply.Name != "tool (registered app)" || strings.Contains(reply.Reason, "comfyui") {
		t.Fatalf("another program's entry offers Remove under its own name: %+v", reply)
	}
	other.Title = "Tool Studio"
	if reply := comfyConflictReply(other, current); reply.Name != "Tool Studio" {
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

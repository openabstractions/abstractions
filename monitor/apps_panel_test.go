package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
)

type appsRegistryFixture struct {
	*fixedRegistry
	mu       sync.Mutex
	page     fwire.DeclarationObservation
	cursor   string
	wait     int64
	observes int
}

func (f *appsRegistryFixture) Observe(cursor string, wait int64) (fwire.DeclarationObservation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cursor, f.wait = cursor, wait
	f.observes++
	return f.page, nil
}

func TestAppsRegistrationsReadsNativeProvidersWithoutSavedPermissions(t *testing.T) {
	own(t)
	program := filepath.Join(t.TempDir(), "engine.exe")
	fixed := &fixedRegistry{}
	declarations, _ := fixed.Declarations()
	fixed.calls = 0
	declarations.Declarations[1].Declaration.Program = program
	declarations.Declarations[1].Declaration.Name = "engine"
	declarations.Declarations[1].Readiness = fwire.DeclarationReadinessIdle
	declarations.Declarations[1].DeclaredBy = "registration-operator"
	registry := &appsRegistryFixture{fixedRegistry: fixed, page: fwire.DeclarationObservation{
		Outcome: fwire.DeclarationListOutcomePage, Cursor: "first", Declarations: declarations.Declarations}}
	panelConfigRuntimeWith(t, func(options *host.Options) {
		endpoint := options.ConfigEndpoint + "-apps-registry"
		listener, err := listen.Listen(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		options.Registry, options.RegistryEndpoint = registryService{listener: listener, handler: registry}, endpoint
	})
	handler := newTestPanel(t).handler("test-key")
	caller, err := panelMachine().ObserveCaller(context.Background())
	if err != nil || caller.Outcome != fwire.CallerOutcomeObserved {
		t.Fatalf("receiving caller %+v %v", caller, err)
	}
	page := decodePanel[appsRegistrationPage](t, 200, panelRequest(t, handler, "/apps/registrations?source=providers&cursor=previous&wait_ms=30000", nil).Body.Bytes())
	if page.Outcome != "page" || page.Cursor != "first" || page.IdentityError != "" || len(page.Entries) != 1 {
		t.Fatalf("provider source %+v", page)
	}
	entry := page.Entries[0]
	if entry.Subject == nil || entry.Subject.Account != caller.Account || entry.Subject.Program != program || entry.Name != "engine" || entry.Role != "provider" || entry.State != "idle" || entry.Provider.DeclaredBy != "registration-operator" {
		t.Fatalf("provider identity/readiness %+v", entry)
	}
	registry.mu.Lock()
	if registry.cursor != "previous" || registry.wait != 30000 || registry.observes != 1 {
		t.Errorf("Observe cursor=%q wait=%d calls=%d", registry.cursor, registry.wait, registry.observes)
	}
	registry.page = fwire.DeclarationObservation{Outcome: fwire.DeclarationListOutcomeForbidden}
	registry.mu.Unlock()
	denied := decodePanel[appsRegistrationPage](t, 200, panelRequest(t, handler, "/apps/registrations?source=providers&wait_ms=0", nil).Body.Bytes())
	if denied.Outcome != "forbidden" || len(denied.Entries) != 0 {
		t.Fatalf("revoked provider source %+v", denied)
	}
	registry.fixedRegistry.mu.Lock()
	defer registry.fixedRegistry.mu.Unlock()
	if registry.fixedRegistry.calls != 0 || registry.fixedRegistry.declareCalls != 0 || registry.fixedRegistry.withdrawName != "" {
		t.Fatal("Apps reached a declaration list or mutation instead of the source Observe")
	}
	for _, target := range []string{"/apps/registrations?source=providers&wait_ms=30001", "/apps/registrations?source=other&wait_ms=0"} {
		if got := panelRequest(t, handler, target, nil); got.Code != 400 {
			t.Fatalf("invalid source/wait %q: %d", target, got.Code)
		}
	}
	request := httptest.NewRequest(http.MethodGet, "/apps/registrations?source=providers&wait_ms=0", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unguarded registration source: %d", response.Code)
	}
}

func TestAppsRegistrationSubjectNeedsReceivingAccountAndNativeIdentity(t *testing.T) {
	program := filepath.Join(t.TempDir(), "same-name.exe")
	if appsRegistrationSubject("", program) != nil || appsRegistrationSubject("account", "same-name.exe") != nil || appsRegistrationSubject("account", "msix:package") != nil {
		t.Fatal("unassociated or nonnative metadata manufactured a permission subject")
	}
	got := appsRegistrationSubject("receiving-account", filepath.Join(filepath.Dir(program), ".", filepath.Base(program)))
	if got == nil || got.Account != "receiving-account" || got.Program != program {
		t.Fatalf("shared native identity spelling %+v", got)
	}
}

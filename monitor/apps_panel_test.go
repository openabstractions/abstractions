package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
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

func TestAppsRegistrationsRefusesMalformedRemoteTrustPage(t *testing.T) {
	for _, key := range []string{"", "short", strings.Repeat("a", 64)} {
		t.Run("key-"+key, func(t *testing.T) {
			own(t)
			fixed := &fixedRegistry{}
			list, err := fixed.Declarations()
			if err != nil {
				t.Fatal(err)
			}
			registry := &appsRegistryFixture{fixedRegistry: fixed, page: fwire.DeclarationObservation{
				Outcome: fwire.DeclarationListOutcomePage, Cursor: "malformed", Declarations: list.Declarations}}
			panelConfigRuntimeWith(t, func(options *host.Options) {
				endpoint := options.ConfigEndpoint + "-apps-malformed-registry"
				listener, err := listen.Listen(endpoint)
				if err != nil {
					t.Fatal(err)
				}
				options.Registry, options.RegistryEndpoint = registryService{listener: listener, handler: registry, mutateReply: func(reply []byte) []byte {
					// Start with the real canonical five-field response, then add
					// the incompatible sixth member a published reader refuses.
					value, _ := json.Marshal(key)
					needle := []byte(`"server_name": `)
					if !bytes.Contains(reply, needle) {
						t.Error("canonical remote fixture absent")
						return reply
					}
					return bytes.Replace(reply, needle, append(append([]byte(`"server_key_sha256": `), value...), []byte(`, "server_name": `)...), 1)
				}}, endpoint
			})
			handler := newTestPanel(t).handler("test-key")
			page := decodePanel[appsRegistrationPage](t, 200, panelRequest(t, handler, "/apps/registrations?source=providers&wait_ms=0", nil).Body.Bytes())
			offsetText, refused := strings.CutPrefix(page.Error, "refused: unknown_field at byte ")
			offset, offsetErr := strconv.ParseUint(offsetText, 10, 64)
			if page.Outcome != "unavailable" || !refused || offsetErr != nil || strconv.FormatUint(offset, 10) != offsetText || len(page.Entries) != 0 {
				t.Fatalf("malformed registry page escaped canonical refusal: %+v", page)
			}
		})
	}
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
	if entry.Subject == nil || entry.Subject.Account != caller.Account || !identity.SameSubjectProgram(entry.Subject.Program, program) || entry.Name != "engine" || entry.Role != "provider" || entry.State != "idle" || entry.Provider.DeclaredBy != "registration-operator" {
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
	if got == nil || got.Account != "receiving-account" || !samePanelProgram(got.Program, program) {
		t.Fatalf("shared native identity spelling %+v", got)
	}
}

func TestSelectionRegistrationProjectionRequiresCurrentIdentity(t *testing.T) {
	program := filepath.Join(t.TempDir(), "app.exe")
	makeView := func() appsRegistrationPage {
		return appsRegistrationPage{Outcome: "page", Entries: []appsRegistration{{Name: "app", Program: program, Subject: appsRegistrationSubject("account", program)}}}
	}
	row := nativeRegistrationFixture(registrationwire.Manifest{Name: "app", Choosers: &registrationwire.Choosers{Entries: []registrationwire.Chooser{}}}, program, "r1", "", nil)
	view := makeView()
	projectChoosers(&view, []registrationwire.Registration{row}, nil, "account")
	if view.Entries[0].ChoosersStatus != "declared" || view.Entries[0].Choosers == nil || view.Entries[0].RegistrationRevision != "r1" {
		t.Fatalf("empty declaration lost %+v", view)
	}
	for _, kind := range []string{"failed", "account", "program", "revision", "shadowed"} {
		t.Run(kind, func(t *testing.T) {
			view := makeView()
			r := row
			account := "account"
			var err error
			switch kind {
			case "failed":
				err = errors.New("unavailable")
			case "account":
				account = "other"
			case "program":
				r.Spec.Program = filepath.Join(t.TempDir(), "foreign.exe")
			case "revision":
				r.Revision = ""
			case "shadowed":
				r.ShadowedBy = &registrationwire.FacetReference{Kind: registrationwire.FacetKindApplication, Name: "other"}
			}
			projectChoosers(&view, []registrationwire.Registration{r}, err, account)
			if view.Entries[0].ChoosersStatus != "unavailable" || view.Entries[0].Choosers != nil || view.Entries[0].RegistrationRevision != "" {
				t.Fatalf("identity fallback %+v", view)
			}
		})
	}
	view = makeView()
	projectChoosers(&view, nil, nil, "account")
	if view.Entries[0].ChoosersStatus != "absent" {
		t.Fatal("legacy absence lost")
	}
}
func TestSelectionRegistrationPagesDiscardPartialOnStale(t *testing.T) {
	calls := 0
	rows, err := selectionRegistrationRows(func(q applications.Query) (applications.Page, error) {
		calls++
		if calls == 1 {
			return applications.Page{Outcome: applications.OutcomePage, Cursor: "one", Registrations: []registrationwire.Registration{{Revision: "old"}}}, nil
		}
		return applications.Page{Outcome: applications.OutcomeStale}, nil
	})
	if err == nil || rows != nil || calls != 2 {
		t.Fatal("stale partial declaration retained")
	}
	calls = 0
	rows, err = selectionRegistrationRows(func(q applications.Query) (applications.Page, error) {
		calls++
		if calls == 1 {
			return applications.Page{Outcome: applications.OutcomePage, Cursor: "one", Registrations: []registrationwire.Registration{{Revision: "current"}}}, nil
		}
		return applications.Page{Outcome: applications.OutcomePage, Cursor: "one"}, nil
	})
	if err != nil || len(rows) != 1 || calls != 2 {
		t.Fatalf("complete pages %+v %v", rows, err)
	}
}

func TestSelectionRegistrationMixedSnapshotsNeverProject(t *testing.T) {
	row := func(profile string) registrationwire.Registration {
		return nativeRegistrationFixture(registrationwire.Manifest{Name: "editor", Choosers: &registrationwire.Choosers{Entries: []registrationwire.Chooser{{Name: "choice", Modality: profile}}}}, "/apps/editor", "", "", nil)
	}
	for _, terminal := range []bool{false, true} {
		t.Run(strconv.FormatBool(terminal), func(t *testing.T) {
			calls := 0
			rows, err := selectionRegistrationRows(func(q applications.Query) (applications.Page, error) {
				calls++
				if calls == 1 {
					return applications.Page{Outcome: applications.OutcomePage, Cursor: "old", Registrations: []registrationwire.Registration{row("chat")}}, nil
				}
				if terminal {
					return applications.Page{Outcome: applications.OutcomePage, Cursor: "new"}, nil
				}
				return applications.Page{Outcome: applications.OutcomePage, Cursor: "new", Registrations: []registrationwire.Registration{row("image")}}, nil
			})
			if err == nil || rows != nil {
				t.Fatal("mixed snapshot retained old profiles")
			}
		})
	}
}

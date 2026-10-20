package main

import (
	"context"
	"encoding/json"
	"errors"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestNativeApplicationUninstallConditionalRegistration(t *testing.T) {
	oldProgram := nativeUninstallProgram
	t.Cleanup(func() { nativeUninstallProgram = oldProgram })
	nativeUninstallProgram = func(_ context.Context, a setup.Artifact, root string) (string, error) {
		return filepath.Join(root, a.ApplicationProgram), nil
	}
	for _, kind := range []string{"applied", "owned", "foreign-version", "replaced-within", "lost-reply", "stale", "concurrent-register"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			program := filepath.Join(root, "host.exe")
			if err := os.WriteFile(program, []byte("inert"), 0600); err != nil {
				t.Fatal(err)
			}
			n := &nativeApplications{application: "fixture-native"}
			removed := false
			removes := 0
			n.transport = func(context.Context) applications.ApplicationsTransport {
				return nativeApplicationExchange(func(b []byte) ([]byte, error) {
					var frame struct {
						Method, Service string
						Arguments       json.RawMessage
					}
					if err := json.Unmarshal(b, &frame); err != nil {
						return nil, err
					}
					var value []byte
					switch frame.Method {
					case "Observe":
						page := applications.Page{Outcome: applications.OutcomePage}
						if !removed || kind == "concurrent-register" {
							p := program
							if kind == "foreign-version" {
								p = filepath.Join(t.TempDir(), "new.exe")
							}
							if kind == "replaced-within" {
								p = filepath.Join(root, "other.exe")
							}
							row := nativeRegistrationFixture(registrationwire.Manifest{Version: "applications@3", Name: "fixture-native", Title: "Fixture", Offers: appmodel.CommandDeclaration{Version: "abstraction.facade/appmodel@1"}}, p, "observed", strings.Repeat("a", 64), nil)
							if kind == "owned" {
								row.Applications[0].OwnedInstances = []string{"actual-owned"}
							}
							page.Registrations = []registrationwire.Registration{row}
						}
						value = applications.EncodePage(&page)
					case "Remove":
						removes++
						var args struct{ Request json.RawMessage }
						if err := json.Unmarshal(frame.Arguments, &args); err != nil {
							return nil, err
						}
						request, err := applications.DecodeChangeRequest(args.Request)
						if err != nil {
							return nil, err
						}
						if request.ExpectedRevision != "observed" {
							t.Fatal("unconditional remove")
						}
						if kind == "lost-reply" {
							return nil, errors.New("lost reply")
						}
						outcome := applications.OutcomeApplied
						if kind == "stale" {
							outcome = applications.OutcomeStale
						} else {
							removed = true
						}
						value = applications.EncodeChange(&applications.Change{Outcome: outcome})
					default:
						t.Fatalf("unexpected method %s", frame.Method)
					}
					return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
				})
			}
			p := &servicePanel{comfyApps: n}
			err := p.unregisterNativeInstallation(context.Background(), "fixture-native", setup.Artifact{ApplicationProgram: "host.exe"}, root)
			wantOK := kind == "applied" || kind == "foreign-version"
			if (err == nil) != wantOK {
				t.Fatalf("result %v", err)
			}
			if (kind == "owned" || kind == "foreign-version" || kind == "replaced-within") && removes != 0 {
				t.Fatal("removed a live or foreign registration")
			}
		})
	}
}

func TestNativeApplicationUninstallRequiresPostAndConfirmation(t *testing.T) {
	p := &servicePanel{}
	for _, handler := range []http.HandlerFunc{p.setupUninstall, p.setupReinstall} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(http.MethodGet, "/setup/uninstall", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatal(w.Code)
		}
		w = httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/setup/uninstall", strings.NewReader(`{"component_id":"fixture","plan_id":"plan","confirm":false}`))
		r.Header.Set("Content-Type", "application/json")
		handler(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatal(w.Code)
		}
	}
}

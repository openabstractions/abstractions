package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type fixturePackageApplications struct {
	setupApplications
	entry      []fwire.ApplicationEntry
	expectName string
	expectPath string
	expectPin  string
}

func (f *fixturePackageApplications) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	return fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: f.entry}, nil
}

func (f *fixturePackageApplications) ExpectConnect(_ context.Context, name, program, digest string) (fwire.ApplicationChange, error) {
	f.expectName, f.expectPath, f.expectPin = name, program, digest
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

func TestCatalogPackageConnectUsesDeclaredRegistration(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		name := "the declared connector"
		if foreign {
			name = "same name at another program"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			target := packageConnectTarget{
				registration: "example-app", program: filepath.Join(t.TempDir(), "connector.exe"), title: "Example App",
			}
			record := func(program string) []fwire.ApplicationEntry {
				return []fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{
					Name: target.registration, Program: program, Title: target.title,
				}}}
			}
			apps := &fixturePackageApplications{}
			if foreign {
				apps.entry = record(filepath.Join(t.TempDir(), "foreign.exe"))
			}
			p := &servicePanel{comfyApps: apps, appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
			const pin = "connector-digest"
			installed := false
			handled, err := p.connectCatalogPackage(ctx, catalogPackageConnect{
				target: target,
				kept: func(context.Context) (bool, bool) {
					return installed && len(apps.entry) == 1, true
				},
				install: func(ctx context.Context, ops setup.PackageConnection) (bool, error) {
					if err := ops.Admit(ctx, target.registration, target.program, true); err != nil {
						return true, err
					}
					ops.Expect(ctx, target.registration, target.program, pin)
					installed = true
					if err := ops.Confirm(ctx, target.registration, target.program+"-other"); err == nil {
						t.Fatal("a different proof program reached registration")
					}
					return true, ops.Confirm(ctx, target.registration, target.program)
				},
				register: func(_ context.Context, program string) error {
					if program != target.program {
						t.Fatalf("registered a different connector: %q", program)
					}
					apps.entry = record(program)
					return nil
				},
			})
			lines := p.appActivity.lines()
			if foreign {
				if !handled || err == nil || !strings.Contains(err.Error(), target.title) ||
					installed || apps.expectName != "" || len(lines) != 0 {
					t.Fatalf("foreign registration reached an effect: handled %v, error %v, installed %v, activity %+v", handled, err, installed, lines)
				}
				return
			}
			if !handled || err != nil || !installed || apps.expectName != target.registration ||
				apps.expectPath != target.program || apps.expectPin != pin {
				t.Fatalf("declared connection: handled %v, error %v, installed %v, expectation %+v", handled, err, installed, apps)
			}
			if len(lines) != 1 || lines[0].App != target.title || lines[0].Program != target.program || !lines[0].UndoRemoves ||
				len(lines[0].Undo) != 1 || lines[0].Undo[0] != target.registration {
				t.Fatalf("activity did not name the declared connection: %+v", lines)
			}
		})
	}
}

func TestCatalogPackageLegacyConfirmationKeepsActivityAtTheCommitBoundary(t *testing.T) {
	ctx := t.Context()
	target := packageConnectTarget{registration: "example-app", title: "Example App",
		program: filepath.Join(t.TempDir(), "connector.exe")}
	apps := &fixturePackageApplications{}
	p := &servicePanel{comfyApps: apps, appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	kept, committed, registered := false, false, 0
	handled, err := p.connectCatalogPackage(ctx, catalogPackageConnect{
		target: target,
		kept:   func(context.Context) (bool, bool) { return kept, true },
		install: func(ctx context.Context, ops setup.PackageConnection) (bool, error) {
			if err := ops.ConfirmLegacy(ctx, "other-app", target.program); err == nil ||
				ops.ConfirmLegacy(ctx, target.registration, target.program+"-other") == nil || registered != 0 {
				t.Fatal("a different package or program reached registration")
			}
			const digest = "connector-digest"
			ops.Expect(ctx, target.registration, target.program, digest)
			if err := ops.ConfirmLegacy(ctx, target.registration, target.program); err != nil {
				return true, err
			}
			if len(p.appActivity.lines()) != 0 {
				t.Fatal("Activity preceded package receipt completion")
			}
			committed = true
			return true, nil
		},
		register: func(_ context.Context, program string) error {
			registered++
			if program != target.program || apps.expectPath != program || apps.expectName != target.registration {
				t.Fatalf("registration does not match the click: %q, expectation %+v", program, apps)
			}
			kept = true
			return nil
		},
	})
	lines := p.appActivity.lines()
	if !handled || err != nil || !committed || registered != 1 || len(lines) != 1 ||
		lines[0].What != "Example App connected to OpenAbstractions" || !lines[0].UndoRemoves {
		t.Fatalf("legacy connection: handled %v, error %v, committed %v, registered %d, activity %+v", handled, err, committed, registered, lines)
	}
}

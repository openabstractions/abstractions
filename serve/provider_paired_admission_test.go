package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
)

func TestPairedProviderNameBusyIsUnavailableBeforeAnyWrite(t *testing.T) {
	for _, operation := range []string{"declare", "replace"} {
		for _, phase := range []string{"entry", "commit"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				registry, caller := registryObserverFixture(t)
				registry.providers.report = func(error) {}
				state := pairingStateFixture(t)
				pairing := &runtimePairing{ctx: context.Background(), state: state}
				declaration := extensionDeclaration()
				before := registry.DeclarationsPinned(context.Background(), caller)
				path := filepath.Join(registry.providers.dir, declaration.Name+".json")
				var original []byte
				if operation == "replace" {
					initial := registry.DeclarePinned(context.Background(), caller, before.Revision, wire.PinnedDeclaration{Declaration: declaration})
					if initial.Outcome != wire.DeclarationEditOutcomeApplied {
						t.Fatal(initial)
					}
					var err error
					original, err = os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					before = registry.DeclarationsPinned(context.Background(), caller)
					declaration.Models = []string{"changed-model"}
				}
				calls := 0
				registry.providers.pairedNameAdmission = func(name string) error {
					calls++
					if phase == "entry" || calls == 2 {
						state.mu.Lock()
						defer state.mu.Unlock()
					}
					return pairing.bindingNameAdmission(name)
				}
				var got wire.DeclarationChange
				if operation == "declare" {
					got = registry.DeclarePinned(context.Background(), caller, before.Revision, wire.PinnedDeclaration{Declaration: declaration})
				} else {
					got = registry.ReplacePinned(context.Background(), caller, before.Revision, wire.PinnedDeclaration{Declaration: declaration})
				}
				if got.Outcome != wire.DeclarationEditOutcomeUnavailable || got.Revision != "" || got.Reason != "" {
					t.Fatalf("uncertain name facts: %+v", got)
				}
				after := registry.DeclarationsPinned(context.Background(), caller)
				if after.Revision != before.Revision {
					t.Fatal("busy pairing changed registry revision")
				}
				if operation == "declare" {
					if _, err := os.Stat(path); !os.IsNotExist(err) {
						t.Fatalf("busy pairing wrote declaration: %v", err)
					}
				} else if current, err := os.ReadFile(path); err != nil || !bytes.Equal(current, original) {
					t.Fatalf("busy pairing replaced declaration: %v", err)
				}
				registry.providers.pairedNameAdmission = pairing.bindingNameAdmission
				if operation == "declare" {
					got = registry.DeclarePinned(context.Background(), caller, before.Revision, wire.PinnedDeclaration{Declaration: declaration})
				} else {
					got = registry.ReplacePinned(context.Background(), caller, before.Revision, wire.PinnedDeclaration{Declaration: declaration})
				}
				if got.Outcome != wire.DeclarationEditOutcomeApplied {
					t.Fatalf("quiescent admission: %+v", got)
				}
			})
		}
	}
}

func TestPairedProviderNameReservationAndUnavailableFactsStayDistinct(t *testing.T) {
	for _, kind := range []string{"reserved", "canceled", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			registry, caller := registryObserverFixture(t)
			state := pairingStateFixture(t)
			pairing := &runtimePairing{ctx: context.Background(), state: state}
			declaration := extensionDeclaration()
			want := wire.DeclarationEditOutcomeUnavailable
			switch kind {
			case "reserved":
				pairing.active = "owned-operation"
				pairing.ops = map[string]*pairingOperation{"owned-operation": {preparation: mwire.MachinePreparation{BindingName: declaration.Name}}}
				want = wire.DeclarationEditOutcomeConflict
			case "canceled":
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				pairing.ctx = ctx
			case "corrupt":
				if err := os.WriteFile(state.path, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			registry.providers.pairedNameAdmission = pairing.bindingNameAdmission
			before := registry.DeclarationsPinned(context.Background(), caller)
			got := registry.DeclarePinned(context.Background(), caller, before.Revision, wire.PinnedDeclaration{Declaration: declaration})
			if got.Outcome != want {
				t.Fatalf("%s: %+v", kind, got)
			}
			if kind == "reserved" {
				if got.Reason != "paired_binding_name" || got.Revision != before.Revision {
					t.Fatalf("reservation lost: %+v", got)
				}
			} else if got.Revision != "" || got.Reason != "" {
				t.Fatalf("uncertainty claimed a conflict: %+v", got)
			}
			if after := registry.DeclarationsPinned(context.Background(), caller); after.Revision != before.Revision {
				t.Fatal("refusal changed declarations")
			}
			if _, err := os.Stat(filepath.Join(registry.providers.dir, declaration.Name+".json")); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote declaration: %v", err)
			}
		})
	}
}

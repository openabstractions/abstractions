package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// R36 live listing D4: the owner's model host declaration, written by Setup
// before 2026-11-01, hosted Setup's coding model under its file name
// 55983c5a….gguf, and the reconciler later rebound qwen3.6-35b-a3b to the
// same object. The listing showed a digest capability line and a chat turn
// for qwen3.6-35b-a3b answered model 55983c5a….gguf. The runtime's
// reconciler repairs the declaration through the registry: the digest entry
// beside a plain one for its object is dropped, and a digest entry alone is
// renamed to its descriptor's family.
func TestReconcileModelHostRepairsDigestNamedEntries(t *testing.T) {
	const digest = "55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1.gguf"
	for _, c := range []struct {
		name     string
		declared []hostedModel
		want     []string
	}{
		{"beside a plain entry", []hostedModel{
			{Name: digest, Store: "oa-chat-1", Object: "sha256:945b"},
			{Name: "qwen3.6-35b-a3b", Store: "oa-chat-1", Object: "sha256:945b"},
			{Name: "smollm2-135m", Store: "huggingface", Object: "sha256:69ae"},
		}, []string{"qwen3.6-35b-a3b=oa-chat-1/sha256:945b", "smollm2-135m=huggingface/sha256:69ae"}},
		{"alone", []hostedModel{
			{Name: digest, Store: "oa-chat-1", Object: "sha256:945b"},
			{Name: "smollm2-135m", Store: "huggingface", Object: "sha256:69ae"},
		}, []string{"qwen3.6-35b-a3b=oa-chat-1/sha256:945b", "smollm2-135m=huggingface/sha256:69ae"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			p, err := openFixtureProviders(t, state, false, func(err error) { t.Errorf("providers: %v", err) })
			if err != nil {
				t.Fatal(err)
			}
			program, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			programs, err := modelHostRuntimePrograms()
			if err != nil {
				t.Fatal(err)
			}
			settings := hostSettings{program: program, endpoint: "modelhost-digest-test", engine: filepath.Join(state, "engine", "llama-server.exe")}
			f := providerFile{Version: providerFileVersion, DeclaredBy: program, DeclaredAt: 1,
				Declaration: declarationOf(modelHostDeclaration(settings, c.declared, programs))}
			_, revision, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			if added := p.add(revision, f); added.Outcome != wire.DeclarationEditOutcomeApplied {
				t.Fatalf("add: %+v", added)
			}
			// Setup's store names its object by the file digest; the
			// descriptor states the family.
			setupCopy := heldGGUF("oa-chat-1", "sha256:945b", "sha256:945b", map[string]string{"family": "qwen3.6-35b-a3b", "architecture": "qwen35moe"})
			setupCopy.Manifest.Names = []content.Name{{Scheme: "oa-chat-1", Name: digest}}
			composed := storageservice.ComposedInventory{Stores: readStores("huggingface", "oa-chat-1"), Manifests: []content.ManifestHolders{
				setupCopy,
				heldGGUF("huggingface", "sha256:69ae", "sha256:69ae", map[string]string{"family": "smollm2-135m"}),
			}}
			idle := func(string, string) bool { return false }
			if wrote, err := p.reconcileModelHost(context.Background(), composed, idle); err != nil || wrote == "" {
				t.Fatalf("reconcile: %q %v", wrote, err)
			}
			files, _, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, f := range files {
				if f.Declaration.Name == modelHostName {
					got = names(hostedIn(f.Declaration))
					if slices.ContainsFunc(f.Declaration.Models, func(m string) bool { return digestModelName.MatchString(m) }) {
						t.Errorf("declared models still name a digest: %v", f.Declaration.Models)
					}
				}
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("declared %v, want %v", got, c.want)
			}
			if wrote, err := p.reconcileModelHost(context.Background(), composed, idle); err != nil || wrote != "" {
				t.Fatalf("a repaired declaration was rewritten: %q %v", wrote, err)
			}
			if removed, _, err := p.modelHostRemovals(); err != nil || len(removed) != 0 {
				t.Fatalf("the repair recorded removals %v (%v)", removed, err)
			}
		})
	}
}

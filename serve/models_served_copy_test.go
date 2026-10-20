package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// R34 live test D10: Qwen3.6 35B is held in the Hugging Face cache and in
// Setup's store, and the owner's model host declaration, written before
// recipes existed, serves Setup's copy. The router built from the registry
// binds the model host's engine to Setup's object alone, so the listing can
// name the copy it serves; the Hugging Face copy stays unbound.
func TestModelHostDeclarationBindsTheServedCopy(t *testing.T) {
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
	settings := hostSettings{program: program, endpoint: "modelhost-served-copy-test", engine: filepath.Join(state, "engine", "llama-server.exe")}
	declared := []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "oa-chat-1", Object: "sha256:945b"}}
	f := providerFile{Version: providerFileVersion, DeclaredBy: program, DeclaredAt: 1,
		Declaration: declarationOf(modelHostDeclaration(settings, declared, programs))}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if added := p.add(revision, f); added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	hosts := slices.DeleteFunc(p.nativeInferenceHosts(), func(h *router.Host) bool { return h.Name != modelHostName })
	if len(hosts) != 1 {
		t.Fatalf("model host routes %d hosts", len(hosts))
	}
	routes := router.New(hosts...)
	setupCopy := heldGGUF("oa-chat-1", "sha256:945b", "sha256:945b", map[string]string{"family": "qwen3.6-35b-a3b"})
	setupCopy.Manifest.Names = []content.Name{{Scheme: "oa-chat-1", Name: "945b.gguf"}}
	hubCopy := heldGGUF("huggingface", "sha256:23ab", "sha256:23ab", map[string]string{"family": "qwen3.6-35b-a3b"})
	hubCopy.Manifest.Names = []content.Name{{Scheme: "huggingface", Name: "unsloth/Qwen3.6-35B-A3B-MTP-GGUF/Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf"}}
	composed := storageservice.ComposedInventory{Stores: readStores("huggingface", "oa-chat-1"), Manifests: []content.ManifestHolders{setupCopy, hubCopy}}
	routes.SetHeld(heldObjects(composed))
	families, _ := routes.Models(true)
	i := slices.IndexFunc(families, func(f router.Family) bool { return f.Family == "qwen3.6-35b-a3b" })
	if i < 0 {
		t.Fatalf("families %+v", families)
	}
	family := families[i]
	if !slices.Equal(family.HeldIn, []string{"huggingface", "oa-chat-1"}) {
		t.Fatalf("held_in %v", family.HeldIn)
	}
	bound := map[string]bool{}
	for _, o := range family.Objects {
		bound[o.Store] = slices.ContainsFunc(o.Engines, func(e router.ModelEngine) bool {
			return e.Host == modelHostName && e.Model == "qwen3.6-35b-a3b"
		})
	}
	if !bound["oa-chat-1"] || bound["huggingface"] {
		t.Fatalf("engine associations %+v", family.Objects)
	}
	if j := slices.IndexFunc(family.Names, func(a router.Alias) bool { return a.Name == "qwen3.6-35b-a3b" }); j < 0 || !family.Names[j].Servable {
		t.Fatalf("names %+v", family.Names)
	}
}

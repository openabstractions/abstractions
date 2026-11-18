package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// declarerFixture is a per-machine installation with OA's programs in its
// tools folder, a registry whose rights let every program of the account
// manage providers, and the runtime's providers reading that installation.
type declarerFixture struct {
	tools    string
	r        *runtimeRights
	p        *runtimeProviders
	registry *runtimeRegistry
}

func newDeclarerFixture(t *testing.T) declarerFixture {
	t.Helper()
	tools := installedShippedDeclarations(t, "lemonade-image")
	signedByRuntimeSigner(t)
	saved := standingOriginalName
	t.Cleanup(func() { standingOriginalName = saved })
	standingOriginalName = func(string) string { return "" }
	f := declarerFixture{tools: tools}
	for _, program := range []string{"pytorchd.exe", "thenoise-provider.exe", "modelhostd.exe", "Abstraction Panel.exe", "openabstractions.exe"} {
		if err := os.WriteFile(f.program(program), []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r, p, o := cardRulesFixture(t)
	p.installation = filepath.Join(tools, installationDeclarationsDir)
	// The runtime admits the installed Panel image alone.
	panel := f.program("Abstraction Panel.exe")
	p.admittedPanel = func(program string) bool { return samePrograms(program, panel) }
	return declarerFixture{tools: tools, r: r, p: p, registry: &runtimeRegistry{providers: p, operator: o}}
}

func (f declarerFixture) program(name string) string {
	return filepath.Join(f.tools, shippedFixtureProgram(name))
}

// declare makes one declaration through the registry as caller, replacing a
// declaration of the same name.
func (f declarerFixture) declare(t *testing.T, caller string, d wire.Declaration) {
	t.Helper()
	files, revision, err := f.p.read()
	if err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: f.r.owner, Program: caller}
	// The caller may manage providers; the person granted it.
	if err := f.r.policy.Set(rwire.Subject{Account: f.r.owner, Program: caller}, ActionProviderManage, "account", true); err != nil {
		t.Fatal(err)
	}
	var change wire.DeclarationChange
	if slices.ContainsFunc(files, func(file providerFile) bool { return file.Declaration.Name == d.Name }) {
		change = f.registry.ReplacePinned(context.Background(), subject, revision, wire.PinnedDeclaration{Declaration: d})
	} else {
		change = f.registry.Declare(context.Background(), subject, revision, d)
	}
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("%s declaring %s: %+v", filepath.Base(caller), d.Name, change)
	}
}

// listed is the registry's standing and the stored declaration of name.
func (f declarerFixture) listed(t *testing.T, name string) (wire.ProductStanding, providerFile) {
	t.Helper()
	standings, _, err := f.p.standings()
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := f.p.read()
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(files, func(file providerFile) bool { return file.Declaration.Name == name })
	for _, s := range standings {
		if s.Name == name && at >= 0 {
			return s.Standing, files[at]
		}
	}
	t.Fatalf("%s is not listed with a standing", name)
	return 0, providerFile{}
}

// setupDeclaration is a native engine declaration as Setup writes one, with
// arguments that name the engine's own folders.
func setupDeclaration(name, program, root string) wire.Declaration {
	return wire.Declaration{Name: name, Program: program, Arguments: []string{"serve", "--endpoint", "{endpoint}", "--root", root},
		Endpoint: name, Transport: wire.DeclarationTransportNative, Contracts: []string{inference.ImageContract},
		Resources: []string{"card:0", "profile:image"}, Models: []string{}, Activation: wire.ActivationOnDemand}
}

// A-SEC6 declarer amendment (architect ruling 2026-11-22, FAC-R9): a
// declaration of a shipped name has product standing when its recorded
// declarer, the caller's program resolved by identity, is a component the
// table lists as a declarer of that name, or when it repeats the
// installation's declaration exactly. These are the ruling's seven cases.
func TestDeclarerStandingFollowsTheRecordedComponent(t *testing.T) {
	t.Run("Setup declares pytorch-image", func(t *testing.T) {
		f := newDeclarerFixture(t)
		f.declare(t, f.program("Abstraction Panel.exe"), setupDeclaration(product.Diffusers, f.program("pytorchd.exe"), t.TempDir()))
		standing, file := f.listed(t, product.Diffusers)
		if standing != wire.ProductStandingProduct || pictureProviderRank(file.Declaration.Name, f.p.shippedProvider(file)) != 0 {
			t.Fatalf("Setup's pytorch-image reads %v, rank %d", standing, pictureProviderRank(file.Declaration.Name, f.p.shippedProvider(file)))
		}
		if title, ok := product.EngineTitle(file.Declaration.Name); !ok || title != "Diffusers" {
			t.Fatalf("Setup's pytorch-image is titled %q", title)
		}
	})
	t.Run("the model host declares modelhost", func(t *testing.T) {
		// Setup declares the model host's modelhost through the Panel's
		// registry client before it starts the model host; the declaration is
		// product, with the llama.cpp title and the routine complete tier for
		// local chat. A re-declaration through the Panel, a restart included,
		// keeps it; the runtime's own allowlist reconcile keeps its declarer.
		f := newDeclarerFixture(t)
		d := setupDeclaration(product.ModelHost, f.program("modelhostd.exe"), t.TempDir())
		d.Contracts = []string{inference.Contract}
		routine := func(when string) {
			t.Helper()
			standing, file := f.listed(t, product.ModelHost)
			if standing != wire.ProductStandingProduct {
				t.Fatalf("%s: the model host's modelhost reads %v", when, standing)
			}
			if title, ok := product.EngineTitle(file.Declaration.Name); !ok || title != "llama.cpp" {
				t.Fatalf("%s: modelhost is titled %q", when, title)
			}
			h, err := router.NewNative(product.ModelHost, `\\.\pipe\modelhost-declarer`, listen.ServerExpectation{Program: file.Declaration.Program}, []string{"model"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			h.BindingID = file.bindingID()
			var a runtimeAsks
			a.useTierFacts(func() []*router.Host { return []*router.Host{h} }, f.p.managedHost, nil)
			if !a.tierFacts.LocalServer(product.ModelHost) {
				t.Fatalf("%s: local chat through modelhost is not routine for complete", when)
			}
		}
		f.declare(t, f.program("Abstraction Panel.exe"), d)
		routine("a fresh Setup")
		d.Arguments = append(slices.Clone(d.Arguments), "--idle", "10m")
		f.declare(t, f.program("Abstraction Panel.exe"), d)
		routine("a re-declaration through the Panel")
		files, revision, err := f.p.read()
		if err != nil {
			t.Fatal(err)
		}
		existing := files[slices.IndexFunc(files, func(file providerFile) bool { return file.Declaration.Name == product.ModelHost })]
		reconciled := providerFile{Version: providerFileVersion, Declaration: existing.Declaration, DeclaredBy: existing.DeclaredBy}
		reconciled.Declaration.Arguments = append(slices.Clone(existing.Declaration.Arguments), "--model", "store/object")
		if change := f.p.replace(revision, reconciled); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("the runtime's reconcile: %+v", change)
		}
		routine("the runtime's allowlist reconcile")
		// openabstractions models host writing it itself reads third_party.
		f.declare(t, f.program("openabstractions.exe"), d)
		if standing, _ := f.listed(t, product.ModelHost); standing != wire.ProductStandingThirdParty {
			t.Fatalf("the command line's modelhost reads %v", standing)
		}
	})
	t.Run("a third-party program with Setup's exact arguments", func(t *testing.T) {
		f := newDeclarerFixture(t)
		d := setupDeclaration(product.Diffusers, f.program("pytorchd.exe"), t.TempDir())
		f.declare(t, filepath.Join(t.TempDir(), "other-app.exe"), d)
		if standing, _ := f.listed(t, product.Diffusers); standing != wire.ProductStandingThirdParty {
			t.Fatalf("a third-party declaration of pytorch-image reads %v", standing)
		}
	})
	t.Run("a third-party program re-declares over Setup's record", func(t *testing.T) {
		f := newDeclarerFixture(t)
		d := setupDeclaration(product.Diffusers, f.program("pytorchd.exe"), t.TempDir())
		f.declare(t, f.program("Abstraction Panel.exe"), d)
		if standing, _ := f.listed(t, product.Diffusers); standing != wire.ProductStandingProduct {
			t.Fatalf("Setup's pytorch-image reads %v", standing)
		}
		d.Arguments = append(slices.Clone(d.Arguments), "--source", t.TempDir())
		f.declare(t, filepath.Join(t.TempDir(), "other-app.exe"), d)
		standing, file := f.listed(t, product.Diffusers)
		if standing != wire.ProductStandingThirdParty || f.p.declarerComponent(file) != "" {
			t.Fatalf("a re-declaration over Setup's record reads %v, declarer %q", standing, f.p.declarerComponent(file))
		}
	})
	t.Run("Setup declares a name it is not listed for", func(t *testing.T) {
		f := newDeclarerFixture(t)
		d := setupDeclaration(product.Lemonade, f.program("lemonaded.exe"), t.TempDir())
		f.p.mu.Lock()
		f.p.installation = filepath.Join(t.TempDir(), installationDeclarationsDir)
		f.p.mu.Unlock()
		f.declare(t, f.program("Abstraction Panel.exe"), d)
		if standing, _ := f.listed(t, product.Lemonade); standing != wire.ProductStandingThirdParty {
			t.Fatalf("Setup's lemonade-image reads %v", standing)
		}
	})
	t.Run("a Panel image the runtime does not admit", func(t *testing.T) {
		// Security check of A-SEC6: an OA-signed copy of the Panel in the
		// per-user folder, with the Panel's own version resource, is no
		// Panel; neither is the installed path while the runtime admits no
		// image there.
		f := newDeclarerFixture(t)
		user := t.TempDir()
		copyOfPanel := filepath.Join(user, shippedFixtureProgram("Abstraction Panel.exe"))
		if err := os.WriteFile(copyOfPanel, []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
		standingUserInstallFolder = func() string { return user }
		signedByRuntimeSigner(t, copyOfPanel)
		standingOriginalName = func(string) string { return "Abstraction Panel.exe" }
		f.declare(t, copyOfPanel, setupDeclaration(product.Diffusers, f.program("pytorchd.exe"), t.TempDir()))
		standing, file := f.listed(t, product.Diffusers)
		if standing != wire.ProductStandingThirdParty || f.p.declarerComponent(file) != "" {
			t.Fatalf("a signed copy of the Panel: %v, declarer %q", standing, f.p.declarerComponent(file))
		}
		if product.DeclarerComponent(copyOfPanel, standingInstallFolders(), []string{user}, standingSameSigner, standingOriginalName) != "oa:panel" {
			t.Fatal("the fixture's copy does not resolve to oa:panel by file identity alone")
		}
		f.p.admittedPanel = func(string) bool { return false }
		f.declare(t, f.program("Abstraction Panel.exe"), setupDeclaration(product.Diffusers, f.program("pytorchd.exe"), t.TempDir()))
		if standing, _ := f.listed(t, product.Diffusers); standing != wire.ProductStandingThirdParty {
			t.Fatalf("an unadmitted Panel path reads %v", standing)
		}
	})
	t.Run("the same declaration through the command line", func(t *testing.T) {
		f := newDeclarerFixture(t)
		f.declare(t, f.program("openabstractions.exe"), setupDeclaration(product.Diffusers, f.program("pytorchd.exe"), t.TempDir()))
		standing, file := f.listed(t, product.Diffusers)
		if standing != wire.ProductStandingThirdParty || f.p.declarerComponent(file) != "oa:cli" {
			t.Fatalf("the command line's pytorch-image reads %v, declarer %q", standing, f.p.declarerComponent(file))
		}
	})
	t.Run("an exact repeat of the installation's declaration", func(t *testing.T) {
		f := newDeclarerFixture(t)
		installed := f.p.installedDeclarations(nil)
		at := slices.IndexFunc(installed, func(file providerFile) bool { return file.Declaration.Name == product.Lemonade })
		if at < 0 {
			t.Fatal("no installed lemonade-image")
		}
		repeat := installed[at]
		repeat.Source, repeat.DeclaredBy = declarationSourceOperator, filepath.Join(t.TempDir(), "other-app.exe")
		if !f.p.shippedProvider(repeat) {
			t.Fatal("an exact repeat of the installation's lemonade-image lost its standing")
		}
		if standing, _ := f.listed(t, product.Lemonade); standing != wire.ProductStandingProduct {
			t.Fatalf("the installation's lemonade-image reads %v", standing)
		}
	})
}

// FAC-R9: the runtime's own allowlist reconcile of the model host
// (REG-CFG2) is the one other writer of the Panel's modelhost declaration;
// it rewrites the declaration and keeps the recorded declarer, so local
// chat keeps its product standing after the runtime updates what is hosted.
func TestRuntimeReconcileKeepsThePanelDeclarer(t *testing.T) {
	const digest = "55983c5a75a1ab969824077b3bb3de4146e82a9234072b48ad4e8f92ad3fe9f1.gguf"
	f := newDeclarerFixture(t)
	p, err := openFixtureProviders(t, t.TempDir(), false, func(err error) { t.Errorf("providers: %v", err) })
	if err != nil {
		t.Fatal(err)
	}
	p.admittedPanel = f.p.admittedPanel
	panel := f.program("Abstraction Panel.exe")
	programs, err := modelHostRuntimePrograms()
	if err != nil {
		t.Fatal(err)
	}
	settings := hostSettings{program: f.program("modelhostd.exe"), endpoint: "modelhost-reconcile-test", engine: filepath.Join(t.TempDir(), "llama-server.exe")}
	declared := []hostedModel{{Name: digest, Store: "oa-chat-1", Object: "sha256:945b"}}
	file := providerFile{Version: providerFileVersion, DeclaredBy: panel, DeclaredAt: 1, Declaration: declarationOf(modelHostDeclaration(settings, declared, programs))}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if added := p.add(revision, file); added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	standing := func(when string) providerFile {
		t.Helper()
		files, _, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		at := slices.IndexFunc(files, func(file providerFile) bool { return file.Declaration.Name == product.ModelHost })
		if at < 0 || !p.shippedProvider(files[at]) {
			t.Fatalf("%s: the Panel's modelhost has no product standing", when)
		}
		return files[at]
	}
	before := standing("declared by the Panel")
	setupCopy := heldGGUF("oa-chat-1", "sha256:945b", "sha256:945b", map[string]string{"family": "qwen3.6-35b-a3b", "architecture": "qwen35moe"})
	setupCopy.Manifest.Names = []content.Name{{Scheme: "oa-chat-1", Name: digest}}
	composed := storageservice.ComposedInventory{Stores: readStores("oa-chat-1"), Manifests: []content.ManifestHolders{setupCopy}}
	if wrote, err := p.reconcileModelHost(context.Background(), composed, func(string, string) bool { return false }); err != nil || wrote == "" {
		t.Fatalf("reconcile: %q %v", wrote, err)
	}
	after := standing("after the runtime's reconcile")
	if !samePrograms(after.DeclaredBy, panel) || slices.Equal(after.Declaration.Arguments, before.Declaration.Arguments) {
		t.Fatalf("the reconcile wrote %v declared by %s", after.Declaration.Arguments, after.DeclaredBy)
	}
}

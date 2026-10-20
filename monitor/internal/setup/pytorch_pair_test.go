package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

var (
	kleinFull    = torch.Flux2Klein4BTorch.Name
	kleinCompact = torch.Flux2Klein4BCompactTorch.Name
	zImage       = torch.ZImageTurboTorch.Name
)

// The served pair comes from the installed sets alone: the person's default
// picture model first, then the pinned table's order, two at most.
func TestPyTorchPairFollowsTheDefaultThenTheTable(t *testing.T) {
	all := []string{zImage, kleinCompact, kleinFull}
	for _, c := range []struct {
		installed []string
		preferred string
		want      []string
	}{
		{nil, "", nil},
		{[]string{kleinCompact}, "", []string{kleinCompact}},
		{[]string{zImage, kleinCompact}, "", []string{kleinCompact, zImage}},
		{all, "", []string{kleinFull, kleinCompact}},
		{all, zImage, []string{zImage, kleinFull}},
		{all, kleinCompact, []string{kleinCompact, kleinFull}},
		{[]string{kleinCompact}, zImage, []string{kleinCompact}},
		{all, "user.LocalApprovedKlein", []string{kleinFull, kleinCompact}},
	} {
		if got := pyTorchPair(c.installed, c.preferred); !slices.Equal(got, c.want) {
			t.Fatalf("installed %v, default %q: %v, want %v", c.installed, c.preferred, got, c.want)
		}
	}
}

// pairFixture is the owner's R22 state: Setup's registration serves the
// compact Klein, the registry lists it, and the PyTorch engine is
// installed. Its seams report what is installed, the default, whether the
// store was read, the wake lease and each models install, which records the
// set's places as pytorchd does.
type pairFixture struct {
	j         Journal
	a         Artifact
	before    fwire.DeclarationWithExtensions
	registry  *theNoiseRegistryFake
	events    *[]string
	installed []string
	preferred string
	readErr   error
	read      bool
	installs  []string
	wakes     []string
}

func newPairFixture(t *testing.T) *pairFixture {
	t.Helper()
	ctx := context.Background()
	j, state, a, _, root := pyTorchFixture(t)
	state, _, err := j.allocatePyTorchRoot(ctx, state, root)
	if err != nil {
		t.Fatal(err)
	}
	writePyTorchReady(t, root)
	e := state.Effects["pytorch-engine"]
	e.Phase = Installed
	state.Effects["pytorch-engine"] = e
	before, err := expectedPyTorchDeclaration(a, []string{kleinCompact})
	if err != nil {
		t.Fatal(err)
	}
	state.EngineDeclarations = map[AppID]fwire.DeclarationWithExtensions{PyTorch: before}
	writePyTorchState(t, j, state)
	_, _, providerState, _, err := pyTorchFolders(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(providerState, 0o700); err != nil {
		t.Fatal(err)
	}
	events := []string{}
	f := &pairFixture{j: j, a: a, before: before, events: &events,
		registry: &theNoiseRegistryFake{declaration: &before, readiness: fwire.DeclarationReadinessIdle,
			withdrawOutcome: fwire.DeclarationEditOutcomeApplied, declareOutcome: fwire.DeclarationEditOutcomeApplied, events: &events}}
	oldRegistry, oldCLI, oldInstalled, oldDefault, oldRead, oldWake := setupEngineRegistry, pyTorchCLI, pictureSetsInstalled, pictureDefaultModel, pictureStoreRead, pictureWake
	setupEngineRegistry = func(context.Context) (engineRegistry, error) { return f.registry, nil }
	pyTorchCLI = func(_ context.Context, args ...string) error {
		if len(args) < 4 || args[0] != "models" || args[1] != "install" || args[2] != "--set" {
			t.Fatalf("unexpected effect %v", args)
		}
		set, _ := torch.TorchModelSetNamed(args[3])
		f.installs = append(f.installs, args[3])
		return os.WriteFile(torch.TorchModelPlaces(providerState, set), []byte("places"), 0o600)
	}
	pictureSetsInstalled = func(Home) []string { return slices.Clone(f.installed) }
	pictureDefaultModel = func(context.Context) (string, error) { return f.preferred, f.readErr }
	pictureStoreRead = func(context.Context) (bool, error) { return f.read, nil }
	pictureWake = func(why string) (func(), error) {
		f.wakes = append(f.wakes, why)
		return func() { f.wakes = append(f.wakes, "released") }, nil
	}
	t.Cleanup(func() {
		setupEngineRegistry, pyTorchCLI, pictureSetsInstalled, pictureDefaultModel, pictureStoreRead, pictureWake =
			oldRegistry, oldCLI, oldInstalled, oldDefault, oldRead, oldWake
	})
	return f
}

// served is the image sets the registry's PyTorch registration declares.
func (f *pairFixture) served() []string {
	if f.registry.declaration == nil {
		return nil
	}
	var out []string
	for _, m := range f.registry.declaration.Declaration.Models {
		out = append(out, m[len("image:"):])
	}
	return out
}

// An upgrade from R22: Z-Image-Turbo's files are installed and nothing is
// clicked. The registration serving the compact Klein stays, Klein listed,
// until the storage inventory has read OA's picture store; then both sets
// are proved once under a wake lease and the registration is replaced in
// one edit, with no withdrawal, so Klein is listed throughout.
func TestInstalledZImageJoinsWithoutAClickAndKleinStaysListed(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed = []string{kleinCompact, zImage}

	if err := ReconcilePictureEngine(ctx, f.j); !errors.Is(err, errPictureStoreUnread) {
		t.Fatalf("an unread store: %v", err)
	}
	if !slices.Equal(f.served(), []string{kleinCompact}) || len(*f.events) != 0 || len(f.installs) != 0 || len(f.wakes) != 0 {
		t.Fatalf("before the store is read: served %v, events %v, installs %v", f.served(), *f.events, f.installs)
	}
	f.read = true
	if err := ReconcilePictureEngine(ctx, f.j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.served(), []string{kleinCompact, zImage}) || !slices.Equal(*f.events, []string{"replace"}) || f.registry.declares != 0 {
		t.Fatalf("after the store is read: served %v, events %v, declares %d", f.served(), *f.events, f.registry.declares)
	}
	if !slices.Equal(f.installs, []string{kleinCompact, zImage}) || len(f.wakes) != 2 || f.wakes[1] != "released" || f.wakes[0] == "" {
		t.Fatalf("installs %v, wake %v", f.installs, f.wakes)
	}
	recorded, err := f.j.Load(ctx)
	if err != nil || !sameEngineDeclaration(recorded.EngineDeclarations[PyTorch], *f.registry.declaration) {
		t.Fatalf("the journal records another declaration: %v", err)
	}
	// A second start with nothing changed does nothing.
	*f.events, f.installs = (*f.events)[:0], nil
	if err := ReconcilePictureEngine(ctx, f.j); err != nil || len(*f.events) != 0 || len(f.installs) != 0 {
		t.Fatalf("a current registration was changed: %v %v %v", err, *f.events, f.installs)
	}
}

// The owner's R24 state: the Panel declared Setup's compact-Klein
// registration on 11-09, the journal records it with an empty extension
// list, and the registry lists it back through its codec, which drops that
// list. Z-Image-Turbo's files are installed and the store is read: Setup
// recognises its own registration and replaces it with the pair.
func TestR24ListedRegistrationWithoutExtensionsIsSetupsOwn(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed, f.read = []string{kleinCompact, zImage}, true
	f.registry.declaredBy = `C:\Program Files\OpenAbstractions\tools\Abstraction Panel.exe`
	args := f.before.Declaration.Arguments
	shape := []string{"serve", "--endpoint", "{endpoint}", "--runtime-program", "", "--root", "", "--source", "", "--state", "", "--image-model", kleinCompact, "--resource", "card:0"}
	for i := range shape {
		if shape[i] != "" && (i >= len(args) || args[i] != shape[i]) || len(args) != len(shape) {
			t.Fatalf("Setup's declaration has another shape than the owner's: %q", args)
		}
	}
	state, err := f.j.Load(ctx)
	if err != nil || state.EngineDeclarations[PyTorch].SupportedExtensions == nil {
		t.Fatalf("the journal no longer records an empty extension list: %v", err)
	}
	page, err := f.registry.Declarations(ctx)
	if err != nil || len(page.Declarations) != 1 || page.Declarations[0].SupportedExtensions != nil {
		t.Fatalf("the codec kept the empty extension list: %+v %v", page, err)
	}
	if err := ReconcilePictureEngine(ctx, f.j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.served(), []string{kleinCompact, zImage}) || !slices.Equal(*f.events, []string{"replace"}) || f.registry.declares != 0 {
		t.Fatalf("served %v, events %v, declares %d", f.served(), *f.events, f.registry.declares)
	}
	// A declaration that differs in a Setup-owned argument stays foreign.
	foreign := *f.registry.declaration
	foreign.Declaration.Arguments = slices.Clone(foreign.Declaration.Arguments)
	foreign.Declaration.Arguments[slices.Index(foreign.Declaration.Arguments, "--state")+1] = `C:\elsewhere`
	f.registry.declaration = &foreign
	if _, _, err := registryDeclaration(ctx, f.registry, f.before); !errors.Is(err, errForeignDeclaration) {
		t.Fatalf("a person's edited registration was claimed: %v", err)
	}
}

// The proof reads the files once per engine package: a later start that
// changes the pair re-reads no set proved for the same package, and a set
// proved for an earlier package is proved again.
func TestPyTorchProofRunsOncePerEnginePackage(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed, f.read = []string{kleinCompact, zImage}, true
	if err := ReconcilePictureEngine(ctx, f.j); err != nil || len(f.installs) != 2 {
		t.Fatalf("first start: %v %v", err, f.installs)
	}
	// The person makes Z-Image the default: the pair's order changes.
	f.installs, f.wakes, f.preferred = nil, nil, zImage
	if err := ReconcilePictureEngine(ctx, f.j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.served(), []string{zImage, kleinCompact}) || len(f.installs) != 0 || len(f.wakes) != 0 {
		t.Fatalf("an unchanged engine package was proved again: served %v, installs %v, wake %v", f.served(), f.installs, f.wakes)
	}
	// The compact Klein's proof belongs to an earlier engine package.
	state, err := f.j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.PyTorchProved[kleinCompact] = "an-earlier-package"
	writePyTorchState(t, f.j, state)
	f.preferred = ""
	if err := ReconcilePictureEngine(ctx, f.j); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.installs, []string{kleinCompact}) || !slices.Equal(f.served(), []string{kleinCompact, zImage}) {
		t.Fatalf("installs %v, served %v", f.installs, f.served())
	}
}

// With three sets installed the registration serves two; the default picks
// which.
func TestThreeInstalledSetsServeTwo(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed, f.read = []string{kleinFull, kleinCompact, zImage}, true
	if err := ReconcilePictureEngine(ctx, f.j); err != nil || !slices.Equal(f.served(), []string{kleinFull, kleinCompact}) {
		t.Fatalf("served %v: %v", f.served(), err)
	}
	f.preferred = zImage
	if err := ReconcilePictureEngine(ctx, f.j); err != nil || !slices.Equal(f.served(), []string{zImage, kleinFull}) {
		t.Fatalf("served %v: %v", f.served(), err)
	}
	var served []string
	for i, arg := range f.registry.declaration.Declaration.Arguments {
		if arg == "--image-model" {
			served = append(served, f.registry.declaration.Declaration.Arguments[i+1])
		}
	}
	if !slices.Equal(served, []string{zImage, kleinFull}) {
		t.Fatalf("pytorchd serves %v", served)
	}
}

// A registration the person removed stays removed: nothing is proved,
// declared or replaced.
func TestRemovedPyTorchRegistrationStaysRemoved(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed, f.read = []string{kleinCompact, zImage}, true
	f.registry.declaration = nil
	if err := ReconcilePictureEngine(ctx, f.j); err != nil || len(*f.events) != 0 || len(f.installs) != 0 || f.registry.declares != 0 {
		t.Fatalf("a removed registration was recreated: %v %v %v %d", err, *f.events, f.installs, f.registry.declares)
	}
}

// Without a recorded registration the first one is declared from what is
// installed.
func TestFirstPyTorchRegistrationServesTheInstalledPair(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	state, err := f.j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.EngineDeclarations = nil
	writePyTorchState(t, f.j, state)
	f.registry.declaration = nil
	f.installed = []string{zImage, kleinCompact}
	if err := reconcilePyTorchRegistration(ctx, f.j, 0); err != nil {
		t.Fatal(err)
	}
	if f.registry.declares != 1 || !slices.Equal(f.served(), []string{kleinCompact, zImage}) {
		t.Fatalf("declares %d, served %v", f.registry.declares, f.served())
	}
}

// The pair is the same whether the default names a model that is no
// picture set or is unset; a default that cannot be read changes nothing,
// so a failed read never reorders the registration or drops the default's
// set from it.
func TestPyTorchPairStaysWhenTheDefaultIsUnreadable(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed, f.read, f.preferred = []string{kleinFull, kleinCompact, zImage}, true, zImage
	if err := ReconcilePictureEngine(ctx, f.j); err != nil || !slices.Equal(f.served(), []string{zImage, kleinFull}) {
		t.Fatalf("served %v: %v", f.served(), err)
	}
	*f.events = (*f.events)[:0]
	f.readErr = errors.New("the runtime is not answering")
	if err := ReconcilePictureEngine(ctx, f.j); err == nil || len(*f.events) != 0 || !slices.Equal(f.served(), []string{zImage, kleinFull}) {
		t.Fatalf("an unreadable default changed the registration: %v %v %v", err, *f.events, f.served())
	}
	f.readErr = nil
	for _, preferred := range []string{"", "qwen3-4b-chat"} {
		f.preferred = preferred
		if err := ReconcilePictureEngine(ctx, f.j); err != nil || !slices.Equal(f.served(), []string{kleinFull, kleinCompact}) {
			t.Fatalf("default %q: served %v: %v", preferred, f.served(), err)
		}
	}
}

// pairCatalogMovedOn makes f's journal read a later release's catalog,
// whose PyTorch engine is another package; known keeps the installed pin
// among the catalog's earlier pins.
func pairCatalogMovedOn(t *testing.T, f *pairFixture, known bool) {
	t.Helper()
	next := *f.j.catalogOverride
	next.Components = append([]Component(nil), next.Components...)
	next.PreviousInstallPins = nil
	if known {
		next.PreviousInstallPins = []KnownInstallPin{{ComponentID: "pytorch-engine", Artifact: f.a}}
	}
	for i := range next.Components {
		if next.Components[i].ID == "pytorch-engine" {
			later := f.a
			later.Version = "2.12.0"
			later.SHA256 = strings.Repeat("c", 64)
			next.Components[i].Artifact = &later
		}
	}
	f.j.catalogOverride = &next
	state, err := f.j.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.j.Authorize(state.Plan) == nil {
		t.Fatal("the journal's plan still matches the release catalog")
	}
}

// The owner's R23: the release catalog pins another PyTorch package than
// the one Setup installed, from an earlier release or an earlier preview
// build. Setup's recorded registration still comes to serve the installed
// Z-Image beside Klein from the receipt-matched installed engine.
func TestInstalledZImageJoinsAfterTheCatalogMovedOn(t *testing.T) {
	for _, known := range []bool{true, false} {
		t.Run(map[bool]string{true: "earlier-release-pin", false: "earlier-preview-pin"}[known], func(t *testing.T) {
			ctx := context.Background()
			f := newPairFixture(t)
			f.installed, f.read = []string{kleinCompact, zImage}, true
			pairCatalogMovedOn(t, f, known)
			if !known {
				// The preview build's package folder served Setup's transfer.
				pairInstalledBy(t, f, localPackageWork(workKey("pytorch-engine", f.a), filepath.Join(t.TempDir(), "pytorch-inputs.zip")))
			}
			if err := ReconcilePictureEngine(ctx, f.j); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.served(), []string{kleinCompact, zImage}) || !slices.Equal(*f.events, []string{"replace"}) {
				t.Fatalf("served %v, events %v", f.served(), *f.events)
			}
			if !slices.Equal(f.installs, []string{kleinCompact, zImage}) {
				t.Fatalf("installs %v", f.installs)
			}
		})
	}
}

// An earlier preview build's pin never starts a first registration, and an
// installed tree that no longer matches its receipt is served by nothing new.
func TestPyTorchReconcileRefusesWhatSetupNeverRegisteredOrChanged(t *testing.T) {
	ctx := context.Background()
	f := newPairFixture(t)
	f.installed, f.read = []string{kleinCompact, zImage}, true
	state, err := f.j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.EngineDeclarations = nil
	writePyTorchState(t, f.j, state)
	f.registry.declaration = nil
	pairCatalogMovedOn(t, f, false)
	if err := reconcilePyTorchRegistration(ctx, f.j, 0); !errors.Is(err, errUnknownInstalledPin) || f.registry.declares != 0 || len(f.installs) != 0 {
		t.Fatalf("an unrecorded preview pin was registered: %v, declares %d, installs %v", err, f.registry.declares, f.installs)
	}

	g := newPairFixture(t)
	g.installed, g.read = []string{kleinCompact, zImage}, true
	pairCatalogMovedOn(t, g, false)
	pairInstalledBy(t, g, localPackageWork(workKey("pytorch-engine", g.a), filepath.Join(t.TempDir(), "pytorch-inputs.zip")))
	inputs, _, _, _, err := pyTorchFolders(g.a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputs, "source", "infer", "fixture.py"), []byte("changed after install"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcilePictureEngine(ctx, g.j); err == nil || errors.Is(err, errUnrecordedPin) || len(*g.events) != 0 || len(g.installs) != 0 || !slices.Equal(g.served(), []string{kleinCompact}) {
		t.Fatalf("a changed installed engine was reconciled: %v, events %v, installs %v", err, *g.events, g.installs)
	}
}

// pairInstalledBy records work as the transfer Setup installed f's PyTorch
// package by.
func pairInstalledBy(t *testing.T, f *pairFixture, work WorkIdentity) {
	t.Helper()
	state, err := f.j.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	effect := state.Effects["pytorch-engine"]
	effect.Work = &work
	state.Effects["pytorch-engine"] = effect
	writePyTorchState(t, f.j, state)
}

// An earlier pin serves Setup's registration only when Setup's own transfer
// installed it from an OpenAbstractions release asset. A pin placed in the
// journal by hand, a transfer of other bytes or a preview package of
// another asset changes nothing.
func TestPyTorchReconcileRefusesAnEarlierPinSetupNeverInstalled(t *testing.T) {
	for name, work := range map[string]func(a Artifact, dir string) *WorkIdentity{
		"hand-placed": func(Artifact, string) *WorkIdentity { return nil },
		"other-bytes": func(a Artifact, dir string) *WorkIdentity {
			a.SHA256 = strings.Repeat("b", 64)
			w := localPackageWork(workKey("pytorch-engine", a), filepath.Join(dir, "pytorch-inputs.zip"))
			return &w
		},
		"other-asset": func(a Artifact, dir string) *WorkIdentity {
			w := localPackageWork(workKey("pytorch-engine", a), filepath.Join(dir, "other-inputs.zip"))
			return &w
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newPairFixture(t)
			f.installed, f.read = []string{kleinCompact, zImage}, true
			pairCatalogMovedOn(t, f, false)
			state, err := f.j.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			effect := state.Effects["pytorch-engine"]
			effect.Work = work(f.a, t.TempDir())
			state.Effects["pytorch-engine"] = effect
			writePyTorchState(t, f.j, state)
			if err := ReconcilePictureEngine(context.Background(), f.j); !errors.Is(err, errUnrecordedPin) || len(*f.events) != 0 || len(f.installs) != 0 || !slices.Equal(f.served(), []string{kleinCompact}) {
				t.Fatalf("an unrecorded pin changed the registration: %v, events %v, installs %v", err, *f.events, f.installs)
			}
		})
	}
}

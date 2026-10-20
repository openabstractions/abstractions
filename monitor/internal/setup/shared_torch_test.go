package setup

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	torch "github.com/openabstractions/abstraction-provider-pytorch/go/torch"
)

// OpenAbstractions builds no ComfyUI (owner, 2026-11-11): the release
// catalog's OA build component carries no pin and no PyTorch link, so no
// plan installs it, and a plan naming ComfyUI is blocked.
func TestTheReleaseInstallsNoComfyBuild(t *testing.T) {
	c := catalogForTarget("windows", "amd64")
	var comfy Component
	for _, component := range c.Components {
		if component.ID == "comfyui-app" {
			comfy = component
		}
	}
	if comfy.Artifact != nil || len(comfy.Dependencies) != 0 || len(comfy.DependencyVersions) != 0 || comfy.Pending == "" {
		t.Fatalf("ComfyUI build component: %+v", comfy)
	}
	plan := mustPlanOf(t, c, ComfyUI, Lemonade)
	if planHasPyTorch(plan) || plan.Preparable {
		t.Fatalf("a plan naming ComfyUI installs something: %+v", plan)
	}
	if c.Available(ComfyUI) {
		t.Fatal("the release offers the OA build of ComfyUI")
	}
	// The automatic driver waits for a dependency before the package that
	// needs it.
	comfy.Dependencies = []string{"pytorch-engine"}
	status := Status{CurrentCatalog: true, Plan: Plan{Preparable: true, Components: []PlannedComponent{
		{Component: comfy}, {Component: Component{ID: "pytorch-engine"}}}},
		Components: []ComponentStatus{{ID: "comfyui-app"}, {ID: "pytorch-engine"}}}
	status.Components[0].Effect.Phase, status.Components[1].Effect.Phase = Pending, Pending
	if got := status.NextAutomatic(); got != "pytorch-engine" {
		t.Fatalf("first automatic package %q", got)
	}
	status.Components[1].Effect.Phase = Installed
	if got := status.NextAutomatic(); got != "comfyui-app" {
		t.Fatalf("package after the component %q", got)
	}
}

// The person's own ComfyUI runs on any graphics card: the row is offered on
// every computer, and no plan is gated on the card for it.
func TestComfyIsOfferedOnEveryCard(t *testing.T) {
	c := catalogForTarget("windows", "amd64")
	old := machineGraphics
	t.Cleanup(func() { machineGraphics = old })
	for _, hardware := range [][]string{{`PCI\VEN_1002&DEV_1586&SUBSYS_38291043&REV_C1`}, {`PCI\VEN_10DE&DEV_2684&SUBSYS_0`}, nil} {
		machineGraphics = func() ([]string, error) { return hardware, nil }
		if !c.Offered(ComfyUI) || !c.Offered(Lemonade) || !c.Offered(OpenCode) {
			t.Fatalf("offer on %v", hardware)
		}
		if plan := mustPlanOf(t, c, ComfyUI, Lemonade); strings.Contains(strings.Join(plan.Blockers, " "), "graphics") {
			t.Fatalf("plan on %v gated on the card: %v", hardware, plan.Blockers)
		}
	}
	machineGraphics = func() ([]string, error) { return nil, errors.New("unreadable") }
	if !c.Offered(ComfyUI) {
		t.Fatal("an unreadable card hid ComfyUI")
	}
}

func TestComfyPthNamesTheInstalledComponentSitePackages(t *testing.T) {
	data := standInHome(t)
	c := fixtureCatalog()
	for i := range c.Components {
		if c.Components[i].ID == "comfyui-app" {
			c.Components[i].Dependencies = []string{"pytorch-engine"}
		}
	}
	state := State{Plan: mustPlanOf(t, c, ComfyUI, Lemonade)}
	journal := Journal{catalogOverride: &c}
	component := RetainedArchiveFor(ComfyUI).Component()
	for _, other := range []string{"", component + "-other"} {
		policy := nativeArchiveInstallPolicyFor(other)
		if policy.runtimeSite != nil || policy.afterReadback != nil {
			t.Fatalf("unrelated component %q inherited retained archive hooks", other)
		}
	}
	policy := nativeArchiveInstallPolicyFor(component)
	if policy.runtimeSite == nil || policy.afterReadback == nil {
		t.Fatal("the compiled retained archive did not select its install policy")
	}
	site, err := policy.runtimeSite(journal, state)
	if err != nil {
		t.Fatal(err)
	}
	a, err := journal.ComponentArtifact(state, "pytorch-engine")
	if err != nil {
		t.Fatal(err)
	}
	want := torch.Component{Root: filepath.Join(data, "OA", "e", "pt", a.Version, "c")}.SitePackages()
	if site != want || !strings.HasSuffix(site, filepath.Join("engine", "venv", "Lib", "site-packages")) {
		t.Fatalf("site %q, want %q", site, want)
	}
	if site, err := policy.runtimeSite(journal, State{Plan: mustPlanOf(t, c, Lemonade)}); err != nil || site != "" {
		t.Fatalf("plan without the component named %q: %v", site, err)
	}
	if _, err := runNativeArchiveCommandWith(context.Background(), sevenZipPin{}, nativeArchiveCheck, "", site); err == nil {
		t.Fatal("the check operation accepted a component site")
	}
	if _, err := runNativeArchiveCommandWith(context.Background(), sevenZipPin{}, nativeArchiveSeal, "", "relative"); err == nil {
		t.Fatal("a relative component site was accepted")
	}
}

// A package that names the PyTorch component version it was built against
// is blocked by a plan pinning another version, and waits until that version
// is the installed one.
func TestComfyPinsThePyTorchComponentVersion(t *testing.T) {
	fixture := fixtureCatalog()
	for i := range fixture.Components {
		if fixture.Components[i].ID == "comfyui-app" {
			fixture.Components[i].Dependencies = []string{"pytorch-engine"}
			fixture.Components[i].DependencyVersions = map[string]string{"pytorch-engine": "1.2.3"}
		}
	}
	if err := fixture.Validate(); err != nil {
		t.Fatal(err)
	}
	plan := mustPlanOf(t, fixture, ComfyUI, Lemonade)
	if strings.Contains(strings.Join(plan.Blockers, " "), "pytorch-engine") {
		t.Fatalf("matching versions blocked: %v", plan.Blockers)
	}
	for i := range fixture.Components {
		if fixture.Components[i].ID == "comfyui-app" {
			fixture.Components[i].DependencyVersions["pytorch-engine"] = "2.12.0"
		}
	}
	plan = mustPlanOf(t, fixture, ComfyUI, Lemonade)
	if plan.Preparable || !strings.Contains(strings.Join(plan.Blockers, " "), "needs pytorch-engine 2.12.0; this release pins 1.2.3") {
		t.Fatalf("another component version was not blocked: %v", plan.Blockers)
	}
	comfy, _ := planComponent(plan, "comfyui-app")
	state := State{Plan: plan, Effects: map[string]Effect{"pytorch-engine": {Phase: Installed}}}
	r := Runner{Journal: Journal{catalogOverride: &fixture}}
	if err := r.dependenciesInstalled(state, "comfyui-app", comfy); err == nil || !strings.Contains(err.Error(), "1.2.3 is installed") {
		t.Fatalf("installed version mismatch: %v", err)
	}
	broken := fixture
	broken.Components = slices.Clone(fixture.Components)
	for i := range broken.Components {
		if broken.Components[i].ID == "lemonade-engine" {
			broken.Components[i].DependencyVersions = map[string]string{"pytorch-engine": "1.2.3"}
		}
	}
	if broken.Validate() == nil {
		t.Fatal("a version pin without its dependency validated")
	}
}

// Removing the PyTorch component is refused while Setup's ComfyUI package,
// which runs its torch from it, has files installed.
func TestPyTorchRemovalWaitsForComfyRemoval(t *testing.T) {
	j, state, _, _, _ := pyTorchFixture(t)
	state.Effects["comfyui-app"] = Effect{Phase: Installed, Revision: 1}
	writePyTorchState(t, j, state)
	old := pyTorchCLI
	pyTorchCLI = func(context.Context, ...string) error { t.Fatal("refused removal ran the CLI"); return nil }
	t.Cleanup(func() { pyTorchCLI = old })
	err := NewRunner(j, nil).removePyTorchLocked(context.Background())
	if err == nil || !strings.Contains(err.Error(), "remove ComfyUI first") {
		t.Fatalf("removal while ComfyUI is installed: %v", err)
	}
	for _, phase := range []Phase{"", Pending, Removed} {
		if err := guardCurrentArchiveEngine(State{Effects: map[string]Effect{"comfyui-app": {Phase: phase}}}); err != nil {
			t.Fatalf("%q held the component: %v", phase, err)
		}
	}
	for _, phase := range []Phase{Requested, Verified, Installing, Installed, Staged, Checked, Uncertain, Failed} {
		if guardCurrentArchiveEngine(State{Effects: map[string]Effect{"comfyui-app": {Phase: phase}}}) == nil {
			t.Fatalf("%q did not hold the component", phase)
		}
	}
}

// A PyTorch update activates into its own version folder; installed
// ComfyUI's oa-pytorch.pth is resealed to the new site-packages first, and
// a refused reseal keeps the earlier version active.
func TestPyTorchUpdateResealsComfyPth(t *testing.T) {
	j, state, a, _, root := pyTorchFixture(t)
	// The plan that installed ComfyUI carries both packages.
	state.Plan = mustPlanOf(t, *j.catalogOverride, ComfyUI, PyTorch)
	state.ApprovedPlanID = state.Plan.ID
	writePyTorchState(t, j, state)
	state, _, err := j.allocatePyTorchRoot(context.Background(), state, root)
	if err != nil {
		t.Fatal(err)
	}
	writePyTorchReady(t, root)
	retained := a
	retained.Version = "0.9.0"
	effect := state.Effects["pytorch-engine"]
	effect.Phase, effect.Retained = Checked, &retained
	state.Effects["pytorch-engine"] = effect
	state.Effects["comfyui-app"] = Effect{Phase: Installed, Revision: 1}
	writePyTorchState(t, j, state)
	archivePin, err := j.ComponentArtifact(state, retainedArchiveComponent)
	if err != nil {
		t.Fatal(err)
	}

	var sites []string
	old := resealArchiveRuntime
	t.Cleanup(func() { resealArchiveRuntime = old })
	resealArchiveRuntime = func(_ context.Context, artifact Artifact, site string) error {
		if !reflect.DeepEqual(artifact, archivePin) {
			t.Fatalf("reseal received a different archive: %+v", artifact)
		}
		sites = append(sites, site)
		return errors.New("fixture refusal")
	}
	r := NewRunner(j, nil)
	if _, err := r.activate(context.Background(), state, "pytorch-engine", a, effect); err == nil || !strings.Contains(err.Error(), "earlier version stays active") {
		t.Fatalf("refused reseal: %v", err)
	}
	if after, err := j.Load(context.Background()); err != nil || after.Effects["pytorch-engine"].Phase != Checked {
		t.Fatalf("refused reseal activated the update: %v", err)
	}
	resealArchiveRuntime = func(_ context.Context, _ Artifact, site string) error {
		sites = append(sites, site)
		return nil
	}
	if _, err := r.activate(context.Background(), state, "pytorch-engine", a, effect); err != nil {
		t.Fatal(err)
	}
	if after, err := j.Load(context.Background()); err != nil || after.Effects["pytorch-engine"].Phase != Installed {
		t.Fatalf("update not activated: %v", err)
	}
	want := pyTorchSitePackages(root)
	if len(sites) != 2 || sites[1] != want || !strings.Contains(want, filepath.Join("pt", a.Version, "c")) {
		t.Fatalf("resealed sites %v, want %q", sites, want)
	}
	sites = nil
	state.Effects[retainedArchiveComponent] = Effect{Phase: Installed, Removing: true}
	if err := r.resealCurrentArchiveEngine(context.Background(), state, want, a.Version); err != nil || len(sites) != 0 {
		t.Fatalf("a removing archive was selected for reseal: %v %v", sites, err)
	}

	// A displaced archive still guards its own engine. Activation only
	// reseals the current installed archive, never the displaced record.
	sites = nil
	_, archive, _, _ := nativeArchiveFixture(t)
	state.RetainedArchive = &KeptPackage{Artifact: archive, Effect: Effect{Phase: Installed}}
	state.Effects["comfyui-app"] = Effect{}
	state.Effects[fixtureComponent] = Effect{Phase: Installed}
	if err := r.resealCurrentArchiveEngine(context.Background(), state, want, a.Version); err != nil || len(sites) != 0 {
		t.Fatalf("an unrelated or displaced archive was selected for reseal: %v %v", sites, err)
	}
}

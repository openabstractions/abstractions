package setup

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/client"
	thenoise "github.com/openabstractions/abstraction-provider-thenoise/go"
)

// fixtureApp is the neutral choice, and fixtureComponent its package kind, of
// the generic update, reconcile and runner tests: one archive with a program
// and a settings file, a health check that wants the program, and no
// application behind either. The release catalog has neither.
const (
	fixtureApp       AppID = "fixture"
	fixtureComponent       = "fixture-app"
	fixtureProgram         = "fixture/fixture.exe"
	fixtureSettings        = "fixture/settings.json"
)

func init() {
	testChoices[fixtureApp] = true
	testPackageKinds[fixtureComponent] = func(_ Artifact, root string) error {
		if regularFile(filepath.Join(root, filepath.FromSlash(fixtureProgram))) != nil {
			return unhealthy("the fixture package lacks its program")
		}
		return nil
	}
}

// withoutSharedTorch drops ComfyUI's dependency on the shared PyTorch
// component and its gfx1151 gate, which the OA build carried until the
// release stopped building ComfyUI (2026-11-11). Archive, update and journal
// mechanics fixtures predate that component.
func withoutSharedTorch(c Catalog) Catalog {
	c.Choices = slices.Clone(c.Choices)
	for i := range c.Choices {
		if c.Choices[i].ID == ComfyUI {
			c.Choices[i].Graphics = ""
		}
	}
	c.Components = slices.Clone(c.Components)
	for i := range c.Components {
		if c.Components[i].ID == "comfyui-app" {
			c.Components[i].Dependencies = nil
			c.Components[i].DependencyVersions = nil
		}
	}
	return c
}

func fixtureCatalog() Catalog {
	c := withoutSharedTorch(NewCatalog())
	c.Choices = append(c.Choices, Choice{ID: fixtureApp, Title: "Fixture", Components: []string{fixtureComponent}})
	c.Components = append(c.Components, Component{ID: fixtureComponent, Title: "Fixture package"})
	for i := range c.Components {
		if c.Components[i].ID == "thenoise-engine" {
			continue // the upstream package remains a fixed reviewed pin.
		}
		c.Components[i].Pending = ""
		c.Components[i].Artifact = &Artifact{
			Version: "1.2.3", SourceCommit: strings.Repeat("a", 40),
			SourceURL: "https://github.com/openabstractions/setup-fixture/commit/" + strings.Repeat("a", 40),
			Platform:  "windows/amd64",
			URL:       "https://github.com/openabstractions/setup-fixture/releases/download/v1.2.3/" + c.Components[i].ID + ".zip",
			SHA256:    strings.Repeat("b", 64), Bytes: 10, PeakBytes: 20,
			License: "Apache-2.0", Publisher: "OpenAbstractions",
			Signature:       "test fixture",
			InstallIdentity: "oa-setup-" + c.Components[i].ID, Action: "archive_per_user",
		}
		fixturePackagePins(c.Components[i].ID, c.Components[i].Artifact)
	}
	return c
}

// fixturePackagePins adds the per-kind pins a catalog entry must carry.
func fixturePackagePins(componentID string, a *Artifact) {
	switch componentID {
	case "comfyui-app", "pytorch-engine":
		a.SourceArchiveURL = "https://github.com/openabstractions/setup-fixture/releases/download/v1.2.3/" + componentID + "-source.zip"
		a.SourceArchiveSHA256 = strings.Repeat("d", 64)
		a.SourceArchiveBytes = 30
		if componentID == "comfyui-app" {
			a.RecordsSHA256 = strings.Repeat("e", 64)
		} else {
			a.InstallIdentity = "pt"
		}
	case "lemonade-engine":
		a.TestedBackendSHA256 = "b9248856707de2e125538ede8a7dc71a9d800bba67e469e60881dae2a5a1c0a2"
		a.TestedModelRevision = bf16Revision
	}
}

func TestCatalogHoldsEachPackageKindToItsPairedPins(t *testing.T) {
	if err := catalogForTarget("windows", "amd64").Validate(); err != nil {
		t.Fatalf("release catalog: %v", err)
	}
	for _, test := range []struct {
		component string
		change    func(*Artifact)
	}{
		{"comfyui-app", func(a *Artifact) { a.RecordsSHA256 = "" }},
		{"comfyui-app", func(a *Artifact) { a.SourceArchiveURL, a.SourceArchiveSHA256, a.SourceArchiveBytes = "", "", 0 }},
		{"comfyui-app", func(a *Artifact) {
			a.SourceArchiveURL = "https://github.com/openabstractions/other/releases/download/v9/comfyui-app-source.zip"
		}},
		{"lemonade-engine", func(a *Artifact) { a.TestedBackendSHA256, a.TestedModelRevision = "", "" }},
		{"lemonade-engine", func(a *Artifact) { a.TestedModelRevision = strings.Repeat("1", 40) }},
		{"lemonade-engine", func(a *Artifact) { a.TestedBackendSHA256 = strings.Repeat("2", 64) }},
	} {
		c := fixtureCatalog()
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		for i := range c.Components {
			if c.Components[i].ID == test.component {
				test.change(c.Components[i].Artifact)
			}
		}
		if err := c.Validate(); err == nil {
			t.Fatalf("%s accepted without its paired or tested pins: %+v", test.component, c.Components)
		}
	}
}

func TestInstallPinRecordCarriesEveryEarlierPin(t *testing.T) {
	c := catalogForTarget("windows", "amd64")
	pins, err := parseInstallPins(installPinsJSON)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range c.Components {
		if component.Artifact == nil {
			continue
		}
		found := false
		for _, pin := range pins {
			found = found || pin.ComponentID == component.ID && pin.Artifact == *component.Artifact
		}
		if !found {
			t.Fatalf("current %s pin is not in install_pins.json; append it so the next change keeps it recognizable", component.ID)
		}
	}
	cpuArchive := theNoiseArtifact()
	if cpuArchive.SHA256 != thenoise.EngineZIPSHA256 || cpuArchive.Bytes != thenoise.EngineZIPBytes {
		t.Fatalf("legacy CPU archive no longer matches the provider pin: %+v", cpuArchive)
	}
	if !slices.Contains(pins, KnownInstallPin{ComponentID: "thenoise-engine", Artifact: cpuArchive}) {
		t.Fatal("the legacy CPU archive no longer matches the retained installation pin")
	}
	for _, pin := range pins {
		if pin.Artifact.Platform == "windows/amd64" && !c.knownInstallArtifact(pin.ComponentID, pin.Artifact) {
			t.Fatalf("recorded %s pin %s is not recognized by the release catalog", pin.ComponentID, pin.Artifact.Version)
		}
	}
	if len(c.PreviousInstallPins) == 0 {
		t.Fatal("the Lemonade pin from before its tested pairing was recorded is not carried")
	}
	reused := strings.Replace(string(installPinsJSON), `"sha256": "235c`, `"sha256": "335c`, 1)
	if _, err := parseInstallPins([]byte(reused)); err == nil {
		t.Fatal("a recorded version reused for different bytes was accepted")
	}
}

func TestPlanHasClosedChoicesDeduplicatedDependenciesAndConfiguration(t *testing.T) {
	c := unpinnedAppsCatalog()
	plan, err := c.Plan([]AppID{Lemonade, ComfyUI, ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Selected) != 2 || len(plan.Components) != 2 || plan.Preparable || plan.Ready {
		t.Fatalf("unexpected incomplete plan: %+v", plan)
	}
	if plan.Components[0].ID != "comfyui-app" || plan.Components[1].ID != "lemonade-engine" ||
		len(plan.Blockers) != 1 || !strings.HasPrefix(plan.Blockers[0], "comfyui-app:") {
		t.Fatalf("ComfyUI app bundle must be one physical download with the image engine: %+v", plan)
	}
	if len(plan.Configuration) != 1 || plan.Configuration[0].ID != "lemonade-model" {
		t.Fatalf("image configuration omitted: %+v", plan.Configuration)
	}
	if _, err := c.Plan([]AppID{"browser-url"}); err == nil {
		t.Fatal("arbitrary browser selection was accepted")
	}
	other, err := c.Plan([]AppID{ComfyUI, Lemonade})
	if err != nil || other.ID != plan.ID {
		t.Fatalf("selection order changed plan identity: %+v, %v", other, err)
	}
}

func TestReviewedLemonadeIsOnePhysicalPackageAndModelStillPending(t *testing.T) {
	c := unpinnedAppsCatalog()
	lemonade := mustPlan(t, c, Lemonade)
	if len(lemonade.Components) != 1 || !lemonade.Preparable || lemonade.Ready ||
		len(lemonade.Configuration) != 1 || lemonade.Configuration[0].ID != "lemonade-model" {
		t.Fatalf("Lemonade package or model review state: %+v", lemonade)
	}
	if c.Choices[2].Title != "Lemonade (OA build)" || c.Choices[2].Name() != "Lemonade" ||
		lemonade.Components[0].Title != "Lemonade (OA build)" ||
		!strings.Contains(lemonade.Configuration[0].Title, "image model") {
		t.Fatalf("Lemonade choice must name local image setup: %+v", lemonade)
	}
	a := lemonade.Components[0].Artifact
	if a == nil || a.SHA256 != "235c2361be3a9729a06b92c97e40a9530ebb8e5088bd3334524472d6da7fbbca" ||
		a.SourceCommit != "6ce82052b69c5ffd9157ed102b739d180cdc24a5" || a.Signature != "unsigned" ||
		a.Platform != "windows/amd64" || a.SourceURL != "https://github.com/lemonade-sdk/lemonade/commit/"+a.SourceCommit {
		t.Fatalf("Lemonade release pin changed: %+v", a)
	}
	// OpenCode installs through its official installer; the component is the
	// Get started row's seam and stays pending until Setup runs that route.
	opencode := mustPlan(t, c, OpenCode)
	if len(opencode.Components) != 1 || opencode.Components[0].ID != OpenCodeOfficialComponent || opencode.Preparable {
		t.Fatalf("the OpenCode goal is not the pending official-installer seam: %+v", opencode)
	}
}

func TestLemonadePinUnavailableOnUnqualifiedTargets(t *testing.T) {
	windows := mustPlan(t, catalogForTarget("windows", "amd64"), Lemonade)
	for _, target := range [][2]string{{"windows", "arm64"}, {"linux", "amd64"}, {"darwin", "arm64"}} {
		catalog := catalogForTarget(target[0], target[1])
		plan := mustPlan(t, catalog, Lemonade)
		if plan.Preparable || len(plan.Blockers) != 1 || plan.Components[0].Artifact != nil {
			t.Fatalf("unqualified target %v was preparable: %+v", target, plan)
		}
		if err := (Journal{catalogOverride: &catalog}).Authorize(windows); err == nil {
			t.Fatalf("Windows artifact plan accepted on unqualified target %v", target)
		}
	}
}

func TestCatalogRefusesUnpinnedOrUntrustedArtifacts(t *testing.T) {
	c := fixtureCatalog()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Components[0].Artifact.URL = "https://evil.example/install.exe"
	if err := c.Validate(); err == nil {
		t.Fatal("untrusted URL accepted")
	}
	c = fixtureCatalog()
	c.Components[0].Artifact.SHA256 = ""
	if err := c.Validate(); err == nil {
		t.Fatal("missing digest accepted")
	}
	c = fixtureCatalog()
	c.Components[0].Artifact.SourceURL = "https://github.com/openabstractions/setup-fixture/commit/" + strings.Repeat("c", 40)
	if err := c.Validate(); err == nil {
		t.Fatal("source URL with a different revision accepted")
	}
	c = fixtureCatalog()
	c.Components[0].Artifact.Action = "cmd /c anything"
	if err := c.Validate(); err == nil {
		t.Fatal("arbitrary installer command accepted")
	}
}

func TestJournalResumesExactClaimAndNeverRetriesUnknownInstall(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	plan, err := c.Plan([]AppID{Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	state, err := j.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	old := state.Effects["lemonade-engine"]
	work := &WorkIdentity{Key: "setup-123", HistoryEpoch: "epoch", Binding: client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}, OperationsBinding: client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}}
	state, err = j.Update(ctx, plan.ID, "lemonade-engine", old, Effect{Phase: Requested, Work: work})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Update(ctx, plan.ID, "lemonade-engine", old, Effect{Phase: Requested, Work: work}); err == nil {
		t.Fatal("stale claim was accepted")
	}
	requested := state.Effects["lemonade-engine"]
	state, err = j.Update(ctx, plan.ID, "lemonade-engine", requested, Effect{Phase: Verified, StagedPath: "pinned.zip"})
	if err != nil {
		t.Fatal(err)
	}
	verified := state.Effects["lemonade-engine"]
	state, err = j.Update(ctx, plan.ID, "lemonade-engine", verified, Effect{Phase: Installing})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Update(ctx, plan.ID, "lemonade-engine", verified, Effect{Phase: Installing}); err == nil {
		t.Fatal("second installer launch claim was accepted")
	}
	reopened, err := j.Load(ctx)
	if err != nil || reopened.Effects["lemonade-engine"].Phase != Installing || reopened.Ready() {
		t.Fatalf("interrupted install became ready or lost claim: %+v, %v", reopened, err)
	}
	if _, err := j.SavePlan(ctx, mustPlan(t, c, fixtureApp)); err == nil {
		t.Fatal("uncertain external installer was discarded by plan replacement")
	}
	state, err = j.Update(ctx, plan.ID, "lemonade-engine", reopened.Effects["lemonade-engine"], Effect{Phase: Uncertain, Reason: "installer process disappeared"})
	if err != nil || state.Effects["lemonade-engine"].Phase != Uncertain {
		t.Fatalf("unknown result lost: %+v, %v", state, err)
	}
	if _, err := j.Update(ctx, plan.ID, "lemonade-engine", state.Effects["lemonade-engine"], Effect{Phase: Installing}); err == nil {
		t.Fatal("unknown installer was silently relaunched")
	}
}

func TestJournalRetainsActiveDownloadAndRejectsForgedPlan(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	plan := mustPlan(t, c, Lemonade)
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	state, err := j.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	forged := plan
	forged.Components = append([]PlannedComponent(nil), plan.Components...)
	artifact := *forged.Components[0].Artifact
	artifact.URL = "https://github.com/attacker/x/releases/download/v1/payload.zip"
	forged.Components[0].Artifact = &artifact
	if _, err := j.SavePlan(ctx, forged); err == nil {
		t.Fatal("forged plan accepted")
	}
	work := &WorkIdentity{Key: "setup-123", HistoryEpoch: "epoch", Binding: client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}, OperationsBinding: client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"}}
	state, err = j.Update(ctx, plan.ID, "lemonade-engine", state.Effects["lemonade-engine"], Effect{Phase: Requested, Work: work})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.SavePlan(ctx, mustPlan(t, c, fixtureApp)); err == nil {
		t.Fatal("active public download identity was dropped")
	}
	if state.Effects["lemonade-engine"].Work.Key != "setup-123" {
		t.Fatal("public job identity lost")
	}
}

func TestJournalUpdateCannotSetComponentRootOwnership(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	plan := mustPlan(t, c, Lemonade)
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	state, err := j.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	componentID := plan.Components[0].ID
	current := state.Effects[componentID]
	next := Effect{Phase: Requested, ComponentRootKey: "forged-root", Work: &WorkIdentity{
		Key:               "setup-123",
		HistoryEpoch:      "epoch",
		Binding:           client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"},
		OperationsBinding: client.JobsBinding{Endpoint: "pipe", LogicalOwner: "owner"},
	}}
	if _, err := j.Update(ctx, plan.ID, componentID, current, next); err == nil {
		t.Fatal("a normal effect transition assigned root ownership")
	}
	reopened, err := j.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Effects[componentID]; got.ComponentRootKey != "" || got.Revision != current.Revision || got.Phase != current.Phase {
		t.Fatalf("rejected transition changed persisted effect: %+v", got)
	}
}

func TestInstalledAndConfiguredAreSeparate(t *testing.T) {
	ctx := context.Background()
	c := fixtureCatalog()
	plan := mustPlanOf(t, c, ComfyUI, Lemonade)
	if len(plan.Configuration) == 0 {
		t.Fatal("the picture plan lists no configuration step")
	}
	j := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &c}
	state, err := j.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range plan.Components {
		state, err = j.Update(ctx, plan.ID, component.ID, state.Effects[component.ID], Effect{Phase: Installed}) // installed identity observed externally
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.Ready() {
		t.Fatal("installed ComfyUI without its image policy was marked ready")
	}
	for _, row := range plan.Configuration {
		if state, err = j.ConfirmConfiguration(ctx, plan.ID, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	if !state.Ready() {
		t.Fatalf("confirmed setup not ready: %+v", state)
	}
	same, err := j.SavePlan(ctx, plan)
	if err != nil || !same.Ready() {
		t.Fatalf("same exact plan lost configuration: %+v, %v", same, err)
	}
	newCatalog := fixtureCatalog()
	for i := range newCatalog.Components {
		if newCatalog.Components[i].ID == "comfyui-app" {
			newCatalog.Components[i].Artifact.Version = "1.2.4"
			newCatalog.Components[i].Artifact.SHA256 = strings.Repeat("c", 64)
		}
	}
	newPlan := mustPlanOf(t, newCatalog, ComfyUI, Lemonade)
	j.catalogOverride = &newCatalog
	changed, err := j.SavePlan(ctx, newPlan)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Configuration["comfyui-image-policy"] || changed.Ready() {
		t.Fatalf("new app version inherited old per-executable configuration: %+v", changed)
	}
}

func mustPlan(t *testing.T, catalog Catalog, choice AppID) Plan {
	t.Helper()
	plan, err := catalog.Plan([]AppID{choice})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// A picture app's engine is its required companion (architect ruling
// 2026-11-08): a ComfyUI tick plans the engine that fits this computer,
// PyTorch on gfx1151 and Lemonade elsewhere, with no engine choice asked
// for and no engine blocker. A chosen engine stays the person's.
func TestComfyIncludesTheEngineThatFitsThisComputer(t *testing.T) {
	c := catalogForTarget("windows", "amd64")
	old := machineGraphics
	t.Cleanup(func() { machineGraphics = old })
	for _, test := range []struct {
		hardware []string
		engine   AppID
	}{
		{[]string{`PCI\VEN_1002&DEV_1586&SUBSYS_38291043&REV_C1`}, PyTorch},
		{[]string{`PCI\VEN_10DE&DEV_2684&SUBSYS_0`}, Lemonade},
	} {
		machineGraphics = func() ([]string, error) { return test.hardware, nil }
		comfy := mustPlan(t, c, ComfyUI)
		if !slices.Equal(comfy.Selected, []AppID{ComfyUI, test.engine}) || c.PictureEngine(ComfyUI) != test.engine {
			t.Fatalf("%v: ComfyUI plans %v, engine %q", test.hardware, comfy.Selected, c.PictureEngine(ComfyUI))
		}
		blockers := strings.Join(comfy.Blockers, " ")
		if strings.Contains(blockers, "picture engine") {
			t.Fatalf("%v: ComfyUI still asks for an engine: %v", test.hardware, comfy.Blockers)
		}
		if strings.Contains(blockers, "graphics") {
			t.Fatalf("%v: graphics blocker %v", test.hardware, comfy.Blockers)
		}
		auto, err := c.Plan([]AppID{ComfyUI, test.engine})
		if err != nil || auto.ID != comfy.ID {
			t.Fatalf("%v: the included engine is not the same plan as choosing it: %v", test.hardware, err)
		}
		chosen, err := c.Plan([]AppID{ComfyUI, TheNoise})
		if err != nil || !slices.Equal(chosen.Selected, []AppID{ComfyUI, TheNoise}) {
			t.Fatalf("%v: a chosen engine is replaced: %v %v", test.hardware, chosen.Selected, err)
		}
	}
}

// AddedSizes is the plan's own pins without what is installed: equal to the
// plan's sizes on a fresh computer, nothing for a package installed at its
// pin, and the whole package for another installed version.
func TestAddedSizesAreThePlanPinsNotInstalled(t *testing.T) {
	c := withTheNoisePin(catalogForTarget("windows", "amd64"))
	plan := mustPlan(t, c, TheNoise)
	if plan.DownloadBytes == 0 || plan.PeakBytes == 0 {
		t.Fatalf("TheNoise plan has no sizes: %+v", plan)
	}
	download, peak, err := c.AddedSizes(plan, nil)
	if err != nil || download != plan.DownloadBytes || peak != plan.PeakBytes {
		t.Fatalf("fresh sizes %d/%d, plan %d/%d: %v", download, peak, plan.DownloadBytes, plan.PeakBytes, err)
	}
	pin := *plan.Components[0].Artifact
	if download, peak, err = c.AddedSizes(plan, map[string]Artifact{"thenoise-engine": pin}); err != nil || download != 0 || peak != 0 {
		t.Fatalf("an installed pin still counts: %d/%d %v", download, peak, err)
	}
	older := pin
	older.Version = "0.0.1"
	if download, _, err = c.AddedSizes(plan, map[string]Artifact{"thenoise-engine": older}); err != nil || download != plan.DownloadBytes {
		t.Fatalf("another installed version does not count the new pin: %d %v", download, err)
	}
}

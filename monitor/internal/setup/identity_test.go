package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
)

// A display string never feeds an identity (architect ruling 2026-11-09):
// the plan and model-plan IDs of a gfx1151 catalogue are those of
// 4d40a3a4f, so an owner's prepared Klein copies, journals and saved plans
// keep their identity when a person-facing word changes.
func TestPlanIdentitiesSurviveWordingChanges(t *testing.T) {
	old := machineGraphics
	t.Cleanup(func() { machineGraphics = old })
	machineGraphics = func() ([]string, error) { return []string{`PCI\VEN_1002&DEV_1586&SUBSYS_38291043&REV_C1`}, nil }
	// A package pin is content a plan binds: offering Lemonade's package is
	// the one change since 4d40a3a4f, so withdrawn it leaves every identity
	// as it was (TestAnEarlierPlanKeepsItsPackagesWhenLemonadeIsOffered).
	c := withTheNoisePin(withoutLemonadePin(catalogForTarget("windows", "amd64")))
	got := map[string]string{}
	for _, selected := range [][]AppID{{ComfyUI}, {PyTorch}, {TheNoise}, {ComfyUI, PyTorch, TheNoise}, {ComfyUI, TheNoise}} {
		plan, err := c.Plan(selected)
		if err != nil {
			t.Fatal(err)
		}
		got[fmt.Sprint(selected)] = plan.ID
		// A saved plan stays authorized (Journal.Authorize compares the whole
		// plan): its every field, words included, is what it was.
		raw, _ := json.Marshal(plan)
		sum := sha256.Sum256(raw)
		got["saved "+fmt.Sprint(selected)] = hex.EncodeToString(sum[:])
	}
	for _, offer := range PictureModelOffers(PyTorch) {
		got[offer.Plan.Title] = offer.Plan.ID
	}
	got["coding"] = CodingModelPlan().ID
	got["lemonade starter"] = ApprovedBF16Plan().ID
	got["thenoise starter"] = TheNoiseModelPlan().ID
	for key, id := range planIdentities {
		if got[key] != id {
			t.Errorf("%s: identity %s, was %s", key, got[key], id)
		}
	}
	if len(got) != len(planIdentities) {
		t.Errorf("identities checked %v", got)
	}
}

// withoutLemonadePin is c with the Lemonade engine package back at its
// Pending literal, as the catalogue was before it offered the package.
func withoutLemonadePin(c Catalog) Catalog {
	c.Components = slices.Clone(c.Components)
	for i := range c.Components {
		if c.Components[i].ID == "lemonade-engine" {
			c.Components[i].Artifact, c.Components[i].Pending = nil, "Not available in this release yet"
		}
	}
	return c
}

// withTheNoisePin is c with TheNoise's reviewed Windows x64 archive pinned,
// as the catalogue shipped it up to 2026-11-11. TheNoise is no longer
// shipped; its bridge, removal and identity tests keep that catalogue.
func withTheNoisePin(c Catalog) Catalog {
	c.Components = slices.Clone(c.Components)
	for i := range c.Components {
		if c.Components[i].ID == "thenoise-engine" {
			pin := theNoiseArtifact()
			c.Components[i].Artifact, c.Components[i].Pending = &pin, ""
		}
	}
	return c
}

// Offering Lemonade's package gives every Setup plan a new catalogue digest,
// as any new package pin does. A plan approved and installed before it stays
// the journal's plan: its packages read installed and the same choices plan
// nothing new to download.
func TestAnEarlierPlanKeepsItsPackagesWhenLemonadeIsOffered(t *testing.T) {
	ctx := context.Background()
	current := withTheNoisePin(catalogForTarget("windows", "amd64"))
	earlier := withoutLemonadePin(current)
	plan := mustPlan(t, earlier, TheNoise)
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json"), catalogOverride: &earlier}
	state, err := journal.SavePlan(ctx, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Update(ctx, plan.ID, "thenoise-engine", state.Effects["thenoise-engine"], Effect{Phase: Installed}); err != nil {
		t.Fatal(err)
	}
	journal.catalogOverride = &current
	installed, err := journal.InstalledVersions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if a, ok := installed["thenoise-engine"]; !ok || a != theNoiseArtifact() {
		t.Fatalf("installed under the new catalogue: %+v", installed)
	}
	again := mustPlan(t, current, TheNoise)
	if again.ID == plan.ID {
		t.Fatal("the Lemonade pin left the catalogue digest unchanged")
	}
	if download, peak, err := current.AddedSizes(again, installed); err != nil || download != 0 || peak != 0 {
		t.Fatalf("re-planning installed TheNoise adds %d bytes, %d on disk (%v)", download, peak, err)
	}
}

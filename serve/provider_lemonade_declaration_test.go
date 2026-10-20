package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// The installed Lemonade image declaration names lemonade/seed for the image
// profile, and the runtime reads it as a valid provider declaration that
// carries the key into its host's extension inventory. The declaration ships
// in redist (installer/declarations), so both tests here are private-workspace
// checks across the two repositories.
func TestInstalledLemonadeDeclarationCarriesTheSeedExtension(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "installer", "declarations", "lemonade-image.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f providerFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Version != providerFileVersion {
		t.Fatalf("declaration %v, version %d", err, f.Version)
	}
	f.Declaration.Program = filepath.Join(t.TempDir(), "lemonaded.exe")
	if field := validProviderDeclaration(f.Declaration); field != "" {
		t.Fatalf("invalid %s", field)
	}
	if len(f.Declaration.SupportedExtensions) != 1 || f.Declaration.SupportedExtensions[0] != (providerExtension{Profile: "image", Key: "lemonade/seed"}) {
		t.Fatalf("supported extensions %+v", f.Declaration.SupportedExtensions)
	}
}

// The installed Lemonade image declaration names card:0, the card lemonaded
// leases before Lemonade starts its engines, so the runtime's one card-rules
// writer gives lemonaded hold on card:0 and on awake, the rules every card
// provider gets (FAC-R3, research/architecture-review-2026-10-02.md D1).
func TestInstalledLemonadeDeclarationGetsTheCardRules(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "installer", "declarations", "lemonade-image.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f providerFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if cards := f.Declaration.resources("card"); len(cards) != 1 || cards[0] != "0" {
		t.Fatalf("card resources %q", cards)
	}
	r, p, o := cardRulesFixture(t)
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "lemonaded.exe")
	f.Declaration.Program = program
	f.DeclaredBy, f.DeclaredAt = r.operators[0], time.Now().UnixMilli()
	if change := p.add(revision, f); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add %+v", change)
	}
	writeCardHoldRules(o, p)()
	requireCardRules(t, r, filepath.Clean(program), "the sweep")
	// Nothing beyond hold on card:0 and on awake is written for the program.
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var written []string
	for _, rule := range snapshot.Rules {
		if rule.Subject.Program == filepath.Clean(program) {
			written = append(written, fmt.Sprintf("%s %s %t", rule.Action, rule.Resource, rule.Permit))
		}
	}
	sort.Strings(written)
	want := []string{resourceservice.ActionHold + " " + resourceservice.ResourceAwake + " true", resourceservice.ActionHold + " card:0 true"}
	sort.Strings(want)
	if !slices.Equal(written, want) {
		t.Fatalf("rules written for lemonaded: %q, want %q", written, want)
	}
}

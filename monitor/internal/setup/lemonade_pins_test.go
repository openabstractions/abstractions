package setup

import (
	"fmt"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-provider-lemonade/go/server"
)

// lemonadePinsMatch is the release gate of DECISION 2026-10-01 point 6: one
// MSI carries the provider, the Panel and the catalog, so every
// lemonade-engine pin of install_pins.json for windows/amd64 needs a provider
// table entry with the same archive and, where the pin records them, the same
// tested backend archive and model revision; the catalog's current pin is
// among the recorded pins.
func lemonadePinsMatch(pins []KnownInstallPin, current Artifact, table []server.Package) error {
	known := map[string]server.Package{}
	for _, p := range table {
		known[p.Version] = p
	}
	check := func(a Artifact) error {
		p, ok := known[a.Version]
		switch {
		case !ok:
			return fmt.Errorf("the Lemonade provider has no entry for pin %s", a.Version)
		case !strings.EqualFold(p.ArchiveSHA256, a.SHA256):
			return fmt.Errorf("the provider's %s entry names another archive than the pin", a.Version)
		case a.TestedBackendSHA256 != "" && !strings.EqualFold(p.BackendArchiveSHA256, a.TestedBackendSHA256):
			return fmt.Errorf("the provider pairs %s with another backend archive than the pin", a.Version)
		case a.TestedModelRevision != "" && p.ModelRevision != a.TestedModelRevision:
			return fmt.Errorf("the provider pairs %s with another model revision than the pin", a.Version)
		}
		return nil
	}
	recorded := false
	pinned := map[string]bool{}
	for _, pin := range pins {
		if pin.ComponentID != "lemonade-engine" || pin.Artifact.Platform != "windows/amd64" {
			continue
		}
		if err := check(pin.Artifact); err != nil {
			return err
		}
		recorded = recorded || pin.Artifact == current
		pinned[pin.Artifact.Version] = true
	}
	if !recorded {
		return fmt.Errorf("install_pins.json does not record the catalog's current Lemonade pin %s", current.Version)
	}
	// The table holds no version Setup cannot install.
	for _, p := range table {
		if !pinned[p.Version] {
			return fmt.Errorf("the provider's %s entry has no windows/amd64 install pin", p.Version)
		}
	}
	return check(current)
}

func currentLemonadePin(t *testing.T) Artifact {
	t.Helper()
	for _, component := range catalogForTarget("windows", "amd64").Components {
		if component.ID == "lemonade-engine" && component.Artifact != nil {
			return *component.Artifact
		}
	}
	t.Fatal("the windows/amd64 catalog has no Lemonade pin")
	return Artifact{}
}

func TestEveryLemonadePinHasAProviderEntry(t *testing.T) {
	pins, err := parseInstallPins(installPinsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.ValidTable(server.Packages); err != nil {
		t.Fatal(err)
	}
	if err := lemonadePinsMatch(pins, currentLemonadePin(t), server.Packages); err != nil {
		t.Fatal(err)
	}
}

// The gate refuses a table entry whose pairing differs from a pin, a pin the
// table lacks, and a current pin install_pins.json does not record.
func TestLemonadePinGateRefusesAMismatchedTable(t *testing.T) {
	pins, err := parseInstallPins(installPinsJSON)
	if err != nil {
		t.Fatal(err)
	}
	current := currentLemonadePin(t)
	for name, mutate := range map[string]func(*server.Package){
		"another model revision": func(p *server.Package) { p.ModelRevision = strings.Repeat("1", 40) },
		"another backend archive": func(p *server.Package) {
			p.BackendArchiveSHA256 = strings.Repeat("2", 64)
		},
		"another archive":  func(p *server.Package) { p.ArchiveSHA256 = strings.Repeat("3", 64) },
		"no entry for pin": func(p *server.Package) { p.Version = "v1999.1.1" },
	} {
		table := append([]server.Package(nil), server.Packages...)
		for i := range table {
			if table[i].Version == current.Version {
				mutate(&table[i])
			}
		}
		if err := lemonadePinsMatch(pins, current, table); err == nil {
			t.Fatalf("%s passed the gate", name)
		}
	}
	unrecorded := current
	unrecorded.Version, unrecorded.SHA256 = "v2026.40.0", strings.Repeat("4", 64)
	if err := lemonadePinsMatch(pins, unrecorded, server.Packages); err == nil {
		t.Fatal("a current pin install_pins.json does not record passed the gate")
	}
	extra := server.Packages[0]
	extra.ID, extra.Version = "lemonade-embeddable-v2026.40.0-windows-amd64", "v2026.40.0"
	if err := lemonadePinsMatch(pins, current, append(append([]server.Package(nil), server.Packages...), extra)); err == nil {
		t.Fatal("a table entry no install pin records passed the gate")
	}
}

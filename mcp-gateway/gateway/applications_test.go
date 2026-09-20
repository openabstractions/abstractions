package gateway

import (
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func TestApplicationsOutputIsCuratedAndBounded(t *testing.T) {
	page := wire.ApplicationPage{Outcome: wire.ApplicationOutcomePage, Applications: []wire.ApplicationEntry{{
		Descriptor: wire.ApplicationDescriptor{
			Name: "editor", Title: "Editor", Program: `C:\secret\editor.exe`, StartGuidance: "secret guidance",
			Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"--secret"}},
		},
		Scope: wire.ScopeLocal,
		Instances: []wire.ApplicationInstance{{
			Instance: "instance-1", ExpiresUnixMs: 123,
			Interfaces: []wire.ApplicationInterface{{Name: "documents", Protocol: "fixture", Contract: "fixture/documents@1"}},
			Contexts:   []wire.ApplicationContext{{Name: "document-1", Title: "Document", Revision: "r7"}},
		}},
	}}}
	out, err := applicationsOutput(page)
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Join([]string{out.Applications[0].Name, out.Applications[0].Title, out.Applications[0].Instances[0].Instance}, " ")
	if strings.Contains(raw, "secret") || out.Applications[0].Instances[0].Contexts[0].Revision != "r7" {
		t.Fatalf("directory projection leaked authority or lost context: %+v", out)
	}

	page.Applications = make([]wire.ApplicationEntry, 64)
	for appIndex := range page.Applications {
		entry := wire.ApplicationEntry{Descriptor: wire.ApplicationDescriptor{Name: strings.Repeat("a", 64), Title: strings.Repeat("t", 256)}, Scope: wire.ScopeLocal, Instances: make([]wire.ApplicationInstance, 2)}
		for instanceIndex := range entry.Instances {
			entry.Instances[instanceIndex] = wire.ApplicationInstance{Instance: strings.Repeat("i", 64), Contexts: make([]wire.ApplicationContext, 32)}
			for contextIndex := range entry.Instances[instanceIndex].Contexts {
				entry.Instances[instanceIndex].Contexts[contextIndex] = wire.ApplicationContext{Name: strings.Repeat("n", 64), Title: strings.Repeat("t", 256), Revision: strings.Repeat("r", 256)}
			}
		}
		page.Applications[appIndex] = entry
	}
	if _, err := applicationsOutput(page); err == nil || !strings.Contains(err.Error(), "262144-byte bound") {
		t.Fatalf("oversized directory result = %v", err)
	}
}

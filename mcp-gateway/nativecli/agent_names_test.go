package nativecli

import (
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	client "github.com/openabstractions/abstraction-facade/go/client"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionedAgentApplicationNamesRemainBoundedRouting(t *testing.T) {
	for _, base := range []string{"oa-claude-integration", "oa-codex-integration", "oa-opencode-integration"} {
		leaf := base + "-" + strings.Repeat("a", 64)
		name := agentApplicationName(leaf, "darwin")
		if name != base+"-"+strings.Repeat("a", 32) || len(name) > 64 || agentApplicationBase(name, "darwin") != base {
			t.Fatal(name)
		}
		if err := client.ValidApplicationDescriptor(wire.ApplicationDescriptor{Name: name, Program: filepath.Join(t.TempDir(), leaf), Title: "Kept integration"}); err != nil {
			t.Fatal(err)
		}
		if agentApplicationName(leaf, "windows") != "" || agentApplicationBase(name, "windows") != "" {
			t.Fatal("Windows version namespace expanded")
		}
		for _, bad := range []string{leaf + "x", strings.ToUpper(leaf), base + "-" + strings.Repeat("a", 63), "foreign-" + leaf} {
			if agentApplicationName(bad, "darwin") != "" {
				t.Fatal(bad)
			}
		}
		for _, bad := range []string{name + "x", strings.ToUpper(name), base + "-" + strings.Repeat("a", 31), "foreign-" + name} {
			if agentApplicationBase(bad, "darwin") != "" {
				t.Fatal(bad)
			}
		}
	}
}

// One list names every connector: its routing labels and its title agree,
// so a Darwin versioned label titles as its base does.
func TestAgentTitleNamesEveryConnectorBase(t *testing.T) {
	want := map[string]string{"oa-claude-integration": "Claude", "oa-codex-integration": "Codex", "oa-opencode-integration": "OpenCode"}
	bases := AgentApplicationBases()
	if len(bases) != len(want) {
		t.Fatalf("bases = %v", bases)
	}
	for _, base := range bases {
		if title, ok := AgentTitle(base); !ok || title != want[base] {
			t.Fatalf("AgentTitle(%s) = %q, %t", base, title, ok)
		}
		versioned := base + "-" + strings.Repeat("b", 32)
		if title, ok := AgentTitle(agentApplicationBase(versioned, "darwin")); !ok || title != want[base] {
			t.Fatalf("AgentTitle of %s = %q, %t", versioned, title, ok)
		}
	}
	for _, other := range []string{"", "oa-claude", "app:oa-claude-integration", "Claude"} {
		if _, ok := AgentTitle(other); ok {
			t.Fatalf("AgentTitle(%q) titled a non-connector", other)
		}
	}
}

func TestAgentApplicationForPartyUsesOnlyTheConnectorRoster(t *testing.T) {
	for party, want := range map[string]string{
		"claude":                  "oa-claude-integration",
		"codex":                   "oa-codex-integration",
		"opencode":                "oa-opencode-integration",
		"":                        "",
		"OpenCode":                "",
		"oa-opencode-integration": "",
		"other":                   "",
	} {
		if got := AgentApplicationForParty(party); got != want {
			t.Errorf("party %q: got %q, want %q", party, got, want)
		}
	}
}

func TestMovedAgentImageNamesOnlySetupsMovedCopies(t *testing.T) {
	folder := `C:\Users\p\AppData\Roaming\OpenAbstractions\setup\agent-integrations\`
	hex := strings.Repeat("ab", 32)
	for _, c := range []struct {
		program, title string
		moved          bool
	}{
		{folder + "oa-claude-integration.exe." + hex + ".tmp.previous", "Claude", true},
		{folder + "OA-Codex-Integration.EXE." + hex + ".TMP.previous.previous", "Codex", true},
		{folder + "oa-opencode-integration.exe." + hex + ".tmp.previous", "OpenCode", true},
		{folder + ".oa-helper-old-123456", "", true},
		{folder + "oa-claude-integration.exe", "", false},
		{folder + "oa-claude-integration.exe." + hex + ".tmp", "", false},
		{folder + "oa-claude-integration.exe.xyz.tmp.previous", "", false},
		{folder + ".oa-helper-old-12a", "", false},
		{`C:\Elsewhere\oa-claude-integration.exe.` + hex + ".tmp.previous", "", false},
		{"/Users/p/Library/agent-integrations/.oa-helper-old-7", "", true},
	} {
		title, moved := MovedAgentImage(c.program)
		if title != c.title || moved != c.moved {
			t.Errorf("%s: %q %v", c.program, title, moved)
		}
	}
	if got := MovedAgentImageSentence("OpenCode"); got != "Restart OpenCode to use the updated connector." {
		t.Fatal(got)
	}
}

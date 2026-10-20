package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// A Codex connection's recipe is approved like Claude's: its own kind on its
// own registration, the saved command rechecked without running it.
func TestCodexMessageStartProfileApprovesOwnKindOnly(t *testing.T) {
	d, p, key := claudeMessageProfileFixture(t)
	d.Name = "oa-codex-integration"
	p.Name = "codex-v1"
	d.Activation.Arguments = p.Arguments()
	holds, closes := 0, 0
	hold := func(context.Context, []string) (messageStartCLIIdentity, error) {
		holds++
		return &messageStartIdentityFixture{key: key, closes: &closes}, nil
	}
	if err := validateCodexMessageStartProfile(t.Context(), d, p, hold); err != nil || holds != 1 || closes != 1 {
		t.Fatal("codex recipe", err, holds, closes)
	}
	if err := validateClaudeMessageStartProfile(t.Context(), d, p, hold); err == nil {
		t.Fatal("Claude approval accepted a Codex registration")
	}
	claude := d
	claude.Name = "oa-claude-integration"
	if err := validateCodexMessageStartProfile(t.Context(), claude, p, hold); err == nil {
		t.Fatal("Codex approval accepted a Claude registration")
	}
	other := p
	other.Name = "claude-v1"
	mismatch := d
	activation := *d.Activation
	activation.Arguments = other.Arguments()
	mismatch.Activation = &activation
	if err := validateCodexMessageStartProfile(t.Context(), mismatch, other, hold); err == nil {
		t.Fatal("Codex approval accepted a claude-v1 recipe")
	}
	changed := &messageStartIdentityFixture{key: "other-key", closes: &closes}
	if err := validateCodexMessageStartProfile(t.Context(), d, p, func(context.Context, []string) (messageStartCLIIdentity, error) { return changed, nil }); err == nil {
		t.Fatal("changed saved command accepted")
	}
	if holds != 1 {
		t.Fatal("refused registration reached the native hold", holds)
	}
	if err := approveMessageStartProfile(t.Context(), wire.ApplicationDescriptor{}, startprofile.Profile{Name: "unknown-v1"}); err == nil {
		t.Fatal("unknown recipe kind approved")
	}
}

type messageStartLocatedFixture struct {
	messageStartIdentityFixture
	program string
}

func (f *messageStartLocatedFixture) Program() string { return f.program }

// Codex Desktop's update removes the saved package folder. The recipe stays
// approved when the hold followed the saved locator to another version of the
// same installation and authenticated it; any other image stays refused.
func TestCodexMessageStartProfileFollowsDesktopUpdate(t *testing.T) {
	d, p, _ := claudeMessageProfileFixture(t)
	d.Name = "oa-codex-integration"
	root := t.TempDir()
	saved := filepath.Join(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0", "app", "resources", "codex.exe")
	current := filepath.Join(root, "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0", "app", "resources", "codex.exe")
	if err := os.MkdirAll(filepath.Dir(saved), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(saved, []byte("never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, saved, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := id.Stored()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "OpenAI.Codex_26.1027.3384.0_x64__2p2nqsd0c76g0")); err != nil {
		t.Fatal(err)
	}
	p.Name, p.CLIFileID = "codex-v1", stored.String()
	activation := *d.Activation
	activation.Arguments = p.Arguments()
	d.Activation = &activation
	closes := 0
	held := func(identity messageStartCLIIdentity) func(context.Context, []string) (messageStartCLIIdentity, error) {
		return func(context.Context, []string) (messageStartCLIIdentity, error) { return identity, nil }
	}
	followed := &messageStartLocatedFixture{messageStartIdentityFixture{key: "new-version-key", closes: &closes}, current}
	if err := validateCodexMessageStartProfile(t.Context(), d, p, held(followed)); err != nil {
		t.Fatal("a recipe saved before a Codex Desktop update was refused", err)
	}
	for name, identity := range map[string]messageStartCLIIdentity{
		"other install": &messageStartLocatedFixture{messageStartIdentityFixture{key: "k", closes: &closes}, filepath.Join(root, "npm", "codex.exe")},
		"no location":   &messageStartIdentityFixture{key: "k", closes: &closes},
		"unverified":    &messageStartLocatedFixture{messageStartIdentityFixture{key: "k", closes: &closes, changed: true}, current},
	} {
		if err := validateCodexMessageStartProfile(t.Context(), d, p, held(identity)); err == nil {
			t.Fatal("followed image accepted:", name)
		}
	}
	if closes != 4 {
		t.Fatal("every hold closes", closes)
	}
}

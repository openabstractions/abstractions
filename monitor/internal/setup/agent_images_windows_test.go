package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestAgentImagesOwnDistinctFilesAndHoldTheirLifetime(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "gateway.exe")
	if err := os.WriteFile(source, []byte("fixture native image"), 0600); err != nil {
		t.Fatal(err)
	}
	codex, err := copyAgentImage(context.Background(), source, root, AgentCodex)
	if err != nil {
		t.Fatal(err)
	}
	defer codex.Close()
	claude, err := copyAgentImage(context.Background(), source, root, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	defer claude.Close()
	original, err := fileid.Resolve(context.Background(), fileid.Self, source, fileid.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if codex.FileKey() == claude.FileKey() || codex.FileKey() == original.Key() || claude.FileKey() == original.Key() {
		t.Fatal("copies share a native file identity")
	}
	for _, image := range []*AgentImage{codex, claude} {
		if err := image.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(image.Program(), []byte("replacement"), 0600); err == nil {
			t.Fatal("held image allowed a writer")
		}
		if err := os.Rename(image.Program(), image.Program()+".moved"); err == nil {
			t.Fatal("held image allowed replacement")
		}
		if _, err := copyAgentImage(context.Background(), source, root, AgentParty(filepath.Base(image.Program()))); err == nil {
			t.Fatal("unknown party accepted")
		}
	}
	if err := codex.Close(); err != nil {
		t.Fatal(err)
	}
	if err := codex.Check(context.Background()); err == nil {
		t.Fatal("closed image remained usable")
	}
	if _, err := copyAgentImage(context.Background(), source, root, AgentCodex); err == nil {
		t.Fatal("existing image was adopted")
	}
}

func TestAgentImagesRefuseForeignTargetsAndLinkedSources(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "gateway.exe")
	if err := os.WriteFile(source, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "oa-claude-integration.exe")
	if err := os.WriteFile(target, []byte("foreign"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAgentImage(context.Background(), source, root, AgentClaude); err == nil {
		t.Fatal("foreign target accepted")
	}
	body, err := os.ReadFile(target)
	if err != nil || string(body) != "foreign" {
		t.Fatal("foreign target changed", err)
	}
	link := filepath.Join(root, "gateway-alias.exe")
	if err := os.Link(source, link); err != nil {
		t.Fatal(err)
	}
	if _, err := copyAgentImage(context.Background(), source, root, AgentCodex); err == nil {
		t.Fatal("multiply named source accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "oa-codex-integration.exe")); !os.IsNotExist(err) {
		t.Fatal("source refusal created target", err)
	}
}

func TestAgentImagesCancellationHasNoTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "gateway.exe")
	if err := os.WriteFile(source, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := copyAgentImage(ctx, source, root, AgentCodex); err == nil {
		t.Fatal("cancelled copy accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "oa-codex-integration.exe")); !os.IsNotExist(err) {
		t.Fatal("cancelled copy created target", err)
	}
}

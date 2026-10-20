package fixture

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestGatewayFixtureAdvertisesServedChatV2(t *testing.T) {
	cfg := NewConfig("chat1715")
	cfg.JobRoot, cfg.Upstream = t.TempDir(), "http://127.0.0.1:1"
	host, err := start(cfg)
	if host != nil {
		defer host.Stop(t)
	}
	if runtime.GOOS == "darwin" {
		if host != nil || !errors.Is(err, identity.ErrNotProven) {
			t.Fatalf("Darwin Unix fixture must refuse Program proof: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	machine := facade.NewUnverified(cfg.RuntimeEndpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, scope := range []facade.Scope{facade.ScopeLocal, facade.ScopeRemote} {
		if _, err := machine.ResolveInferenceChatV2(ctx, facade.Requirements{Scope: scope}); err != nil {
			t.Fatalf("chat@2 scope %v: %v", scope, err)
		}
	}
}

func TestPreparedOpenCodeBootstrapIncludesInstalledModules(t *testing.T) {
	source, target := t.TempDir(), filepath.Join(t.TempDir(), "copy")
	file := filepath.Join(source, "node_modules", "fixture", "package.json")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(`{"name":"fixture"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyPreparedTree(source, target); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(target, "node_modules", "fixture", "package.json"))
	if err != nil || string(got) != `{"name":"fixture"}` {
		t.Fatalf("prepared module omitted or changed: %q %v", got, err)
	}
}

func TestPreparedOpenCodeBootstrapRefusesRelativeInputBeforeWriting(t *testing.T) {
	target := filepath.Join(t.TempDir(), "absent")
	if err := copyPreparedTree("relative", target); err == nil {
		t.Fatal("relative prepared source accepted")
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("refused input created its destination: %v", err)
	}
}

func TestPreparedOpenCodeBootstrapRefusesLinkedInput(t *testing.T) {
	source, outside := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "marker"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "linked")); err != nil {
		t.Skipf("owned symlink fixture unavailable without changing privileges: %v", err)
	}
	target := filepath.Join(t.TempDir(), "copy")
	if err := copyPreparedTree(source, target); err == nil {
		t.Fatal("linked prepared input accepted")
	}
	if _, err := os.Stat(filepath.Join(target, "linked", "marker")); !os.IsNotExist(err) {
		t.Fatalf("linked target copied: %v", err)
	}
}

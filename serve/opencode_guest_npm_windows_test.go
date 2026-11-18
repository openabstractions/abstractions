//go:build windows

package main

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// TestOpenCodeGuestPackageInstallFailsFastWithoutNetwork: the sandboxed guest
// runs OpenCode's npm installer (npm's own Arborist and registry client) for
// each config folder, a project's .opencode included, before its plugins
// load. With no network, npm retried every refused registry connection for
// about 70 s (measured 74 s). The guest environment marks npm offline, and the
// same install in a project's .opencode fails from npm's empty cache at once.
func TestOpenCodeGuestPackageInstallFailsFastWithoutNetwork(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	if err := os.Mkdir(plugin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "package.json"), []byte(`{"name":"@openabstractions/opencode"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(root, "opencode.json")
	if err := os.WriteFile(config, []byte(`{"provider":{"oa":{"npm":"@openabstractions/opencode"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(root, "data")
	selection := bootstrap.Selection{Endpoint: `\.\pipe\runtime`, Server: listen.ServerExpectation{
		Principal: identity.User{Kind: "windows", SID: "S-1-5-21-1"}, Program: filepath.Join(root, "runtime.exe")}}
	env, err := openCodeGuestEnvironment(&installedOpenCode{Config: config, Plugin: plugin}, data, `\.\pipe\session`,
		filepath.Join(root, "owner.exe"), "S-1-5-21-1", "launch", selection)
	if err != nil {
		t.Fatal(err)
	}
	offline := false
	for _, item := range env {
		if key, value, _ := strings.Cut(item, "="); strings.EqualFold(key, "npm_config_offline") && value == "true" {
			offline = true
		}
	}
	if !offline {
		t.Fatal("the guest environment leaves npm reaching for a registry the sandbox cannot reach")
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the npm installer check needs it")
	}
	cli := filepath.Join(filepath.Dir(node), "node_modules", "npm", "bin", "npm-cli.js")
	if _, err := os.Stat(cli); err != nil {
		t.Skip("npm is not installed beside node")
	}
	// A loopback port nothing listens on refuses connections, as the
	// sandbox's closed network does.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := listener.Addr().String()
	listener.Close()
	project := filepath.Join(root, "project", ".opencode")
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, node, cli, "install", "--ignore-scripts", "--no-audit", "--no-fund", "@opencode-ai/plugin@1.18.34")
	command.Dir = project
	command.Env = append(env, "npm_config_registry=http://"+closed+"/", "npm_config_update_notifier=false")
	start := time.Now()
	output, err := command.CombinedOutput()
	elapsed := time.Since(start)
	if ctx.Err() != nil {
		t.Fatalf("npm still waits on the closed network after %s", elapsed)
	}
	if err == nil || !strings.Contains(string(output), "ENOTCACHED") {
		t.Fatalf("npm did not fail offline (%v):\n%s", err, output)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("the offline install took %s", elapsed)
	}
	t.Logf("offline install failed in %s", elapsed)
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// startRemoteRuntime serves chat@1 and router@1 over the mutual-TLS remote
// transport on a loopback port, with one hosted host named openrouter, and
// returns its address and the trust files a local runtime reaches it with.
func startRemoteRuntime(t *testing.T) (string, inferenceRemoteTrust) {
	t.Helper()
	serverTLS, clientDigest, trust := remoteTrustFiles(t, t.TempDir())
	remoteRouter := router.New(router.NewHosted("openrouter", "https://openrouter.invalid/api/v1", router.WireOpenAICompatible, ""))
	// The remote runtime's own survey; its host reads down, and is still listed.
	remoteRouter.Survey()
	remoteProvider, err := inference.New(inference.Config{Router: remoteRouter,
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "unknown"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { remoteProvider.Close() })
	authorize := func(_ context.Context, peer remote.Peer) (inference.Subject, string, error) {
		if peer.Key != clientDigest {
			return inference.Subject{}, "", errors.New("unmapped")
		}
		return inference.Subject{Account: "peer", Program: "/remote/openabstractions"}, "peer", nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- (remote.Server{TLS: serverTLS, Handler: service.RemoteHandler(remoteProvider, remoteRouter, authorize), Timeout: 30 * time.Second}).Serve(ctx, listener)
	}()
	t.Cleanup(func() { cancel(); <-served })
	return listener.Addr().String(), trust
}

// registryListing is the part of `provider list --json` the migration test
// compares, read without the typed registry client so the comparison holds
// across the definition move.
type registryListing struct {
	Outcome      string
	Revision     string
	Declarations []struct {
		Declaration struct {
			Name, Program, Endpoint, Transport, Activation string
			Contracts, Guarantees, Resources               []string
			Remote                                         *struct{ ServerName, Roots, Certificate, Key, Credential string }
		}
		Readiness, Why string
	}
}

func readRegistryListing(t *testing.T, endpoint []string) (registryListing, string) {
	t.Helper()
	out, err := runProvider(t, append([]string{"list", "--json"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider list: %v\n%s", err, out)
	}
	var listing registryListing
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		t.Fatalf("provider list json: %v\n%s", err, out)
	}
	return listing, out
}

// readiness is each listed declaration's readiness by name.
func (l registryListing) readiness() map[string]string {
	out := map[string]string{}
	for _, d := range l.Declarations {
		out[d.Declaration.Name] = d.Readiness
	}
	return out
}

// A state directory the registration build wrote, with providers/<name>.json
// carrying stores and profiles and hosts.json carrying a remote runtime,
// starts on the registry runtime: the files are rewritten once, and the
// registry lists the same two providers and the remote, each ready.
func TestAStateDirectoryFromTheRegistrationBuildMigratesOnce(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	suffix := time.Now().UnixNano() % 1_000_000_000

	// An attached chat provider, running before the runtime starts.
	chatProgram := copyTestBinary(t, "oa-migrated-chat")
	chatEndpoint := fmt.Sprintf("migrated-chat-%d", suffix)
	chat := exec.Command(chatProgram, providerFixtureArg, "chat", chatEndpoint, t.TempDir())
	if err := chat.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Process.Kill(); chat.Wait() })
	eventually(t, 10*time.Second, "the chat provider listening", func() bool { return answers(chatEndpoint) })

	// An attached inventory source.
	inventoryd := buildInventoryd(t)
	storesEndpoint := fmt.Sprintf("migrated-stores-%d", suffix)
	startInventoryd(t, inventoryd, storesEndpoint)

	// A remote runtime over mutual TLS.
	address, trust := startRemoteRuntime(t)

	options, _ := isolatedRuntime(t)
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	quote := func(s string) string {
		raw, _ := json.Marshal(s)
		return string(raw)
	}
	providers := filepath.Join(options.stateDir, "providers")
	write(filepath.Join(providers, "local-chat.json"), `{
  "version": 1,
  "declaration": {
    "name": "local-chat",
    "program": `+quote(chatProgram)+`,
    "arguments": [],
    "endpoint": `+quote(chatEndpoint)+`,
    "transport": "oa-native@1",
    "contracts": ["abstraction.inference/chat@1"],
    "guarantees": [`+quote(fixtureGuarantee)+`],
    "profiles": ["chat"],
    "activation": "attach"
  },
  "declared_by": "/opt/oa/openabstractions",
  "declared_unix_ms": 1789000000000
}
`)
	write(filepath.Join(providers, "local-stores.json"), `{
  "version": 1,
  "declaration": {
    "name": "local-stores",
    "program": `+quote(inventoryd)+`,
    "arguments": [],
    "endpoint": `+quote(storesEndpoint)+`,
    "transport": "oa-native@1",
    "contracts": ["abstraction.storage/inventory-source@1"],
    "activation": "attach",
    "stores": ["ollama"]
  },
  "declared_by": "/opt/oa/openabstractions",
  "declared_unix_ms": 1789000000000
}
`)
	hostsPath := filepath.Join(options.stateDir, "inference", inferenceHostsFile)
	write(hostsPath, `{
  "local": [],
  "hosted": [
    {
      "name": "lab",
      "base": "tls://`+address+`",
      "wire": "oa-remote@1",
      "credential": "openrouter",
      "profiles": ["chat"],
      "declared_by": "operator",
      "remote": {
        "server_name": `+quote(trust.ServerName)+`,
        "roots": `+quote(trust.Roots)+`,
        "certificate": `+quote(trust.Certificate)+`,
        "key": `+quote(trust.Key)+`
      }
    }
  ]
}
`)

	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	want := map[string]string{"local-chat": "ready", "local-stores": "ready", "lab": "ready"}
	check := func(round string) registryListing {
		t.Helper()
		var listing registryListing
		var raw string
		deadline := time.Now().Add(30 * time.Second)
		for {
			listing, raw = readRegistryListing(t, endpoint)
			if listing.Outcome == "page" && holdsAll(listing.readiness(), want) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the registry never listed %v\n%s", round, want, raw)
			}
			time.Sleep(100 * time.Millisecond)
		}
		for _, d := range listing.Declarations {
			decl := d.Declaration
			switch decl.Name {
			case "local-chat":
				if decl.Program != chatProgram || decl.Endpoint != chatEndpoint || decl.Transport != "oa-native@1" || decl.Activation != "attach" ||
					!slices.Equal(decl.Contracts, []string{inference.Contract}) || !slices.Equal(decl.Guarantees, []string{fixtureGuarantee}) ||
					!slices.Equal(decl.Resources, []string{"profile:chat"}) {
					t.Fatalf("%s: local-chat %+v", round, decl)
				}
			case "local-stores":
				if decl.Program != inventoryd || !slices.Equal(decl.Resources, []string{"store:ollama"}) {
					t.Fatalf("%s: local-stores %+v", round, decl)
				}
			case "lab":
				if decl.Transport != "oa-remote@1" || decl.Activation != "remote" || decl.Endpoint != "tls://"+address || decl.Remote == nil ||
					decl.Remote.ServerName != trust.ServerName || decl.Remote.Key != trust.Key || decl.Remote.Credential != "openrouter" {
					t.Fatalf("%s: lab %+v", round, decl)
				}
			}
		}

		// The router still lists the remote's host in its domain.
		call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		routes, err := client.New(options.endpoint).ResolveRouter(call, client.Requirements{})
		if err != nil {
			t.Fatalf("%s: %v", round, err)
		}
		snapshot, err := routes.HostsContext(call, true)
		if err != nil {
			t.Fatalf("%s: %v", round, err)
		}
		domainHost := false
		for _, h := range snapshot.Hosts {
			domainHost = domainHost || h.Host == "lab/openrouter" && h.Domain == "lab"
		}
		if !domainHost {
			t.Fatalf("%s: router@1 hosts lack the remote's host: %+v", round, snapshot.Hosts)
		}
		return listing
	}

	stop := startInferenceRuntime(t, options)
	check("first start")
	stop()

	// The rewrite: resources replace stores and profiles, the remote is a
	// declaration, and hosts.json keeps no remote entry.
	files := map[string][]byte{}
	for _, name := range []string{"local-chat", "local-stores", "lab"} {
		path := filepath.Join(providers, name+".json")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("declaration %s after the rewrite: %v", name, err)
		}
		text := string(raw)
		if strings.Contains(text, `"stores"`) || strings.Contains(text, `"profiles"`) || !strings.Contains(text, `"resources"`) && name != "lab" {
			t.Fatalf("declaration %s was not rewritten:\n%s", name, raw)
		}
		files[path] = raw
	}
	hosts, err := os.ReadFile(hostsPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(hosts), "oa-remote@1") || strings.Contains(string(hosts), `"remote"`) {
		t.Fatalf("hosts.json keeps a remote entry:\n%s", hosts)
	}
	files[hostsPath] = hosts

	// A second start reads the rewritten state and rewrites nothing.
	startInferenceRuntime(t, options)
	check("second start")
	for path, before := range files {
		after, err := os.ReadFile(path)
		if err != nil || string(after) != string(before) {
			t.Fatalf("%s changed on the second start: %v\nbefore:\n%s\nafter:\n%s", path, err, before, after)
		}
	}
}

// holdsAll reports whether got holds every entry of want.
func holdsAll(got, want map[string]string) bool {
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

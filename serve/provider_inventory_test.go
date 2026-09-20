package main

import (
	"context"
	"fmt"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// buildInventoryd gives the in-binary inventory fixture its own program path,
// preserving the program-bound provider proof without private source packages.
func buildInventoryd(t *testing.T) string {
	t.Helper()
	return copyTestBinary(t, "oa-inventory-source")
}

func startInventoryd(t *testing.T, binary, endpoint string) {
	t.Helper()
	cmd := exec.Command(binary, providerFixtureArg, "inventory", endpoint, t.TempDir())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	eventually(t, 20*time.Second, "inventoryd listening on "+endpoint, func() bool { return answers(endpoint) })
}

// inventoryd reaches the runtime through a provider declaration: the runtime
// accepts the stores the declaration names and inventory.provide permits, and
// lists nothing from a store it was not given, from a source nobody declared,
// or from a store whose rule the person denied.
func TestADeclaredInventorySourceIsListedAndAnUndeclaredOneIsRefused(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	binary := buildInventoryd(t)
	suffix := time.Now().UnixNano() % 1_000_000_000
	declared, undeclared := fmt.Sprintf("inventory-declared-%d", suffix), fmt.Sprintf("inventory-undeclared-%d", suffix)
	startInventoryd(t, binary, declared)
	startInventoryd(t, binary, undeclared)

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, err := runProvider(t, append([]string{"add", "local-stores", "--program", binary, "--provider-endpoint", declared,
		"--contract", storageInventorySource, "--activate", "attach", "--store", "ollama", "--store", "huggingface", "--store", "not-described"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	if strings.Contains(out, "not every rule") {
		t.Fatalf("provider add rules: %s", out)
	}
	var state fwire.DeclarationState
	eventually(t, 20*time.Second, "the declared source being accepted", func() bool {
		state = providerStates(t, endpoint)["local-stores"]
		return state.Readiness == fwire.DeclarationReadinessReady && len(state.Accepted) == 2
	})
	if !slices.Equal(state.Accepted, []string{"store:huggingface", "store:ollama"}) ||
		!slices.Equal(state.Declaration.Resources, []string{"store:ollama", "store:huggingface", "store:not-described"}) {
		t.Fatalf("accepted %v of %v", state.Accepted, state.Declaration.Resources)
	}

	// The operator wrote inventory.provide for the declared program, with its provenance.
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	subject := rights.Subject{Account: account.Uid, Program: binary}
	read, err := operator.ReadRuleContext(call, subject, ActionInventoryProvide, ResourceStore("ollama"))
	if err != nil || read.Outcome.String() != "found" || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
		t.Fatalf("inventory.provide rule: %+v %v", read, err)
	}

	// The runtime's inventory lists the declared source only.
	composed := inventorySourcesOf(t, options)
	if len(composed) != 1 || composed[0].Endpoint != declared || !slices.Equal(composed[0].Stores, []string{"huggingface", "ollama"}) {
		t.Fatalf("inventory sources %+v", composed)
	}
	for _, s := range composed {
		if s.Endpoint == undeclared {
			t.Fatal("an undeclared source was read")
		}
	}

	// A denied store is refused on the next reading.
	page, err := operator.ListPolicyContext(call, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	deny := rights.PolicyRule{Subject: subject, Action: ActionInventoryProvide, Resource: ResourceStore("huggingface"), Permit: false}
	if edit, err := operator.SetRuleContext(call, page.Revision, deny); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("deny: %+v %v", edit, err)
	}
	eventually(t, 20*time.Second, "the denied store being refused", func() bool {
		return slices.Equal(providerStates(t, endpoint)["local-stores"].Accepted, []string{"store:ollama"})
	})
}

// inventorySource is one ready source and the stores the runtime accepts.
type inventorySource struct {
	Name, Program, Endpoint string
	Stores                  []string
}

// inventorySourcesOf reads the sources a running runtime accepts stores from,
// through its provider listing.
func inventorySourcesOf(t *testing.T, options runtimeFlags) []inventorySource {
	t.Helper()
	var out []inventorySource
	for _, state := range providerStates(t, []string{"--endpoint", options.endpoint, "--timeout", "30s"}) {
		d := state.Declaration
		if slices.Contains(d.Contracts, storageInventorySource) && state.Readiness == fwire.DeclarationReadinessReady && len(state.Accepted) > 0 {
			var stores []string
			for _, resource := range state.Accepted {
				stores = append(stores, strings.TrimPrefix(resource, "store:"))
			}
			out = append(out, inventorySource{Name: d.Name, Program: d.Program, Endpoint: d.Endpoint, Stores: stores})
		}
	}
	return out
}

// Stores belong to inventory sources only, and a source names at least one.
func TestProviderStoresBelongToInventorySources(t *testing.T) {
	program := filepath.Join(t.TempDir(), "inventoryd")
	source := providerDeclaration{Name: "local-stores", Program: program, Endpoint: "inventory", Transport: transportNative,
		Contracts: []string{storageInventorySource}, Activation: "on_demand", Resources: []string{"store:ollama"}}
	if field := validProviderDeclaration(source); field != "" {
		t.Fatalf("source: %s", field)
	}
	for _, edit := range []func(*providerDeclaration){
		func(d *providerDeclaration) { d.Resources = nil },
		func(d *providerDeclaration) { d.Resources = []string{"profile:chat"} },
		func(d *providerDeclaration) { d.Resources = []string{"store:Ollama"} },
		func(d *providerDeclaration) { d.Resources = []string{"store:ollama", "store:ollama"} },
		func(d *providerDeclaration) { d.Contracts = []string{"abstraction.inference/chat@1"} },
	} {
		d := source
		edit(&d)
		if field := validProviderDeclaration(d); field != "resources" {
			t.Fatalf("%+v: %q", d, field)
		}
	}
}

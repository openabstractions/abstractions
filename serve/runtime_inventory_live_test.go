package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	rwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// liveLMStudio is where LM Studio answers on the machine this test was
// written for. It is read; it is never started, stopped or reconfigured.
const liveLMStudio = "http://127.0.0.1:1234"

// buildLocalStoresInventoryd builds the real provider from this checkout.
func buildLocalStoresInventoryd(t *testing.T) string {
	t.Helper()
	name := "inventoryd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
	module, err := filepath.Abs(filepath.Join("..", "openabstractions-flat", "abstraction-storage-over-local-stores", "go"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "-C", module, "build", "-o", target, "./cmd/inventoryd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build inventoryd: %v\n%s", err, out)
	}
	return target
}

// The runtime composes what this machine's stores hold and the router names
// the stores behind the models a host serves. Live: it builds the real
// inventoryd, declares it to an isolated runtime with the stores this machine
// has, reads abstraction.storage/inventory@1 through the facade client, and
// reads the router's catalogue against the LM Studio that is running here.
func TestLiveLocalStoresReachTheInventoryAndTheRouter(t *testing.T) {
	if os.Getenv("OA_LIVE_LOCAL_STORES") != "1" {
		t.Skip("set OA_LIVE_LOCAL_STORES=1 to read this machine's own stores and LM Studio")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	binary := buildLocalStoresInventoryd(t)
	endpointName := fmt.Sprintf("live-local-stores-%d", time.Now().UnixNano()%1_000_000_000)

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "60s"}

	// The LM Studio this machine is already running, as a host of this
	// isolated runtime only. A fresh runtime declares it at its own default
	// address, and that declaration is the one to keep.
	if out, err := runInference(t, append([]string{"host", "add", "lmstudio", "--base", liveLMStudio}, endpoint...)...); err != nil &&
		!strings.Contains(out+err.Error(), "conflict: name") {
		t.Fatalf("host add lmstudio: %v\n%s", err, out)
	}
	hosts, err := runInference(t, append([]string{"host", "list", "--json"}, endpoint...)...)
	if err != nil || !strings.Contains(hosts, `"Name":"lmstudio"`) {
		t.Fatalf("host list: %v\n%s", err, hosts)
	}

	// Every store inventoryd finds at a fixed location on this machine.
	stores := []string{"ollama", "huggingface", "lmstudio", "comfyui", "fastflowlm", "jan"}
	add := []string{"add", "local-stores", "--program", binary, "--provider-endpoint", endpointName,
		"--contract", storageInventorySource, "--arg", "serve", "--arg", "--endpoint", "--arg", providerEndpointToken}
	for _, store := range stores {
		add = append(add, "--store", store)
	}
	out, err := runProvider(t, append(add, endpoint...)...)
	if err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	if strings.Contains(out, "not every rule") {
		t.Fatalf("provider add rules: %s", out)
	}

	var state fwire.DeclarationState
	eventually(t, 90*time.Second, "the local-stores source being accepted", func() bool {
		state = providerStates(t, endpoint)["local-stores"]
		return state.Readiness == fwire.DeclarationReadinessReady && len(state.Accepted) == len(stores)
	})
	t.Logf("accepted stores: %v", state.Accepted)

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	subject := rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}
	for _, rule := range [][2]string{
		{ActionInventoryRead, storageservice.InventoryResource},
		{contentReadAction, storageservice.InventoryResource},
		{routerservice.ActionInventory, routerservice.ResourceInventory},
	} {
		page, err := operator.ListPolicyContext(call, "", 1)
		if err != nil || page.Outcome.String() != "page" {
			t.Fatalf("list policy: %+v %v", page, err)
		}
		permit := rights.PolicyRule{Subject: subject, Action: rule[0], Resource: rule[1], Permit: true}
		if edit, err := operator.SetRuleContext(call, page.Revision, permit); err != nil || edit.Outcome.String() != "applied" {
			t.Fatalf("permit %v: %+v %v", rule, edit, err)
		}
	}

	// The composed inventory, read as an application reads it.
	inventory, err := machine.ResolveStorageInventory(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	var listed []content.Store
	byStore := map[string]int{}
	lmstudioNames := []string{}
	continuation := ""
	for pages := 0; pages < 512; pages++ {
		page, err := inventory.List(call, continuation, 256)
		if err != nil {
			t.Fatalf("inventory list: %v", err)
		}
		if page.Outcome != content.InventoryOutcomePage {
			t.Fatalf("inventory outcome %s", page.Outcome)
		}
		if continuation == "" {
			listed = page.Stores
		}
		for _, m := range page.Manifests {
			byStore[m.Manifest.Store]++
			if m.Manifest.Store == "lmstudio" && len(m.Manifest.Names) > 0 {
				lmstudioNames = append(lmstudioNames, m.Manifest.Names[0].Name)
			}
		}
		for _, o := range page.Objects {
			byStore[o.Store]++
		}
		if page.Complete {
			break
		}
		continuation = page.Continuation
	}
	for _, s := range listed {
		t.Logf("store %-12s program=%-12s present=%v records=%d errors=%d", s.Name, s.Program, s.Present, byStore[s.Name], len(s.Errors))
	}
	if len(listed) != len(stores) {
		t.Fatalf("stores %d, accepted %d", len(listed), len(stores))
	}
	if byStore["lmstudio"] == 0 {
		t.Fatalf("the LM Studio store's objects did not reach the inventory: %v", byStore)
	}
	slices.Sort(lmstudioNames)
	t.Logf("lmstudio records: %d, first: %s", byStore["lmstudio"], lmstudioNames[0])

	// The router's catalogue, with the stores behind each family.
	routerClient, err := machine.ResolveRouter(call, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	var served []rwire.Family
	eventually(t, 4*time.Minute, "an LM Studio family held in the lmstudio store", func() bool {
		snapshot, err := routerClient.ModelsContext(call, true)
		if err != nil {
			t.Fatalf("router models: %v", err)
		}
		served = snapshot.Models
		for _, f := range snapshot.Models {
			if !slices.Contains(f.HeldIn, "lmstudio") {
				continue
			}
			for _, a := range f.Names {
				if a.Host == "lmstudio" {
					return true
				}
			}
		}
		return false
	})
	holdsOnly, described, lmstudioDescribed := 0, 0, 0
	for _, f := range served {
		servable := slices.ContainsFunc(f.Names, func(a rwire.Alias) bool { return a.Servable })
		if !servable {
			holdsOnly++
		}
		if f.FamilySource == router.FromDescriptor {
			described++
			if slices.ContainsFunc(f.Names, func(a rwire.Alias) bool { return a.Host == "lmstudio" }) {
				lmstudioDescribed++
			}
		}
		if len(f.HeldIn) == 0 && servable {
			continue
		}
		t.Logf("family %-40s source=%-10s servable=%v held_in=%v", f.Family, f.FamilySource, servable, f.HeldIn)
	}
	t.Logf("families: %d, of them held here and served by no host: %d", len(served), holdsOnly)
	t.Logf("family identity: %d from a store's descriptor, %d from a host's alias; %d of LM Studio's from a descriptor",
		described, len(served)-described, lmstudioDescribed)

	// R2: the model identity is the descriptor manifest. LM Studio's store is
	// read by the same inventoryd this test built, so every family LM Studio
	// serves has a descriptor behind it.
	if lmstudioDescribed == 0 {
		t.Fatalf("no family LM Studio serves took its identity from a descriptor")
	}
	for _, f := range served {
		if !slices.Contains(f.HeldIn, "lmstudio") {
			continue
		}
		if f.FamilySource != router.FromDescriptor {
			t.Errorf("family %q is held in lmstudio and its identity is %q", f.Family, f.FamilySource)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// liveEngines are where a llama-server was found on this machine on
// 2026-09-22 (research/model-bridge/EXPERIMENT-2026-09-22.md §3): two inside
// Lemonade's own cache and one Ollama vendors beside the AMD bundle. The test
// uses whichever exists; none of them is installed, downloaded or modified by
// this project, and a machine with none of them skips.
func liveEngines() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".cache", "lemonade", "bin", "llamacpp", "rocm-stable", "llama-server.exe"),
		filepath.Join(home, ".cache", "lemonade", "bin", "llamacpp", "vulkan", "llama-server.exe"),
		filepath.Join(home, "AppData", "Local", "AMD", "AI_Bundle", "Ollama", "lib", "ollama", "llama-server.exe"),
	}
}

// embeddingNames are the words a store's own name for an embedding model
// carries. llama-server loads one and has no chat template for it, so a chat
// through it says nothing about this provider. This test measures the host,
// so it hosts the smallest GGUF that is not one of these.
var embeddingNames = []string{"embed", "bge", "minilm", "sentence-transformer", "nomic", "gte-", "e5-", "jina"}

func embeddingModel(name string) bool {
	lower := strings.ToLower(name)
	for _, word := range embeddingNames {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func installedEngine() string {
	for _, path := range liveEngines() {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// buildModelhostd builds the real model host provider from this checkout.
func buildModelhostd(t *testing.T) string {
	t.Helper()
	name := "modelhostd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
	module, err := filepath.Abs(filepath.Join("..", "openabstractions-flat", "abstraction-provider-modelhost", "go"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "-C", module, "build", "-o", target, "./cmd/modelhostd")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build modelhostd: %v\n%s", err, out)
	}
	return target
}

// One model, loaded once, held under a lease, given back when the card is
// asked for. Live: it builds the real inventoryd and modelhostd, declares both
// to an isolated runtime, hosts the smallest GGUF the composed inventory lists
// with the llama-server this machine already has and --embeddings, answers
// one chat through the runtime, answers a second one off the same load,
// answers one embed@1 call off the same load, and then asks the card for its
// whole capacity and asserts the host let go.
//
// It installs nothing, downloads nothing and touches no other engine: the only
// process it stops is the one modelhostd started, by that process's own id.
func TestLiveHostTheSmallestGGUFUnderALease(t *testing.T) {
	if os.Getenv("OA_LIVE_MODELHOST") != "1" {
		t.Skip("set OA_LIVE_MODELHOST=1 to load one of this machine's own model files with its own llama.cpp")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	engine := installedEngine()
	if engine == "" {
		t.Skipf("no llama-server at any of the paths the 2026-09-22 experiment found: %v", liveEngines())
	}
	t.Logf("engine: %s", engine)

	inventoryd := buildLocalStoresInventoryd(t)
	modelhostd := buildModelhostd(t)
	stamp := time.Now().UnixNano() % 1_000_000_000

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "600s"}

	stores := []string{"ollama", "huggingface", "lmstudio", "comfyui", "fastflowlm", "jan"}
	add := []string{"add", "local-stores", "--program", inventoryd, "--provider-endpoint", fmt.Sprintf("live-host-inventory-%d", stamp),
		"--contract", storageInventorySource, "--arg", "serve", "--arg", "--endpoint", "--arg", providerEndpointToken}
	for _, store := range stores {
		add = append(add, "--store", store)
	}
	if out, err := runProvider(t, append(add, endpoint...)...); err != nil {
		t.Fatalf("provider add local-stores: %v\n%s", err, out)
	}
	eventually(t, 90*time.Second, "the local-stores source being accepted", func() bool {
		state := providerStates(t, endpoint)["local-stores"]
		return state.Readiness == fwire.DeclarationReadinessReady && len(state.Accepted) == len(stores)
	})

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	subject := rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}
	rule := func(who rights.Subject, action, resource string, permit bool) {
		t.Helper()
		page, err := operator.ListPolicyContext(call, "", 1)
		if err != nil || page.Outcome.String() != "page" {
			t.Fatalf("list policy: %+v %v", page, err)
		}
		edit := rights.PolicyRule{Subject: who, Action: action, Resource: resource, Permit: permit}
		if applied, err := operator.SetRuleContext(call, page.Revision, edit); err != nil || applied.Outcome.String() != "applied" {
			t.Fatalf("rule %s on %s for %s: %+v %v", action, resource, who.Program, applied, err)
		}
	}
	permit := func(who rights.Subject, action, resource string) {
		t.Helper()
		rule(who, action, resource, true)
	}
	permit(subject, ActionInventoryRead, storageservice.InventoryResource)
	permit(subject, contentReadAction, storageservice.InventoryResource)
	permit(subject, resourceservice.ActionTableRead, resourceservice.ResourceAccount)
	permit(subject, resourceservice.ActionHold, instrument.Card0)

	// The smallest GGUF the composed inventory lists. An embedding model is
	// left out: llama-server serves one without a chat template, and this test
	// measures the host, not what a model can answer.
	inventory, err := machine.ResolveStorageInventory(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	type candidate struct {
		store, id, name string
		size            int64
	}
	var smallest candidate
	continuation := ""
	for pages := 0; pages < 512; pages++ {
		page, err := inventory.List(call, continuation, 256)
		if err != nil {
			t.Fatalf("inventory list: %v", err)
		}
		if page.Outcome != content.InventoryOutcomePage {
			t.Fatalf("inventory outcome %s", page.Outcome)
		}
		for _, m := range page.Manifests {
			if len(m.Manifest.Names) == 0 {
				continue
			}
			name := m.Manifest.Names[0].Name
			if embeddingModel(name) {
				continue
			}
			for _, e := range m.Manifest.Entries {
				if !strings.HasSuffix(strings.ToLower(e.Locator), ".gguf") || e.Size <= 0 || shard.MatchString(e.Locator) {
					continue
				}
				if smallest.size == 0 || e.Size < smallest.size {
					smallest = candidate{store: m.Manifest.Store, id: m.Manifest.ID, name: name, size: e.Size}
				}
			}
		}
		if page.Complete {
			break
		}
		continuation = page.Continuation
	}
	if smallest.size == 0 {
		t.Skip("this machine's inventory lists no GGUF that is not an embedding model")
	}
	t.Logf("smallest GGUF: store=%s size=%d name=%s id=%s", smallest.store, smallest.size, smallest.name, smallest.id)

	object := smallest.store + "/" + smallest.id
	providerEndpointName := fmt.Sprintf("live-host-%d", stamp)
	hostArgs := []string{"host", object, "--engine", engine, "--program", modelhostd,
		"--provider-endpoint", providerEndpointName,
		"--resource-endpoint", options.endpoint + "-resource-table", "--idle", "10m", "--embeddings",
		// The smallest GGUF is a chat model, not an embedding one: its own
		// metadata carries no pooling head, so llama-server's OpenAI-compatible
		// /v1/embeddings refuses it as "pooling type 'none' is not OAI
		// compatible" without an explicit pooling choice. --pooling is an
		// ordinary llama-server flag a person adds with --engine-arg
		// (CONTRACT.md MODELHOST-L3: this layer chooses none of the engine's own
		// arguments); a model built for embeddings carries its own pooling
		// type and needs no override.
		"--engine-arg", "--pooling", "--engine-arg", "mean"}
	out, err := runModels(t, append(hostArgs, endpoint...)...)
	if err != nil {
		t.Fatalf("models host: %v\n%s", err, out)
	}
	t.Logf("models host: %s", strings.TrimSpace(out))
	t.Cleanup(func() {
		if out, err := runModels(t, append([]string{"unhost", object}, endpoint...)...); err != nil {
			t.Logf("sweep: unhost: %v\n%s", err, out)
		}
	})

	// The runtime writes the provider its own hold rule when the declaration
	// lands; without it the first call reads not_permitted.
	eventually(t, 60*time.Second, "the model host being ready and in the router's list", func() bool {
		state, ok := providerStates(t, endpoint)[modelHostName]
		if !ok {
			return false
		}
		if state.Readiness != fwire.DeclarationReadinessReady {
			t.Logf("model host: %s %s", state.Readiness, state.Why)
			return false
		}
		return true
	})
	permit(subject, inference.ActionComplete, inference.ResourceHost(modelHostName))

	chat, err := machine.ResolveInference(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	ask := func(text string) (inferenceclient.Reply, time.Duration) {
		t.Helper()
		began := time.Now()
		reply, err := chat.Complete(call, inferenceclient.Request{Model: smallest.name,
			Options:  &inferenceclient.Options{MaxOutput: 16},
			Messages: []inferenceclient.Message{{Role: inferenceclient.RoleUser, Parts: []inferenceclient.Part{{Kind: inferenceclient.PartKindText, Text: text}}}}})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		return reply, time.Since(began)
	}

	first, loadTook := ask("Say hi in three words.")
	if first.Outcome != inferenceclient.ReplyOutcomeCompleted {
		t.Fatalf("the first call read %s: %s", first.Outcome, first.Reason)
	}
	t.Logf("first call: %s in %s, text %q", first.Outcome, loadTook, replyText(first))

	table, err := machine.ResolveResourceTable(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	held, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatalf("holders: %v", err)
	}
	lease := ""
	for _, row := range held.Holders {
		if row.Lease != "" && strings.EqualFold(row.Program, modelhostd) {
			lease = row.Lease
			t.Logf("the model host holds %s: lease %s, %d bytes, %s", instrument.Card0, row.Lease, row.Amount, row.Evidence)
		}
	}
	if lease == "" {
		t.Fatalf("the table names no lease held by %s:\n%+v", modelhostd, held.Holders)
	}

	second, reuseTook := ask("Say hi again in three words.")
	if second.Outcome != inferenceclient.ReplyOutcomeCompleted {
		t.Fatalf("the second call read %s: %s", second.Outcome, second.Reason)
	}
	t.Logf("second call: %s in %s (the first took %s to load), text %q", second.Outcome, reuseTook, loadTook, replyText(second))
	if reuseTook >= loadTook {
		t.Logf("the second call was not faster than the first; the load may have been cached by the operating system")
	}

	// The same GGUF, still resident under the same lease, answers embed@1 too:
	// this host was declared --embeddings, so llama-server serves both
	// endpoints off the one engine it started. The call goes through the
	// runtime's own embed@1 (nativeEmbed, abstraction-inference embed.go),
	// exactly as chat went through machine.ResolveInference above: the
	// runtime picks this declared native provider for profile embed, decides
	// abstraction.inference/complete on the same host resource the chat rule
	// already permits, and reaches this provider's own embed@1 over the
	// identity-bound transport.
	embeddings, err := machine.ResolveEmbeddings(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	embedBegan := time.Now()
	embedded, err := embeddings.Embed(call, iwire.EmbedRequest{Model: smallest.name, Inputs: []string{"Say hi in three words."}})
	embedTook := time.Since(embedBegan)
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	if embedded.Outcome != iwire.EmbedOutcomeCompleted {
		t.Fatalf("the embed call read %s: %s", embedded.Outcome, embedded.Reason)
	}
	vectors, err := inferenceclient.Vectors(embedded)
	if err != nil || len(vectors) != 1 {
		t.Fatalf("embed vectors: %v (%d vector(s))", err, len(vectors))
	}
	t.Logf("embed@1: %s in %s, %d dimensions (reported %d), %d input tokens",
		embedded.Outcome, embedTook, len(vectors[0]), embedded.Dimensions, embedded.Usage.Input)

	// No engine this test did not start is touched: every attached holder's
	// yield rule is denied, so the asks reach the model host's lease and the
	// LM Studio, Ollama or ComfyUI running here keep what they hold.
	for _, attached := range unloadableHosts {
		rule(subject, resourceservice.ActionYield, claimedHostPrefix+attached, false)
	}

	// Ask the card for the pool the lease service itself would use: the
	// instrument's capacity where it has one, and the machine's memory where
	// it has none (RES-L2). That is more than is free while this host holds a
	// grant, so the service has to ask its holders.
	before, err := table.HoldersContext(call, instrument.Card0, true)
	if err != nil {
		t.Fatalf("holders: %v", err)
	}
	t.Logf("card:0 before the ask: capacity %d, held %d, instrument %s, %d holder(s)",
		before.Capacity, before.Held, before.Instrument, len(before.Holders))
	for _, row := range before.Holders {
		t.Logf("  %s %s %d bytes lease=%q detail=%q", row.Evidence, row.Program, row.Amount, row.Lease, row.Detail)
	}
	// The Windows counters report a capacity only sometimes, and the pool is
	// the machine's memory when they do not, so the amount that is more than
	// free is one of two figures. Both are tried, largest first: an ask above
	// a reported capacity reads insufficient without asking anybody, and one
	// below what is free is granted without asking anybody.
	amounts := []int64{instrument.MachineMemory()}
	if before.Capacity > 0 {
		amounts = append(amounts, before.Capacity)
	}
	answered, granted := "", ""
	var asked string
	for _, amount := range amounts {
		out, askErr := runResourcesAsk(t, []string{instrument.Card0, strconv.FormatInt(amount, 10), "--wait", "60s", "--release", "--json"}, endpoint)
		t.Logf("resources ask %s %d: %v\n%s", instrument.Card0, amount, askErr, strings.TrimSpace(out))
		var result askOutput
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("resources ask json: %v\n%s", err, out)
		}
		asked, granted = out, result.Outcome
		for _, row := range result.Asked {
			if strings.EqualFold(row.Holder, modelhostd) {
				answered = row.Answer
				t.Logf("the model host was asked and answered %q, freeing %d bytes in %dms", row.Answer, row.Amount, row.TookMS)
			}
		}
		if answered != "" {
			break
		}
	}
	if answered == "" {
		t.Fatalf("the model host was not asked (last outcome %s):\n%s", granted, asked)
	}

	// Whatever the instrument could see of a file this small, the provider let
	// go: the lease it held is no longer in the table.
	eventually(t, 60*time.Second, "the model host releasing its lease", func() bool {
		state, err := table.HoldersContext(call, instrument.Card0, true)
		if err != nil {
			return false
		}
		for _, row := range state.Holders {
			if row.Lease == lease {
				return false
			}
		}
		return true
	})
	t.Logf("after the ask the model host holds no lease; it unloaded %s", smallest.name)
}

// replyText is the text of a completed reply, for the log.
func replyText(reply inferenceclient.Reply) string {
	var text strings.Builder
	for _, part := range reply.Message.Parts {
		text.WriteString(part.Text)
	}
	return strings.TrimSpace(text.String())
}

// runResourcesAsk is the operator command that provokes the yield: it asks the
// card for bytes and reports every holder the service asked.
func runResourcesAsk(t *testing.T, args []string, endpoint []string) (string, error) {
	t.Helper()
	var out, diagnostics strings.Builder
	err := resourcesAskCommand(append(args, endpoint...), &out, &diagnostics)
	return out.String() + diagnostics.String(), err
}

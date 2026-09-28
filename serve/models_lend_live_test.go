package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// shard matches one part of a split GGUF, which no engine loads alone.
var shard = regexp.MustCompile(`(?i)-\d{5}-of-\d{5}\.gguf$`)

// LendDirectoryName is the directory the provider creates inside an engine's
// own models folder. The test names it to sweep what a failed run left.
const LendDirectoryName = "openabstractions-lend"

// buildModelbridged builds the real lending provider from this checkout.
func buildModelbridged(t *testing.T) string {
	t.Helper()
	name := "modelbridged"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
	module, err := filepath.Abs(filepath.Join("..", "openabstractions-flat", "abstraction-provider-modelbridge", "go"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "-C", module, "build", "-o", target, "./cmd/modelbridged")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build modelbridged: %v\n%s", err, out)
	}
	return target
}

func runModels(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, diagnostics strings.Builder
	err := modelsCommand(args, &out, &diagnostics)
	return out.String() + diagnostics.String(), err
}

// lmStudioModels is the model ids LM Studio lists right now.
func lmStudioModelIDs(t *testing.T) []string {
	t.Helper()
	response, err := http.Get(liveLMStudio + "/v1/models")
	if err != nil {
		t.Fatalf("LM Studio at %s: %v", liveLMStudio, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("LM Studio /v1/models: %v\n%s", err, raw)
	}
	out := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		out = append(out, m.ID)
	}
	slices.Sort(out)
	return out
}

// sweepLends takes back every lend the ledger still holds and removes what is
// left of the lend directory, so a failed run leaves the machine as it was.
func sweepLends(t *testing.T, endpoint []string) {
	t.Helper()
	listed, err := runModels(t, append([]string{"lends", "--json"}, endpoint...)...)
	if err != nil {
		t.Logf("sweep: lends: %v\n%s", err, listed)
		return
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(listed), &rows); err != nil {
		t.Logf("sweep: lends json: %v\n%s", err, listed)
		return
	}
	for _, row := range rows {
		id, _ := row["ID"].(string)
		link, _ := row["Link"].(string)
		if id != "" {
			if out, err := runModels(t, append([]string{"unlend", id}, endpoint...)...); err != nil {
				t.Logf("sweep: unlend %s: %v\n%s", id, err, out)
			}
		}
		if link == "" {
			continue
		}
		os.Remove(link)
		for dir := filepath.Dir(link); strings.Contains(dir, LendDirectoryName); dir = filepath.Dir(dir) {
			if os.Remove(dir) != nil {
				break
			}
		}
	}
}

// The smallest GGUF this machine holds outside LM Studio's own store becomes
// an LM Studio model by one link, and stops being one when the lend is taken
// back. Live: it builds the real inventoryd and modelbridged, declares both to
// an isolated runtime, and reads the LM Studio that is already running here.
// Nothing is left in ~/.lmstudio.
func TestLiveLendTheSmallestGGUFToLMStudio(t *testing.T) {
	if os.Getenv("OA_LIVE_LEND") != "1" {
		t.Skip("set OA_LIVE_LEND=1 to lend one of this machine's own model files to the LM Studio running here")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	inventoryd := buildLocalStoresInventoryd(t)
	modelbridged := buildModelbridged(t)
	providerState := t.TempDir()
	stamp := time.Now().UnixNano() % 1_000_000_000

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "120s"}

	stores := []string{"ollama", "huggingface", "lmstudio", "comfyui", "fastflowlm", "jan"}
	add := []string{"add", "local-stores", "--program", inventoryd, "--provider-endpoint", fmt.Sprintf("live-lend-inventory-%d", stamp),
		"--contract", storageInventorySource, "--arg", "serve", "--arg", "--endpoint", "--arg", providerEndpointToken}
	for _, store := range stores {
		add = append(add, "--store", store)
	}
	if out, err := runProvider(t, append(add, endpoint...)...); err != nil {
		t.Fatalf("provider add local-stores: %v\n%s", err, out)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bridge := []string{"add", "modelbridge", "--program", modelbridged, "--provider-endpoint", fmt.Sprintf("live-lend-bridge-%d", stamp),
		"--contract", storageLending, "--arg", "serve", "--arg", "--endpoint", "--arg", providerEndpointToken,
		"--arg", "--state", "--arg", providerState, "--arg", "--runtime-program", "--arg", self}
	if out, err := runProvider(t, append(bridge, endpoint...)...); err != nil {
		t.Fatalf("provider add modelbridge: %v\n%s", err, out)
	}
	eventually(t, 90*time.Second, "the local-stores source being accepted", func() bool {
		state := providerStates(t, endpoint)["local-stores"]
		return state.Readiness == fwire.DeclarationReadinessReady && len(state.Accepted) == len(stores)
	})

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	machine := client.New(options.endpoint)
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	subject := rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}
	permit := func(action, resource string) {
		t.Helper()
		page, err := operator.ListPolicyContext(call, "", 1)
		if err != nil || page.Outcome.String() != "page" {
			t.Fatalf("list policy: %+v %v", page, err)
		}
		rule := rights.PolicyRule{Subject: subject, Action: action, Resource: resource, Permit: true}
		if edit, err := operator.SetRuleContext(call, page.Revision, rule); err != nil || edit.Outcome.String() != "applied" {
			t.Fatalf("permit %s on %s: %+v %v", action, resource, edit, err)
		}
	}
	permit(ActionInventoryRead, storageservice.InventoryResource)
	permit(contentReadAction, storageservice.InventoryResource)

	// The smallest GGUF the inventory lists outside LM Studio's own store.
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
			if m.Manifest.Store == "lmstudio" || len(m.Manifest.Names) == 0 {
				continue
			}
			for _, e := range m.Manifest.Entries {
				// One shard of a split GGUF is no model an engine can load,
				// so it is never the file to measure a lend with.
				if !strings.HasSuffix(strings.ToLower(e.Locator), ".gguf") || e.Size <= 0 || shard.MatchString(e.Locator) {
					continue
				}
				if smallest.size == 0 || e.Size < smallest.size {
					smallest = candidate{store: m.Manifest.Store, id: m.Manifest.ID, name: m.Manifest.Names[0].Name, size: e.Size}
				}
			}
		}
		if page.Complete {
			break
		}
		continuation = page.Continuation
	}
	if smallest.size == 0 {
		t.Skip("this machine holds no GGUF outside LM Studio's own store")
	}
	t.Logf("smallest GGUF: store=%s size=%d name=%s id=%s", smallest.store, smallest.size, smallest.name, smallest.id)

	before := lmStudioModelIDs(t)
	t.Logf("LM Studio before: %v", before)

	// The first lend is refused until the person answers the first-use
	// question. No rule on engine:lmstudio exists yet.
	object := smallest.store + "/" + smallest.id
	out, err := runModels(t, append([]string{"lend", object, "--to", "lmstudio"}, endpoint...)...)
	if err == nil || !strings.Contains(out+err.Error(), "not_permitted") {
		t.Fatalf("the first lend was not refused not_permitted: %v\n%s", err, out)
	}
	t.Logf("first lend, no rule yet: %v", err)

	permit(ActionLend, ResourceEngine("lmstudio"))
	// The provider is on-demand: this call is what launches it, and the
	// runtime waits for its first readiness probe before it forwards.
	out, err = runModels(t, append([]string{"lend", object, "--to", "lmstudio"}, endpoint...)...)
	if err != nil {
		t.Fatalf("lend after the rule: %v\n%s\nmodelbridge: %+v", err, out, providerStates(t, endpoint)["modelbridge"])
	}
	t.Logf("lend: %s", strings.TrimSpace(out))
	// Whatever happens next, this machine is left as it was found: every
	// entry the ledger still holds is taken back and its directories pruned.
	t.Cleanup(func() { sweepLends(t, endpoint) })

	listed, err := runModels(t, append([]string{"lends", "--json"}, endpoint...)...)
	if err != nil {
		t.Fatalf("lends: %v\n%s", err, listed)
	}
	var ledger []struct {
		ID, Engine, Store, Object, Name, Link string
		Kind                                  string
		Size                                  int64
		Created                               string
	}
	if err := json.Unmarshal([]byte(listed), &ledger); err != nil {
		t.Fatalf("lends json: %v\n%s", err, listed)
	}
	if len(ledger) != 1 {
		t.Fatalf("the ledger holds %d entries, wanted one:\n%s", len(ledger), listed)
	}
	entry := ledger[0]
	t.Logf("ledger: entry=%s engine=%s name=%s kind=%s size=%d link=%s", entry.ID, entry.Engine, entry.Name, entry.Kind, entry.Size, entry.Link)
	if _, err := os.Lstat(entry.Link); err != nil {
		t.Fatalf("the link the ledger names is not there: %v", err)
	}

	// The borrowed model appears under the engine's own naming, which is the
	// engine's business: LM Studio prefixes an embedding model with
	// text-embedding-. The test asserts a new id carrying the lent family.
	var after, added []string
	eventually(t, 30*time.Second, "LM Studio listing the lent model", func() bool {
		after = lmStudioModelIDs(t)
		added = nil
		for _, id := range after {
			if !slices.Contains(before, id) {
				added = append(added, id)
			}
		}
		return slices.ContainsFunc(added, func(id string) bool { return strings.Contains(id, entry.Name) })
	})
	t.Logf("LM Studio after the lend: added %v", added)

	out, err = runModels(t, append([]string{"unlend", entry.ID}, endpoint...)...)
	if err != nil {
		t.Fatalf("unlend: %v\n%s", err, out)
	}
	t.Logf("unlend: %s", strings.TrimSpace(out))
	if _, err := os.Lstat(entry.Link); !os.IsNotExist(err) {
		t.Fatalf("the link is still there after unlend: %v", err)
	}
	root := filepath.Join(filepath.Dir(filepath.Dir(entry.Link)))
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("the lend directory %s was not pruned: %v", root, err)
	}
	listed, err = runModels(t, append([]string{"lends", "--json"}, endpoint...)...)
	if err != nil || !strings.Contains(listed, "[]") && !strings.Contains(listed, "null") {
		t.Fatalf("the ledger still holds the entry: %v\n%s", err, listed)
	}
	t.Logf("ledger after unlend: %s", strings.TrimSpace(listed))
	t.Logf("LM Studio right after the unlend: %v", lmStudioModelIDs(t))
}

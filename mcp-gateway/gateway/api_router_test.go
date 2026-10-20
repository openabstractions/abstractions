package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

func TestRouterAPICustomModelsResultRemainsClosedAndUnchanged(t *testing.T) {
	families := []routerwire.Family{{Family: "guide", HeldIn: []string{"store"},
		Names: []routerwire.Alias{{Name: "guide", Servable: true, Profiles: []string{"chat"}}},
		Objects: []routerwire.ModelObject{{Digest: "sha256:raw", Cause: routerwire.EngineCauseBound,
			Why: "display only", Engines: []routerwire.ModelEngine{{Engine: "selected"}}}}}}
	out := ModelsOutput{ObservedUnixMS: 1, Models: modelsFrom(families)}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	// capabilities is the per-alias line of the architect ruling 2026-11-12
	// (model capabilities), added to the accepted result.
	const expected = `{"observed_unix_ms":1,"models":[{"family":"guide","name":"guide","aliases":["guide"],"profiles":["chat"],"servable":true,"held_in":["store"],"capabilities":["local guide: chat"]}]}`
	if string(raw) != expected {
		t.Fatalf("accepted 5cb custom result changed: %s", raw)
	}
	schema, err := outputSchema[ModelsOutput]().Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["models"].([]any)[0].(map[string]any)["objects"] = []any{}
	if err := schema.Validate(value); err == nil {
		t.Fatal("custom Model schema silently opened")
	}
}

func TestRouterAPIReadKeepsNativeGuideAndInventoryAuthority(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its Darwin ceiling")
	}
	dir := t.TempDir()
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), []string{routerservice.ActionInventory})
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	host := router.OpenAICompatible("provider", "http://127.0.0.1:1", "/v1", []string{"guide"})
	host.Engines = []router.Engine{{ID: "selected", Name: "Selected engine", Source: "registered-generation"}}
	host.EngineObjects = []router.ModelObject{{Digest: "sha256:verified", Engines: []router.ModelEngine{{Host: "provider", Model: "guide", Engine: "selected", Profiles: []string{"chat"}, Source: "registered-generation"}}}}
	r := router.New(host)
	r.SetHeld([]router.HeldObject{
		{Store: "store", Names: []string{"verified"}, Digest: "sha256:verified", Evidence: content.EvidenceHashed, Descriptor: map[string]string{"family": "guide"}},
		{Store: "store", Names: []string{"unverified"}, Digest: "sha256:named", Evidence: content.EvidenceNamed, Descriptor: map[string]string{"family": "guide"}},
	})
	r.Survey()
	var decision atomic.Int32
	var calls atomic.Int32
	sink, err := logging.OpenRetainedFileSink(filepath.Join(dir, "logs"), logging.LogsRetention{MaxUse: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	endpoint := completionEndpoint()
	h, err := facaderuntime.Listen(facaderuntime.Options{Endpoint: endpoint, LogEndpoint: completionEndpoint(), ConfigEndpoint: completionEndpoint(), RightsEndpoint: completionEndpoint(), RouterEndpoint: completionEndpoint(), Router: r,
		ConfigWithoutMachine: true, ConfigStore: casapi.BoundedFileStore{MaxBytes: 1 << 20}, ConfigUserKey: filepath.Join(dir, "config.json"), Sink: sink, RightsPolicy: policy,
		RouterPolicy: func(_ context.Context, peer *identity.Peer, action, resource string) error {
			native, err := identity.SubjectOf(peer, listen.Program)
			if err != nil {
				return err
			}
			if native.Account != account.Uid || native.Program != identity.CanonicalProgramPath(exe) || action != routerservice.ActionInventory || resource != routerservice.ResourceInventory {
				return errors.New("foreign inventory authority")
			}
			calls.Add(1)
			if decision.Load() == 1 {
				return errors.New("revoked")
			}
			if decision.Load() == 2 {
				return routerservice.ErrPolicyUnavailable
			}
			return nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("native router fixture did not join")
		}
	})
	oa, err := NewOA(facade.New(endpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, session := connected(t, oa, "untrusted-operator-name", "2026-07-28")
	client, err := oa.machine.ResolveRouter(t.Context(), facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"Models", "Hosts"} {
		t.Run(method, func(t *testing.T) {
			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolAPIRead, Arguments: map[string]any{"contract": facade.RouterContract, "method": method, "arguments": map[string]any{"fresh": false}}})
			if err != nil || result.IsError {
				t.Fatal(result, err)
			}
			assertAppModelUntrusted(t, result)
			var envelope struct {
				Value json.RawMessage `json:"value"`
			}
			if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &envelope); err != nil {
				t.Fatal(err)
			}
			entry, ok := findAPIRead(facade.RouterContract, method)
			if !ok {
				t.Fatal("missing generated projection")
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(entry.ResultSchema, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(result.StructuredContent); err != nil {
				t.Fatal(err)
			}
			if method == "Models" {
				direct, err := client.ModelsContext(t.Context(), false)
				if err != nil {
					t.Fatal(err)
				}
				var projected routerwire.ModelsSnapshot
				if err := routerwire.FromJSON(envelope.Value, &projected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(projected.Models, direct.Models) || len(projected.Models) != 1 || len(projected.Models[0].Objects) != 2 {
					t.Fatal("native Models changed", projected, direct)
				}
				if !strings.Contains(string(envelope.Value), "sha256:verified") {
					t.Fatal("raw native guide lost its digest")
				}
			} else {
				direct, err := client.HostsContext(t.Context(), false)
				if err != nil {
					t.Fatal(err)
				}
				var projected routerwire.HostsSnapshot
				if err := routerwire.FromJSON(envelope.Value, &projected); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(projected.Hosts, direct.Hosts) || len(projected.Hosts) != 1 || len(projected.Hosts[0].Engines) != 1 || projected.Hosts[0].Engines[0].Version != "" {
					t.Fatal("native Hosts changed", projected, direct)
				}
			}
			for _, state := range []int32{1, 2} {
				decision.Store(state)
				refused, err := oa.APIRead(t.Context(), APIReadInput{Contract: facade.RouterContract, Method: method, Arguments: json.RawMessage(`{"fresh":false}`)})
				word := "forbidden"
				if state == 2 {
					word = "unavailable"
				}
				if err != nil || !refused.IsError || !strings.Contains(string(refused.JSON), word) || strings.Contains(string(refused.JSON), "registered-generation") {
					t.Fatal("inventory refusal disclosed guide", string(refused.JSON), err)
				}
			}
			decision.Store(0)
		})
	}
	if calls.Load() < 8 {
		t.Fatal("inventory policy bypassed", calls.Load())
	}
}

func TestRouterAPIReadSelectionSchemasAndBounds(t *testing.T) {
	for _, method := range []string{"Pick", "Offer", "ObserveOffer", "Modalities"} {
		if _, ok := findAPIRead(facade.RouterContract, method); ok {
			t.Fatal("unaccepted method exposed", method)
		}
	}
	for _, method := range []string{"Models", "Hosts"} {
		entry, ok := findAPIRead(facade.RouterContract, method)
		if !ok {
			t.Fatal(method)
		}
		if _, err := entry.invoke(nil, facade.RouterContract, method, []byte(`{"fresh":false,"subject":"operator"}`)); err == nil {
			t.Fatal("caller authority argument accepted")
		}
		if _, err := (&OA{}).APIRead(t.Context(), APIReadInput{Contract: facade.RouterContract, Method: method, Arguments: json.RawMessage(strings.Repeat(" ", (1<<20)+1))}); err == nil {
			t.Fatal("oversize arguments accepted")
		}
	}
	if result, err := apiResult(APIReadResult{JSON: []byte(strings.Repeat(" ", (1<<20)+1))}); err == nil || result != nil {
		t.Fatal("oversize answer yielded partial output")
	}
}

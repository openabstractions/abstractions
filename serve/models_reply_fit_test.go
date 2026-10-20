package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	router "github.com/openabstractions/abstraction-router/go"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// One provider whose engine facts carry a source over router@1's bound
// loses those facts in the Models and Hosts replies read through the native
// endpoint. Every host, every model and the other provider's engine facts
// are still listed.
func TestModelsReplyThroughTheEndpointDropsOneBadEngine(t *testing.T) {
	facet := func(model string) *registration.ProviderFacet {
		return &registration.ProviderFacet{Engines: []registration.EngineFact{{ID: "llama.cpp", Name: "llama.cpp", Version: "b6000"}},
			Recipes: []registration.ModelRecipe{{Model: model, Engine: "llama.cpp", Modalities: []string{router.ProfileChat},
				Objects: []registration.RecipeObject{{Role: "weights", Digest: fixtureDigest(model)}}}}}
	}
	good, err := router.NewNative("modelhost", "fixture", listen.ServerExpectation{}, []string{"chat:family-000"}, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	goodFile := providerFile{Generation: "good", Declaration: providerDeclaration{Name: "modelhost", Program: `C:\OA\tools\modelhostd.exe`}, ProviderFacet: facet("family-000")}
	good.BindingID = goodFile.bindingID()
	setProviderEngineGuide(good, goodFile)
	bad, err := router.NewNative("other", "fixture", listen.ServerExpectation{}, []string{"chat:family-001"}, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	projectProviderEngineGuide(bad, facet("family-001"), strings.Repeat("s", 300))

	routes := router.New(good, bad)
	routes.SetHeld(heldObjects(largeHeldInventory(1, 4)))
	routes.Survey()
	endpoint := listen.Endpoint(fmt.Sprintf("models-reply-fit-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	h, err := routerservice.Listen(endpoint, routes)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := h.EnablePolicy(func(context.Context, *identity.Peer, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() { cancel(); <-done }()

	client := routerclient.New(endpoint)
	models, err := client.Models(true)
	if err != nil {
		t.Fatalf("Models with one bad engine source: %v", err)
	}
	engines := map[string]int{}
	families := map[string]bool{}
	for _, family := range models.Models {
		families[family.Family] = true
		for _, object := range family.Objects {
			for _, engine := range object.Engines {
				if len(engine.Source) > 256 {
					t.Fatalf("an over-bound source reached the client: %d bytes", len(engine.Source))
				}
				engines[engine.Host]++
			}
		}
	}
	if engines["modelhost"] == 0 || engines["other"] != 0 {
		t.Fatalf("model engines by host: %v", engines)
	}
	hosts, err := client.Hosts(true)
	if err != nil {
		t.Fatalf("Hosts with one bad engine source: %v", err)
	}
	listed := map[string]int{}
	for _, host := range hosts.Hosts {
		listed[host.Host] = len(host.Engines)
	}
	if got, ok := listed["modelhost"]; !ok || got != 1 {
		t.Fatalf("good host engines: %v", listed)
	}
	if got, ok := listed["other"]; !ok || got != 0 {
		t.Fatalf("bad host is listed without its engine: %v", listed)
	}
	t.Logf("families %d, model engines %v, host engines %v", len(families), engines, listed)
}

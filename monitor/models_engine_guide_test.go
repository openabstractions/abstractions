package main

import (
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	rclient "github.com/openabstractions/abstraction-router/go/client"
)

func TestModelsEngineGuideProjectsPublicAnswerWithoutDigests(t *testing.T) {
	const digest = "sha256:content-identity-is-not-page-text"
	const explanation = "Configured by the provider; byte verification and readiness are reported separately."
	catalog := rclient.ModelsSnapshot{Models: []wire.Family{{Family: "picture", Title: "A picture model", HeldIn: []string{"source"}, Objects: []wire.ModelObject{
		{Digest: digest, Cause: wire.EngineCauseBound, Why: explanation, Engines: []wire.ModelEngine{{Host: "lab/provider", Engine: "remote-selected-engine"}}},
		{Digest: digest, Cause: wire.EngineCauseUnknown, Why: "A configured companion has not been observed."},
	}}}}
	models, _ := projectLibrary(catalog, libraryFiles{})
	if len(models) != 1 || len(models[0].EngineGuide) != 3 || models[0].EngineGuide[0] != "lab/provider / remote-selected-engine" ||
		models[0].EngineGuide[1] != explanation || models[0].EngineGuide[2] != "A configured companion has not been observed." {
		t.Fatalf("public answer changed: %+v", models)
	}
	if strings.Contains(strings.Join(models[0].EngineGuide, " "), digest) {
		t.Fatal("digest reached page text")
	}
	if models[0].Title != "A picture model" {
		t.Fatal("engine guide replaced the existing catalogue title")
	}
	if len(models[0].RunsOn) != 1 || models[0].RunsOn[0] != "remote-selected-engine" {
		t.Fatalf("the caption's engines are %q", models[0].RunsOn)
	}
}

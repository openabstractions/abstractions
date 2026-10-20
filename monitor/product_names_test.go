package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// Each engine provider reads by its one upstream name (product.EngineTitle)
// in Go's providerDisplay and in the pages' oaEngineName (architect ruling
// 2026-11-09): llama.cpp, Diffusers, Lemonade, TheNoise.
func TestEngineProvidersReadAlikeInGoAndThePages(t *testing.T) {
	titles := product.EngineTitles()
	for name, want := range titles {
		if got, _ := providerDisplay(name, true); got != want {
			t.Errorf("providerDisplay(%q) = %q, want %q", name, got, want)
		}
	}
	t.Run("emitted browser labels", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Skip("node is unavailable")
		}
		expected, err := json.Marshal(titles)
		if err != nil {
			t.Fatal(err)
		}
		emitted := strings.ReplaceAll(panelProductTitleScript+plainScript, "</script><script>", "\n")
		emitted = strings.TrimSuffix(strings.TrimPrefix(emitted, "<script>"), "</script>")
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, node, "-e", panelProductTitleFixture)
		command.Stdin = strings.NewReader(emitted + "\nvar expectedEngineTitles=" + string(expected) + ";")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("emitted engine and resource labels: %v\n%s", err, output)
		}
	})
}

const panelProductTitleFixture = `
const assert = require('node:assert/strict');
const vm = require('node:vm');
const page = {};
vm.runInNewContext(require('node:fs').readFileSync(0, 'utf8'), page);
assert.equal(Object.keys(page.oaEngineTitles).length, 30);
assert.equal(Object.keys(page.oaResourceTitles).length, 12);
assert.equal(Object.hasOwn(page.oaEngineTitles, 'hf'), false);
assert.equal(Object.hasOwn(page.oaResourceTitles, 'lm-studio'), false);
for (const [name, want] of Object.entries(page.expectedEngineTitles)) {
	assert.equal(page.oaEngineName(name), want, name);
}
for (const [name, want] of [
	['llama.cpp', 'llama.cpp'], ['llamacpp', 'llama.cpp'], ['modelhostd', 'llama.cpp'],
	['pytorchd', 'Diffusers'], ['diffusers', 'Diffusers'], ['lemonade', 'Lemonade'],
	['thenoise', 'TheNoise'], ['comfy-image-1828', 'ComfyUI'], ['  PYTORCHD-abcd ', 'Diffusers'],
	['lm-studio', 'LM Studio'], ['hf', 'Hf'],
	['', 'Unnamed engine'], [null, 'Unnamed engine'], ['new-engine', 'New engine'],
]) {
	assert.equal(page.oaEngineName(name), want, String(name));
}
for (const [name, want] of [
	['store:modelhost-abcd', 'llama.cpp models'], ['lemonade', 'Lemonade'],
	['store:comfyui', 'ComfyUI models'], ['hf', 'Hugging Face'],
	['thenoise-image', 'Thenoise image'], ['store:lm-studio', 'Lm studio models'],
	['store:new-app', 'New app models'], ['', ''],
]) {
	assert.equal(page.oaResourceLabel(name), want, name);
}
`

// The Setup bridges and the picture permission name the providers by
// product's names.
func TestSetupNamesTheProductProviders(t *testing.T) {
	if pictureEngineHost != product.Lemonade {
		t.Fatalf("pictureEngineHost = %q, want %q", pictureEngineHost, product.Lemonade)
	}
}

// A model call's server reads by its registered app, else by the engine
// OpenAbstractions ships under that name, else "Model server".
func TestEngineServerTitleNamesShippedEnginesOnly(t *testing.T) {
	for host, want := range map[string]string{"modelhost": "llama.cpp", "pytorch": "Diffusers", "pytorch-image": "Diffusers", "lemonade-image": "Lemonade", "thenoise-image": "TheNoise", "10.0.0.7:8080": "Model server", "local-http": "Model server", "": "Model server"} {
		if got := engineServerTitle(host, true); got != want {
			t.Errorf("engineServerTitle(%q) = %q, want %q", host, got, want)
		}
		if got := engineServerTitle(host, false); got != "Model server" {
			t.Errorf("an unshipped %q reads %q", host, got)
		}
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

func TestAgentReleaseProductionCatalogueCanonical(t *testing.T) {
	tools, err := surfaceCatalogue(t.Context(), "agent-release")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := agentRelease(tools, "stable", "0.8.0.0")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if len(document) != 8 || string(document["requests"]) != "[]" || string(document["verbs"]) != "[]" || string(document["release"]) != "[0,8,0,0]" {
		t.Fatal(string(raw))
	}
	needs, err := registration.DecodeNeeds(document["needs"])
	if err != nil {
		t.Fatal(err)
	}
	if len(needs.Files) != 0 || len(needs.Models) != 3 || needs.Models[0].Modality != "chat" || needs.Models[1].Modality != "image" || needs.Models[2].Modality != "video" {
		t.Fatal("helper setup declaration", needs)
	}
	for _, need := range needs.Models {
		if need.Placement != registration.PlacementAny || need.MinimumContext != 0 || need.Reserve != 0 || len(need.RequiredFeatures) != 0 {
			t.Fatal("helper need widens its minimal setup declaration", need)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatal(err)
	}
	canonical, _ := json.Marshal(value)
	if !bytes.Equal(raw, canonical) {
		t.Fatal("resource differs from native canonical JSON")
	}
	var listed []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(document["tools"], &listed); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range listed {
		names[tool.Name] = true
	}
	for _, name := range []string{"oa_asks_request_answering", "oa_messages_prepare", "oa_messages_reply", "oa_messages_wait", "oa_models_list", "oa_setup_fit"} {
		if !names[name] {
			t.Errorf("full production tool absent: %s", name)
		}
	}
	again, err := agentRelease(tools, "stable", "0.8.0.0")
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("release resource is nondeterministic", err)
	}
	t.Logf("full production agent release: %d tools, %d compact bytes", len(listed), len(raw))
}

func TestAgentReleaseRequiresExplicitBoundedInputs(t *testing.T) {
	tools := []byte(`{"tools":[{"name":"fixture","inputSchema":{"type":"object"}}]}`)
	for _, pair := range [][2]string{{"", "0.8.0.0"}, {"stable", ""}, {"Stable", "0.8.0.0"}, {"stable", "0.8.0"}, {"stable", "0.8.0.65536"}, {"stable", "0.08.0.1"}, {"stable", "0.0.0.0"}, {strings.Repeat("s", 33), "0.8.0.0"}} {
		if _, err := agentRelease(tools, pair[0], pair[1]); err == nil {
			t.Fatal("invalid release admitted", pair)
		}
	}
	for _, raw := range []string{`{"tools":[]}`, `{"tools":[{"name":"fixture"}]}`, `{"tools":[{"name":"fixture","inputSchema":{}},{"name":"fixture","inputSchema":{}}]}`} {
		if _, err := agentRelease([]byte(raw), "stable", "0.8.0.1"); err == nil {
			t.Fatal("invalid declaration admitted", raw)
		}
	}
}

func TestAgentReleaseCLIRequiresPairAndKeepsResourceCanonical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "release.json")
	args := []string{"--out", path, "--surface", "agent-release", "--release-channel", "stable", "--release-version", "0.8.0.0"}
	if err := run(args, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run(append(args, "--check"), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]string{
		{"--out", path, "--surface", "agent-release", "--release-channel", "stable"},
		{"--out", path, "--surface", "messages", "--release-channel", "stable", "--release-version", "0.8.0.0"},
	} {
		if err := run(invalid, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid flags admitted", invalid)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("invalid producer input changed artifact", err)
		}
	}
}

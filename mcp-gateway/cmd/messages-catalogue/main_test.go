package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMessagesHostToolsMatchNativeDefinitionsWithoutChangingReceiverRoster(t *testing.T) {
	data, err := catalogue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var document struct{ Tools, HostTools []*mcp.Tool }
	// Decode the optional metadata separately: existing consumers decoding only
	// tools/instructions continue to receive the original six-tool projection.
	var wire struct {
		Tools     []*mcp.Tool `json:"tools"`
		HostTools []*mcp.Tool `json:"host_tools"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	document.Tools, document.HostTools = wire.Tools, wire.HostTools
	if len(document.Tools) != 6 || len(document.HostTools) != 3 {
		t.Fatal(len(document.Tools), len(document.HostTools))
	}
	full, _, err := metadataTools(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	for _, projected := range document.HostTools {
		if projected.Name != "oa_messages_request_access" && projected.Name != "oa_applications_list" && projected.Name != "oa_models_connect" {
			t.Fatal(projected.Name)
		}
		found := false
		for _, native := range full {
			if native.Name == projected.Name {
				a, _ := json.Marshal(native)
				b, _ := json.Marshal(projected)
				if !reflect.DeepEqual(a, b) {
					t.Fatal("host definition drift", projected.Name)
				}
				found = true
			}
		}
		if !found {
			t.Fatal(projected.Name)
		}
	}
}

func TestMessagesCatalogueFresh(t *testing.T) {
	data, err := catalogue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "..", "adopters", "opencode", "generated", "messages-tools.json")
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, data) {
		t.Fatal("Messages catalogue drift: go -C mcp-gateway run ./cmd/messages-catalogue --out ../adopters/opencode/generated/messages-tools.json")
	}
}

func TestAgentCatalogueFresh(t *testing.T) {
	data, err := surfaceCatalogue(t.Context(), "agent")
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join("..", "..", "..", "adopters", "opencode", "generated", "agent-tools.json"))
	if err != nil || !bytes.Equal(current, data) {
		t.Fatal("native agent catalogue drift", err)
	}
}

func TestMessagesCatalogueCarriesReceiverInstructions(t *testing.T) {
	data, err := catalogue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Instructions string `json:"instructions"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"ping or message Claude", "oa_messages_prepare", "never look them up as models",
		"OA accepts it only for a question addressed to this chat that is still open",
		"If an OA wake prompt includes a ticket, pass it to oa_messages_wait as wake_ticket",
		"In 'OA: another message (...)', the code in parentheses is the ticket",
	} {
		if !strings.Contains(document.Instructions, text) {
			t.Fatal(document.Instructions)
		}
	}
}

func TestMessagesCatalogueDirectsAgentDiscoveryToPermittedConversations(t *testing.T) {
	data, err := catalogue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Instructions string      `json:"instructions"`
		Tools        []*mcp.Tool `json:"tools"`
		HostTools    []*mcp.Tool `json:"host_tools"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"oa_messages_inboxes_list", "needs_permission", "program and title", "oa_messages_request_access", "exact app:<name>", "When inboxes, other_chats and needs_permission are all empty, no agent can be messaged right now."} {
		if !strings.Contains(document.Instructions, text) {
			t.Fatalf("missing discovery guidance %q", text)
		}
	}
	for _, tool := range append(document.Tools, document.HostTools...) {
		if tool.Name == "oa_messages_inboxes_list" {
			raw, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			branches, ok := schema["anyOf"].([]any)
			if !ok || len(branches) != 2 {
				t.Fatal("native result lost its existing permission-result wrapper", schema)
			}
			schema = branches[0].(map[string]any)
			field := schema["properties"].(map[string]any)["needs_permission"].(map[string]any)
			item := field["items"].(map[string]any)
			properties := item["properties"].(map[string]any)
			// Listed output schemas stay open to fields a later OA adds
			// (gateway openOutputSchemas); the entry names program and title.
			if field["maxItems"] != float64(64) || len(properties) != 2 ||
				properties["program"].(map[string]any)["maxLength"] != float64(68) || properties["title"].(map[string]any)["maxLength"] != float64(800) {
				t.Fatal("discovery schema exposes more than bounded program/title", field)
			}
		}
		switch tool.Name {
		case "oa_messages_request_access", "oa_messages_inboxes_list":
			if strings.Contains(tool.Description, "oa_applications_list") || !strings.Contains(tool.Description, "needs_permission") || !strings.Contains(tool.Description, "app:<name>") {
				t.Fatalf("wrong receiver discovery guidance: %s", tool.Name)
			}
		case "oa_applications_list":
			if !strings.Contains(tool.Description, "oa_messages_inboxes_list") || !strings.Contains(tool.Description, "may read") {
				t.Fatal("application directory lost its read boundary")
			}
		}
	}
}

func TestMessagesCatalogueEmptyDiscoveryGuidance(t *testing.T) {
	sentences := []string{
		"When inboxes, other_chats and needs_permission are all empty, no agent can be messaged right now.",
		"oa_messages_prepare checks reach or permission first and sends nothing; its outcome gives the next step.",
	}
	for _, name := range []string{
		"adopters/opencode/generated/messages-tools.json",
		"mcp-gateway/claude-plugin/hooks/messages-tools.json",
	} {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		for _, sentence := range sentences {
			if !strings.Contains(string(data), sentence) {
				t.Fatalf("%s lacks %q", name, sentence)
			}
		}
	}
	for _, surface := range []string{"messages", "agent"} {
		data, err := surfaceCatalogue(t.Context(), surface)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Instructions string      `json:"instructions"`
			Tools        []*mcp.Tool `json:"tools"`
		}
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		texts := []string{document.Instructions}
		for _, tool := range document.Tools {
			texts = append(texts, tool.Description)
		}
		for _, text := range texts {
			for _, sentence := range strings.Split(text, ". ") {
				if lower := strings.ToLower(sentence); (strings.Contains(lower, "empty list") || strings.Contains(lower, "lists are empty")) && strings.Contains(lower, "installed") {
					t.Fatalf("empty discovery asserts installation state: %s", sentence)
				}
			}
		}
		for _, sentence := range sentences {
			if !strings.Contains(document.Instructions, sentence) {
				t.Fatalf("%s instructions lack %q", surface, sentence)
			}
		}
	}
}

func TestAsksCatalogueFresh(t *testing.T) {
	data, err := surfaceCatalogue(t.Context(), "asks")
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join("..", "..", "..", "adopters", "opencode", "generated", "asks-tools.json"))
	if err != nil || !bytes.Equal(current, data) {
		t.Fatal("Asks catalogue drift", err)
	}
}

func TestMessagesCatalogueCheckRefusesDriftWithoutWriting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	if err := run([]string{"--out", path}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--out", path, "--check"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	stale := []byte("{\"tools\":[]}\n")
	if err := os.WriteFile(path, stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"--out", path, "--check"}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "stale Messages catalogue") {
		t.Fatal(err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, stale) {
		t.Fatal("--check wrote the asset", err)
	}
}

func TestMessagesHookCatalogueFresh(t *testing.T) {
	data, err := catalogue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(filepath.Join("..", "..", "claude-plugin", "hooks", "messages-tools.json"))
	if err != nil || !bytes.Equal(current, data) {
		t.Fatal("compiled Claude wake catalogue drift", err)
	}
}

// --help and no arguments print the usage to stdout and succeed; a usage
// error still goes to stderr and fails.
func TestHelpPrintsUsageToStdoutAndSucceeds(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {}, {"--surface", "agent", "--help"}} {
		var stdout, stderr bytes.Buffer
		if err := run(args, &stdout, &stderr); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(stdout.String(), "Usage: messages-catalogue") || !strings.Contains(stdout.String(), "-surface") || stderr.Len() != 0 {
			t.Fatalf("%v: stdout %q stderr %q", args, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run([]string{"--bogus"}, &stdout, &stderr); err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage: messages-catalogue") {
		t.Fatalf("usage error: %v stdout %q stderr %q", err, stdout.String(), stderr.String())
	}
}

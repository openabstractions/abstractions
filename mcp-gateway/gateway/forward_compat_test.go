package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

// Owner's PC, 2026-11-12: a Claude Code session started before the R32
// upgrade listed oa_models_list with additionalProperties false, and R32's
// setup_needed, setup_use, download_bytes and preset_times failed every later
// call on the host ("data/models/18 must NOT have additional properties").
// Every listed output schema now admits a field a later OA adds.
func TestListedOutputSchemasAdmitFieldsALaterOAAdds(t *testing.T) {
	b := &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}
	_, cs := connected(t, b, "claimed-host", "2025-11-25")
	listed, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	for _, tool := range listed.Tools {
		if tool.OutputSchema == nil {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"additionalProperties":false`) {
			t.Fatalf("%s lists a closed output schema: %s", tool.Name, raw)
		}
		if tool.Name != ToolModels {
			continue
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			t.Fatal(err)
		}
		// What this session's host holds against a result of a later OA.
		_, out := call[map[string]any](t, cs, ToolModels, map[string]any{})
		out["a_later_field"] = true
		models, _ := out["models"].([]any)
		if len(models) == 0 {
			t.Fatal("fixture lists no model", out)
		}
		models[0].(map[string]any)["a_later_model_field"] = "x"
		var instance any
		encoded, _ := json.Marshal(out)
		if err := json.Unmarshal(encoded, &instance); err != nil {
			t.Fatal(err)
		}
		if err := resolved.Validate(instance); err != nil {
			t.Fatal("the listed schema refuses an added field", err)
		}
		checked = true
	}
	if !checked {
		t.Fatal("oa_models_list lists no output schema")
	}
}

// Owner's PC, 2026-11-12: the model host was declared and disabled
// (program:changed after the R32 upgrade, or waiting for Keep), and chat
// answered "isn't set up on this computer". A model whose provider waits for
// the person says so, and names the OpenAbstractions question.
func TestRefusalForAWaitingProviderNamesThePersonsQuestion(t *testing.T) {
	ctx := context.Background()
	for word, want := range map[string]string{
		"program:changed":      "qwen3.6-35b-a3b is set up. The program that runs it was updated and waits for the person's OK in the OpenAbstractions question. It starts once they allow it.",
		"approval:pending":     "qwen3.6-35b-a3b is set up and waits for the person to allow the model host change in the OpenAbstractions question. It starts once they allow it.",
		"host:program:changed": "qwen3.6-35b-a3b is set up. The program that runs it was updated and waits for the person's OK in the OpenAbstractions question. It starts once they allow it.",
	} {
		if got := refusalSentence(ctx, word, "qwen3.6-35b-a3b", "chat"); got != want {
			t.Fatalf("%s: %q", word, got)
		}
	}
	picture := pictureRefusal("z-image-turbo", JobSubmitOutput{Outcome: "invalid", Reason: "host:program:changed"})
	if !strings.Contains(picture, "was updated and waits for the person's OK") {
		t.Fatalf("picture refusal %q", picture)
	}
}

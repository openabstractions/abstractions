package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	config "github.com/openabstractions/abstraction-config/go"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestPanelTypedSettingsRenderedScript(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable")
	}
	command := exec.Command(node, "config_typed_page_test.js")
	command.Env = append(os.Environ(), "OA_TYPED_SETTINGS_SCRIPT="+typedSettingsScript)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("rendered typed settings script: %v\n%s", err, output)
	}
}

func TestPanelTypedConfigConditionalControls(t *testing.T) {
	own(t)
	if err := os.MkdirAll(filepath.Dir(config.UserPath()), 0700); err != nil {
		t.Fatal(err)
	}
	panelConfigRuntimeWith(t, func(options *host.Options) {
		options.ConfigTyped.ReadPolicy = func(context.Context, *identity.Peer, string) error { return nil }
		options.ConfigTyped.DynamicDefault = func(string) (*wire.Value, error) { return &wire.Value{Kind: wire.ValueKindBytes, Bytes: 4096}, nil }
	})
	handler := (&servicePanel{}).handler("test-key")
	var catalogue wire.DescribeResult
	result := panelRequest(t, handler, "/settings/typed?action=describe", nil)
	if result.Code != 200 || wire.FromJSON(result.Body.Bytes(), &catalogue) != nil || catalogue.Outcome != wire.ReadOutcomeRead || len(catalogue.Descriptors) != len(wire.ConfigKeyDescriptors()) {
		t.Fatalf("catalogue: %d %s", result.Code, result.Body)
	}
	var user wire.UserReadResultV2
	result = panelRequest(t, handler, "/settings/typed?action=user", nil)
	if wire.FromJSON(result.Body.Bytes(), &user) != nil || user.Snapshot == nil {
		t.Fatalf("user: %s", result.Body)
	}
	request := struct {
		Revision string
		Changes  []json.RawMessage
	}{user.Snapshot.Revision, []json.RawMessage{json.RawMessage(`{"key":"logs_max_retention","set":{"kind":"duration","duration_ms":1000}}`)}}
	var applied wire.UserReplaceResultV2
	result = panelRequest(t, handler, "/settings/typed", request)
	if wire.FromJSON(result.Body.Bytes(), &applied) != nil || applied.Outcome != wire.ReplaceOutcomeApplied {
		t.Fatalf("apply: %s", result.Body)
	}
	result = panelRequest(t, handler, "/settings/typed", request)
	var conflict wire.UserReplaceResultV2
	if wire.FromJSON(result.Body.Bytes(), &conflict) != nil || conflict.Outcome != wire.ReplaceOutcomeConflict {
		t.Fatalf("conflict: %s", result.Body)
	}
	var effective wire.ReadResult
	result = panelRequest(t, handler, "/settings/typed?action=read", nil)
	if wire.FromJSON(result.Body.Bytes(), &effective) != nil || effective.Outcome != wire.ReadOutcomeRead {
		t.Fatalf("read: %s", result.Body)
	}
	found := false
	for _, entry := range effective.Entries {
		if entry.Key == "logs_max_retention" {
			found = entry.Value != nil && entry.Value.DurationMs == 1000 && entry.Origin.Location == wire.LocationUser
		}
	}
	if !found {
		t.Fatalf("saved effective value missing: %+v", effective)
	}
	var observation wire.ConfigObservationV2
	result = panelRequest(t, handler, "/settings/typed?action=observe", nil)
	if wire.FromJSON(result.Body.Bytes(), &observation) != nil || observation.Outcome != wire.ObservationOutcomeV2Changed {
		t.Fatalf("observe: %s", result.Body)
	}
}

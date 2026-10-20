package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestSetupOwnDownloadCarriesPlanContentAndRehostsUnpinnedOnce(t *testing.T) {
	plan := setup.CodingModelPlan()
	pin := plan.Assets[0].Download
	want := "sha256:" + pin.SHA256
	root := modelHostRootState{ModelHostChatRoot: setup.ModelHostChatRoot{Store: "oa-chat-fixture"},
		Models: []modelHostChatModel{{ID: "download-result", Name: pin.SHA256 + filepath.Ext(plan.Assets[0].Path),
			Digest: "sha256:" + strings.Repeat("b", 64), Hosted: true}}}
	content := ""
	p := &servicePanel{modelHostOA: func(_ context.Context, args ...string) ([]byte, error) {
		if !slices.Equal(args, []string{"models", "hosts", "--json", "--timeout", "30s"}) {
			t.Fatalf("unexpected command %v", args)
		}
		return json.Marshal([]map[string]string{{"store": root.Store, "object": "download-result", "name": "coding", "content": content}})
	}}
	writes := 0
	host := func(model modelHostChatModel, digest string) error {
		writes++
		if model.ID != "download-result" || digest != want {
			t.Fatalf("observed evidence supplied the pin: %s %s", model.ID, digest)
		}
		args := modelHostHostArgs(root.Store, model.ID, "engine", "provider", plan.EngineArgs, "coding", digest)
		i := slices.Index(args, "--content")
		if i < 0 || args[i+1] != want {
			t.Fatalf("download's command lost the plan pin: %v", args)
		}
		content = digest + ":" + strconv.FormatInt(pin.Bytes, 10)
		return nil
	}
	for i := 0; i < 2; i++ {
		if err := stepHostDownloaded(context.Background(), plan, root, setup.StepHosting{Name: "coding"}, p.stepHostedContent, host); err != nil {
			t.Fatal(err)
		}
	}
	if writes != 1 {
		t.Fatalf("already-hosted own download was hosted %d times", writes)
	}
	adopted := modelHostHostArgs("external", "adopted", "engine", "provider", plan.EngineArgs, "coding")
	if slices.Contains(adopted, "--content") {
		t.Fatalf("adopted copy received an own-download pin: %v", adopted)
	}
}

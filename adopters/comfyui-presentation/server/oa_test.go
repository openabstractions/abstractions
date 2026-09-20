package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

type fakeApplications struct {
	page       facade.ApplicationPage
	activation facade.ApplicationActivationResult
	observed   int
	activated  int
}

func (f *fakeApplications) Observe(context.Context, string, int64) (facade.ApplicationPage, error) {
	f.observed++
	return f.page, nil
}
func (f *fakeApplications) Activate(context.Context, string) (facade.ApplicationActivationResult, error) {
	f.activated++
	return f.activation, nil
}

type fakeRights struct {
	outcome rightswire.DecisionOutcome
	calls   []string
}

func (f *fakeRights) DecideContext(_ context.Context, action, resource string) (rights.Decision, error) {
	f.calls = append(f.calls, action+" "+resource)
	return rights.Decision{Outcome: f.outcome, PolicyRevision: "policy-1"}, nil
}

type fakeLog struct {
	records []map[string]string
	err     error
}

func (f *fakeLog) LogContext(_ context.Context, _ int64, _ string, attrs map[string]string) error {
	copy := map[string]string{}
	for k, v := range attrs {
		copy[k] = v
	}
	f.records = append(f.records, copy)
	return f.err
}

func oaFixture(t *testing.T) (*oaAdapter, *fakeApplications, *fakeRights, *fakeLog, *[]string) {
	t.Helper()
	kinds := []string{}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in command
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		kinds = append(kinds, in.Kind)
		switch in.Kind {
		case "context":
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"application_instance": "oa-instance", "instance": "browser-page", "context": "workflow", "revision": "9"}})
		case "preview":
			arguments := in.Arguments.(map[string]any)
			if arguments["application_instance"] != "oa-instance" || arguments["instance"] != "browser-page" || arguments["context"] != "workflow" || arguments["revision"] != "9" {
				t.Errorf("OA preview bridge arguments = %#v", arguments)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"outcome": "proposed", "token": "proposal", "instance": "browser-page", "context": "workflow", "revision": "9"}})
		case "read":
			arguments := in.Arguments.(map[string]any)
			if arguments["application_instance"] != "oa-instance" || arguments["instance"] != "browser-page" || arguments["context"] != "workflow" || arguments["revision"] != "9" || arguments["operation"] != "op-1" {
				t.Errorf("OA read bridge arguments = %#v", arguments)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"outcome": "applied", "operation": "op-1", "instance": "browser-page", "context": "workflow", "revision": "9", "actual": 24}})
		}
	}))
	t.Cleanup(h.Close)
	apps := &fakeApplications{page: facade.ApplicationPage{Outcome: facade.ApplicationOutcomePage, Applications: []facade.ApplicationEntry{{
		Descriptor: facade.ApplicationDescriptor{Name: comfyApplication},
		Instances:  []facade.ApplicationInstance{{Instance: "oa-instance", Interfaces: []facade.ApplicationInterface{presentationInterface}, Contexts: []facade.ApplicationContext{{Name: "workflow", Revision: "9"}}}},
	}}}, activation: facade.ApplicationActivationResult{Outcome: facade.ApplicationActivationOutcomeReady, Instance: "oa-instance"}}
	decisions := &fakeRights{outcome: rightswire.DecisionOutcomePermitted}
	logs := &fakeLog{}
	return newOAAdapter(&bridgeClient{endpoint: h.URL, token: "fixture", client: h.Client()}, apps, decisions, logs), apps, decisions, logs, &kinds
}

func validOAPreview() oaPreviewInput {
	return oaPreviewInput{ApplicationInstance: "oa-instance", Instance: "browser-page", Context: "workflow", Revision: "9", NodeID: 1, Widget: "steps", Value: 24}
}

func validOARead() oaReadInput {
	return oaReadInput{ApplicationInstance: "oa-instance", Instance: "browser-page", Context: "workflow", Revision: "9", Operation: "op-1"}
}

func TestOADenialHasNoBridgeOrDirectoryEffect(t *testing.T) {
	a, apps, decisions, logs, kinds := oaFixture(t)
	decisions.outcome = rightswire.DecisionOutcomeNotGranted
	result := a.preview(context.Background(), validOAPreview())
	if result["outcome"] != "forbidden" || len(*kinds) != 0 || apps.observed != 0 {
		t.Fatalf("denial result=%v bridge=%v observed=%d", result, *kinds, apps.observed)
	}
	if len(logs.records) != 1 || logs.records[0]["oa.outcome"] != "not_granted" {
		t.Fatalf("denial audit = %#v", logs.records)
	}
}

func TestOARightsUnavailableRemainsTypedAndHasNoEffect(t *testing.T) {
	a, apps, decisions, _, kinds := oaFixture(t)
	decisions.outcome = rightswire.DecisionOutcomeUnavailable
	result := a.preview(context.Background(), validOAPreview())
	if result["outcome"] != "unavailable" || result["reason"] != "unavailable" || len(*kinds) != 0 || apps.observed != 0 {
		t.Fatalf("unavailable result=%v bridge=%v observed=%d", result, *kinds, apps.observed)
	}
}

func TestOAPreviewRequiresBridgeAndDirectoryBindingThenAudits(t *testing.T) {
	a, apps, _, logs, kinds := oaFixture(t)
	result := a.preview(context.Background(), validOAPreview())
	if result["outcome"] != "proposed" || len(*kinds) != 2 || (*kinds)[0] != "context" || (*kinds)[1] != "preview" || apps.observed != 1 {
		t.Fatalf("preview result=%v bridge=%v observed=%d", result, *kinds, apps.observed)
	}
	if _, exposed := result["token"]; exposed {
		t.Fatalf("proposal effect handle exposed to MCP: %v", result)
	}
	if result["application_instance"] != "oa-instance" {
		t.Fatalf("verified OA application binding omitted: %v", result)
	}
	if len(logs.records) != 1 || logs.records[0]["oa.application_instance"] != "oa-instance" || logs.records[0]["oa.context"] != "workflow" || logs.records[0]["oa.revision"] != "9" {
		t.Fatalf("preview audit = %#v", logs.records)
	}
	read := a.readOutcome(context.Background(), validOARead())
	if read["outcome"] != "applied" || len(*kinds) != 4 || (*kinds)[2] != "context" || (*kinds)[3] != "read" {
		t.Fatalf("read result=%v bridge=%v", read, *kinds)
	}
}

func TestOAStaleAndUnauditedPreviewNeverReachEffect(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*oaAdapter, *fakeApplications, *fakeLog, *oaPreviewInput)
		want string
	}{
		{"caller binding", func(_ *oaAdapter, _ *fakeApplications, _ *fakeLog, in *oaPreviewInput) { in.Revision = "8" }, "stale"},
		{"directory binding", func(_ *oaAdapter, apps *fakeApplications, _ *fakeLog, _ *oaPreviewInput) {
			apps.page.Applications[0].Instances[0].Contexts[0].Revision = "8"
		}, "stale"},
		{"audit", func(_ *oaAdapter, _ *fakeApplications, logs *fakeLog, _ *oaPreviewInput) {
			logs.err = errors.New("sink unavailable")
		}, "unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, apps, _, logs, kinds := oaFixture(t)
			input := validOAPreview()
			test.edit(a, apps, logs, &input)
			result := a.preview(context.Background(), input)
			if result["outcome"] != test.want || len(*kinds) != 1 || (*kinds)[0] != "context" {
				t.Fatalf("result=%v bridge=%v", result, *kinds)
			}
		})
	}
}

func TestOAReadRequiresCurrentExactBinding(t *testing.T) {
	a, _, _, _, kinds := oaFixture(t)
	input := validOARead()
	input.Context = "old-workflow"
	if result := a.readOutcome(context.Background(), input); result["outcome"] != "stale" || len(*kinds) != 1 || (*kinds)[0] != "context" {
		t.Fatalf("stale read result=%v bridge=%v", result, *kinds)
	}
}

func TestOAReadRefusesMismatchedReturnedBinding(t *testing.T) {
	a, _, _, _, _ := oaFixture(t)
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in command
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		result := map[string]any{"application_instance": "oa-instance", "instance": "browser-page", "context": "workflow", "revision": "9"}
		if in.Kind == "read" {
			result = map[string]any{"outcome": "applied", "instance": "other-page", "context": "workflow", "revision": "9"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": result})
	}))
	defer h.Close()
	a.bridge = &bridgeClient{endpoint: h.URL, token: "fixture", client: h.Client()}
	if result := a.readOutcome(context.Background(), validOARead()); result["outcome"] != "stale" || result["reason"] != "result_binding_changed" {
		t.Fatalf("mismatched result binding = %v", result)
	}
}

func TestOAActivationIsSeparate(t *testing.T) {
	a, apps, _, _, kinds := oaFixture(t)
	result := a.activate(context.Background())
	if result["outcome"] != facade.ApplicationActivationOutcomeReady.String() || apps.activated != 1 || len(*kinds) != 0 {
		t.Fatalf("activation result=%v activated=%d bridge=%v", result, apps.activated, *kinds)
	}
}

func TestOAActivationRequiresAuditBeforeDirectoryCall(t *testing.T) {
	a, apps, _, logs, kinds := oaFixture(t)
	logs.err = errors.New("sink unavailable")
	result := a.activate(context.Background())
	if result["outcome"] != "unavailable" || apps.activated != 0 || len(*kinds) != 0 {
		t.Fatalf("activation result=%v activated=%d bridge=%v", result, apps.activated, *kinds)
	}
}

func TestOAMachineRequiresPairedTrustedRuntimeFlags(t *testing.T) {
	if _, err := oaMachine("endpoint", ""); err == nil {
		t.Fatal("endpoint without runtime program accepted")
	}
	if _, err := oaMachine("endpoint", "relative.exe"); err == nil {
		t.Fatal("relative runtime program accepted")
	}
	if machine, err := oaMachine("endpoint", filepath.Join(t.TempDir(), "runtime.exe")); err != nil || machine == nil {
		t.Fatal(machine, err)
	}
}

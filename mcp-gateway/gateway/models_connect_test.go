package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

type connectPermissionFixture struct {
	observation  askswire.PermissionObservation
	calls        int
	intent       askswire.PermissionIntent
	requestErr   error
	observeErr   error
	observedKeys []string
	observedIDs  []string
	question     func(context.Context, string, int64) (askswire.PendingQuestion, error)
}

type modelsConnectSchemaFixture struct {
	*fixtureBackend
	calls int
}

func (f *modelsConnectSchemaFixture) ModelsConnect(context.Context, ModelsConnectInput) (ModelsConnectOutput, error) {
	f.calls++
	return ModelsConnectOutput{Outcome: "pending", QuestionIDs: []string{"actual-returned-question"}}, nil
}

func TestModelsConnectClosedProviderOnlySchemaRefusesPurposeBeforeBackend(t *testing.T) {
	f := &modelsConnectSchemaFixture{fixtureBackend: &fixtureBackend{principal: "schema", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}}
	_, session := connected(t, f, "fixture", "2025-11-25")
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == ToolModelsConnect {
			raw, _ := json.Marshal(tool.OutputSchema)
			var schema struct {
				AnyOf []struct{ Properties map[string]json.RawMessage }
			}
			if err := json.Unmarshal(raw, &schema); err != nil || len(schema.AnyOf) == 0 || schema.AnyOf[0].Properties["question_ids"] == nil {
				t.Fatal(string(raw), err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("missing actual tool")
	}
	for _, args := range []map[string]any{{"provider": "deepseek", "purpose": ""}, {"provider": "deepseek", "purpose": "reason"}, {"provider": "deepseek", "program": "foreign"}, {"provider": "deepseek", "sessionID": "foreign"}, {"provider": "custom"}} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolModelsConnect, Arguments: args})
		if err == nil && !result.IsError {
			t.Fatal("extra argument reached backend", args)
		}
	}
	if f.calls != 0 {
		t.Fatal(f.calls)
	}
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolModelsConnect, Arguments: map[string]any{"provider": "deepseek"}})
	if err != nil || result.IsError || f.calls != 1 {
		t.Fatal(result, err, f.calls)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var output ModelsConnectOutput
	if err := json.Unmarshal(raw, &output); err != nil || !slices.Equal(output.QuestionIDs, []string{"actual-returned-question"}) {
		t.Fatal(string(raw), err)
	}
}

func (p *connectPermissionFixture) RequestContext(_ context.Context, in askswire.PermissionIntent) (askswire.PermissionObservation, error) {
	p.calls++
	p.intent = in
	return p.observation, p.requestErr
}
func (p *connectPermissionFixture) ObserveContext(ctx context.Context, key string, _ int64) (askswire.PermissionObservation, error) {
	p.observedKeys = append(p.observedKeys, key)
	if p.observeErr != nil {
		return askswire.PermissionObservation{}, p.observeErr
	}
	<-ctx.Done()
	return p.observation, ctx.Err()
}

func (p *connectPermissionFixture) ObserveQuestionContext(ctx context.Context, id string, wait int64) (askswire.PendingQuestion, error) {
	p.observedIDs = append(p.observedIDs, id)
	if p.question != nil {
		return p.question(ctx, id, wait)
	}
	if p.observeErr != nil {
		return askswire.PendingQuestion{}, p.observeErr
	}
	<-ctx.Done()
	return askswire.PendingQuestion{Observation: p.observation}, ctx.Err()
}

func TestModelsConnectWaitsForFreshQuestionPermitWithinSameCall(t *testing.T) {
	p := &connectPermissionFixture{observation: askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending,
		Answer: &askswire.Answer{ID: "native-question", Pending: true}}}
	p.question = func(ctx context.Context, id string, wait int64) (askswire.PendingQuestion, error) {
		if id != "native-question" || wait <= 0 || wait > 30000 || ctx.Err() != nil {
			t.Fatal("question identity or wait budget changed", id, wait, ctx.Err())
		}
		return askswire.PendingQuestion{Observation: askswire.PermissionObservation{
			Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: "fresh-native-policy"}}, nil
	}
	binding, _ := hostedcatalogue.Select("deepseek")
	reads := 0
	ctx, cancel := context.WithTimeout(t.Context(), 250*time.Millisecond)
	defer cancel()
	out, err := connectModels(ctx, ModelsConnectInput{Provider: "deepseek"}, func(context.Context) (modelsConnectPermission, error) { return p, nil }, func(context.Context) (routerwire.HostsSnapshot, error) {
		reads++
		return routerwire.HostsSnapshot{Hosts: []routerwire.HostState{{Host: binding.Name, Base: binding.Base,
			Wire: binding.Kind, Credential: binding.Credential, Hosted: true, Servable: true}}}, nil
	})
	if err != nil || out.Outcome != "connected" || p.calls != 1 || reads != 1 ||
		!slices.Equal(p.observedIDs, []string{"native-question"}) || len(p.observedKeys) != 0 {
		t.Fatal(out, err, p.calls, reads, p.observedIDs, p.observedKeys)
	}
}

func TestModelsConnectQuestionWaitNeverManufacturesPermission(t *testing.T) {
	for _, kind := range []string{"answer-only", "refused", "blocked", "denied", "no-policy", "pending", "cancelled", "cancelled-after-permit", "unavailable"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
			defer cancel()
			p := &connectPermissionFixture{observation: askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending,
				Answer: &askswire.Answer{ID: "native-question", Pending: true}}}
			want := "pending"
			p.question = func(ctx context.Context, id string, wait int64) (askswire.PendingQuestion, error) {
				if id != "native-question" || wait <= 0 || wait > 30000 {
					t.Fatal(id, wait)
				}
				out := askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered,
					Answer: &askswire.Answer{ID: id, Option: "allow"}}
				switch kind {
				case "answer-only":
					want = "unavailable"
				case "refused":
					out.Answer.Option = "refuse"
					want = "declined"
				case "blocked":
					out.Answer.Option = "never"
					want = "declined"
				case "denied":
					out = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeDenied, PolicyRevision: "fresh-policy"}
					want = "declined"
				case "no-policy":
					out = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted}
				case "pending":
					<-ctx.Done()
					return askswire.PendingQuestion{}, ctx.Err()
				case "cancelled-after-permit":
					cancel()
					out = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: "fresh-policy"}
				case "unavailable":
					return askswire.PendingQuestion{}, errors.New("unknown_method")
				}
				return askswire.PendingQuestion{Observation: out}, nil
			}
			if kind == "cancelled" {
				cancel()
			}
			out, err := connectModels(ctx, ModelsConnectInput{Provider: "deepseek"}, func(context.Context) (modelsConnectPermission, error) { return p, nil }, func(context.Context) (routerwire.HostsSnapshot, error) {
				t.Fatal("question supplied no current authority to read setup")
				return routerwire.HostsSnapshot{}, nil
			})
			if err != nil || out.Outcome != want || p.calls != 1 || len(p.observedKeys) != 0 {
				t.Fatal(out, err, want, p.calls, p.observedKeys)
			}
		})
	}
}

func TestModelsConnectRequiresOwnFreshSetupAfterPermission(t *testing.T) {
	binding, _ := hostedcatalogue.Select("deepseek")
	for _, kind := range []string{"connected", "host-missing", "binding-conflict", "not-servable", "duplicate", "declined", "pending", "pending-wait-lost", "answer-without-grant", "request-lost", "no-question-unavailable", "no-question-denied"} {
		t.Run(kind, func(t *testing.T) {
			p := &connectPermissionFixture{observation: askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: "native-policy"}}
			host := routerwire.HostState{Host: binding.Name, Base: binding.Base, Wire: binding.Kind, Credential: binding.Credential, Hosted: true, Servable: true}
			hosts := []routerwire.HostState{host}
			want := "unavailable"
			reads := 0
			switch kind {
			case "connected":
				want = "connected"
			case "host-missing":
				hosts = nil
			case "binding-conflict":
				hosts[0].Base = "https://person.example"
			case "not-servable":
				hosts[0].Servable = false
			case "duplicate":
				hosts = append(hosts, host)
			case "declined":
				p.observation = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered, Answer: &askswire.Answer{ID: "native-question", Option: "refuse"}}
				want = "declined"
			case "pending", "pending-wait-lost":
				p.observation = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending, Answer: &askswire.Answer{ID: "native-question", Pending: true}}
				want = "pending"
				if kind == "pending-wait-lost" {
					p.observeErr = errors.New("rights:question_pending:must-not-be-parsed")
				}
			case "no-question-unavailable":
				p.observation = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}
			case "no-question-denied":
				p.observation = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeDenied, PolicyRevision: "native-policy"}
				want = "declined"
			case "answer-without-grant":
				p.observation = askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered, Answer: &askswire.Answer{ID: "native-question", Option: "allow"}}
				want = "unavailable"
			case "request-lost":
				p.requestErr = errors.New("secret-shaped-private-error")
				want = "unknown"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
			defer cancel()
			out, err := connectModels(ctx, ModelsConnectInput{Provider: "deepseek"}, func(context.Context) (modelsConnectPermission, error) { return p, nil }, func(context.Context) (routerwire.HostsSnapshot, error) {
				reads++
				return routerwire.HostsSnapshot{Hosts: hosts}, nil
			})
			if err != nil || out.Outcome != want || p.calls != 1 {
				t.Fatal(out, err, p.calls)
			}
			if p.intent.Purpose != "" || p.intent.Selection == nil || string(p.intent.Selection.Selector.Target) != "credential:deepseek" {
				t.Fatal(p.intent)
			}
			if (want == "pending" || want == "declined" || want == "unknown") && reads != 0 {
				t.Fatal("permission supplied no setup proof", reads)
			}
			if want == "pending" {
				if !slices.Equal(out.QuestionIDs, []string{"native-question"}) {
					t.Fatal("pending lost submitted question", out)
				}
			} else if len(out.QuestionIDs) != 0 {
				t.Fatal("nonpending exposed question IDs", out)
			}
			for _, key := range p.observedKeys {
				if key != p.intent.RequestKey {
					t.Fatal("wait changed submitted Request", key, p.intent.RequestKey)
				}
			}
			for _, id := range p.observedIDs {
				if id != "native-question" {
					t.Fatal("wait changed the returned question", id)
				}
			}
			if kind == "no-question-unavailable" && reads != 0 {
				t.Fatal("no question read provider setup")
			}
		})
	}
}

func TestModelsConnectUnsupportedProviderHasNoNativeEffects(t *testing.T) {
	for _, in := range []ModelsConnectInput{{Provider: "custom"}} {
		out, err := connectModels(t.Context(), in, func(context.Context) (modelsConnectPermission, error) {
			t.Fatal("unsupported input requested a native question")
			return nil, nil
		}, func(context.Context) (routerwire.HostsSnapshot, error) {
			t.Fatal("unsupported input read native hosts")
			return routerwire.HostsSnapshot{}, nil
		})
		if err != nil || out.Outcome == "connected" {
			t.Fatal(out, err)
		}
	}
}

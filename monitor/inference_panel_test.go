package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// panelOperator is an operator@1 fixture: one host at one revision, keys it
// issues and revokes, and an audit of three entries, answering forbidden while
// deny is set.
type panelOperator struct {
	mu         sync.Mutex
	deny       bool
	emptyHosts bool
	calls      []string
	callers    []inference.Subject
	keys       []iwire.LocalKey
}

func (o *panelOperator) seen(call string, s inference.Subject) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.calls, o.callers = append(o.calls, call), append(o.callers, s)
	return !o.deny
}

func (o *panelOperator) Hosts(_ context.Context, s inference.Subject) iwire.HostList {
	if !o.seen("hosts", s) {
		return iwire.HostList{Outcome: iwire.ListOutcomeForbidden, Hosts: []iwire.HostState{}}
	}
	if o.emptyHosts {
		return iwire.HostList{Outcome: iwire.ListOutcomePage, Hosts: []iwire.HostState{}}
	}
	return iwire.HostList{Outcome: iwire.ListOutcomePage, Revision: "hosts-v1:a", Hosts: []iwire.HostState{{
		Entry: iwire.HostEntry{Name: "openrouter", Hosted: true, Kind: "openai-compatible", Base: "https://openrouter.ai/api/v1", Credential: "openrouter", Ceiling: &iwire.CeilingLimit{TokensPerDay: 1000}},
		Up:    true, Spend: &iwire.Spend{Day: "2026-09-17", Tokens: 42, Micros: 1500}}}}
}

// A native provider offers models while operator Hosts has no HTTP servers.
// The Panel must project the public router offer for the chosen modality,
// preserving empty results and the service's permission/unavailability refusal.
func TestPanelModelsShowNativeOffersThroughTheRouter(t *testing.T) {
	own(t)
	ctx := context.Background()
	native, err := router.NewNative("modelhost", "unused-native-fixture", listen.ServerExpectation{},
		[]string{"chat:qwen3.6-demo", "image:flux2-klein-4b"}, []string{router.ProfileChat, router.ProfileImage})
	if err != nil {
		t.Fatal(err)
	}
	r := router.New(native)
	if err := r.SurveyContext(ctx); err != nil {
		t.Fatal(err)
	}
	operator := &panelOperator{emptyHosts: true}
	var policy atomic.Int32
	stop := panelConfigRuntimeWith(t, func(o *host.Options) {
		o.Router, o.RouterEndpoint = r, o.ConfigEndpoint+"-router"
		o.RouterPolicy = func(_ context.Context, peer *identity.Peer, action, resource string) error {
			if peer == nil || action != routerservice.ActionInventory || resource != routerservice.ResourceInventory {
				return errors.New("unexpected inventory caller or permission")
			}
			switch policy.Load() {
			case 1:
				return errors.New("inventory read denied")
			case 2:
				return routerservice.ErrPolicyUnavailable
			}
			return nil
		}
		provider, err := inference.New(inference.Config{Router: r,
			Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
			Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
				return nil, "unknown"
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		endpoint := o.ConfigEndpoint + "-inference"
		service, err := inferenceservice.Listen(endpoint, provider)
		if err != nil {
			t.Fatal(err)
		}
		service.Operator = operator
		o.Inference, o.InferenceEndpoint = panelInference{Host: service, provider: provider}, endpoint
	})
	h := newTestPanel(t).handler("test-key")
	hosts := decodePanel[iwire.HostList](t, 200, panelRequest(t, h, "/inference/hosts", nil).Body.Bytes())
	if hosts.Outcome != iwire.ListOutcomePage || len(hosts.Hosts) != 0 {
		t.Fatalf("native models fixture unexpectedly has HTTP servers: %+v", hosts)
	}
	request := httptest.NewRequest(http.MethodGet, "/inference/models", nil)
	request.Host = "127.0.0.1:8734"
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("model offers without Panel key: %d", response.Code)
	}
	if response := panelRequest(t, h, "/inference/models", struct{}{}); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("model offers write answered %d", response.Code)
	}
	direct, err := panelMachine().ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ profile, model string }{{"chat", "qwen3.6-demo"}, {"image", "flux2-klein-4b"}} {
		panel := decodePanel[routerwire.OfferSnapshot](t, 200, panelRequest(t, h, "/inference/models?profile="+test.profile, nil).Body.Bytes())
		public, err := direct.OfferContext(ctx, routerclient.OfferRequest{Profile: test.profile, Placement: routerwire.PlacementAny})
		if err != nil || panel.Outcome != routerwire.OfferOutcomeOffered || public.Outcome != routerwire.OfferOutcomeOffered || !reflect.DeepEqual(panel.Offers, public.Offers) {
			t.Fatalf("Panel and direct public %s offers diverged: panel=%+v public=%+v err=%v", test.profile, panel, public, err)
		}
		if len(panel.Offers) != 1 || len(panel.Offers[0].Names) != 1 || panel.Offers[0].Names[0] != test.model || !reflect.DeepEqual(panel.Offers[0].Servers, []string{"modelhost"}) {
			t.Fatalf("native %s model missing: %+v", test.profile, panel)
		}
		if test.profile == "chat" && (panel.Offers[0].Context != 32768 || panel.Offers[0].ContextSource != routerwire.ContextSourceDefault) {
			t.Fatalf("chat context lost: %+v", panel)
		}
		if test.profile == "image" && panel.Offers[0].ContextSource != routerwire.ContextSourceNone {
			t.Fatalf("image acquired a context window: %+v", panel)
		}
	}
	empty := decodePanel[routerwire.OfferSnapshot](t, 200, panelRequest(t, h, "/inference/models?profile=speech", nil).Body.Bytes())
	if len(empty.Offers) != 0 {
		t.Fatalf("speech should be explicitly empty: %+v", empty)
	}
	for _, test := range []struct {
		policy  int32
		outcome routerwire.OfferOutcome
		status  int
	}{{1, routerwire.OfferOutcomeNotPermitted, http.StatusForbidden}, {2, routerwire.OfferOutcomeUnavailable, http.StatusServiceUnavailable}} {
		policy.Store(test.policy)
		response := panelRequest(t, h, "/inference/models", nil)
		public, publicErr := direct.OfferContext(ctx, routerclient.OfferRequest{Profile: "chat", Placement: routerwire.PlacementAny})
		if publicErr != nil || public.Outcome != test.outcome || response.Code != test.status || strings.TrimSpace(response.Body.String()) != "router offer: "+test.outcome.String() {
			t.Fatalf("router refusal %s became success: %d %s, public %+v %v", test.outcome, response.Code, response.Body, public, publicErr)
		}
	}
	stop()
	if response := panelRequest(t, h, "/inference/models", nil); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("stopped runtime offers answered %d: %s", response.Code, response.Body)
	}
}

func (o *panelOperator) AddHost(_ context.Context, s inference.Subject, expected string, h iwire.HostEntry) iwire.HostChange {
	o.seen("add "+expected+" "+h.Name+" "+h.Kind, s)
	if expected != "hosts-v1:a" {
		return iwire.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: "hosts-v1:a", Reason: "revision"}
	}
	return iwire.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: "hosts-v1:b"}
}

func (o *panelOperator) RemoveHost(_ context.Context, s inference.Subject, expected, name string) iwire.HostChange {
	o.seen("remove "+expected+" "+name, s)
	return iwire.HostChange{Outcome: iwire.EditOutcomeUnknown, Revision: expected}
}

func (o *panelOperator) Keys(_ context.Context, s inference.Subject) iwire.KeyList {
	o.seen("keys", s)
	o.mu.Lock()
	defer o.mu.Unlock()
	return iwire.KeyList{Outcome: iwire.ListOutcomePage, Keys: append([]iwire.LocalKey{}, o.keys...)}
}

func (o *panelOperator) IssueKey(_ context.Context, s inference.Subject, program, credential string) iwire.KeyIssued {
	o.seen("issue "+program+" "+credential, s)
	record := iwire.LocalKey{Program: program, Name: "local-key.x", Credential: credential, IssuedBy: s.Program, State: iwire.KeyStateActive}
	o.mu.Lock()
	o.keys = append(o.keys, record)
	o.mu.Unlock()
	return iwire.KeyIssued{Outcome: iwire.EditOutcomeApplied, Key: "oalk_fixture", Record: &record}
}

func (o *panelOperator) RevokeKey(_ context.Context, s inference.Subject, program string) iwire.KeyRevoked {
	o.seen("revoke "+program, s)
	return iwire.KeyRevoked{Outcome: iwire.EditOutcomeApplied}
}

func (o *panelOperator) Audit(_ context.Context, s inference.Subject, cursor, max int64) iwire.AuditPage {
	o.seen("audit", s)
	entries := []iwire.AuditEntry{}
	for i := cursor; i < 3 && int64(len(entries)) < max; i++ {
		entries = append(entries, iwire.AuditEntry{Sequence: i, Route: iwire.AuditRouteWindow, Rung: "tcp-loopback/test user=bound process=bound path=bound", Program: "/usr/bin/python3", Outcome: "completed"})
	}
	return iwire.AuditPage{Outcome: iwire.AuditOutcomePage, Entries: entries, Next: cursor + int64(len(entries)), AtEnd: true}
}

func (o *panelOperator) Gateway(_ context.Context, s inference.Subject) iwire.GatewayState {
	o.seen("gateway", s)
	return iwire.GatewayState{Outcome: iwire.ListOutcomePage, Revision: "gateway-v1:a"}
}

func (o *panelOperator) SetGateway(_ context.Context, s inference.Subject, expected string, open bool, address string) iwire.GatewayChange {
	o.seen(fmt.Sprintf("set gateway %s %t %s", expected, open, address), s)
	return iwire.GatewayChange{Outcome: iwire.EditOutcomeApplied, Revision: "gateway-v1:b"}
}

type panelInference struct {
	*inferenceservice.Host
	provider *inference.Provider
}

func (s panelInference) OperatorAvailable() bool { return true }
func (s panelInference) Close() error            { return errors.Join(s.Host.Close(), s.provider.Close()) }

// The inference page reads hosts with spend, issues and revokes keys and pages
// the audit through the resolved operator@1, passing typed outcomes through
// unchanged; malformed edits are refused before any call.
func TestPanelInferenceGoesThroughTheOperatorService(t *testing.T) {
	own(t)
	operator := &panelOperator{}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		provider, err := inference.New(inference.Config{Router: router.New(),
			Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
			Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
				return nil, "unknown"
			}})
		if err != nil {
			t.Fatal(err)
		}
		endpoint := o.ConfigEndpoint + "-inference"
		h, err := inferenceservice.Listen(endpoint, provider)
		if err != nil {
			t.Fatal(err)
		}
		h.Operator = operator
		o.Inference, o.InferenceEndpoint = panelInference{Host: h, provider: provider}, endpoint
	})
	h := newTestPanel(t).handler("test-key")

	if r := panelRequest(t, h, "/inference?k=wrong", nil); r.Code != 403 {
		t.Fatalf("page without the key: %d", r.Code)
	}
	list := decodePanel[iwire.HostList](t, 200, panelRequest(t, h, "/inference/hosts", nil).Body.Bytes())
	if list.Outcome != iwire.ListOutcomePage || list.Revision != "hosts-v1:a" || list.Hosts[0].Spend.Tokens != 42 {
		t.Fatalf("hosts %+v", list)
	}
	ctx := context.Background()
	direct, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	directList, err := direct.Hosts(ctx)
	if err != nil || !reflect.DeepEqual(list, directList) {
		t.Fatalf("Panel and direct public Hosts diverged: Panel=%+v direct=%+v err=%v", list, directList, err)
	}
	for _, bad := range []any{
		inferenceHostEdit{Edit: "add", Revision: "hosts-v1:a", Host: iwire.HostEntry{Name: "bad name", Kind: "ollama", Base: "http://127.0.0.1:1"}},
		inferenceHostEdit{Edit: "drop", Revision: "hosts-v1:a", Name: "ollama"},
		inferenceHostEdit{Edit: "remove", Name: "ollama"},
	} {
		if r := panelRequest(t, h, "/inference/hosts", bad); r.Code != 400 {
			t.Fatalf("host edit %+v answered %d", bad, r.Code)
		}
	}
	for _, bad := range []inferenceKeyEdit{{Edit: "issue", Program: "relative.exe"}, {Edit: "revoke", Program: filepath.Join(t.TempDir(), "app"), Credential: "x"}, {Edit: "show", Program: filepath.Join(t.TempDir(), "app")}} {
		if r := panelRequest(t, h, "/inference/keys", bad); r.Code != 400 {
			t.Fatalf("key edit %+v answered %d", bad, r.Code)
		}
	}
	added := decodePanel[iwire.HostChange](t, 200, panelRequest(t, h, "/inference/hosts", inferenceHostEdit{Edit: "add", Revision: "hosts-v1:a",
		Host: iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11434"}}).Body.Bytes())
	stale := decodePanel[iwire.HostChange](t, 200, panelRequest(t, h, "/inference/hosts", inferenceHostEdit{Edit: "add", Revision: "hosts-v1:old",
		Host: iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11434"}}).Body.Bytes())
	if added.Outcome != iwire.EditOutcomeApplied || stale.Outcome != iwire.EditOutcomeConflict {
		t.Fatalf("add %+v, stale add %+v", added, stale)
	}
	directStale, err := direct.AddHost(ctx, "hosts-v1:old", iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11434"})
	if err != nil || directStale.Outcome != stale.Outcome || directStale.Revision != stale.Revision {
		t.Fatalf("Panel and direct stale AddHost diverged: Panel=%+v direct=%+v err=%v", stale, directStale, err)
	}
	removeResponse := panelRequest(t, h, "/inference/hosts", inferenceHostEdit{Edit: "remove", Revision: list.Revision, Name: "lemonade"})
	panelRemoved := decodePanel[iwire.HostChange](t, removeResponse.Code, removeResponse.Body.Bytes())
	directRemoved, err := direct.RemoveHost(ctx, list.Revision, "lemonade")
	if err != nil || !reflect.DeepEqual(panelRemoved, directRemoved) {
		t.Fatalf("Panel and direct RemoveHost diverged: Panel=%+v direct=%+v err=%v", panelRemoved, directRemoved, err)
	}
	program := filepath.Join(t.TempDir(), "python3")
	issued := decodePanel[iwire.KeyIssued](t, 200, panelRequest(t, h, "/inference/keys", inferenceKeyEdit{Edit: "issue", Program: program, Credential: "openrouter"}).Body.Bytes())
	if issued.Outcome != iwire.EditOutcomeApplied || issued.Key != "oalk_fixture" || issued.Record.Program != program {
		t.Fatalf("issue %+v", issued)
	}
	keys := decodePanel[iwire.KeyList](t, 200, panelRequest(t, h, "/inference/keys", nil).Body.Bytes())
	revoked := decodePanel[iwire.KeyRevoked](t, 200, panelRequest(t, h, "/inference/keys", inferenceKeyEdit{Edit: "revoke", Program: program}).Body.Bytes())
	if len(keys.Keys) != 1 || revoked.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("keys %+v, revoke %+v", keys, revoked)
	}
	if r := panelRequest(t, h, "/inference/audit?cursor=-1", nil); r.Code != 400 {
		t.Fatalf("negative cursor answered %d", r.Code)
	}
	audit := decodePanel[iwire.AuditPage](t, 200, panelRequest(t, h, "/inference/audit?cursor=1", nil).Body.Bytes())
	if audit.Outcome != iwire.AuditOutcomePage || len(audit.Entries) != 2 || audit.Entries[0].Sequence != 1 || !audit.AtEnd {
		t.Fatalf("audit %+v", audit)
	}
	if r := panelRequest(t, h, "/inference/gateway", inferenceGatewayEdit{Revision: "gateway-v1:a", Open: true, Address: "0.0.0.0:8793"}); r.Code != 400 {
		t.Fatalf("a gateway edit off loopback answered %d", r.Code)
	}
	gatewayState := decodePanel[iwire.GatewayState](t, 200, panelRequest(t, h, "/inference/gateway", nil).Body.Bytes())
	opened := decodePanel[iwire.GatewayChange](t, 200, panelRequest(t, h, "/inference/gateway", inferenceGatewayEdit{Revision: "gateway-v1:a", Open: true, Address: "127.0.0.1:8793"}).Body.Bytes())
	if gatewayState.Revision != "gateway-v1:a" || opened.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("gateway %+v, set %+v", gatewayState, opened)
	}
	operator.mu.Lock()
	operator.deny = true
	operator.mu.Unlock()
	if denied := decodePanel[iwire.HostList](t, 200, panelRequest(t, h, "/inference/hosts", nil).Body.Bytes()); denied.Outcome != iwire.ListOutcomeForbidden {
		t.Fatalf("a forbidden listing %+v", denied)
	}
	if denied, err := direct.Hosts(ctx); err != nil || denied.Outcome != iwire.ListOutcomeForbidden {
		t.Fatalf("same caller direct Hosts refusal: %+v %v", denied, err)
	}
	operator.mu.Lock()
	defer operator.mu.Unlock()
	if strings.Join(operator.calls, "|") != "hosts|hosts|add hosts-v1:a ollama ollama|add hosts-v1:old ollama ollama|add hosts-v1:old ollama ollama|remove hosts-v1:a lemonade|remove hosts-v1:a lemonade|issue "+program+" openrouter|keys|revoke "+program+"|audit|gateway|set gateway gateway-v1:a true 127.0.0.1:8793|hosts|hosts" {
		t.Fatalf("operator calls %q", operator.calls)
	}
	for _, s := range operator.callers {
		if s.Program == "" || s.Account == "" {
			t.Fatalf("an operator call was not bound: %+v", s)
		}
	}
}

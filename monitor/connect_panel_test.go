package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
)

// presetFakeHolder keeps the registrations it stored by name: a second
// Store of a name is a conflict carrying the saved metadata, as the holder
// answers, and Rotate replaces the saved key.
type presetFakeHolder struct {
	stored  []cwire.Registration
	rotated []cwire.Rotation
	outcome cwire.StoreOutcome
	failure error
}

func (h *presetFakeHolder) saved(name string) *cwire.Registration {
	for i := range h.stored {
		if h.stored[i].Name == name {
			return &h.stored[i]
		}
	}
	return nil
}

func (h *presetFakeHolder) Store(_ context.Context, expected string, reg cwire.Registration) (cwire.StoreResult, error) {
	if h.failure != nil {
		return cwire.StoreResult{}, h.failure
	}
	if expected != "" {
		return cwire.StoreResult{}, fmt.Errorf("a new credential is stored at no revision, got %q", expected)
	}
	if h.outcome != 0 {
		return cwire.StoreResult{Outcome: h.outcome}, nil
	}
	if kept := h.saved(reg.Name); kept != nil {
		return cwire.StoreResult{Outcome: cwire.StoreOutcomeConflict, Revision: "c1",
			Current: &cwire.Metadata{Name: kept.Name, Kind: kept.Kind, Revision: "c1", State: cwire.StateActive, Scope: kept.Scope}}, nil
	}
	reg.Secret = append([]byte(nil), reg.Secret...)
	h.stored = append(h.stored, reg)
	return cwire.StoreResult{Outcome: cwire.StoreOutcomeStored, Revision: "c1"}, nil
}

func (h *presetFakeHolder) Rotate(_ context.Context, expected string, rotation cwire.Rotation) (cwire.RotateResult, error) {
	kept := h.saved(rotation.Name)
	if kept == nil || expected != "c1" {
		return cwire.RotateResult{Outcome: cwire.RotateOutcomeConflict}, nil
	}
	rotation.Secret = append([]byte(nil), rotation.Secret...)
	kept.Secret = rotation.Secret
	h.rotated = append(h.rotated, rotation)
	return cwire.RotateResult{Outcome: cwire.RotateOutcomeRotated, Revision: "c2"}, nil
}

type presetFakeOperator struct {
	revision int
	hosts    []iclient.HostState
	refuse   map[string]bool // AddHost refused for a budget of this amount, in either unit
	calls    []string
}

func TestConnectNamedCreationPreservesScopeAndNeverOverwrites(t *testing.T) {
	presets, _ := hostedPresets()
	holder, operator := &presetFakeHolder{}, &presetFakeOperator{}
	blank := ""
	first, err := connectPresetEntry(context.Background(), holder, operator, presets[0], "", []byte("dummy-one"), 0, &blank)
	if err != nil || first.Outcome != "applied" {
		t.Fatalf("first: %+v %v", first, err)
	}
	second, err := connectPresetEntry(context.Background(), holder, &presetFakeOperator{}, presets[0], "", []byte("dummy-two"), 0, &blank)
	if err != nil || second.Outcome != "applied" || first.Name == second.Name {
		t.Fatalf("unique names: %+v %+v %v", first, second, err)
	}
	if !reflect.DeepEqual(holder.stored[0].Scope, cwire.Scope{Targets: []string{"api.deepseek.com"}, Consumers: presetConsumers}) || holder.stored[0].Kind != "bearer" || operator.hosts[0].Entry.Ceiling != nil {
		t.Fatal("preset restrictions or no-limit semantics changed")
	}
	name := first.Name
	before := len(operator.calls)
	duplicate, err := connectPresetEntry(context.Background(), holder, operator, presets[0], "", []byte("dummy-replacement"), 0, &name)
	if err != nil || duplicate.Outcome != "conflict" || len(holder.rotated) != 0 || len(operator.calls) != before || string(holder.stored[0].Secret) != "dummy-one" {
		t.Fatalf("overwrite: %+v %v", duplicate, err)
	}
	bad := "not a valid name"
	if result, _ := connectPresetEntry(context.Background(), holder, operator, presets[0], "", []byte("dummy"), 0, &bad); result.Outcome != "invalid" || len(holder.stored) != 2 {
		t.Fatal("invalid name reached storage")
	}
}

func TestConnectNamedMoneyBudgetRetainsPresetUnit(t *testing.T) {
	presets, _ := hostedPresets()
	holder, operator := &presetFakeHolder{}, &presetFakeOperator{}
	name := "work-router"
	if result, err := connectPresetEntry(context.Background(), holder, operator, presets[1], "", []byte("dummy"), 0, &name); err != nil || result.Outcome != "applied" {
		t.Fatalf("connect: %+v %v", result, err)
	}
	if result, err := budgetPreset(context.Background(), operator, presets, presets[1].Credential, 2_000_000); err != nil || result.Outcome != "applied" {
		t.Fatalf("budget: %+v %v", result, err)
	}
	if got := operator.hosts[0].Entry.Ceiling; got.MicrosPerDay != 2_000_000 || got.TokensPerDay != 0 {
		t.Fatalf("unit: %+v", got)
	}
}

func TestConnectBudgetUnitsIgnoreCrossPresetCredentialNames(t *testing.T) {
	presets, _ := hostedPresets()
	for _, host := range []iclient.HostEntry{
		{Name: "example.com", Hosted: true, Kind: "openai-compatible", Base: "https://example.com/v1", Credential: "openrouter", DeclaredBy: "operator"},
		{Name: "deepseek", Hosted: true, Kind: "openai-compatible", Base: "https://api.deepseek.com/v1", Credential: "openrouter", DeclaredBy: "operator"},
	} {
		t.Run(host.Name, func(t *testing.T) {
			operator := &presetFakeOperator{hosts: []iclient.HostState{{Entry: host}}}
			if result, err := budgetPreset(context.Background(), operator, presets, host.Name, 123); err != nil || result.Outcome != "applied" {
				t.Fatalf("budget: %+v %v", result, err)
			}
			if got := operator.hosts[0].Entry.Ceiling; got.TokensPerDay != 123 || got.MicrosPerDay != 0 {
				t.Fatalf("credential name selected unit: %+v", got)
			}
		})
	}
}

func (o *presetFakeOperator) rev() string { return fmt.Sprintf("r%d", o.revision) }

func (o *presetFakeOperator) Hosts(context.Context) (iclient.HostList, error) {
	o.calls = append(o.calls, "hosts")
	return iclient.HostList{Outcome: iwire.ListOutcomePage, Revision: o.rev(), Hosts: append([]iclient.HostState(nil), o.hosts...)}, nil
}

func (o *presetFakeOperator) AddHost(_ context.Context, expected string, e iclient.HostEntry) (iclient.HostChange, error) {
	o.calls = append(o.calls, "add "+e.Name)
	if expected != o.rev() {
		return iclient.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: o.rev(), Reason: "revision"}, nil
	}
	if e.Ceiling != nil && (o.refuse[fmt.Sprint(e.Ceiling.MicrosPerDay)] || o.refuse[fmt.Sprint(e.Ceiling.TokensPerDay)]) {
		return iclient.HostChange{Outcome: iwire.EditOutcomeInvalid, Reason: "ceiling"}, nil
	}
	e.DeclaredBy = "operator"
	o.hosts = append(o.hosts, iclient.HostState{Entry: e, Up: true})
	o.revision++
	return iclient.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: o.rev()}, nil
}

func (o *presetFakeOperator) RemoveHost(_ context.Context, expected, name string) (iclient.HostChange, error) {
	o.calls = append(o.calls, "remove "+name)
	if expected != o.rev() {
		return iclient.HostChange{Outcome: iwire.EditOutcomeConflict, Revision: o.rev()}, nil
	}
	for i, h := range o.hosts {
		if h.Entry.Name == name {
			o.hosts = append(o.hosts[:i], o.hosts[i+1:]...)
			o.revision++
			return iclient.HostChange{Outcome: iwire.EditOutcomeApplied, Revision: o.rev()}, nil
		}
	}
	return iclient.HostChange{Outcome: iwire.EditOutcomeUnknown, Revision: o.rev()}, nil
}

// The shipped presets load, DeepSeek first, and each fixed preset names its
// address and credential; the generic row names neither. DeepSeek's usage
// object carries token counts and no cost, so its budget is in tokens: a
// money budget would never trip (INF-C2).
func TestHostedPresetsAreData(t *testing.T) {
	presets, err := hostedPresets()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, p := range presets {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{"deepseek", "openrouter", "github-download", "openai-compatible"}) {
		t.Fatalf("presets %v", ids)
	}
	if p := presets[0]; p.Base != "https://api.deepseek.com/v1" || p.Credential != "deepseek" || p.API != "openai-compatible" || p.Budget != budgetTokens {
		t.Fatalf("DeepSeek preset %+v", p)
	}
	if p := presets[1]; p.Credential != "openrouter" || p.Budget != budgetMoney {
		t.Fatalf("OpenRouter preset %+v", p)
	}
	for _, bad := range []string{
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"http://insecure.example/v1","credential":"a","budget":"money"}]}`,
		`{"presets":[{"id":"a","label":"A","api":"carrier-pigeon","base":"https://a.example/v1","credential":"a","budget":"money"}]}`,
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"https://a.example/v1","credential":"","budget":"money"}]}`,
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"","credential":"","budget":"money"},{"id":"a","label":"B","api":"openai-compatible","base":"","credential":"","budget":"money"}]}`,
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"","credential":"","budget":"money","secret":"x"}]}`,
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"","credential":""}]}`,
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"","credential":"","budget":"dollars"}]}`,
		`{"presets":[{"id":"a","label":"A","api":"openai-compatible","base":"","credential":"","budget":"money","models":[]}]}`,
	} {
		if _, err := loadHostedPresets([]byte(bad)); err == nil {
			t.Errorf("a malformed preset file loaded: %s", bad)
		}
	}
}

func TestDownloadPresetStoresOnlyHostBoundDownloadCredential(t *testing.T) {
	presets, err := hostedPresets()
	if err != nil {
		t.Fatal(err)
	}
	preset := presets[2]
	holder := &presetFakeHolder{}
	// A nil inference operator proves this branch never reads/adds model hosts.
	for _, value := range []string{"fixture-first", "fixture-replacement"} {
		result, err := connectPreset(context.Background(), holder, nil, preset, "", []byte(value), 0)
		if err != nil || result != (connectResult{Outcome: "applied", Name: "github-download"}) {
			t.Fatalf("result %v %v", result, err)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), value) {
			t.Fatal("reply disclosed value")
		}
	}
	if len(holder.stored) != 1 || len(holder.rotated) != 1 || !reflect.DeepEqual(holder.stored[0].Scope, cwire.Scope{Targets: []string{"api.github.com"}, Consumers: []string{"abstraction.download/http-execution@1"}}) {
		t.Fatal("source scope or conditional replacement differs")
	}
	if result, _ := connectPreset(context.Background(), holder, nil, preset, "", []byte("fixture"), 1); result.Outcome != "invalid" {
		t.Fatal("download accepted model budget")
	}
	// A token saved for a model API cannot silently become a download token.
	holder.stored[0].Scope.Consumers = presetConsumers
	if result, _ := connectPreset(context.Background(), holder, nil, preset, "", []byte("fixture"), 0); result.Outcome != "conflict" {
		t.Fatal("unrelated scope rotated")
	}
	holder.stored[0].Scope.Consumers = append([]string{"abstraction.download/http-execution@1"}, presetConsumers...)
	if result, _ := connectPreset(context.Background(), holder, nil, preset, "", []byte("fixture"), 0); result.Outcome != "conflict" {
		t.Fatal("broader scope rotated")
	}
	holder.failure = fmt.Errorf("untrusted diagnostic: %s", "fixture-secret-diagnostic")
	if _, err := connectPreset(context.Background(), holder, nil, preset, "", []byte("fixture-secret-diagnostic"), 0); err == nil || strings.Contains(err.Error(), "fixture-secret-diagnostic") {
		t.Fatal("credential error reflected input")
	}
	for _, bad := range []string{
		`{"presets":[{"id":"x","label":"X","kind":"download","base":"https://api.github.com","credential":"x","api":"openai-compatible"}]}`,
		`{"presets":[{"id":"x","label":"X","kind":"download","base":"https://api.github.com","credential":"x","budget":"money"}]}`,
		`{"presets":[{"id":"x","label":"X","kind":"download"}]}`,
		`{"presets":[{"id":"x","label":"X","kind":"unknown"}]}`,
	} {
		if _, err := loadHostedPresets([]byte(bad)); err == nil {
			t.Fatal("mixed preset accepted")
		}
	}
}

// A second preset added as data connects with no Go change: its key is saved
// under its credential name, scoped to its address's host and to chat and
// model-list reads, and its model server is added with the chosen budget in
// the preset's unit.
func TestConnectingAPresetSavesTheKeyAndAddsItsServer(t *testing.T) {
	presets, err := loadHostedPresets([]byte(`{"presets":[{"id":"together","label":"Together","api":"openai-compatible","base":"https://api.together.xyz/v1","credential":"together","budget":"money"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	holder, operator := &presetFakeHolder{}, &presetFakeOperator{}
	secret := []byte("sk-together-never-echoed")
	result, err := connectPreset(context.Background(), holder, operator, presets[0], "", secret, 2_000_000)
	if err != nil || result != (connectResult{Outcome: "applied", Name: "together"}) {
		t.Fatalf("connect %+v %v", result, err)
	}
	want := cwire.Registration{Name: "together", Kind: "bearer", Scope: cwire.Scope{Targets: []string{"api.together.xyz"},
		Consumers: []string{"abstraction.inference/chat@1", "abstraction.router/router@1"}}, Secret: secret}
	if len(holder.stored) != 1 || !reflect.DeepEqual(holder.stored[0], want) {
		t.Fatalf("stored %+v", holder.stored)
	}
	if len(operator.hosts) != 1 {
		t.Fatalf("hosts %+v", operator.hosts)
	}
	if e := operator.hosts[0].Entry; e.Name != "together" || !e.Hosted || e.Kind != "openai-compatible" || e.Base != "https://api.together.xyz/v1" ||
		e.Credential != "together" || e.Ceiling == nil || e.Ceiling.MicrosPerDay != 2_000_000 {
		t.Fatalf("server %+v", e)
	}
	raw, _ := json.Marshal(result)
	if strings.Contains(string(raw), string(secret)) {
		t.Fatal("the reply carries the key")
	}

	// The generic preset names its server and credential after the address,
	// and its budget is in tokens.
	generic := hostedPreset{ID: "openai-compatible", Label: "Any", API: "openai-compatible", Budget: budgetTokens}
	result, err = connectPreset(context.Background(), holder, operator, generic, "https://llm.example.org/v1", []byte("k"), 1_000_000)
	if c := operator.hosts[1].Entry.Ceiling; err != nil || result.Outcome != "applied" || result.Name != "llm.example.org" || c == nil || c.TokensPerDay != 1_000_000 || c.MicrosPerDay != 0 {
		t.Fatalf("generic connect %+v %v %+v", result, err, operator.hosts)
	}
	if result, _ := connectPreset(context.Background(), holder, operator, generic, "http://plain.example.org/v1", []byte("k"), 0); result.Outcome != "invalid" {
		t.Fatalf("an http address %+v", result)
	}
	if result, _ := connectPreset(context.Background(), holder, operator, generic, "https://LLM.example.org/v1", []byte("k"), 0); result.Outcome != "invalid" {
		t.Fatalf("a host name with capitals %+v", result)
	}

	// A credential already saved under that name for another address is a
	// conflict, and no server is added.
	other := cwire.Registration{Name: "elsewhere", Kind: "bearer", Scope: cwire.Scope{Targets: []string{"api.other.example"}, Consumers: presetConsumers}}
	holder.stored = append(holder.stored, other)
	elsewhere := hostedPreset{ID: "elsewhere", Label: "Elsewhere", API: "openai-compatible", Base: "https://api.elsewhere.example/v1", Credential: "elsewhere", Budget: budgetMoney}
	before := len(operator.hosts)
	if result, err := connectPreset(context.Background(), holder, operator, elsewhere, "", []byte("k"), 0); err != nil || result.Outcome != "conflict" || result.Part != "credential" || len(operator.hosts) != before || len(holder.rotated) != 0 {
		t.Fatalf("a credential saved for another use %+v %v", result, err)
	}
}

// A Connect whose server was refused leaves its key saved; the next Connect
// replaces that key and adds the server, so the row recovers. A server list
// that changed between the read and the edit is read again once.
func TestConnectRecoversAfterTheServerWasRefused(t *testing.T) {
	presets, err := hostedPresets()
	if err != nil {
		t.Fatal(err)
	}
	holder, operator := &presetFakeHolder{}, &presetFakeOperator{refuse: map[string]bool{"5000000": true}}
	result, err := connectPreset(context.Background(), holder, operator, presets[0], "", []byte("sk-first"), 5_000_000)
	if err != nil || result.Outcome != "invalid" || result.Part != "server" || len(holder.stored) != 1 || len(operator.hosts) != 0 {
		t.Fatalf("refused server %+v %v", result, err)
	}
	result, err = connectPreset(context.Background(), holder, operator, presets[0], "", []byte("sk-second"), 1_000_000)
	if err != nil || result != (connectResult{Outcome: "applied", Name: "deepseek"}) {
		t.Fatalf("second connect %+v %v", result, err)
	}
	if len(holder.rotated) != 1 || string(holder.stored[0].Secret) != "sk-second" {
		t.Fatalf("the saved key was not replaced: %+v", holder.rotated)
	}
	if len(operator.hosts) != 1 || operator.hosts[0].Entry.Ceiling.TokensPerDay != 1_000_000 {
		t.Fatalf("hosts %+v", operator.hosts)
	}

	// The server list moved under the edit: the Connect reads it again.
	moving := &movingOperator{presetFakeOperator: &presetFakeOperator{}}
	if result, err := connectPreset(context.Background(), &presetFakeHolder{}, moving, presets[1], "", []byte("k"), 2_000_000); err != nil || result.Outcome != "applied" {
		t.Fatalf("connect over a moving list %+v %v", result, err)
	}
}

// movingOperator changes its revision once between the first read and the
// first edit, as another Panel's edit would.
type movingOperator struct {
	*presetFakeOperator
	moved bool
}

func (o *movingOperator) AddHost(ctx context.Context, expected string, e iclient.HostEntry) (iclient.HostChange, error) {
	if !o.moved {
		o.moved = true
		o.revision++
	}
	return o.presetFakeOperator.AddHost(ctx, expected, e)
}

// A new budget replaces the declaration's budget in its preset's unit, and
// keeps the other units; a refused one restores the old.
func TestPresetBudgetReplacesAndRestores(t *testing.T) {
	presets, err := hostedPresets()
	if err != nil {
		t.Fatal(err)
	}
	operator := &presetFakeOperator{refuse: map[string]bool{"7000000": true}}
	operator.hosts = []iclient.HostState{
		{Entry: iclient.HostEntry{Name: "deepseek", Hosted: true, Kind: "openai-compatible", Base: "https://api.deepseek.com/v1",
			Credential: "deepseek", Ceiling: &iclient.CeilingLimit{TokensPerDay: 100, MicrosPerDay: 2_000_000}, DeclaredBy: "operator"}},
		{Entry: iclient.HostEntry{Name: "openrouter", Hosted: true, Kind: "openai-compatible", Base: "https://openrouter.ai/api/v1",
			Credential: "openrouter", DeclaredBy: "operator"}},
	}
	result, err := budgetPreset(context.Background(), operator, presets, "deepseek", 5_000_000)
	if err != nil || result.Outcome != "applied" {
		t.Fatalf("budget %+v %v", result, err)
	}
	if c := operator.hosts[len(operator.hosts)-1].Entry.Ceiling; c == nil || c.TokensPerDay != 5_000_000 || c.MicrosPerDay != 2_000_000 {
		t.Fatalf("DeepSeek's budget is in tokens: %+v", c)
	}
	if result, err := budgetPreset(context.Background(), operator, presets, "openrouter", 3_000_000); err != nil || result.Outcome != "applied" {
		t.Fatalf("budget %+v %v", result, err)
	}
	if c := operator.hosts[len(operator.hosts)-1].Entry.Ceiling; c == nil || c.MicrosPerDay != 3_000_000 || c.TokensPerDay != 0 {
		t.Fatalf("OpenRouter's budget is money: %+v", c)
	}
	result, err = budgetPreset(context.Background(), operator, presets, "deepseek", 7_000_000)
	if err != nil || result.Outcome != "invalid" {
		t.Fatalf("refused budget %+v %v", result, err)
	}
	for _, h := range operator.hosts {
		if h.Entry.Name == "deepseek" && h.Entry.Ceiling.TokensPerDay != 5_000_000 {
			t.Fatalf("the refused budget did not restore the server: %+v", operator.hosts)
		}
	}
	if len(operator.hosts) != 2 {
		t.Fatalf("hosts %+v", operator.hosts)
	}
	if result, _ := budgetPreset(context.Background(), operator, presets, "nothing", 0); result.Outcome != "unknown" {
		t.Fatalf("an unknown server %+v", result)
	}
}

// The handler lists the presets and refuses a malformed edit before it
// resolves any service.
func TestConnectHandlerRefusesMalformedEdits(t *testing.T) {
	p := &servicePanel{}
	rec := httptest.NewRecorder()
	p.connectService(rec, httptest.NewRequest(http.MethodGet, "/connect", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"deepseek"`) {
		t.Fatalf("list %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{
		`{"edit":"connect","preset":"deepseek","secret":""}`,
		`{"edit":"connect","preset":"nothing","secret":"k"}`,
		`{"edit":"connect","preset":"deepseek","base":"https://other.example/v1","secret":"k"}`,
		`{"edit":"connect","preset":"openai-compatible","secret":"k"}`,
		`{"edit":"connect","preset":"deepseek","secret":"k","budget":-1}`,
		`{"edit":"connect","preset":"github-download","secret":"k","budget":1}`,
		`{"edit":"connect","preset":"deepseek","secret":"k","micros":1}`,
		`{"edit":"budget","name":"deepseek","secret":"k"}`,
		`{"edit":"budget","name":"../x"}`,
		`{"edit":"limit","name":"deepseek"}`,
		`{"edit":"drop"}`,
		`{"edit":"connect","preset":"deepseek","secret":"k","extra":1}`,
	} {
		rec := httptest.NewRecorder()
		p.connectService(rec, httptest.NewRequest(http.MethodPost, "/connect", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), `"k"`) {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
}

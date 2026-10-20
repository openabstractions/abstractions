package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/probe"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"golang.org/x/sys/windows"
)

// factsRules is a rights operator holding a fixed rule list; only the reads
// the facts make are served.
type factsRules struct {
	assistantRules
	rules []rightsclient.PolicyRule
}

func (f factsRules) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "rev-1", Rules: f.rules, Complete: true}, nil
}

func (f factsRules) ReadRuleContext(_ context.Context, s rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error) {
	for _, rule := range f.rules {
		if rule.Subject == s && rule.Action == action && rule.Resource == resource {
			return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeFound, Revision: "rev-1", Record: &rightsclient.RuleRecord{Rule: rule, Why: "test"}}, nil
		}
	}
	return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeUnknown, Revision: "rev-1"}, nil
}

// factsInference is an inference operator with a fixed audit and host list.
type factsInference struct {
	entries []iclient.AuditEntry
	hosts   []iclient.HostState
}

func (f factsInference) Audit(_ context.Context, cursor, _ int64) (iclient.AuditPage, error) {
	if cursor != 0 {
		return iclient.AuditPage{}, errors.New("one page only")
	}
	return iclient.AuditPage{Outcome: iclient.AuditOutcomePage, Entries: f.entries, Next: int64(len(f.entries)), AtEnd: true}, nil
}

func (f factsInference) Hosts(context.Context) (iclient.HostList, error) {
	return iclient.HostList{Outcome: iclient.ListOutcomePage, Hosts: f.hosts}, nil
}

func factsShortPath(t *testing.T, path string) string {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		t.Fatalf("short path of %q: %v", path, err)
	}
	short := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(short, path) {
		t.Skip("this volume keeps no 8.3 short names")
	}
	return short
}

// A registered app reached through its 8.3 short alias reads by its
// registered title, in the Panel's sentences and the native prompt's.
func TestARegisteredAppThroughItsShortAliasReadsByItsTitle(t *testing.T) {
	long := filepath.Join(t.TempDir(), "Long Application Folder", "long application.exe")
	if err := os.MkdirAll(filepath.Dir(long), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(long, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	short := factsShortPath(t, long)
	names := newProgramTitler([]fwire.ApplicationEntry{{Descriptor: fwire.ApplicationDescriptor{Name: "long", Program: long, Title: "Long App"}, Scope: fwire.ScopeLocal}})
	if got := names.name(short); got != "Long App" {
		t.Errorf("the short alias reads %q", got)
	}
	if got := firstUseSentence(trayPublisher{Names: names}, short, "abstraction.model/lookup", "hf"); !strings.HasPrefix(got, "Long App wants to ") {
		t.Errorf("the prompt sentence reads %q", got)
	}
}

// The facts filter on the server by program identity: a rule, a model call
// and the answering rule recorded under the registered program's 8.3 short
// alias belong to the app; another program's do not. A call names its model
// and its server's registered title, or "Model server", never the raw host.
func TestAppFactsMatchProgramsByIdentityThroughAShortAlias(t *testing.T) {
	apps, p := appPagePanel(t, false)
	long := filepath.Join(t.TempDir(), "Example Application Folder", "example application.exe")
	engine := filepath.Join(t.TempDir(), "Engine Host Folder", "engine host.exe")
	for _, program := range []string{long, engine} {
		if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	apps.entries[0].Descriptor.Program = long
	apps.entries = append(apps.entries, fwire.ApplicationEntry{Descriptor: fwire.ApplicationDescriptor{Name: "engine", Program: engine, Title: "Engine host"}, Scope: fwire.ScopeLocal})
	short := factsShortPath(t, long)
	other := filepath.Join(t.TempDir(), "other.exe")
	account := probe.Self().Account
	mine := rightsclient.Subject{Account: account, Program: short}
	rules := []rightsclient.PolicyRule{
		{Subject: mine, Action: "abstraction.model/lookup", Resource: "hf", Permit: true},
		{Subject: rightsclient.Subject{Account: account, Program: other}, Action: "abstraction.model/lookup", Resource: "hf", Permit: true},
		{Subject: rightsclient.Subject{Account: "someone else", Program: long}, Action: "abstraction.model/lookup", Resource: "civitai", Permit: true},
		{Subject: mine, Action: asks.AnswerAction, Resource: rights.MetaScopeApps, Permit: true},
	}
	savedRules, savedInference := assistantRuleOperator, appInferenceReader
	t.Cleanup(func() { assistantRuleOperator, appInferenceReader = savedRules, savedInference })
	assistantRuleOperator = func() assistantRules { return factsRules{rules: rules} }
	now := time.Now().UnixMilli()
	appInferenceReader = func(context.Context) (appInferenceRead, error) {
		return factsInference{
			entries: []iclient.AuditEntry{
				{UnixMs: now - 2000, Program: short, Host: "engine-host", Model: "llama", Outcome: "completed"},
				{UnixMs: now - 1000, Program: long, Host: "10.0.0.7:8080", Model: "qwen", Outcome: "completed"},
				{UnixMs: now, Program: other, Host: "engine-host", Model: "secret", Outcome: "completed"},
			},
			hosts: []iclient.HostState{{Entry: iclient.HostEntry{Name: "engine-host", DeclaredBy: factsShortPath(t, engine)}}},
		}, nil
	}
	rec := appPageGet(t, p, "/app/example/facts?k="+appPageKey)
	if rec.Code != 200 {
		t.Fatalf("facts: %d %s", rec.Code, rec.Body)
	}
	var facts appFacts
	if err := json.Unmarshal(rec.Body.Bytes(), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.RulesOutcome != "page" || len(facts.Rules) != 1 || facts.Rules[0].Resource != "hf" || facts.Rules[0].Subject.Program != short {
		t.Errorf("rules %+v", facts.Rules)
	}
	if facts.Assistant == nil || facts.Assistant.Program != short {
		t.Errorf("the answering rule under the short alias makes the app the assistant: %+v", facts.Assistant)
	}
	want := []appCall{{Model: "qwen", Server: "Model server", UnixMs: now - 1000, Outcome: "completed"}, {Model: "llama", Server: "Engine host", UnixMs: now - 2000, Outcome: "completed"}}
	if facts.CallsOutcome != "page" || len(facts.Calls) != 2 || facts.Calls[0] != want[0] || facts.Calls[1] != want[1] {
		t.Errorf("calls %+v", facts.Calls)
	}
	if strings.Contains(rec.Body.String(), "10.0.0.7") || strings.Contains(rec.Body.String(), "engine-host") {
		t.Error("a raw host reached the page")
	}
	if rec := appPageGet(t, p, "/app/nothing/facts?k="+appPageKey); !strings.Contains(rec.Body.String(), `"outcome":"missing"`) {
		t.Errorf("a missing app: %s", rec.Body)
	}
	if rec := appPageGet(t, p, "/app/example/facts"); rec.Code != 403 {
		t.Errorf("facts need the Panel key: %d", rec.Code)
	}
}

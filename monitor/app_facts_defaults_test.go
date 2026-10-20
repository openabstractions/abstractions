package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// fakeAppDefaults lists defaults and explains each target as the runtime
// would: explained maps "action resource" to the step and outcome.
type fakeAppDefaults struct {
	page      rwire.DefaultPage
	err       error
	explained map[string]rwire.DecisionExplanation
	asked     []string
}

func (f *fakeAppDefaults) ListDefaultsContext(context.Context) (rwire.DefaultPage, error) {
	return f.page, f.err
}

func (f *fakeAppDefaults) ExplainForContext(_ context.Context, s rightsclient.Subject, action, resource string) (rwire.DecisionExplanation, error) {
	f.asked = append(f.asked, action+" "+resource)
	if e, ok := f.explained[action+" "+resource]; ok {
		return e, nil
	}
	none := rwire.DecisionSourceNone
	return rwire.DecisionExplanation{Outcome: rwire.DecisionOutcomeNotGranted, PolicyRevision: "r1", Source: &none}, nil
}

func withAppDefaults(t *testing.T, f appDefaultsRead) {
	t.Helper()
	previous := appDefaultsReader
	appDefaultsReader = func(context.Context) (appDefaultsRead, error) { return f, nil }
	t.Cleanup(func() { appDefaultsReader = previous })
}

func explainedAs(outcome rwire.DecisionOutcome, source rwire.DecisionSource, name string) rwire.DecisionExplanation {
	return rwire.DecisionExplanation{Outcome: outcome, PolicyRevision: "r1", Source: &source, DefaultName: name}
}

// The app page lists a default only where the runtime explains the app's
// decision on its target as that default permitting (RIGHTS-O14): a typed
// Block, the app's own rule and a switched-off default all keep it off the
// page; an every-resource default is asked on the app's own entry; a
// default the runtime keeps from the program reads as a script host; a
// refusal or an unread rule list lists nothing.
func TestAppDefaultFactsListWhatTheRuntimeExplains(t *testing.T) {
	defaults := &fakeAppDefaults{
		page: rwire.DefaultPage{Outcome: rwire.PolicyPageOutcomePage, Revision: "r1", Defaults: []rwire.AccountDefault{
			{Name: "announce", Action: "abstraction.facade/application.announce", EveryResource: true, On: true},
			{Name: "ask", Action: "abstraction.asks/question.ask", Resource: "account", On: false},
			{Name: "download", Action: "abstraction.job/acceptance.submit", Resource: "download-jobs", On: true},
			{Name: "notify", Action: "abstraction.asks/notification.post", Resource: "account", On: true},
			{Name: "register", Action: "abstraction.facade/application.register_self", Resource: "account", On: true},
		}},
		explained: map[string]rwire.DecisionExplanation{
			"abstraction.facade/application.announce app:pretend-lemonade": explainedAs(rwire.DecisionOutcomePermitted, rwire.DecisionSourceAccountDefault, "announce"),
			"abstraction.job/acceptance.submit download-jobs":              explainedAs(rwire.DecisionOutcomePermitted, rwire.DecisionSourceAccountDefault, "download"),
			// A typed Block for the app decides before the default.
			"abstraction.asks/notification.post account": explainedAs(rwire.DecisionOutcomeDenied, rwire.DecisionSourceTypedBlock, ""),
			// The app's own rule, saved under another spelling of its program.
			"abstraction.facade/application.register_self account": explainedAs(rwire.DecisionOutcomePermitted, rwire.DecisionSourceOwnRule, ""),
		},
	}
	withAppDefaults(t, defaults)
	program := filepath.Join(t.TempDir(), "Lemonade", "lemonade.exe")
	entry := fwire.ApplicationEntry{Descriptor: fwire.ApplicationDescriptor{Name: "pretend-lemonade", Program: program, Title: "Pretend Lemonade"}}
	facts := appFacts{RulesOutcome: "page", Defaults: []appDefault{}}
	appDefaultFacts(context.Background(), entry, &facts)
	if facts.DefaultsOutcome != "page" || facts.ScriptHost || facts.Subject == nil {
		t.Fatalf("facts %+v", facts)
	}
	want := []appDefault{
		{Name: "announce", Label: "Say when it is running", Action: "abstraction.facade/application.announce", Resource: "app:pretend-lemonade"},
		{Name: "download", Label: "Download in the background", Action: "abstraction.job/acceptance.submit", Resource: "download-jobs"},
	}
	if len(facts.Defaults) != len(want) || facts.Defaults[0] != want[0] || facts.Defaults[1] != want[1] {
		t.Fatalf("defaults %+v, want %+v", facts.Defaults, want)
	}
	if len(defaults.asked) != 5 {
		t.Fatalf("the runtime was asked about %v; every configured default is its to decide", defaults.asked)
	}

	// The runtime keeps the defaults from a script host.
	excluded := &fakeAppDefaults{page: defaults.page, explained: map[string]rwire.DecisionExplanation{
		"abstraction.job/acceptance.submit download-jobs": explainedAs(rwire.DecisionOutcomeNotGranted, rwire.DecisionSourceDefaultExcluded, "download"),
	}}
	withAppDefaults(t, excluded)
	host := appFacts{RulesOutcome: "page", Defaults: []appDefault{}}
	appDefaultFacts(context.Background(), entry, &host)
	if !host.ScriptHost || host.DefaultsOutcome != "excluded" || len(host.Defaults) != 0 {
		t.Fatalf("excluded %+v", host)
	}

	// A refusal explains nothing; unread rules list nothing.
	forbidden := &fakeAppDefaults{page: defaults.page, explained: map[string]rwire.DecisionExplanation{
		"abstraction.facade/application.announce app:pretend-lemonade": {Outcome: rwire.DecisionOutcomeForbidden},
	}}
	withAppDefaults(t, forbidden)
	refused := appFacts{RulesOutcome: "page", Defaults: []appDefault{}}
	appDefaultFacts(context.Background(), entry, &refused)
	if refused.DefaultsOutcome != "forbidden" || len(refused.Defaults) != 0 {
		t.Fatalf("refused %+v", refused)
	}
	unread := appFacts{RulesOutcome: "unavailable", Defaults: []appDefault{}}
	appDefaultFacts(context.Background(), entry, &unread)
	if unread.DefaultsOutcome != "unavailable" || len(unread.Defaults) != 0 {
		t.Fatalf("rules unread %+v", unread)
	}
	withAppDefaults(t, &fakeAppDefaults{err: errors.New("down")})
	failed := appFacts{RulesOutcome: "page", Defaults: []appDefault{}}
	appDefaultFacts(context.Background(), entry, &failed)
	if failed.DefaultsOutcome != "unavailable" || len(failed.Defaults) != 0 {
		t.Fatalf("defaults unread %+v", failed)
	}
}

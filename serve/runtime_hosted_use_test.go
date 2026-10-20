package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// hostedUseFixture is the runtime's rights and credentials deciders with one
// hosted server, deepseek, whose declaration names the Panel's key
// oa-panel-deepseek. asked records every first-use question admitted.
func hostedUseFixture(t *testing.T) (*runtimeCredentials, rwire.Subject, *[][2]string) {
	t.Helper()
	r, a, _ := inputAsks(t)
	if err := r.policy.RegisterAction(credentials.ActionApply); err != nil {
		t.Fatal(err)
	}
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
	if err != nil {
		t.Fatal(err)
	}
	r.policy.Definitions = catalog
	asked := &[][2]string{}
	r.firstUse = func(s rwire.Subject, publisher func() asks.Publisher, action, resource string) string {
		*asked = append(*asked, [2]string{action, resource})
		return a.admitFirstUse(s, publisher, action, resource)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &runtimeCredentials{runtimeRights: r, hostedCredential: func(host string) string {
		if host == "deepseek" {
			return "oa-panel-deepseek"
		}
		return ""
	}}
	return c, rwire.Subject{Account: r.owner, Program: identity.CanonicalProgramPath(filepath.Clean(exe))}, asked
}

// OpenCode's first call to a hosted server asks one question, about the
// account the server spends, and the person's Allow lets both checks pass
// (architect ruling C, 2026-11-08). Asking again replays the same question.
func TestHostedServerFirstUseAsksOnceAboutItsAccount(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	subject := inference.Subject{Account: bound.Account, Program: bound.Program}
	account := credentials.ResourceFor("oa-panel-deepseek")
	var ids []string
	for range 2 {
		collector := &inference.Asked{}
		word, err := c.decideInference(inference.WithAsked(context.Background(), collector), subject, inference.ActionComplete, "host:deepseek")
		if err != nil || word != rwire.DecisionOutcomeNotGranted.String() {
			t.Fatalf("first use %q %v", word, err)
		}
		ids = append(ids, collector.IDs()...)
	}
	if len(*asked) != 2 || (*asked)[0] != [2]string{credentials.ActionApply, account} || (*asked)[1] != (*asked)[0] {
		t.Fatalf("questions %q, want the account's question each time", *asked)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("question ids %q, want one question asked once", ids)
	}
	if err := c.policy.Set(bound, credentials.ActionApply, account, true); err != nil {
		t.Fatal(err)
	}
	if word, err := c.decideInference(context.Background(), subject, inference.ActionComplete, "host:deepseek"); err != nil || word != rwire.DecisionOutcomePermitted.String() {
		t.Fatalf("after Allow %q %v", word, err)
	}
	if refusal, err := c.standingInference(context.Background(), subject, "deepseek", ""); err != nil || refusal != "" {
		t.Fatalf("standing after Allow %q %v", refusal, err)
	}
	// An explicit complete rule on the server still decides first.
	if err := c.policy.Set(bound, inference.ActionComplete, "host:deepseek", false); err != nil {
		t.Fatal(err)
	}
	if word, _ := c.decideInference(context.Background(), subject, inference.ActionComplete, "host:deepseek"); word != rwire.DecisionOutcomeDenied.String() {
		t.Fatalf("complete deny %q", word)
	}
}

// Don't allow is the account's deny: remembered, and asked no more.
func TestHostedServerDontAllowIsRemembered(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	subject := inference.Subject{Account: bound.Account, Program: bound.Program}
	if err := c.policy.Set(bound, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek"), false); err != nil {
		t.Fatal(err)
	}
	collector := &inference.Asked{}
	word, err := c.decideInference(inference.WithAsked(context.Background(), collector), subject, inference.ActionComplete, "host:deepseek")
	if err != nil || word != rwire.DecisionOutcomeDenied.String() || len(*asked) != 0 || len(collector.IDs()) != 0 {
		t.Fatalf("after Don't allow %q %v, questions %q", word, err, *asked)
	}
	if refusal, _ := c.standingInference(context.Background(), subject, "deepseek", ""); refusal != "rights:denied" {
		t.Fatalf("standing %q", refusal)
	}
}

// A local server keeps its own complete question.
func TestLocalServerKeepsItsCompleteQuestion(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	subject := inference.Subject{Account: bound.Account, Program: bound.Program}
	if word, _ := c.decideInference(inference.WithAsked(context.Background(), &inference.Asked{}), subject, inference.ActionComplete, "host:modelhost"); word != rwire.DecisionOutcomeNotGranted.String() {
		t.Fatalf("local %q", word)
	}
	if len(*asked) != 1 || (*asked)[0] != [2]string{inference.ActionComplete, "host:modelhost"} {
		t.Fatalf("questions %q", *asked)
	}
}

// OpenAbstractions' own read of a key the person just entered (the router's
// model listing) decides quietly: no question appears (architect ruling C).
func TestOwnKeyCheckAsksNobody(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	word, err := c.decideQuietly(context.Background(), cwire.Subject{Account: bound.Account, Program: bound.Program}, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek"))
	if err != nil || word != rwire.DecisionOutcomeNotGranted.String() || len(*asked) != 0 {
		t.Fatalf("quiet decision %q %v, questions %q", word, err, *asked)
	}
	if word, _ := c.decide(context.Background(), cwire.Subject{Account: bound.Account, Program: bound.Program}, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek")); word != rwire.DecisionOutcomeNotGranted.String() || len(*asked) != 1 {
		t.Fatalf("the asking decision is unchanged: %q %q", word, *asked)
	}
}

// The DeepSeek preset serves chat only. R10 registered chat, embed,
// transcription, speech and image for it; an existing registration is read
// within the preset, and a new one writes only chat.
func TestHostedPresetServerRegistersOnlyItsProfiles(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"
	all := []string{"chat", "embed", "transcription", "speech", "image"}
	for name, declared := range map[string][]string{"new": nil, "existing": all} {
		if got := entryProfiles(true, "openai-compatible", deepseek, declared); !slices.Equal(got, []string{"chat"}) {
			t.Fatalf("%s DeepSeek server serves %v", name, got)
		}
	}
	if got := entryProfiles(true, "openai-compatible", "https://llm.example.com/v1", nil); !slices.Equal(got, all) {
		t.Fatalf("a custom hosted server keeps its wire's profiles: %v", got)
	}
	if got := entryProfiles(false, "llamacpp", "http://127.0.0.1:8080", nil); !slices.Equal(got, all) {
		t.Fatalf("a local server keeps its profiles: %v", got)
	}
	file := newHostFile(iwire.HostEntry{Name: "deepseek", Hosted: true, Kind: "openai-compatible", Base: deepseek, Credential: "oa-panel-deepseek"}, "operator")
	if entry := file.hostEntry(); !slices.Equal(entry.Profiles, []string{"chat"}) {
		t.Fatalf("stored entry %v", entry.Profiles)
	}
	host, err := file.routerHost()
	if err != nil || !slices.Equal(host.HostProfiles(), []string{"chat"}) {
		t.Fatalf("router host %v %v", host, err)
	}
}

// twoAccountRouter is a router with two hosted servers serving deepseek-chat,
// in the order named, each under its own declared key, and nothing local.
// Its standing reader is the runtime's, for the subject bound.
func twoAccountRouter(t *testing.T, c *runtimeCredentials, bound rwire.Subject, order ...string) *router.Router {
	t.Helper()
	listing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"deepseek-chat"}]}`))
	}))
	t.Cleanup(listing.Close)
	credential := map[string]string{"deepseek": "oa-panel-deepseek", "second": "oa-panel-second"}
	c.hostedCredential = func(host string) string { return credential[host] }
	var hosts []*router.Host
	for _, name := range order {
		hosts = append(hosts, router.NewHosted(name, listing.URL+"/v1", router.WireOpenAICompatible, credential[name]))
	}
	r := router.New(hosts...)
	r.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	r.Survey()
	subject := inference.Subject{Account: bound.Account, Program: bound.Program}
	r.UseHostedStanding(func(ctx context.Context, host string) router.HostedStanding {
		return c.hostedStandingOf(ctx, subject, host)
	})
	return r
}

// routeAndDecide routes deepseek-chat across both accounts for the caller and
// decides complete on the server taken, as admission does. It returns the
// server and the decision.
func routeAndDecide(t *testing.T, c *runtimeCredentials, r *router.Router, bound rwire.Subject) (string, string) {
	t.Helper()
	allowed := []string{"deepseek", "second"}
	d, _ := r.RouteFor(context.Background(), router.Request{Model: "deepseek-chat", Hosts: &allowed})
	if d.Verdict != router.Hosted {
		t.Fatalf("verdict %s: %s", d.Verdict, d.Why)
	}
	subject := inference.Subject{Account: bound.Account, Program: bound.Program}
	word, err := c.decideInference(inference.WithAsked(context.Background(), &inference.Asked{}), subject, inference.ActionComplete, inference.ResourceHost(d.Host))
	if err != nil {
		t.Fatal(err)
	}
	return d.Host, word
}

// A program the person allowed on DeepSeek is routed to DeepSeek when a
// second account serves the same model and ranks first; nobody is asked about
// the second account.
func TestHostedRouteTakesTheAllowedAccountWithoutAsking(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	r := twoAccountRouter(t, c, bound, "second", "deepseek")
	if err := c.policy.Set(bound, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek"), true); err != nil {
		t.Fatal(err)
	}
	host, word := routeAndDecide(t, c, r, bound)
	if host != "deepseek" || word != rwire.DecisionOutcomePermitted.String() || len(*asked) != 0 {
		t.Fatalf("routed to %s, %s, questions %q", host, word, *asked)
	}
}

// With neither account allowed the ordinary ranking stands and the account
// taken asks its one question. Reading the standings asked nothing.
func TestHostedRouteWithNoAllowedAccountAsksOnce(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	r := twoAccountRouter(t, c, bound, "second", "deepseek")
	host, word := routeAndDecide(t, c, r, bound)
	if host != "second" || word != rwire.DecisionOutcomeNotGranted.String() {
		t.Fatalf("routed to %s, %s", host, word)
	}
	if len(*asked) != 1 || (*asked)[0] != [2]string{credentials.ActionApply, credentials.ResourceFor("oa-panel-second")} {
		t.Fatalf("questions %q, want the second account's one question", *asked)
	}
}

// A denied account is passed over for an undecided one, which asks.
func TestHostedRoutePassesOverADeniedAccount(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	r := twoAccountRouter(t, c, bound, "deepseek", "second")
	if err := c.policy.Set(bound, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek"), false); err != nil {
		t.Fatal(err)
	}
	host, word := routeAndDecide(t, c, r, bound)
	if host != "second" || word != rwire.DecisionOutcomeNotGranted.String() {
		t.Fatalf("routed to %s, %s", host, word)
	}
	if len(*asked) != 1 || (*asked)[0] != [2]string{credentials.ActionApply, credentials.ResourceFor("oa-panel-second")} {
		t.Fatalf("questions %q", *asked)
	}
	// A complete deny of the program's own on a server ranks it last too.
	if err := c.policy.Set(bound, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek"), true); err != nil {
		t.Fatal(err)
	}
	if err := c.policy.Set(bound, inference.ActionComplete, inference.ResourceHost("deepseek"), false); err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: bound.Account, Program: bound.Program}
	if got := c.hostedStandingOf(context.Background(), subject, "deepseek"); got != router.HostedRefused {
		t.Fatalf("standing %v", got)
	}
}

// A caller ctx binds no peer for keeps the ordinary order.
func TestHostedStandingWithoutAPeerIsUndecided(t *testing.T) {
	c, bound, asked := hostedUseFixture(t)
	if err := c.policy.Set(bound, credentials.ActionApply, credentials.ResourceFor("oa-panel-deepseek"), true); err != nil {
		t.Fatal(err)
	}
	if got := c.hostedStanding(context.Background(), "deepseek"); got != router.HostedUndecided || len(*asked) != 0 {
		t.Fatalf("standing %v, questions %q", got, *asked)
	}
}

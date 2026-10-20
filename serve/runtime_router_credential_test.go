package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// A hosted listing refused for want of its credential is backed off by the
// router's clock. Storing the credential is a host change for that host: its
// listing is read before the store returns, so the person's own key shows its
// models at once, not after a tick or the backoff (router CONTRACT.md
// ROUTE-D12).
func TestStoringACredentialReadsItsHostedListingAtOnce(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionProviderManage, inference.ActionHostManage, inference.ActionComplete, credentials.ActionApply},
		[]installationRule{{inference.ActionHostManage, credentials.ResourceAccount}}); err != nil {
		t.Fatal(err)
	}
	var stored atomic.Bool
	var listings atomic.Int64
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		listings.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"deepseek-fixture"}]}`))
	}))
	defer fixture.Close()
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), supervisors: map[string]*providerSupervisor{},
		changes: make(chan struct{}), report: func(err error) { t.Errorf("provider: %v", err) }}
	ceilings, err := inference.OpenCeilings(filepath.Join(t.TempDir(), "spend.json"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	modelRouter := router.New()
	// The holder has no value until the person stores the key.
	modelRouter.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		if !stored.Load() {
			return nil, &router.CredentialRefusal{Outcome: "unknown", Name: "deepseek"}
		}
		return map[string]string{"Authorization": "Bearer fixture-secret"}, nil
	})
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p, router: modelRouter, ceilings: ceilings,
		report: func(error) {}}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
	entry := iwire.HostEntry{Name: "deepseek", Hosted: true, Kind: router.WireOpenAICompatible, Base: fixture.URL, Credential: "deepseek", Profiles: []string{"chat"}}
	if change := o.AddHost(context.Background(), caller, revision, entry); change.Outcome != iwire.EditOutcomeApplied {
		t.Fatalf("add outcome %+v", change)
	}
	offered := func() bool {
		decision, _ := modelRouter.Route(router.Request{Model: "deepseek-fixture"})
		return decision != nil && decision.Verdict == router.Hosted
	}
	if offered() {
		t.Fatal("a listing without its credential was offered")
	}
	// A slow clock: no tick reads the listing during this test.
	stop := make(chan struct{})
	polled := make(chan struct{})
	go func() {
		defer close(polled)
		modelRouter.Poll(time.Hour, stop)
	}()
	defer func() { close(stop); <-polled }()

	// The key is in the holder, but nothing told the router: the clock
	// keeps backing off from the failed listing.
	stored.Store(true)
	time.Sleep(200 * time.Millisecond)
	if offered() || listings.Load() != 0 {
		t.Fatalf("the clock read a backed-off listing: offered=%v listings=%d", offered(), listings.Load())
	}
	if err := o.credentialStored(context.Background(), cwire.Subject{Account: r.owner, Program: r.operators[0]}, "deepseek"); err != nil {
		t.Fatal(err)
	}
	if !offered() || listings.Load() != 1 {
		t.Fatalf("the stored credential's listing was not read before the store returned: offered=%v listings=%d", offered(), listings.Load())
	}
}

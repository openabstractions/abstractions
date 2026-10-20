package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestPairedStandingBusyAddAndFinalRemovalEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	rights, _, _, _ := incomingFixture(t)
	state := pairingStateFixture(t)
	cert, tr := pairingStatePeer(t)
	peer := pairingStateAdmission(cert)
	revision, _ := pairingStateRead(t, state)
	revision, err := state.stage(ctx, revision, cert, tr, "A", "")
	if err != nil {
		t.Fatal(err)
	}
	revision, err = state.activate(ctx, revision, pairingKey(cert), func(context.Context, pairingPeerRecord) (string, error) { return "owned fixture effect", nil })
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: rights.owner, Program: "remote:" + pairingKey(cert)}
	for _, rule := range []struct{ action, resource string }{{remoteServeAction, "account"}, {inference.ActionComplete, inference.ResourceHost("owned")}} {
		if err = rights.policy.Set(subject, rule.action, rule.resource, true); err != nil {
			t.Fatal(err)
		}
	}
	minimum := rights.policy.DecideForAtLeastRevisionContext(ctx, subject, remoteServeAction, "account", "").ObservedRevision
	if minimum == "" {
		t.Fatal("missing ordered observation")
	}
	started, stopped := make(chan struct{}, 1), make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"owned\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		started <- struct{}{}
		<-r.Context().Done()
		stopped <- struct{}{}
	}))
	defer backend.Close()
	credentials := &runtimeCredentials{runtimeRights: rights}
	provider, err := inference.New(inference.Config{Router: router.New(router.NewHosted("owned", backend.URL, router.WireOpenAICompatible, "")), Decide: rights.decideInference, Standing: credentials.standingInference, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		t.Error("unexpected fixture credential application")
		return nil, "unavailable"
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	state.onChanged = func() { provider.Recheck(ctx) }
	bound := inference.Subject{Account: subject.Account, Program: subject.Program}
	var injectBusy atomic.Bool
	var admissionReads atomic.Int32
	admit := func(peer remote.Peer) error {
		if injectBusy.Load() && admissionReads.Add(1) == 2 {
			return errPairingBusy
		}
		return state.admit(peer)
	}
	ingress := context.WithValue(ctx, incomingRemoteKey{}, incomingRemoteCall{peer: peer, admit: admit, minimum: minimum, account: subject.Account, program: subject.Program})
	accepted := provider.Start(ingress, bound, iwire.Request{Model: "fixture-model", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "owned"}}}}})
	if accepted.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("actual provider start: %v", accepted.Outcome)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	assertAlive := func() {
		t.Helper()
		if page := provider.Observe(ctx, bound, accepted.Operation, 0, 32, 4096, 0); page.AtEnd {
			t.Fatal("transient busy ended A")
		}
	}
	// The first standing admission succeeds, the admission inside the actual
	// decision observes contention, and a later read would succeed again.
	// Preserve that exact uncertainty instead of treating Forbidden as removal.
	injectBusy.Store(true)
	if err := provider.RecheckChecked(ctx); err == nil {
		t.Fatal("success-to-busy admission reported settled withdrawal")
	}
	injectBusy.Store(false)
	assertAlive()
	other, otherT := pairingStatePeer(t)
	revision, err = state.stage(ctx, revision, other, otherT, "B", "")
	if err != nil {
		t.Fatal(err)
	}
	revision, err = state.activate(ctx, revision, pairingKey(other), func(context.Context, pairingPeerRecord) (string, error) {
		provider.Recheck(ctx)
		assertAlive()
		return "B actual controlled effect", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertAlive()
	// The only withdrawal event is invoked while the state is busy. The
	// owner's required post-unlock recheck must deliver the final removal.
	_, err = state.change(ctx, revision, func(doc *pairingDocument) error {
		p := doc.Peers[pairingKey(cert)]
		p.State = "removing"
		doc.Peers[pairingKey(cert)] = p
		provider.Recheck(ctx)
		assertAlive()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("final revoke event did not stop A")
	}
	if err = state.admit(peer); err == nil {
		t.Fatal("removed key admitted")
	}
}

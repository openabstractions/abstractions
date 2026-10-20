package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

type machineVisibilityListFixture struct {
	mwire.MachineControl
	page mwire.MachinePage
}

func TestPairingListCurrentReceiverObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := pairingStateFixture(t)
	p := &runtimePairing{ctx: ctx, state: s, epoch: strings.Repeat("e", 32), started: time.Now(), normalAddress: "192.168.10.20:54321", stopNormal: func() {}}
	read := func() string { return p.List(ctx, "", 64, func() error { return nil }).LocalServiceAddress }
	if read() != "" {
		t.Fatal("inactive receiver was advertised")
	}
	p.normalActive = true
	if read() != p.normalAddress {
		t.Fatal("active receiver absent")
	}
	p.normalActive = false
	if read() != "" || p.normalAddress != "192.168.10.20:54321" {
		t.Fatal("observation changed address conflict state")
	}
	p.normalActive = true
	p.exposureFailed = true
	if read() != "" {
		t.Fatal("failed exposure was advertised")
	}
	p.exposureFailed = false
	cancel()
	if read() != "" {
		t.Fatal("cancelled runtime was advertised")
	}
}

func (f *machineVisibilityListFixture) List(string, int64) (mwire.MachinePage, error) {
	return f.page, nil
}

func TestPairingLastCallLifecycleAndRevision(t *testing.T) {
	ctx := context.Background()
	s := pairingStateFixture(t)
	cert, transcript := pairingStatePeer(t)
	key, peer := pairingKey(cert), pairingStateAdmission(cert)
	revision, _ := pairingStateRead(t, s)
	revision, err := s.stage(ctx, revision, cert, transcript, "fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	revision, err = s.activate(ctx, revision, key, func(context.Context, pairingPeerRecord) (string, error) { return "fixture-rights", nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := s.admit(peer); err != nil {
		t.Fatal(err)
	}
	_, peers := pairingStateRead(t, s)
	if s.lastCall(key, peers[key]) != 0 {
		t.Fatal("handshake created a last-call timestamp")
	}
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	observed := s.observeCall(peer)
	observed()
	time.Sleep(2 * time.Millisecond)
	newer := s.observeCall(peer)
	newer()
	_, orderingPeers := pairingStateRead(t, s)
	latest := s.lastCall(key, orderingPeers[key])
	observed()
	if s.lastCall(key, orderingPeers[key]) != latest {
		t.Fatal("late completion overwrote a newer observed call")
	}
	p := &runtimePairing{state: s, epoch: strings.Repeat("a", 32), started: time.Now()}
	page := p.List(ctx, "", 64, func() error { return nil })
	if page.Outcome != mwire.MachineListOutcomePage || len(page.Machines) != 1 || page.Machines[0].LastCallUnixMs <= 0 {
		t.Fatalf("last-call page: %+v", page)
	}
	after, err := os.ReadFile(s.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("observational call changed trust document", err)
	}
	reloaded, err := loadRuntimePairingState(ctx, filepath.Dir(s.root))
	if err != nil || reloaded.lastCall(key, peers[key]) != 0 {
		t.Fatal("restart invented an epoch timestamp", err)
	}
	removedRequest, repairedRequest := s.observeCall(peer), s.observeCall(peer)
	revision, err = s.remove(ctx, revision, key, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	observed() // Completion from the retired lifecycle cannot repopulate it.
	removedRequest()
	if s.lastCall(key, peers[key]) != 0 {
		t.Fatal("removed lifecycle retained observation")
	}
	revision, err = s.finishRemoval(ctx, revision, key, func(context.Context, pairingPeerRecord) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if s.admit(peer) == nil {
		t.Fatal("removed peer admitted")
	}
	transcript[0] ^= 1
	revision, err = s.stage(ctx, revision, cert, transcript, "replacement ceremony", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.activate(ctx, revision, key, func(context.Context, pairingPeerRecord) (string, error) { return "new-fixture-rights", nil }); err != nil {
		t.Fatal(err)
	}
	if err = s.admit(peer); err != nil {
		t.Fatal(err)
	}
	_, currentPeers := pairingStateRead(t, s)
	repairedRequest()
	if s.lastCall(key, currentPeers[key]) != 0 {
		t.Fatal("prior lifecycle completion populated replacement")
	}
	s.observeCall(peer)()
	if s.lastCall(key, currentPeers[key]) <= 0 {
		t.Fatal("replacement lifecycle did not observe admitted call")
	}
	fixture := &machineVisibilityListFixture{page: page}
	var encoded []byte
	dispatcher := &mwire.MachineControlDispatcher{Handler: fixture}
	codec := mwire.NewMachineControlClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) {
		var err error
		encoded, err = dispatcher.ExchangeFrame(frame)
		return encoded, err
	}))
	decoded, err := codec.List("", 64)
	if err != nil || decoded.Machines[0].LastCallUnixMs != page.Machines[0].LastCallUnixMs || !bytes.Contains(encoded, []byte("last_call_unix_ms")) {
		t.Fatal("new generated field lost", err)
	}
	fixture.page.Machines[0].LastCallUnixMs = 0
	legacy, err := codec.List("", 64)
	if err != nil || legacy.Machines[0].LastCallUnixMs != 0 || bytes.Contains(encoded, []byte("last_call_unix_ms")) {
		t.Fatal("absent last call acquired a value", err)
	}
}

func TestRemoteLastCallRecordsBeforeUpstreamFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rights, _, config, peer := incomingFixture(t)
	s := pairingStateFixture(t)
	_, transcript := pairingStatePeer(t)
	key := pairingKey(peer.Certificate)
	revision, _ := pairingStateRead(t, s)
	revision, err := s.stage(ctx, revision, peer.Certificate, transcript, "owned fixture", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.activate(ctx, revision, key, func(context.Context, pairingPeerRecord) (string, error) { return "fixture-rights", nil }); err != nil {
		t.Fatal(err)
	}
	config.Admit, config.observeCall = s.admit, s.observeCall
	subject := rights.self()
	subject.Program = "remote:" + key
	for _, rule := range [][2]string{{remoteServeAction, "account"}, {inference.ActionComplete, inference.ResourceHost("owned")}} {
		if err = rights.policy.Set(subject, rule[0], rule[1], true); err != nil {
			t.Fatal(err)
		}
	}
	arrived, release := make(chan struct{}), make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
			return
		}
		close(arrived)
		select {
		case <-release:
		case <-r.Context().Done():
		}
		http.Error(w, "owned upstream failure", http.StatusServiceUnavailable)
	}))
	defer backend.Close()
	releaseOnce := sync.OnceFunc(func() { close(release) })
	defer releaseOnce()
	routes := router.New(router.NewHosted("owned", backend.URL, router.WireOpenAICompatible, ""))
	provider, err := inference.New(inference.Config{Router: routes, Decide: rights.decideInference, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		t.Error("unexpected credential application")
		return nil, "unavailable"
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	prepared, err := fixturePrepareRemote(ctx, fixtureRemoteOptions(config), rights, &runtimeInference{provider: provider, router: routes})
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan iwire.Embeddings, 1)
	failed := make(chan error, 1)
	go func() {
		client := iwire.NewEmbedderClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(ctx, peer, frame) }))
		result, err := client.Embed(iwire.EmbedRequest{Model: "fixture-model", Inputs: []string{"owned fixture"}, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}})
		completed <- result
		failed <- err
	}()
	select {
	case <-arrived:
	case <-ctx.Done():
		t.Fatal("upstream dispatch did not arrive")
	}
	_, peers := pairingStateRead(t, s)
	if s.lastCall(key, peers[key]) <= 0 {
		t.Fatal("contact waited for upstream/RPC success")
	}
	// Release the owned backend without waiting for a successful RPC response.
	releaseOnce()
	select {
	case result := <-completed:
		if err := <-failed; err != nil || result.Outcome != iwire.EmbedOutcomeUnavailable {
			t.Fatalf("upstream failure: %+v %v", result, err)
		}
	case <-ctx.Done():
		t.Fatal("owned failure did not settle")
	}
	if s.lastCall(key, peers[key]) <= 0 {
		t.Fatal("later upstream failure erased contact")
	}
}

func TestRemoteLastCallCountsIngressContact(t *testing.T) {
	rights, services, config, peer := incomingFixture(t)
	var calls atomic.Int32
	var preparedCalls atomic.Int32
	config.observeCall = func(remote.Peer) func() { preparedCalls.Add(1); return func() { calls.Add(1) } }
	prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services)
	if err != nil {
		t.Fatal(err)
	}
	transport := remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) })
	endpoint := fwire.NewEndpointClient(transport)
	if _, err := endpoint.Describe(); err == nil || calls.Load() != 0 {
		t.Fatal("missing permission counted")
	}
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	subject := rights.self()
	subject.Program = mapped.Program()
	if err = rights.policy.Set(subject, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	if _, err = endpoint.Describe(); err != nil || calls.Load() != 1 {
		t.Fatal("admitted Describe not counted", err)
	}
	views := routerwire.NewRouterClient(transport)
	if _, err = views.Models(false); err == nil || calls.Load() != 2 {
		t.Fatal("post-ingress resource refusal lost contact")
	}
	if err = rights.policy.Set(subject, routerservice.ActionInventory, routerservice.ResourceInventory, true); err != nil {
		t.Fatal(err)
	}
	if _, err = views.Hosts(false); err != nil || calls.Load() != 3 {
		t.Fatal("authorized inventory not counted", err)
	}
	if _, err = views.Pick(routerwire.PickRequest{Model: "unoffered"}); err == nil || calls.Load() != 3 {
		t.Fatal("unoffered method counted")
	}
	beforeMalformed := preparedCalls.Load()
	_, _ = transport.ExchangeFrame([]byte(`{"version":1,"service":"abstraction.facade/endpoint@1","method":"Describe","arguments":{"forged":true}}`))
	if calls.Load() != 4 || preparedCalls.Load() != beforeMalformed+1 {
		t.Fatal("post-ingress typed argument refusal lost contact")
	}
	peer.Key[0] ^= 1
	if _, err = endpoint.Describe(); err == nil || calls.Load() != 4 {
		t.Fatal("forged peer counted")
	}
}

func TestPairingRecordOutsideCeremonyNotifies(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "posted", true: "blocked"}[blocked], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			s := pairingStateFixture(t)
			a, book := firstUseBook(t)
			a.rights = testRights(t, t.TempDir())
			if err := a.rights.install([]string{notificationPostAction}, nil); err != nil {
				t.Fatal(err)
			}
			if err := a.rights.policy.Set(a.rights.self(), notificationPostAction, notificationPostResource, !blocked); err != nil {
				t.Fatal(err)
			}
			reported := make(chan error, 1)
			p := &runtimePairing{ctx: ctx, cancel: cancel, state: s, asks: a, rights: a.rights, epoch: strings.Repeat("c", 32), report: func(err error) { reported <- err }}
			p.startVisibility()
			cert, transcript := pairingStatePeer(t)
			key := pairingKey(cert)
			revision, _ := pairingStateRead(t, s)
			if _, err := s.stage(ctx, revision, cert, transcript, "ordinary ceremony", ""); err != nil {
				t.Fatal(err)
			}
			if history, _, err := book.NotificationHistory(); err != nil || len(history) != 0 {
				t.Fatal("trusted ceremony posted warning", err)
			}
			other, otherTranscript := pairingStatePeer(t)
			if err := cas.ChangeLimitContext(ctx, s.path, pairingStateLimit, func(raw []byte) ([]byte, error) {
				var doc pairingDocument
				if err := json.Unmarshal(raw, &doc); err != nil {
					return nil, err
				}
				doc.Peers[pairingKey(other)] = pairingPeerRecord{Certificate: other.Raw, Transcript: otherTranscript, State: "inactive", Label: "untrusted label"}
				return json.Marshal(doc)
			}); err != nil {
				t.Fatal(err)
			}
			for range 10 {
				if _, _, err := s.snapshot(ctx); err == nil {
					t.Fatal("outside record accepted")
				}
			}
			done := make(chan struct{})
			go func() { p.workers.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("notification worker did not finish")
			}
			history, _, err := book.NotificationHistory()
			if err != nil {
				t.Fatal(err)
			}
			if blocked {
				if len(history) != 0 {
					t.Fatal("notification Block bypassed")
				}
				select {
				case err := <-reported:
					if !errors.Is(err, asks.ErrNotificationSuppressed) {
						t.Fatal(err)
					}
				default:
					t.Fatal("blocked post not reported")
				}
			} else if len(history) != 1 || history[0].Request.Title != pairingStateWarningTitle || strings.Contains(history[0].Request.Body, "untrusted label") {
				t.Fatalf("bounded generic warning: %+v", history)
			}
			if s.admit(pairingStateAdmission(cert)) == nil || s.admit(pairingStateAdmission(other)) == nil {
				t.Fatal("notification enabled a changed pin", key)
			}
		})
	}
}

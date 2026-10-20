package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/machines"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// The fixture installs a canonical already-approved directional interpretation.
// The separate machine-control journey owns native consent and /2 authentication.
func TestPairedHostRoutesApprovedChatAndEmbedding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := pairingStateFixture(t)
	localTLS, _, err := state.identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	localCert, err := x509.ParseCertificate(localTLS.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	peerKeyBytes, peerDER, err := newPairingIdentity()
	if err != nil {
		t.Fatal(err)
	}
	peerTLS, err := pairingIdentity(pairingDocument{PrivateKey: peerKeyBytes, Certificate: peerDER})
	if err != nil {
		t.Fatal(err)
	}
	peerCert, err := x509.ParseCertificate(peerDER)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	keys := remote.PairKeys{Role: 1, LocalKey: sha256.Sum256(localCert.RawSubjectPublicKeyInfo), PeerKey: sha256.Sum256(peerCert.RawSubjectPublicKeyInfo)}
	initiator, err := preparePairingProposal(mwire.MachinePreparation{Request: mwire.MachineScopeKindOneHostInference, Offer: mwire.MachineScopeKindNone, BindingName: "machine-b"}, keys)
	if err != nil {
		t.Fatal(err)
	}
	responder, err := preparePairingProposal(mwire.MachinePreparation{Request: mwire.MachineScopeKindNone, Offer: mwire.MachineScopeKindOneHostInference, OfferHost: "owned", NormalServiceAddress: listener.Addr().String(), BindingName: "machine-a"}, remote.PairKeys{Role: 2, LocalKey: keys.PeerKey, PeerKey: keys.LocalKey})
	if err != nil {
		t.Fatal(err)
	}
	transcript := sha256.Sum256([]byte("owned paired route fixture"))
	consent := &pairingConsent{Operation: "fixture", Question: "fixture", Scope: "fixture", Program: "fixture", TextHash: strings.Repeat("a", 64), ExpectedRights: "fixture", Role: 1, LocalKey: keys.LocalKey, PeerKey: keys.PeerKey, Transcript: transcript, Check: "123456", Initiator: initiator, Responder: responder, NativeYes: true, TransportComplete: true}
	key := hex.EncodeToString(keys.PeerKey[:])
	revision, _ := pairingStateRead(t, state)
	_, err = state.change(ctx, revision, func(d *pairingDocument) error {
		d.Peers[key] = pairingPeerRecord{Certificate: peerDER, Transcript: transcript, State: "active", ActivationKind: "native_no_grant", BindingName: "machine-b", PeerServiceAddress: listener.Addr().String(), Consent: consent}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rights, _, config, _ := incomingFixture(t)
	clientPeer := pairingStateAdmission(localCert)
	roots := x509.NewCertPool()
	roots.AddCert(localCert)
	config.TLS = &tls.Config{Certificates: []tls.Certificate{peerTLS}, ClientCAs: roots}
	var pinned atomic.Bool
	pinned.Store(true)
	config.Admit = func(p remote.Peer) error {
		if !pinned.Load() || p.Key != clientPeer.Key {
			return remote.ErrTrust
		}
		return nil
	}
	var effects atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
			return
		}
		effects.Add(1)
		w.Write([]byte(`{"data":[{"index":0,"embedding":[1,2]}],"usage":{"prompt_tokens":1}}`))
	}))
	defer upstream.Close()
	receiverRoutes := router.New(router.NewHosted("owned", upstream.URL, router.WireOpenAICompatible, ""))
	if err := receiverRoutes.SurveyContext(ctx); err != nil {
		t.Fatal(err)
	}
	provider, err := inference.New(inference.Config{Router: receiverRoutes, Decide: rights.decideInference, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		t.Error("unexpected credential application")
		return nil, "unavailable"
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	prepared, err := fixturePrepareRemote(ctx, fixtureRemoteOptions(config), rights, &runtimeInference{provider: provider, router: receiverRoutes})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- prepared.server.Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Error(err)
		}
	})
	subject := rwire.Subject{Account: rights.owner, Program: "remote:" + hex.EncodeToString(clientPeer.Key[:])}
	for _, rule := range []struct{ action, resource string }{{remoteServeAction, "account"}, {routerservice.ActionInventory, routerservice.ResourceInventory}} {
		if err := rights.policy.Set(subject, rule.action, rule.resource, true); err != nil {
			t.Fatal(err)
		}
	}
	providers := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{})}
	pairing := &runtimePairing{ctx: ctx, state: state, rights: rights, services: &runtimeInference{providers: providers}}
	hosts, err := pairing.pairedHosts()
	if err != nil || len(hosts) != 1 {
		t.Fatalf("paired hosts: %v %v", hosts, err)
	}
	host := hosts[0]
	if !slices.Equal(host.HostProfiles(), []string{router.ProfileChat, router.ProfileEmbed}) {
		t.Fatalf("approved profiles: %v", host.HostProfiles())
	}
	if host.Name != "machine-b" || host.BindingID != "paired:"+key+":"+hex.EncodeToString(transcript[:]) {
		t.Fatal("selected route identity changed")
	}
	loaded, err := loadRuntimePairingState(ctx, filepath.Dir(state.root))
	if err != nil {
		t.Fatal(err)
	}
	restarted := &runtimePairing{ctx: ctx, state: loaded, rights: rights, services: &runtimeInference{providers: providers}}
	restored, err := restarted.pairedHosts()
	if err != nil || len(restored) != 1 || !slices.Equal(restored[0].HostProfiles(), host.HostProfiles()) {
		t.Fatalf("restored profiles: %v %v", restored, err)
	}
	routes := router.New(host)
	selected := []string{"machine-b"}
	for _, profile := range []string{router.ProfileChat, router.ProfileEmbed} {
		d, _, err := routes.RouteContext(ctx, router.Request{Model: "fixture-model", Profile: profile, Hosts: &selected, Fresh: true})
		if err != nil || d.Host != "machine-b" {
			t.Fatalf("%s route: %+v %v", profile, d, err)
		}
	}
	forwarder, err := inference.New(inference.Config{Router: routes,
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := forwarder.Close(); err != nil {
			t.Error(err)
		}
	})
	binding := inference.ExecutionBinding{Host: host.Name, BindingID: host.BindingID}
	var embed func(iwire.EmbedRequest) (iwire.Embeddings, error)
	if statusTransportProvesProgram(t) {
		endpoint := listen.Endpoint(fmt.Sprintf("paired-exact-%d", endpointSeq.Add(1)))
		mediator, err := inferenceservice.ListenMediated(endpoint, forwarder, inference.ExecutionRemote, binding)
		if err != nil {
			t.Fatal(err)
		}
		mediatedDone := make(chan error, 1)
		go func() { mediatedDone <- mediator.Serve(ctx) }()
		t.Cleanup(func() {
			//unchecked: the fixture joins Serve below, which reports its terminal error
			_ = mediator.Close()
			if err := <-mediatedDone; err != nil {
				t.Error(err)
			}
		})
		expectation := runtimeExpectation(t)
		embed = iwire.NewEmbedderClient(listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second, MaxFrame: 1 << 20, Server: &expectation}.WithContext(ctx)).Embed
	} else {
		// The measured local transport refusal leaves the remote TLS and
		// receiving rights checks testable at the provider entry point.
		_, account, err := principal()
		if err != nil {
			t.Fatal(err)
		}
		caller := inference.Subject{Account: account, Program: runtimeExpectation(t).Program}
		call := inference.WithExecutionBinding(inference.WithExecutionPlacement(ctx, inference.ExecutionRemote), binding)
		embed = func(request iwire.EmbedRequest) (iwire.Embeddings, error) {
			return forwarder.Embed(call, caller, request), nil
		}
	}
	request := iwire.EmbedRequest{Model: "fixture-model", Inputs: []string{"owned fixture"}, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}}
	assertEmbed := func(want bool) {
		t.Helper()
		before := effects.Load()
		result, e := embed(request)
		if want {
			if e != nil || result.Outcome != iwire.EmbedOutcomeCompleted || effects.Load() != before+1 {
				t.Fatalf("embedding: %+v %v effects=%d", result, e, effects.Load()-before)
			}
		} else if result.Outcome == iwire.EmbedOutcomeCompleted || effects.Load() != before {
			t.Fatalf("refused embedding: %+v %v effects=%d", result, e, effects.Load()-before)
		}
	}
	assertEmbed(false) // Selecting a machine supplies no receiving resource grant.
	if err := rights.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("owned"), true); err != nil {
		t.Fatal(err)
	}
	assertEmbed(true)
	if err := rights.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("owned"), false); err != nil {
		t.Fatal(err)
	}
	assertEmbed(false)
	if err := rights.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("owned"), true); err != nil {
		t.Fatal(err)
	}
	pinned.Store(false)
	assertEmbed(false)
	pinned.Store(true)
	revision, _ = pairingStateRead(t, state)
	if _, err = state.change(ctx, revision, func(d *pairingDocument) error { delete(d.Peers, key); return nil }); err != nil {
		t.Fatal(err)
	}
	assertEmbed(false) // A retained transport rechecks the removed sending pin.
	if hosts, err = pairing.pairedHosts(); err != nil || len(hosts) != 0 {
		t.Fatalf("removed route: %v %v", hosts, err)
	}
}

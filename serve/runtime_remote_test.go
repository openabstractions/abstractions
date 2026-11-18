package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asksapi "github.com/openabstractions/abstraction-asks/go"
	credentialsapi "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	logging "github.com/openabstractions/abstraction-logging/go"
	resourcewire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	rightsapi "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

type remoteFixtureTransport func([]byte) ([]byte, error)

func (f remoteFixtureTransport) ExchangeFrame(frame []byte) ([]byte, error) { return f(frame) }

func incomingFixture(t *testing.T) (*runtimeRights, *runtimeInference, *runtimeRemoteConfig, remote.Peer) {
	t.Helper()
	rights := testRights(t, t.TempDir())
	rights.configure(&host.Options{OnError: func(err error) { t.Error(err) }}, nil)
	if err := rights.install([]string{remoteServeAction, inference.ActionComplete, routerservice.ActionInventory}, nil); err != nil {
		t.Fatal(err)
	}
	tlsConfig, digest, trust := remoteTrustFiles(t, t.TempDir())
	data, err := os.ReadFile(trust.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("fixture certificate missing")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	peer := remote.Peer{Certificate: certificate, Key: sha256.Sum256(certificate.RawSubjectPublicKeyInfo)}
	config := &runtimeRemoteConfig{Listen: true, Address: "127.0.0.1:0", TLS: tlsConfig, Admit: func(p remote.Peer) error {
		if p.Key != digest {
			return remote.ErrTrust
		}
		return nil
	}}
	routes := router.New()
	provider, err := inference.New(inference.Config{Router: routes, Decide: rights.decideInference, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		t.Error("unexpected credential application")
		return nil, "unavailable"
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	return rights, &runtimeInference{provider: provider, router: routes}, config, peer
}

func fixtureRemoteOptions(config *runtimeRemoteConfig) runtimeFlags {
	return runtimeFlags{remoteConfiguration: func(context.Context) (*runtimeRemoteConfig, error) { return config, nil }}
}

func TestRemoteRuntimeTransportBudgets(t *testing.T) {
	rights, services, config, _ := incomingFixture(t)
	prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services)
	if err != nil {
		t.Fatal(err)
	}
	s := prepared.server
	if s.Timeout != 35*time.Second || s.HandshakeTimeout != 5*time.Second ||
		s.MaxConnections != 64 || s.MaxSourceConnections != 4 || s.MaxPeerCalls != 8 ||
		s.PeerRequestsPerSecond != 32 || s.PeerBurst != 64 {
		t.Fatalf("runtime transport budgets: %+v", s)
	}
}

func TestRemoteRuntimeOffAndInvalidConfigurationNeverListen(t *testing.T) {
	rights, services, config, _ := incomingFixture(t)
	for _, options := range []runtimeFlags{{}, fixtureRemoteOptions(nil), fixtureRemoteOptions(&runtimeRemoteConfig{})} {
		prepared, err := fixturePrepareRemote(context.Background(), options, nil, nil)
		if err != nil || prepared != nil {
			t.Fatalf("off: %v %v", prepared, err)
		}
	}
	for _, mutate := range []func(*runtimeRemoteConfig){
		func(c *runtimeRemoteConfig) { c.TLS = nil },
		func(c *runtimeRemoteConfig) { c.Admit = nil },
		func(c *runtimeRemoteConfig) { c.Address = "" },
	} {
		invalid := *config
		mutate(&invalid)
		if _, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(&invalid), rights, services); err == nil {
			t.Fatal("invalid incoming configuration accepted")
		}
	}
	if _, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), nil, services); err == nil {
		t.Fatal("nil rights accepted")
	}
	if err := os.Remove(rights.path); err != nil {
		t.Fatal(err)
	}
	if _, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services); err == nil {
		t.Fatal("missing rights accepted")
	}
	// start repeats the shared pure guard before touching its listener factory.
	calls := 0
	prepared := &runtimeRemote{server: remote.Server{}}
	_, err := prepared.start(context.Background(), func(error) { t.Fatal("unexpected worker") }, func(string, string) (net.Listener, error) { calls++; return nil, errors.New("unexpected bind") })
	if err == nil || calls != 0 {
		t.Fatalf("invalid prebind: %v calls=%d", err, calls)
	}
}

func TestRemoteRuntimeDescribeUsesLiveGrantAndExplicitServices(t *testing.T) {
	rights, services, config, peer := incomingFixture(t)
	prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services)
	if err != nil {
		t.Fatal(err)
	}
	client := fwire.NewEndpointClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	if _, err := client.Describe(); err == nil {
		t.Fatal("missing remote grant accepted")
	}
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: rights.owner, Program: mapped.Program()}
	if err := rights.policy.Set(subject, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	description, err := client.Describe()
	if err != nil {
		t.Fatal(err)
	}
	if len(description.Services) != 3 {
		t.Fatalf("offered services: %+v", description.Services)
	}
	for _, service := range description.Services {
		switch service.Contract {
		case "abstraction.inference/chat@1", "abstraction.inference/embed@1", "abstraction.router/router@1":
		default:
			t.Fatalf("extra offered service: %+v", service)
		}
	}
	views := routerwire.NewRouterClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	if _, err := views.Models(false); err == nil {
		t.Fatal("remote.serve granted router inventory")
	}
	if err := rights.policy.Set(subject, routerservice.ActionInventory, routerservice.ResourceInventory, true); err != nil {
		t.Fatal(err)
	}
	if _, err := views.Models(false); err != nil {
		t.Fatal(err)
	}
	if _, err := views.Hosts(false); err != nil {
		t.Fatal(err)
	}
	if _, err := views.Pick(routerwire.PickRequest{Model: "unserved"}); err == nil {
		t.Fatal("router route method served")
	}
	if len(services.router.Asked()) != 0 {
		t.Fatal("unoffered route reached router")
	}
	if err := rights.policy.Set(subject, remoteServeAction, "account", false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Describe(); err == nil {
		t.Fatal("changed grant retained")
	}
	config.Admit = func(remote.Peer) error { return remote.ErrTrust }
	// The frozen callback closes over the live book, rather than this config field.
	peer.Key[0] ^= 1
	if _, err := prepared.server.Handler(context.Background(), peer, nil); err == nil {
		t.Fatal("changed key admitted")
	}
}

func TestRemoteInferenceContextBindsSubjectAndMinimum(t *testing.T) {
	rights, _, config, peer := incomingFixture(t)
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	subject := inference.Subject{Account: rights.owner, Program: mapped.Program()}
	if result, _ := rights.decideInference(context.Background(), subject, inference.ActionComplete, inference.ResourceHost("owned")); result != "unavailable" {
		t.Fatal("legacy remote took ordinary decision path")
	}
	resource := inference.ResourceHost("owned")
	if err := rights.policy.Set(rwire.Subject{Account: subject.Account, Program: subject.Program}, inference.ActionComplete, resource, true); err != nil {
		t.Fatal(err)
	}
	call := incomingRemoteCall{peer: peer, admit: config.Admit, account: subject.Account, program: subject.Program}
	ctx := context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if result, _ := rights.decideInference(ctx, subject, inference.ActionComplete, resource); result != "permitted" {
		t.Fatalf("exact owner: %s", result)
	}
	observation := rights.policy.DecideForAtLeastRevisionContext(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, inference.ActionComplete, resource, "").ObservedRevision
	if observation == "" {
		t.Fatal("current revision observation missing")
	}
	call.minimum = observation
	ctx = context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if result, _ := rights.decideInference(ctx, subject, inference.ActionComplete, resource); result != "permitted" {
		t.Fatalf("current minimum: %s", result)
	}
	other, _, _, otherPeer := incomingFixture(t)
	otherSubject := rwire.Subject{Account: other.owner, Program: "remote:" + fmt.Sprintf("%x", otherPeer.Key)}
	foreignMinimum := other.policy.DecideForAtLeastRevisionContext(context.Background(), otherSubject, inference.ActionComplete, resource, "").ObservedRevision
	if foreignMinimum == "" {
		t.Fatal("independent epoch observation missing")
	}
	call.minimum = foreignMinimum
	ctx = context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if result, _ := rights.decideInference(ctx, subject, inference.ActionComplete, resource); result == "permitted" {
		t.Fatal("foreign epoch dropped")
	}
	call.minimum = ""
	ctx = context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if result, _ := rights.decideInference(ctx, subject, inference.ActionComplete, inference.ResourceHost("foreign")); result == "permitted" {
		t.Fatal("foreign resource permitted")
	}
	call.minimum = "malformed"
	ctx = context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if result, _ := rights.decideInference(ctx, subject, inference.ActionComplete, resource); result == "permitted" {
		t.Fatal("minimum dropped")
	}
	call.minimum = ""
	call.program = "remote:foreign"
	ctx = context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if result, _ := rights.decideInference(ctx, subject, inference.ActionComplete, resource); result != "forbidden" {
		t.Fatalf("foreign context: %s", result)
	}
}

func TestRemoteSubjectMismatchRetainsCapabilityRefusals(t *testing.T) {
	for _, field := range []string{"owner", "account", "program"} {
		t.Run(field, func(t *testing.T) {
			rights, _, config, peer := incomingFixture(t)
			mapped, err := remote.SubjectOf(peer, config.Admit)
			if err != nil {
				t.Fatal(err)
			}
			call := incomingRemoteCall{peer: peer, admit: config.Admit, account: rights.owner, program: mapped.Program()}
			subject := inference.Subject{Account: call.account, Program: call.program}
			switch field {
			case "owner":
				rights.owner += "-foreign"
			case "account":
				subject.Account += "-foreign"
			case "program":
				subject.Program += "-foreign"
			}
			ctx := context.WithValue(context.Background(), incomingRemoteKey{}, call)
			if got, _ := rights.decideInference(ctx, subject, inference.ActionComplete, "host:owned"); got != "forbidden" {
				t.Fatalf("inference mismatch: %s", got)
			}
			c := &runtimeCredentials{runtimeRights: rights}
			if got, _ := c.decide(ctx, cwire.Subject{Account: subject.Account, Program: subject.Program}, credentialsapi.ActionApply, "secret:owned"); got != "unavailable" {
				t.Fatalf("credential mismatch: %s", got)
			}
			if got := c.useDecision(ctx, rwire.Subject{Account: subject.Account, Program: subject.Program}, "secret:owned"); got != rwire.DecisionOutcomeUnavailable {
				t.Fatalf("credential use mismatch: %s", got)
			}
			if got, _ := c.standingInference(ctx, subject, "owned", ""); got != "remote:unavailable" {
				t.Fatalf("standing mismatch: %s", got)
			}
		})
	}
}

func TestRemoteRuntimeMarksProviderExecutionLeaf(t *testing.T) {
	rights, _, config, peer := incomingFixture(t)
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	if err := rights.policy.Set(rwire.Subject{Account: rights.owner, Program: mapped.Program()}, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	native, err := router.NewNative("second-hop", "owned-never-opened", listen.ServerExpectation{}, []string{"fixture-model"}, []string{router.ProfileChat})
	if err != nil {
		t.Fatal(err)
	}
	routes := router.New(native)
	var decisions atomic.Int32
	provider, err := inference.New(inference.Config{Router: routes, Decide: func(context.Context, inference.Subject, string, string) (string, error) {
		decisions.Add(1)
		return "not_granted", nil
	}, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		t.Error("leaf applied credential")
		return nil, "unavailable"
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	request := iwire.Request{Model: "fixture-model", Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "owned fixture"}}}}}
	control := provider.Start(context.Background(), inference.Subject{Account: rights.owner, Program: mapped.Program()}, request)
	if control.Outcome != iwire.StartOutcomeNotPermitted || decisions.Load() != 1 {
		t.Fatalf("reachable non-leaf control: %+v calls=%d", control, decisions.Load())
	}
	decisions.Store(0)
	prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, &runtimeInference{provider: provider, router: routes})
	if err != nil {
		t.Fatal(err)
	}
	client := iwire.NewChatClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	result, err := client.Start(request)
	if err != nil || result.Outcome != iwire.StartOutcomeNoHost || decisions.Load() != 0 {
		t.Fatalf("leaf reply: %+v %v", result, err)
	}
}

func TestRemoteRuntimeListenerOwnsShutdownAndLivePins(t *testing.T) {
	rights, services, config, _ := incomingFixture(t)
	serverTLS, digest, trust := remoteTrustFiles(t, t.TempDir())
	config.TLS = serverTLS
	var paired atomic.Bool
	paired.Store(true)
	config.Admit = func(peer remote.Peer) error {
		if !paired.Load() || peer.Key != digest {
			return remote.ErrTrust
		}
		return nil
	}
	clientCertificate, err := tls.LoadX509KeyPair(trust.Certificate, trust.Key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(clientCertificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := remote.SubjectOf(remote.Peer{Certificate: parsed, Key: digest}, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	if err := rights.policy.Set(rwire.Subject{Account: rights.owner, Program: mapped.Program()}, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services)
	if err != nil {
		t.Fatal(err)
	}
	var address string
	changed := make(chan struct{})
	failures := make(chan error, 1)
	stop, err := prepared.start(context.Background(), func(err error) { failures <- err }, func(network, addressIn string) (net.Listener, error) {
		if network != "tcp" || addressIn != "127.0.0.1:0" {
			t.Fatalf("unexpected forwarded bind: %q %q", network, addressIn)
		}
		listener, err := listenRemoteExposure(context.Background(), network, addressIn, func(netip.Addr) (remoteExposureSource, error) {
			return remoteExposureSource{changed: changed, check: func(netip.Addr) error { return nil }, close: func() error { return nil }}, nil
		}, func(ctx context.Context, n, a string) (net.Listener, error) {
			if n != "tcp" || a != "127.0.0.1:0" {
				t.Fatalf("unexpected forwarded bind: %q %q", n, a)
			}
			return net.Listen("tcp", "127.0.0.1:0")
		})
		if err == nil {
			address = listener.Addr().String()
		}
		return listener, err
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	rootsData, err := os.ReadFile(trust.Roots)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(rootsData) {
		t.Fatal("fixture roots")
	}
	serverCertificate, err := x509.ParseCertificate(serverTLS.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	serverKey := sha256.Sum256(serverCertificate.RawSubjectPublicKeyInfo)
	transport, err := remote.Client(address, &tls.Config{ServerName: trust.ServerName, RootCAs: roots, Certificates: []tls.Certificate{clientCertificate}}, time.Second, remoteRouterReplyBytes, func(peer remote.Peer) error {
		if peer.Key != serverKey {
			return remote.ErrTrust
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	client := fwire.NewEndpointClient(transport)
	if _, err := client.Describe(); err != nil {
		t.Fatal(err)
	}
	views := routerwire.NewRouterClient(transport)
	assertRefusedModalities := func() {
		t.Helper()
		page, err := views.Modalities("", 64)
		if err != nil || page.Outcome != routerwire.ModalityOutcomeNotPermitted || len(page.Modalities) != 0 || page.Next != "" || page.Complete {
			t.Fatalf("inventory refusal: %+v %v", page, err)
		}
	}
	assertRefusedModalities() // remote.serve alone supplies no inventory permission.
	subject := rwire.Subject{Account: rights.owner, Program: mapped.Program()}
	if err := rights.policy.Set(subject, routerservice.ActionInventory, routerservice.ResourceInventory, true); err != nil {
		t.Fatal(err)
	}
	services.router.SetHosts(&router.Host{Profiles: []string{"fixture/custom@1"}})
	page, err := views.Modalities("", 64)
	if err != nil || page.Outcome != routerwire.ModalityOutcomePage || !page.Complete || !slices.Contains(page.Modalities, "fixture/custom@1") {
		t.Fatalf("declared modalities without a model: %+v %v", page, err)
	}
	first, err := views.Modalities("", 1)
	if err != nil || first.Next == "" {
		t.Fatalf("profile continuation: %+v %v", first, err)
	}
	services.router.SetHosts()
	gap, err := views.Modalities(first.Next, 1)
	if err != nil || gap.Outcome != routerwire.ModalityOutcomeGap || len(gap.Modalities) != 0 {
		t.Fatalf("changed profile catalogue: %+v %v", gap, err)
	}
	if err := rights.policy.Set(subject, routerservice.ActionInventory, routerservice.ResourceInventory, false); err != nil {
		t.Fatal(err)
	}
	assertRefusedModalities()
	if _, err := transport.ExchangeFrame(bytes.Repeat([]byte{'x'}, remoteRequestBytes+1)); err == nil {
		t.Fatal("generic input bound raised to router reply bound")
	}
	paired.Store(false)
	if _, err := client.Describe(); err == nil {
		t.Fatal("removed incoming pin retained")
	}
	select {
	case err := <-failures:
		t.Fatalf("per-call refusal stopped listener: %v", err)
	default:
	}
	close(changed)
	select {
	case err := <-failures:
		if !errors.Is(err, errRemoteExposure) {
			t.Fatalf("category invalidation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("remote listener did not stop on category change")
	}
}

func TestRemoteProviderStartAndEmbedKeepResolvedPolicyFence(t *testing.T) {
	rights, services, config, peer := incomingFixture(t)
	var observations atomic.Int32
	config.observeCall = func(remote.Peer) func() { return func() { observations.Add(1) } }
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: rights.owner, Program: mapped.Program()}
	if err := rights.policy.Set(subject, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	var upstream atomic.Int32
	models := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			upstream.Add(1)
			http.Error(w, "must not infer", 500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
	}))
	defer models.Close()
	routes := router.New(router.NewHosted("owned", models.URL, router.WireOpenAICompatible, "owned"))
	// The owned inventory endpoint needs no secret. This fixture applier permits
	// only its empty-header model listing; inference remains policy-refused.
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{}, nil
	})
	var decisions atomic.Int32
	provider, err := inference.New(inference.Config{Router: routes, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
		t.Error("denied fixture applied credentials")
		return nil, "unavailable"
	}, Decide: func(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
		decisions.Add(1)
		if action != inference.ActionComplete || resource != inference.ResourceHost("owned") {
			t.Errorf("resolved decision %s %s", action, resource)
		}
		return rights.decideInference(ctx, s, action, resource)
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	services = &runtimeInference{provider: provider, router: routes}
	for _, minimum := range []string{"", "malformed"} {
		config.Minimum = func(context.Context, remote.Peer) (string, error) { return minimum, nil }
		prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services)
		if err != nil {
			t.Fatal(err)
		}
		transport := remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) })
		chat := iwire.NewChatClient(transport)
		embed := iwire.NewEmbedderClient(transport)
		before := decisions.Load()
		start, startErr := chat.Start(iwire.Request{Model: "fixture-model", Credential: "owned", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "owned fixture"}}}}})
		embedded, embedErr := embed.Embed(iwire.EmbedRequest{Model: "fixture-model", Credential: "owned", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Inputs: []string{"owned fixture"}})
		if minimum == "" {
			if startErr != nil || embedErr != nil || start.Outcome == iwire.StartOutcomeAccepted || embedded.Outcome == iwire.EmbedOutcomeCompleted {
				t.Fatalf("missing resource grant: %+v %v %+v %v", start, startErr, embedded, embedErr)
			}
			if decisions.Load()-before != 2 {
				t.Fatalf("actual Start/Embed policy calls=%d start=%+v embed=%+v", decisions.Load()-before, start, embedded)
			}
		} else if startErr == nil || embedErr == nil {
			t.Fatal("invalid minimum passed receiving admission")
		}
	}
	// The resource owner also retains the fence when called directly by its
	// typed receiver after generic admission. No ordinary decision fallback runs.
	call := incomingRemoteCall{peer: peer, admit: config.Admit, minimum: "malformed", account: rights.owner, program: mapped.Program()}
	ctx := context.WithValue(inference.WithLeafExecution(context.Background()), incomingRemoteKey{}, call)
	before := decisions.Load()
	directSubject := inference.Subject{Account: rights.owner, Program: mapped.Program()}
	start := provider.Start(ctx, directSubject, iwire.Request{Model: "fixture-model", Credential: "owned", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "owned fixture"}}}}})
	embedded := provider.Embed(ctx, directSubject, iwire.EmbedRequest{Model: "fixture-model", Credential: "owned", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Inputs: []string{"owned fixture"}})
	if decisions.Load()-before != 2 || start.Outcome != iwire.StartOutcomeUnavailable || embedded.Outcome != iwire.EmbedOutcomeUnavailable {
		t.Fatalf("resource fence: calls=%d start=%+v embed=%+v", decisions.Load()-before, start, embedded)
	}
	if upstream.Load() != 0 {
		t.Fatal("denied source fixture performed inference")
	}
	if observations.Load() != 2 {
		t.Fatal("post-ingress resource refusals lost contact observations")
	}
}

func TestRemoteAcceptedOperationsKeepTheirOwnStandingAuthority(t *testing.T) {
	for _, revoke := range []string{"policy", "pairing"} {
		t.Run(revoke, func(t *testing.T) {
			rights, _, config, peer := incomingFixture(t)
			var admittedCalls atomic.Int32
			config.observeCall = func(remote.Peer) func() { return func() { admittedCalls.Add(1) } }
			mapped, err := remote.SubjectOf(peer, config.Admit)
			if err != nil {
				t.Fatal(err)
			}
			subject := rwire.Subject{Account: rights.owner, Program: mapped.Program()}
			for _, rule := range []struct{ action, resource string }{{remoteServeAction, "account"}, {inference.ActionComplete, inference.ResourceHost("owned")}} {
				if err := rights.policy.Set(subject, rule.action, rule.resource, true); err != nil {
					t.Fatal(err)
				}
			}
			var paired atomic.Bool
			paired.Store(true)
			originalAdmit := config.Admit
			config.Admit = nil
			config.AdmitContext = func(ctx context.Context, p remote.Peer) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if !paired.Load() {
					return remote.ErrTrust
				}
				return originalAdmit(p)
			}
			started := make(chan struct{}, 2)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					w.Header().Set("Content-Type", "application/json")
					w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"owned\"}}]}\n\n"))
				w.(http.Flusher).Flush()
				started <- struct{}{}
				<-r.Context().Done()
			}))
			defer backend.Close()
			credentials := &runtimeCredentials{runtimeRights: rights}
			routes := router.New(router.NewHosted("owned", backend.URL, router.WireOpenAICompatible, ""))
			var seenMu sync.Mutex
			seen := map[string]int{}
			provider, err := inference.New(inference.Config{Router: routes, Decide: rights.decideInference, Standing: func(ctx context.Context, subject inference.Subject, host, credential string) (string, error) {
				call, _ := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall)
				seenMu.Lock()
				seen[call.minimum]++
				seenMu.Unlock()
				return credentials.standingInference(ctx, subject, host, credential)
			},
				Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
					t.Error("fixture applied secret")
					return nil, "unavailable"
				}})
			if err != nil {
				t.Fatal(err)
			}
			defer provider.Close()
			observation := func() string {
				return rights.policy.DecideForAtLeastRevisionContext(context.Background(), subject, remoteServeAction, "account", "").ObservedRevision
			}
			minimum := observation()
			if minimum == "" {
				t.Fatal("first minimum missing")
			}
			oldState, err := os.ReadFile(rights.path)
			if err != nil {
				t.Fatal(err)
			}
			config.Minimum = func(context.Context, remote.Peer) (string, error) { return minimum, nil }
			prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, &runtimeInference{provider: provider, router: routes})
			if err != nil {
				t.Fatal(err)
			}
			request := iwire.Request{Model: "fixture-model", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "owned fixture"}}}}}
			start := func() string {
				ingress, cancel := context.WithCancel(context.Background())
				client := iwire.NewChatClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(ingress, peer, frame) }))
				accepted, err := client.Start(request)
				cancel()
				if err != nil || accepted.Outcome != iwire.StartOutcomeAccepted {
					t.Fatalf("accept: %+v %v", accepted, err)
				}
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("owned backend did not start")
				}
				return accepted.Operation
			}
			first := start()
			firstMinimum := minimum
			if err := rights.policy.RegisterAction("abstraction.fixture/advance"); err != nil {
				t.Fatal(err)
			}
			secondMinimum := observation()
			if secondMinimum == minimum || secondMinimum == "" {
				t.Fatal("second minimum did not advance")
			}
			minimum = secondMinimum
			newState, err := os.ReadFile(rights.path)
			if err != nil {
				t.Fatal(err)
			}
			second := start()
			if admittedCalls.Load() != 2 {
				t.Fatal("authorized chat admissions lost last-call observations")
			}
			// Restore only this owned fixture's earlier policy snapshot. One minimum
			// remains observable and the other is now ahead of the receiving state.
			if err := os.WriteFile(rights.path, oldState, 0600); err != nil {
				t.Fatal(err)
			}
			seenMu.Lock()
			clear(seen)
			seenMu.Unlock()
			if err := provider.RecheckChecked(context.Background()); err == nil {
				t.Fatal("unobservable minimum reported successful settlement")
			}
			seenMu.Lock()
			firstSeen, secondSeen := seen[firstMinimum], seen[secondMinimum]
			seenMu.Unlock()
			if firstSeen < 1 || secondSeen < 1 {
				t.Fatalf("per-operation minima not independently evaluated: %d %d", firstSeen, secondSeen)
			}
			bound := inference.Subject{Account: subject.Account, Program: subject.Program}
			end := func(id string) iwire.Reply {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				cursor := int64(0)
				for ctx.Err() == nil {
					page := provider.Observe(ctx, bound, id, cursor, 32, 4096, 1000)
					for _, delta := range page.Deltas {
						if delta.Kind == iwire.DeltaKindEnd && delta.End != nil {
							return *delta.End
						}
					}
					cursor = page.Next
				}
				t.Fatal("owned operation did not end")
				return iwire.Reply{}
			}
			for _, id := range []string{first, second} {
				if page := provider.Observe(context.Background(), bound, id, 0, 32, 4096, 0); page.AtEnd {
					t.Fatalf("uncertain minimum ended %s: %+v", id, page)
				}
			}
			client := iwire.NewChatClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
			accepted, err := client.Start(request)
			var refused *iwire.ServiceError
			if !errors.As(err, &refused) || string(refused.Code) != "unavailable" || accepted.Outcome == iwire.StartOutcomeAccepted {
				t.Fatalf("fresh minimum admission: %+v %v", accepted, err)
			}
			if err := os.WriteFile(rights.path, newState, 0600); err != nil {
				t.Fatal(err)
			}
			if revoke == "policy" {
				if err := rights.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("owned"), false); err != nil {
					t.Fatal(err)
				}
			} else {
				paired.Store(false)
			}
			if err := provider.RecheckChecked(context.Background()); err != nil {
				t.Fatalf("actual withdrawal: %v", err)
			}
			for _, id := range []string{first, second} {
				if reply := end(id); reply.Outcome != iwire.ReplyOutcomeCancelled {
					t.Fatalf("actual withdrawal standing: %+v", reply)
				}
			}
		})
	}
}

func TestRemoteCredentialApplyPreservesTypedUseAndMinimum(t *testing.T) {
	r := testRights(t, t.TempDir())
	holder, err := credentialsapi.Open(credentialsapi.Config{Backend: &privateReleaseMemory{values: map[string][]byte{}}, Namespace: "pmc1-owned-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	c := &runtimeCredentials{runtimeRights: r, holder: holder, host: &credservice.Host{}}
	c.host.Authorization = credservice.AuthorizationConfig{Admit: c.admitScoped, Quiet: c.checkScoped, Legacy: c.checkLegacy}
	r.configure(&host.Options{Credentials: c.host, OnError: func(err error) { t.Error(err) }}, nil)
	if err := r.install(append(append([]string{}, cwire.ResourceActions...), remoteServeAction, inference.ActionComplete), nil); err != nil {
		t.Fatal(err)
	}
	_, _, config, peer := incomingFixture(t)
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	local := rwire.Subject{Account: r.owner, Program: r.operators[0]}
	remoteSubject := rwire.Subject{Account: r.owner, Program: mapped.Program()}
	if err := r.policy.Set(remoteSubject, inference.ActionComplete, inference.ResourceHost("owned"), true); err != nil {
		t.Fatal(err)
	}
	name := "owned-remote"
	stored := holder.Store(cwire.Subject{Account: local.Account, Program: local.Program}, "", cwire.Registration{Name: name, Kind: "bearer", Scope: cwire.Scope{Targets: []string{"api.fixture.invalid"}, Consumers: []string{"abstraction.inference/chat@1"}}, Secret: []byte("P_MC1A_OWNED_FAKE_VALUE")})
	if stored.Outcome != cwire.StoreOutcomeStored {
		t.Fatalf("fixture store: %s", stored.Outcome)
	}
	target, err := credentialsapi.PrepareCredentialTarget(name)
	if err != nil {
		t.Fatal(err)
	}
	selection := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: credentialsapi.ActionApply, ResourceProfile: credentialsapi.ScopedUseResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: target}}
	selection, err = r.policy.Definitions.PrepareSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	for _, subject := range []rwire.Subject{local, remoteSubject} {
		revision, _, err := r.policy.SelectorSnapshotContext(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err := r.policy.SetSelectorRuleContext(context.Background(), revision, rwire.SelectorRule{Subject: subject, Selection: selection, Permit: true}, rightsapi.RuleEdit{By: local, Why: "owned remote receiving fixture"}, func() error { return nil })
		if err != nil || result.Outcome != rwire.PolicyEditOutcomeApplied {
			t.Fatalf("fixture rule: %s %v", result.Outcome, err)
		}
		if err := r.policy.Set(subject, credentialsapi.ActionApply, credentialsapi.ResourceFor(name), true); err != nil {
			t.Fatal(err)
		}
	}
	use := cwire.Use{Subject: cwire.Subject{Account: local.Account, Program: local.Program}, Consumer: "abstraction.inference/chat@1", Name: name, Target: "api.fixture.invalid"}
	if applied := holder.Apply(context.Background(), use, c.decide); applied.Outcome != cwire.ApplyOutcomeApplied || len(applied.Headers) == 0 {
		t.Fatalf("local apply changed: %s", applied.Outcome)
	}
	var askCount atomic.Int32
	c.firstUse = func(rwire.Subject, func() asksapi.Publisher, string, string) string {
		askCount.Add(1)
		return "unexpected"
	}
	minimum := r.policy.DecideForAtLeastRevisionContext(context.Background(), remoteSubject, remoteServeAction, "account", "").ObservedRevision
	if minimum == "" {
		t.Fatal("owned current minimum missing")
	}
	call := incomingRemoteCall{peer: peer, admit: config.Admit, account: r.owner, program: remoteSubject.Program, minimum: minimum}
	use.Subject = cwire.Subject{Account: remoteSubject.Account, Program: remoteSubject.Program}
	ctx := context.WithValue(context.Background(), incomingRemoteKey{}, call)
	if applied := holder.Apply(ctx, use, c.decide); applied.Outcome != cwire.ApplyOutcomeApplied || len(applied.Headers) == 0 {
		t.Fatalf("current typed minimum refused: %s", applied.Outcome)
	}
	if refusal, err := c.standingInference(ctx, inference.Subject{Account: remoteSubject.Account, Program: remoteSubject.Program}, "owned", name); err != nil || refusal != "" {
		t.Fatalf("current typed standing: %s %v", refusal, err)
	}
	oldState, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := r.policy.SelectorSnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blocked, err := r.policy.SetSelectorRuleContext(context.Background(), revision, rwire.SelectorRule{Subject: remoteSubject, Selection: selection, Permit: false}, rightsapi.RuleEdit{By: local, Why: "owned typed block"}, func() error { return nil })
	if err != nil || blocked.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("typed block: %s %v", blocked.Outcome, err)
	}
	if applied := holder.Apply(ctx, use, c.decide); applied.Outcome != cwire.ApplyOutcomeNotPermitted || len(applied.Headers) != 0 {
		t.Fatalf("typed Block lost to legacy Permit: %s", applied.Outcome)
	}
	later := r.policy.DecideForAtLeastRevisionContext(context.Background(), remoteSubject, remoteServeAction, "account", "").ObservedRevision
	if later == minimum {
		t.Fatal("fixture did not advance")
	}
	if err := os.WriteFile(r.path, oldState, 0600); err != nil {
		t.Fatal(err)
	}
	stale := call
	stale.minimum = later
	if applied := holder.Apply(context.WithValue(context.Background(), incomingRemoteKey{}, stale), use, c.decide); applied.Outcome != cwire.ApplyOutcomeUnavailable || len(applied.Headers) != 0 {
		t.Fatal("restored older state released value")
	}
	if applied := holder.Apply(ctx, use, c.decide); applied.Outcome != cwire.ApplyOutcomeApplied {
		t.Fatalf("restored current minimum: %s", applied.Outcome)
	}
	for _, variant := range []string{"missing", "mismatched", "pin", "incomparable", "malformed"} {
		candidate := call
		ctx := context.Background()
		switch variant {
		case "missing":
		case "incomparable":
			parts := strings.Split(candidate.minimum, ".")
			parts[1] = strings.Repeat("0", 32)
			candidate.minimum = strings.Join(parts, ".")
			ctx = context.WithValue(ctx, incomingRemoteKey{}, candidate)
		case "malformed":
			candidate.minimum = "not-a-revision"
			ctx = context.WithValue(ctx, incomingRemoteKey{}, candidate)
		case "mismatched":
			candidate.program = "remote:foreign"
			ctx = context.WithValue(ctx, incomingRemoteKey{}, candidate)
		case "pin":
			candidate.minimum = ""
			candidate.admit = func(remote.Peer) error { return remote.ErrTrust }
			ctx = context.WithValue(ctx, incomingRemoteKey{}, candidate)
		default:
			ctx = context.WithValue(ctx, incomingRemoteKey{}, candidate)
		}
		applied := holder.Apply(ctx, use, c.decide)
		if applied.Outcome == cwire.ApplyOutcomeApplied || len(applied.Headers) != 0 {
			t.Fatalf("remote %s released applicable value", variant)
		}
	}
	if askCount.Load() != 0 {
		t.Fatal("remote credential branch raised local first-use")
	}
	missingTarget := call
	missingTarget.minimum = ""
	if c.useDecision(context.WithValue(context.Background(), incomingRemoteKey{}, missingTarget), remoteSubject, "invalid-target") != rwire.DecisionOutcomeUnavailable {
		t.Fatal("missing typed target took legacy fallback")
	}
}

type remoteTestSink func(logging.Record) error

func (f remoteTestSink) Write(r logging.Record) error { return f(r) }
func fixturePrepareRemote(ctx context.Context, options runtimeFlags, rights *runtimeRights, services *runtimeInference) (*runtimeRemote, error) {
	return prepareRuntimeRemote(ctx, options, rights, services, remoteTestSink(func(logging.Record) error { return nil }), nil)
}

func TestRemoteRuntimeAuditMandatoryAndAttributed(t *testing.T) {
	rights, services, config, peer := incomingFixture(t)
	if _, err := prepareRuntimeRemote(context.Background(), fixtureRemoteOptions(config), rights, services, nil, nil); err == nil {
		t.Fatal("missing retained sink accepted")
	}
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	if err = rights.policy.Set(rwire.Subject{Account: rights.owner, Program: mapped.Program()}, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var records []logging.Record
	sink := remoteTestSink(func(r logging.Record) error { mu.Lock(); defer mu.Unlock(); records = append(records, r); return nil })
	prepared, err := prepareRuntimeRemote(context.Background(), fixtureRemoteOptions(config), rights, services, sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := fwire.NewEndpointClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	if _, err := client.Describe(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if len(records) != 2 || records[0].Msg != "remote.call.attempt" || records[0].Attrs["program"] != mapped.Program() || records[0].Attrs["method"] != "Describe" || records[0].Attrs["profile"] != "see" {
		t.Fatalf("audit: %+v", records)
	}
	mu.Unlock()
	lastObservation := func(admission, outcome string) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		r := records[len(records)-1]
		if r.Msg != "remote.call.return" || r.Attrs["admission"] != admission || r.Attrs["outcome"] != outcome {
			t.Fatalf("observation: %+v", r)
		}
	}
	lastObservation("admitted", "response")
	if err := rights.policy.Set(rwire.Subject{Account: rights.owner, Program: mapped.Program()}, remoteServeAction, "account", false); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Describe(); err == nil {
		t.Fatal("rights refusal lost")
	}
	lastObservation("rights-refused", "response")
	if err := rights.policy.Set(rwire.Subject{Account: rights.owner, Program: mapped.Program()}, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	leases := resourcewire.NewLeasesClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	if _, err := leases.Acquire("awake", 1, 0); err == nil {
		t.Fatal("unoffered method accepted")
	}
	lastObservation("profile-or-service-refused", "response")
	chat := iwire.NewChatClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	_, _ = chat.Start(iwire.Request{Extensions: map[string]string{inference.ClaimExtension: "fixture-display"}})
	mu.Lock()
	if records[len(records)-2].Attrs["claim"] != "fixture-display" || records[len(records)-2].Attrs["method"] != "Start" {
		t.Fatalf("decoded claim: %+v", records)
	}
	mu.Unlock()
	lastObservation("handler-entered", "response")
	embed := iwire.NewEmbedderClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	_, _ = embed.Embed(iwire.EmbedRequest{Extensions: map[string]string{inference.ClaimExtension: "embed-display"}})
	lastObservation("handler-entered", "response")
	if _, err := prepared.server.Handler(context.Background(), peer, []byte("malformed")); err == nil {
		t.Fatal("malformed accepted")
	}
	mu.Lock()
	if records[len(records)-2].Attrs["method"] != "unknown" {
		t.Fatal("malformed call not audited")
	}
	mu.Unlock()
	lastObservation("malformed-frame", "return-error")
	failed, err := prepareRuntimeRemote(context.Background(), fixtureRemoteOptions(config), rights, services, remoteTestSink(func(logging.Record) error { return errors.New("sensitive sink detail") }), nil)
	if err != nil {
		t.Fatal(err)
	}
	client = fwire.NewEndpointClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return failed.server.Handler(context.Background(), peer, frame) }))
	if _, err := client.Describe(); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("failed audit: %v", err)
	}
}

func TestRemoteHostPrebindExposureRefusalKeepsLocal(t *testing.T) {
	rights, services, config, _ := incomingFixture(t)
	for _, address := range []string{"localhost:1234", "0.0.0.0:1234", "[fe80::1%3]:1234"} {
		t.Run(address, func(t *testing.T) {
			selected := *config
			selected.Address = address
			reports := 0
			bound := 0
			prepared, err := prepareRuntimeRemoteHost(context.Background(), fixtureRemoteOptions(&selected), rights, services, remoteTestSink(func(logging.Record) error { return nil }), func(err error) {
				if !errors.Is(err, errRemoteExposure) {
					t.Fatal(err)
				}
				reports++
			})
			if err != nil || prepared != nil || reports != 1 {
				t.Fatalf("local host continuation: %v %v %d", prepared, err, reports)
			}
			stop, err := prepared.start(context.Background(), func(error) { t.Fatal("no listener may fail") }, func(string, string) (net.Listener, error) { bound++; return nil, errors.New("unexpected bind") })
			if err != nil {
				t.Fatal(err)
			}
			stop()
			if bound != 0 {
				t.Fatal("prebind exposure refusal opened listener")
			}
		})
	}
	if _, err := prepareRuntimeRemoteHost(context.Background(), fixtureRemoteOptions(config), rights, services, nil, nil); err == nil {
		t.Fatal("unrelated missing audit sink failure swallowed")
	}
}

func TestRemoteSubjectGetsNoWake(t *testing.T) {
	rights, services, config, peer := incomingFixture(t)
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: rights.owner, Program: mapped.Program()}
	// Even an explicit policy grant cannot make an unoffered native-only
	// resource dispatcher reachable through this incoming remote endpoint.
	if err = rights.policy.Set(subject, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	if err = rights.policy.RegisterAction("abstraction.resource/hold"); err != nil {
		t.Fatal(err)
	}
	if err = rights.policy.Set(subject, "abstraction.resource/hold", "awake", true); err != nil {
		t.Fatal(err)
	}
	prepared, err := fixturePrepareRemote(context.Background(), fixtureRemoteOptions(config), rights, services)
	if err != nil {
		t.Fatal(err)
	}
	leases := resourcewire.NewLeasesClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return prepared.server.Handler(context.Background(), peer, frame) }))
	if _, err := leases.Acquire("awake", 1, 0); err == nil {
		t.Fatal("remote resource wake reached a handler")
	}
	if remoteOfferedMethod("abstraction.resource/leases@1", "Acquire") {
		t.Fatal("native resource service offered remotely")
	}
}

func TestRemoteAuditOneWriterDeadlineAndNoLateDispatch(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var writes, dispatch atomic.Int32
	a := &remoteAudit{slot: make(chan struct{}, 1), sink: remoteTestSink(func(logging.Record) error { writes.Add(1); close(entered); <-release; close(finished); return nil })}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		attempt := &remoteAuditAttempt{audit: a, ctx: ctx, attrs: map[string]string{}}
		if err := attempt.write(""); err != nil {
			result <- err
			return
		}
		dispatch.Add(1)
		result <- nil
	}()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := a.write(context.Background(), logging.Record{}); err == nil {
		t.Fatal("busy writer accepted")
	}
	if writes.Load() != 1 || dispatch.Load() != 0 {
		t.Fatal("unbounded writer or late dispatch")
	}
	close(release)
	<-finished
	// The sole writer's slot release follows its actual Write return.
	deadline := time.After(time.Second)
	for len(a.slot) != 0 {
		select {
		case <-deadline:
			t.Fatal("writer not joined")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if dispatch.Load() != 0 {
		t.Fatal("timed out attempt dispatched later")
	}
}

func TestRemoteAuditRetainedAcrossReceivingReplacement(t *testing.T) {
	rights, services, config, peer := incomingFixture(t)
	mapped, err := remote.SubjectOf(peer, config.Admit)
	if err != nil {
		t.Fatal(err)
	}
	if err = rights.policy.Set(rwire.Subject{Account: rights.owner, Program: mapped.Program()}, remoteServeAction, "account", true); err != nil {
		t.Fatal(err)
	}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{}, 2)
	var writes atomic.Int32
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		for i := int32(0); i < writes.Load(); i++ {
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Error("owned audit writer not joined")
			}
		}
	})
	sink := remoteTestSink(func(logging.Record) error {
		if writes.Add(1) == 1 {
			close(entered)
		}
		<-release
		finished <- struct{}{}
		return nil
	})
	config.audit = &remoteAudit{sink: sink, slot: make(chan struct{}, 1)}
	prepare := func() *runtimeRemote {
		t.Helper()
		r, e := prepareRuntimeRemote(context.Background(), fixtureRemoteOptions(config), rights, services, sink, nil)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	first := prepare()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := fwire.NewEndpointClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return first.server.Handler(ctx, peer, frame) })).Describe()
		done <- e
	}()
	<-entered
	cancel()
	if e := <-done; e == nil {
		t.Fatal("cancelled audit dispatched")
	}
	second := prepare()
	if _, e := fwire.NewEndpointClient(remoteFixtureTransport(func(frame []byte) ([]byte, error) { return second.server.Handler(context.Background(), peer, frame) })).Describe(); e == nil {
		t.Fatal("replacement bypassed busy audit")
	}
	if writes.Load() != 1 {
		t.Fatal("replacement created another blocked sink writer")
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.After(time.Second)
	for len(config.audit.slot) != 0 {
		select {
		case <-deadline:
			t.Fatal("owned writer did not join")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestRemoteAuditDecodedClaimBeforeService(t *testing.T) {
	var records []logging.Record
	a := &remoteAudit{slot: make(chan struct{}, 1), sink: remoteTestSink(func(r logging.Record) error { records = append(records, r); return nil })}
	attempt := &remoteAuditAttempt{audit: a, ctx: context.Background(), attrs: map[string]string{"claim": "absent"}}
	called := false
	wrapper := &remoteAuditedChat{Chat: remoteClaimChat{start: func(r iwire.Request) (iwire.Admission, error) {
		called = true
		if len(records) != 1 || records[0].Attrs["claim"] != "display label" {
			t.Fatal("claim not logged before service")
		}
		return iwire.Admission{}, nil
	}}, attempt: attempt}
	if _, err := wrapper.Start(iwire.Request{Extensions: map[string]string{inference.ClaimExtension: "display\nlabel", "private": "secret-content"}}); err != nil {
		t.Fatal(err)
	}
	if !called || strings.Contains(fmt.Sprint(records), "secret-content") {
		t.Fatal("wrong projection")
	}
	wrapper.Chat = remoteClaimChat{start: func(iwire.Request) (iwire.Admission, error) {
		return iwire.Admission{}, errors.New("private-handler-detail")
	}}
	wrapper.attempt = &remoteAuditAttempt{audit: a, ctx: context.Background(), attrs: map[string]string{}}
	if _, err := wrapper.Start(iwire.Request{}); err == nil || wrapper.attempt.observation != "handler-return-error" {
		t.Fatal("handler error observation lost")
	}
	if strings.Contains(fmt.Sprint(records), "private-handler-detail") {
		t.Fatal("raw handler error logged")
	}
	attempt = &remoteAuditAttempt{audit: &remoteAudit{slot: make(chan struct{}, 1), sink: remoteTestSink(func(logging.Record) error { return errors.New("fail") })}, ctx: context.Background(), attrs: map[string]string{}}
	wrapper.attempt = attempt
	called = false
	if _, err := wrapper.Start(iwire.Request{}); err == nil || called {
		t.Fatal("audit failure dispatched")
	}
}

func TestRemoteAuditDeadline(t *testing.T) {
	release, finished := make(chan struct{}), make(chan struct{})
	a := &remoteAudit{slot: make(chan struct{}, 1), sink: remoteTestSink(func(logging.Record) error { defer close(finished); <-release; return nil })}
	err := a.write(context.Background(), logging.Record{})
	if err == nil || !strings.Contains(err.Error(), "deadline") {
		close(release)
		<-finished
		t.Fatalf("deadline: %v", err)
	}
	if len(a.slot) != 1 {
		t.Fatal("blocked write lost ownership")
	}
	close(release)
	<-finished
	deadline := time.After(time.Second)
	for len(a.slot) != 0 {
		select {
		case <-deadline:
			t.Fatal("writer not joined")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

type remoteClaimChat struct {
	iwire.Chat
	start func(iwire.Request) (iwire.Admission, error)
}

func (s remoteClaimChat) Start(r iwire.Request) (iwire.Admission, error) { return s.start(r) }

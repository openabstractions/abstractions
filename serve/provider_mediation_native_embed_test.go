package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"math"
	"os"
	"runtime"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	router "github.com/openabstractions/abstraction-router/go"
)

// nativeEmbedCaller is the bound subject every fixture call in this file
// uses; its identity plays no part in nativeAdmit's FAC-R8 gate.
var nativeEmbedCaller = inference.Subject{Account: "S-1-5-21-fixture", Program: `C:\apps\embedder.exe`}

// embedderFunc adapts a plain function to iwire.Embedder, for a fake
// declared provider's own embed@1.
type embedderFunc func(iwire.EmbedRequest) (iwire.Embeddings, error)

func (f embedderFunc) Embed(req iwire.EmbedRequest) (iwire.Embeddings, error) { return f(req) }

// packEmbedVector base64-encodes one little-endian float32 vector, the
// contract's wire shape for a completed embed@1 reply.
func packEmbedVector(values ...float32) string {
	packed := make([]byte, 4*len(values))
	for i, v := range values {
		binary.LittleEndian.PutUint32(packed[4*i:], math.Float32bits(v))
	}
	return base64.StdEncoding.EncodeToString(packed)
}

// newNativeEmbedTestProvider builds a runtimeProviders holding one on-demand
// native declaration, and the router.Host nativeInferenceHosts would build
// for it, exactly as newNativeAdmitTestProvider does — but this one's
// endpoint is a real fake declared provider serving its own embed@1, so a
// call that clears nativeAdmit's FAC-R8 gate reaches an actual reply instead
// of stopping at the gate. The supervisor's readiness is still driven
// directly by the test through s.set, as a real probe would drive it.
func newNativeEmbedTestProvider(t *testing.T, name string, embed func(iwire.EmbedRequest) (iwire.Embeddings, error)) (*runtimeProviders, *providerSupervisor, *router.Host) {
	t.Helper()
	if err := identity.CanEver(listen.Program); err != nil || runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(error) {}}
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: exe, Endpoint: "oa-test-native-embed-" + name,
		Transport: transportNative, Contracts: []string{inference.Contract}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationOnDemand.String()}}
	s := newProviderSupervisor(p, f)
	p.supervisors[name] = s

	endpoint := providerEndpointPath(f.Declaration.Endpoint)
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); l.Close() })
	dispatcher := &iwire.EmbedderDispatcher{Handler: embedderFunc(embed)}
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				callCtx, done := context.WithTimeout(ctx, 5*time.Second)
				defer done()
				call, err := listen.ReceiveFramed(callCtx, conn, listen.Program, 1<<20)
				if err != nil {
					return
				}
				defer call.Close()
				if reply, err := dispatcher.ExchangeFrame(call.Frame); err == nil {
					call.Reply(reply)
				}
			}()
		}
	}()

	host, err := router.NewNative(name, endpoint, listen.ServerExpectation{Principal: principal, Program: exe}, f.Declaration.Models, nil)
	if err != nil {
		t.Fatal(err)
	}
	host.BindingID = f.bindingID()
	return p, s, host
}

// runtimeEmbedProvider composes an inference.Provider gated by providers'
// nativeAdmit (FAC-R8), exactly as the runtime composes one in
// composeInference: the Admit hook runs after the router picks a host and
// before the rights decision.
func runtimeEmbedProvider(t *testing.T, providers *runtimeProviders, host *router.Host) *inference.Provider {
	t.Helper()
	p, err := inference.New(inference.Config{Router: router.New(host),
		Admit: providers.nativeAdmit,
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// A ready declared native provider costs an embed@1 call nothing beyond the
// read (FAC-R8: "a provider that is ready costs a call nothing beyond the
// description read"), and the runtime's embed@1 reaches the declared
// provider's own embed@1 over the identity-bound transport (nativeEmbed).
func TestRuntimeEmbedsThroughAReadyNativeProvider(t *testing.T) {
	var gotModel string
	providers, s, host := newNativeEmbedTestProvider(t, "ready-embedder", func(req iwire.EmbedRequest) (iwire.Embeddings, error) {
		gotModel = req.Model
		return iwire.Embeddings{Outcome: iwire.EmbedOutcomeCompleted, Dimensions: 2, Usage: iwire.Usage{Input: int64(len(req.Inputs))},
			Vectors: []string{packEmbedVector(0.5, -1.25), packEmbedVector(1.5, -1.25)}}, nil
	})
	s.set(wire.DeclarationReadinessReady, "")
	p := runtimeEmbedProvider(t, providers, host)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e := p.Embed(ctx, nativeEmbedCaller, iwire.EmbedRequest{Model: "owner/fixture-model", Inputs: []string{"alpha", "beta"}})
	if e.Outcome != iwire.EmbedOutcomeCompleted || e.Dimensions != 2 || len(e.Vectors) != 2 || e.Host != "ready-embedder" {
		t.Fatalf("embed through a ready native provider: %+v", e)
	}
	if gotModel != "owner/fixture-model" {
		t.Fatalf("model forwarded = %q", gotModel)
	}
	if activated(s) {
		t.Fatalf("a ready provider was activated")
	}
}

// FAC-R8: the first embed@1 call that reaches an on-demand declared native
// provider's OA endpoint activates its launch and waits for readiness inside
// the caller's own deadline. A caller whose deadline passes first reads
// unavailable with reason activating:<name>; the launch keeps running, and a
// later call finds the provider ready and reaches its embed@1.
func TestRuntimeEmbedsThroughANativeProviderActivatingThenReady(t *testing.T) {
	providers, s, host := newNativeEmbedTestProvider(t, "slow-embedder", func(req iwire.EmbedRequest) (iwire.Embeddings, error) {
		return iwire.Embeddings{Outcome: iwire.EmbedOutcomeCompleted, Dimensions: 1, Usage: iwire.Usage{Input: int64(len(req.Inputs))},
			Vectors: []string{packEmbedVector(9)}}, nil
	})
	p := runtimeEmbedProvider(t, providers, host)

	go func() {
		time.Sleep(150 * time.Millisecond)
		s.set(wire.DeclarationReadinessReady, "")
	}()

	shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	first := p.Embed(shortCtx, nativeEmbedCaller, iwire.EmbedRequest{Model: "owner/fixture-model", Inputs: []string{"a"}})
	if first.Outcome != iwire.EmbedOutcomeUnavailable || first.Reason != "activating:slow-embedder" {
		t.Fatalf("first call under a short deadline: %+v, want unavailable activating:slow-embedder", first)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}

	laterCtx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	second := p.Embed(laterCtx, nativeEmbedCaller, iwire.EmbedRequest{Model: "owner/fixture-model", Inputs: []string{"a"}})
	if second.Outcome != iwire.EmbedOutcomeCompleted || len(second.Vectors) != 1 {
		t.Fatalf("later call once ready: %+v", second)
	}
}

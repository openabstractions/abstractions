package main

import (
	"context"
	"os"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-router/go"
)

func TestNativeProfileInventoryDoesNotActivateOnDemandProvider(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, contract, profile string }{
		{"chat", inference.Contract, "chat"},
		{"image", inference.ImageContract, "image"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{})}
			f := providerFile{Declaration: providerDeclaration{Name: tc.name + "-idle", Program: program, Endpoint: "private-" + tc.name + "-idle", Transport: transportNative,
				Contracts: []string{tc.contract}, Models: []string{"owner/fixture-model"}, Resources: []string{"profile:" + tc.profile}, Activation: wire.ActivationOnDemand.String()}}
			s := newProviderSupervisor(p, f)
			p.supervisors[f.Declaration.Name] = s
			s.start()
			defer s.halt()
			if activated(s) || s.pid() != 0 || s.state().Readiness != wire.DeclarationReadinessIdle {
				t.Fatalf("profile-bearing on-demand provider started before admission: %+v", s.state())
			}
			hosts := p.nativeInferenceHosts()
			if len(hosts) != 1 || hosts[0].Name != f.Declaration.Name {
				t.Fatalf("declared model inventory absent while provider idle: %+v", hosts)
			}
			if activated(s) || s.pid() != 0 {
				t.Fatal("router inventory read activated an idle provider")
			}
			p.setMediated(map[string]mediatedProvider{f.bindingID(): {Endpoint: "oa-" + tc.name + "-mediator", Ready: true}})
			candidates := p.Candidates()
			if len(candidates) != 1 || !candidates[0].Ready || candidates[0].Activate != nil {
				t.Fatalf("OA mediator should resolve without downstream activation: %+v", candidates)
			}
			catalog, err := resolution.New(candidates)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := catalog.ForCaller(func(wire.ServiceReference) bool { return true }).Resolve(wire.ResolveRequest{
				Capability: "abstraction.inference", Contracts: []string{tc.contract}, Scope: wire.ScopeLocal,
			})
			if err != nil || resolved.Status != wire.ResolutionStatusResolved || resolved.Reference == nil || resolved.Reference.Endpoint != candidates[0].Reference.Endpoint {
				t.Fatalf("resolve OA mediator: %+v, %v", resolved, err)
			}
			if activated(s) || s.pid() != 0 {
				t.Fatal("Resolve launched downstream provider")
			}
			provider, err := inference.New(inference.Config{Router: router.New(hosts...), Admit: p.nativeAdmit,
				Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "denied", nil },
				Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
					return nil, "denied"
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { provider.Close() })
			if tc.name == "chat" {
				request := iwire.Request{Model: "owner/fixture-model", Messages: []iwire.Message{{Role: iwire.RoleUser,
					Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hello"}}}}}
				if got := provider.Start(context.Background(), nativeEmbedCaller, request); got.Outcome != iwire.StartOutcomeNotPermitted {
					t.Fatalf("denied chat preflight: %+v", got)
				}
			} else {
				request := iwire.ImageRequest{Model: "owner/fixture-model", Mode: iwire.ImageModeGenerate, Prompt: "one light", Size: "auto", Count: 1,
					Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly}}
				if got := provider.StartImage(context.Background(), nativeEmbedCaller, request); got.Outcome != iwire.StartOutcomeNotPermitted {
					t.Fatalf("valid image request bypassed denied preflight: %+v", got)
				}
			}
			if activated(s) || s.pid() != 0 {
				t.Fatal("denied Start launched downstream provider")
			}
		})
	}
}

func TestCancelledNativeAdmissionDoesNotActivate(t *testing.T) {
	p, s, host := newNativeAdmitTestProvider(t, "cancelled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := p.nativeAdmit(inference.WithAdmissionProfile(ctx, router.ProfileChat), host, "owner/fixture-model"); ok || activated(s) {
		t.Fatal("cancelled admission activated an idle provider")
	}
}

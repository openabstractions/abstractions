package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

type imageEndpointDescriptor struct{}

func (imageEndpointDescriptor) DescribeService() (string, bool, string) {
	return inference.ImageContract, true, ""
}

func TestLaunchedProviderEndpointIsStableWithinOneRuntimeAndAccount(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := providerDeclaration{Name: "lemonade-image", Program: program, Endpoint: "lemonade-image", Transport: transportNative,
		Arguments: []string{"serve", "--endpoint", providerEndpointToken}, Contracts: []string{inference.ImageContract},
		Resources: []string{"profile:image"}, Models: []string{"stability/sdxl"}, Activation: wire.ActivationOnDemand.String()}
	f := providerFile{Declaration: d}
	state := t.TempDir()
	makeSupervisor := func(dir string, principal identity.User) *providerSupervisor {
		p := &runtimeProviders{dir: filepath.Join(dir, providersDir), principal: principal, supervisors: map[string]*providerSupervisor{}, report: func(error) {}}
		s := newProviderSupervisor(p, f)
		p.supervisors[d.Name] = s
		return s
	}
	account := identity.User{Kind: "windows", SID: "S-1-5-21-100"}
	first := makeSupervisor(state, account)
	restarted := makeSupervisor(state, account)
	otherRuntime := makeSupervisor(t.TempDir(), account)
	otherAccount := makeSupervisor(state, identity.User{Kind: "windows", SID: "S-1-5-21-200"})
	otherDeclaration := d
	otherDeclaration.Name = "second-image"
	otherName := newProviderSupervisor(first.p, providerFile{Declaration: otherDeclaration})
	if first.endpoint == providerEndpointPath(d.Endpoint) || first.endpoint != restarted.endpoint || first.endpoint == otherRuntime.endpoint || first.endpoint == otherAccount.endpoint {
		t.Fatalf("scoped endpoints: first=%q restart=%q runtime=%q account=%q", first.endpoint, restarted.endpoint, otherRuntime.endpoint, otherAccount.endpoint)
	}
	if first.endpoint == otherName.endpoint {
		t.Fatalf("distinct declarations share %q", first.endpoint)
	}
	if args := first.arguments(); len(args) != 3 || args[2] != first.endpoint {
		t.Fatalf("launch arguments %q", args)
	}
	if identity.CanEver(listen.Program) == nil {
		principal, err := currentPrincipal()
		if err != nil {
			t.Fatal(err)
		}
		probed := makeSupervisor(t.TempDir(), principal)
		probed.p.timing.probeBudget = 2 * time.Second
		listener, err := listen.ListenFramed(probed.endpoint, listen.Program)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		answered := make(chan error, 1)
		go func() {
			conn, err := listener.Accept()
			if err != nil {
				answered <- err
				return
			}
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if err != nil {
				answered <- err
				return
			}
			defer call.Close()
			reply, err := wire.DescribeEndpoint(call.Frame, "fixture", "", imageEndpointDescriptor{})
			if err == nil {
				err = call.Reply(reply)
			}
			answered <- err
		}()
		readiness, why := probed.probe()
		if readiness != wire.DeclarationReadinessReady {
			t.Fatalf("scoped probe: %s %q", readiness, why)
		}
		if err := <-answered; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	// The router uses the same supervisor address for native mediation.
	first.readiness = wire.DeclarationReadinessReady
	hosts := first.p.nativeInferenceHosts()
	if len(hosts) != 1 {
		t.Fatalf("native hosts %+v", hosts)
	}
	mediated, ok := hosts[0].NativeTransport()
	if !ok || mediated.Endpoint != first.endpoint || len(hosts[0].HostProfiles()) != 1 || hosts[0].HostProfiles()[0] != router.ProfileImage {
		t.Fatalf("mediation endpoint %#v, profiles %v", mediated, hosts[0].HostProfiles())
	}
}

func TestAttachOnlyProviderRetainsDeclaredEndpoint(t *testing.T) {
	p := &runtimeProviders{dir: filepath.Join(t.TempDir(), providersDir), principal: identity.User{Kind: "windows", SID: "S-1-5-21-100"}}
	f := providerFile{Declaration: providerDeclaration{Name: "external", Endpoint: "external-provider", Transport: transportNative, Activation: wire.ActivationAttach.String()}}
	s := newProviderSupervisor(p, f)
	if s.endpoint != providerEndpointPath(f.Declaration.Endpoint) {
		t.Fatalf("attach endpoint %q", s.endpoint)
	}
}

func TestLegacyLaunchedProviderWithoutEndpointTokenRetainsDeclaredEndpoint(t *testing.T) {
	p := &runtimeProviders{dir: filepath.Join(t.TempDir(), providersDir), principal: identity.User{Kind: "windows", SID: "S-1-5-21-100"}}
	f := providerFile{Declaration: providerDeclaration{Name: "legacy", Endpoint: "legacy-provider", Transport: transportNative,
		Activation: wire.ActivationOnDemand.String(), Arguments: []string{"serve", "--endpoint", "legacy-provider"}}}
	s := newProviderSupervisor(p, f)
	if s.endpoint != providerEndpointPath(f.Declaration.Endpoint) || s.arguments()[2] != "legacy-provider" {
		t.Fatalf("legacy endpoint %q and arguments %q", s.endpoint, s.arguments())
	}
}

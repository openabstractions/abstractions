package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/go/privatedir/privatetest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	state := privatetest.Dir(t)
	makeSupervisor := func(dir string, principal identity.User) *providerSupervisor {
		p := &runtimeProviders{dir: filepath.Join(dir, providersDir), principal: principal, supervisors: map[string]*providerSupervisor{}, report: func(error) {}, changes: make(chan struct{})}
		s := newFixtureProviderSupervisor(t, p, f)
		p.supervisors[d.Name] = s
		return s
	}
	account := identity.User{Kind: "windows", SID: "S-1-5-21-100"}
	first := makeSupervisor(state, account)
	restarted := makeSupervisor(state, account)
	otherRuntime := makeSupervisor(privatetest.Dir(t), account)
	otherAccount := makeSupervisor(state, identity.User{Kind: "windows", SID: "S-1-5-21-200"})
	otherDeclaration := d
	otherDeclaration.Name = "second-image"
	otherName := newFixtureProviderSupervisor(t, first.p, providerFile{Declaration: otherDeclaration})
	if first.endpoint == mustProviderEndpoint(t, d.Endpoint) || first.endpoint != restarted.endpoint || first.endpoint == otherRuntime.endpoint || first.endpoint == otherAccount.endpoint {
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
		probed := makeSupervisor(privatetest.Dir(t), principal)
		probed.p.timing.probeBudget = 2 * time.Second
		listener, err := listen.ListenFramed(probed.endpoint, listen.Program)
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		answered := make(chan error, 1)
		go func() {
			serve := func() error {
				conn, err := listener.Accept()
				if err != nil {
					return err
				}
				defer conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
				if err != nil {
					return err
				}
				defer call.Close()
				reply, err := wire.DescribeEndpoint(call.Frame, "fixture", "", imageEndpointDescriptor{})
				if err == nil {
					err = call.Reply(reply)
				}
				return err
			}
			// This legacy descriptor explicitly lacks endpoint@2 metadata. Its
			// unsupported observation is followed by one endpoint@1 readiness read.
			for range 2 {
				if err := serve(); err != nil {
					answered <- err
					return
				}
			}
			answered <- nil
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
	p := &runtimeProviders{dir: filepath.Join(privatetest.Dir(t), providersDir), principal: identity.User{Kind: "windows", SID: "S-1-5-21-100"}}
	f := providerFile{Declaration: providerDeclaration{Name: "external", Endpoint: "external-provider", Transport: transportNative, Activation: wire.ActivationAttach.String()}}
	s := newFixtureProviderSupervisor(t, p, f)
	if s.endpoint != mustProviderEndpoint(t, f.Declaration.Endpoint) {
		t.Fatalf("attach endpoint %q", s.endpoint)
	}
}

func TestLegacyLaunchedProviderWithoutEndpointTokenRetainsDeclaredEndpoint(t *testing.T) {
	p := &runtimeProviders{dir: filepath.Join(privatetest.Dir(t), providersDir), principal: identity.User{Kind: "windows", SID: "S-1-5-21-100"}}
	f := providerFile{Declaration: providerDeclaration{Name: "legacy", Endpoint: "legacy-provider", Transport: transportNative,
		Activation: wire.ActivationOnDemand.String(), Arguments: []string{"serve", "--endpoint", "legacy-provider"}}}
	s := newFixtureProviderSupervisor(t, p, f)
	if s.endpoint != mustProviderEndpoint(t, f.Declaration.Endpoint) || s.arguments()[2] != "legacy-provider" {
		t.Fatalf("legacy endpoint %q and arguments %q", s.endpoint, s.arguments())
	}
}

func mustProviderEndpoint(t *testing.T, name string) string {
	t.Helper()
	endpoint, err := fixtureProviderEndpoint(t, name)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint
}

func TestProviderNamespaceFailureStopsBeforeLaunchOrProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix namespace failure")
	}
	dir := privatetest.Dir(t)
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", dir)
	p := &runtimeProviders{dir: privatetest.Dir(t), report: func(error) {}}
	d := providerDeclaration{Name: "fixture", Endpoint: "fixture", Activation: wire.ActivationOnDemand.String(), Arguments: []string{providerEndpointToken}}
	s := newProviderSupervisor(p, providerFile{Declaration: d})
	if !errors.Is(s.endpointErr, bootstrap.ErrNoTrustedInstallation) || s.endpoint != "" {
		t.Fatalf("endpoint error %q %v", s.endpoint, s.endpointErr)
	}
	if readiness, why := s.probe(); readiness != wire.DeclarationReadinessNotReady || !strings.Contains(why, "endpoint:") {
		t.Fatalf("probe %s %q", readiness, why)
	}
	if more, _ := s.launch(0); more {
		t.Fatal("launched with invalid namespace")
	}
	var output, diagnostics strings.Builder
	if err := statusDescribe([]string{"model.v1_test"}, &output, &diagnostics); !errors.Is(err, bootstrap.ErrNoTrustedInstallation) || output.Len() != 0 {
		t.Fatalf("short status namespace refusal %v, output %q", err, output.String())
	}

}

func TestProviderEndpointPreservesDeclaredNameGrammar(t *testing.T) {
	for _, name := range []string{"model.v1", "model_v1"} {
		t.Run(name, func(t *testing.T) {
			expected := mustProviderEndpoint(t, name)
			for _, activation := range []string{wire.ActivationAttach.String(), wire.ActivationOnDemand.String()} {
				d := providerDeclaration{Name: "fixture", Endpoint: name, Activation: activation}
				s := newFixtureProviderSupervisor(t, &runtimeProviders{}, providerFile{Declaration: d})
				if s.endpointErr != nil || s.endpoint != expected {
					t.Fatalf("%s: endpoint %q: %v", activation, s.endpoint, s.endpointErr)
				}
			}
			l, err := listen.Listen(expected)
			if err != nil {
				t.Fatal(err)
			}
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, name := range []string{"../escape", "model/name", ""} {
		if _, err := providerEndpointPath(name); err == nil {
			t.Fatalf("accepted %q", name)
		}
	}
}

var fixtureProviderDirectories sync.Map // *testing.T -> its owned temporary directory

// One anchor per test keeps deterministic name comparisons within an owned namespace.
func fixtureProviderEndpoint(t *testing.T, name string) (string, error) {
	t.Helper()
	fingerprint := sha256.Sum256([]byte(t.Name()))
	anchor := fmt.Sprintf(`\\.\pipe\oa-test-provider-%d-%x-provider-v1`, os.Getpid(), fingerprint[:8])
	if runtime.GOOS != "windows" {
		directory, ok := fixtureProviderDirectories.Load(t)
		if !ok {
			directory, _ = fixtureProviderDirectories.LoadOrStore(t, shortSocketDir(t))
			t.Cleanup(func() { fixtureProviderDirectories.Delete(t) })
		}
		anchor = filepath.Join(directory.(string), "provider-v1.sock")
	}
	return providerEndpointPathFor(name, func(service string) (string, error) {
		if service != "provider-v1" {
			t.Fatal(service)
		}
		return anchor, nil
	})
}
func newFixtureProviderSupervisor(t *testing.T, p *runtimeProviders, f providerFile) *providerSupervisor {
	t.Helper()
	if p == nil {
		p = &runtimeProviders{}
	}
	p.endpointFor = func(name string) (string, error) { return fixtureProviderEndpoint(t, name) }
	return newProviderSupervisor(p, f)
}

func openFixtureProviders(t *testing.T, state string, declared bool, report func(error)) (*runtimeProviders, error) {
	t.Helper()
	return openProvidersWithEndpoint(state, declared, report, func(name string) (string, error) { return fixtureProviderEndpoint(t, name) })
}

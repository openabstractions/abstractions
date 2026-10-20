package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/listen"
)

func TestProviderEndpointTokenUsesProbeAddress(t *testing.T) {
	const name = "native-image-example"
	explicit := listen.Endpoint("literal-custom-address")
	declaration := providerDeclaration{Endpoint: name, Arguments: []string{"--endpoint", providerEndpointToken, "--label", name, "--other", explicit}}
	refused := newProviderSupervisor(nil, providerFile{Declaration: declaration})
	if !errors.Is(refused.endpointErr, bootstrap.ErrNoTrustedInstallation) || refused.endpoint != "" {
		t.Fatalf("default wrapper refusal: %q %v", refused.endpoint, refused.endpointErr)
	}
	s := newFixtureProviderSupervisor(t, nil, providerFile{Declaration: declaration})
	want, err := fixtureProviderEndpoint(t, name)
	if err != nil {
		t.Fatal(err)
	}
	if s.endpoint != want {
		t.Fatalf("probe endpoint = %q, want %q", s.endpoint, want)
	}
	got := s.arguments()
	if !reflect.DeepEqual(got, []string{"--endpoint", want, "--label", name, "--other", explicit}) {
		t.Fatalf("provider arguments = %q", got)
	}
	if declaration.Arguments[1] != providerEndpointToken || listen.Endpoint(got[1]) != want {
		t.Fatal("endpoint token mutated declaration or gained a second platform prefix")
	}
}

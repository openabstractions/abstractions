package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type providerEndpoint2Answer struct {
	value registration.Description
	err   error
}

func (a providerEndpoint2Answer) Describe() (registration.Description, error) { return a.value, a.err }

type providerEndpoint1Answer struct{ value wire.Description }

func (a providerEndpoint1Answer) Describe() (wire.Description, error) { return a.value, nil }

type providerEndpointFrames struct {
	next           registration.EndpointV2
	legacy         wire.Endpoint
	calls2, calls1 int
	alter          func([]byte) []byte
}

func (t *providerEndpointFrames) ExchangeFrame(frame []byte) ([]byte, error) {
	contract, err := wire.ServiceName(frame)
	if err != nil {
		return nil, err
	}
	if contract == "abstraction.facade/endpoint@2" {
		t.calls2++
		reply, err := (&registration.EndpointV2Dispatcher{Handler: t.next}).ExchangeFrame(frame)
		if err == nil && t.alter != nil {
			reply = t.alter(reply)
		}
		return reply, err
	}
	t.calls1++
	return (&wire.EndpointDispatcher{Handler: t.legacy}).ExchangeFrame(frame)
}
func providerEndpoint2Fixture() registration.Description {
	return registration.Description{Outcome: registration.DescriptionOutcomeDescribed, Program: "fixture", Version: "v",
		Services: []registration.ServiceState{{Service: "test/service@1", Readiness: wire.ServiceReadinessNotReady, Reason: "engine:loading", Guarantees: []string{"test/guarantee@1"}, Capabilities: map[string]string{"display": "value"}, DefinitionHash: "sha256:" + strings.Repeat("a", 64)}},
		Checks:   []registration.ProducerCheck{{Kind: "abstraction.provider.modelhost/engine", Copy: "release", State: registration.ProducerCheckStateCurrent, Checked: "2026-11-02T00:00:00.000000Z"}}}
}

func TestProviderEndpoint2GeneratedDescriptionAndLegacyFallback(t *testing.T) {
	description := providerEndpoint2Fixture()
	transport := &providerEndpointFrames{next: providerEndpoint2Answer{value: description}}
	legacy, observed, err := providerDescribeEndpoint(transport)
	if err != nil || observed == nil || len(observed.Checks) != 1 || observed.Checks[0].State != registration.ProducerCheckStateCurrent || observed.Services[0].DefinitionHash != description.Services[0].DefinitionHash {
		t.Fatalf("%#v %v", observed, err)
	}
	if legacy.Services[0].Readiness != wire.ServiceReadinessNotReady || legacy.Services[0].Why != "engine:loading" || transport.calls1 != 0 {
		t.Fatal(legacy, transport.calls1)
	}
	for _, code := range []string{"unknown_service", "unknown_method"} {
		want := wire.Description{Outcome: wire.DescriptionOutcomeDescribed, Program: "old", Version: "v1", Services: []wire.ServiceState{{Contract: "test/service@1", Readiness: wire.ServiceReadinessReady, Why: "", Guarantees: []string{}, Capabilities: map[string]string{}}}}
		old := &providerEndpointFrames{next: providerEndpoint2Answer{err: &registration.ServiceError{Code: registration.ServiceErrorCode(code)}}, legacy: providerEndpoint1Answer{want}}
		got, observed, err := providerDescribeEndpoint(old)
		if err != nil || observed != nil || old.calls2 != 1 || old.calls1 != 1 {
			t.Fatal(got, observed, err)
		}
		before, e := wire.ToJSON(&want)
		if e != nil {
			t.Fatal(e)
		}
		after, e := wire.ToJSON(&got)
		if e != nil || !bytes.Equal(before, after) {
			t.Fatal("legacy projection bytes changed", e)
		}
	}
}

func TestProviderEndpoint2SnapshotBindingAndTypedRefusal(t *testing.T) {
	s := &providerSupervisor{p: &runtimeProviders{changes: make(chan struct{})}, file: providerFile{Generation: "old", Declaration: providerDeclaration{Name: "fixture"}}}
	value := providerEndpoint2Fixture()
	legacy, observed, err := providerDescribeEndpoint(&providerEndpointFrames{next: providerEndpoint2Answer{value: value}})
	if err != nil {
		t.Fatal(err)
	}
	s.recordEndpoint(t.Context(), legacy.Services, nil, observed)
	observed.Checks[0].State = registration.ProducerCheckStateRefused
	first, ok := s.registrationDescription(s.file.bindingID())
	if !ok || first.Checks[0].State != registration.ProducerCheckStateCurrent {
		t.Fatal(first, ok)
	}
	first.Services[0].Capabilities["display"] = "changed"
	first.Checks[0].Copy = "changed"
	again, ok := s.registrationDescription(s.file.bindingID())
	if !ok || again.Services[0].Capabilities["display"] != "value" || again.Checks[0].Copy != "release" {
		t.Fatal(again, ok)
	}
	newFile := s.file
	newFile.Generation = "new"
	if _, ok := s.registrationDescription(newFile.bindingID()); ok {
		t.Fatal("old binding borrowed for new generation")
	}
	action := "fixture/read"
	refused := registration.Description{Outcome: registration.DescriptionOutcomeForbidden, Reason: "rights:denied", Refusal: &rights.RefusalDetail{Source: rights.RefusalSourceRights, Decision: rights.RefusalDecisionDenied, Ask: rights.RefusalAskNone, Action: action}}
	_, observation, err := providerDescribeEndpoint(&providerEndpointFrames{next: providerEndpoint2Answer{value: refused}})
	if err != nil || observation == nil {
		t.Fatal(observation, err)
	}
	s.recordEndpoint(t.Context(), nil, nil, observation)
	got, ok := s.registrationDescription(s.file.bindingID())
	if !ok || got.Refusal == nil || got.Refusal.Decision != rights.RefusalDecisionDenied || got.Refusal.Action != action {
		t.Fatal(got, ok)
	}
	got.Refusal.Action = "changed"
	again, ok = s.registrationDescription(s.file.bindingID())
	if !ok || again.Refusal.Action != action {
		t.Fatal("refusal alias", again)
	}
	s.record(t.Context(), nil, nil)
	if _, ok := s.registrationDescription(s.file.bindingID()); ok {
		t.Fatal("failed probe retained observation")
	}
}

func TestProviderEndpoint2MalformedBoundAndAuthorityNeverFallback(t *testing.T) {
	for _, alter := range []func([]byte) []byte{func(b []byte) []byte { return b[:len(b)/2] }, func(b []byte) []byte { return append(bytes.Repeat([]byte(" "), providerDescriptionFrameLimit), b...) }} {
		transport := &providerEndpointFrames{next: providerEndpoint2Answer{value: providerEndpoint2Fixture()}, alter: alter}
		_, observed, err := providerDescribeEndpoint(transport)
		if err == nil || observed != nil || transport.calls1 != 0 {
			t.Fatal("unsafe fallback", observed, err, transport.calls1)
		}
	}
	transport := &providerEndpointFrames{next: providerEndpoint2Answer{err: &registration.ServiceError{Code: registration.ServiceErrorCode("forbidden"), Message: "identity"}}}
	_, observed, err := providerDescribeEndpoint(transport)
	if err == nil || observed != nil || transport.calls1 != 0 {
		t.Fatal("authority fallback", observed, err)
	}
}

func TestProviderEndpoint2ExplicitUnsupportedRetainsObservationWithOneLegacyRead(t *testing.T) {
	legacy := wire.Description{Outcome: wire.DescriptionOutcomeDescribed, Program: "legacy", Services: []wire.ServiceState{{Contract: "test/service@1", Readiness: wire.ServiceReadinessReady, Guarantees: []string{}, Capabilities: map[string]string{}}}}
	for _, value := range []registration.Description{{Outcome: registration.DescriptionOutcomeUnsupported, Reason: "unsupported:not_implemented"}, {Outcome: registration.DescriptionOutcomeUnsupported, Reason: "unsupported:other"}, {Outcome: registration.DescriptionOutcomeUnavailable, Reason: "unsupported:not_implemented"}} {
		transport := &providerEndpointFrames{next: providerEndpoint2Answer{value: value}, legacy: providerEndpoint1Answer{legacy}}
		got, observed, err := providerDescribeEndpoint(transport)
		if err != nil || observed == nil || observed.Outcome != value.Outcome || observed.Reason != value.Reason {
			t.Fatal(got, observed, err)
		}
		fallback := value.Outcome == registration.DescriptionOutcomeUnsupported && value.Reason == "unsupported:not_implemented"
		if fallback {
			if transport.calls1 != 1 || got.Outcome != wire.DescriptionOutcomeDescribed {
				t.Fatal(got, transport.calls1)
			}
		} else if transport.calls1 != 0 {
			t.Fatal("broadened fallback", value, transport.calls1)
		}
		if len(observed.Services) != 0 || len(observed.Checks) != 0 {
			t.Fatal("fabricated endpoint2 success", observed)
		}
	}
}

func TestProviderEndpoint2IdentityBoundProbeKeepsChecksIndependent(t *testing.T) {
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skip(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	for _, ready := range []bool{false, true} {
		endpoint := shortMediationEndpoint(t)
		p := &runtimeProviders{principal: principal, timing: defaultProviderTiming, changes: make(chan struct{})}
		s := &providerSupervisor{p: p, endpoint: endpoint, file: providerFile{Generation: "generation", Declaration: providerDeclaration{Name: "fixture", Program: program, Transport: transportNative, Contracts: []string{"test/service@1"}}}}
		listener, err := listen.ListenFramed(endpoint, listen.Program)
		if err != nil {
			t.Fatal(err)
		}
		description := providerEndpoint2Fixture()
		if ready {
			description.Services[0].Readiness = wire.ServiceReadinessReady
			description.Services[0].Reason = ""
		}
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
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, providerDescriptionFrameLimit)
			if err != nil {
				answered <- err
				return
			}
			defer call.Close()
			reply, err := (&registration.EndpointV2Dispatcher{Handler: providerEndpoint2Answer{value: description}}).ExchangeFrame(call.Frame)
			if err == nil {
				err = call.Reply(reply)
			}
			answered <- err
		}()
		readiness, why := s.probe()
		_ = listener.Close()
		if err := <-answered; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		want := wire.DeclarationReadinessNotReady
		if ready {
			want = wire.DeclarationReadinessReady
		}
		if readiness != want {
			t.Fatal(readiness, why)
		}
		observation, ok := s.registrationDescription(s.file.bindingID())
		if !ok || observation.Checks[0].State != registration.ProducerCheckStateCurrent {
			t.Fatal(observation, ok)
		}
	}
}

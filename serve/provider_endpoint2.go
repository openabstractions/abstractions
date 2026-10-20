package main

import (
	"bytes"
	"context"
	"errors"
	"slices"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

const providerDescriptionFrameLimit = 1 << 20

// boundedProviderDescription retains the existing identity-bound connection.
// The endpoint@2 value ceiling and the complete reply-frame ceiling are distinct.
type boundedProviderDescription struct{ wire.EndpointTransport }

func (t boundedProviderDescription) ExchangeFrame(frame []byte) ([]byte, error) {
	reply, err := t.EndpointTransport.ExchangeFrame(frame)
	if err == nil && len(reply) > providerDescriptionFrameLimit {
		return nil, errors.New("bound:reply_bytes")
	}
	return reply, err
}

func providerDescribeEndpoint(transport wire.EndpointTransport) (wire.Description, *registration.Description, error) {
	description, err := registration.NewEndpointV2Client(boundedProviderDescription{transport}).Describe()
	var refusal *registration.ServiceError
	if errors.As(err, &refusal) && (string(refusal.Code) == "unknown_service" || string(refusal.Code) == "unknown_method") {
		legacy, err := wire.NewEndpointClient(transport).Describe()
		return legacy, nil, err
	}
	if err != nil {
		return wire.Description{}, nil, err
	}
	if description.Outcome != registration.DescriptionOutcomeDescribed &&
		(description.Program != "" || description.Version != "" || len(description.Services) != 0 || len(description.Checks) != 0) {
		return wire.Description{}, nil, errors.New("invalid endpoint description")
	}
	if description.Refusal != nil && description.Outcome != registration.DescriptionOutcomeForbidden && description.Outcome != registration.DescriptionOutcomeUnavailable {
		return wire.Description{}, nil, errors.New("invalid endpoint refusal")
	}
	// Encoding/decoding through the canonical owner bounds and isolates all maps,
	// slices and Rights refusal pointers before the observation enters supervision.
	raw, err := registration.ToJSON(&description)
	if err != nil {
		return wire.Description{}, nil, err
	}
	var observed registration.Description
	if err := registration.FromJSON(raw, &observed); err != nil {
		return wire.Description{}, nil, err
	}
	if providerEndpointCompatibilityAbsent(&observed) {
		legacy, err := wire.NewEndpointClient(transport).Describe()
		return legacy, &observed, err
	}
	legacy := wire.Description{}
	if observed.Outcome == registration.DescriptionOutcomeDescribed {
		legacy.Outcome, legacy.Program, legacy.Version = wire.DescriptionOutcomeDescribed, observed.Program, observed.Version
		legacy.Services = []wire.ServiceState{}
		for _, service := range observed.Services {
			legacy.Services = append(legacy.Services, wire.ServiceState{Contract: service.Service, Readiness: service.Readiness,
				Why: service.Reason, Guarantees: service.Guarantees, Capabilities: service.Capabilities})
		}
	}
	return legacy, &observed, nil
}

func providerEndpointCompatibilityAbsent(description *registration.Description) bool {
	return description != nil && description.Outcome == registration.DescriptionOutcomeUnsupported &&
		description.Reason == "unsupported:not_implemented" && description.Refusal == nil
}

// registrationDescription returns one genuine endpoint@2 observation from the
// exact immutable supervisor binding. Registry owns current-generation eligibility.
func (s *providerSupervisor) registrationDescription(bindingID string) (registration.Description, bool) {
	if bindingID != s.file.bindingID() {
		return registration.Description{}, false
	}
	s.mu.Lock()
	raw := bytes.Clone(s.endpointDescription)
	s.mu.Unlock()
	if len(raw) == 0 {
		return registration.Description{}, false
	}
	var description registration.Description
	if err := registration.FromJSON(raw, &description); err != nil {
		return registration.Description{}, false
	}
	return description, true
}

func (s *providerSupervisor) recordEndpoint(ctx context.Context, services []wire.ServiceState, stores []string, observation *registration.Description) {
	var raw []byte
	if observation != nil {
		var err error
		raw, err = registration.ToJSON(observation)
		if err != nil {
			raw = nil
		}
	}
	accepted := s.acceptStores(ctx, stores)
	s.mu.Lock()
	changed := !bytes.Equal(s.endpointDescription, raw) || !slices.Equal(s.accepted, accepted)
	s.described, s.accepted, s.endpointDescription = slices.Clone(services), accepted, raw
	s.mu.Unlock()
	if changed {
		s.p.notify()
	}
}

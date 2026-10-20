package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configclient "github.com/openabstractions/abstraction-config/go/client"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// configureRuntimeTypedConfig uses the existing registration and logging
// owners. It creates no designation table and enables no agent start profile.
func configureRuntimeTypedConfig(options *host.Options, applications *applicationsHost, rights *runtimeRights, logOutput string) {
	knownKey := func(key string) bool {
		for _, d := range wire.ConfigKeyDescriptors() {
			if d.Key == key {
				return true
			}
		}
		return false
	}
	var providerProof configservice.KeyReadPolicy
	var providerProofKeys configservice.KeyReadSetPolicy
	if applications != nil && applications.directory != nil {
		directory := applications.directory
		directory.programConfigKeyKnown = knownKey
		providerProof = directory.providerConfigReadProof
		providerProofKeys = directory.providerConfigReadProofKeys
	}
	// Operators receive independent exact key-read rules from the existing
	// installation owner. A write grant is never used as a read grant. An
	// operator is admitted before the registry is read: either proof admits.
	operator := func(ctx context.Context, peer *identity.Peer) bool {
		return rights != nil && rights.authorizeOperator(ctx, peer) == nil
	}
	undesignated := fmt.Errorf("%w: native key-read designation required", configservice.ErrKeyNotDesignated)
	proof := func(ctx context.Context, peer *identity.Peer, key string) error {
		if operator(ctx, peer) {
			return nil
		}
		if providerProof != nil {
			return providerProof(ctx, peer, key)
		}
		return undesignated
	}
	proofKeys := func(ctx context.Context, peer *identity.Peer, keys []string) map[string]error {
		out := make(map[string]error, len(keys))
		if operator(ctx, peer) {
			for _, key := range keys {
				out[key] = nil
			}
			return out
		}
		if providerProofKeys != nil {
			return providerProofKeys(ctx, peer, keys)
		}
		for _, key := range keys {
			out[key] = undesignated
		}
		return out
	}
	options.ConfigTyped.ReadPolicy = host.ConfigKeyReadPolicyFromRights(rights.decider(), proof)
	options.ConfigTyped.ReadPolicyKeys = host.ConfigKeyReadSetPolicyFromRights(rights.decider(), proofKeys)
	if rights != nil {
		// This is an independent current-owner claim, not a cached permit.
		// Provider retirement must not withdraw a settings client's defaults.
		rights.configReadDefault = func(program, key string) bool { return rights.isOperator(program) && knownKey(key) }
	}
	path, pathErr := runtimeLogPath(logOutput)
	options.ConfigTyped.DynamicDefault = func(key string) (*wire.Value, error) {
		if key != "logs_max_use" {
			return nil, errors.New("config: dynamic default owner unavailable")
		}
		if pathErr != nil {
			return nil, pathErr
		}
		retention, err := logging.DefaultLogsRetention(path)
		if err != nil {
			return nil, err
		}
		return &wire.Value{Kind: wire.ValueKindBytes, Bytes: retention.MaxUse}, nil
	}
}

func admitRuntimeNASStore(state *configservice.RestartState, selected string) error {
	return state.Admit("nas_store", &wire.Value{Kind: wire.ValueKindText, Text: selected})
}

func runtimeConfigClient(options runtimeFlags) (*configclient.Client, error) {
	transport, err := runtimeConfigTransport(options)
	if err != nil {
		return nil, err
	}
	return configclient.NewWithTransport(transport), nil
}

func runtimeConfigTransport(options runtimeFlags) (listen.FrameClient, error) {
	endpoint := options.configEndpoint
	if endpoint == "" {
		var err error
		endpoint, err = options.companionEndpoint("config-v1", "", "config")
		if err != nil {
			return listen.FrameClient{}, err
		}
	}
	who, _, err := principal()
	if err != nil {
		return listen.FrameClient{}, err
	}
	program, err := os.Executable()
	if err != nil {
		return listen.FrameClient{}, err
	}
	expected := listen.ServerExpectation{Principal: who, Program: filepath.Clean(program)}
	return listen.FrameClient{Endpoint: endpoint, Server: &expected}, nil
}

func runtimeMessageStartSettings(ctx context.Context, options runtimeFlags) (messageStartSettings, error) {
	reader, err := runtimeConfigClient(options)
	if err != nil {
		return messageStartSettings{}, err
	}
	names := []string{"messages_start_questions", "messages_start_notes", "messages_start_per_hour", "messages_start_open_app"}
	result, err := reader.ReadTypedContext(ctx, names)
	if err != nil {
		return messageStartSettings{}, err
	}
	if result.Outcome != wire.ReadOutcomeRead || len(result.Refused) != 0 || len(result.Entries) != len(names) {
		return messageStartSettings{}, fmt.Errorf("messages: start settings unavailable: %s", result.Outcome)
	}
	values := map[string]*wire.Value{}
	for _, entry := range result.Entries {
		values[entry.Key] = entry.Value
	}
	settings := defaultMessageStartSettings()
	for key, target := range map[string]*bool{"messages_start_questions": &settings.Questions, "messages_start_notes": &settings.Notes, "messages_start_open_app": &settings.OpenApp} {
		value := values[key]
		if value == nil || value.Kind != wire.ValueKindBoolean {
			return messageStartSettings{}, fmt.Errorf("messages: invalid start setting: %s", key)
		}
		*target = value.Boolean
	}
	rate := values["messages_start_per_hour"]
	if rate == nil || rate.Kind != wire.ValueKindChoice {
		return messageStartSettings{}, errors.New("messages: invalid start rate")
	}
	settings.PerHour, err = strconv.Atoi(rate.Choice)
	if err != nil || !validMessageStartRate(settings.PerHour) {
		return messageStartSettings{}, errors.New("messages: invalid start rate")
	}
	return settings, nil
}

func configureRuntimeMessageStartSettings(applications *applicationsHost, options runtimeFlags) {
	if applications != nil {
		applications.messageStartSettings = func(ctx context.Context, _ string) (messageStartSettings, error) {
			return runtimeMessageStartSettings(ctx, options)
		}
	}
}

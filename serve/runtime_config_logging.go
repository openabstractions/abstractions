package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configclient "github.com/openabstractions/abstraction-config/go/client"
	logging "github.com/openabstractions/abstraction-logging/go"
)

func configDuration(value *wire.Value) (time.Duration, error) {
	if value == nil || value.Kind != wire.ValueKindDuration || value.DurationMs < 0 || value.DurationMs > math.MaxInt64/int64(time.Millisecond) {
		return 0, errors.New("config: duration is not representable")
	}
	return time.Duration(value.DurationMs) * time.Millisecond, nil
}

func runtimeLogsRetention(entries []wire.Entry) (logging.ResolvedLogsRetention, error) {
	var policy logging.ResolvedLogsRetention
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.Key != "logs_max_use" && entry.Key != "logs_max_retention" {
			continue
		}
		if seen[entry.Key] {
			return policy, errors.New("logging: duplicate config entry")
		}
		seen[entry.Key] = true
		value := entry.Value
		if entry.Key == "logs_max_use" {
			if value == nil {
				return policy, errors.New("logging: resolved size required")
			}
			if value.Kind == wire.ValueKindUnlimited {
				continue
			}
			if value.Kind != wire.ValueKindBytes || value.Bytes < 0 {
				return policy, errors.New("logging: invalid configured size")
			}
			bytes := value.Bytes
			policy.MaxUse = &bytes
		} else {
			// This catalogue key has no default age bound.
			if value == nil || value.Kind == wire.ValueKindUnlimited {
				continue
			}
			duration, err := configDuration(value)
			if err != nil {
				return policy, err
			}
			policy.MaxRetention = &duration
		}
	}
	if !seen["logs_max_use"] || !seen["logs_max_retention"] {
		return policy, errors.New("logging: config keys not admitted")
	}
	return policy, nil
}

// runtimeLogSettingsWait is how long one Config observation of the logging
// settings waits; a variable so a test can shorten it.
var runtimeLogSettingsWait = 30 * time.Second

// followRuntimeLogSettings applies service-effective policy through the existing
// retained sink owner. The native Config observer owns waiting and invalidation.
func followRuntimeLogSettings(ctx context.Context, options runtimeFlags, sink *logging.FileSink, report func(error)) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fail := func(err error) {
			if ctx.Err() == nil && report != nil {
				report(fmt.Errorf("logging config: %w", err))
			}
		}
		transport, err := runtimeConfigTransport(options)
		if err != nil {
			fail(err)
			return
		}
		// The client's observation limit covers the wait (CFG-W12).
		reader := configclient.NewWithTransport(transport)
		cursor := ""
		wait := runtimeLogSettingsWait
		for ctx.Err() == nil {
			started := time.Now()
			observation, err := reader.ObserveTypedContext(ctx, cursor, wait.Milliseconds())
			if err != nil {
				fail(err)
				return
			}
			switch observation.Outcome {
			case wire.ObservationOutcomeV2Changed:
				if observation.Snapshot == nil {
					fail(errors.New("missing snapshot"))
					return
				}
				policy, err := runtimeLogsRetention(observation.Snapshot.Entries)
				if err != nil {
					fail(err)
					return
				}
				if err := sink.SetResolvedRetention(policy); err != nil {
					fail(err)
				}
				cursor = observation.Cursor
			case wire.ObservationOutcomeV2Unchanged:
			case wire.ObservationOutcomeV2Gap:
				cursor = ""
			case wire.ObservationOutcomeV2Corrupt, wire.ObservationOutcomeV2Unavailable:
				fail(fmt.Errorf("settings %s", observation.Outcome))
				// A stable source outage waits before returning the same cursor.
				// Fast authority/capacity refusals retain their terminal behavior.
				if observation.Cursor == "" || (observation.Cursor == cursor && time.Since(started) < wait) {
					return
				}
				cursor = observation.Cursor
			default:
				fail(fmt.Errorf("observer %s", observation.Outcome))
				return
			}
		}
	}()
	return func() { <-done }
}

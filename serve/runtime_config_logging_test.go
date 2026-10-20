package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
)

type unavailableLogConfigStore struct {
	casapi.Store
	unavailable atomic.Bool
}

func (s *unavailableLogConfigStore) Read(path string) (casapi.Value, error) {
	if s.unavailable.Load() {
		return casapi.Value{}, errors.New("inert config read outage")
	}
	return s.Store.Read(path)
}

func TestConfigNativeLoggingContinuesAfterStableUnavailable(t *testing.T) {
	previous := runtimeLogSettingsWait
	runtimeLogSettingsWait = 2 * time.Second
	t.Cleanup(func() { runtimeLogSettingsWait = previous })
	options := runtimeFlags{stateDir: t.TempDir(), configEndpoint: listen.Endpoint(fmt.Sprintf("log-config-outage-%d-%d", os.Getpid(), runtimeNASConfigSerial.Add(1)))}
	path := filepath.Join(t.TempDir(), "config.json")
	write := func(size int) {
		t.Helper()
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"logs_max_use":%d}`, size)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(64)
	store := &unavailableLogConfigStore{Store: casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}}
	host, err := configservice.ListenWithStore(options.configEndpoint, store, path)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		t.Skip("Darwin Unix refused Program proof; this native Config fixture needs a Program-capable endpoint")
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := host.OmitMachineRung(); err != nil {
		host.Close()
		t.Fatal(err)
	}
	if err := host.EnableTypedConfig(configservice.TypedOptions{ReadPolicy: func(_ context.Context, _ *identity.Peer, key string) error {
		if key == "logs_max_use" || key == "logs_max_retention" {
			return nil
		}
		return errors.New("outside logging grant")
	}}); err != nil {
		host.Close()
		t.Fatal(err)
	}
	if err := host.EnableObservation(configservice.FileObservation(path)); err != nil {
		host.Close()
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- host.Serve(context.Background()) }()
	t.Cleanup(func() {
		host.Close()
		if err := <-served; err != nil {
			t.Error(err)
		}
	})
	sink, err := logging.OpenResolvedRetainedFileSink(filepath.Join(t.TempDir(), "records"), logging.ResolvedLogsRetention{})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errorsSeen := make(chan error, 8)
	join := followRuntimeLogSettings(ctx, options, sink, func(err error) { errorsSeen <- err })
	defer func() { cancel(); join() }()
	waitPolicy := func(size int64) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			usage, err := sink.Usage()
			if err != nil {
				t.Fatal(err)
			}
			if usage.Usage != nil && usage.Usage.MaxUse != nil && *usage.Usage.MaxUse == size {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("logging detached after config outage", usage)
			}
			runtime.Gosched()
		}
	}
	waitPolicy(64)
	store.unavailable.Store(true)
	write(64)
	select {
	case <-errorsSeen:
	case <-time.After(4 * time.Second):
		t.Fatal("initial outage not observed")
	}
	// The second unavailable is the actual controller's native wait
	// (runtimeLogSettingsWait) expiring at the same error cursor. Repair must
	// keep this follower alive.
	select {
	case <-errorsSeen:
	case <-time.After(35 * time.Second):
		t.Fatal("stable unavailable wait did not return")
	}
	store.unavailable.Store(false)
	write(512)
	waitPolicy(512)
}

func TestConfigNativeRetentionDurationChecksBeforeConversion(t *testing.T) {
	max := int64(math.MaxInt64) / int64(time.Millisecond)
	for _, milliseconds := range []int64{0, 1, max} {
		value, err := configDuration(&wire.Value{Kind: wire.ValueKindDuration, DurationMs: milliseconds})
		if err != nil || value.Milliseconds() != milliseconds {
			t.Fatal(milliseconds, value, err)
		}
	}
	for _, value := range []*wire.Value{nil, {Kind: wire.ValueKindUnlimited}, {Kind: wire.ValueKindDuration, DurationMs: -1}, {Kind: wire.ValueKindDuration, DurationMs: max + 1}} {
		if _, err := configDuration(value); err == nil {
			t.Fatal("unrepresentable duration accepted", value)
		}
	}
	zero, err := runtimeLogsRetention([]wire.Entry{{Key: "logs_max_use", Value: &wire.Value{Kind: wire.ValueKindBytes}}, {Key: "logs_max_retention", Value: &wire.Value{Kind: wire.ValueKindDuration}}})
	if err != nil || zero.MaxUse == nil || *zero.MaxUse != 0 || zero.MaxRetention == nil || *zero.MaxRetention != 0 {
		t.Fatal(zero, err)
	}
	unlimited, err := runtimeLogsRetention([]wire.Entry{{Key: "logs_max_use", Value: &wire.Value{Kind: wire.ValueKindUnlimited}}, {Key: "logs_max_retention", Value: &wire.Value{Kind: wire.ValueKindUnlimited}}})
	if err != nil || unlimited.MaxUse != nil || unlimited.MaxRetention != nil {
		t.Fatal(unlimited, err)
	}
}

func TestConfigNativeLoggingFollowsZeroUnlimitedAndCorruptRepair(t *testing.T) {
	options := runtimeFlags{stateDir: t.TempDir()}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"logs_max_use":1048576}`), 0600); err != nil {
		t.Fatal(err)
	}
	serveRuntimeNASConfig(t, &options, path)
	sink, err := logging.OpenResolvedRetainedFileSink(filepath.Join(t.TempDir(), "records"), logging.ResolvedLogsRetention{})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	ctx, cancel := context.WithCancel(context.Background())
	errorsSeen := make(chan error, 8)
	join := followRuntimeLogSettings(ctx, options, sink, func(err error) { errorsSeen <- err })
	defer func() { cancel(); join() }()
	wait := func(unlimited bool, max int64) {
		t.Helper()
		deadline := time.Now().Add(4 * time.Second)
		for {
			usage, err := sink.Usage()
			if err != nil {
				t.Fatal(err)
			}
			if usage.Usage != nil && ((unlimited && usage.Usage.MaxUse == nil) || (!unlimited && usage.Usage.MaxUse != nil && *usage.Usage.MaxUse == max)) {
				return
			}
			if time.Now().After(deadline) {
				if usage.Usage != nil && usage.Usage.MaxUse != nil {
					t.Log("current size limit", *usage.Usage.MaxUse)
				}
				for len(errorsSeen) != 0 {
					t.Log("config follower error", <-errorsSeen)
				}
				t.Fatal("policy did not follow native Config", usage)
			}
			runtime.Gosched()
		}
	}
	wait(false, 1048576)
	if err := os.WriteFile(path, []byte("!"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errorsSeen:
	case <-time.After(4 * time.Second):
		t.Fatal("corruption not reported")
	}
	if err := os.WriteFile(path, []byte(`{"logs_max_use":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	wait(false, 0)
	record := logging.Record{Schema: 1, Time: logging.Timestamp{Time: time.Now().UTC()}, Msg: "bounded"}
	if err := sink.Write(record); err != nil {
		t.Fatal("zero retention refused a write", err)
	}
	usage, err := sink.Usage()
	if err != nil || usage.Usage == nil || usage.Usage.RetainedBytes != 0 {
		t.Fatal("zero retention kept a write", usage, err)
	}
	if err := os.WriteFile(path, []byte(`{"logs_max_use":1,"logs_max_retention":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	wait(false, 1)
	if err := sink.Write(record); err != nil {
		t.Fatal("zero age with positive size refused a write", err)
	}
	usage, err = sink.Usage()
	if err != nil || usage.Usage == nil || usage.Usage.MaxRetentionMs == nil || *usage.Usage.MaxRetentionMs != 0 || usage.Usage.RetainedBytes != 0 {
		t.Fatal("mixed limits did not retain nothing", usage, err)
	}
	if err := os.WriteFile(path, []byte(`{"logs_max_use":{"kind":"unlimited"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	wait(true, 0)
	if err := sink.Write(record); err != nil {
		t.Fatal(err)
	}
}

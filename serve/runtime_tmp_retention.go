package main

import (
	"context"
	"errors"
	"time"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
)

type jobTmpCollector interface {
	CollectJobTmp(context.Context, time.Time, time.Duration) ([]acceptanceprovider.TmpRemoval, error)
}

// collectRuntimeJobTmp is an inert single-pass owner for the future shared
// retention scope. Nothing schedules or calls it in 0.8.0. Activation requires
// the separate installed/data-loss acceptance for 0.9.0.
func collectRuntimeJobTmp(ctx context.Context, options runtimeFlags, now time.Time, collector jobTmpCollector, sink logging.Sink) error {
	reader, err := runtimeConfigClient(options)
	if err != nil {
		return err
	}
	return collectRuntimeJobTmpWith(ctx, now, collector, sink, reader.ReadTypedContext)
}

func collectRuntimeJobTmpWith(ctx context.Context, now time.Time, collector jobTmpCollector, sink logging.Sink, read func(context.Context, []string) (wire.ReadResult, error)) error {
	if collector == nil || read == nil {
		return errors.New("runtime: temporary collection owner unavailable")
	}
	duration, level, err := readTmpMaxRetention(ctx, read)
	if err != nil {
		return err
	}
	if sink == nil {
		return errors.New("runtime: temporary removal logging unavailable")
	}
	// Every explicit pass reads current policy; unavailable policy never uses
	// a cached answer to authorize deletion.
	removed, collectErr := collector.CollectJobTmp(ctx, now, duration)
	var logErrors []error
	for _, removal := range removed {
		if err := sink.Write(logging.Record{Time: logging.At(now), Level: logging.LevelInfo, Msg: "abstraction.storage/retention.removed", Attrs: map[string]string{
			"class": "tmp", "key": removal.OperationID, "name": removal.Name, "level": level, "kind": "download",
		}}); err != nil {
			logErrors = append(logErrors, err)
		}
	}
	return errors.Join(append(logErrors, collectErr)...)
}

// readTmpMaxRetention reads the person's tmp_max_retention for one pass and
// the configuration level that set it. An unreadable, refused or non-positive
// value is an error: no cached answer authorizes a removal.
func readTmpMaxRetention(ctx context.Context, read func(context.Context, []string) (wire.ReadResult, error)) (time.Duration, string, error) {
	if read == nil {
		return 0, "", errors.New("runtime: temporary retention setting unavailable")
	}
	answer, err := read(ctx, []string{"tmp_max_retention"})
	if err != nil {
		return 0, "", err
	}
	if answer.Outcome != wire.ReadOutcomeRead || answer.Refusal != nil || len(answer.Refused) != 0 || len(answer.Entries) != 1 || answer.Entries[0].Key != "tmp_max_retention" {
		return 0, "", errors.New("runtime: temporary retention setting unavailable")
	}
	entry := answer.Entries[0]
	duration, err := configDuration(entry.Value)
	if err != nil || duration <= 0 {
		return 0, "", errors.New("runtime: positive temporary retention required")
	}
	return duration, entry.Origin.Location.String(), nil
}

package main

import (
	"context"
	"errors"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// jobPartialCollector removes the partial files of failed or cancelled work.
// Completed results, messages, records and locks keep their own retention,
// which arrives in 0.9.0.
type jobPartialCollector interface {
	CollectJobPartials(context.Context, time.Time, time.Duration) ([]acceptanceprovider.TmpRemoval, error)
}

// minimumPartialAge is the least time a failed or cancelled operation's
// partial file stays after the operation ended. A shorter tmp_max_retention
// setting is raised to it.
const minimumPartialAge = 24 * time.Hour

// Runtime partial collection runs one pass this long after start, then once
// a day.
var (
	runtimeJobTmpFirstPass = 10 * time.Minute
	runtimeJobTmpInterval  = 24 * time.Hour
	runtimeJobTmpClock     = time.Now
)

// serveRuntimeJobTmp schedules partial collection for the runtime's lifetime.
// Each pass reads tmp_max_retention again, and a pass whose policy cannot be
// read removes nothing. The returned function waits for a running pass after
// ctx ends.
func serveRuntimeJobTmp(ctx context.Context, options runtimeFlags, collector jobPartialCollector, sink logging.Sink, report func(error)) func() {
	read := func(ctx context.Context, keys []string) (wire.ReadResult, error) {
		reader, err := runtimeConfigClient(options)
		if err != nil {
			return wire.ReadResult{}, err
		}
		return reader.ReadTypedContext(ctx, keys)
	}
	return scheduleRuntimeJobTmp(ctx, runtimeJobTmpFirstPass, runtimeJobTmpInterval, runtimeJobTmpClock, collector, sink, read, report)
}

func scheduleRuntimeJobTmp(ctx context.Context, first, interval time.Duration, clock func() time.Time, collector jobPartialCollector, sink logging.Sink, read func(context.Context, []string) (wire.ReadResult, error), report func(error)) func() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTimer(first)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
			// A pass ends before the next one is due.
			pass, cancel := context.WithTimeout(ctx, interval)
			err := collectRuntimeJobTmpWith(pass, clock(), collector, sink, read)
			cancel()
			if err != nil && ctx.Err() == nil && report != nil {
				report(errors.Join(errors.New("runtime: job partial collection"), err))
			}
			timer.Reset(interval)
		}
	}()
	return wg.Wait
}

// collectRuntimeJobTmpWith runs one partial pass with policy read for it.
// Every removal writes one log record naming the operation and the file.
func collectRuntimeJobTmpWith(ctx context.Context, now time.Time, collector jobPartialCollector, sink logging.Sink, read func(context.Context, []string) (wire.ReadResult, error)) error {
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
	age := max(duration, minimumPartialAge)
	// Every pass reads current policy; unavailable policy never uses a cached
	// answer to authorize deletion.
	removed, collectErr := collector.CollectJobPartials(ctx, now, age)
	var logErrors []error
	for _, removal := range removed {
		if err := sink.Write(logging.Record{Time: logging.At(now), Level: logging.LevelInfo, Msg: "abstraction.storage/retention.removed", Attrs: map[string]string{
			"class": "tmp", "key": removal.OperationID, "name": removal.Name, "level": level, "kind": "download",
			"reason": "partial of failed or cancelled work", "age": age.String(),
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

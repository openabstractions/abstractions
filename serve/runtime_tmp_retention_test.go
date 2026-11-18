package main

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	inference "github.com/openabstractions/abstraction-inference/go"
	job "github.com/openabstractions/abstraction-job/go"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The runtime schedules partial collection from one site. Full collection,
// which also removes completed results, stays unscheduled until 0.9.0.
func TestRuntimeJobTmpOnlyPartialCollectionIsScheduled(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	scheduled := 0
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			var name string
			switch target := call.Fun.(type) {
			case *ast.Ident:
				name = target.Name
			case *ast.SelectorExpr:
				name = target.Sel.Name
			}
			switch name {
			case "CollectJobTmp", "CollectTmp":
				t.Errorf("%s calls full temporary collection, which removes completed results", path)
			case "serveRuntimeJobTmp":
				if path != "runtime.go" {
					t.Errorf("%s schedules partial collection outside the runtime start", path)
				}
				scheduled++
			}
			return true
		})
	}
	if scheduled != 1 {
		t.Fatalf("runtime schedules partial collection %d times, want 1", scheduled)
	}
}

type tmpPassFixture struct {
	mu       sync.Mutex
	calls    []time.Duration
	removals []acceptanceprovider.TmpRemoval
}

func (f *tmpPassFixture) CollectJobPartials(_ context.Context, _ time.Time, age time.Duration) ([]acceptanceprovider.TmpRemoval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, age)
	return f.removals, nil
}

func (f *tmpPassFixture) passes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type tmpLogFixture struct {
	mu      sync.Mutex
	records []logging.Record
}

func (f *tmpLogFixture) Write(r logging.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, r)
	return nil
}

func tmpConfigFixture() wire.ReadResult {
	return wire.ReadResult{Outcome: wire.ReadOutcomeRead, Entries: []wire.Entry{{Key: "tmp_max_retention", Value: &wire.Value{Kind: wire.ValueKindDuration, DurationMs: 864000000}, Origin: wire.KeyOrigin{Location: wire.LocationUser}}}}
}

func TestRuntimeJobTmpPolicyFailureNeverCallsCollector(t *testing.T) {
	for _, name := range []string{"read-error", "outcome", "refused", "refusal", "count", "key", "nil", "zero", "negative", "wrong-kind", "overflow"} {
		t.Run(name, func(t *testing.T) {
			answer := tmpConfigFixture()
			var readErr error
			switch name {
			case "read-error":
				readErr = errors.New("fixture unavailable")
			case "outcome":
				answer.Outcome = wire.ReadOutcomeUnavailable
			case "refused":
				answer.Refused = []wire.KeyRefusal{{Key: "tmp_max_retention"}}
			case "refusal":
				answer.Refusal = &rightswire.RefusalDetail{}
			case "count":
				answer.Entries = append(answer.Entries, answer.Entries[0])
			case "key":
				answer.Entries[0].Key = "other"
			case "nil":
				answer.Entries[0].Value = nil
			case "zero":
				answer.Entries[0].Value.DurationMs = 0
			case "negative":
				answer.Entries[0].Value.DurationMs = -1
			case "wrong-kind":
				answer.Entries[0].Value.Kind = wire.ValueKindText
			case "overflow":
				answer.Entries[0].Value.DurationMs = 1<<63 - 1
			}
			collector := &tmpPassFixture{}
			err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, &tmpLogFixture{}, func(context.Context, []string) (wire.ReadResult, error) { return answer, readErr })
			if err == nil || len(collector.calls) != 0 {
				t.Fatal(err, collector.calls)
			}
		})
	}
}

func TestRuntimeJobTmpMissingLogSinkNeverCallsCollector(t *testing.T) {
	collector := &tmpPassFixture{}
	err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, nil, func(context.Context, []string) (wire.ReadResult, error) {
		return tmpConfigFixture(), nil
	})
	if err == nil || len(collector.calls) != 0 {
		t.Fatal(err, collector.calls)
	}
}

func TestRuntimeJobTmpReadsEveryPassAndLogsEachRemovalOnce(t *testing.T) {
	collector := &tmpPassFixture{removals: []acceptanceprovider.TmpRemoval{{OperationID: "fixture", Name: "work/fixture"}}}
	sink := &tmpLogFixture{}
	reads := 0
	read := func(_ context.Context, keys []string) (wire.ReadResult, error) {
		if !reflect.DeepEqual(keys, []string{"tmp_max_retention"}) {
			t.Fatal(keys)
		}
		reads++
		answer := tmpConfigFixture()
		answer.Entries[0].Value.DurationMs = int64(reads) * int64(36*time.Hour/time.Millisecond)
		return answer, nil
	}
	if err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, sink, read); err != nil {
		t.Fatal(err)
	}
	collector.removals = nil
	if err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, sink, read); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(collector.calls, []time.Duration{36 * time.Hour, 72 * time.Hour}) || reads != 2 || len(sink.records) != 1 {
		t.Fatal(collector.calls, reads, sink.records)
	}
	r := sink.records[0]
	if r.Msg != "abstraction.storage/retention.removed" || r.Attrs["class"] != "tmp" || r.Attrs["key"] != "fixture" || r.Attrs["name"] != "work/fixture" || r.Attrs["level"] != "user" || r.Attrs["reason"] != "partial of failed or cancelled work" {
		t.Fatal(r)
	}
}

// A tmp_max_retention below a day still keeps an ended partial for a day.
func TestRuntimeJobTmpAgeNeverBelowOneDay(t *testing.T) {
	for _, ms := range []int64{1, 1000, int64(time.Hour / time.Millisecond), int64((24*time.Hour - time.Millisecond) / time.Millisecond)} {
		collector := &tmpPassFixture{}
		read := func(context.Context, []string) (wire.ReadResult, error) {
			answer := tmpConfigFixture()
			answer.Entries[0].Value.DurationMs = ms
			return answer, nil
		}
		if err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, &tmpLogFixture{}, read); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(collector.calls, []time.Duration{24 * time.Hour}) {
			t.Fatalf("%d ms: %v", ms, collector.calls)
		}
	}
}

// The schedule runs a first pass after its delay, then one per interval,
// reports a failed pass and keeps going, and stops with its context.
func TestRuntimeJobTmpScheduleRunsPassesAndStops(t *testing.T) {
	collector := &tmpPassFixture{}
	var mu sync.Mutex
	var reported []error
	reads := 0
	read := func(context.Context, []string) (wire.ReadResult, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if reads == 2 {
			return wire.ReadResult{}, errors.New("fixture policy unavailable")
		}
		return tmpConfigFixture(), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	wait := scheduleRuntimeJobTmp(ctx, time.Millisecond, 5*time.Millisecond, time.Now, collector, &tmpLogFixture{}, read, func(err error) {
		mu.Lock()
		defer mu.Unlock()
		reported = append(reported, err)
	})
	deadline := time.Now().Add(10 * time.Second)
	for collector.passes() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	wait()
	after := collector.passes()
	if after < 3 {
		t.Fatalf("schedule ran %d passes", after)
	}
	mu.Lock()
	if len(reported) != 1 {
		t.Fatalf("reported %v", reported)
	}
	mu.Unlock()
	time.Sleep(20 * time.Millisecond)
	if collector.passes() != after {
		t.Fatal("schedule ran after its context ended")
	}
}

// Before its first delay the schedule removes nothing, and a context that
// ends first runs no pass.
func TestRuntimeJobTmpScheduleWaitsForFirstPass(t *testing.T) {
	collector := &tmpPassFixture{}
	ctx, cancel := context.WithCancel(context.Background())
	wait := scheduleRuntimeJobTmp(ctx, time.Hour, time.Hour, time.Now, collector, &tmpLogFixture{}, func(context.Context, []string) (wire.ReadResult, error) {
		return tmpConfigFixture(), nil
	}, nil)
	cancel()
	wait()
	if collector.passes() != 0 {
		t.Fatal("pass ran before the first delay")
	}
}

type tmpNamerBase struct{ acceptanceprovider.Executor }

func (tmpNamerBase) OperationTmpNames(r *job.Record) (string, string, error) {
	return "results/" + r.ID, "work/" + r.ID, nil
}

// The runtime's inference wrapper passes the download kind's names through
// and names nothing for an inference job or a base without names.
func TestRuntimeInferenceExecutorNamesOnlyBaseTmpFiles(t *testing.T) {
	named := &runtimeInferenceJobExecutor{base: tmpNamerBase{}}
	if result, partial, err := named.OperationTmpNames(&job.Record{ID: "op", Kind: "download"}); err != nil || result != "results/op" || partial != "work/op" {
		t.Fatal(result, partial, err)
	}
	if _, _, err := named.OperationTmpNames(&job.Record{ID: "op", Kind: inference.InferenceJobKind}); !errors.Is(err, acceptanceprovider.ErrResultNotNamed) {
		t.Fatal(err)
	}
	unnamed := &runtimeInferenceJobExecutor{base: nil}
	if _, _, err := unnamed.OperationTmpNames(&job.Record{ID: "op", Kind: "download"}); !errors.Is(err, acceptanceprovider.ErrResultNotNamed) {
		t.Fatal(err)
	}
}

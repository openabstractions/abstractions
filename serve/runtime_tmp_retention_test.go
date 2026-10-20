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
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestRuntimeJobTmpCollectionRemainsUnscheduledIn08(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for range runtimeJobTmpCollectionSites(file, path) {
			t.Errorf("0.8 must not schedule temporary collection: %s", path)
		}
	}
}

func runtimeJobTmpCollectionSites(file *ast.File, path string) []token.Pos {
	// These are the two inert owner's existing internal handoffs. A caller
	// elsewhere, including a closure or initializer, would activate collection.
	allowed := map[*ast.CallExpr]bool{}
	if filepath.Base(path) == "runtime_tmp_retention.go" {
		for _, declaration := range file.Decls {
			owner, ok := declaration.(*ast.FuncDecl)
			if !ok || owner.Recv != nil || owner.Body == nil {
				continue
			}
			ast.Inspect(owner.Body, func(node ast.Node) bool {
				if _, closure := node.(*ast.FuncLit); closure {
					return false
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if callee, ok := call.Fun.(*ast.Ident); ok && owner.Name.Name == "collectRuntimeJobTmp" && callee.Name == "collectRuntimeJobTmpWith" {
					allowed[call] = true
				}
				if callee, ok := call.Fun.(*ast.SelectorExpr); ok && owner.Name.Name == "collectRuntimeJobTmpWith" && callee.Sel.Name == "CollectJobTmp" {
					if receiver, ok := callee.X.(*ast.Ident); ok && receiver.Name == "collector" {
						allowed[call] = true
					}
				}
				return true
			})
		}
	}
	var sites []token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		if call, ok := node.(*ast.CallExpr); ok {
			callee := call.Fun
			for {
				parenthesized, ok := callee.(*ast.ParenExpr)
				if !ok {
					break
				}
				callee = parenthesized.X
			}
			var name string
			switch target := callee.(type) {
			case *ast.Ident:
				name = target.Name
			case *ast.SelectorExpr:
				name = target.Sel.Name
			}
			if !allowed[call] && (name == "collectRuntimeJobTmp" || name == "collectRuntimeJobTmpWith" || name == "CollectJobTmp" || name == "CollectTmp") {
				sites = append(sites, call.Pos())
			}
		}
		return true
	})
	return sites
}

func TestRuntimeJobTmpGuardRejectsAllEntryCalls(t *testing.T) {
	for _, name := range []string{"collectRuntimeJobTmp", "collectRuntimeJobTmpWith", "CollectJobTmp", "CollectTmp"} {
		for _, callee := range []string{name, "owner." + name, "(owner." + name + ")"} {
			t.Run(callee, func(t *testing.T) {
				file, err := parser.ParseFile(token.NewFileSet(), "startup.go", "package main\nfunc start() { "+callee+"() }", 0)
				if err != nil {
					t.Fatal(err)
				}
				if got := len(runtimeJobTmpCollectionSites(file, "startup.go")); got != 1 {
					t.Fatalf("got %d calls, want 1", got)
				}
			})
		}
	}
	for _, fixture := range []struct {
		path, source string
		want         int
	}{
		{"runtime_tmp_retention.go", "func collectRuntimeJobTmp() { collectRuntimeJobTmpWith() }; func collectRuntimeJobTmpWith() { collector.CollectJobTmp() }", 0},
		{"startup.go", "func collectRuntimeJobTmp() { collectRuntimeJobTmpWith() }; func collectRuntimeJobTmpWith() { collector.CollectJobTmp() }", 2},
		{"runtime_tmp_retention.go", "func start() { collectRuntimeJobTmpWith(); collector.CollectJobTmp() }", 2},
		{"runtime_tmp_retention.go", "func collectRuntimeJobTmpWith() { go func() { collector.CollectJobTmp() }() }", 1},
		{"startup.go", "var CollectTmp = 1; func collectRuntimeJobTmp() {}; func other() {}", 0},
	} {
		file, err := parser.ParseFile(token.NewFileSet(), fixture.path, "package main\n"+fixture.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(runtimeJobTmpCollectionSites(file, fixture.path)); got != fixture.want {
			t.Errorf("%s: got %d, want %d", fixture.source, got, fixture.want)
		}
	}
}

type tmpPassFixture struct {
	calls    []time.Duration
	removals []acceptanceprovider.TmpRemoval
}

func (f *tmpPassFixture) CollectJobTmp(_ context.Context, _ time.Time, retention time.Duration) ([]acceptanceprovider.TmpRemoval, error) {
	f.calls = append(f.calls, retention)
	return f.removals, nil
}

type tmpLogFixture struct{ records []logging.Record }

func (f *tmpLogFixture) Write(r logging.Record) error { f.records = append(f.records, r); return nil }

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
	collector := &tmpPassFixture{removals: []acceptanceprovider.TmpRemoval{{OperationID: "fixture", Name: "results/fixture"}}}
	sink := &tmpLogFixture{}
	reads := 0
	read := func(_ context.Context, keys []string) (wire.ReadResult, error) {
		if !reflect.DeepEqual(keys, []string{"tmp_max_retention"}) {
			t.Fatal(keys)
		}
		reads++
		answer := tmpConfigFixture()
		answer.Entries[0].Value.DurationMs = int64(reads) * 1000
		return answer, nil
	}
	if err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, sink, read); err != nil {
		t.Fatal(err)
	}
	collector.removals = nil
	if err := collectRuntimeJobTmpWith(context.Background(), time.Now(), collector, sink, read); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(collector.calls, []time.Duration{time.Second, 2 * time.Second}) || reads != 2 || len(sink.records) != 1 {
		t.Fatal(collector.calls, reads, sink.records)
	}
	r := sink.records[0]
	if r.Msg != "abstraction.storage/retention.removed" || r.Attrs["class"] != "tmp" || r.Attrs["key"] != "fixture" || r.Attrs["level"] != "user" {
		t.Fatal(r)
	}
}

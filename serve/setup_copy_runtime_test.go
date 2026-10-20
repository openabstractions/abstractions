package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	logging "github.com/openabstractions/abstraction-logging/go"
	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

type setupCopyActivityFixture struct{ records []logging.Record }

func (s *setupCopyActivityFixture) Write(record logging.Record) error {
	s.records = append(s.records, record)
	return nil
}

func TestSetupCopyRepairActivityRequiresActualRepairReadback(t *testing.T) {
	current := false
	row := setupCopyRow{Kind: setupCopyGatewaySource, Owner: cacheproducer.ClaudeProducer, Location: "fixture-source", Observe: func(context.Context) (setupCopyReading, error) {
		if current {
			return setupCopyReading{State: setupCopyCurrent}, nil
		}
		return setupCopyReading{State: setupCopyStale}, nil
	}, Repair: func(context.Context) error { current = true; return nil }}
	var inventory setupCopyInventory
	var sink setupCopyActivityFixture
	for pass := 0; pass < 2; pass++ {
		results, err := inventory.check(context.Background(), []setupCopyRow{row})
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			if err := recordSetupCopyRepair(&sink, result, time.Now()); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(sink.records) != 1 {
		t.Fatal("repair or clean replay emitted wrong Activity count", len(sink.records))
	}
	if err := recordSetupCopyRepair(&sink, setupCopyResult{Repaired: true, After: setupCopyReading{State: setupCopyUnknown}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 1 {
		t.Fatal("unknown readback emitted repair Activity")
	}
}

func TestSetupCopyRepairRequiresCurrentNativeKeepAndBytesAtEachBoundary(t *testing.T) {
	run := agent1478Fixture(t)
	const name = "repair-agent"
	agent1478Register(t, run, name, run.f.answerer.Program)
	pending := agent1478Set(t, run, name, "", true)
	question := agent1478Question(t, run, name)
	answer(t, run.f, question.ID, "keep")
	agent1478Wait(t, run, name, "enrolled")
	held, err := fileid.Open(run.ctx, fileid.Self, run.f.answerer.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := held.FileID().Stored()
	if err != nil {
		t.Fatal(err)
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := privatedir.Tighten(root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "owned-marker.json")
	marker := cacheproducer.ClaudeMarker{Self: stored.String(), MarketplaceOwned: true, PluginOwned: true, Files: map[string]string{"1": "a", "2": "b", "3": "c", "4": "d", "5": "e"}}
	raw, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	copy := cacheproducer.Copy{Owner: cacheproducer.ClaudeProducer, Kind: cacheproducer.SourceCopy, Location: root, Receipt: path}
	helperBytes, err := os.ReadFile(run.f.answerer.Program)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, "published-source.exe")
	if err := os.WriteFile(sourcePath, helperBytes, 0600); err != nil {
		t.Fatal(err)
	}
	sourcePin, err := updateImage(run.ctx, sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	run.d.agentPublishedSource = func(ctx context.Context) (agentUpdateImage, error) { return updateImage(ctx, sourcePath) }
	authorize, err := run.d.agentCopyAuthority(run.ctx, copy)
	if err != nil || authorize == nil {
		t.Fatal(err)
	}
	if err := authorize(run.ctx); err != nil {
		t.Fatal(err)
	}
	old, err := updateImage(run.ctx, run.f.answerer.Program)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run.f.answerer.Program, append(append([]byte(nil), helperBytes...), []byte(" changed in place")...), 0600); err != nil {
		t.Fatal(err)
	}
	drift, err := updateImage(run.ctx, run.f.answerer.Program)
	if err != nil || drift.Key != old.Key || drift.Hash == old.Hash || !updateImageMatches(run.ctx, sourcePin, sourcePath) {
		t.Fatal("fixture must change helper bytes under the same FileKey and preserve published source", old, drift, err)
	}
	if err := authorize(run.ctx); err == nil {
		t.Fatal("producer write guard admitted same-FileKey helper byte drift")
	}
	if _, err := run.d.agentCopyAuthority(run.ctx, copy); err == nil {
		t.Fatal("changed helper bytes admitted another repair")
	}
	if err := os.WriteFile(run.f.answerer.Program, helperBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := authorize(run.ctx); err != nil {
		t.Fatal("exact captured bytes did not recover guard", err)
	}
	got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: name, ExpectedRevision: pending.Revision, Enabled: false})
	if got.Outcome != applications.OutcomeApplied {
		t.Fatal(got)
	}
	if err := authorize(run.ctx); err == nil {
		t.Fatal("captured copy repair survived native revocation")
	}
	if _, err := run.d.agentCopyAuthority(run.ctx, copy); err == nil {
		t.Fatal("revoked native Keep admitted another repair")
	}
}

func TestSetupCopyRuntimeRejectsUnlistedProducerPlacement(t *testing.T) {
	// This uses the actual gateway producer's placement DTO. A producer adding
	// another emitted location kind must first receive a roster disposition.
	for _, unlisted := range []cacheproducer.Copy{
		{Kind: "new-unlisted-cache", Owner: cacheproducer.ClaudeProducer, Location: "new-location", Receipt: "receipt"},
		{Kind: cacheproducer.SourceCopy, Owner: "new-unlisted-producer", Location: "new-location", Receipt: "receipt"},
	} {
		copies := []cacheproducer.Copy{
			{Kind: cacheproducer.SourceCopy, Owner: cacheproducer.ClaudeProducer, Location: "owned-source", Receipt: "receipt"},
			unlisted,
		}
		if rows, err := gatewayCopyRows(copies); err == nil || len(rows) != 0 {
			t.Fatalf("unlisted producer placement admitted: %+v, %v", rows, err)
		}
	}
}

func TestSetupCopyRuntimeUsesProducerClassification(t *testing.T) {
	for _, test := range []struct {
		from cacheproducer.CopyState
		to   setupCopyState
	}{
		{cacheproducer.CopyCurrent, setupCopyCurrent},
		{cacheproducer.CopyStale, setupCopyStale},
		{cacheproducer.CopyAbsent, setupCopyAbsent},
		{cacheproducer.CopyForeign, setupCopyForeign},
		{cacheproducer.CopyUnreadable, setupCopyUnreadable},
		{cacheproducer.CopyHumanNeeded, setupCopyHumanNeeded},
		{"new-meaning", setupCopyUnknown},
	} {
		if got := gatewayCopyReading(cacheproducer.CopyReading{State: test.from, Reason: "producer reason"}); got.State != test.to || got.Reason != "producer reason" {
			t.Fatalf("producer state %q: %+v", test.from, got)
		}
	}
}

func TestSetupCopyRuntimeReadyProviderDoesNotAssertFileProof(t *testing.T) {
	supervisor := &providerSupervisor{file: providerFile{Declaration: providerDeclaration{Name: modelHostName, Endpoint: "owned-provider"}}}
	providers := &runtimeProviders{supervisors: map[string]*providerSupervisor{modelHostName: supervisor}}
	rows := providerCopyRows(providers)
	if len(rows) != 1 || rows[0].Kind != setupCopyEngine || rows[0].Repair != nil {
		t.Fatalf("provider row: %+v", rows)
	}
	for _, state := range []wire.DeclarationReadiness{wire.DeclarationReadinessReady, wire.DeclarationReadinessIdle, wire.DeclarationReadinessUnreachable} {
		supervisor.readiness = state
		got, err := rows[0].Observe(context.Background())
		if err != nil || got.State != setupCopyUnknown {
			t.Fatalf("provider %s asserted copy proof: %+v, %v", state, got, err)
		}
	}
	supervisor.readiness, supervisor.why = wire.DeclarationReadinessRefused, "program:untrusted"
	if got, err := rows[0].Observe(context.Background()); err != nil || got.State != setupCopyRefused {
		t.Fatalf("refused provider: %+v, %v", got, err)
	}
}

func TestSetupCopyRuntimeUsesOneSerializedStartupAndRefreshPass(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	providers := &runtimeProviders{changes: make(chan struct{})}
	started := make(chan int32, 3)
	releaseFirst := make(chan struct{})
	var count, active atomic.Int32
	stop := startSetupCopyPasses(ctx, providers, func(ctx context.Context) error {
		if active.Add(1) != 1 {
			t.Error("copy passes overlapped")
		}
		defer active.Add(-1)
		n := count.Add(1)
		started <- n
		if n == 1 {
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}, func(err error) { t.Errorf("copy pass: %v", err) })
	defer stop()
	await := func(want int32) {
		t.Helper()
		select {
		case got := <-started:
			if got != want {
				t.Fatalf("pass %d, want %d", got, want)
			}
		case <-ctx.Done():
			t.Fatal("copy pass did not run")
		}
	}
	await(1)
	for range 10 {
		providers.notify()
	}
	close(releaseFirst)
	await(2)
	stop()
	if count.Load() != 2 {
		t.Fatalf("coalesced passes = %d", count.Load())
	}
}

func TestSetupCopyRuntimeStopCancelsAndJoinsProducer(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	stop := startSetupCopyPasses(context.Background(), nil, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}, func(err error) { t.Errorf("shutdown reported as copy failure: %v", err) })
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("producer did not start")
	}
	stop()
	select {
	case <-finished:
	default:
		t.Fatal("copy worker outlived its runtime")
	}
}

func TestSetupCopyRuntimeIsolatedRunsDoNotDiscoverInstalledCopies(t *testing.T) {
	for _, options := range []runtimeFlags{{isolated: "fixture"}, {stateDir: "fixture"}} {
		stop := startRuntimeSetupCopies(context.Background(), options, &applicationsHost{}, nil,
			func(err error) { t.Errorf("isolated inventory reached installed state: %v", err) })
		stop()
	}
}

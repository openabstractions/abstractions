package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	job "github.com/openabstractions/abstraction-job/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// gatedDownloads is a stand-in download executor: each job it claims writes
// progress until finish closes, then completes. It counts the jobs it ran.
type gatedDownloads struct {
	finish chan struct{}
	mu     sync.Mutex
	ran    map[string]bool
}

func (*gatedDownloads) Profile() string { return "gated-downloads-v1" }
func (*gatedDownloads) Prepare(_, _ string, spec []byte) ([]byte, error) {
	return json.Marshal(map[string]string{"spec": string(spec)})
}

func (d *gatedDownloads) Serve(ctx context.Context, store job.Store) error {
	var running sync.WaitGroup
	defer running.Wait()
	for ctx.Err() == nil {
		records, _ := store.List()
		for _, r := range records {
			d.mu.Lock()
			seen := d.ran[r.ID]
			d.mu.Unlock()
			if seen || r.State.Terminal() || !store.Claimable(r) {
				continue
			}
			held, err := store.Claim(r.ID, "downloads", time.Minute)
			if err != nil {
				continue
			}
			d.mu.Lock()
			d.ran[r.ID] = true
			d.mu.Unlock()
			running.Add(1)
			go func(id string, epoch int64) {
				defer running.Done()
				for ctx.Err() == nil {
					done := false
					select {
					case <-d.finish:
						done = true
					default:
					}
					_, err := store.Update(id, epoch, func(r *job.Record) error {
						r.State = job.StateRunning
						if done {
							r.State = job.StateComplete
						}
						return nil
					})
					if done || errors.Is(err, job.ErrStaleEpoch) || errors.Is(err, job.ErrTerminal) {
						return
					}
					time.Sleep(2 * time.Millisecond)
				}
			}(held.ID, held.Lease.Epoch)
		}
		time.Sleep(3 * time.Millisecond)
	}
	return nil
}

func (d *gatedDownloads) runs() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.ran)
}

// abstraction-job JOB-A19 with the runtime's rights: a download Blocked mid-
// transfer fails with rights:denied; the same request submitted again while
// Blocked is refused; once unblocked it runs once more and completes; and a
// further submission of the completed request replays with no transfer.
func TestABlockedDownloadRunsAgainOnceUnblockedAndThenReplays(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{host.JobSubmitAction}, nil); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(t.TempDir(), "fetch.exe")
	kind := "posix"
	if runtime.GOOS == "windows" {
		kind = "windows"
	}
	scope, err := acceptanceprovider.LocalSubjectScope(kind, r.owner, executable)
	if err != nil {
		t.Fatal(err)
	}
	binding := acceptanceprovider.Binding{Scope: scope, Origin: "local",
		Subject: &acceptanceprovider.AuthenticatedSubject{AccountKind: kind, Account: r.owner, Executable: executable, Program: executable}}
	app := rwire.Subject{Account: r.owner, Program: executable}

	downloads := &gatedDownloads{finish: make(chan struct{}), ran: map[string]bool{}}
	p, err := acceptanceprovider.OpenWithExecutor(t.TempDir(), "owner", downloads)
	if err != nil {
		t.Fatal(err)
	}
	options := host.Options{JobRoot: "jobs"}
	gateJobStanding(&options, r)
	p.SetStanding(options.JobStanding)
	ctx, cancel := context.WithCancel(context.Background())
	unhook := options.JobStandingChanges(func() { p.RecheckSoon(ctx) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		//unchecked: the executor stops when the test ends
		_ = p.Execute(ctx)
	}()
	t.Cleanup(func() { unhook(); cancel(); <-done; p.StopRechecks() })

	history, err := p.Bind(scope).GetHistoryWindow()
	if err != nil {
		t.Fatal(err)
	}
	s := api.Submission{Identity: api.RequestIdentity{Key: "model.gguf", HistoryEpoch: history.HistoryEpoch},
		Kind: "download", Spec: []byte(`{"source":"model.gguf"}`), RequiredGuarantees: []string{acceptanceprovider.GuaranteeServiceRestart}}
	submit := func() api.AcceptanceResult {
		t.Helper()
		v, err := p.BindBinding(binding).Submit(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	observe := func() *api.OperationSnapshot {
		t.Helper()
		v, err := p.BindOperationsBinding(binding).ObserveWork(s.Identity)
		if err != nil || v.Snapshot == nil {
			t.Fatalf("observe %+v %v", v, err)
		}
		return v.Snapshot
	}

	first := submit()
	if first.Receipt == nil {
		t.Fatalf("submit %+v", first)
	}
	waitUntil(t, "the first run", func() bool { return observe().State == api.WorkStateRunning })
	block := func(permit bool) {
		t.Helper()
		if err := r.policy.Set(app, host.JobSubmitAction, host.JobKindResource("download"), permit); err != nil {
			t.Fatal(err)
		}
	}
	block(false)
	waitUntil(t, "the Blocked run to fail", func() bool { return observe().State == api.WorkStateFailed })
	if f := observe().Failure; f == nil || f.Message != "rights:denied" {
		t.Fatalf("Blocked run failure %+v", f)
	}
	if v := submit(); v.Outcome != api.AcceptanceOutcomeForbidden || v.Receipt != nil {
		t.Fatalf("resubmission while Blocked %+v", v)
	}
	if n := downloads.runs(); n != 1 {
		t.Fatalf("a refused resubmission ran: %d runs", n)
	}

	if err := r.policy.Revoke(app, host.JobSubmitAction, host.JobKindResource("download")); err != nil {
		t.Fatal(err)
	}
	second := submit()
	if second.Outcome != api.AcceptanceOutcomeAccepted || second.Receipt == nil || second.Receipt.OperationID == first.Receipt.OperationID {
		t.Fatalf("resubmission once unblocked %+v", second)
	}
	waitUntil(t, "the rerun", func() bool { return downloads.runs() == 2 })
	close(downloads.finish)
	waitUntil(t, "the rerun to complete", func() bool { return observe().State == api.WorkStateComplete })

	replay := submit()
	if replay.Receipt == nil || replay.Receipt.OperationID != second.Receipt.OperationID {
		t.Fatalf("a completed request was not replayed %+v", replay)
	}
	time.Sleep(30 * time.Millisecond)
	if n := downloads.runs(); n != 2 {
		t.Fatalf("a replayed request transferred again: %d runs", n)
	}
}

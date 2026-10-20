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
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// steadyDownloads is a stand-in download executor: it claims each queued job
// and writes progress every few milliseconds, through the store it was given,
// until a store call fences it.
type steadyDownloads struct {
	mu     sync.Mutex
	steps  map[string]int64
	fenced map[string]bool
}

func (*steadyDownloads) Profile() string { return "steady-downloads-v1" }
func (*steadyDownloads) Prepare(_, _ string, spec []byte) ([]byte, error) {
	return json.Marshal(map[string]string{"spec": string(spec)})
}

func (d *steadyDownloads) Serve(ctx context.Context, store job.Store) error {
	var running sync.WaitGroup
	defer running.Wait()
	claimed := map[string]bool{}
	for ctx.Err() == nil {
		records, _ := store.List()
		for _, r := range records {
			if claimed[r.ID] || r.State.Terminal() || !store.Claimable(r) {
				continue
			}
			held, err := store.Claim(r.ID, "downloads", time.Minute)
			if err != nil {
				continue
			}
			claimed[r.ID] = true
			running.Add(1)
			go func(id string, epoch int64) {
				defer running.Done()
				for step := int64(1); ctx.Err() == nil; step++ {
					_, err := store.Update(id, epoch, func(r *job.Record) error {
						r.State, r.Progress.Done, r.Progress.Total = job.StateRunning, step, 1<<20
						return nil
					})
					if errors.Is(err, job.ErrStaleEpoch) || errors.Is(err, job.ErrTerminal) {
						d.mu.Lock()
						d.fenced[id] = true
						d.mu.Unlock()
						return
					}
					d.mu.Lock()
					d.steps[id] = step
					d.mu.Unlock()
					time.Sleep(2 * time.Millisecond)
				}
			}(held.ID, held.Lease.Epoch)
		}
		time.Sleep(3 * time.Millisecond)
	}
	return nil
}

func (d *steadyDownloads) progress(id string) int64 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.steps[id]
}

// JOB-A18 with the runtime's rights: a download the account-wide download
// default admitted keeps running through an unrelated rights change, and ends
// once the person Blocks that program or turns the default off, each before
// the edit returns, with the refusal as its reason.
func TestADownloadEndsWhenItsDefaultOrItsProgramIsRefused(t *testing.T) {
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

	downloads := &steadyDownloads{steps: map[string]int64{}, fenced: map[string]bool{}}
	p, err := acceptanceprovider.OpenWithExecutor(t.TempDir(), "owner", downloads)
	if err != nil {
		t.Fatal(err)
	}
	options := host.Options{JobRoot: "jobs"}
	gateJobStanding(&options, r)
	if options.JobStanding == nil || options.JobStandingChanges == nil {
		t.Fatal("the runtime configured no job standing")
	}
	p.SetStanding(options.JobStanding)
	ctx, cancel := context.WithCancel(context.Background())
	unhook := options.JobStandingChanges(func() { p.RecheckSoon(ctx) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		//unchecked: the executor stops when the test ends
		_ = p.Execute(ctx)
	}()
	t.Cleanup(func() { unhook(); cancel(); <-done })

	submit := func(key string) (string, api.RequestIdentity) {
		t.Helper()
		history, err := p.Bind(scope).GetHistoryWindow()
		if err != nil {
			t.Fatal(err)
		}
		identity := api.RequestIdentity{Key: key, HistoryEpoch: history.HistoryEpoch}
		accepted, err := p.BindBinding(binding).Submit(api.Submission{Identity: identity,
			Kind: "download", Spec: []byte(`{"source":"` + key + `"}`), RequiredGuarantees: []string{acceptanceprovider.GuaranteeServiceRestart}})
		if err != nil || accepted.Receipt == nil {
			t.Fatalf("submit %s: %+v %v", key, accepted, err)
		}
		id := accepted.Receipt.OperationID
		waitUntil(t, key+" running", func() bool { return downloads.progress(id) > 2 })
		return id, identity
	}
	// The edit has returned: the running job has been asked to stop, and the
	// executor's next progress write fails it with the reason and fences it.
	ended := func(id string, identity api.RequestIdentity, reason string) {
		t.Helper()
		var snapshot *api.OperationSnapshot
		waitUntil(t, id+" failed", func() bool {
			observed, err := p.BindOperationsBinding(binding).ObserveWork(identity)
			snapshot = observed.Snapshot
			return err == nil && snapshot != nil && snapshot.State == api.WorkStateFailed
		})
		if snapshot.Failure == nil || snapshot.Failure.Message != reason {
			t.Fatalf("job %s: %+v, want failed with %s", id, snapshot, reason)
		}
		waitUntil(t, id+" fenced", func() bool {
			downloads.mu.Lock()
			defer downloads.mu.Unlock()
			return downloads.fenced[id]
		})
	}

	// The default admits it, and an unrelated change leaves it running.
	first, firstIdentity := submit("default-admitted")
	if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "other.exe")}, host.JobSubmitAction, host.JobKindResource("download"), false); err != nil {
		t.Fatal(err)
	}
	before := downloads.progress(first)
	waitUntil(t, "progress after an unrelated change", func() bool { return downloads.progress(first) > before+2 })
	if observed, err := p.BindOperationsBinding(binding).ObserveWork(firstIdentity); err != nil || observed.Snapshot == nil ||
		observed.Snapshot.State != api.WorkStateRunning || observed.Snapshot.CancellationRequested {
		t.Fatalf("an unrelated change touched the job: %+v %v", observed.Snapshot, err)
	}

	// Block: the person's Don't allow for this program.
	if err := r.policy.Set(app, host.JobSubmitAction, host.JobKindResource("download"), false); err != nil {
		t.Fatal(err)
	}
	ended(first, firstIdentity, "rights:denied")

	// Remove the Block; the default admits again. Turning it off ends it.
	if err := r.policy.Revoke(app, host.JobSubmitAction, host.JobKindResource("download")); err != nil {
		t.Fatal(err)
	}
	second, secondIdentity := submit("default-again")
	revision, _, err := r.policy.DefaultsSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if off, err := r.policy.SetDefault(revision, rights.DefaultDownload, false, r.self(), func() error { return nil }); err != nil || off.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(off, err)
	}
	ended(second, secondIdentity, "rights:not_granted")
}

func waitUntil(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(2 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

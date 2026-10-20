package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
)

type agentImageCacheRun struct {
	d                 *applicationDirectory
	program           string
	reads, exclusions atomic.Int64
	clock             atomic.Int64
	excluded          atomic.Bool
	fail              atomic.Bool
}

// agentImageCacheFixture counts each injected image read (the read and its
// signature verification) and each gateway lookup over a temporary file.
func agentImageCacheFixture(t *testing.T) *agentImageCacheRun {
	t.Helper()
	r := &agentImageCacheRun{program: filepath.Join(t.TempDir(), "agent.bin")}
	if err := os.WriteFile(r.program, []byte("first image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readApplicationAgentStamp(r.program); err != nil {
		t.Skip("no native image stamp on this platform:", err)
	}
	base := time.Now()
	r.d = &applicationDirectory{now: func() time.Time { return base.Add(time.Duration(r.clock.Load())) }}
	r.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		r.reads.Add(1)
		if r.fail.Load() {
			return applicationAgentImage{}, errors.New("image unavailable")
		}
		h, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return applicationAgentImage{}, err
		}
		defer h.Close()
		return applicationAgentImage{Program: program, Key: h.FileID().Key(), Binding: binding}, nil
	}
	r.d.agentExcluded = func(context.Context, string, string) (bool, error) {
		r.exclusions.Add(1)
		return r.excluded.Load(), nil
	}
	return r
}

func (r *agentImageCacheRun) admit(t *testing.T, binding string) applicationAgentImage {
	t.Helper()
	image, err := r.d.admittedAgentImage(context.Background(), r.program, binding)
	if err != nil {
		t.Fatal(err)
	}
	return image
}

func (r *agentImageCacheRun) expect(t *testing.T, reads, exclusions int64) {
	t.Helper()
	if got, gotExcluded := r.reads.Load(), r.exclusions.Load(); got != reads || gotExcluded != exclusions {
		t.Fatalf("image reads %d, gateway lookups %d; want %d and %d", got, gotExcluded, reads, exclusions)
	}
}

func TestApplicationAgentImageVerifiedOncePerKey(t *testing.T) {
	r := agentImageCacheFixture(t)
	first := r.admit(t, "binding")
	for range 100 {
		if got := r.admit(t, "binding"); !reflect.DeepEqual(got, first) {
			t.Fatal("unchanged image changed", got, first)
		}
	}
	r.expect(t, 1, 1)
	r.clock.Store(int64(applicationAgentImageRecheck - time.Second))
	r.admit(t, "binding")
	r.expect(t, 1, 1)
	r.clock.Store(int64(applicationAgentImageRecheck))
	r.admit(t, "binding")
	r.expect(t, 2, 2)
	r.admit(t, "other-binding")
	r.admit(t, "other-binding")
	r.expect(t, 3, 3)
	r.clock.Store(int64(-time.Second)) // A clock set back verifies again.
	r.admit(t, "binding")
	r.expect(t, 4, 4)
}

func TestApplicationAgentImageReplacementNoticedByNextCheck(t *testing.T) {
	for _, change := range []string{"replaced", "rewritten", "linked"} {
		t.Run(change, func(t *testing.T) {
			r := agentImageCacheFixture(t)
			first := r.admit(t, "binding")
			r.admit(t, "binding")
			r.expect(t, 1, 1)
			switch change {
			case "replaced":
				if err := os.Rename(r.program, r.program+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(r.program, []byte("other image"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "rewritten":
				// Same file, same size and a restored write time: only the
				// metadata change time records the rewrite.
				info, err := os.Stat(r.program)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(r.program, []byte("FIRST IMAGE"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err = os.Chtimes(r.program, info.ModTime(), info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "linked":
				if err := os.Link(r.program, r.program+".link"); err != nil {
					t.Skip("hard links unavailable:", err)
				}
			}
			got := r.admit(t, "binding")
			r.expect(t, 2, 2)
			if change == "replaced" && got.Key == first.Key {
				t.Fatal("replacement kept the verified file key")
			}
			r.admit(t, "binding")
			r.expect(t, 2, 2)
		})
	}
}

// A contended caller waits in the mutex queue; a caller that leaves takes no
// lock, and its abandoned acquisition releases the mutex again.
func TestApplicationDirectoryLockWaitsAndLeaves(t *testing.T) {
	d := &applicationDirectory{}
	d.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	left := make(chan bool)
	go func() { left <- d.lock(ctx) }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if <-left {
		t.Fatal("cancelled caller took the lock")
	}
	got := make(chan bool)
	go func() { got <- d.lock(context.Background()) }()
	select {
	case <-got:
		t.Fatal("lock taken while held")
	case <-time.After(20 * time.Millisecond):
	}
	d.mu.Unlock()
	if !<-got {
		t.Fatal("waiting caller lost the released lock")
	}
	d.mu.Unlock()
	until := time.Now().Add(2 * time.Second)
	for !d.mu.TryLock() {
		if time.Now().After(until) {
			t.Fatal("abandoned acquisition kept the lock")
		}
		time.Sleep(time.Millisecond)
	}
	d.mu.Unlock()
}

func TestApplicationAgentImageFailuresAreNotRetained(t *testing.T) {
	r := agentImageCacheFixture(t)
	r.fail.Store(true)
	for range 2 {
		if _, err := r.d.admittedAgentImage(context.Background(), r.program, "binding"); err == nil {
			t.Fatal("failed read admitted")
		}
	}
	r.expect(t, 2, 0)
	r.fail.Store(false)
	r.admit(t, "binding")
	r.admit(t, "binding")
	r.expect(t, 3, 1)
	r.excluded.Store(true)
	r.d.agentImages.forget()
	for range 3 {
		if _, err := r.d.admittedAgentImage(context.Background(), r.program, "binding"); !errors.Is(err, errApplicationAgentSharedGateway) {
			t.Fatal("shared gateway admitted", err)
		}
	}
	r.expect(t, 4, 2)
}

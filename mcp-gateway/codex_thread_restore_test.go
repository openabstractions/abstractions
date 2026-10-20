package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

const (
	restoreThreadA = "01a10a6d-d9f4-7c61-9e33-4fe29defa7c6"
	restoreThreadB = "01a1bfd2-7478-7d50-b7c0-6dfbbaa41be0"
)

// restoreClock releases one restore check per tick, so a test decides when
// the helper looks at the loaded threads.
type restoreClock chan time.Time

func (c restoreClock) after(time.Duration) <-chan time.Time { return c }

func withParentThreads(t *testing.T, answers ...[]string) chan struct{} {
	t.Helper()
	asked := make(chan struct{}, 16)
	prior := codexParentThreads
	n := 0
	codexParentThreads = func(string) ([]string, error) {
		answer := answers[min(n, len(answers)-1)]
		n++
		asked <- struct{}{}
		return answer, nil
	}
	t.Cleanup(func() { codexParentThreads = prior })
	return asked
}

// otherHelperLock holds a thread's lock as another helper process does: the
// file lock alone, outside this process's own record.
func otherHelperLock(t *testing.T, ctx context.Context, state, thread string) func() {
	t.Helper()
	release, err := cas.LockContext(ctx, filepath.Join(state, "codex-receiving", thread), false)
	if err != nil {
		t.Fatal(err)
	}
	return release
}

func otherHelperCanLock(ctx context.Context, state, thread string) bool {
	try, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	release, err := cas.LockContext(try, filepath.Join(state, "codex-receiving", thread), false)
	if err != nil {
		return false
	}
	release()
	return true
}

func newRestoreConnection(ctx context.Context) (*codexReceivingConnection, chan string) {
	started := make(chan string, 4)
	r := newCodexReceivingConnection(ctx, func(ctx context.Context, thread string) error {
		started <- thread
		<-ctx.Done()
		return ctx.Err()
	}, func(string) {})
	return r, started
}

func waitAsked(t *testing.T, asked chan struct{}) {
	t.Helper()
	select {
	case <-asked:
	case <-time.After(5 * time.Second):
		t.Fatal("restore never checked the loaded threads")
	}
}

// The owner's restart: Codex Desktop resumes one thread and starts the helper
// for it; no turn runs. The helper binds that thread without any tool call.
func TestRestoreCodexThreadBindsTheParentsOnlyLoadedThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state := t.TempDir()
	asked := withParentThreads(t, []string{restoreThreadA})
	r, started := newRestoreConnection(ctx)
	defer r.stop("test_end")
	clock := make(restoreClock)
	done := make(chan struct{})
	restoring, stopRestoring := context.WithCancel(ctx)
	go func() { restoreCodexThread(restoring, r, t.TempDir(), state, clock.after); close(done) }()
	clock <- time.Now()
	waitAsked(t, asked)
	select {
	case got := <-started:
		if got != restoreThreadA {
			t.Fatalf("restored %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loaded thread was not bound without a turn")
	}
	// The restored binding holds the thread: a second helper of the same
	// app-server must not take it over.
	if otherHelperCanLock(ctx, state, restoreThreadA) {
		t.Fatal("a second helper could restore the same thread")
	}
	// The thread's own later tool call carries the same thread and keeps it.
	r.observe(map[string]any{"threadId": restoreThreadA})
	r.mu.Lock()
	ended := r.closed
	r.mu.Unlock()
	if ended {
		t.Fatal("matching host metadata ended the restored binding")
	}
	stopRestoring()
	<-done
	release, ok := codexRestoreLock(ctx, state, restoreThreadA)
	if !ok {
		t.Fatal("restore kept its lock after the connection ended")
	}
	release()
}

// Several loaded threads, or none, name no thread for this helper; it waits
// for host metadata, which then binds as before and ends restoring.
func TestRestoreCodexThreadWaitsWhenTheLoadedThreadIsAmbiguous(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	asked := withParentThreads(t, []string{restoreThreadA, restoreThreadB}, nil)
	r, started := newRestoreConnection(ctx)
	defer r.stop("test_end")
	clock := make(restoreClock)
	done := make(chan struct{})
	go func() { restoreCodexThread(ctx, r, t.TempDir(), t.TempDir(), clock.after); close(done) }()
	for range 2 {
		clock <- time.Now()
		waitAsked(t, asked)
	}
	select {
	case got := <-started:
		t.Fatalf("ambiguous loaded threads bound %q", got)
	default:
	}
	r.observe(map[string]any{"threadId": restoreThreadB})
	if got := <-started; got != restoreThreadB {
		t.Fatalf("host metadata bound %q", got)
	}
	clock <- time.Now()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("restoring continued after host metadata bound the connection")
	}
}

// Host metadata always wins: a restore never replaces a binding, and a helper
// whose thread another helper already receives for does not restore it.
func TestRestoreCodexThreadNeverReplacesABinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, started := newRestoreConnection(ctx)
	defer r.stop("test_end")
	r.observe(map[string]any{"threadId": restoreThreadB})
	<-started
	if r.restore(restoreThreadA) || r.restore(restoreThreadB) {
		t.Fatal("restore replaced a host metadata binding")
	}
	r.mu.Lock()
	ended := r.closed
	r.mu.Unlock()
	if ended {
		t.Fatal("restore ended the host metadata binding")
	}

	state := t.TempDir()
	held := otherHelperLock(t, ctx, state, restoreThreadA)
	defer held()
	asked := withParentThreads(t, []string{restoreThreadA})
	other, otherStarted := newRestoreConnection(ctx)
	defer other.stop("test_end")
	clock := make(restoreClock)
	go restoreCodexThread(ctx, other, t.TempDir(), state, clock.after)
	clock <- time.Now()
	waitAsked(t, asked)
	clock <- time.Now()
	waitAsked(t, asked)
	select {
	case got := <-otherStarted:
		t.Fatalf("second helper restored %q while another held it", got)
	default:
	}
}

func TestCodexThreadTitleReadsTheNewestName(t *testing.T) {
	home := t.TempDir()
	index := `{"id":"` + restoreThreadA + `","thread_name":"Review asks and rights modules","updated_at":"2026-10-05T05:00:12Z"}
{"id":"` + restoreThreadB + `","thread_name":"Other chat","updated_at":"2026-10-06T05:00:12Z"}
not json ` + restoreThreadA + `
{"id":"` + restoreThreadA + `","thread_name":"Release 0.8.0","updated_at":"2026-10-24T08:11:58Z"}
`
	if err := os.WriteFile(filepath.Join(home, "session_index.jsonl"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := codexThreadTitle(home, restoreThreadA); got != "Release 0.8.0" {
		t.Fatalf("title %q", got)
	}
	if got := codexThreadTitle(home, "11111111-1111-4111-8111-111111111111"); got != "" {
		t.Fatalf("unnamed thread titled %q", got)
	}
	if got := codexThreadTitle(t.TempDir(), restoreThreadA); got != "" {
		t.Fatalf("missing index titled %q", got)
	}
}

// A rename in Codex reaches the bound chat's presence title.
func TestFollowCodexThreadTitleCarriesARename(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	home := t.TempDir()
	write := func(name string) {
		line := `{"id":"` + restoreThreadA + `","thread_name":"` + name + `"}` + "\n"
		f, err := os.OpenFile(filepath.Join(home, "session_index.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	write("First name")
	status := &messagesInboxStatus{routingChanged: make(chan struct{}, 1)}
	clock := make(restoreClock)
	go followCodexThreadTitle(ctx, status, home, restoreThreadA, clock.after)
	title := func() string {
		<-status.routingChanged
		status.routingMu.Lock()
		defer status.routingMu.Unlock()
		return status.chatTitle
	}
	if got := title(); got != "First name" {
		t.Fatalf("title %q", got)
	}
	write("Renamed chat")
	clock <- time.Now()
	if got := title(); got != "Renamed chat" {
		t.Fatalf("renamed title %q", got)
	}
}

func TestCodexThreadLockPathsKeepsThreadLocksOnly(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "thread-writer-locks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{".coordination.lock", restoreThreadA + ".lock", "notes.txt", "x.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := codexThreadLockPaths(home)
	if err != nil || len(paths) != 1 || codexLockThread(paths[0]) != restoreThreadA {
		t.Fatalf("paths %v %v", paths, err)
	}
	if _, err := codexThreadLockPaths(t.TempDir()); err == nil {
		t.Fatal("missing lock folder answered threads")
	}
}

// Only a restart's app-server, started shortly before this helper, names the
// helper's thread by its single lock. An older app-server may have started
// this helper for a new chat whose lock is not there yet; a parent born after
// the helper is a reused PID.
func TestCodexParentEligibleOnlyJustBeforeTheHelper(t *testing.T) {
	self := time.Date(2026, 11, 9, 10, 19, 59, 0, time.UTC)
	for _, c := range []struct {
		name   string
		parent time.Time
		want   bool
	}{
		{"restart gap", self.Add(-19 * time.Second), true},
		{"window edge", self.Add(-codexRestoreWindow), true},
		{"older app-server", self.Add(-codexRestoreWindow - time.Second), false},
		{"long-running app-server", self.Add(-3 * time.Hour), false},
		{"parent born after the helper", self.Add(time.Second), false},
		{"same instant", self, false},
		{"unknown parent time", time.Time{}, false},
	} {
		if got := codexParentEligible(c.parent, self); got != c.want {
			t.Errorf("%s: eligible %t", c.name, got)
		}
	}
}

// A helper bound by host metadata takes its thread's lock once the helper
// that held it lets go, and keeps it until its binding ends.
func TestHoldCodexThreadWaitsForTheLockAndKeepsIt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state := t.TempDir()
	other := otherHelperLock(t, ctx, state, restoreThreadA)
	clock := make(restoreClock)
	bound, unbind := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { holdCodexThread(bound, state, restoreThreadA, clock.after); close(done) }()
	// The bound helper waits on its retry clock while the other one holds.
	clock <- time.Now()
	other()
	// The helper may already have retried after the release; then it holds
	// the lock and no longer reads its retry clock.
	select {
	case clock <- time.Now():
	case <-time.After(time.Second):
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, held := codexHeldThreads.Load(restoreThreadA); held {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("bound helper never took its thread's lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if otherHelperCanLock(ctx, state, restoreThreadA) {
		t.Fatal("a restoring helper took the bound helper's thread")
	}
	unbind()
	<-done
	release, ok := codexRestoreLock(ctx, state, restoreThreadA)
	if !ok {
		t.Fatal("lock outlived the binding")
	}
	release()
}

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// Owner report, 2026-11-09: after a Codex restart, OA messaging came alive
// only once the person typed into the chat. Codex Desktop resumes the open
// thread and starts this helper for it, but it runs SessionStart hooks when
// the thread's first turn begins, and initialize and tools/list carry no
// threadId. The helper learned its thread only from a turn's tool call.
//
// The Desktop app-server holds the writer lock of every thread it has loaded
// (CODEX_HOME/thread-writer-locks/<thread>.lock). When the helper's own
// parent app-server started shortly before it, as on a restart, and holds
// exactly one such lock, that thread is the one it resumed this helper for.
// The helper binds it as host correlation; native
// enrollment and message rights keep all authority. Any later host metadata
// must name the same thread. Several loaded threads, none, or a holder other
// than the parent leave the helper waiting for metadata as before.

// codexParentThreads lists the threads whose writer locks this process's
// parent holds. Production reads the native lock holders; tests replace it.
var codexParentThreads = codexParentLoadedThreads

// The first check waits for the app-server to finish loading the thread it
// started this helper for; later checks back off while nothing is bound.
var codexRestoreDelays = []time.Duration{3 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second, time.Minute}

// A Codex restart starts the app-server and then the helpers of the threads
// it resumes, seconds apart (19 s on 2026-11-09). A helper started long after
// its app-server, by a new chat or an MCP reload, may belong to a thread whose
// lock does not exist yet; it waits for host metadata. A parent born after
// this helper is a reused PID.
const codexRestoreWindow = 2 * time.Minute

func codexParentEligible(parentStart, selfStart time.Time) bool {
	return !parentStart.IsZero() && !selfStart.IsZero() && parentStart.Before(selfStart) && selfStart.Sub(parentStart) <= codexRestoreWindow
}

// codexThreadLocks bounds the lock directory read. A Desktop holds a few.
const codexThreadLocks = 64

func codexThreadLockPaths(home string) ([]string, error) {
	dir, err := os.Open(filepath.Join(home, "thread-writer-locks"))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(codexThreadLocks + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(names) > codexThreadLocks {
		return nil, errors.New("Codex thread locks exceed the bound")
	}
	var paths []string
	for _, name := range names {
		thread, ok := strings.CutSuffix(name, ".lock")
		if ok && codexQueueThread.MatchString(thread) {
			paths = append(paths, filepath.Join(home, "thread-writer-locks", name))
		}
	}
	return paths, nil
}

func codexLockThread(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".lock")
}

// codexRestoreLock keeps one helper restoring a thread at a time. The helper
// whose thread is loaded and a short-lived helper of the same app-server can
// both see it; the second must not take the chat over at the runtime. A
// process holds each thread's lock once; a second claim in it answers held.
var codexHeldThreads sync.Map

func codexRestoreLock(ctx context.Context, state, thread string) (func(), bool) {
	if !filepath.IsAbs(state) || !codexQueueThread.MatchString(thread) {
		return nil, false
	}
	if _, held := codexHeldThreads.LoadOrStore(thread, struct{}{}); held {
		return nil, false
	}
	try, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	release, err := cas.LockContext(try, filepath.Join(state, "codex-receiving", thread), false)
	if err != nil {
		codexHeldThreads.Delete(thread)
		return nil, false
	}
	return func() { release(); codexHeldThreads.Delete(thread) }, true
}

// codexHoldRetry is how often a bound helper retries its thread's lock while
// another helper holds it.
var codexHoldRetry = 5 * time.Second

// holdCodexThread keeps a helper bound by host metadata on its thread's lock
// until ctx ends, so no other helper restores and takes over that chat. While
// another helper holds the lock it retries; a lock this process already holds
// (a restored binding) needs no second hold.
func holdCodexThread(ctx context.Context, state, thread string, after func(time.Duration) <-chan time.Time) {
	for {
		if _, held := codexHeldThreads.Load(thread); held {
			return
		}
		if release, ok := codexRestoreLock(ctx, state, thread); ok {
			<-ctx.Done()
			release()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-after(codexHoldRetry):
		}
	}
}

// restoreCodexThread binds the parent's single loaded thread while no host
// metadata has bound this connection. It returns when the connection is
// bound or closed, or ctx ends. A restored binding keeps its lock until ctx ends.
func restoreCodexThread(ctx context.Context, r *codexReceivingConnection, home, state string, after func(time.Duration) <-chan time.Time) {
	for attempt := 0; ; attempt++ {
		delay := codexRestoreDelays[min(attempt, len(codexRestoreDelays)-1)]
		select {
		case <-ctx.Done():
			return
		case <-after(delay):
		}
		if r.settled() {
			return
		}
		threads, err := codexParentThreads(home)
		if err != nil || len(threads) != 1 {
			continue
		}
		release, ok := codexRestoreLock(ctx, state, threads[0])
		if !ok {
			continue
		}
		if !r.restore(threads[0]) {
			release()
			if r.settled() {
				return
			}
			continue
		}
		fmt.Fprintf(os.Stderr, "OA_CODEX_RECEIVING restored thread=%s\n", threads[0])
		<-ctx.Done()
		release()
		return
	}
}

// codexSessionIndexBytes bounds the title read to the index's newest part.
const codexSessionIndexBytes = 4 << 20

// codexThreadTitle reads the thread's current name from Codex's session
// index, CODEX_HOME/session_index.jsonl. Codex appends a line when it names
// or renames a thread, and `codex queue --thread` accepts that exact name, so
// the newest line for the thread is its current name. A thread without a
// line answers "".
func codexThreadTitle(home, thread string) string {
	if !codexQueueThread.MatchString(thread) {
		return ""
	}
	f, err := os.Open(filepath.Join(home, "session_index.jsonl"))
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > codexSessionIndexBytes {
		if _, err := f.Seek(info.Size()-codexSessionIndexBytes, io.SeekStart); err != nil {
			return ""
		}
	}
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 64<<10), 1<<20)
	title := ""
	for lines.Scan() {
		line := lines.Bytes()
		if !strings.Contains(string(line), thread) {
			continue
		}
		var entry struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(line, &entry) == nil && entry.ID == thread {
			title = hostChatTitle(entry.Name)
		}
	}
	return title
}

// codexTitleRefresh is how often a bound helper rereads its thread's name, so
// a rename or Codex's own naming after the first turn reaches OA's listing.
var codexTitleRefresh = 30 * time.Second

func followCodexThreadTitle(ctx context.Context, status *messagesInboxStatus, home, thread string, after func(time.Duration) <-chan time.Time) {
	shown := ""
	for {
		if title := codexThreadTitle(home, thread); title != "" && title != shown {
			status.setChatTitle(title)
			shown = title
		}
		select {
		case <-ctx.Done():
			return
		case <-after(codexTitleRefresh):
		}
	}
}

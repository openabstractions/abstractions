package gateway

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A local video job's clip is saved when the job ends, whether or not an
// agent reads its status (PICTURES-STORAGE ruling 5 applies unchanged: the
// tool process writes the file once, under the person's account, from bytes
// it verified). The gateway that submitted the job follows it; a gateway
// that starts follows every local video job its state names that has no
// saved clip yet.

// videoWatchPoll is how often a followed job is read; tests shorten it.
var videoWatchPoll = 5 * time.Second

// videoWatchErrorPause bounds the wait after a failed read, such as while
// the runtime restarts.
var videoWatchErrorPause = 60 * time.Second

// videoWatchLimit is how long a job is followed: the provider's 24 hours of
// retention for a request (inference INF-G5) and a margin.
const videoWatchLimit = 26 * time.Hour

// videoWatch is one process's followed handles and save locks.
type videoWatch struct {
	mu       sync.Mutex
	followed map[string]bool
	saving   map[string]*sync.Mutex
}

// lock holds the handle's save lock until the returned function runs.
func (w *videoWatch) lock(handle string) func() {
	w.mu.Lock()
	if w.saving == nil {
		w.saving = map[string]*sync.Mutex{}
	}
	m := w.saving[handle]
	if m == nil {
		m = &sync.Mutex{}
		w.saving[handle] = m
	}
	w.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// follow marks handle followed, or reports that it already is.
func (w *videoWatch) follow(handle string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.followed == nil {
		w.followed = map[string]bool{}
	}
	if w.followed[handle] {
		return false
	}
	w.followed[handle] = true
	return true
}

func (w *videoWatch) done(handle string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.followed, handle)
}

// watchVideo follows a local video job in the background until it ends and
// its clip is saved. A repeated call for the same handle does nothing.
func (o *OA) watchVideo(handle string) {
	if !o.videoWatch.follow(handle) {
		return
	}
	go func() {
		defer o.videoWatch.done(handle)
		o.followVideo(context.Background(), handle)
	}()
}

// followVideo reads the job until it ends; the read of the ended job saves
// its clip. A read that fails, as while the runtime restarts, is tried again.
func (o *OA) followVideo(ctx context.Context, handle string) {
	ctx, cancel := context.WithTimeout(ctx, videoWatchLimit)
	defer cancel()
	pause := videoWatchPoll
	for {
		read, stop := context.WithTimeout(ctx, 2*time.Minute)
		out, err := o.Status(read, JobHandleInput{Handle: handle})
		stop()
		switch {
		case err != nil:
			if strings.Contains(err.Error(), "unknown handle") || strings.Contains(err.Error(), "invalid handle record") {
				return
			}
			pause = min(pause*2, videoWatchErrorPause)
		case out.Outcome != "observed":
			// A job the runtime no longer shows has nothing left to save.
			if out.Outcome == "unknown" || out.Outcome == "forbidden" || out.Outcome == "invalid" {
				return
			}
			pause = min(pause*2, videoWatchErrorPause)
		case out.State == "complete":
			if out.Path == "" && out.Message != "" {
				log.Printf("video %s: %s", handle, out.Message)
			}
			if out.Path != "" || !strings.Contains(out.Message, "being saved") {
				return
			}
			pause = videoWatchPoll
		case out.State == "failed" || out.State == "cancelled":
			return
		default:
			pause = videoWatchPoll
		}
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// WatchUnsavedVideos follows every local video job this gateway's state
// names whose clip is not saved yet and whose handle is younger than the
// follow limit. The gateway calls it once when it starts.
func (o *OA) WatchUnsavedVideos() {
	entries, err := os.ReadDir(o.stateDir)
	if err != nil {
		log.Printf("video watch: the state folder cannot be read: %v", err)
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		handle, ok := strings.CutSuffix(name, ".json")
		if !ok || len(handle) != 32 || !entry.Type().IsRegular() {
			continue
		}
		info, err := entry.Info()
		if err != nil || time.Since(info.ModTime()) > videoWatchLimit {
			continue
		}
		if _, err := os.Stat(filepath.Join(o.stateDir, handle+".video.json")); err == nil {
			continue
		}
		raw, err := readBounded(filepath.Join(o.stateDir, name), maxHandleRecordBytes)
		var rec handleRecord
		if err != nil || json.Unmarshal(raw, &rec) != nil || !rec.LocalVideo {
			continue
		}
		o.watchVideo(handle)
	}
}

// restartingWord is the runtime's waiting word for a job it sends again
// after the provider lost it in a restart (inference INF-G7).
const restartingWord = "provider:restarting"

// restartedSentence is what a job sent again after a restart says.
const restartedSentence = "Restarted after an OpenAbstractions update: the video is being made again from the start."

// jobPhase is one short line of what a job does now, from its snapshot's
// state, waiting word (inference INF-G6, INF-G7) and progress.
func jobPhase(state, waiting string, done, total int64, localVideo bool) string {
	switch state {
	case "complete":
		return "done"
	case "failed", "cancelled", "":
		return ""
	}
	switch {
	case localVideo && activatingCompletionHost(waiting):
		return "preparing the video engine"
	case waiting == "engine:starting":
		return "starting the video engine"
	case waiting == "model:loading" || strings.HasPrefix(waiting, "model:loading:"):
		// Older checkpoints can retain the provider's last observation.
		return "loading the model"
	case waiting == restartingWord:
		return "restarted after an OpenAbstractions update"
	case waiting == "queued" || waiting == "queue:1":
		return "waiting for the video engine"
	case strings.HasPrefix(waiting, "queue:"):
		return "waiting for the video engine: place " + strings.TrimPrefix(waiting, "queue:") + " in its queue"
	case waiting != "":
		return "waiting: " + waiting
	case total == 100:
		// A provider that knows only a fraction reports percent (INF-G6).
		return "generating: " + itoa(done) + "%"
	case total > 0:
		return "generating: step " + itoa(done) + " of " + itoa(total)
	}
	return "starting"
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

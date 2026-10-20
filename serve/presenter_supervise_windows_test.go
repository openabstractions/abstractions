package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// presenterFake is a session where the presenter runs only after a launch
// started it, and exits when the test says so.
type presenterFake struct {
	mu       sync.Mutex
	running  bool
	launches [][]string
	waits    []time.Duration
}

func (f *presenterFake) supervision(t *testing.T, image string, ticks int, cancel context.CancelFunc, between func(tick int)) presenterSupervision {
	tick := 0
	return presenterSupervision{
		alive: func() bool { f.mu.Lock(); defer f.mu.Unlock(); return f.running },
		image: func() (string, error) { return image, nil },
		launch: func(image, dir string, args ...string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.launches = append(f.launches, append([]string{image, dir}, args...))
			f.running = true
			return nil
		},
		every: time.Second, most: 8 * time.Second,
		sleep: func(_ context.Context, d time.Duration) bool {
			f.waits = append(f.waits, d)
			if between != nil {
				between(tick)
			}
			tick++
			if tick > ticks {
				cancel()
				return false
			}
			return true
		},
	}
}

// With the Panel closed, the user-mode supervisor starts the installed
// presenter program, never the Panel, and starts it again after it exits:
// an app's first-use request then still has the program that draws its
// popup (the presenter's poll and present are monitor's tray presenter
// tests).
func TestSupervisorKeepsSessionPresenterRunning(t *testing.T) {
	image := filepath.Join(t.TempDir(), presenterImage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &presenterFake{}
	exitAt := 3
	f.supervision(t, image, 5, cancel, func(tick int) {
		if tick == exitAt {
			f.mu.Lock()
			f.running = false
			f.mu.Unlock()
		}
	}).run(ctx)
	want := []string{image, filepath.Dir(image), "--background"}
	if len(f.launches) != 2 || !slices.Equal(f.launches[0], want) || !slices.Equal(f.launches[1], want) {
		t.Fatal("presenter not started, or not restarted after it exited", f.launches)
	}
	if filepath.Base(f.launches[0][0]) == legacyPresenterImage {
		t.Fatal("the supervisor started the Panel")
	}
}

// A presenter that keeps exiting is started at a doubling interval up to the
// ceiling; one that stays up resets it.
func TestSupervisorBacksOffAPresenterThatKeepsExiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &presenterFake{}
	s := f.supervision(t, filepath.Join(t.TempDir(), presenterImage), 6, cancel, func(int) {
		f.mu.Lock()
		f.running = false
		f.mu.Unlock()
	})
	s.run(ctx)
	if want := []time.Duration{time.Second, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second, 8 * time.Second}; !slices.Equal(f.waits, want) {
		t.Fatal(f.waits)
	}
}

// A runtime-only package has no presenter, and an upgrade in progress
// refuses the launch.
func TestSupervisorStartsNoPresenterWithoutImageOrDuringUpgrade(t *testing.T) {
	for _, mode := range []string{"absent", "upgrade"} {
		ctx, cancel := context.WithCancel(context.Background())
		f := &presenterFake{}
		s := f.supervision(t, filepath.Join(t.TempDir(), presenterImage), 3, cancel, nil)
		if mode == "absent" {
			s.image = func() (string, error) { return "", os.ErrNotExist }
		} else {
			s.guard = func() error { return errors.New("upgrade in progress") }
			s.launch = func(string, string, ...string) error { t.Fatal("launched during an upgrade"); return nil }
		}
		s.run(ctx)
		cancel()
		if len(f.launches) != 0 {
			t.Fatal(mode, f.launches)
		}
	}
}

// The supervisor reads the same session claim the presenter holds.
func TestInstalledPresenterClaimMatchesPresenter(t *testing.T) {
	if installedPresenterClaim != `Local\OpenAbstractions.QuestionPresenter.e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` {
		t.Fatal(installedPresenterClaim)
	}
}

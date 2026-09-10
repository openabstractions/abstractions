package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
)

// The window used to redraw once a second whether or not anything had happened,
// because nothing could tell it that this machine's answers had changed. With
// nothing moving there is now no reason to wake up at all.
func TestAnIdleWindowHasNoReasonToWakeUp(t *testing.T) {
	if got := redrawIn(state{}); got != 0 {
		t.Fatalf("an empty window wants waking in %s", got)
	}
}

func TestAWindowWakesWhenTheClockWouldChangeTheWords(t *testing.T) {
	now := time.Now()
	s := state{Jobs: []view{{moved: now.Add(-3400 * time.Millisecond)}}}
	if got := redrawIn(s); got <= 0 || got > time.Second {
		t.Fatalf("an elapsed time in seconds wants waking in %s", got)
	}
	s = state{Jobs: []view{{moved: now.Add(-90 * time.Minute)}}}
	if got := redrawIn(s); got <= time.Second || got > time.Hour {
		t.Fatalf("an elapsed time in hours wants waking in %s", got)
	}
	s.Delegation.lapses = now.Add(2 * time.Second)
	if got := redrawIn(s); got > 2*time.Second {
		t.Fatalf("a heartbeat about to go stale wants waking in %s", got)
	}
}

// The half that never worked: a change made by something that has never heard
// of this program. Notepad, an installer, jobd, another machine.
func TestAnEditByAStrangerReachesTheWindow(t *testing.T) {
	own(t)
	path := config.UserPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	w, _ := panelOver(t, storeIn(t, t.TempDir()))
	sub := config.Watch()
	defer sub.Close()
	ch := sub.Changes()
	<-ch

	want := t.TempDir()
	b, err := json.Marshal(config.Config{NASStore: want})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("a stranger's edit was never reported; told by %s", sub.How())
	}
	w.panel.stale()
	if got := w.delegation(sub.Current()).NASStore; got != want {
		t.Fatalf("the window shows %q; told by %s", got, sub.How())
	}
}

package win

import (
	"fmt"
	"testing"
	"time"
)

const wmClose = 0x0010

func TestHiddenWindowEndsOnlyCommittedSessionShutdown(t *testing.T) {
	const queryEnd, endSession, closeApp = 0x0011, 0x0016, 1
	ready := make(chan *Window, 1)
	done := make(chan error, 1)
	go func() { done <- RunHidden("owned session-end probe", func(w *Window) { ready <- w }) }()
	w := <-ready
	finished := false
	defer func() {
		if !finished {
			w.Do(w.Close)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("owned window cleanup failed")
			}
		}
	}()
	processed := make(chan bool, 1)
	w.Do(func() {
		accepted := dispatch(uintptr(w.h), queryEnd, 0, closeApp)
		dispatch(uintptr(w.h), endSession, 0, closeApp)
		processed <- accepted != 0
	})
	if !<-processed {
		t.Fatal("session query refused")
	}
	select {
	case <-done:
		finished = true
		t.Fatal("cancelled shutdown ended window")
	default:
	}
	w.Do(func() { dispatch(uintptr(w.h), endSession, 1, closeApp); processed <- true })
	<-processed
	select {
	case err := <-done:
		finished = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Error("committed ENDSESSION did not end the hidden window")
	}
}

type built struct {
	w     *Window
	typed string
}

// TestWindow opens two windows at once, from two goroutines, and fills each
// with the one control package win still draws itself.
//
// Two windows because the window class registers once for the process rather
// than once per window: a second registration fails, and while it was done per
// window a second window was impossible even though the table keyed by handle
// said otherwise.
//
// Nothing here waits on the clock. A window reports itself ready from inside its
// own message loop, since Do only runs when the pump dispatches; it is closed by
// a message, and Run returns when that loop ends.
func TestWindow(t *testing.T) {
	const windows = 2
	ready := make(chan built, windows)
	done := make(chan error, windows)

	for i := range windows {
		go func() {
			done <- Run(fmt.Sprintf("abstraction probe %d", i), 320, 240, func(w *Window) {
				ready <- fill(w)
			})
		}()
	}

	open := make([]built, 0, windows)
	for range windows {
		b := <-ready
		if b.w.dpi < 96 {
			t.Errorf("window opened at %d dpi", b.w.dpi)
		}
		if b.typed != "typed" {
			t.Errorf("field read back %q", b.typed)
		}
		open = append(open, b)
	}

	for _, b := range open {
		postMessage.Call(uintptr(b.w.h), wmClose, 0, 0)
	}
	for range windows {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}

	live.Lock()
	left := len(byHwnd)
	live.Unlock()
	if left != 0 {
		t.Errorf("%d closed windows still in the table", left)
	}
}

func fill(w *Window) built {
	field := w.Field("typed")
	return built{w: w, typed: field.Text()}
}

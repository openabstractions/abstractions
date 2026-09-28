package win

import (
	"fmt"
	"testing"
)

const wmClose = 0x0010

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

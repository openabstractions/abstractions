package win

import (
	"fmt"
	"syscall"
	"testing"
	"unsafe"
)

const (
	wmClose = 0x0010

	// The reading halves of the two messages List sends, used only to read back
	// what it wrote. A struct laid out wrongly still writes somewhere; asking
	// comctl32 for the value again is what proves it wrote where comctl32 looks.
	lvmGetColumn   = lvmFirst + 95
	lvmGetItemText = lvmFirst + 115
)

type built struct {
	w      *Window
	typed  string
	width  int32
	cell   string
	chosen int
}

// TestWindow opens two windows at once, from two goroutines, and fills each
// with the controls the panel uses.
//
// Two windows because the window class registers once for the process rather
// than once per window: a second registration fails, and while it was done per
// window a second window was impossible even though the table keyed by handle
// said otherwise.
//
// The list is written and then read back through comctl32 rather than trusted,
// because LVCOLUMNW and LVITEMW are the two structs whose layout this package
// has to get right.
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
		if want := int32(b.w.px(120)); b.width != want {
			t.Errorf("column read back %d wide, wrote %d", b.width, want)
		}
		if b.cell != "two" {
			t.Errorf("cell read back %q, wrote %q", b.cell, "two")
		}
		if b.chosen != 1 {
			t.Errorf("row %d selected, chose 1", b.chosen)
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
	w.Head("A heading")
	w.Dim("Something quieter")
	w.Button("Press", func() {})
	field := w.Field("typed")

	list := w.List(Column{Title: "Tier", Width: 80}, Column{Title: "State", Width: 120})
	list.Set([][]string{{"a", "one"}, {"b", "two"}})
	list.Choose(1)

	col := lvColumn{Mask: lvcfWidth}
	sendMessage.Call(uintptr(list.h), lvmGetColumn, 1, uintptr(unsafe.Pointer(&col)))

	buf := make([]uint16, 64)
	cell := lvItem{Mask: lvifText, SubItem: 1, Text: &buf[0], TextMax: int32(len(buf))}
	sendMessage.Call(uintptr(list.h), lvmGetItemText, 1, uintptr(unsafe.Pointer(&cell)))

	return built{w: w, typed: field.Text(), width: col.CX,
		cell: syscall.UTF16ToString(buf), chosen: list.Chosen()}
}

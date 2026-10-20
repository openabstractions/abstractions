//go:build windows

package win

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
	"unsafe"
)

func TestShortcutSDKSlots(t *testing.T) {
	word := unsafe.Sizeof(comProc(0))
	if unsafe.Offsetof(shellLinkVtbl{}.GetPath) != 3*word ||
		unsafe.Offsetof(shellLinkVtbl{}.SetDescription) != 7*word ||
		unsafe.Offsetof(shellLinkVtbl{}.GetArguments) != 10*word ||
		unsafe.Offsetof(shellLinkVtbl{}.SetArguments) != 11*word ||
		unsafe.Offsetof(shellLinkVtbl{}.SetPath) != 20*word ||
		unsafe.Offsetof(persistFileVtbl{}.Load) != 5*word ||
		unsafe.Offsetof(persistFileVtbl{}.Save) != 6*word {
		t.Fatal("shell link bindings do not match the SDK interface slots")
	}
}

// A written link reads back with its target, arguments and description, and
// a second write replaces the first.
func TestShortcutWritesAndReadsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ComfyUI.lnk")
	if _, err := ReadShortcut(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a missing link read as %v", err)
	}
	target := filepath.Join(dir, "Abstraction Panel.exe")
	want := Shortcut{Target: target, Arguments: "-open-app comfyui", Description: "ComfyUI with OpenAbstractions image tools"}
	if err := WriteShortcut(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadShortcut(path)
	if err != nil || got != want {
		t.Fatalf("read back %+v, %v; want %+v", got, err, want)
	}
	want.Arguments = "-open-app other"
	if err := WriteShortcut(path, want); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadShortcut(path); err != nil || got != want {
		t.Fatalf("rewritten link read back %+v, %v", got, err)
	}
}

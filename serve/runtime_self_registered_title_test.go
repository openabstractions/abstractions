package main

import (
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// R21: a program registration without a product name named the Keep
// question by its path. A title is a display name; a path reads as the file
// name without its extension.
func TestSelfRegisteredTitleIsNeverAPath(t *testing.T) {
	program := `C:\Users\person\AppData\Local\oa\a\cu\0.8.0-r17\launch-helper.exe`
	for _, tc := range []struct {
		v    wire.ApplicationDescriptor
		want string
	}{
		{wire.ApplicationDescriptor{Name: program, Program: program}, "launch-helper"},
		{wire.ApplicationDescriptor{Name: "cu", Program: program, Title: program}, "launch-helper"},
		{wire.ApplicationDescriptor{Name: program, Program: program, Title: "ComfyUI"}, "ComfyUI"},
		{wire.ApplicationDescriptor{Name: "comfyui", Program: program}, "comfyui"},
	} {
		if got := selfRegisteredTitle(tc.v); got != tc.want {
			t.Errorf("%+v: %q, want %q", tc.v, got, tc.want)
		}
	}
}

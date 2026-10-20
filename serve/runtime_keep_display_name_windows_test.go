//go:build windows

package main

import (
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// R31: the ComfyUI Keep popup read "Let comfyui-connector work with
// OpenAbstractions?". The connector registers itself through applications@3
// with its manifest title "ComfyUI"; the Keep question the Panel's fallback
// raises names the app by that title, never by the program's file name.
func TestKeepQuestionNamesTheAppByItsManifestTitle(t *testing.T) {
	run := newConnectRun(t)
	run.registerSelf(t)
	if r := run.d.askConfirmation(run.ctx, run.f.panel, "comfyui"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("AskConfirmation %+v", r)
	}
	pending := run.pendingKeeps(t)
	if len(pending) != 1 {
		t.Fatalf("want one Keep question, got %+v", pending)
	}
	record := pending[0]
	if record.Content == nil {
		t.Fatal("the Keep question retained no display slots")
	}
	if got := record.Content.Slots["title"]; got != "ComfyUI" {
		t.Fatalf("Keep title slot %q, want the manifest title %q (text %q)", got, "ComfyUI", record.Text)
	}
	if strings.Contains(record.Text, "comfy-helper") && !strings.Contains(record.Text, "ComfyUI") {
		t.Fatalf("Keep text names the file only: %q", record.Text)
	}
}

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// keepingDirectory is the runtime as Setup's Confirm sees it: entries that
// registered themselves read SelfRegistered until the runtime keeps them,
// which happens on the observe numbered keepAt. AskConfirmation records the
// app asked about and returns it to unconfirmed, as the runtime's Undo of
// Setup's Keep does.
type keepingDirectory struct {
	mu       sync.Mutex
	entries  []fwire.ApplicationEntry
	keep     []string
	keepAt   int
	observes int
	asked    []string
}

func (k *keepingDirectory) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.observes++
	if k.keepAt > 0 && k.observes >= k.keepAt {
		for i := range k.entries {
			if slices.Contains(k.keep, k.entries[i].Descriptor.Name) {
				k.entries[i].SelfRegistered = false
			}
		}
	}
	return fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: slices.Clone(k.entries)}, nil
}

func (k *keepingDirectory) Register(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict}, nil
}

func (k *keepingDirectory) Remove(context.Context, string) (fwire.ApplicationChange, error) {
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict}, nil
}

func (k *keepingDirectory) Activate(context.Context, string) (fwire.ApplicationActivationResult, error) {
	return fwire.ApplicationActivationResult{}, nil
}

func (k *keepingDirectory) AskConfirmation(_ context.Context, name string) (fwire.ApplicationChange, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.asked = append(k.asked, name)
	for i := range k.entries {
		if k.entries[i].Descriptor.Name == name {
			k.entries[i].SelfRegistered = true
		}
	}
	k.keepAt = 0
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

func keepingFixture(t *testing.T) (*servicePanel, *keepingDirectory, []setup.StepApp) {
	t.Helper()
	opencode := filepath.Join(t.TempDir(), "opencode.exe")
	editor := filepath.Join(t.TempDir(), "editor.exe")
	dir := &keepingDirectory{keep: []string{"opencode"}, keepAt: 3, entries: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "opencode", Program: opencode, Title: "OpenCode"}, SelfRegistered: true},
		{Descriptor: fwire.ApplicationDescriptor{Name: "editor", Program: editor, Title: "Editor"}, SelfRegistered: true},
	}}
	p := &servicePanel{comfyApps: dir, appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	pending := []setup.StepApp{{Name: "opencode", Title: "OpenCode", Program: opencode, Unconfirmed: true, Use: setup.UseCode}}
	saved := setupKeepPoll
	setupKeepPoll = time.Millisecond
	t.Cleanup(func() { setupKeepPoll = saved })
	return p, dir, pending
}

// Setup reports Ready only after the Keeps are written: Confirm returns once
// the app the question named reads as kept, not before, and tells it in
// Activity with Undo. The app the question did not name is not waited for
// and stays unconfirmed.
func TestSetupConfirmWaitsForTheKeepsAndTellsThemWithUndo(t *testing.T) {
	p, dir, pending := keepingFixture(t)
	if err := p.confirmSetupKeeps(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if dir.observes < dir.keepAt {
		t.Fatalf("Confirm returned after %d reads, before the Keep on read %d", dir.observes, dir.keepAt)
	}
	if !dir.entries[1].SelfRegistered {
		t.Fatal("an app the question did not name was kept")
	}
	lines := p.appActivity.lines()
	if len(lines) != 1 || lines[0].App != "OpenCode" || lines[0].What != "Setup kept it when you chose Set up" || !slices.Equal(lines[0].Undo, []string{"opencode"}) {
		t.Fatalf("Activity: %+v", lines)
	}

	h := p.handler(applicationTestKey)
	got := applicationCall(t, h, "GET", "/activity/app-requests", "")
	var page activityPage
	if err := json.Unmarshal(got.Body.Bytes(), &page); err != nil || len(page.Events) != 1 || page.Events[0].Undo == nil || !slices.Equal(page.Events[0].Undo.Apps, []string{"opencode"}) {
		t.Fatalf("the Activity row carries no Undo: %s", got.Body.String())
	}
	undo, _ := json.Marshal(page.Events[0].Undo)
	if rec := applicationCall(t, h, "POST", "/activity/app-requests", string(undo)); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Undone") {
		t.Fatalf("Undo: %d %s", rec.Code, rec.Body.String())
	}
	if !slices.Equal(dir.asked, []string{"opencode"}) || !dir.entries[0].SelfRegistered {
		t.Fatalf("Undo did not return the kept app to unconfirmed: %v", dir.asked)
	}
	if rec := applicationCall(t, h, "POST", "/activity/app-requests", string(undo)); rec.Code != 409 || len(dir.asked) != 1 {
		t.Fatalf("a second Undo: %d asked %v", rec.Code, dir.asked)
	}
}

// When the runtime does not keep the named app, Confirm fails and Setup does
// not report Ready; nothing is told in Activity.
func TestSetupConfirmFailsWithoutTheKeeps(t *testing.T) {
	p, dir, pending := keepingFixture(t)
	dir.keepAt = 0
	saved := setupKeepWait
	setupKeepWait = 20 * time.Millisecond
	t.Cleanup(func() { setupKeepWait = saved })
	if err := p.confirmSetupKeeps(context.Background(), pending); err == nil {
		t.Fatal("Confirm returned with the named app still unconfirmed")
	}
	if len(p.appActivity.lines()) != 0 {
		t.Fatal("Activity told a Keep that was not written")
	}
}

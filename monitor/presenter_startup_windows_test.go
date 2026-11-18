//go:build windows && !panel

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// startupSurface records the leased prompt it was asked to show and ends
// the poll loop once it has one.
type startupSurface struct {
	fakeSurface
	mu    sync.Mutex
	shown []trayQuestionPresentation
	ids   []string
	stop  context.CancelFunc
}

func (s *startupSurface) AskLeased(_ context.Context, occurrence bookOccurrence, start func() bool, shown func()) (string, error) {
	if !start() {
		return "", errPromptNotShown
	}
	shown()
	s.mu.Lock()
	s.shown = append(s.shown, occurrence.Presentation())
	s.ids = append(s.ids, occurrence.ID())
	s.mu.Unlock()
	s.stop()
	return "", nil
}

// Installed check 2026-11-21: the presenter logged "Could not show the
// question: service resolution: runtime_unavailable: abstraction.asks/
// operator@1" while the runtime was still starting, then logged nothing
// more. The production poll loop re-resolves the operator on every poll:
// after that startup failure it still shows an agent's "Open ComfyUI?"
// question, as one plain question naming the app, with the program path,
// request code and expiry kept in Show details.
func TestPresenterShowsOpenQuestionAfterStartupRuntimeUnavailable(t *testing.T) {
	book, operator := trayTestBook(t, func(peer *identity.Peer) (string, bool) {
		process, err := peer.Process.AtLeast(identity.ProofKernel)
		return "fixture-desktop", err == nil && process.PID == os.Getpid()
	})
	program := filepath.Join(t.TempDir(), "opencode.exe")
	code := "2408f5a114f85b3efe1a"
	record, outcome, err := book.AskApplicationAs("scope", program, "application-command:startup", asks.ApplicationCommandOnceKey,
		map[string]string{"program": program, "operation": "Open ComfyUI", "operationmore": "", "command": "open",
			"code": code, "until": time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339)}, listen.Seen{}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	if !strings.Contains(record.Text, program) || !strings.Contains(record.Text, code) {
		t.Fatalf("the stored record text changed shape: %q", record.Text)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*trayPollInterval)
	defer cancel()
	surface := &startupSurface{fakeSurface: fakeSurface{dismissed: make(chan struct{})}, stop: cancel}
	var resolves atomic.Int32
	presenter := &questionPresenter{session: unlockedSession(), surface: surface, account: probe.Self().Account,
		operator: func(context.Context) (presenterOperator, error) {
			if resolves.Add(1) == 1 {
				return nil, errors.New("service resolution: runtime_unavailable: abstraction.asks/operator@1 (capability abstraction.asks)")
			}
			return operator, nil
		}}
	pollTrayLoop(ctx, nil, presenter, func() { t.Error("the open question opened the Panel") })
	surface.mu.Lock()
	defer surface.mu.Unlock()
	if resolves.Load() < 2 || len(surface.ids) != 1 || surface.ids[0] != record.ID {
		t.Fatalf("after the startup failure the loop resolved %d times and showed %v, want %s", resolves.Load(), surface.ids, record.ID)
	}
	view := surface.shown[0]
	if view.Instruction != "Allow opencode to open ComfyUI this once?" || view.Content != "" {
		t.Fatalf("heading %q body %q", view.Instruction, view.Content)
	}
	if visible := view.Instruction + view.Content + view.Footer; strings.Contains(visible, program) || strings.Contains(visible, code) || strings.Contains(visible, "Request") {
		t.Fatalf("the popup shows the raw record: %q", visible)
	}
	for _, want := range []string{"Program: " + program, "Request: " + code} {
		if !strings.Contains(view.Details, want) {
			t.Fatalf("details miss %q: %q", want, view.Details)
		}
	}
	labels := []string{}
	for _, choice := range view.Choices {
		labels = append(labels, choice.Label)
	}
	if strings.Join(labels, ",") != "Allow once,Not now" {
		t.Fatalf("buttons %v", labels)
	}
}

package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// ownedSurface is a prompt surface whose prompts a window owns.
type ownedSurface struct{ *fakeSurface }

func (ownedSurface) OwnerWindow() uintptr { return 0x4242 }

func systemAccessFixture(id string) askclient.RecordMetadata {
	return askclient.RecordMetadata{ID: id, Asker: "reader.exe", Key: asks.SystemSecurityLogKey, About: "a1b2c3d4",
		Text:    "reader.exe wants to read one page of the Windows Security log. Windows asks for administrator approval next. Request a1b2c3d4 expires 2026-10-07T12:15:00Z.",
		Options: []string{"once", "refuse", "never"}, Asked: "2026-10-07T12:00:00Z"}
}

// TestSystemAccessLaunchFollowsOnce is FAC-H3: the presenter launches the
// helper right after recording the person's once on a system-access
// question, with that question as the ticket, and after nothing else.
func TestSystemAccessLaunchFollowsOnce(t *testing.T) {
	for _, tc := range []struct {
		record askclient.RecordMetadata
		option string
		launch bool
	}{
		{systemAccessFixture("0123456789abcdef0123456789abcdef"), "once", true},
		{systemAccessFixture("0123456789abcdef0123456789abcdee"), "refuse", false},
		{systemAccessFixture("0123456789abcdef0123456789abcded"), "never", false},
		{systemAccessFixture("0123456789abcdef0123456789abcdec"), "", false},
		{pendingFixture(), "allow", false},
	} {
		operator := &fakeOperator{records: []askclient.RecordMetadata{tc.record}}
		p := testPresenter(unlockedSession(), ownedSurface{&fakeSurface{option: tc.option}}, operator, &fakeRules{})
		var launched []string
		p.launch = func(owner uintptr, operation, ticket string) {
			launched = append(launched, fmt.Sprintf("%#x %s %s", owner, operation, ticket))
		}
		result := p.present(context.Background(), tc.record)
		if tc.launch != (len(launched) == 1) || len(launched) > 1 {
			t.Fatalf("%s %q: launched %v (%+v)", tc.record.Key, tc.option, launched, result)
		}
		if tc.launch && launched[0] != "0x4242 security-log-page "+tc.record.ID {
			t.Fatalf("launched %q, want it owned by the prompt's window", launched[0])
		}
	}
	record := systemAccessFixture("0123456789abcdef0123456789abcdeb")
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	operator.beforeAnswer = func() {
		operator.mu.Lock()
		operator.records[0].Option = "refuse"
		operator.mu.Unlock()
	}
	p := testPresenter(unlockedSession(), &fakeSurface{option: "once"}, operator, &fakeRules{})
	p.launch = func(uintptr, string, string) { t.Fatal("launched after an answer recorded elsewhere") }
	p.present(context.Background(), record)
}

// TestQuestionsPageLeavesSystemAccessOnceToThePrompt: the page offers and
// accepts refuse and never, and leaves once to the native prompt.
func TestQuestionsPageLeavesSystemAccessOnceToThePrompt(t *testing.T) {
	record := systemAccessFixture("0123456789abcdef0123456789abcdef")
	if got := strings.Join(pageOptions(record), ","); got != "refuse,never" {
		t.Fatalf("page options %q", got)
	}
	if !promptOnly(&record, "once") || promptOnly(&record, "refuse") || promptOnly(nil, "once") {
		t.Fatal("prompt-only answers")
	}
	other := pendingFixture()
	other.Options = []string{"once", "refuse"}
	if got := strings.Join(pageOptions(other), ","); got != "once,refuse" || promptOnly(&other, "once") {
		t.Fatalf("another question lost once: %q", got)
	}
	if len(record.Options) != 3 {
		t.Fatal("pageOptions changed the record")
	}
}

// TestSystemAccessPromptSaysWindowsAsksNext: the prompt for the
// service-owned key says the administrator prompt follows; question text
// alone selects nothing.
func TestSystemAccessPromptSaysWindowsAsksNext(t *testing.T) {
	view := trayQuestionViewForAsker(systemAccessFixture("0123456789abcdef0123456789abcdef"), trayPublisher{}, nil, "")
	if !strings.Contains(view.Title, "Administrator access") || view.Warning == "" || view.Default != "refuse" {
		t.Fatalf("%+v", view)
	}
	forged := systemAccessFixture("0123456789abcdef0123456789abcdef")
	forged.Key = "download.reach"
	if view := trayQuestionViewForAsker(forged, trayPublisher{}, nil, ""); view.Warning != "" || strings.Contains(view.Title, "Administrator") {
		t.Fatalf("question text selected the administrator view: %+v", view)
	}
}

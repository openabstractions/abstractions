package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// applicationSurfaceFake is the launcher's question panel: it records each
// question and report and answers with the scripted options in order.
type applicationSurfaceFake struct {
	unlocked  bool
	answers   []string
	asked     []trayQuestionPresentation
	reports   []presenterReport
	onAsk     func(int)
	notShowns int
	// unanswered keeps each question open until Dismiss.
	unanswered bool
	dismissed  chan struct{}
}

func (s *applicationSurfaceFake) Unlocked() bool { return s.unlocked }

func (s *applicationSurfaceFake) Ask(prompt trayQuestionPresentation) (string, error) {
	s.asked = append(s.asked, prompt)
	if s.onAsk != nil {
		s.onAsk(len(s.asked))
	}
	if s.notShowns > 0 {
		s.notShowns--
		return "", errPromptNotShown
	}
	if s.unanswered {
		<-s.dismissed
		return "", nil
	}
	answer := s.answers[0]
	s.answers = s.answers[1:]
	return answer, nil
}

func (s *applicationSurfaceFake) Dismiss() {
	if s.dismissed != nil {
		select {
		case s.dismissed <- struct{}{}:
		default:
		}
	}
}

func (s *applicationSurfaceFake) showReport(report presenterReport) bool {
	s.reports = append(s.reports, report)
	return true
}

func nativeApplicationPanel(t *testing.T, apps *applicationDirectoryFake, title string) *servicePanel {
	t.Helper()
	p, _ := applicationPanel(t, apps, manifestText(applicationTestProgram(t), title))
	return p
}

func TestNativeRegistrationAsksOnceAndRegistersOnTheConfirmation(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := nativeApplicationPanel(t, apps, "Example")
	surface := &applicationSurfaceFake{unlocked: true, answers: []string{"register"}, notShowns: 1}
	if err := p.askApplicationRequest(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	// A question the locked session did not show is asked again; nothing
	// was written before the confirmation.
	if len(surface.asked) != 2 || apps.registers != 1 || apps.entries["example"].Title != "Example" {
		t.Fatalf("asked %d registers %d entries %+v", len(surface.asked), apps.registers, apps.entries)
	}
	q := surface.asked[1]
	if q.Title != "Register an application" || q.Instruction != "Register Example?" || q.Level != promptLevelBlue ||
		q.Default != "" || len(q.Choices) != 1 || q.Choices[0].Option != "register" || q.Permission != nil || q.AppChoice {
		t.Fatalf("question %+v", q)
	}
	if !strings.Contains(q.Content, "grants it no permissions") || !strings.Contains(q.Content, "Program: "+applicationTestProgram(t)) ||
		!strings.Contains(q.Details, "Start arguments: --serve <b>") || !strings.Contains(q.Details, "example/ready@1") {
		t.Fatalf("review text:\n%s\n--\n%s", q.Content, q.Details)
	}
	if len(surface.reports) != 0 {
		t.Fatalf("a confirmed registration wrote a second dialog: %+v", surface.reports)
	}
}

func TestNativeRegistrationCancelRegistersNothing(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := nativeApplicationPanel(t, apps, "Example")
	surface := &applicationSurfaceFake{unlocked: true, answers: []string{""}}
	err := p.askApplicationRequest(context.Background(), surface)
	if !errors.Is(err, errApplicationNotChanged) || !strings.Contains(err.Error(), "Nothing was registered.") {
		t.Fatalf("cancel: %v", err)
	}
	if apps.registers != 0 || len(apps.entries) != 0 || len(surface.reports) != 0 {
		t.Fatalf("a cancel registered or reported: %d %+v", apps.registers, surface.reports)
	}
	// The cancel settled the request: a later confirmation finds none.
	if _, err := p.confirmApplicationRegister(context.Background(), ""); err == nil {
		t.Fatal("a cancelled request stayed confirmable")
	}
}

func TestNativeRegistrationRefusesAReservedTitleWithoutAQuestion(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := nativeApplicationPanel(t, apps, "OpenAbstractions")
	surface := &applicationSurfaceFake{unlocked: true}
	err := p.askApplicationRequest(context.Background(), surface)
	if !errors.Is(err, errApplicationNotChanged) || len(surface.asked) != 0 || apps.registers != 0 {
		t.Fatalf("reserved title: %v asked %d", err, len(surface.asked))
	}
	if len(surface.reports) != 1 || surface.reports[0].Instruction != reservedTitleRefusal {
		t.Fatalf("reports %+v", surface.reports)
	}
}

// An entry that appears between the question and the click is reviewed
// again: the replacement needs its own confirmation of both entries.
func TestNativeRegistrationReviewsAnEntryThatChangedUnderThePerson(t *testing.T) {
	program := applicationTestProgram(t)
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := nativeApplicationPanel(t, apps, "Example 2")
	surface := &applicationSurfaceFake{unlocked: true, answers: []string{"register", "register"}}
	surface.onAsk = func(n int) {
		if n == 1 {
			apps.entries["example"] = fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example 1"}
		}
	}
	if err := p.askApplicationRequest(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	if len(surface.asked) != 2 || apps.entries["example"].Title != "Example 2" || apps.removes != 1 {
		t.Fatalf("asked %d entries %+v removes %d", len(surface.asked), apps.entries, apps.removes)
	}
	second := surface.asked[1]
	if !strings.Contains(second.Content, "Registering replaces it.") || !strings.Contains(second.Details, "Registered now") ||
		!strings.Contains(second.Details, "Example 1") || !strings.Contains(second.Details, "Replacement") {
		t.Fatalf("the second review does not show both entries:\n%s\n--\n%s", second.Content, second.Details)
	}
}

func TestNativeRemovalAsksAndRemoves(t *testing.T) {
	program := applicationTestProgram(t)
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": {Name: "example", Program: program, Title: "Example"}}}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}}
	p.appRequest.remove = "example"
	surface := &applicationSurfaceFake{unlocked: true, answers: []string{"remove"}}
	if err := p.askApplicationRequest(context.Background(), surface); err != nil {
		t.Fatal(err)
	}
	q := surface.asked[0]
	if q.Instruction != "Remove Example?" || q.Choices[0].Option != "remove" || !strings.Contains(q.Content, "Program: "+program) || len(apps.entries) != 0 {
		t.Fatalf("removal %+v entries %+v", q, apps.entries)
	}
}

// The run is bounded (applicationRequestWait): a session that stays locked,
// or a question nobody answers, ends it with nothing changed.
func TestNativeRequestEndsUnchangedAfterTheWait(t *testing.T) {
	saved := applicationRequestWait
	applicationRequestWait = 300 * time.Millisecond
	defer func() { applicationRequestWait = saved }()

	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := nativeApplicationPanel(t, apps, "Example")
	locked := &applicationSurfaceFake{}
	err := p.askApplicationRequest(context.Background(), locked)
	if !errors.Is(err, errApplicationNotChanged) || !strings.Contains(err.Error(), "Nothing was registered.") || len(locked.asked) != 0 {
		t.Fatalf("locked past the wait: %v asked %d", err, len(locked.asked))
	}
	if _, err := p.confirmApplicationRegister(context.Background(), ""); err == nil || apps.registers != 0 {
		t.Fatal("a request that timed out stayed confirmable")
	}

	program := applicationTestProgram(t)
	apps = &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": {Name: "example", Program: program, Title: "Example"}}}
	p = &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{}}
	p.appRequest.remove = "example"
	open := &applicationSurfaceFake{unlocked: true, unanswered: true, dismissed: make(chan struct{})}
	start := time.Now()
	err = p.askApplicationRequest(context.Background(), open)
	if !errors.Is(err, errApplicationNotChanged) || !strings.Contains(err.Error(), "Nothing was removed.") || len(open.asked) != 1 {
		t.Fatalf("unanswered past the wait: %v asked %d", err, len(open.asked))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second || len(apps.entries) != 1 || apps.removes != 0 {
		t.Fatalf("after %v the entry was changed: %+v", elapsed, apps.entries)
	}
}

// A locked session asks nothing until it unlocks; a run that ends before
// returns the context's error and writes nothing.
func TestNativeRegistrationWaitsForTheUnlockedSession(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p := nativeApplicationPanel(t, apps, "Example")
	surface := &applicationSurfaceFake{}
	ctx, cancel := context.WithTimeout(context.Background(), 600*time.Millisecond)
	defer cancel()
	if err := p.askApplicationRequest(ctx, surface); !errors.Is(err, context.DeadlineExceeded) || len(surface.asked) != 0 || apps.registers != 0 {
		t.Fatalf("locked: %v asked %d", err, len(surface.asked))
	}
}

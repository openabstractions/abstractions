package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	client "github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// trayTestBook starts an isolated runtime exposing only a question operator
// this test process is authorized against, the way first_use_panel_test.go
// trusts a fixture runtime. It returns the loaded book, so the test can admit
// questions directly, and the operator client the tray polls through.
func trayTestBook(t *testing.T) (*asks.Book, *askclient.Operator) {
	t.Helper()
	own(t)
	dir := t.TempDir()
	book, err := asks.LoadApplicationBook(dir + "/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	self := func(ctx context.Context, peer *identity.Peer) bool {
		process, err := peer.Process.AtLeast(listen.Program.Process)
		return err == nil && process.PID == os.Getpid() && ctx.Err() == nil
	}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.QuestionBook = book
		o.QuestionEndpoint = listen.Endpoint(fmt.Sprintf("tray-asks-%d", os.Getpid()))
		o.QuestionOperator = func(ctx context.Context, peer *identity.Peer) error {
			if !self(ctx, peer) {
				return fmt.Errorf("not this test")
			}
			return nil
		}
	})
	operator, err := panelMachine().ResolveAsksOperator(context.Background(), facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	return book, operator
}

func ask(t *testing.T, book *asks.Book, requestKey, program, action, resource string) string {
	t.Helper()
	// Permission occurrences carry a canonical program tuple, even when the
	// fixture's visible requester name is a short file name.
	if !filepath.IsAbs(program) {
		absolute, err := filepath.Abs(program)
		if err != nil {
			t.Fatal(err)
		}
		program = absolute
	}
	record, outcome, err := book.AskApplication("test-scope", "test-program", requestKey, asks.FirstUseKey,
		map[string]string{"program": program, "action": action, "resource": resource}, listen.Seen{Why: "test"})
	if err != nil || outcome != "pending" {
		t.Fatalf("admit %s: outcome %q err %v", requestKey, outcome, err)
	}
	return record.ID
}

func TestTrayAnnouncerAnnouncesEachNewPendingQuestionOnce(t *testing.T) {
	book, operator := trayTestBook(t)
	firstID := ask(t, book, "q1", "one.exe", "abstraction.model/lookup", "hf")

	announcer := newTrayAnnouncer()
	fresh, polled, unavailable := pollTray(context.Background(), panelMachine(), announcer)
	if unavailable {
		t.Fatal("runtime reported unavailable")
	}
	if polled == nil {
		t.Fatal("a reachable poll returns the operator it resolved, for the caller's reads")
	}
	if len(fresh) != 1 || fresh[0].ID != firstID {
		t.Fatalf("first poll should announce the one pending question, got %+v", fresh)
	}

	// Polling again with nothing new announces nothing.
	fresh, _, unavailable = pollTray(context.Background(), panelMachine(), announcer)
	if unavailable || len(fresh) != 0 {
		t.Fatalf("second poll re-announced: unavailable=%v fresh=%+v", unavailable, fresh)
	}

	// A second, distinct pending question is announced once, on its own.
	secondID := ask(t, book, "q2", "two.exe", "abstraction.inference/complete", "openai")
	fresh, _, unavailable = pollTray(context.Background(), panelMachine(), announcer)
	if unavailable || len(fresh) != 1 || fresh[0].ID != secondID {
		t.Fatalf("third poll should announce only the new question, got unavailable=%v fresh=%+v", unavailable, fresh)
	}

	// Answering the first question removes it from "pending"; a later poll
	// still never re-announces either id.
	if _, err := operator.AnswerQuestionContext(context.Background(), firstID, "allow"); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.RetireQuestionContext(context.Background(), secondID); err != nil {
		t.Fatal(err)
	}
	fresh, _, unavailable = pollTray(context.Background(), panelMachine(), announcer)
	if unavailable || len(fresh) != 0 {
		t.Fatalf("answered/retired questions were re-announced: unavailable=%v fresh=%+v", unavailable, fresh)
	}
}

func TestTrayAnnouncerCanRequeuePromptDismissedBySessionLock(t *testing.T) {
	announcer := newTrayAnnouncer()
	record := askclient.RecordMetadata{ID: "lock-question"}
	if got := announcer.update([]askclient.RecordMetadata{record}); len(got) != 1 {
		t.Fatalf("first poll should announce the question, got %+v", got)
	}
	if got := announcer.update([]askclient.RecordMetadata{record}); len(got) != 0 {
		t.Fatalf("question should not repeat before lock, got %+v", got)
	}
	announcer.forget(record.ID)
	if got := announcer.update([]askclient.RecordMetadata{record}); len(got) != 1 {
		t.Fatalf("lock dismissal should leave question ready after unlock, got %+v", got)
	}
}

func TestTrayPollUnavailableRuntimeReportsNoQuestions(t *testing.T) {
	own(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}
	if runtime.GOOS != "windows" {
		uid, _ := strconv.Atoi(account.Uid)
		gid, _ := strconv.Atoi(account.Gid)
		principal = identity.User{Kind: "posix", UID: uid, GID: gid}
	}
	// An endpoint nothing is listening on: resolution fails the way an
	// absent or not-yet-started runtime does, the same fixture
	// explore_panel_test.go builds for the same purpose.
	absent := client.NewVerified(listen.Endpoint(fmt.Sprintf("tray-absent-%d", time.Now().UnixNano())),
		listen.ServerExpectation{Principal: principal, Program: exe})
	previous := panelMachine
	panelMachine = func() *facade.Machine { return absent }
	t.Cleanup(func() { panelMachine = previous })

	announcer := newTrayAnnouncer()
	fresh, polled, unavailable := pollTray(context.Background(), panelMachine(), announcer)
	if !unavailable || polled != nil {
		t.Fatal("expected the poll to report the runtime unavailable with no operator")
	}
	if len(fresh) != 0 {
		t.Fatalf("an unavailable runtime must report no questions, got %+v", fresh)
	}
}

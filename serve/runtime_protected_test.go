package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
)

func protectedFixture(t *testing.T) (*runtimeProtected, *asks.Book, string, []byte) {
	t.Helper()
	a, book := firstUseBook(t)
	p := newRuntimeProtected(a, filepath.Join(t.TempDir(), protected.HelperFile), func(*identity.Peer) (identity.Process, error) {
		return identity.Process{PID: 7, StartTime: time.Unix(7, 0)}, nil
	})
	request, err := logservice.EncodeProtectedSourceRequest(logservice.ProtectedSourceRequest{Source: logservice.ProtectedSourceID, MaxRecords: 8, MaxBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	return p, book, filepath.Join(t.TempDir(), "reader.exe"), request
}

func noPublisher() asks.Publisher { return asks.Publisher{} }

// pendingSystemQuestion returns the one system-access question in the Book.
func pendingSystemQuestion(t *testing.T, book *asks.Book) asks.Record {
	t.Helper()
	history, _, err := book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	var found []asks.Record
	for _, r := range history {
		if r.Key == asks.SystemSecurityLogKey && r.Pending() {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d pending system-access questions", len(found))
	}
	return found[0]
}

// helperExchange plays the helper against the broker: one claim, then one
// completion with result or failure.
func helperExchange(t *testing.T, p *runtimeProtected, ticket string, result []byte, failure string) []byte {
	t.Helper()
	frame := func(m map[string]any) map[string]any {
		data, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		var reply map[string]any
		if err := json.Unmarshal(p.broker.Exchange(context.Background(), &identity.Peer{}, data), &reply); err != nil {
			t.Fatal(err)
		}
		return reply
	}
	claim := frame(map[string]any{"version": 1, "kind": "claim", "operation": protected.OperationSecurityLogPage, "ticket": ticket})
	if claim["refused"] != nil {
		t.Fatalf("claim refused: %v", claim["refused"])
	}
	var request []byte
	if s, ok := claim["request"].(string); ok {
		//unchecked: a test reads the base64 text json.Marshal wrote
		request, _ = jsonBytes(s)
	}
	done := map[string]any{"version": 1, "kind": "complete", "operation": protected.OperationSecurityLogPage, "ticket": ticket}
	if failure != "" {
		done["failure"] = failure
	} else {
		done["result"] = result
	}
	if reply := frame(done); reply["refused"] != nil {
		t.Fatalf("completion refused: %v", reply["refused"])
	}
	return request
}

func jsonBytes(s string) ([]byte, error) {
	var b []byte
	err := json.Unmarshal([]byte(`"`+s+`"`), &b)
	return b, err
}

// TestProtectedReadAdmitsOnceAndDeliversOnce is LOG-N1 and ASK-S1 in the
// runtime: one question per frozen request, waiting states, the helper's
// result delivered once, and the next read asking again.
func TestProtectedReadAdmitsOnceAndDeliversOnce(t *testing.T) {
	p, book, program, request := protectedFixture(t)
	ctx := context.Background()
	if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("first read: %v", err)
	}
	if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("replayed read: %v", err)
	}
	question := pendingSystemQuestion(t, book)
	if question.AskerProgram != program || question.Asker != "reader.exe" || len(question.About) != 8 || question.Options[0] != "once" {
		t.Fatalf("question %+v", question)
	}
	if _, err := p.approve(ctx, question.ID); err == nil {
		t.Fatal("approved before the person answered")
	}
	if _, _, err := book.AnswerApplication(question.ID, "once"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationPending) {
		t.Fatalf("after once: %v", err)
	}
	other, err := logservice.EncodeProtectedSourceRequest(logservice.ProtectedSourceRequest{Source: logservice.ProtectedSourceID, MaxRecords: 9, MaxBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.readFor(ctx, program, noPublisher, other); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("another request rode on the approval: %v", err)
	}
	if got := helperExchange(t, p, question.ID, []byte("page bytes"), ""); string(got) != string(request) {
		t.Fatalf("the helper received %q", got)
	}
	if _, err := p.readFor(ctx, filepath.Join(filepath.Dir(program), "other.exe"), noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("another program took the page: %v", err)
	}
	result, err := p.readFor(ctx, program, noPublisher, request)
	if err != nil || string(result) != "page bytes" {
		t.Fatalf("delivery: %q %v", result, err)
	}
	if record, found, err := book.ApplicationRecord(question.ID); err != nil || found {
		t.Fatalf("the delivered question stayed: %+v %v", record, err)
	}
	if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("a second page needs a second yes: %v", err)
	}
}

// TestProtectedReadRefusals: refuse is refused for its interval, never
// stands, an unclaimed once expires, an elevated denial is forbidden, and
// only the operator's once approves a claim.
func TestProtectedReadRefusals(t *testing.T) {
	ctx := context.Background()
	t.Run("refuse then ask again", func(t *testing.T) {
		p, book, program, request := protectedFixture(t)
		p.readFor(ctx, program, noPublisher, request)
		q := pendingSystemQuestion(t, book)
		book.AnswerApplication(q.ID, "refuse")
		if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrProtectedRefused) {
			t.Fatalf("%v", err)
		}
		p.now = func() time.Time { return time.Now().Add(firstUseRefuseInterval + time.Second) }
		if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
			t.Fatalf("after the interval: %v", err)
		}
	})
	t.Run("never stands", func(t *testing.T) {
		p, book, program, request := protectedFixture(t)
		p.readFor(ctx, program, noPublisher, request)
		q := pendingSystemQuestion(t, book)
		book.AnswerApplication(q.ID, "never")
		p.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
		if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrProtectedRefused) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("unclaimed once expires", func(t *testing.T) {
		p, book, program, request := protectedFixture(t)
		p.readFor(ctx, program, noPublisher, request)
		q := pendingSystemQuestion(t, book)
		book.AnswerApplication(q.ID, "once")
		p.now = func() time.Time { return time.Now().Add(protected.ClaimWindow + protected.HelperDeadline + 16*time.Second) }
		if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
			t.Fatalf("%v", err)
		}
		if again := pendingSystemQuestion(t, book); again.ID == q.ID {
			t.Fatal("the expired question was reused")
		}
	})
	t.Run("denied elevated", func(t *testing.T) {
		p, book, program, request := protectedFixture(t)
		p.readFor(ctx, program, noPublisher, request)
		q := pendingSystemQuestion(t, book)
		book.AnswerApplication(q.ID, "once")
		helperExchange(t, p, q.ID, nil, protected.FailureDenied)
		if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrProtectedDenied) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("only the operator's once approves", func(t *testing.T) {
		p, book, program, request := protectedFixture(t)
		p.readFor(ctx, program, noPublisher, request)
		q := pendingSystemQuestion(t, book)
		if _, _, err := book.AnswerApplicationAsResponder(q.ID, "once", filepath.Join(t.TempDir(), "assistant.exe"), func(asks.Record, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := p.approve(ctx, q.ID); err == nil {
			t.Fatal("a responder's once approved a claim")
		}
		if _, err := p.approve(ctx, "ffffffffffffffffffffffffffffffff"); err == nil {
			t.Fatal("an unknown ticket approved")
		}
	})
}

// TestProtectedReadAtCapacityLeavesNoQuestion is FAC-H2's capacity: with 16
// open tickets no system-access question is admitted, and a pending one
// whose ticket cannot be registered (after a restart) is withdrawn.
func TestProtectedReadAtCapacityLeavesNoQuestion(t *testing.T) {
	ctx := context.Background()
	fill := func(p *runtimeProtected) {
		for i := 0; i < protected.MaxTickets; i++ {
			if err := p.broker.Register(fmt.Sprintf("%032x", i+1), protected.OperationSecurityLogPage, `C:\Apps\other.exe`, []byte(fmt.Sprintf("r%d", i))); err != nil {
				t.Fatal(err)
			}
		}
	}
	pending := func(book *asks.Book) int {
		history, _, err := book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range history {
			if r.Key == asks.SystemSecurityLogKey && r.Pending() {
				n++
			}
		}
		return n
	}
	p, book, program, request := protectedFixture(t)
	fill(p)
	if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, protected.ErrUnavailable) {
		t.Fatalf("at capacity: %v", err)
	}
	if n := pending(book); n != 0 {
		t.Fatalf("%d system-access questions left pending at capacity", n)
	}

	p, book, program, request = protectedFixture(t)
	if _, err := p.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatal(err)
	}
	restarted := newRuntimeProtected(p.asks, p.helper, func(*identity.Peer) (identity.Process, error) { return identity.Process{}, nil })
	fill(restarted)
	if _, err := restarted.readFor(ctx, program, noPublisher, request); !errors.Is(err, protected.ErrUnavailable) {
		t.Fatalf("after a restart at capacity: %v", err)
	}
	if n := pending(book); n != 0 {
		t.Fatalf("%d system-access questions left pending without a ticket", n)
	}
}

// restartedRuntime opens the Book at path as a runtime start does once it
// owns its endpoints: the stale withdrawal, then a protected path with an
// empty broker. It returns what the start reported.
func restartedRuntime(t *testing.T, path string) (*runtimeProtected, *asks.Book, []error) {
	t.Helper()
	book, err := asks.LoadApplicationBook(path)
	if err != nil {
		t.Fatal(err)
	}
	var reports []error
	owner, _ := firstUseBook(t)
	a := &runtimeAsks{book: book, rights: owner.rights, scope: "asks-owner-program@1:runtime", display: "runtime.exe", report: func(err error) { reports = append(reports, err) }}
	if err := a.withdrawStale(); err != nil {
		t.Fatal(err)
	}
	startReports := reports
	a.report = func(err error) { t.Error(err) }
	return newRuntimeProtected(a, filepath.Join(t.TempDir(), protected.HelperFile), func(*identity.Peer) (identity.Process, error) {
		return identity.Process{PID: 7, StartTime: time.Unix(7, 0)}, nil
	}), book, startReports
}

// TestRuntimeWithoutItsEndpointsWithdrawsNothing: a second runtime started on
// a live runtime's state directory fails to own the endpoints and leaves the
// live runtime's pending system-access question alone (ASK-S1).
func TestRuntimeWithoutItsEndpointsWithdrawsNothing(t *testing.T) {
	options := gatedRuntime(t)
	book, err := asks.LoadApplicationBook(filepath.Join(options.stateDir, "asks", "application.json"))
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "reader.exe")
	live, outcome, err := book.AskApplicationAs("asks-owner-program@1:live", program, "system-access:live", asks.SystemSecurityLogKey,
		map[string]string{"code": "a1b2c3d4", "until": "later"}, listen.Seen{}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		t.Fatalf("%q %v", outcome, err)
	}
	// The second runtime is its own process, as a second start is: it runs
	// the whole composition, Book included, and fails only at host.Listen.
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "openabstractions.exe")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	second := exec.CommandContext(ctx, bin, "serve", "runtime", "--isolated", options.isolated, "--state-dir", options.stateDir)
	out, err := second.CombinedOutput()
	if err == nil {
		t.Fatalf("a second runtime on the same endpoints started:\n%s", out)
	}
	if !strings.Contains(string(out), "taken") && !strings.Contains(string(out), "in use") {
		t.Logf("second runtime output:\n%s", out)
	}
	record, found, err := book.ApplicationRecord(live.ID)
	if err != nil || !found || !record.Pending() {
		t.Fatalf("the live runtime's question was withdrawn by a runtime that failed to start: %+v %v %v", record, found, err)
	}
}

// TestProtectedOnceDoesNotSurviveARestart: a once answered on runtime A
// gives runtime B no ticket. The helper's claim on B is refused, and the
// next read on B asks a new question.
func TestProtectedOnceDoesNotSurviveARestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "application.json")
	request, err := logservice.EncodeProtectedSourceRequest(logservice.ProtectedSourceRequest{Source: logservice.ProtectedSourceID, MaxRecords: 8, MaxBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "reader.exe")
	a, book, _ := restartedRuntime(t, path)
	if _, err := a.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatal(err)
	}
	question := pendingSystemQuestion(t, book)
	if _, _, err := book.AnswerApplication(question.ID, "once"); err != nil {
		t.Fatal(err)
	}
	b, _, reports := restartedRuntime(t, path)
	if len(reports) != 0 {
		t.Fatalf("an answered question was withdrawn: %v", reports)
	}
	claim, err := json.Marshal(map[string]any{"version": 1, "kind": "claim", "operation": protected.OperationSecurityLogPage, "ticket": question.ID})
	if err != nil {
		t.Fatal(err)
	}
	var reply map[string]any
	if err := json.Unmarshal(b.broker.Exchange(ctx, &identity.Peer{}, claim), &reply); err != nil {
		t.Fatal(err)
	}
	if reply["refused"] == nil || reply["request"] != nil {
		t.Fatalf("runtime B handed over A's request: %v", reply)
	}
	if _, err := b.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("the next read on B: %v", err)
	}
	if fresh := pendingSystemQuestion(t, book); fresh.ID == question.ID {
		t.Fatal("B reused A's question")
	}
}

// TestProtectedQuestionDoesNotOutliveItsRuntime is ASK-S1 across a restart:
// the runtime's startup withdraws the previous runtime's pending
// system-access question, so its stale ticket can be neither answered nor
// claimed, and the next read asks a new question.
func TestProtectedQuestionDoesNotOutliveItsRuntime(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "application.json")
	start := func() (*runtimeProtected, *asks.Book, []error) {
		return restartedRuntime(t, path)
	}
	request, err := logservice.EncodeProtectedSourceRequest(logservice.ProtectedSourceRequest{Source: logservice.ProtectedSourceID, MaxRecords: 8, MaxBytes: 8192})
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "reader.exe")

	before, book, _ := start()
	if _, err := before.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatal(err)
	}
	stale := pendingSystemQuestion(t, book)

	after, book, reports := start()
	if len(reports) != 1 {
		t.Fatalf("startup reports %v, want one withdrawal", reports)
	}
	if record, found, err := book.ApplicationRecord(stale.ID); err != nil || found {
		t.Fatalf("the previous runtime's question survived the restart: %+v %v", record, err)
	}
	if _, outcome, err := book.AnswerApplication(stale.ID, "once"); err != nil || outcome == "answered" {
		t.Fatalf("the stale question was answered after the restart: %q %v", outcome, err)
	}
	if _, err := after.approve(ctx, stale.ID); err == nil {
		t.Fatal("the stale ticket was approved after the restart")
	}
	if _, err := after.readFor(ctx, program, noPublisher, request); !errors.Is(err, logservice.ErrOSAuthorizationRequired) {
		t.Fatalf("the next read: %v", err)
	}
	if fresh := pendingSystemQuestion(t, book); fresh.ID == stale.ID {
		t.Fatal("the next read reused the stale question")
	}
}

// TestProtectedPathExistsOnlyBesideATrustedHelper is FAC-H2's and FAC-H4's
// composition: no path off Windows, for an isolated or addressed runtime, or
// beside a helper this account can change.
func TestProtectedPathExistsOnlyBesideATrustedHelper(t *testing.T) {
	a, _ := firstUseBook(t)
	image := func() (string, error) { return filepath.Join(t.TempDir(), "openabstractions.exe"), nil }
	for name, options := range map[string]runtimeFlags{
		"default":   {},
		"isolated":  {isolated: "test"},
		"addressed": {endpoint: `\\.\pipe\x`},
	} {
		if p := composeProtected(options, a, image); p != nil {
			t.Fatalf("%s: a protected path beside an untrusted helper", name)
		}
	}
	if composeProtected(runtimeFlags{}, nil, image) != nil {
		t.Fatal("a protected path without a question book")
	}
	p, _, _, _ := protectedFixture(t)
	if p.Covers(logservice.ProtectedSourceID) {
		t.Fatal("covers before the broker serves")
	}
	var none *runtimeProtected
	if none.Covers(logservice.ProtectedSourceID) {
		t.Fatal("a nil path covers")
	}
	none.serve(context.Background(), func(error) {})()
}

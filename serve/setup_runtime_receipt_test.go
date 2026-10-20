package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func setupFrozenQuestion() asks.SetupQuestionFacts {
	return asks.SetupQuestionFacts{Targets: []asks.SetupTargetFact{{Name: "opencode", Title: "OpenCode", Program: setupTestApp, Use: "code"}}, Code: &asks.SetupCodeFact{Title: "Reviewed model", License: "Apache-2.0", State: "served", Ref: asks.SetupModelFact{Family: "reviewed-model", CatalogVersion: "reviewed-catalog"}}}
}
func setupSlots(t *testing.T, f asks.SetupQuestionFacts) map[string]string {
	t.Helper()
	v, err := asks.SetupFactSlots(f)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func setupBookFixture(t *testing.T) (*setupRequests, *applicationDirectory, *asks.Book) {
	t.Helper()
	dir := t.TempDir()
	d := setupDirectory(setupGrants())
	d.dir = dir
	raw, err := json.Marshal(d.descriptors)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "descriptors.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	marks, err := json.Marshal(d.unconfirmed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, selfRegisteredFile), marks, 0600); err != nil {
		t.Fatal(err)
	}
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	a := &runtimeAsks{book: book, scope: "setup-native", display: "runtime", rights: &runtimeRights{owner: setupTestAccount}}
	d.agentQuestions = &agentEnrollmentQuestions{a: a}
	q := &setupRequests{receiptPath: filepath.Join(dir, "receipt.json"), lastRead: time.Now()}
	check := setupFactsCheck(func(_ context.Context, _ string, m asks.SetupModelFact) error {
		if m.Family != "reviewed-model" || m.CatalogVersion != "reviewed-catalog" {
			return errors.New("changed model")
		}
		return nil
	})
	q.factsCheck.Store(&check)
	return q, d, book
}

func setupReport(q *setupRequests, d *applicationDirectory, receipt applications.SetupPresenterReceipt) applications.SetupStepReport {
	return q.report(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestPanel}, receipt, func() error { return nil })
}
func startSetupQuestion(t *testing.T, q *setupRequests, d *applicationDirectory, request string) applications.SetupStepReport {
	t.Helper()
	got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Request: request, Facts: setupSlots(t, setupFrozenQuestion())})
	if got.Outcome != applications.OutcomePage || got.Question == "" {
		t.Fatal(got)
	}
	return got
}
func setupReceiptKey(t *testing.T) string {
	t.Helper()
	key, err := applicationToken()
	if err != nil {
		t.Fatal(err)
	}
	return key
}
func answerSetupQuestion(t *testing.T, b *asks.Book, id, option string) asks.Record {
	t.Helper()
	r, out, err := b.AnswerApplicationIfPendingAuthorized(id, option, func() error { return nil })
	if err != nil || out != "answered" {
		t.Fatal(out, err)
	}
	return r
}

func TestSetupRuntimeCooldownReadsBookAnswerAcrossRestart(t *testing.T) {
	q, d, book := setupBookFixture(t)
	started := startSetupQuestion(t, q, d, "")
	record, found, err := book.ApplicationRecord(started.Question)
	if err != nil || !found || record.Key != asks.SetupModelsKey || !agentHex(record.About, 24) || !record.Pending() {
		t.Fatal(record, found, err)
	}
	answered := answerSetupQuestion(t, book, record.ID, "not_now")
	// Restart reads the durable Book answer; no presenter supplied a timestamp.
	now := answered.Answered.Add(23 * time.Hour)
	q = &setupRequests{receiptPath: q.receiptPath, lastRead: now, now: func() time.Time { return now }}
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	if got := q.request(context.Background(), d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || got.Reason != "" || q.pending != nil {
		t.Fatal(got, q.pending)
	}
	if _, out, err := book.AnswerApplicationIfPendingAuthorized(record.ID, "not_now", nil); err != nil || out != "conflict" {
		t.Fatal(out, err)
	}
	now = answered.Answered.Add(24*time.Hour + time.Second)
	q.lastRead = now
	if got := q.request(context.Background(), d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
		t.Fatal(got, q.pending)
	}
}

func TestSetupRuntimeSettingsFencesOldCompletionAndRequiresBookYes(t *testing.T) {
	q, d, book := setupBookFixture(t)
	first := startSetupQuestion(t, q, d, "")
	answerSetupQuestion(t, book, first.Question, "not_now")
	settings := startSetupQuestion(t, q, d, "")
	if settings.Question == first.Question {
		t.Fatal("Settings reused decline")
	}
	for _, id := range []string{first.Question, settings.Question} {
		if got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: id}); got.Outcome != applications.OutcomeConflict {
			t.Fatal(got)
		}
	}
	answerSetupQuestion(t, book, settings.Question, "setup")
	receipt := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: settings.Question}
	if got := setupReport(q, d, receipt); got.Outcome != applications.OutcomePage || got.Question != "" {
		t.Fatal(got)
	}
	q.lastRead = time.Now()
	q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "code")
	if q.pending == nil {
		t.Fatal("new app request absent")
	}
	pending := q.pending.ID
	if got := setupReport(q, d, receipt); got.Outcome != applications.OutcomePage || q.pending == nil || q.pending.ID != pending {
		t.Fatal(got, q.pending)
	}
	if applications.SetupReceiptOutcome(3).Known() {
		t.Fatal("decline is still a receipt")
	}
}

func TestSetupRuntimeNativeAndFactsRechecksBeforeQuestionAdmission(t *testing.T) {
	for _, kind := range []string{"app", "native-changed", "model-changed", "target-changed", "unknown-request"} {
		t.Run(kind, func(t *testing.T) {
			q, d, book := setupBookFixture(t)
			s := rights.Subject{Account: setupTestAccount, Program: setupTestPanel}
			receipt := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
			calls := 0
			recheck := func() error {
				calls++
				if kind == "native-changed" && calls > 1 {
					return errors.New("changed")
				}
				return nil
			}
			switch kind {
			case "app":
				s.Program = setupTestApp
			case "model-changed":
				f := setupFrozenQuestion()
				f.Code.Ref.Family = "other"
				receipt.Facts = setupSlots(t, f)
			case "target-changed":
				f := setupFrozenQuestion()
				f.Targets[0].Program = setupTestPanel
				receipt.Facts = setupSlots(t, f)
			case "unknown-request":
				receipt.Request = "unknown"
			}
			got := q.report(context.Background(), d, s, receipt, recheck)
			if got.Outcome == applications.OutcomePage {
				t.Fatal(got)
			}
			if kind == "unknown-request" && (got.Outcome != applications.OutcomeConflict || got.Reason != "request") {
				t.Fatal("unknown request refusal changed", got)
			}
			if _, err := os.Stat(q.receiptPath); !os.IsNotExist(err) {
				t.Fatal("refusal persisted presenter state", err)
			}
			records, _, err := book.ApplicationHistory()
			if err != nil || len(records) != 0 {
				t.Fatal("refusal admitted question", records, err)
			}
		})
	}
}

func TestSetupRuntimeQuestionIsNotResponderAnswerable(t *testing.T) {
	q, d, b := setupBookFixture(t)
	started := startSetupQuestion(t, q, d, "")
	r, _, _ := b.ApplicationRecord(started.Question)
	a := d.agentQuestions.a
	if _, err := a.authorizeAnswerer(context.Background(), answerer{subject: rights.Subject{Account: setupTestAccount, Program: setupTestApp}}, r, a.scope, "setup"); err == nil {
		t.Fatal("Setup became responder-answerable")
	}
}

func TestSetupRuntimeUnregisteredTargetStaysUnregistered(t *testing.T) {
	for _, revision := range []string{"", "stale-revision"} {
		t.Run(revision, func(t *testing.T) {
			q, d, b := setupBookFixture(t)
			before := maps.Clone(d.unconfirmed)
			snapshot := setupFrozenQuestion()
			snapshot.Targets[0].Name = "installed-only"
			snapshot.Targets[0].Revision = revision
			got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, snapshot)})
			if revision != "" {
				if got.Outcome != applications.OutcomeConflict {
					t.Fatal(got)
				}
				return
			}
			if got.Outcome != applications.OutcomePage {
				t.Fatal(got)
			}
			answerSetupQuestion(t, b, got.Question, "setup")
			if _, exists := d.descriptors["installed-only"]; exists {
				t.Fatal("Setup answer registered the app")
			}
			if !reflect.DeepEqual(d.unconfirmed, before) {
				t.Fatal("Setup answer invented a Keep", d.unconfirmed)
			}
		})
	}
}

func TestSetupRuntimeNativeTargetRequiresItsExactCurrentRevision(t *testing.T) {
	q, _, _ := setupBookFixture(t)
	d := registration1459Directory(t, t.TempDir())
	program := registration1459Program("setup-target.exe")
	revision := registration1459Publish(t, d, "native-target", program, "Target", "operator", "")
	snapshot := setupFrozenQuestion()
	snapshot.Targets[0] = asks.SetupTargetFact{Name: "native-target", Title: "Target", Program: program, Use: "code"}
	if err := q.validateFacts(context.Background(), d, snapshot); err == nil {
		t.Fatal("empty revision matched current native registration")
	}
	snapshot.Targets[0].Revision = revision
	if err := q.validateFacts(context.Background(), d, snapshot); err != nil {
		t.Fatal(err)
	}
	registration1459Publish(t, d, "native-target", program, "Changed target", "operator", revision)
	if err := q.validateFacts(context.Background(), d, snapshot); err == nil {
		t.Fatal("stale revision matched replaced native registration")
	}
}

func TestSetupRuntimeNewStartRetiresOnlyPriorPendingQuestion(t *testing.T) {
	q, d, b := setupBookFixture(t)
	first := startSetupQuestion(t, q, d, "")
	second := startSetupQuestion(t, q, d, "")
	if _, found, err := b.ApplicationRecord(first.Question); err != nil || found {
		t.Fatal("superseded pending question remains", found, err)
	}
	answerSetupQuestion(t, b, second.Question, "not_now")
	startSetupQuestion(t, q, d, "")
	if r, found, err := b.ApplicationRecord(second.Question); err != nil || !found || r.Option != "not_now" {
		t.Fatal("answered question was retired", r, found, err)
	}
}

func TestSetupRuntimeRequestCoalescesForBookPendingLifetime(t *testing.T) {
	q, d, book := setupBookFixture(t)
	started := startSetupQuestion(t, q, d, "")
	record, found, err := book.ApplicationRecord(started.Question)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	now := record.Asked.Add(2 * time.Minute)
	q.now = func() time.Time { return now }
	q.lastRead = now
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	if got := q.request(context.Background(), d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending != nil {
		t.Fatal("repeat after sixty seconds restarted the pending question", got, q.pending)
	}
	if retained, found, err := book.ApplicationRecord(started.Question); err != nil || !found || retained.ID != record.ID || !retained.Pending() {
		t.Fatal("pending occurrence changed", retained, found, err)
	}
	now = book.PendingUntil(record).Add(time.Second)
	q.lastRead = now
	if got := q.request(context.Background(), d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
		t.Fatal("Book expiry did not release request coalescing", got, q.pending)
	}
}

func TestSetupRuntimeStartedLostReplyReplaysBookOccurrence(t *testing.T) {
	q, d, book := setupBookFixture(t)
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	if got := q.request(context.Background(), d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
		t.Fatal(got)
	}
	request := *q.pending
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	q.read(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestPanel}, request.ID)
	facts := setupFrozenQuestion()
	facts.Requester, facts.RequestUse = request.Application, request.Use
	receipt := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Request: request.ID, Facts: setupSlots(t, facts)}
	first := setupReport(q, d, receipt) // Model a successful admission whose reply was lost.
	if first.Outcome != applications.OutcomePage {
		t.Fatal(first)
	}
	before, err := os.ReadFile(q.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	// Restart has neither the pending request nor lastRequest in memory.
	check := q.factsCheck.Load()
	q = &setupRequests{receiptPath: q.receiptPath}
	q.factsCheck.Store(check)
	replayed := setupReport(q, d, receipt)
	if replayed.Outcome != applications.OutcomePage || replayed.Question != first.Question {
		t.Fatal("lost started reply minted another question", first, replayed)
	}
	after, err := os.ReadFile(q.receiptPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("replay changed the presenter receipt", err)
	}
	for _, change := range []string{"request", "facts"} {
		changed := receipt
		if change == "request" {
			changed.Request = "another-request"
		} else {
			changedFacts := facts
			code := *facts.Code
			code.Title = "Changed display plan"
			changedFacts.Code = &code
			changed.Facts = setupSlots(t, changedFacts)
		}
		if got := setupReport(q, d, changed); got.Outcome != applications.OutcomeConflict {
			t.Fatal("changed replay admitted", change, got)
		}
	}
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 || records[0].ID != first.Question || !records[0].Pending() {
		t.Fatal("lost reply changed Book custody", records, err)
	}
}

func TestSetupRuntimeStartedReplayRepairsBookBeforeReceiptCommit(t *testing.T) {
	q, d, book := setupBookFixture(t)
	prior := startSetupQuestion(t, q, d, "")
	receipt := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	interrupted := false
	got := q.report(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestPanel}, receipt, func() error {
		r, _, err := book.ObserveApplication(d.agentQuestions.a.scope, "setup:"+receipt.Key)
		if err != nil {
			return err
		}
		if r.ID != "" {
			// This is the actual independent Book write. Cancellation prevents
			// the enclosing receipt CAS from replacing the prior receipt.
			interrupted = true
			cancel()
		}
		return nil
	})
	if got.Outcome != applications.OutcomeUnavailable || !interrupted {
		t.Fatal("fault did not interrupt receipt commit after Book admission", got, interrupted)
	}
	admitted, _, err := book.ObserveApplication(d.agentQuestions.a.scope, "setup:"+receipt.Key)
	if err != nil || admitted.ID == "" || admitted.ID == prior.Question {
		t.Fatal("Book admission absent after fault", admitted, err)
	}
	raw, err := os.ReadFile(q.receiptPath)
	saved, decodeErr := decodeSetupRuntimeReceipt(raw)
	if err != nil || decodeErr != nil || saved == nil || saved.Occurrence != prior.Question || saved.Admission == nil || saved.Admission.Key != receipt.Key {
		t.Fatal("fault lost prior receipt or new admission identity", saved, err, decodeErr)
	}
	newer := receipt
	newer.Key = setupReceiptKey(t)
	if got := setupReport(q, d, newer); got.Outcome != applications.OutcomeConflict || got.Reason != "occurrence" {
		t.Fatal("a distinct start displaced an unfinished accepted admission", got)
	}
	if attempted, status, err := book.ObserveApplication(d.agentQuestions.a.scope, "setup:"+newer.Key); err != nil || attempted.ID != "" || status != "unknown" {
		t.Fatal("refused newer attempt admitted a Book question", attempted, status, err)
	}
	retained, err := os.ReadFile(q.receiptPath)
	if err != nil || string(retained) != string(raw) {
		t.Fatal("failed newer attempt discarded recoverable admission identity", err)
	}
	now := admitted.Asked.Add(2 * time.Minute)
	q.now = func() time.Time { return now }
	q.mu.Lock()
	suppressed, suppressErr := q.suppressRequestLocked(context.Background(), d)
	q.mu.Unlock()
	if suppressErr != nil || !suppressed {
		t.Fatal("split admission failed to coalesce for its pending Book lifetime", suppressed, suppressErr)
	}
	now = book.PendingUntil(admitted).Add(time.Second)
	q.mu.Lock()
	suppressed, suppressErr = q.suppressRequestLocked(context.Background(), d)
	q.mu.Unlock()
	if suppressErr != nil || suppressed {
		t.Fatal("split admission suppressed beyond its Book expiry", suppressed, suppressErr)
	}
	// Recovery has no process-local pending request or start history.
	check := q.factsCheck.Load()
	q = &setupRequests{receiptPath: q.receiptPath}
	q.factsCheck.Store(check)
	replayed := setupReport(q, d, receipt)
	if replayed.Outcome != applications.OutcomePage || replayed.Question != admitted.ID {
		t.Fatal("same-key replay failed to repair split persistence", replayed, admitted)
	}
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 || records[0].ID != admitted.ID {
		t.Fatal("recovery duplicated the occurrence or retained superseded pending state", records, err)
	}
	answerSetupQuestion(t, book, admitted.ID, "setup")
	completion := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: admitted.ID}
	if got := setupReport(q, d, completion); got.Outcome != applications.OutcomePage {
		t.Fatal("recovered occurrence could not complete", got)
	}
	before, err := os.ReadFile(q.receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := setupReport(q, d, completion); got.Outcome != applications.OutcomePage {
		t.Fatal("completion replay failed", got)
	}
	after, err := os.ReadFile(q.receiptPath)
	if err != nil || string(before) != string(after) {
		t.Fatal("completion replay changed retained receipt", err)
	}
}

func TestSetupRuntimePreBookInterruptionDoesNotSuppressRequestsAfterRestart(t *testing.T) {
	q, d, book := setupBookFixture(t)
	receipt := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	checks := 0
	got := q.report(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestPanel}, receipt, func() error {
		checks++
		if checks == 3 {
			// The first receipt CAS committed the identity. Interrupt the next
			// CAS before its Book admission, leaving no pending question.
			cancel()
		}
		return nil
	})
	if got.Outcome != applications.OutcomeUnavailable || checks < 3 {
		t.Fatal("pre-Book interruption was not exercised", got, checks)
	}
	raw, err := os.ReadFile(q.receiptPath)
	saved, decodeErr := decodeSetupRuntimeReceipt(raw)
	if err != nil || decodeErr != nil || saved == nil || saved.Admission == nil || saved.Admission.Key != receipt.Key {
		t.Fatal("pre-Book fault did not retain its admission marker", saved, err, decodeErr)
	}
	if attempted, status, err := book.ObserveApplication(d.agentQuestions.a.scope, "setup:"+receipt.Key); err != nil || attempted.ID != "" || status != "unknown" {
		t.Fatal("fault occurred after Book admission", attempted, status, err)
	}
	check := q.factsCheck.Load()
	q = &setupRequests{receiptPath: q.receiptPath, lastRead: time.Now()}
	q.factsCheck.Store(check)
	if got := q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
		t.Fatal("no-Book marker suppressed an ordinary request after restart", got, q.pending)
	}
	if records, _, err := book.ApplicationHistory(); err != nil || len(records) != 0 {
		t.Fatal("ordinary request fabricated a question", records, err)
	}
}

func TestSetupRuntimeOlderStartedReplayPreservesNewerReceipt(t *testing.T) {
	for _, answered := range []bool{false, true} {
		t.Run(fmt.Sprintf("answered-%v", answered), func(t *testing.T) {
			q, d, book := setupBookFixture(t)
			old := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
			first := setupReport(q, d, old)
			if first.Outcome != applications.OutcomePage {
				t.Fatal(first)
			}
			if answered {
				answerSetupQuestion(t, book, first.Question, "setup")
			}
			current := startSetupQuestion(t, q, d, "")
			before, err := os.ReadFile(q.receiptPath)
			if err != nil {
				t.Fatal(err)
			}
			got := setupReport(q, d, old)
			if answered && (got.Outcome != applications.OutcomePage || got.Question != first.Question) {
				t.Fatal("retained older occurrence did not replay", got)
			}
			if !answered && got.Outcome == applications.OutcomePage {
				t.Fatal("retired occurrence became current", got)
			}
			after, err := os.ReadFile(q.receiptPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("older replay replaced newer receipt", current, got, err)
			}
			answerSetupQuestion(t, book, current.Question, "setup")
			if got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: current.Question}); got.Outcome != applications.OutcomePage {
				t.Fatal("older replay disrupted current completion", got)
			}
		})
	}
}

// The four interruption cuts surround the existing receipt and Book writes.
// The callback reads fixture bytes directly so a native recheck invoked under
// the Book's retirement lock never recursively acquires that lock.
func setupInterruptStartAt(t *testing.T, q *setupRequests, d *applicationDirectory, receipt applications.SetupPresenterReceipt, cut string) applications.SetupStepReport {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fired := false
	got := q.report(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestPanel}, receipt, func() error {
		var saved *setupRuntimeReceipt
		raw, err := os.ReadFile(q.receiptPath)
		if err == nil {
			saved, err = decodeSetupRuntimeReceipt(raw)
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		var state struct {
			Admissions []struct{ Key, ID string }
		}
		raw, err = os.ReadFile(filepath.Join(d.dir, "questions.json"))
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return err
		}
		bookID := ""
		for _, admission := range state.Admissions {
			if admission.Key == "setup:"+receipt.Key {
				bookID = admission.ID
			}
		}
		marked := saved != nil && saved.Admission != nil && saved.Admission.Key == receipt.Key
		committed := saved != nil && bookID != "" && saved.Occurrence == bookID && saved.Admission == nil
		interrupt := cut == "before-marker" && !marked && bookID == "" ||
			cut == "marker-before-book" && marked && bookID == "" ||
			cut == "book-before-receipt" && bookID != "" && !committed ||
			cut == "receipt-before-reply" && committed
		if interrupt {
			fired = true
			cancel()
		}
		return nil
	})
	if !fired || got.Outcome == applications.OutcomePage {
		t.Fatal("interruption cut was not exercised", cut, fired, got)
	}
	return got
}

func TestSetupRuntimeStartInterruptionMatrixAndDifferentKey(t *testing.T) {
	for _, cut := range []string{"before-marker", "marker-before-book", "book-before-receipt", "receipt-before-reply"} {
		t.Run(cut, func(t *testing.T) {
			q, d, book := setupBookFixture(t)
			startSetupQuestion(t, q, d, "")
			original := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
			setupInterruptStartAt(t, q, d, original, cut)
			accepted, _, err := book.ObserveApplication(d.agentQuestions.a.scope, "setup:"+original.Key)
			if err != nil || (accepted.ID != "") != (cut == "book-before-receipt" || cut == "receipt-before-reply") {
				t.Fatal("unexpected Book custody at interruption", accepted, err)
			}
			check := q.factsCheck.Load()
			q = &setupRequests{receiptPath: q.receiptPath, lastRead: time.Now()}
			q.factsCheck.Store(check)
			if got := q.request(context.Background(), d, rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending != nil {
				t.Fatal("interruption bypassed the existing pending Book occurrence", got, q.pending)
			}
			different := original
			different.Key = setupReceiptKey(t)
			current := setupReport(q, d, different)
			if cut == "book-before-receipt" {
				if current.Outcome != applications.OutcomeConflict {
					t.Fatal("different key displaced recoverable accepted start", current)
				}
				recovered := setupReport(q, d, original)
				if recovered.Outcome != applications.OutcomePage || recovered.Question != accepted.ID {
					t.Fatal("original key did not reconcile after newer refusal", recovered)
				}
				answerSetupQuestion(t, book, recovered.Question, "setup")
				if got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: recovered.Question}); got.Outcome != applications.OutcomePage {
					t.Fatal("recovered start could not complete", got)
				}
				current = setupReport(q, d, different)
			}
			if current.Outcome != applications.OutcomePage || current.Question == "" {
				t.Fatal("different key stayed blocked after completed or unaccepted attempt", current)
			}
			if records, _, err := book.ApplicationHistory(); err != nil {
				t.Fatal(err)
			} else {
				pending := 0
				for _, record := range records {
					if record.Pending() {
						pending++
					}
				}
				if pending != 1 {
					t.Fatal("interruption left orphaned pending questions", records)
				}
			}
			answerSetupQuestion(t, book, current.Question, "setup")
			if got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: current.Question}); got.Outcome != applications.OutcomePage {
				t.Fatal("current start did not complete", got)
			}
		})
	}
}

func TestSetupRuntimeLostPresenterDoesNotRequireItsKeyAfterAnswerOrExpiry(t *testing.T) {
	for _, terminal := range []string{"setup", "not_now", "expired"} {
		t.Run(terminal, func(t *testing.T) {
			q, d, book := setupBookFixture(t)
			startSetupQuestion(t, q, d, "")
			lost := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
			setupInterruptStartAt(t, q, d, lost, "book-before-receipt")
			accepted, _, err := book.ObserveApplication(d.agentQuestions.a.scope, "setup:"+lost.Key)
			if err != nil || accepted.ID == "" {
				t.Fatal(accepted, err)
			}
			if terminal == "expired" {
				if err := book.SetPendingLifetime(time.Nanosecond); err != nil {
					t.Fatal(err)
				}
				if _, found, err := book.ApplicationRecord(accepted.ID); err != nil || found {
					t.Fatal("Book did not expire the lost presenter's question", found, err)
				}
			} else {
				answerSetupQuestion(t, book, accepted.ID, terminal)
			}
			check := q.factsCheck.Load()
			q = &setupRequests{receiptPath: q.receiptPath}
			q.factsCheck.Store(check)
			// A new native Settings session knows none of the lost client's key.
			var current applications.SetupStepReport
			if terminal == "expired" {
				current = startSetupQuestion(t, q, d, "")
			} else {
				fresh := applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Facts: setupSlots(t, setupFrozenQuestion())}
				setupInterruptStartAt(t, q, d, fresh, "marker-before-book")
				raw, err := os.ReadFile(q.receiptPath)
				saved, decodeErr := decodeSetupRuntimeReceipt(raw)
				if err != nil || decodeErr != nil || saved == nil || saved.Occurrence != accepted.ID {
					t.Fatal("failed newer start lost terminal Book reconciliation", saved, err, decodeErr)
				}
				q = &setupRequests{receiptPath: q.receiptPath}
				q.factsCheck.Store(check)
				completion := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: accepted.ID})
				if terminal == "setup" && completion.Outcome != applications.OutcomePage || terminal == "not_now" && completion.Outcome != applications.OutcomeConflict {
					t.Fatal("failed newer start changed terminal completion authority", completion)
				}
				current = setupReport(q, d, fresh)
				if current.Outcome != applications.OutcomePage {
					t.Fatal("fresh session required the lost presenter's key", current)
				}
			}
			if current.Question == accepted.ID {
				t.Fatal("fresh session reused the terminal lost question", current)
			}
			if err := book.SetPendingLifetime(asks.DefaultPendingLifetime); err != nil {
				t.Fatal(err)
			}
			answerSetupQuestion(t, book, current.Question, "setup")
			if got := setupReport(q, d, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: current.Question}); got.Outcome != applications.OutcomePage {
				t.Fatal("fresh session required the lost presenter's key", got)
			}
		})
	}
}

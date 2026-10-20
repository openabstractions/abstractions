package main

import (
	"context"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type setupRequestWireHandler struct {
	applications.Applications
	q       *setupRequests
	d       *applicationDirectory
	ctx     context.Context
	subject rights.Subject
}

func (h *setupRequestWireHandler) RequestSetup(use string) (applications.SetupResult, error) {
	return h.q.request(h.ctx, h.d, h.subject, use), nil
}

func (h *setupRequestWireHandler) ReceiveSetupRequest(after string) (applications.SetupRequestRead, error) {
	return h.q.read(h.ctx, h.d, h.subject, after), nil
}
func (h *setupRequestWireHandler) ReportSetupStep(receipt applications.SetupPresenterReceipt) (applications.SetupStepReport, error) {
	return h.q.report(h.ctx, h.d, h.subject, receipt, func() error { return nil }), nil
}

func TestReceiveSetupRequestWireAcknowledgesUnderOperatorAuthority(t *testing.T) {
	q, d, book := setupBookFixture(t)
	h := &setupRequestWireHandler{q: q, d: d, ctx: context.Background(), subject: rights.Subject{Account: setupTestAccount, Program: setupTestApp}}
	dispatch := &applications.ApplicationsDispatcher{Handler: h}
	client := applications.NewApplicationsClient(dispatch)
	if got, err := client.RequestSetup("code"); err != nil || got.Outcome != applications.SetupOutcomeAsked {
		t.Fatal("app request", got, err)
	}
	if got, err := client.ReceiveSetupRequest(""); err != nil || got.Outcome != applications.OutcomeForbidden || got.Request != nil {
		t.Fatal("app received operator request", got, err)
	}
	h.subject.Program = setupTestPanel
	got, err := client.ReceiveSetupRequest("")
	if err != nil || got.Outcome != applications.OutcomePage || got.Request == nil {
		t.Fatal("presenter receive", got, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	h.ctx = ctx
	if _, err := client.ReceiveSetupRequest(got.Request.ID); err != nil || q.pending != nil {
		t.Fatal("acknowledgement did not clear request", err, q.pending)
	}
	h.ctx = context.Background()
	started, err := client.ReportSetupStep(applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: setupReceiptKey(t), Request: got.Request.ID, Facts: setupSlots(t, func() asks.SetupQuestionFacts {
		f := setupFrozenQuestion()
		f.Requester = got.Request.Application
		f.RequestUse = got.Request.Use
		return f
	}())})
	if err != nil || started.Outcome != applications.OutcomePage || started.Question == "" {
		t.Fatal("native question start", started, err)
	}
	answerSetupQuestion(t, book, started.Question, "not_now")
	h.subject.Program = setupTestApp
	if got, err := client.RequestSetup("code"); err != nil || got.Outcome != applications.SetupOutcomeAsked || got.Reason != "" || q.pending != nil {
		t.Fatal("app request repeated declined question", got, err)
	}
	if got, err := client.ReportSetupStep(applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: started.Question}); err != nil || got.Outcome != applications.OutcomeForbidden {
		t.Fatal("app supplied presenter outcome", got, err)
	}
	for _, method := range []string{"ReadSetupRequest", "SetEnabled"} {
		reply, err := dispatch.ExchangeFrame([]byte(`{"version":1,"service":"abstraction.facade/applications@3","method":"` + method + `","arguments":{}}`))
		if err != nil || !strings.Contains(string(reply), "unknown_method") {
			t.Fatal("unpublished predecessor method still admitted", method, string(reply), err)
		}
	}
}

// The fixture programs are absolute on the platform the tests run on
// (setup_test_paths_*_test.go); identity compares only absolute programs.
const setupTestAccount = "S-1-5-21-setup"

// setupDirectory is a directory where OpenCode is a confirmed registration,
// the Panel is the operator, and grants says which (program, action) pairs
// are allowed.
func setupDirectory(grants map[string]bool) *applicationDirectory {
	return &applicationDirectory{owner: setupTestAccount,
		descriptors: map[string]wire.ApplicationDescriptor{
			"opencode": {Name: "opencode", Program: setupTestApp, Title: "OpenCode"},
			"other":    {Name: "other", Program: setupTestOther, Title: "An app"}},
		unconfirmed: map[string]selfMark{
			"opencode": setupKeptHelper(setupTestApp), "other": setupKeptHelper(setupTestOther)},
		operator: func(program string) bool { return program == setupTestPanel },
		decide: func(_ context.Context, s rights.Subject, action, resource string) (bool, error) {
			return grants[s.Program+" "+action+" "+resource], nil
		},
	}
}

func setupGrants() map[string]bool {
	return map[string]bool{
		setupTestApp + " " + rightspolicy.ActionQuestionAsk + " account":   true,
		setupTestPanel + " " + ActionApplicationManage + " account":        true,
		setupTestOther + " " + rightspolicy.ActionQuestionAsk + " account": true,
	}
}

// An app with the ask right gets one pending request; a second call while
// it is pending answers asked and keeps the first. The presenter reads the
// use and the app's display name only, and clears the request once it names
// it as started.
func TestRequestSetupKeepsOnePendingRequest(t *testing.T) {
	ctx := context.Background()
	d := setupDirectory(setupGrants())
	q := &setupRequests{lastRead: time.Now()}
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	if got := q.request(ctx, d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || got.Reason != "" {
		t.Fatalf("first request = %+v", got)
	}
	first := *q.pending
	if got := q.request(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestOther}, "image"); got.Outcome != applications.SetupOutcomeAsked || got.Reason != "pending" {
		t.Fatalf("second request = %+v", got)
	}
	panel := rights.Subject{Account: setupTestAccount, Program: setupTestPanel}
	read := q.read(ctx, d, panel, "")
	if read.Outcome != applications.OutcomePage || read.Request == nil || *read.Request != first || read.Request.Use != "code" || read.Request.Application != "OpenCode" {
		t.Fatalf("read = %+v", read)
	}
	// Clearing receive leaves the request in flight until started is reported.
	readCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	q.read(readCtx, d, panel, first.ID)
	if q.pending != nil || q.lastRequest == nil || q.lastRequest.ID != first.ID {
		t.Fatal("clearing receive lost the in-flight request", q.pending, q.lastRequest)
	}
	if got := q.request(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestOther}, "image"); got.Outcome != applications.SetupOutcomeAsked || q.pending != nil || q.lastRequest.ID != first.ID {
		t.Fatal("repeat in clearing-read/start gap admitted another request", got, q.pending)
	}
}

// Refusals: a caller with no proven local subject has no person reachable,
// an unknown use is invalid, a program without the ask right is forbidden,
// and only the operator reads the request.
func TestRequestSetupRefusals(t *testing.T) {
	ctx := context.Background()
	d := setupDirectory(setupGrants())
	q := &setupRequests{}
	if got := q.request(ctx, d, rights.Subject{}, "code"); got.Outcome != applications.SetupOutcomeForbidden || got.Reason != "no person reachable on this runtime" {
		t.Fatalf("remote = %+v", got)
	}
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	if got := q.request(ctx, d, app, "everything"); got.Outcome != applications.SetupOutcomeInvalid {
		t.Fatalf("unknown use = %+v", got)
	}
	if got := q.request(ctx, d, rights.Subject{Account: setupTestAccount, Program: setupTestNoAsk}, "code"); got.Outcome != applications.SetupOutcomeForbidden {
		t.Fatalf("no ask right = %+v", got)
	}
	if q.pending != nil {
		t.Fatal("a refused call kept a request")
	}
	if read := q.read(ctx, d, app, ""); read.Outcome != applications.OutcomeForbidden || read.Request != nil {
		t.Fatalf("an app read the request: %+v", read)
	}
}

// A request no presenter reads expires; the next call keeps a new one.
func TestRequestSetupExpires(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	q := &setupRequests{now: func() time.Time { return now }, lastRead: now}
	d := setupDirectory(setupGrants())
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	q.request(ctx, d, app, "code")
	first := q.pending.ID
	now = now.Add(setupRequestLifetime + time.Second)
	q.lastRead = now
	if got := q.request(ctx, d, app, "code"); got.Reason != "" || q.pending.ID == first {
		t.Fatalf("expired request kept: %+v", got)
	}
}

// The runtime's own reads answer cannot_run and already_set_up before any
// request is kept; anything else is the person's to answer.
func TestRequestSetupCheckAnswersFromFitAndTheChatDefault(t *testing.T) {
	ctx := context.Background()
	entries := []modelwire.FitEntry{
		{Title: "Paired big", Family: "big", Fit: modelwire.FitNo, Availability: modelwire.AvailabilityServed, Machine: "studio", NeedBytes: 70e9},
		{Title: "Qwen3.6 35B", Family: "qwen3.6-35b-a3b", Fit: modelwire.FitFits, Availability: modelwire.AvailabilityDownload, NeedBytes: 25e9},
	}
	pool := int64(30e9)
	fit := func(context.Context, modelwire.CatalogUse) modelwire.FitResult {
		return modelwire.FitResult{Outcome: modelwire.FitOutcomeFitted, PoolBytes: pool, Entries: entries}
	}
	selected := iwire.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Policy: &iwire.SelectionPolicyV2{Name: "chat",
		Candidates: []iwire.SelectionCandidateV2{{Model: "qwen3.6-35b-a3b", Placement: iwire.CandidatePlacementLocal}}}}
	chat := func(context.Context, string) iwire.SelectionReadV2 { return selected }
	check := runtimeSetupCheck(fit, chat)
	if _, _, ok := check(ctx, setupTestAccount, "code"); ok {
		t.Fatal("a pick that needs a download answered without the person")
	}
	entries[1].Availability = modelwire.AvailabilityServed
	if outcome, reason, ok := check(ctx, setupTestAccount, "code"); !ok || outcome != applications.SetupOutcomeAlreadySetUp || reason != "Qwen3.6 35B answers apps on this computer." {
		t.Fatalf("served and selected = %v %q %v", outcome, reason, ok)
	}
	selected.Policy.Candidates[0].Model = "another"
	if _, _, ok := check(ctx, setupTestAccount, "code"); ok {
		t.Fatal("a served pick the chat default does not select answered without the person")
	}
	entries[1].Availability, entries[1].Fit = modelwire.AvailabilityDownload, modelwire.FitNo
	// Fit now reports the lease book's available amount, including a main
	// model reservation whose bytes the instrument has not measured yet.
	pool = 10e9
	if outcome, reason, ok := check(ctx, setupTestAccount, "code"); !ok || outcome != applications.SetupOutcomeCannotRun || reason != "This computer has 10 GB free for models; Qwen3.6 35B needs 24 GB." {
		t.Fatalf("nothing fits = %v %q %v", outcome, reason, ok)
	}
	if _, _, ok := check(ctx, setupTestAccount, "image"); ok {
		t.Fatal("the picture use answered without the person")
	}
	// Bound to the request, cannot_run keeps no request.
	q := &setupRequests{}
	q.bind(check)
	if got := q.request(ctx, setupDirectory(setupGrants()), rights.Subject{Account: setupTestAccount, Program: setupTestApp}, "code"); got.Outcome != applications.SetupOutcomeCannotRun || q.pending != nil {
		t.Fatalf("bound check = %+v pending %v", got, q.pending)
	}
}

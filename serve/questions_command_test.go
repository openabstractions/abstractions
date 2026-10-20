package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
)

type questionOperatorStub struct {
	list   func(string, int64) (askswire.OperatorPage, error)
	answer func(string, string) (askswire.OperatorDecision, error)
}

func (s questionOperatorStub) ListQuestionsContext(_ context.Context, cursor string, limit int64) (askswire.OperatorPage, error) {
	return s.list(cursor, limit)
}

func (s questionOperatorStub) AnswerQuestionContext(_ context.Context, id, option string) (askswire.OperatorDecision, error) {
	return s.answer(id, option)
}

type askerOperatorStub struct {
	questionOperatorStub
	programs map[string]string
}

func (s askerOperatorStub) ReadQuestionAskerContext(_ context.Context, id string) (askclient.QuestionAsker, error) {
	program, ok := s.programs[id]
	if !ok {
		return askclient.QuestionAsker{}, &askswire.ServiceError{Code: askswire.ServiceErrorCodeUnknownMethod}
	}
	return askclient.QuestionAsker{Program: program}, nil
}

// TestQuestionsPendingNamesEachBoundProgram: two programs with one file name
// list apart by the program each record keeps (asks ASK-A2, ASK-O11); a
// record without one shows its display name.
func TestQuestionsPendingNamesEachBoundProgram(t *testing.T) {
	records := []askswire.RecordMetadata{
		{ID: "q1", Asker: "opencode.exe", Text: "First?", Options: []string{"once"}},
		{ID: "q2", Asker: "opencode.exe", Text: "Second?", Options: []string{"once"}},
		{ID: "q3", Asker: "serve.exe", Text: "Third?", Options: []string{"once"}},
	}
	first, second := `C:\one\opencode.exe`, `C:\two\opencode.exe`
	operator := askerOperatorStub{programs: map[string]string{"q1": first, "q2": second}}
	askers := questionAskers(context.Background(), operator, records)
	if len(askers) != 2 || askers["q1"] != first || askers["q2"] != second {
		t.Fatalf("askers %+v", askers)
	}
	var out bytes.Buffer
	if err := printPendingQuestions(&out, records, askers, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{first, second, "serve.exe"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("listing %q lacks %q", out.String(), want)
		}
	}
	if questionAskers(context.Background(), questionOperatorStub{}, records) != nil {
		t.Fatal("an operator without the asker read must give no programs")
	}
}

func TestQuestionsPendingPaginatesAndDiscardsChangingList(t *testing.T) {
	first := askswire.RecordMetadata{ID: "q1", Text: "Replace canvas?", Options: []string{"once", "refuse"}}
	answered := askswire.RecordMetadata{ID: "q2", Option: "once"}
	second := askswire.RecordMetadata{ID: "q3", Text: "Another question?"}
	operator := questionOperatorStub{list: func(cursor string, limit int64) (askswire.OperatorPage, error) {
		if limit != 64 {
			t.Fatalf("limit %d", limit)
		}
		switch cursor {
		case "":
			return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Records: []askswire.RecordMetadata{first, answered}, Next: "next"}, nil
		case "next":
			return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Records: []askswire.RecordMetadata{second}, Complete: true}, nil
		default:
			t.Fatalf("unexpected cursor %q", cursor)
			return askswire.OperatorPage{}, nil
		}
	}}
	got, err := listPendingQuestions(context.Background(), operator)
	if err != nil || len(got) != 2 || got[0].ID != "q1" || got[1].ID != "q3" {
		t.Fatalf("pending %+v, %v", got, err)
	}
	var out bytes.Buffer
	if err := printPendingQuestions(&out, got, nil, false); err != nil || !strings.Contains(out.String(), "q1") || strings.Contains(out.String(), "q2") {
		t.Fatalf("printed %q, %v", out.String(), err)
	}
	operator.list = func(cursor string, _ int64) (askswire.OperatorPage, error) {
		if cursor == "" {
			return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomePage, Records: []askswire.RecordMetadata{first}, Next: "next"}, nil
		}
		return askswire.OperatorPage{Outcome: askswire.OperatorPageOutcomeGap}, nil
	}
	got, err = listPendingQuestions(context.Background(), operator)
	if len(got) != 0 || err == nil || !strings.Contains(err.Error(), "run pending again") {
		t.Fatalf("gap leaked %+v, %v", got, err)
	}
}

func TestQuestionsAnswerPreservesDecisionAndUncertainReply(t *testing.T) {
	operator := questionOperatorStub{answer: func(id, option string) (askswire.OperatorDecision, error) {
		return askswire.OperatorDecision{Outcome: askswire.OperatorDecisionOutcomeAnswered,
			Record: &askswire.RecordMetadata{ID: id, Option: option}}, nil
	}}
	decision, err := answerQuestion(context.Background(), operator, "q1", "once")
	if err != nil || decision.Record == nil || decision.Record.Option != "once" {
		t.Fatalf("answer %+v, %v", decision, err)
	}
	operator.answer = func(_, _ string) (askswire.OperatorDecision, error) {
		return askswire.OperatorDecision{}, errors.New("connection ended")
	}
	_, err = answerQuestion(context.Background(), operator, "q1", "once")
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitUncertain || !strings.Contains(err.Error(), "repeat this same id and option") {
		t.Fatalf("uncertain answer %v", err)
	}
	operator.answer = func(_, _ string) (askswire.OperatorDecision, error) {
		return askswire.OperatorDecision{Outcome: askswire.OperatorDecisionOutcomeConflict}, nil
	}
	_, err = answerQuestion(context.Background(), operator, "q1", "refuse")
	if !errors.As(err, &exit) || exit.code != exitRefusedCall {
		t.Fatalf("conflicting answer %v", err)
	}
}

func TestQuestionsRejectsMalformedAnswerBeforeResolution(t *testing.T) {
	for _, args := range [][]string{{"answer"}, {"answer", "id", "once", "extra"}, {"answer", "id\n", "once"}, {"pending", "extra"}} {
		var output, diagnostics bytes.Buffer
		err := questionsCommand(args, &output, &diagnostics)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitUsage {
			t.Fatalf("%v: got %v", args, err)
		}
	}
}

func TestQuestionsFirstUseAnswerStaysWithRuleWritingPanel(t *testing.T) {
	records := []askswire.RecordMetadata{{ID: "first", Key: asks.FirstUseKey}, {ID: "replace", Key: "content.replace"}}
	if err := answerableQuestion(records, "replace", "once"); err != nil {
		t.Fatalf("ordinary application question: %v", err)
	}
	err := answerableQuestion(records, "first", "allow")
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "Panel") {
		t.Fatalf("first-use answer %v", err)
	}
}

// TestQuestionsSystemAccessOnceStaysWithThePrompt is ASK-S1 on the command
// line: refuse and never may be answered here; once only in the native
// prompt that raises the administrator prompt next.
func TestQuestionsSystemAccessOnceStaysWithThePrompt(t *testing.T) {
	records := []askswire.RecordMetadata{{ID: "system", Key: asks.SystemSecurityLogKey}}
	err := answerableQuestion(records, "system", "once")
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "prompt") {
		t.Fatalf("system-access once on the command line: %v", err)
	}
	for _, option := range []string{"refuse", "never"} {
		if err := answerableQuestion(records, "system", option); err != nil {
			t.Fatalf("%s: %v", option, err)
		}
	}
}

func TestQuestionsCommandAnswersThroughRuntimeOperator(t *testing.T) {
	options := gatedRuntime(t)
	if reply, err := runRightsCommand(t, "grant", "--endpoint", options.endpoint,
		"--program", probeSelf().Program, "--action", host.QuestionAskAction,
		"--resource", host.QuestionAskResource); err != nil || reply.Outcome != "applied" {
		t.Fatalf("grant question.ask: %+v %v", reply, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	app, err := client.NewVerified(options.endpoint, runtimeExpectation(t)).ResolveAsks(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	question := askclient.ApplicationQuestion{RequestKey: "questions-cli-integration", Key: "download.reach",
		Slots: map[string]string{"host": "example.com"}}
	admitted, err := app.AskContext(ctx, question)
	if err != nil || admitted.Outcome.String() != "pending" || admitted.Answer == nil {
		t.Fatalf("admission %+v %v", admitted, err)
	}
	var output, diagnostics bytes.Buffer
	if err := questionsCommand([]string{"pending", "--endpoint", options.endpoint}, &output, &diagnostics); err != nil ||
		!strings.Contains(output.String(), admitted.Answer.ID) {
		t.Fatalf("pending %q, %v; diagnostics %q", output.String(), err, diagnostics.String())
	}
	output.Reset()
	if err := questionsCommand([]string{"answer", admitted.Answer.ID, "once", "--endpoint", options.endpoint}, &output, &diagnostics); err != nil ||
		!strings.Contains(output.String(), "once") {
		t.Fatalf("answer %q, %v; diagnostics %q", output.String(), err, diagnostics.String())
	}
	observed, err := app.ObserveContext(ctx, question.RequestKey, 0)
	if err != nil || observed.Outcome.String() != "answered" || observed.Answer == nil || observed.Answer.Option != "once" || !observed.Answer.Yes {
		t.Fatalf("observed %+v %v", observed, err)
	}
}

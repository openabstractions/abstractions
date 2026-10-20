package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func panelChoiceRuntime(t *testing.T) (*askclient.ChoiceClient, *askclient.Operator) {
	t.Helper()
	own(t)
	book, err := asks.LoadApplicationBook(filepath.Join(t.TempDir(), "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	self := func(ctx context.Context, peer *identity.Peer) error {
		process, err := peer.Process.AtLeast(listen.Program.Process)
		if err != nil || process.PID != os.Getpid() {
			return fmt.Errorf("not this test")
		}
		return ctx.Err()
	}
	policyPath := filepath.Join(t.TempDir(), "policy.json")
	policy, err := rights.LoadDecisionPolicy(policyPath, []string{host.QuestionAskAction})
	if err != nil {
		t.Fatal(err)
	}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.QuestionBook = book
		o.QuestionEndpoint = listen.Endpoint(fmt.Sprintf("choice-panel-%d-%d", os.Getpid(), time.Now().UnixNano()))
		o.QuestionOperator = self
		o.QuestionAskPolicy = host.AskPolicyFromRights(policy)
	})
	ctx := context.Background()
	app, err := panelMachine().ResolveAsksChoices(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := app.AskContext(ctx, choiceFixture().Request)
	if err != nil || denied.Outcome != askclient.ChoiceOutcomeForbidden {
		t.Fatalf("admission before exact fixture rule %+v %v", denied, err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Set(rightsclient.Subject{Account: probe.Self().Account, Program: program}, host.QuestionAskAction, host.QuestionAskResource, true); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		after, err := os.ReadFile(policyPath)
		if err != nil || !bytes.Equal(before, after) {
			t.Errorf("ordinary answering changed Rights policy: %v", err)
		}
	})
	return app, operator
}

func admitPublicChoice(t *testing.T, app *askclient.ChoiceClient, operator *askclient.Operator, key string) askclient.ChoiceRecord {
	t.Helper()
	request := choiceFixture().Request
	request.RequestRef.Key = key
	request.Title = "Allow System unavailable gap invalid?"
	admitted, err := app.AskContext(context.Background(), request)
	if err != nil || admitted.Outcome != askclient.ChoiceOutcomePending {
		t.Fatalf("admission %+v %v", admitted, err)
	}
	read, err := operator.ReadChoiceContext(context.Background(), admitted.ID)
	if err != nil || read.Record == nil {
		t.Fatalf("read %+v %v", read, err)
	}
	return *read.Record
}

func TestChoicePresenterAndPanelDecideThroughPublicApplicationContract(t *testing.T) {
	app, operator := panelChoiceRuntime(t)
	ctx := context.Background()
	for _, option := range []string{"cancel", ""} {
		record := admitPublicChoice(t, app, operator, "public-"+option)
		surface := &fakeSurface{option: option}
		p := &questionPresenter{session: unlockedSession(), surface: surface, choices: func(context.Context) (presenterChoiceOperator, error) { return operator, nil }, rules: func() grants.RuleOperator { t.Fatal("ordinary presenter used Rights"); return nil }}
		result := p.presentChoice(ctx, record)
		observation, err := app.ObserveContext(ctx, record.Request.RequestRef, 0)
		want := askclient.ChoiceOutcomeAnswered
		if option == "" {
			want = askclient.ChoiceOutcomeDismissed
		}
		if result.Err != nil || err != nil || observation.Outcome != want || observation.Answer == nil || observation.Answer.Option != option {
			t.Fatalf("public result %+v observation %+v %v", result, observation, err)
		}
		data, _ := json.Marshal(observation.Answer)
		if err := os.WriteFile(filepath.Join(t.TempDir(), "answer.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		ack, err := app.AcknowledgeContext(ctx, record.Request.RequestRef, *observation.Answer)
		if err != nil || ack.Outcome != askclient.ChoiceAckOutcomeAcknowledged {
			t.Fatalf("ack %+v %v", ack, err)
		}
	}
	handler := (&servicePanel{}).handler("test-key")
	post := func(action choiceAction) askclient.ChoiceDecision {
		t.Helper()
		data, _ := json.Marshal(action)
		request := httptest.NewRequest(http.MethodPost, "/choices", bytes.NewReader(data))
		request.Host = "127.0.0.1:8734"
		request.Header.Set("X-Panel-Key", "test-key")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("choice handler %d %s", response.Code, response.Body.String())
		}
		var result askclient.ChoiceDecision
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	record := admitPublicChoice(t, app, operator, "panel-stale")
	forged := cloneChoiceRecord(record)
	forged.Request.Options[0].Label = "Different label"
	if result := post(choiceAction{Action: "answer", Shown: &forged, Option: "allow"}); result.Outcome != askclient.ChoiceDecisionOutcomeConflict {
		t.Fatalf("forged shown request %+v", result)
	}
	if result := post(choiceAction{Action: "answer", Shown: &record, Option: "cancel"}); result.Outcome != askclient.ChoiceDecisionOutcomeAnswered || result.Record.Answer.Option != "cancel" {
		t.Fatalf("offered cancel %+v", result)
	}
	if result := post(choiceAction{Action: "dismiss", Shown: &record}); result.Outcome != askclient.ChoiceDecisionOutcomeConflict {
		t.Fatalf("terminal stale close %+v", result)
	}
	closed := admitPublicChoice(t, app, operator, "panel-close")
	if result := post(choiceAction{Action: "dismiss", Shown: &closed}); result.Outcome != askclient.ChoiceDecisionOutcomeDismissed || result.Record.Answer.Option != "" {
		t.Fatalf("UI close %+v", result)
	}
	observed, err := app.ObserveContext(ctx, closed.Request.RequestRef, 0)
	if err != nil || observed.Outcome != askclient.ChoiceOutcomeDismissed {
		t.Fatalf("application close observation %+v %v", observed, err)
	}
}

func TestChoicePageUsesExactAppLabelsAndIndependentHistory(t *testing.T) {
	app, operator := panelChoiceRuntime(t)
	first := admitPublicChoice(t, app, operator, "page-one")
	second := admitPublicChoice(t, app, operator, "page-two")
	report := runRightsPage(t)
	if len(report.Choices) != 2 {
		t.Fatalf("ordinary list %+v status %q", report.Choices, report.ChoicesStatus)
	}
	for _, row := range report.Choices {
		if len(row.Lines) == 0 || row.Lines[0] != first.Request.Title || len(row.Buttons) != 3 || row.Buttons[0] != "Replace file" || row.Buttons[1] != "Cancel this delivery" || row.Buttons[2] != "Close" {
			t.Fatalf("app labels interpreted %+v", row)
		}
	}
	if _, err := operator.AnswerChoiceContext(context.Background(), first.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if _, err := operator.DismissChoiceContext(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	report = runRightsPage(t)
	if len(report.ChoiceHistory) != 2 || len(report.Choices) == 2 {
		t.Fatalf("ordinary history %+v pending %+v", report.ChoiceHistory, report.Choices)
	}
}

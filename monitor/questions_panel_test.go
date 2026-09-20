package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// panelQuestionRuntime hosts a real application Book whose operator policy
// admits this test process only while allow is set.
func panelQuestionRuntime(t *testing.T, allow *atomic.Bool) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	book, err := asks.LoadApplicationBook(filepath.Join(t.TempDir(), "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("panel-questions-%d", time.Now().UnixNano())
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		QuestionBook: book, QuestionEndpoint: listen.Endpoint(prefix + "q"),
		QuestionOperator: func(ctx context.Context, peer *identity.Peer) error {
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() || !allow.Load() {
				return asksservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)
}

func decodePanel[T any](t *testing.T, code int, body []byte) T {
	t.Helper()
	var v T
	if code != 200 || json.Unmarshal(body, &v) != nil {
		t.Fatalf("panel reply %d %s", code, body)
	}
	return v
}

func TestServicePanelQuestionOperator(t *testing.T) {
	own(t)
	var allow atomic.Bool
	panelQuestionRuntime(t, &allow)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app, err := panelMachine().ResolveAsks(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	pending := askclient.ApplicationQuestion{RequestKey: "panel-pending", Key: "download.reach", Slots: map[string]string{"host": "pending.example"}}
	answered := askclient.ApplicationQuestion{RequestKey: "panel-answered", Key: "download.reach", Slots: map[string]string{"host": "answered.example"}}
	ids := map[string]string{}
	for _, q := range []askclient.ApplicationQuestion{pending, answered} {
		admitted, err := app.AskContext(ctx, q)
		if err != nil || admitted.Answer == nil {
			t.Fatalf("admission %+v %v", admitted, err)
		}
		ids[q.RequestKey] = admitted.Answer.ID
	}
	h := (&servicePanel{}).handler("test-key")
	list := func() wire.OperatorPage {
		r := panelRequest(t, h, "/questions?cursor=", nil)
		return decodePanel[wire.OperatorPage](t, r.Code, r.Body.Bytes())
	}
	act := func(action questionAction) (int, []byte) {
		r := panelRequest(t, h, "/questions", action)
		return r.Code, r.Body.Bytes()
	}

	if page := list(); page.Outcome != wire.OperatorPageOutcomeForbidden || len(page.Records) != 0 {
		t.Fatalf("unauthorized list %+v", page)
	}
	code, body := act(questionAction{Action: "answer", ID: ids[answered.RequestKey], Option: "once"})
	if d := decodePanel[wire.OperatorDecision](t, code, body); d.Outcome != wire.OperatorDecisionOutcomeForbidden || d.Record != nil {
		t.Fatalf("unauthorized answer %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: ids[pending.RequestKey]})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeForbidden || d.Record != nil {
		t.Fatalf("unauthorized retirement %+v", d)
	}
	if o, err := app.ObserveContext(ctx, pending.RequestKey, 0); err != nil || o.Outcome != askclient.ObservationOutcomePending {
		t.Fatalf("refused panel actions changed the question %+v %v", o, err)
	}

	allow.Store(true)
	if page := list(); page.Outcome != wire.OperatorPageOutcomePage || len(page.Records) != 2 || !page.Complete {
		t.Fatalf("authorized list %+v", page)
	}
	code, body = act(questionAction{Action: "answer", ID: ids[answered.RequestKey], Option: "once"})
	if d := decodePanel[wire.OperatorDecision](t, code, body); d.Outcome != wire.OperatorDecisionOutcomeAnswered || d.Record == nil || d.Record.Option != "once" {
		t.Fatalf("panel answer %+v", d)
	}
	code, body = act(questionAction{Action: "answer", ID: ids[answered.RequestKey], Option: "refuse"})
	if d := decodePanel[wire.OperatorDecision](t, code, body); d.Outcome != wire.OperatorDecisionOutcomeConflict {
		t.Fatalf("conflicting panel answer %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: ids[pending.RequestKey]})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeRetired || d.Record == nil || d.Record.ID != ids[pending.RequestKey] {
		t.Fatalf("panel retirement %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: ids[pending.RequestKey]})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeRetired || d.Record != nil {
		t.Fatalf("panel retirement replay %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: "never-admitted"})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeUnknown {
		t.Fatalf("unknown panel retirement %+v", d)
	}
	if o, err := app.ObserveContext(ctx, pending.RequestKey, 0); err != nil || o.Outcome != askclient.ObservationOutcomeGone {
		t.Fatalf("application after panel retirement %+v %v", o, err)
	}
	if o, err := app.ObserveContext(ctx, answered.RequestKey, 0); err != nil || o.Outcome != askclient.ObservationOutcomeAnswered {
		t.Fatalf("application after panel answer %+v %v", o, err)
	}
	if page := list(); page.Outcome != wire.OperatorPageOutcomePage || len(page.Records) != 1 || page.Records[0].ID != ids[answered.RequestKey] {
		t.Fatalf("list after retirement %+v", page)
	}
	for _, bad := range []questionAction{{Action: "pause", ID: "x"}, {Action: "answer", ID: "bad\nid", Option: "once"}, {Action: "retire", ID: "x", Option: "once"}, {Action: "answer", ID: "x"}} {
		if code, body := act(bad); code != 400 {
			t.Fatalf("invalid panel action %+v reached the service: %d %s", bad, code, body)
		}
	}
}

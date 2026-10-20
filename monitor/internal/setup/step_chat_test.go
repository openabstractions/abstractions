package setup

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// chatFake is the chat permission owner: it offers the one rule on host to
// every code app with a program and records each grant. A grant for an app
// whose registration is not confirmed writes nothing.
type chatFake struct {
	w      *stepWorld
	host   string
	grants []StepChatOffer
	// previous is the rules Setup wrote before, as each grant received them.
	previous [][]ApprovedRule
}

const fixtureCoder = `C:\Users\fixture\AppData\Local\Programs\coder\Coder.exe`

func newChatFake(w *stepWorld) *chatFake {
	w.apps = []StepApp{{Name: "opencode", Title: "OpenCode", Use: UseCode, Program: fixtureCoder}}
	return &chatFake{w: w, host: ChatHost}
}

func (f *chatFake) rules() []ApprovedRule {
	return []ApprovedRule{{Action: "abstraction.inference/complete", Resource: "host:" + f.host}}
}

func (f *chatFake) with(s SetupStep) SetupStep {
	s.Owners.Chat = &StepChat{
		Offer: func(_ context.Context, app StepApp, pick StepPick) (StepChatOffer, error) {
			if app.Program == "" {
				return StepChatOffer{Reason: "no program"}, nil
			}
			return StepChatOffer{App: app.Name, Program: app.Program, Rules: f.rules(), Permission: fmt.Sprintf("%s can use %s on this computer.", app.Title, pick.Title)}, nil
		},
		Grant: func(_ context.Context, offer StepChatOffer, previous []ApprovedRule) ([]ApprovedRule, bool, error) {
			for _, app := range f.w.apps {
				if app.Name == offer.App && (app.Unconfirmed || app.Manifest != "") {
					return nil, false, nil
				}
			}
			f.grants = append(f.grants, offer)
			f.previous = append(f.previous, previous)
			return offer.Rules, true, nil
		},
	}
	return s
}

// Owner, 2026-11-01: "yes setup should also grant model that we included".
// The one question says OpenCode can use the included model; Set up writes
// exactly that rule for OpenCode's program once the model is in place, and
// a rerun, an app's request after Remove from OA and the hosting again keep
// it without a question or a second write.
func TestStepGrantsTheIncludedModelToTheAppItSetsUp(t *testing.T) {
	w := newStepWorld(t)
	f := newChatFake(w)
	if outcome, err := f.with(w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 {
		t.Fatalf("run = %q, %v asked %d", outcome, err, w.asked)
	}
	q := w.questions[0]
	if q.Instruction != "About 1 GB to download." || !strings.HasPrefix(q.Details, "OpenCode can use Qwen3.6 35B on this computer. 1 GB free needed. ") ||
		!strings.HasSuffix(q.Details, "OpenCode can use Qwen3.6 35B on this computer. No other model is included.") {
		t.Fatalf("question = %+v", q)
	}
	if len(f.grants) != 1 || f.grants[0].Program != fixtureCoder || !slices.Equal(f.grants[0].Rules, f.rules()) {
		t.Fatalf("grants = %+v", f.grants)
	}
	state, _ := w.step().Load(context.Background())
	if len(state.ChatGrants) != 1 || !state.ChatGrants[0].Granted || state.ChatGrants[0].Program != fixtureCoder || state.ChatGrants[0].Rules != RulesDigest(f.rules()) {
		t.Fatalf("journal = %+v", state.ChatGrants)
	}
	if outcome, err := f.with(w.step()).Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.asked != 1 || len(f.grants) != 1 {
		t.Fatalf("rerun = %q, %v asked %d grants %d", outcome, err, w.asked, len(f.grants))
	}
	w.hostedNow = false
	request := f.with(w.step())
	request.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
	if outcome, err := request.Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || w.hosted != 2 || len(f.grants) != 1 {
		t.Fatalf("request after Remove from OA = %q, %v asked %d hosted %d grants %d", outcome, err, w.asked, w.hosted, len(f.grants))
	}
}

// Each runtime-admitted native Not now writes no rule. Request cooldown is
// tested at the runtime owner rather than this Panel step.
func TestStepNotNowGrantsNothing(t *testing.T) {
	w := newStepWorld(t)
	f := newChatFake(w)
	w.answer = false
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	request := func() SetupStep {
		s := f.with(w.step())
		s.Owners.Now = func() time.Time { return now }
		s.Requester = &StepRequester{App: "OpenCode", Use: UseCode}
		return s
	}
	for range 2 {
		if outcome, err := request().Run(context.Background()); err != nil || outcome != stepAnsweredNotNow {
			t.Fatalf("request = %q, %v", outcome, err)
		}
	}
	if w.asked != 2 || len(f.grants) != 0 {
		t.Fatalf("asked %d grants %+v", w.asked, f.grants)
	}
	if state, _ := w.step().Load(context.Background()); len(state.ChatGrants) != 0 {
		t.Fatalf("Not now recorded a permission: %+v", state)
	}
}

// An app without a program the rights could name gets no permission, and
// the question keeps saying it asks at its first chat.
func TestStepGrantsNothingWithoutAProgram(t *testing.T) {
	w := newStepWorld(t)
	f := newChatFake(w)
	w.apps[0].Program = ""
	if _, err := f.with(w.step()).Run(context.Background()); err != nil || len(f.grants) != 0 {
		t.Fatalf("run %v grants %+v", err, f.grants)
	}
	if q := w.questions[0]; !strings.HasPrefix(q.Details, "OpenCode gets Qwen3.6 35B") || !strings.HasSuffix(q.Details, "OpenCode asks before its first chat.") {
		t.Fatalf("question = %+v", q)
	}
}

// A registration the answer is still confirming gets its rule on a later
// run, without another question; the rule is never written before.
func TestStepGrantWaitsForTheConfirmedRegistration(t *testing.T) {
	w := newStepWorld(t)
	f := newChatFake(w)
	w.apps[0].Unconfirmed = true
	if outcome, err := f.with(w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || len(f.grants) != 0 {
		t.Fatalf("run = %q, %v grants %+v", outcome, err, f.grants)
	}
	w.apps[0].Unconfirmed = false
	if outcome, err := f.with(w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || len(f.grants) != 1 {
		t.Fatalf("after confirmation = %q, %v asked %d grants %d", outcome, err, w.asked, len(f.grants))
	}
}

// Another program for the app, or another app, is a new answer: it asks.
// A rule that changed with the model's host is written again without one.
func TestStepGrantFollowsTheAnswerAndTheHost(t *testing.T) {
	w := newStepWorld(t)
	f := newChatFake(w)
	if _, err := f.with(w.step()).Run(context.Background()); err != nil || len(f.grants) != 1 {
		t.Fatalf("first run %v grants %d", err, len(f.grants))
	}
	old := f.rules()
	f.host = "modelhost-renamed"
	if outcome, err := f.with(w.step()).Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 1 || len(f.grants) != 2 || f.grants[1].Rules[0].Resource != "host:modelhost-renamed" {
		t.Fatalf("renamed host = %q, %v asked %d grants %+v", outcome, err, w.asked, f.grants)
	}
	// The renewal hands the owner the rules Setup wrote for the old host, so
	// it can remove them; the journal then holds the new host's.
	if len(f.previous) != 2 || !slices.Equal(f.previous[1], old) {
		t.Fatalf("previous = %+v", f.previous)
	}
	if state, _ := w.step().Load(context.Background()); !slices.Equal(state.ChatGrants[0].Written, f.rules()) {
		t.Fatalf("journal = %+v", state.ChatGrants)
	}
	w.apps[0].Program = `C:\Elsewhere\Coder.exe`
	w.answer = false
	if outcome, err := f.with(w.step()).Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 2 || len(f.grants) != 2 {
		t.Fatalf("changed program = %q, %v asked %d grants %d", outcome, err, w.asked, len(f.grants))
	}
}

// A step set up before this release recorded no permission. Fit reads the
// model it hosts as served; the step knows it as its own, asks once that
// OpenCode can use it, with nothing to download, and writes the rule. The
// journal keeps the model as Setup's own.
func TestStepAsksOnceToGrantAModelItAlreadyHosts(t *testing.T) {
	w := newStepWorld(t)
	if outcome, err := w.step().Run(context.Background()); err != nil || outcome != stepSetUp {
		t.Fatalf("first run = %q, %v", outcome, err)
	}
	f := newChatFake(w)
	s := f.with(w.step())
	s.Owners.Fit = func(context.Context, string) (StepFit, error) {
		pick := w.pick()
		pick.Served, pick.DownloadBytes = w.hostedNow, 0
		return StepFit{Pick: &pick, PoolBytes: 1 << 40}, nil
	}
	reserves := w.transfer.reserves
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepSetUp || w.asked != 2 || len(f.grants) != 1 || w.transfer.reserves != reserves {
		t.Fatalf("run = %q, %v asked %d grants %d reserves %d", outcome, err, w.asked, len(f.grants), w.transfer.reserves)
	}
	if q := w.questions[1]; q.Instruction != "Nothing to download." || !strings.HasPrefix(q.Details, "OpenCode can use Qwen3.6 35B, which this computer already runs. ") {
		t.Fatalf("question = %+v", q)
	}
	state, _ := w.step().Load(context.Background())
	if state.Served || state.ModelPlanID != w.plan.ID || !state.Ready() {
		t.Fatalf("journal = %+v", state)
	}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepAlreadySetUp || w.asked != 2 {
		t.Fatalf("rerun = %q, %v asked %d", outcome, err, w.asked)
	}
}

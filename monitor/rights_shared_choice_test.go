package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-identity/listen"
)

// An assistant's answer to an app's shared question (asks ASK-R9, ASK-O13)
// reaches Answered for you and Activity with the app's words, the chosen
// label and the assistant, and offers no Undo; a pending shared question is
// marked in Questions waiting; the yes names shared questions.
func TestSharedChoiceAnswersReachAnsweredForYou(t *testing.T) {
	book, _, _, assistant := assistantRuntime(t, "abstraction.model/lookup")
	panel := (&servicePanel{}).handler("test-key")
	if w := assistantState(t, panel).Warning; !strings.Contains(w.Choices, "options an app shares") || !strings.Contains(w.Choices, "cannot be undone") ||
		!strings.Contains(w.Caution, "If it started the work that led an app to ask") {
		t.Fatalf("the yes does not name shared questions %+v", w)
	}
	requester := filepath.Join(t.TempDir(), "pretend-lemonade.exe")
	now := time.Now()
	share := func(key string) string {
		q := askswire.ChoiceRequest{RequestRef: askswire.RequestRef{Key: key, ReplayUntilMs: now.Add(time.Hour).UnixMilli()}, Title: "Existing output: \"model.gguf\"",
			Options: []askswire.ChoiceOption{{ID: "replace", Label: "Replace"}, {ID: "keep", Label: "Keep"}}, PendingUntilMs: now.Add(10 * time.Minute).UnixMilli()}
		r, err := book.AskChoiceAs("requester-scope", requester, q, []string{"keep"}, listen.Seen{}, asks.Publisher{}, nil)
		if err != nil || r.Outcome != askswire.ChoiceOutcomePending {
			t.Fatal(r, err)
		}
		return r.ID
	}
	answered, waiting := share("answered"), share("waiting")
	if d, _, err := book.AnswerSharedChoice(answered, "keep", assistant, func(asks.ChoiceCandidate) error { return nil }); err != nil || d.Outcome != askswire.ChoiceDecisionOutcomeAnswered {
		t.Fatal(d, err)
	}
	r := panelRequest(t, panel, "/rights/answered", nil)
	page := decodePanel[answeredPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Answers) != 1 {
		t.Fatalf("answered for you %+v", page)
	}
	a := page.Answers[0]
	if a.Kind != "choice" || a.ID != answered || a.OptionLabel != "Keep" || a.State != "chosen" || !strings.Contains(a.Sentence, "model.gguf") || a.AnswererName == "" || a.Program != requester {
		t.Fatalf("the choice entry %+v", a)
	}
	if r := panelRequest(t, panel, "/rights/answered", map[string]string{"id": answered}); r.Code == 200 && strings.Contains(r.Body.String(), "\"applied\"") {
		t.Fatalf("Undo of an app's question applied: %s", r.Body.String())
	}
	choices := panelRequest(t, panel, "/choices", nil)
	shared := decodePanel[struct {
		Shared []string `json:"Shared"`
	}](t, choices.Code, choices.Body.Bytes())
	if len(shared.Shared) != 1 || shared.Shared[0] != waiting {
		t.Fatalf("Questions waiting does not mark the shared question %+v", shared)
	}
}

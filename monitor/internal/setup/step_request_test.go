package setup

import (
	"context"
	"strings"
	"testing"
)

// Runtime owns request cooldown. The Panel step uses its native question owner
// each time it is admitted; the old journal timestamp supplies no decision.
func TestStepRequestLeavesCooldownToRuntime(t *testing.T) {
	w := newStepWorld(t)
	request := func() SetupStep {
		s := w.step()
		s.Requester = &StepRequester{App: "OpenCode", Use: "code"}
		return s
	}
	w.answer = false
	if outcome, err := request().Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 1 {
		t.Fatalf("first request = %q, %v asked %d", outcome, err, w.asked)
	}
	state, _ := w.step().Load(context.Background())
	if !state.Declined() || state.RequestDeclinedAt != "" {
		t.Fatalf("journal = %+v", state)
	}
	if _, err := w.step().save(context.Background(), func(st *StepState) error { st.RequestDeclinedAt = "2026-11-02T10:00:00Z"; return nil }); err != nil {
		t.Fatal(err)
	}
	if outcome, err := request().Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 2 {
		t.Fatalf("second request = %q, %v asked %d notices %d", outcome, err, w.asked, len(w.notices))
	}
	// Settings uses that same native question owner.
	settings := w.step()
	if outcome, err := settings.Run(context.Background()); err != nil || outcome != stepAnsweredNotNow || w.asked != 3 {
		t.Fatalf("Settings run = %q, %v asked %d", outcome, err, w.asked)
	}
	if state, _ := w.step().Load(context.Background()); state.RequestDeclinedAt != "" {
		t.Fatalf("a Settings Not now counted as a request's: %+v", state)
	}
}

// A request for a use this computer cannot set up says so in one sentence
// and asks about no other use.
func TestStepRequestForAUseWithNoOffer(t *testing.T) {
	w := newStepWorld(t)
	w.apps = bothApps()
	f := newPictureFake(w)
	f.offer.Reason = "ComfyUI's registered program is a shared interpreter"
	s := f.with(w, w.step())
	s.Requester = &StepRequester{App: "ComfyUI", Use: "image"}
	if outcome, err := s.Run(context.Background()); err != nil || outcome != stepCannotRun || w.asked != 0 {
		t.Fatalf("run = %q, %v asked %d", outcome, err, w.asked)
	}
	if n := w.last(); n.Body != "ComfyUI asked for pictures, which this computer cannot set up yet." {
		t.Fatalf("notice = %+v", n)
	}
	// With the picture offer, an image request asks about pictures only.
	f.offer.Reason = ""
	s = f.with(w, w.step())
	s.Requester = &StepRequester{App: "ComfyUI", Use: "image"}
	w.answer = false
	if _, err := s.Run(context.Background()); err != nil || w.asked != 1 {
		t.Fatalf("image request asked %d, %v", w.asked, err)
	}
	if q := w.questions[0]; q.Title != "Set up pictures for ComfyUI?" || q.Instruction != "About 16 GB to download." || !strings.HasPrefix(q.Details, "ComfyUI gets pictures on this computer. ") {
		t.Fatalf("question = %+v", q)
	}
}

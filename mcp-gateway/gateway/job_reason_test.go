package gateway

import "testing"

// A job refused gpu:standby tells the model the person's sentence.
func TestJobReasonSentenceForStandby(t *testing.T) {
	want := "The computer is going to sleep; try again when it's awake."
	if got := jobReasonSentence("gpu:standby"); got != want {
		t.Fatalf("reason: %q", got)
	}
	if got := jobReasonSentence("image unavailable: gpu:standby"); got != want {
		t.Fatalf("failure text: %q", got)
	}
	if got := jobReasonSentence("gpu:quarantined"); got != "" {
		t.Fatalf("other word: %q", got)
	}
}

// A picture job whose provider the runtime stopped after failed starts fails
// with host:start_failed:<name>; the sentence names the engine by its title,
// or the provider by its name when OpenAbstractions does not ship it (R34
// live test, D1).
func TestJobReasonSentenceForAStoppedProvider(t *testing.T) {
	if got, want := jobReasonSentence("host:start_failed:lemonade-image"), "Lemonade isn't set up on this computer. OpenAbstractions Setup sets it up."; got != want {
		t.Fatalf("Lemonade: %q", got)
	}
	if got, want := jobReasonSentence("start_failed:someone-image"), "The provider someone-image can't start on this computer. Its log in OpenAbstractions says why."; got != want {
		t.Fatalf("other provider: %q", got)
	}
	if got := pictureFailure("auto", "host:start_failed:lemonade-image"); got.Outcome != "failed" || got.Message != "Lemonade isn't set up on this computer. OpenAbstractions Setup sets it up." {
		t.Fatalf("picture failure %+v", got)
	}
}

package main

import (
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// A runtime log record names the contract its call came through. The live
// walkthrough of 2026-10-02 (D12) found image@1 calls logged as
// abstraction.inference/chat@1.
func TestInferenceLogRecordsNameTheirContract(t *testing.T) {
	for _, c := range []struct {
		profile string
		want    string
	}{
		{"", "abstraction.inference/chat@1"},
		{router.ProfileEmbed, "abstraction.inference/embed@1"},
		{"preload", "abstraction.inference/preload@1"},
		{router.ProfileImage, "abstraction.inference/image@1"},
		{router.ProfileVideo, "abstraction.inference/video@1"},
		{router.ProfileSpeech, "abstraction.inference/speech@1"},
		{router.ProfileTranscription, "abstraction.inference/transcription@1"},
		{router.ProfileLive, "abstraction.inference/live@1"},
	} {
		rec := inference.Record{Profile: c.profile, Host: "modelhost", Model: "flux2-klein-4b", Outcome: "forbidden", Reason: "output:forbidden"}
		got := inferenceLogRecord(rec, time.Unix(0, 0))
		if got.Msg != c.want {
			t.Fatalf("profile %q is logged as %q, want %q", c.profile, got.Msg, c.want)
		}
		if got.Attrs["reason"] != "output:forbidden" || got.Attrs["host"] != "modelhost" {
			t.Fatalf("profile %q attributes %v", c.profile, got.Attrs)
		}
		if _, ok := got.Attrs["operation"]; ok {
			t.Fatalf("an empty attribute is kept: %v", got.Attrs)
		}
	}
}

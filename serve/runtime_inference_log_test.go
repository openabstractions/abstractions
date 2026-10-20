package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	logging "github.com/openabstractions/abstraction-logging/go"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestInferenceLogChat3PreservesObservedUsagePresence(t *testing.T) {
	zero, output := int64(0), int64(12)
	result := inferenceLogRecord(inference.Record{ChatVersion: 3, Model: "actual-model", Outcome: "completed", FinishReason: "other", ChatUsage: &iwire.ChatUsage{Input: &zero, Output: &output, Reasoning: &zero}}, time.Unix(0, 0))
	if result.Msg != inference.ChatV3Contract || result.Attrs["tokens_in"] != "0" || result.Attrs["tokens_out"] != "12" || result.Attrs["reasoning_tokens"] != "0" {
		t.Fatalf("chat3 record %+v", result)
	}
	if _, present := result.Attrs["cache_read_tokens"]; present {
		t.Fatal("absent usage became zero")
	}
	if result.Attrs["gen_ai.usage.output_tokens"] != "12" || result.Attrs["gen_ai.response.finish_reasons"] != `["other"]` || result.Attrs["gen_ai.provider.name"] != "openabstractions" || result.Attrs["gen_ai.request.model"] != "actual-model" {
		t.Fatalf("GenAI mapping %v", result.Attrs)
	}
	absent := inferenceLogRecord(inference.Record{ChatVersion: 3, Outcome: "failed"}, time.Unix(0, 0)).Attrs
	for _, key := range []string{"tokens_in", "tokens_out", "gen_ai.usage.input_tokens", "gen_ai.response.finish_reasons"} {
		if _, present := absent[key]; present {
			t.Fatalf("unobserved %s became present", key)
		}
	}
}

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

// A local caller's request record carries the caller as its private subject;
// a claim a paired runtime relayed does not (LOG-P9).
func TestInferenceLogStampsOnlyALocalProvenCaller(t *testing.T) {
	sink, closeSink, err := runtimeLogSink(filepath.Join(t.TempDir(), "records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSink.Close()
	raw := sink.(*logging.FileSink)
	writer, err := runtimeAttributedLogWriter(raw)
	if err != nil {
		t.Fatal(err)
	}
	caller := filepath.Join(t.TempDir(), "opencode.exe")
	local := inference.Record{Program: caller, Host: "lemonade", Outcome: "unavailable", Reason: "engine:none"}
	relayed := inference.Record{Program: caller, Claim: "remote-app", Domain: "paired", Outcome: "response"}
	// A direct remote caller is named remote:<key hash>, never a local program.
	direct := inference.Record{Program: "remote:" + strings.Repeat("ab", 32), Domain: "paired", Outcome: "response"}
	for _, rec := range []inference.Record{local, relayed, direct} {
		if err := writeInferenceLog(writer, rec, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	history, err := raw.ScanApplicationHistory("")
	if err != nil || len(history.Rows) != 3 || history.Rows[0].Emitter == nil || history.Rows[1].Emitter == nil || history.Rows[2].Emitter == nil {
		t.Fatalf("history %+v %v", history, err)
	}
	if !logging.SameCaller(history.Rows[0].Emitter.Subject, caller) || history.Rows[1].Emitter.Subject != "" || history.Rows[2].Emitter.Subject != "" {
		t.Fatalf("subjects %q %q %q", history.Rows[0].Emitter.Subject, history.Rows[1].Emitter.Subject, history.Rows[2].Emitter.Subject)
	}
}

// F20: a call a named selection policy routed to a fallback is logged with the
// route that ran and the preferred route known unavailable; an explicit call
// carries no selection attributes.
func TestInferenceLogRecordNamesThePreferredRoute(t *testing.T) {
	rec := inference.Record{Operation: "op-1", Host: "ollama", Model: "local-model", Outcome: "completed",
		Preferred: "cloud-model", PreferredHost: "cloud", Selection: "preferred_route_unavailable"}
	got := inferenceLogRecord(rec, time.Unix(0, 0)).Attrs
	if got["host"] != "ollama" || got["model"] != "local-model" || got["preferred_model"] != "cloud-model" ||
		got["preferred_host"] != "cloud" || got["selection"] != "preferred_route_unavailable" {
		t.Fatalf("selection attributes %v", got)
	}
	explicit := inferenceLogRecord(inference.Record{Host: "ollama", Model: "local-model", Outcome: "completed"}, time.Unix(0, 0)).Attrs
	for _, key := range []string{"preferred_model", "preferred_host", "selection"} {
		if _, ok := explicit[key]; ok {
			t.Fatalf("explicit call logged %s: %v", key, explicit)
		}
	}
}

package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// AddHost entries: profiles are seeded or owned words, declared_by belongs to
// the runtime, and a ceiling needs a credential.
func TestInferenceHostEntryValidation(t *testing.T) {
	hosted := iwire.HostEntry{Name: "openrouter", Hosted: true, Kind: "openai-compatible", Base: "https://openrouter.ai/api/v1", Credential: "openrouter"}
	for _, c := range []struct {
		edit  func(*iwire.HostEntry)
		field string
	}{
		{func(*iwire.HostEntry) {}, ""},
		{func(e *iwire.HostEntry) { e.Profiles = []string{"chat", "example/rerank@1"} }, ""},
		{func(e *iwire.HostEntry) { e.Profiles = []string{"chat", "chat"} }, "profiles"},
		{func(e *iwire.HostEntry) { e.Profiles = []string{"vision"} }, "profiles"},
		{func(e *iwire.HostEntry) { e.DeclaredBy = "operator" }, "declared_by"},
		{func(e *iwire.HostEntry) { e.Ceiling = &iwire.CeilingLimit{ImagesPerDay: -1} }, "ceiling"},
		{func(e *iwire.HostEntry) { e.Base = "http://openrouter.ai/api/v1" }, "base"},
		{func(e *iwire.HostEntry) { e.Hosted, e.Kind, e.Credential = false, "ollama", "" }, "name"},
	} {
		entry := hosted
		c.edit(&entry)
		if got := validEntry(entry); got != c.field {
			t.Fatalf("%+v: invalid field %q, want %q", entry, got, c.field)
		}
	}
	if got := defaultProfiles(true, "anthropic-messages"); !slices.Equal(got, []string{"chat"}) {
		t.Fatalf("anthropic default %v", got)
	}
	if got := defaultProfiles(false, ""); !slices.Equal(got, iwire.HostProfiles) {
		t.Fatalf("local default %v", got)
	}
}

// A ceiling record keeps every unit field, enforced or not, so the state file
// needs no migration when a profile starts counting requests, images, audio
// seconds or characters.
func TestCeilingRecordsCarryEveryUnit(t *testing.T) {
	limit := ceilingOf(&iwire.CeilingLimit{TokensPerDay: 1, MicrosPerDay: 2, RequestsPerDay: 3, ImagesPerDay: 4, AudioSecondsPerDay: 5, CharactersPerDay: 6})
	raw, err := json.Marshal(inferenceHosts{Ceilings: map[string]inference.Ceiling{"openrouter": limit}})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"tokens_per_day":1`, `"micros_per_day":2`, `"requests_per_day":3`, `"images_per_day":4`, `"audio_seconds_per_day":5`, `"characters_per_day":6`} {
		if !strings.Contains(string(raw), field) {
			t.Fatalf("%s lacks %s", raw, field)
		}
	}
	var back inferenceHosts
	if err := json.Unmarshal(raw, &back); err != nil || *ceilingLimit(back.Ceilings["openrouter"]) != (iwire.CeilingLimit{TokensPerDay: 1, MicrosPerDay: 2, RequestsPerDay: 3, ImagesPerDay: 4, AudioSecondsPerDay: 5, CharactersPerDay: 6}) {
		t.Fatalf("round trip %+v %v", back, err)
	}
}

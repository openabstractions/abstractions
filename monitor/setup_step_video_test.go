package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
)

// The video work runs models host --get for the set, with the first active
// Hugging Face download credential by name; a list that cannot be read
// runs it without one.
func TestStepVideoCommandAndCredential(t *testing.T) {
	if got := stepVideoArgs("ltx-2.5", ""); !slices.Equal(got, []string{"models", "host", "--video-model", "ltx-2.5", "--get"}) {
		t.Fatal(got)
	}
	if got := stepVideoArgs("ltx-2.5", "hf"); !slices.Equal(got, []string{"models", "host", "--video-model", "ltx-2.5", "--get", "--credential", "hf"}) {
		t.Fatal(got)
	}
	hf := cwire.Scope{Targets: []string{"huggingface.co"}, Consumers: []string{"abstraction.download/http-execution@1"}}
	pages := map[string]cwire.MetadataPage{
		"": {Outcome: cwire.PageOutcomePage, Next: "2", Records: []cwire.Metadata{
			{Name: "zz-hf", State: cwire.StateActive, Scope: hf},
			{Name: "a-revoked", State: cwire.StateRevoked, Scope: hf},
			{Name: "a-chat", State: cwire.StateActive, Scope: cwire.Scope{Targets: []string{"huggingface.co"}, Consumers: []string{"abstraction.inference/chat@1"}}},
		}},
		"2": {Outcome: cwire.PageOutcomePage, Complete: true, Records: []cwire.Metadata{{Name: "b-hf", State: cwire.StateActive, Scope: hf}}},
	}
	list := func(_ context.Context, cursor string) (cwire.MetadataPage, error) { return pages[cursor], nil }
	if got := stepVideoCredential(context.Background(), list); got != "b-hf" {
		t.Fatal(got)
	}
	failing := func(context.Context, string) (cwire.MetadataPage, error) { return cwire.MetadataPage{}, errors.New("no holder") }
	if got := stepVideoCredential(context.Background(), failing); got != "" {
		t.Fatal(got)
	}
}

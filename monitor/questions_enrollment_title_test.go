package main

import (
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-identity/listen"
)

// An agent connection question lists as "Connect Claude?" on the Questions
// page, against the real Book. The record keeps its retained title, so the
// question's fingerprint is unchanged.
func TestQuestionsPageListsAConnectionByItsApp(t *testing.T) {
	withoutOwnPrograms(t)
	book, _, program := panelFirstUseRuntime(t, "abstraction.model/lookup")
	slots := map[string]string{"program": program, "title": "Claude integration"}
	record, _, err := book.AskApplicationFrom("runtime-scope", "runtime", "enrollment:claude", asks.AgentEnrollmentKey, slots, listen.Seen{Why: "test"}, asks.Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	if record.Text != "Connect Claude integration?" {
		t.Fatalf("retained text %q", record.Text)
	}
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/questions?cursor=", nil)
	page := decodePanel[questionPage](t, r.Code, r.Body.Bytes())
	if got := page.Sentences[record.ID]; got != "Connect Claude?" {
		t.Fatalf("listed line %q", got)
	}
}

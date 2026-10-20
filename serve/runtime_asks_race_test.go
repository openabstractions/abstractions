package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// firstUseRecords is every retained first-use question.
func firstUseRecords(t *testing.T, book *asks.Book) []asks.Record {
	t.Helper()
	records, _, err := book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	return records
}

// ASK-R2: a refusal decided before a delegated allow landed reaches admit
// after it. The admission reads the decision again and keeps the answered
// allow instead of asking a question the person just answered; a refusal
// after the permit is revoked still asks again.
func TestALateRefusalKeepsAnAllowThatJustLanded(t *testing.T) {
	r, a, book := inputAsks(t)
	a.applied = &appliedEffects{path: filepath.Join(t.TempDir(), "first-use-effects.json"), report: func(err error) { t.Error(err) }}
	subject := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "app.exe")}
	action, resource := host.ModelLookupAction, "hf"

	// The first refusal asks.
	a.admitFirstUse(subject, nil, action, resource)
	records := firstUseRecords(t, book)
	if len(records) != 1 || !records[0].Pending() {
		t.Fatalf("first question %+v", records)
	}
	id := records[0].ID

	// The interleaving: a second refusal is decided not_granted here, then the
	// delegated allow lands (answer, target permit, applied effect), then the
	// refusal's admission runs.
	if _, outcome, err := book.AnswerApplicationAsResponder(id, "allow", filepath.Join(t.TempDir(), "responder.exe"), nil); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	if err := r.policy.Set(subject, action, resource, true); err != nil {
		t.Fatal(err)
	}
	if err := a.applied.add(id); err != nil {
		t.Fatal(err)
	}
	a.admitFirstUse(subject, nil, action, resource)
	records = firstUseRecords(t, book)
	if len(records) != 1 || records[0].ID != id || records[0].Option != "allow" {
		t.Fatalf("a late refusal re-asked an allow that just landed: %+v", records)
	}

	// Once the permit is revoked, the next refusal asks again.
	if err := r.policy.Revoke(subject, action, resource); err != nil {
		t.Fatal(err)
	}
	a.admitFirstUse(subject, nil, action, resource)
	records = firstUseRecords(t, book)
	if len(records) != 1 || records[0].ID == id || !records[0].Pending() {
		t.Fatalf("a refusal after the revoke did not ask again: %+v", records)
	}
	if applied, err := a.applied.has(id); err != nil || applied {
		t.Fatalf("the released question's effect stayed recorded: %v %v", applied, err)
	}
}

// ASK-R2, INF-V4: a paired input allow wrote content.write and content.read.
// With only the read revoked, the next refusal asks again; with both still
// permitted, the answered allow is kept.
func TestAPairedAllowIsAskedAgainWhenOnlyItsReadLapses(t *testing.T) {
	r, a, book := inputAsks(t)
	a.applied = &appliedEffects{path: filepath.Join(t.TempDir(), "first-use-effects.json"), report: func(err error) { t.Error(err) }}
	subject := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "app.exe")}
	const resource = "input:image"

	a.admitPairedInput(subject, nil, resource)
	records := firstUseRecords(t, book)
	if len(records) != 1 || !records[0].Pending() {
		t.Fatalf("paired question %+v", records)
	}
	id := records[0].ID
	if _, outcome, err := book.AnswerApplicationAsResponder(id, "allow", filepath.Join(t.TempDir(), "responder.exe"), nil); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	for _, action := range []string{contentWriteAction, contentReadAction} {
		if err := r.policy.Set(subject, action, resource, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.applied.add(id); err != nil {
		t.Fatal(err)
	}
	a.admitPairedInput(subject, nil, resource)
	if records = firstUseRecords(t, book); len(records) != 1 || records[0].ID != id {
		t.Fatalf("a paired allow with both rules permitted was asked again: %+v", records)
	}

	if err := r.policy.Revoke(subject, contentReadAction, resource); err != nil {
		t.Fatal(err)
	}
	a.admitPairedInput(subject, nil, resource)
	records = firstUseRecords(t, book)
	if len(records) != 1 || records[0].ID == id || !records[0].Pending() {
		t.Fatalf("a paired allow whose read lapsed was not asked again: %+v", records)
	}
}

// has alone reads within the size limit: a well-formed record over the
// bound is refused, reported and set aside, never read whole.
func TestHasRefusesAnOversizedEffectsRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "first-use-effects.json")
	content := []byte(`{"applied":["` + strings.Repeat("a", maxAppliedEffectsBytes) + `"]}`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var reports []error
	e := &appliedEffects{path: path, report: func(err error) { reports = append(reports, err) }}
	if applied, err := e.has(strings.Repeat("a", maxAppliedEffectsBytes)); err != nil || applied {
		t.Fatalf("an oversized record answered: %v %v", applied, err)
	}
	if len(reports) != 1 || !errors.Is(reports[0], cas.ErrTooLarge) {
		t.Fatalf("reports %v, wanted one size refusal", reports)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the oversized record stayed in place: %v", err)
	}
	if kept, err := os.ReadFile(path + ".corrupt"); err != nil || len(kept) != len(content) {
		t.Fatalf("the oversized record was not set aside whole: %v", err)
	}
}

// A corrupt or oversized effects record is reported once, set aside, and the
// next add works on a fresh record.
func TestAnUnreadableEffectsRecordIsSetAside(t *testing.T) {
	for name, content := range map[string][]byte{
		"corrupt":   []byte("{not json"),
		"oversized": append([]byte(`{"applied":["`), append([]byte(strings.Repeat("a", maxAppliedEffectsBytes)), []byte(`"]}`)...)...),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "first-use-effects.json")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			var reports []error
			e := &appliedEffects{path: path, report: func(err error) { mu.Lock(); reports = append(reports, err); mu.Unlock() }}
			if applied, err := e.has("q1"); err != nil || applied {
				t.Fatalf("reading an unreadable record: %v %v", applied, err)
			}
			if err := e.add("q1"); err != nil {
				t.Fatalf("add after setting aside: %v", err)
			}
			if err := e.add("q2"); err != nil {
				t.Fatalf("a second add: %v", err)
			}
			for _, id := range []string{"q1", "q2"} {
				if applied, err := e.has(id); err != nil || !applied {
					t.Fatalf("%s after the fresh record: %v %v", id, applied, err)
				}
			}
			kept, err := os.ReadFile(path + ".corrupt")
			if err != nil || string(kept) != string(content) {
				t.Fatalf("the unreadable record was not set aside unchanged: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(reports) != 1 || !errors.Is(reports[0], errCorruptEffects) && !strings.Contains(reports[0].Error(), "exceeds") {
				t.Fatalf("reports %v, wanted one", reports)
			}
		})
	}
	// A record that turns unreadable before a change is set aside by add too.
	path := filepath.Join(t.TempDir(), "first-use-effects.json")
	if err := os.WriteFile(path, []byte(`{"applied":"wrong"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reported := 0
	e := &appliedEffects{path: path, report: func(error) { reported++ }}
	if err := e.add("q1"); err != nil {
		t.Fatalf("add over an unreadable record: %v", err)
	}
	if applied, err := e.has("q1"); err != nil || !applied || reported != 1 {
		t.Fatalf("after add: applied %v err %v reports %d", applied, err, reported)
	}
}

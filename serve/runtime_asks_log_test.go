package main

import (
	"sync"
	"testing"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	logging "github.com/openabstractions/abstraction-logging/go"
)

type questionLogSink struct {
	mu      sync.Mutex
	records []logging.Record
}

func (s *questionLogSink) Write(r logging.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
	return nil
}

func (s *questionLogSink) questions(id string) []map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []map[string]string
	for _, r := range s.records {
		if r.Msg == questionLogMessage && r.Attrs["question_id"] == id {
			out = append(out, r.Attrs)
		}
	}
	return out
}

// Owner R31, 2026-11-12: the logs showed each open question and the start
// that followed, never what the person answered. One question writes
// exactly one raised record and one settled record, joined by its id.
func TestRuntimeAsksLogsOneRaisedAndOneSettledRecord(t *testing.T) {
	f := newAnswererFixture(t)
	sink := &questionLogSink{}
	f.a.useLogs(sink)
	question := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	answer(t, f, question.ID, "allow")
	// A second answer finds nothing pending and writes nothing.
	if _, outcome, _ := f.a.book.AnswerApplication(question.ID, "refuse"); outcome == "answered" {
		t.Fatal("a settled question was answered again")
	}
	rows := sink.questions(question.ID)
	if len(rows) != 2 {
		t.Fatalf("records for one question: %+v", rows)
	}
	raised, settled := rows[0], rows[1]
	if raised["event"] != "raised" || raised["title"] != question.Text || raised["kind"] != question.Key || raised["program"] != question.Asker || raised["program"] == "" {
		t.Fatalf("raised: %+v question %+v", raised, question)
	}
	if settled["event"] != "settled" || settled["outcome"] != "allowed" || settled["kind"] != question.Key || settled["program"] != question.Asker || settled["app"] != raised["app"] || settled["took_ms"] == "" || settled["settled_at"] == "" {
		t.Fatalf("settled: %+v", settled)
	}
}

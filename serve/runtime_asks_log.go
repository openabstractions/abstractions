package main

import (
	"strconv"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	logging "github.com/openabstractions/abstraction-logging/go"
)

// questionLogMessage names the Asks records in OA logging: one when a
// question is raised and one when it settles, joined by question_id.
const questionLogMessage = "abstraction.asks/question"

// logQuestions writes one record for each question the runtime's Book
// raises and one for each it settles. A write failure is reported; the
// question stands.
func (a *runtimeAsks) logQuestions(sink logging.Sink) {
	if a == nil || a.book == nil || sink == nil {
		return
	}
	write := func(r asks.Record, attrs map[string]string, at time.Time) {
		attrs["question_id"] = r.ID
		attrs["kind"] = r.Key
		attrs["app"] = questionApp(r)
		attrs["program"] = r.Asker
		if err := sink.Write(logging.Record{Schema: logging.SchemaVersion, Time: logging.At(at), Level: logging.LevelInfo, Msg: questionLogMessage, Attrs: attrs}); err != nil && a.report != nil {
			a.report(err)
		}
	}
	a.book.SetQuestionEvents(asks.QuestionEvents{
		Raised: func(r asks.Record) {
			write(r, map[string]string{"event": "raised", "title": r.Text}, r.Asked)
		},
		Settled: func(r asks.Record, outcome string, at time.Time) {
			attrs := map[string]string{"event": "settled", "outcome": outcome, "settled_at": at.UTC().Format(time.RFC3339Nano)}
			if !r.Asked.IsZero() && !at.Before(r.Asked) {
				attrs["took_ms"] = strconv.FormatInt(at.Sub(r.Asked).Milliseconds(), 10)
			}
			if r.AnsweredBy != "" {
				attrs["answered_by"] = r.AnsweredBy
			}
			write(r, attrs, at)
		},
	})
}

// questionApp is the display name of the application a question is about:
// its about slot, else the application its permission names.
func questionApp(r asks.Record) string {
	if r.About != "" {
		return r.About
	}
	if r.Content != nil {
		for _, slot := range []string{"app", "application"} {
			if v := r.Content.Slots[slot]; v != "" {
				return v
			}
		}
	}
	if r.Permission != nil && len(r.Permission.Targets) > 0 {
		if app, ok := strings.CutPrefix(r.Permission.Targets[0].Resource, "app:"); ok {
			return app
		}
	}
	return ""
}

package gateway

import (
	"context"
	"errors"
	"fmt"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestColdReplyViewDefersMixedPageUntilEveryReplyIsSent(t *testing.T) {
	v := &messagesReplyView{}
	page := wire.Page{Outcome: wire.OutcomePage, Cursor: "page-one", Messages: []wire.Message{}}
	for i := 0; i < 10; i++ {
		page.Messages = append(page.Messages, wire.Message{Header: wire.Header{Kind: wire.KindQuestion, MessageID: fmt.Sprint(i)}})
	}
	after := ""
	read := func(_ context.Context, in MessagesWaitInput) (wire.Page, error) { after = in.After; return page, nil }
	if _, err := v.wait(t.Context(), MessagesWaitInput{After: "forged"}, read); err != nil || after != "" {
		t.Fatal(after, err)
	}
	for i := 0; i < 9; i++ {
		if _, err := v.reply(t.Context(), MessagesReplyInput{MessageID: fmt.Sprint(i)}, func(context.Context, MessagesReplyInput) (wire.SendResult, error) {
			return wire.SendResult{Outcome: wire.OutcomeSent}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, outcome := range []wire.Outcome{wire.OutcomeUnknown, wire.OutcomeUnavailable} {
		v.reply(t.Context(), MessagesReplyInput{MessageID: "9"}, func(context.Context, MessagesReplyInput) (wire.SendResult, error) {
			return wire.SendResult{Outcome: outcome}, nil
		})
		if _, err := v.wait(t.Context(), MessagesWaitInput{After: "page-one"}, read); err != nil || after != "" {
			t.Fatal("uncertain reply advanced cursor", after, err)
		}
	}
	completed := wire.TaskStateCompleted
	page.Messages[9].Header.TaskState = &completed
	if _, err := v.wait(t.Context(), MessagesWaitInput{After: "page-one"}, read); err != nil || after != "" {
		t.Fatal("terminal hint bypassed explicit Reply reconciliation", after, err)
	}
	old := page
	page = wire.Page{Outcome: wire.OutcomePage, Cursor: "unexpected-next", Messages: []wire.Message{{Header: wire.Header{Kind: wire.KindQuestion, MessageID: "later"}}}}
	if _, err := v.wait(t.Context(), MessagesWaitInput{After: "page-one"}, read); err == nil || after != "" || v.questions["9"] != coldQuestionUncertain {
		t.Fatal("changed page erased uncertain Reply", after, err, v.questions)
	}
	page = old
	v.reply(t.Context(), MessagesReplyInput{MessageID: "9"}, func(context.Context, MessagesReplyInput) (wire.SendResult, error) {
		return wire.SendResult{Outcome: wire.OutcomeSent}, nil
	})
	page = wire.Page{Outcome: wire.OutcomePage, Cursor: "page-two", Messages: []wire.Message{{Header: wire.Header{Kind: wire.KindQuestion, MessageID: "next"}}}}
	if _, err := v.wait(t.Context(), MessagesWaitInput{After: "forged-newer"}, read); err != nil || after != "page-one" {
		t.Fatal(after, err)
	}
	if len(v.questions) != 1 || len(v.questions) > 10 {
		t.Fatal(v.questions)
	}
	if _, err := v.wait(t.Context(), MessagesWaitInput{After: "page-two"}, read); err != nil || after != "page-one" {
		t.Fatal("next pending page acknowledged", after, err)
	}
}

func TestColdReplyViewFailureNotesAndTerminalBounds(t *testing.T) {
	for _, state := range []wire.TaskState{wire.TaskStateCompleted, wire.TaskStateCanceled, wire.TaskStateFailed, wire.TaskStateRejected} {
		v := &messagesReplyView{}
		page := wire.Page{Outcome: wire.OutcomePage, Cursor: "terminal", Messages: []wire.Message{{Header: wire.Header{Kind: wire.KindQuestion, MessageID: "q", TaskState: &state}}}}
		v.wait(t.Context(), MessagesWaitInput{}, func(context.Context, MessagesWaitInput) (wire.Page, error) { return page, nil })
		v.wait(t.Context(), MessagesWaitInput{}, func(_ context.Context, in MessagesWaitInput) (wire.Page, error) {
			if in.After != "terminal" {
				t.Fatal(in)
			}
			return wire.Page{Outcome: wire.OutcomeEnded}, nil
		})
	}
	v := &messagesReplyView{}
	page := wire.Page{Outcome: wire.OutcomePage, Cursor: "unfinished-note", Messages: []wire.Message{{Header: wire.Header{Kind: wire.KindNote, MessageID: "n"}}}}
	read := func(_ context.Context, in MessagesWaitInput) (wire.Page, error) {
		if in.After != "" {
			t.Fatal("unfinished note acknowledged", in)
		}
		return page, nil
	}
	if _, err := v.wait(t.Context(), MessagesWaitInput{}, read); err == nil {
		t.Fatal("note processing completion inferred")
	}
	if _, err := v.wait(t.Context(), MessagesWaitInput{After: "unfinished-note"}, read); err == nil {
		t.Fatal("model cursor bypassed note custody")
	}
	v = &messagesReplyView{}
	page = wire.Page{Outcome: wire.OutcomePage, Cursor: "pending", Messages: []wire.Message{{Header: wire.Header{Kind: wire.KindQuestion, MessageID: "q"}}}}
	v.wait(t.Context(), MessagesWaitInput{}, func(context.Context, MessagesWaitInput) (wire.Page, error) { return page, nil })
	v.reply(t.Context(), MessagesReplyInput{MessageID: "q"}, func(context.Context, MessagesReplyInput) (wire.SendResult, error) {
		return wire.SendResult{}, errors.New("failed closed")
	})
	v.wait(t.Context(), MessagesWaitInput{After: "pending"}, func(_ context.Context, in MessagesWaitInput) (wire.Page, error) {
		if in.After != "" {
			t.Fatal("failed run discarded pending body", in)
		}
		return wire.Page{Outcome: wire.OutcomeForbidden}, nil
	})
}

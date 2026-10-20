package gateway

import (
	"context"
	"errors"
	"sync"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// A cold view owns only its current native body-page cursor and at most ten
// question IDs. It changes no native acknowledgement or live-session behavior.
type messagesReplyView struct {
	mu            sync.Mutex
	after, cursor string
	questions     map[string]coldQuestionState
	blocked       bool
}

type coldQuestionState uint8

const (
	coldQuestionPending coldQuestionState = iota
	coldQuestionSent
	coldQuestionUncertain
)

func (v *messagesReplyView) ready() bool {
	if v.blocked || v.cursor == "" {
		return false
	}
	for _, state := range v.questions {
		if state != coldQuestionSent {
			return false
		}
	}
	return true
}

func (v *messagesReplyView) wait(ctx context.Context, in MessagesWaitInput, read func(context.Context, MessagesWaitInput) (wire.Page, error)) (wire.Page, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	wasReady := v.ready()
	if wasReady {
		v.after = v.cursor
	}
	// A model-supplied after never advances past unfinished cold work.
	in.After = v.after
	if len(v.questions) != 0 && !v.ready() {
		in.WaitMs = 0
	}
	page, err := read(ctx, in)
	if err != nil || page.Outcome != wire.OutcomePage {
		return page, err
	}
	if len(page.Messages) > 10 || len(page.Cursor) > 256 {
		return wire.Page{}, errors.New("gateway: cold reply page exceeds native bounds")
	}
	questions := map[string]coldQuestionState{}
	blocked := false
	for _, message := range page.Messages {
		h := message.Header
		if h.Kind != wire.KindQuestion {
			blocked = true
			continue
		}
		if h.MessageID == "" {
			return wire.Page{}, errors.New("gateway: cold reply question missing ID")
		}
		state := v.questions[h.MessageID]
		// An uncertain Reply requires explicit identical reconciliation on
		// this connection. A later terminal hint cannot substitute for sent.
		if state != coldQuestionUncertain && h.Refused {
			state = coldQuestionSent
		}
		if state != coldQuestionUncertain && h.TaskState != nil {
			switch *h.TaskState {
			case wire.TaskStateCompleted, wire.TaskStateCanceled, wire.TaskStateFailed, wire.TaskStateRejected:
				state = coldQuestionSent
			}
		}
		questions[h.MessageID] = state
	}
	if !wasReady {
		for id, state := range v.questions {
			if _, present := questions[id]; !present && state != coldQuestionSent {
				// A changed page cannot silently erase an uncertain Reply or
				// unfinished question. Keep its cursor for explicit reconciliation.
				return wire.Page{}, errors.New("gateway: unfinished cold question left its native page")
			}
		}
	}
	v.cursor, v.questions, v.blocked = page.Cursor, questions, blocked
	if blocked {
		return wire.Page{}, errors.New("gateway: cold reply view cannot confirm non-question processing")
	}
	return page, nil
}

func (v *messagesReplyView) reply(ctx context.Context, in MessagesReplyInput, send func(context.Context, MessagesReplyInput) (wire.SendResult, error)) (wire.SendResult, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out, err := send(ctx, in)
	if err == nil && out.Outcome == wire.OutcomeSent {
		if _, read := v.questions[in.MessageID]; read {
			v.questions[in.MessageID] = coldQuestionSent
		}
	} else if err != nil || out.Outcome == wire.OutcomeUnknown || out.Outcome == wire.OutcomeUnavailable {
		if state, read := v.questions[in.MessageID]; read && state != coldQuestionSent {
			v.questions[in.MessageID] = coldQuestionUncertain
		}
	}
	return out, err
}

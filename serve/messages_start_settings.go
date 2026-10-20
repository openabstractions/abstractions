package main

import (
	"context"
	"errors"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// messageStartSettings is the effective policy read through Config. It grants
// no start authority; the existing per-direction right is checked afterwards.
type messageStartSettings struct {
	Questions, Notes, Receiver, OpenApp bool
	PerHour                             int
}

func defaultMessageStartSettings() messageStartSettings {
	return messageStartSettings{Questions: true, Receiver: true, PerHour: 1}
}

func validMessageStartRate(rate int) bool { return rate == 1 || rate == 2 || rate == 4 }

func messageStartKind(kind string) bool { return kind == "question" || kind == "note" }

func (h *applicationsHost) readMessageStartSettings(ctx context.Context, application string) (messageStartSettings, error) {
	value := defaultMessageStartSettings()
	if h.messageStartSettings != nil {
		var err error
		value, err = h.messageStartSettings(ctx, application)
		if err != nil {
			return messageStartSettings{}, err
		}
	}
	if !validMessageStartRate(value.PerHour) {
		return messageStartSettings{}, errors.New("messages: invalid start rate")
	}
	return value, ctx.Err()
}

func (s messageStartSettings) permits(kind msg.Kind) bool {
	return s.Receiver && (kind == msg.KindQuestion && s.Questions || kind == msg.KindNote && s.Notes)
}

// Package lostreply loses the reply to one committed request frame for Go
// client fixtures. The wrapped exchange runs to completion, so the service has
// processed the request and answered; the caller receives ErrLostReply instead
// of the reply. It fires once, for the first frame whose JSON "method" member
// equals the configured method.
package lostreply

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

// ErrLostReply wraps io.ErrUnexpectedEOF, the error a truncated reply produces.
var ErrLostReply = fmt.Errorf("lostreply: fixture discarded the committed reply: %w", io.ErrUnexpectedEOF)

// Exchanger is the frame transport shape generated Go clients accept.
type Exchanger interface {
	ExchangeFrame([]byte) ([]byte, error)
}

type writer interface {
	WriteFrame([]byte) error
}

// Transport discards one reply and passes every other frame through unchanged.
type Transport struct {
	inner     Exchanger
	method    string
	mu        sync.Mutex
	fired     bool
	discarded int
}

// New wraps inner; an empty method selects "Submit".
func New(inner Exchanger, method string) *Transport {
	if method == "" {
		method = "Submit"
	}
	return &Transport{inner: inner, method: method}
}

func (t *Transport) ExchangeFrame(frame []byte) ([]byte, error) {
	reply, err := t.inner.ExchangeFrame(frame)
	if err != nil {
		return reply, err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.fired && methodOf(frame) == t.method {
		t.fired, t.discarded = true, len(reply)
		return nil, ErrLostReply
	}
	return reply, nil
}

// WriteFrame forwards one-way frames when the wrapped transport supports them.
func (t *Transport) WriteFrame(frame []byte) error {
	w, ok := t.inner.(writer)
	if !ok {
		return errors.New("lostreply: wrapped transport has no WriteFrame")
	}
	return w.WriteFrame(frame)
}

// Fired reports whether the fault ran and how many reply bytes it discarded.
func (t *Transport) Fired() (bool, int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.fired, t.discarded
}

func methodOf(frame []byte) string {
	var envelope struct {
		Method string `json:"method"`
	}
	if json.Unmarshal(frame, &envelope) != nil {
		return ""
	}
	return envelope.Method
}

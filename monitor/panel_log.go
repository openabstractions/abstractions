package main

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	logging "github.com/openabstractions/abstraction-logging/go"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
)

// panelProgram is the name the Panel claims at hop 0 of its own records.
const panelProgram = "Abstraction Panel"

// panelLog is the Panel's own logging, adopted through the resolved sink. The
// first record resolves abstraction.logging/sink@1; with no runtime that record
// fails with the resolution error and goes nowhere else [LOG-S11]. Once bound,
// records queue in an AsyncSink that retries and rebinds when the service
// disappears [LOG-S12], and its counts are the sink state the page shows.
type panelLog struct {
	mu         sync.Mutex
	sink       *logging.AsyncSink
	bindError  string
	transition string
	changedAt  time.Time
}

// panelSinkState is the Panel's own sink as LOG-S13 counts it.
type panelSinkState struct {
	Adopted    bool   `json:"adopted"`
	State      string `json:"state"`
	BindError  string `json:"bindError,omitempty"`
	Accepted   uint64 `json:"accepted"`
	Dropped    uint64 `json:"dropped"`
	Written    uint64 `json:"written"`
	Failed     uint64 `json:"failed"`
	Abandoned  uint64 `json:"abandoned"`
	Queued     int    `json:"queued"`
	Failing    bool   `json:"failing"`
	LastError  string `json:"lastError,omitempty"`
	Transition string `json:"transition,omitempty"`
	ChangedAt  string `json:"changedAt,omitempty"`
}

func resolvePanelLog(ctx context.Context) (*logclient.Client, error) {
	return panelMachine().ResolveLog(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

func (l *panelLog) bind(ctx context.Context) (*logging.AsyncSink, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sink != nil {
		return l.sink, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	bound, err := resolvePanelLog(bounded)
	if err != nil {
		l.bindError = err.Error()
		return nil, err
	}
	l.bindError = ""
	l.sink = logging.NewAsyncSink(logging.NewRebindingClientSink(bound, resolvePanelLog), logging.AsyncOptions{
		Program: panelProgram,
		OnFailure: func(err error, c logging.AsyncCounts) {
			l.note(fmt.Sprintf("service unreachable (%v); %d queued, %d dropped", err, c.Queued, c.Dropped))
		},
		OnOverflow: func(c logging.AsyncCounts) {
			l.note(fmt.Sprintf("queue full, dropping newest records; %d queued, %d dropped", c.Queued, c.Dropped))
		},
		OnRecovery: func(c logging.AsyncCounts) { l.note(fmt.Sprintf("delivering again; %d dropped", c.Dropped)) },
	})
	return l.sink, nil
}

// note keeps the latest transition and reports it on standard error, as the
// default reporter would.
func (l *panelLog) note(transition string) {
	fmt.Fprintln(os.Stderr, "abstraction.logging: "+transition)
	l.mu.Lock()
	l.transition, l.changedAt = transition, time.Now()
	l.mu.Unlock()
}

// record logs one Panel event at level with string attributes.
func (l *panelLog) record(ctx context.Context, level logging.Level, message string, attrs map[string]string) error {
	sink, err := l.bind(ctx)
	if err != nil {
		return err
	}
	return sink.Write(logging.Record{Schema: logging.SchemaVersion, Time: logging.At(time.Now()), Level: level, Msg: message,
		Identity: logging.Identity{logging.Claim(panelProgram)}, Attrs: attrs})
}

func (l *panelLog) state() panelSinkState {
	l.mu.Lock()
	sink, bindError, transition, changedAt := l.sink, l.bindError, l.transition, l.changedAt
	l.mu.Unlock()
	state := panelSinkState{BindError: bindError, Transition: transition}
	if !changedAt.IsZero() {
		state.ChangedAt = changedAt.Format(time.RFC3339)
	}
	if sink == nil {
		state.State = "not bound"
		if bindError != "" {
			state.State = "resolution failed"
		}
		return state
	}
	c := sink.Counts()
	state.Adopted, state.Accepted, state.Dropped, state.Written, state.Failed, state.Abandoned = true, c.Accepted, c.Dropped, c.Written, c.Failed, c.Abandoned
	state.Queued, state.Failing, state.LastError = c.Queued, c.Failing, c.LastError
	state.State = "delivering"
	if c.Failing {
		state.State = "failing"
	}
	return state
}

// close drains the queue within ctx.
func (l *panelLog) close(ctx context.Context) {
	l.mu.Lock()
	sink := l.sink
	l.sink = nil
	l.mu.Unlock()
	if sink != nil {
		_, _ = sink.Close(ctx)
	}
}

// logAction records one change the person asked the Panel to make. It never
// carries URLs, settings values or rule contents; a failure to log is shown in
// the Panel's sink state and does not stop the action.
func (p *servicePanel) logAction(ctx context.Context, action string, attrs map[string]string) {
	all := map[string]string{"panel.action": action}
	for k, v := range attrs {
		all[k] = v
	}
	_ = p.log.record(ctx, logging.LevelInfo, "panel action", all)
}

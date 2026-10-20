package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A slice of live records between enrolled agents completed within the
// background budget only when each record read both agents' enrollment and
// image once; at the measured 7-25 ms per Windows image verification the
// installed preview failed every pass with "expiry cleanup unavailable".
func TestMessageProgressSteadyLiveSliceFitsBudget(t *testing.T) {
	h, _, _, _, _ := messagesStartProgressFixture(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	actor := messageActor{Account: h.messageRights.owner, Program: program, Application: "sender", Context: "source"}
	receiver := h.directory.descriptors["receiver"].Program
	clock := time.Now()
	h.messages.now = func() time.Time { return clock }
	// The per-pair hourly bound admits 20 sends; the receiver queue holds 50.
	for i := range 48 {
		if i%15 == 14 {
			clock = clock.Add(time.Hour)
		}
		request := msg.SendRequest{To: "app:receiver", Context: "ctx", Kind: msg.KindNote, Subject: fmt.Sprintf("note %d", i), Body: fmt.Sprintf("body %d", i), IdempotencyKey: fmt.Sprintf("note-%d", i), ExpiresUnixMs: clock.Add(7 * 24 * time.Hour).UnixMilli()}
		if got := h.messages.accept(t.Context(), actor, receiver, request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(i, got)
		}
	}
	read := h.directory.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	var images atomic.Int32
	h.directory.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		images.Add(1)
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
			return applicationAgentImage{}, ctx.Err()
		}
		return read(ctx, program, binding)
	}
	var reports []error
	h.ctx, h.report = t.Context(), func(err error) { reports = append(reports, err) }
	for pass := range 3 {
		images.Store(0)
		if failing := h.messageProgressTick(""); failing != "" || len(reports) != 0 {
			t.Fatal("steady pass failed", pass, failing, reports)
		}
		// One verification per enrolled agent, sender and receiver.
		if n := images.Load(); n > 2 {
			t.Fatal("pass verified an agent image per record", pass, n)
		}
	}
}

// messagesProgressSends accepts n messages of kind from the fixture's sender
// to its receiver, advancing the store clock past the per-pair hourly bound.
func messagesProgressSends(t *testing.T, h *applicationsHost, kind msg.Kind, n int) *time.Time {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	actor := messageActor{Account: h.messageRights.owner, Program: program, Application: "sender", Context: "source"}
	receiver := h.directory.descriptors["receiver"].Program
	clock := time.Now()
	h.messages.now = func() time.Time { return clock }
	for i := range n {
		if i%15 == 14 {
			clock = clock.Add(time.Hour)
		}
		request := msg.SendRequest{To: "app:receiver", Context: "ctx", Kind: kind, Subject: fmt.Sprintf("subject %d", i), Body: fmt.Sprintf("body %d", i), IdempotencyKey: fmt.Sprintf("send-%d", i), ExpiresUnixMs: clock.Add(7 * 24 * time.Hour).UnixMilli()}
		if kind == msg.KindQuestion {
			request.AnswerByUnixMs = clock.Add(24 * time.Hour).UnixMilli()
		}
		if got := h.messages.accept(t.Context(), actor, receiver, request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(i, got)
		}
	}
	return &clock
}

// Wake is on for questions and the receiver runs. The start check reads
// presence before its fresh consent read, so pending questions to a running
// receiver add no image verification to the pass.
func TestMessageProgressWakeRunningReceiverReadsAgentsOnce(t *testing.T) {
	h, _, _, calls, _ := messagesStartProgressFixture(t)
	messagesProgressSends(t, h, msg.KindQuestion, 40)
	h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
		return defaultMessageStartSettings(), nil
	}
	h.directory.instances["running"] = applicationLease{application: "receiver", owner: rights.Subject{Account: h.messageRights.owner, Program: h.directory.descriptors["receiver"].Program}, session: "fixture", deadline: time.Now().Add(time.Hour)}
	read := h.directory.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	var images atomic.Int32
	h.directory.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		images.Add(1)
		return read(ctx, program, binding)
	}
	var reports []error
	h.ctx, h.report = t.Context(), func(err error) { reports = append(reports, err) }
	for pass := range 3 {
		images.Store(0)
		if failing := h.messageProgressTick(""); failing != "" || len(reports) != 0 {
			t.Fatal("wake pass failed", pass, failing, reports)
		}
		if n := images.Load(); n > 2 {
			t.Fatal("start check verified agent images per question", pass, n)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("running receiver started", calls.Load())
	}
}

// A single short expiry page returns the same slice to every pass. A pass cut
// short after a few records leaves the next pass to start at the record it
// did not finish, so every record is reached.
func TestMessageProgressBudgetCutPassReachesLaterRecords(t *testing.T) {
	h, _, _, _, _ := messagesStartProgressFixture(t)
	messagesProgressSends(t, h, msg.KindNote, 20)
	const perPass = 4
	visits := 0
	var cancel context.CancelFunc
	reached := map[int]bool{}
	h.messages.inspect = func(name string, index int) {
		if name != "progress_record" {
			return
		}
		if visits++; visits > perPass {
			cancel()
			return
		}
		reached[index] = true
	}
	for range 7 {
		var ctx context.Context
		ctx, cancel = context.WithCancel(t.Context())
		visits = 0
		if err := h.progressMessages(ctx); err == nil {
			t.Fatal("pass was not cut", visits, reached)
		}
		cancel()
	}
	// The 20 notes; the fixture's one-hour question expired when the sends
	// moved the clock past the hourly bound.
	if len(reached) != 20 {
		t.Fatal("slice size", reached)
	}
	for index := range 20 {
		if !reached[index] {
			t.Fatal("record never reached", index, reached)
		}
	}
}

func TestMessageProgressReportsFailureOnceAndRecovery(t *testing.T) {
	s, _, _, _ := messagesStoreFixture(t)
	var reports []string
	h := &applicationsHost{messages: s, ctx: t.Context(), report: func(err error) { reports = append(reports, err.Error()) }}
	head := filepath.Join(s.dir, "expiry", "head.json")
	if err := os.WriteFile(head, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	failing := ""
	for range 3 {
		failing = h.messageProgressTick(failing)
	}
	want := "messages: expiry cleanup unavailable: messages: invalid expiry head"
	if len(reports) != 1 || reports[0] != want || failing == "" {
		t.Fatal("repeated failure", reports, failing)
	}
	if err := os.Remove(head); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		failing = h.messageProgressTick(failing)
	}
	if len(reports) != 2 || reports[1] != "messages: expiry cleanup recovered" || failing != "" {
		t.Fatal("recovery", reports, failing)
	}
	// Nothing to do is success and stays quiet.
	if failing = h.messageProgressTick(failing); failing != "" || len(reports) != 2 {
		t.Fatal("idle pass reported", reports, failing)
	}
}

func TestMessageProgressFailureClassOmitsDetail(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.Join(errors.New("messages: current agent metadata unavailable"), context.DeadlineExceeded), "pass budget exceeded"},
		{fmt.Errorf("messages: read: %w", &fs.PathError{Op: "open", Path: `C:\Users\person\private\state.json`, Err: fs.ErrPermission}), "file open: permission denied"},
		{fmt.Errorf("messages: move: %w", &os.LinkError{Op: "rename", Old: `C:\Users\person\a`, New: `C:\Users\person\b`, Err: fs.ErrExist}), "file rename: file already exists"},
		{os.NewSyscallError("FlushFileBuffers", errors.New("handle closed")), "system call FlushFileBuffers: handle closed"},
		{errors.New("messages: invalid expiry cursor"), "messages: invalid expiry cursor"},
		// 159 ASCII bytes and a two-byte rune straddle the 160-byte cut.
		{errors.New(strings.Repeat("a", 159) + "\u00e9tail"), strings.Repeat("a", 159)},
	} {
		if got := messageProgressFailureClass(c.err); got != c.want || strings.Contains(got, "person") || !utf8.ValidString(got) {
			t.Fatal(c.err, got)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"time"

	appmodel "github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// S5 architect ruling 2026-11-22 (decisions/RULING-2026-11-22-S5-reply-only.md):
// a turn OA starts in a chat because another agent's message arrived is a
// reply-only turn until the person allows more. This connection enforces it
// on OA's own tools. The Claude Mod and the OpenCode plugin also enforce it
// on their hosts' tools; for Codex this is the only enforcement in 0.8.1.
//
// The mark starts when this inbox's wake is reserved for a pointer, when a
// read confirms a wake key, or, for Codex, when a body read happens while a
// queued pointer is still held. It ends with the host adapter's keyed turn
// signal ("ended" or "person") or after replyOnlyLease. A model cannot end
// it: the turn signal needs the adapter key, and Codex registers none.

// replyOnlyLease bounds a mark no turn signal ends: the wake ticket lease.
const replyOnlyLease = codexWakeLease

// replyOnlyReason is the refusal the ruling words.
func replyOnlyReason(sender string) string {
	if sender == "" {
		sender = "another agent"
	}
	return "This turn was started by a message from " + sender + ". Ask the person to allow more for this conversation."
}

// replyOnlyAllowed are OA's tools a reply-only turn keeps: reading and
// replying in the thread, the host adapter's lifecycle, and the read-only
// tools (models, apps, app state, job status, OA's own API description).
// oa_messages_reply and oa_applications_command_call have their own checks.
var replyOnlyAllowed = map[string]bool{
	"oa_messages_wait":                true,
	"oa_messages_watch":               true,
	"oa_messages_inbox_open":          true,
	"oa_messages_inboxes_list":        true,
	"oa_claude_session_start":         true,
	"oa_claude_session_end":           true,
	"oa_models_list":                  true,
	"oa_applications_list":            true,
	"oa_applications_describe":        true,
	"oa_applications_commands_list":   true,
	"oa_applications_enrollment_read": true,
	"oa_api_describe":                 true,
	"oa_api_read":                     true,
	"oa_inference_job_status":         true,
	"oa_inference_job_diagnose":       true,
	"oa_jobs_follow":                  true,
	"oa_asks_watch":                   true,
	"oa_asks_list_answerable":         true,
	"oa_asks_list_choices":            true,
}

// replyOnlyAppTools are the app-model binding tools a command call may name
// that only read an open app.
var replyOnlyAppTools = map[string]bool{"app_describe": true, "app_contexts": true, "app_read": true, "app_changes": true, "app_job": true}

// replyOnlyTurn is one inbox's mark. The zero value has no mark.
type replyOnlyTurn struct {
	mu        sync.Mutex
	until     time.Time
	senders   []string
	replyable map[string]bool
	now       func() time.Time
}

func (t *replyOnlyTurn) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// mark starts or extends the mark with the turn's senders (application
// names) and the IDs of the messages it read, which it may answer.
func (t *replyOnlyTurn) mark(senders []string, read []wire.Message) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.clock()
	if !now.Before(t.until) {
		t.senders, t.replyable = nil, nil
	}
	t.until = now.Add(replyOnlyLease)
	for _, s := range senders {
		if s != "" && !slices.Contains(t.senders, s) {
			t.senders = append(t.senders, s)
		}
	}
	for _, m := range read {
		if m.Header.Kind != wire.KindNote && m.Header.Kind != wire.KindQuestion && m.Header.Kind != wire.KindReply {
			continue
		}
		sender, ok := replyOnlySender(m.Header.From, m.Header.RemoteOrigin != nil)
		if !ok {
			continue
		}
		if !slices.Contains(t.senders, sender) {
			t.senders = append(t.senders, sender)
		}
		if t.replyable == nil {
			t.replyable = map[string]bool{}
		}
		t.replyable[m.Header.MessageID] = true
	}
}

// read adds a body read to a live mark and reports whether one is live.
func (t *replyOnlyTurn) read(messages []wire.Message) bool {
	if !t.active() {
		return false
	}
	t.mark(nil, messages)
	return true
}

func (t *replyOnlyTurn) end() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.until, t.senders, t.replyable = time.Time{}, nil, nil
}

func (t *replyOnlyTurn) active() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.clock().Before(t.until)
}

// state is the live mark's senders; ok is false without one.
func (t *replyOnlyTurn) state() ([]string, bool) {
	if t == nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.clock().Before(t.until) {
		return nil, false
	}
	return slices.Clone(t.senders), true
}

func (t *replyOnlyTurn) canReply(id string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return id != "" && t.clock().Before(t.until) && t.replyable[id]
}

// turnBackend is the backend that owns this connection's chat inbox and so
// its mark: the chat receiver when one is bound.
func (b *messagesBackend) turnBackend() *messagesBackend {
	b.chatMu.Lock()
	defer b.chatMu.Unlock()
	if b.requireChat && b.chatReceiver != nil {
		return b.chatReceiver
	}
	return b
}

// fullTools reports whether the person allowed full tool use for every
// sender of the marked turn: this receiving program's own rule
// application.message_full_tools on app:<sender>. Any failure is reply-only.
func (b *messagesBackend) fullTools(ctx context.Context, senders []string) bool {
	if len(senders) == 0 || b.decideFullTools == nil {
		return false
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	for _, sender := range senders {
		// A paired computer's sender has no local rule: never full use.
		if strings.HasPrefix(sender, replyOnlyRemote) {
			return false
		}
		decision, err := b.decideFullTools(call, "app:"+sender)
		if err != nil || decision.Outcome != rightswire.DecisionOutcomePermitted {
			return false
		}
	}
	return true
}

// peerFullTools is the watch result's live answer for a marked turn; nil
// when no mark is live.
func (b *messagesBackend) peerFullTools(ctx context.Context) *bool {
	senders, ok := b.replyOnly.state()
	if !ok {
		return nil
	}
	full := b.fullTools(ctx, senders)
	return &full
}

// senderTitle names the turn's first sender in the refusal, as the wake
// prompt names it.
func (b *messagesBackend) senderTitle(ctx context.Context, senders []string) string {
	if len(senders) == 0 || b.Backend == nil {
		return ""
	}
	call, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	page, err := b.Backend.Applications(call, gateway.ApplicationsInput{})
	if err != nil || page.Outcome != "page" {
		return ""
	}
	for _, app := range page.Applications {
		if app.Name == senders[0] {
			return wakeSenderTitle(app)
		}
	}
	return ""
}

// ReplyOnlyRefusal is the gateway's check before any tool call: the
// refusal text for a call a reply-only turn may not make, or "".
func (b *messagesBackend) ReplyOnlyRefusal(ctx context.Context, name string, arguments json.RawMessage) string {
	t := b.turnBackend()
	senders, ok := t.replyOnly.state()
	if !ok || replyOnlyAllowed[name] {
		return ""
	}
	switch name {
	case "oa_messages_reply":
		var in gateway.MessagesReplyInput
		if json.Unmarshal(arguments, &in) == nil && t.replyOnly.canReply(in.MessageID) {
			return ""
		}
	case gateway.ToolApplicationsCall:
		if b.readOnlyCommand(ctx, arguments) {
			return ""
		}
	}
	if t.fullTools(ctx, senders) {
		return ""
	}
	return replyOnlyReason(b.senderTitle(ctx, senders))
}

// readOnlyCommand reports a command call that only reads an open app: an
// app-model read tool, or a command the kept manifest classes read or
// reveal. A lookup that fails is not read-only.
func (b *messagesBackend) readOnlyCommand(ctx context.Context, arguments json.RawMessage) bool {
	var in struct {
		Application string `json:"application"`
		Instance    string `json:"instance"`
		Tool        string `json:"tool"`
	}
	if json.Unmarshal(arguments, &in) != nil || in.Application == "" || in.Tool == "" {
		return false
	}
	if replyOnlyAppTools[in.Tool] {
		return true
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := b.ApplicationsCommands(call, appmodel.CommandsQuery{Application: in.Application, Instance: in.Instance})
	if err != nil || out.Answer == nil || out.Answer.Outcome != appmodel.ReadingOutcomePage {
		return false
	}
	for _, command := range out.Answer.Commands {
		if command.Name == in.Tool {
			return command.Class == appmodel.CommandClassRead || command.Class == appmodel.CommandClassReveal
		}
	}
	return false
}

// replyOnlyRemote prefixes a sender on a paired computer in a mark. No
// local pair rule names it, so its turn never gets full tool use.
const replyOnlyRemote = "remote:"

// replyOnlySender is a mark's name for a message's sender: the local agent's
// application name, or replyOnlyRemote and the sender as the message names it
// for a paired computer's.
func replyOnlySender(from string, remote bool) (string, bool) {
	if remote {
		return replyOnlyRemote + from, from != ""
	}
	name, ok := strings.CutPrefix(from, "app:")
	return name, ok && name != ""
}

// eligibleWakeSenders are the application names whose messages make this
// inbox's wake, as wakeSender reads them.
func (b *messagesBackend) eligibleWakeSenders(headers []wire.Header) []string {
	var names []string
	now := time.Now().UnixMilli()
	for _, h := range headers {
		if h.To != "app:"+b.application || h.Context != b.contextName || h.Refused || h.ExpiresUnixMs <= now {
			continue
		}
		eligible := h.Kind == wire.KindReply || h.Kind == wire.KindNote || h.Kind == wire.KindQuestion && h.TaskState != nil && *h.TaskState == wire.TaskStateSubmitted && h.AnswerByUnixMs > now
		if name, ok := replyOnlySender(h.From, h.RemoteOrigin != nil); eligible && ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// incomingMessages are a page's messages addressed to this inbox's own
// program: the peers' notes, questions and replies a turn reads.
func (b *messagesBackend) incomingMessages(messages []wire.Message) []wire.Message {
	var in []wire.Message
	for _, m := range messages {
		if m.Header.To == "app:"+b.application {
			in = append(in, m)
		}
	}
	return in
}

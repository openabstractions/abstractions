//go:build windows

package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// J-MSG: the person tells OpenCode to message Claude. OpenCode's agent sends
// with one call naming the receiver and the body, the person allows the
// direction once, and Claude's chat receives it. While that Send is in
// flight the runtime stops answering past the presence renewal's budget, as a
// busy machine does; the Send still completes.
func init() { register(20, "J-MSG", runMessages) }

func runMessages(ctx context.Context, e *env, o *options, r *result) {
	a, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !r.check("sender app kept", err == nil, "%v", err) {
		return
	}
	b, err := e.enrollAgent(ctx, "oa-claude-integration", "Claude")
	if !r.check("receiver app kept", err == nil, "%v", err) {
		return
	}
	receiver, err := e.connect(ctx, b, []string{"CLAUDE_CODE_SESSION_ID=" + newUUID()})
	if !r.check("receiver connected", err == nil, "%v", err) {
		return
	}
	defer receiver.Close()
	_, text, _, err := receiver.call(ctx, "oa_claude_session_start", map[string]any{"chat_title": "Journey review"})
	r.check("receiver chat bound", err == nil && !strings.Contains(text, "unavailable"), "%s", clip(firstNonEmpty(text, errString(err)), 300))
	// The chat's presence is admitted shortly after the session starts; an
	// unavailable answer says to try again.
	opened, tries := false, 0
	for ; tries < 5 && !opened; tries++ {
		if tries > 0 {
			time.Sleep(time.Second)
		}
		_, text, _, err = receiver.call(ctx, "oa_messages_inbox_open", map[string]any{})
		opened = err == nil && !strings.Contains(strings.ToLower(text), "unavailable")
	}
	r.check("receiver inbox open", opened, "after %d calls: %s", tries, clip(firstNonEmpty(text, errString(err)), 300))

	sender, err := e.connect(ctx, a, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("sender connected", err == nil, "%v", err) {
		return
	}
	defer sender.Close()
	defer func() { r.Calls = sender.calls }()
	// The OpenCode plugin announces its session on the helper it hosts.
	bound := false
	var startText string
	for i := 0; i < 20 && !bound; i++ {
		_, startText, _, err = sender.call(ctx, "oa_opencode_session_start", map[string]any{"chat_title": "Journey build"})
		bound = err == nil && strings.Contains(startText, `"observed"`)
		if !bound {
			time.Sleep(500 * time.Millisecond)
		}
	}
	r.check("sender chat bound", bound, "%s", clip(firstNonEmpty(startText, errString(err)), 300))
	sender.calls = 0 // host lifecycle calls are not the agent's
	_, _, _, _ = sender.call(ctx, "oa_messages_inbox_open", map[string]any{})
	mark := e.answers.mark()
	body := "journey hello " + newID()[:6]
	send := sender.tools["oa_messages_send"]
	oneCall := send != nil && schemaHas(send, "receiver")
	r.check("send takes receiver and body", oneCall, "oa_messages_send properties: %s", schemaProperties(send))
	sendArgs := map[string]any{"receiver": "Claude", "body": body}
	if !oneCall {
		// The agent prepares, then sends the prepared arguments.
		_, ptext, prepared, perr := sender.call(ctx, "oa_messages_prepare", map[string]any{"receiver": "Claude", "body": body})
		if !r.check("prepare answered", perr == nil, "%v", perr) {
			return
		}
		if sa, ok := prepared["send_arguments"].(map[string]any); ok {
			sendArgs = sa
		} else {
			r.check("prepare gave send arguments", false, "%s", clip(ptext, 400))
		}
	}
	accessKey := "journey-access-" + newID()[:8]
	sent := false
	stalled := false
	var last string
	for step := 0; step < 10 && !sent; step++ {
		var stall chan error
		if !stalled && e.answers.answeredSince(mark) > 0 {
			// The direction is allowed: this Send is the one the stall hits.
			stalled = true
			stall = make(chan error, 1)
			go func() { stall <- e.freezeRuntime(o.stall) }()
			time.Sleep(200 * time.Millisecond)
		}
		_, text, out, err := sender.call(ctx, "oa_messages_send", sendArgs)
		if stall != nil {
			ferr := <-stall
			r.check("runtime stalled past the renewal budget during the Send", ferr == nil, "%v for %s", ferr, o.stall)
			r.check("the Send in flight during the stall completes", err == nil && strings.Contains(text, `"sent"`), "%s", clip(firstNonEmpty(text, errString(err)), 300))
		}
		if err != nil {
			last = err.Error()
			break
		}
		last = text
		outcome, _ := out["outcome"].(string)
		switch {
		case outcome == "sent" || strings.Contains(text, `"outcome":"sent"`):
			sent = true
		case strings.Contains(text, "request_access") || strings.Contains(text, "access:") || outcome == "forbidden" || strings.Contains(text, "needs_permission"):
			// The result names the permission step; the agent asks once and
			// waits for the person.
			_, atext, _, aerr := sender.call(ctx, "oa_messages_request_access", map[string]any{"receiver": "app:oa-claude-integration", "idempotency_key": accessKey})
			if aerr != nil {
				last = aerr.Error()
				break
			}
			if !e.answers.waitAnswered(ctx, mark, 60*time.Second) {
				last = "no permission question reached the person: " + atext
				step = 6
				continue
			}
			r.check("the access request reports the person's Allow", strings.Contains(atext, `"permitted"`), "%s", clip(atext, 300))
			if !strings.Contains(atext, `"permitted"`) {
				_, _, _, _ = sender.call(ctx, "oa_messages_request_access", map[string]any{"receiver": "app:oa-claude-integration", "idempotency_key": accessKey})
			}
			if !oneCall {
				_, _, prepared, _ := sender.call(ctx, "oa_messages_prepare", map[string]any{"receiver": "Claude", "body": body})
				if sa, ok := prepared["send_arguments"].(map[string]any); ok {
					sendArgs = sa
				}
			}
		default:
			// Unavailable or uncertain: the result asks to try again in a few
			// seconds with the identical call.
			time.Sleep(3 * time.Second)
		}
	}
	r.check("message sent", sent, "%s", clip(last, 500))
	permissions := 0
	for _, q := range e.answers.since(mark) {
		if q.Source == "permissions" || strings.Contains(q.Key, "message") {
			permissions++
		}
	}
	r.check("the person was asked once", permissions == 1, "%d message permission questions", permissions)
	if !sent {
		return
	}
	got := false
	var seen string
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) && !got {
		args := map[string]any{}
		if w := receiver.tools["oa_messages_wait"]; w != nil && schemaHas(w, "wait_ms") {
			args["wait_ms"] = 10000
		}
		_, text, _, err := receiver.call(ctx, "oa_messages_wait", args)
		if err != nil {
			seen = err.Error()
			break
		}
		seen = text
		got = strings.Contains(text, body)
	}
	r.check("receiver got the message", got, "%s", clip(seen, 400))
	r.check("few sender calls", sender.calls <= 6, "%d sender tool calls after the chat started (limit 6)", sender.calls)
	if got {
		// Grant the reverse direction through the isolated Panel before restart.
		// The runtime must preserve both permissions along with both inboxes.
		_, text, out, err := receiver.call(ctx, "oa_messages_request_access", map[string]any{
			"receiver": "OpenCode", "idempotency_key": "journey-reverse-" + newID(),
		})
		if !r.check("the isolated Panel allowed Claude to message OpenCode", err == nil && out["outcome"] == "permitted", "%s", clip(firstNonEmpty(text, errString(err)), 350)) {
			return
		}
		checkMessagesRestart(ctx, e, r, sender, receiver)
	}
}

func checkMessagesRestart(ctx context.Context, e *env, r *result, sender, receiver *session) {
	watch := func(ctx context.Context, chat *session) (string, string) {
		res, text, out, err := chat.call(ctx, "oa_messages_watch", map[string]any{"wait_ms": 0})
		if err != nil || res.IsError || out["outcome"] != "page" {
			return "", firstNonEmpty(text, errString(err))
		}
		contextName, _ := out["context"].(string)
		return contextName, text
	}
	senderContext, senderBefore := watch(ctx, sender)
	receiverContext, receiverBefore := watch(ctx, receiver)
	if !r.check("both chat inboxes are live before restart", senderContext != "" && receiverContext != "", "OpenCode: %s; Claude: %s", clip(senderBefore, 300), clip(receiverBefore, 300)) {
		return
	}
	err := e.restartRuntime(ctx)
	if !r.check("isolated runtime restarted", err == nil, "%v", err) {
		return
	}
	recovery, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for _, item := range []struct {
		chat *session
		want string
	}{{sender, senderContext}, {receiver, receiverContext}} {
		var got, last string
		for recovery.Err() == nil {
			got, last = watch(recovery, item.chat)
			if got != "" {
				break
			}
			select {
			case <-recovery.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
		if !r.check(item.chat.name+" reattached its original chat without InboxOpen", got == item.want, "%s", clip(last, 350)) {
			return
		}
	}
	// A zero-wait header read may use retained state before the peer's next
	// renewal. Prepare must also see that peer's live, re-admitted inbox.
	for _, route := range []struct {
		from *session
		to   string
		want string
	}{{sender, "Claude", receiverContext}, {receiver, "OpenCode", senderContext}} {
		var found bool
		var last string
		for recovery.Err() == nil {
			res, text, out, err := route.from.call(recovery, "oa_messages_prepare", map[string]any{"receiver": route.to, "body": "restart discovery check"})
			last = firstNonEmpty(text, errString(err))
			if err == nil && !res.IsError && out["outcome"] == "prepared" {
				found = out["context"] == route.want
				break
			}
			select {
			case <-recovery.Done():
			case <-time.After(time.Second):
			}
		}
		r.check(route.from.name+" finds the same "+route.to+" chat after restart", found, "%s", clip(last, 400))
	}
}

func schemaHas(t *mcp.Tool, name string) bool {
	if t == nil {
		return false
	}
	raw, _ := json.Marshal(t.InputSchema)
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(raw, &schema)
	_, ok := schema.Properties[name]
	return ok
}

func schemaProperties(t *mcp.Tool) string {
	if t == nil {
		return "(tool absent)"
	}
	raw, _ := json.Marshal(t.InputSchema)
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	_ = json.Unmarshal(raw, &schema)
	var names []string
	for n := range schema.Properties {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ",") + " required=" + strings.Join(schema.Required, ",")
}

//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Architect ruling P3, 2026-11-22, end to end: the actual gateway process
// sends by program name through the runtime to two live Claude chats. It
// receives ambiguous_receiver with the ruling's result text and hints and
// nothing is delivered; the resend with the chosen row's context and the
// same idempotency key is delivered there under that key.
func TestMessagesChooseChatThroughGatewayAndRuntime(t *testing.T) {
	run, _ := messages1608Fixture(t)
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	helper := filepath.Join(folder, "oa-codex-integration.exe")
	build := exec.Command("go", "-C", filepath.Join(root, "mcp-gateway"), "test", "-c", "-o", helper, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("gateway fixture build: %v\n%s", err, out)
	}
	sender := wire.ApplicationDescriptor{Name: "oa-codex-integration", Title: "Choose-chat sender", Program: identity.NormalizeSubjectProgram(helper)}
	if got := run.d.register(run.ctx, run.f.panel, sender); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	agent1478Set(t, run, sender.Name, "", true)
	answer(t, run.f, agent1478Question(t, run, sender.Name).ID, "keep")
	agent1478Wait(t, run, sender.Name, "enrolled")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	claudeImage := filepath.Join(folder, "claude-choose-peer.exe")
	if err := os.WriteFile(claudeImage, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, routingClaude, identity.NormalizeSubjectProgram(claudeImage))
	agent1478Set(t, run, routingClaude, "", true)
	answer(t, run.f, agent1478Question(t, run, routingClaude).ID, "keep")
	agent1478Wait(t, run, routingClaude, "enrolled")
	run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	policy := run.f.a.rights
	if err := policy.policy.RegisterAction(ActionApplicationMessage); err != nil {
		t.Fatal(err)
	}
	// The person allows the sender to message Claude; disclosure of the
	// choices depends on it.
	if err := policy.policy.Set(rights.Subject{Account: policy.owner, Program: sender.Program}, ActionApplicationMessage, "app:"+routingClaude, true); err != nil {
		t.Fatal(err)
	}
	appEndpoint := fullEndpoint(t, "choose-chat-applications")
	apps, err := listenApplications(appEndpoint, run.d, policy.owner, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	apps.messages, err = openMessageStore(run.state, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	apps.messageRights = policy
	opts := facaderuntime.Options{Endpoint: fullEndpoint(t, "choose-chat-runtime"), LogEndpoint: fullEndpoint(t, "choose-chat-log"), ConfigEndpoint: fullEndpoint(t, "choose-chat-config"), ConfigWithoutMachine: true, Sink: remoteTestSink(func(logging.Record) error { return nil }), Applications: apps, ApplicationsEndpoint: appEndpoint}
	host, err := facaderuntime.Listen(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := runtimeTestContext(t, run.ctx)
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		host.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})

	// Two live Claude chats, Pictures opened first.
	claudeCall, _ := messages1608CallImage(t, claudeImage)
	claude := &messagesReceiver{host: apps, call: claudeCall, ctx: ctx}
	f := &messagesObserveRun{run: run}
	picturesInstance, pictures := messagesTypedChatAt(t, f, claude, routingClaude, "chat-pictures", "Pictures", "pictures-source")
	time.Sleep(10 * time.Millisecond)
	reportsInstance, reports := messagesTypedChatAt(t, f, claude, routingClaude, "chat-reports", "Reports", "reports-source")
	inbox := func(instance, context string) []msg.Message {
		t.Helper()
		page, err := claude.Wait(instance, context, msg.ViewBody, "", "", 0)
		if err != nil || page.Outcome != msg.OutcomePage {
			t.Fatal("chat inbox", page, err)
		}
		return page.Messages
	}

	config, err := json.Marshal(struct {
		Endpoint, State, Application, Context string
		Server                                listen.ServerExpectation
	}{opts.Endpoint, t.TempDir(), sender.Name, "choose-chat-sender", runtimeExpectation(t)})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, helper, "-test.run=^TestAgentStartGatewayFixture$", "-test.count=1")
	cmd.Env = append(os.Environ(), "OA_RECEIVER_START_GATEWAY_FIXTURE="+string(config))
	var diagnostics bytes.Buffer
	cmd.Stderr = &diagnostics
	session, err := mcp.NewClient(&mcp.Implementation{Name: "choose-chat-host", Version: "fixture"}, nil).Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatal("gateway connection", err, diagnostics.String())
	}
	defer session.Close()
	instructions := session.InitializeResult().Instructions
	if !strings.Contains(instructions, "One live chat receives directly. If several can, the result lists them with hints: pick one and resend with its context, or Prepare with its title.") || strings.Contains(instructions, "newest opened live inbox") {
		t.Fatal("gateway instructions", instructions)
	}
	send := func(args map[string]any) (map[string]any, string) {
		t.Helper()
		var out *mcp.CallToolResult
		var err error
		// The gateway admits tools once its inbox has opened.
		for deadline := time.Now().Add(20 * time.Second); ; {
			out, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_send", Arguments: args})
			if err == nil && !out.IsError || time.Now().After(deadline) {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil || out.IsError {
			t.Fatal("oa_messages_send", out, err, diagnostics.String())
		}
		encoded, err := json.Marshal(out.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(encoded, &result); err != nil {
			t.Fatal(string(encoded), err)
		}
		var text []string
		for _, content := range out.Content {
			if value, ok := content.(*mcp.TextContent); ok {
				text = append(text, value.Text)
			}
		}
		return result, strings.Join(text, "\n")
	}

	// The sender messaged Pictures before: it carries last_exchanged.
	first, _ := send(map[string]any{"receiver": "Claude", "context": pictures, "kind": "note", "body": "Earlier note."})
	if first["outcome"] != "sent" || first["context"] != pictures {
		t.Fatal("earlier note", first)
	}

	const key = "choose-chat-key"
	body := "Please check the draft."
	ambiguous, _ := send(map[string]any{"receiver": "Claude", "kind": "note", "body": body, "idempotency_key": key})
	if ambiguous["outcome"] != "ambiguous_receiver" || ambiguous["send_arguments"] != nil {
		t.Fatal("program-only send to two live chats", ambiguous)
	}
	guidance, _ := ambiguous["guidance"].(string)
	if !strings.HasPrefix(guidance, "Several Claude chats can receive. Choose the one this message belongs to, using the person's words and these hints, and resend with its context.") || !strings.Contains(guidance, `"Pictures" (last_exchanged)`) || !strings.Contains(guidance, "Nothing was sent.") {
		t.Fatal("ambiguous_receiver result text", guidance)
	}
	choices, _ := ambiguous["choices"].([]any)
	if len(choices) != 2 {
		t.Fatal("choices", ambiguous)
	}
	rows := make([]map[string]any, len(choices))
	for i, choice := range choices {
		rows[i], _ = choice.(map[string]any)
		if rows[i]["Program"] != "app:"+routingClaude {
			t.Fatal("choice program", rows[i])
		}
	}
	if rows[0]["Context"] != reports || rows[0]["ChatTitle"] != "Reports" || rows[0]["Hints"] != nil || rows[1]["Context"] != pictures || rows[1]["ChatTitle"] != "Pictures" {
		t.Fatal("choices are not the live chats, most recently opened first", rows)
	}
	if hints, _ := rows[1]["Hints"].([]any); !slices.Equal(hints, []any{"last_exchanged"}) {
		t.Fatal("last_exchanged hint", rows)
	}
	if got := inbox(reportsInstance, reports); len(got) != 0 {
		t.Fatal("an ambiguous send delivered", got)
	}
	if got := inbox(picturesInstance, pictures); len(got) != 1 {
		t.Fatal("an ambiguous send delivered", got)
	}

	// The agent resends with the chosen row's context and the same key.
	resent, text := send(map[string]any{"receiver": "Claude", "context": rows[0]["Context"], "kind": "note", "body": body, "idempotency_key": key})
	if resent["outcome"] != "sent" || resent["context"] != reports || resent["chat_title"] != "Reports" || !strings.Contains(text, `Tell the person the chat title the result names: "Reports".`) {
		t.Fatal("resend with the chosen context", resent, text)
	}
	delivered := inbox(reportsInstance, reports)
	if len(delivered) != 1 || delivered[0].Body != body {
		t.Fatal("chosen chat inbox", delivered)
	}
	record, _, err := apps.messages.readByID(ctx, delivered[0].Header.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	stored, err := decodeRetainedMessageRequest(record.Spec)
	if err != nil || stored.IdempotencyKey != key {
		t.Fatal("the resend did not reuse the idempotency key", stored, err)
	}
}

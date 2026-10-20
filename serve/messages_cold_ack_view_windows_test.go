package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type coldNativeViewProbe struct {
	gateway.Backend
	gateway.MessagesBackend
	r                 *messagesReceiver
	instance, context string
	failReply         bool
}

func (p *coldNativeViewProbe) MessagesWait(_ context.Context, in gateway.MessagesWaitInput) (msg.Page, error) {
	return p.r.Wait(p.instance, p.context, msg.ViewBody, in.After, "", in.WaitMs)
}
func (p *coldNativeViewProbe) MessagesReply(_ context.Context, in gateway.MessagesReplyInput) (msg.SendResult, error) {
	if p.failReply {
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, nil
	}
	return p.r.Reply(p.instance, p.context, in.MessageID, in.Body)
}

func TestMessagesColdAckViewNativeTakeoverRedeliversOnlyUnansweredQuestion(t *testing.T) {
	for _, phase := range []string{"wait-again", "reply-sent", "reply-unknown"} {
		t.Run(phase, func(t *testing.T) {
			f := messagesObserveFixture(t)
			callB, _ := messages1608Call(t)
			cold := &messagesReceiver{host: f.reader.host, call: callB, ctx: f.run.ctx}
			bi, canonical := messagesTypedChatAt(t, f, cold, "native-agent", "same-cold-session", "cold", "cold-source")
			oi, sender := messagesStateOpen(t, f, f.other, "other-agent", "other-source")
			actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "other-source"}
			accepted := f.store.accept(f.run.ctx, actor, f.selfProgram, msg.SendRequest{To: "app:native-agent", Context: canonical, Kind: msg.KindQuestion, Body: "original question", IdempotencyKey: "cold-view", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}, false, "")
			if accepted.Outcome != msg.OutcomeSent {
				t.Fatal(accepted)
			}
			backend := &coldNativeViewProbe{r: cold, instance: bi, context: canonical, failReply: phase == "reply-unknown"}
			st, ct := mcp.NewInMemoryTransports()
			ss, err := gateway.NewMessagesReplyServer(backend).Connect(t.Context(), st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer ss.Close()
			cs, err := mcp.NewClient(&mcp.Implementation{Name: "cold-view-fixture", Version: "1"}, nil).Connect(t.Context(), ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer cs.Close()
			wait := func(after string) msg.Page {
				t.Helper()
				out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0, "after": after}})
				if err != nil || out.IsError {
					t.Fatal(out, err)
				}
				raw, err := json.Marshal(out.StructuredContent)
				if err != nil {
					t.Fatal(err)
				}
				page, err := msg.DecodePage(raw)
				if err != nil {
					t.Fatal(string(raw), err)
				}
				return *page
			}
			page := wait("")
			if len(page.Messages) != 1 || page.Messages[0].Header.MessageID != accepted.MessageID {
				t.Fatal(page)
			}
			if phase != "wait-again" {
				out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolMessagesReply, Arguments: map[string]any{"message_id": accepted.MessageID, "body": "one answer"}})
				if err != nil || out.IsError {
					t.Fatal(out, err)
				}
			}
			wait(page.Cursor)
			callC, _ := messages1608Call(t)
			ui := &messagesReceiver{host: f.reader.host, call: callC, ctx: f.run.ctx}
			ci, again := messagesTypedChatAt(t, f, ui, "native-agent", "same-cold-session", "late UI", "ui-source")
			if again != canonical {
				t.Fatal("canonical context changed", again, canonical)
			}
			late, err := ui.Wait(ci, canonical, msg.ViewBody, "", "", 0)
			if err != nil || late.Outcome != msg.OutcomePage {
				t.Fatal(late, err)
			}
			unanswered := 0
			for _, message := range late.Messages {
				if message.Header.Kind == msg.KindQuestion && (message.Header.TaskState == nil || *message.Header.TaskState == msg.TaskStateSubmitted) {
					unanswered++
				}
			}
			if phase == "reply-sent" {
				if unanswered != 0 {
					t.Fatal("accepted reply became another question", late)
				}
			} else {
				if unanswered != 1 {
					t.Fatal("unanswered body lost across takeover", late)
				}
				if out, err := ui.Reply(ci, canonical, accepted.MessageID, "one answer"); err != nil || out.Outcome != msg.OutcomeSent {
					t.Fatal(out, err)
				}
			}
			answers, err := sender.Wait(oi, "other-source", msg.ViewBody, "", "", 0)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, message := range answers.Messages {
				if message.Header.Kind == msg.KindReply && message.Body == "one answer" {
					count++
				}
			}
			if count != 1 {
				t.Fatal("expected one original-question reply", answers)
			}
		})
	}
}

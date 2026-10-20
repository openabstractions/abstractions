package main

import (
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// This inert native probe covers OA ownership separately from vendor transcript
// interleaving. The current single-owner boundary retires prior helpers; a
// cold helper cannot claim continued receiving authority after late UI attach.
func TestMessagesColdResumeProbeSameSessionLateUIKeepsCustodyAndFencesRetiredHelpers(t *testing.T) {
	for _, acknowledge := range []bool{false, true} {
		t.Run(map[bool]string{false: "queued", true: "body-acknowledged"}[acknowledge], func(t *testing.T) {
			f := messagesObserveFixture(t)
			ai, canonical := messagesTypedChatAt(t, f, f.reader, "native-agent", "same-session", "UI", "ui-source")
			oi, sender := messagesStateOpen(t, f, f.other, "other-agent", "other-source")
			actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "other-source"}
			request := msg.SendRequest{To: "app:native-agent", Context: canonical, Kind: msg.KindQuestion, Body: "fixture question", IdempotencyKey: "cold-race", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
			accepted := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
			if accepted.Outcome != msg.OutcomeSent {
				t.Fatal(accepted)
			}
			callB, _ := messages1608Call(t)
			cold := &messagesReceiver{host: f.reader.host, call: callB, ctx: f.run.ctx}
			bi, second := messagesTypedChatAt(t, f, cold, "native-agent", "same-session", "cold helper", "cold-source")
			read, err := cold.Wait(bi, canonical, msg.ViewBody, "", "", 0)
			if err != nil || len(read.Messages) != 1 || read.Messages[0].Header.MessageID != accepted.MessageID {
				t.Fatal(read, err)
			}
			if acknowledge {
				if out, err := cold.Wait(bi, canonical, msg.ViewBody, read.Cursor, "", 0); err != nil || out.Outcome != msg.OutcomePage {
					t.Fatal("cold body cursor ACK", out, err)
				}
			}
			callC, _ := messages1608Call(t)
			ui := &messagesReceiver{host: f.reader.host, call: callC, ctx: f.run.ctx}
			ci, third := messagesTypedChatAt(t, f, ui, "native-agent", "same-session", "late UI", "late-ui-source")
			if canonical != second || second != third {
				t.Fatal("same host session forked its canonical inbox", canonical, second, third)
			}
			for attempt := 0; attempt < 3; attempt++ {
				for _, old := range []struct {
					r                *messagesReceiver
					instance, source string
				}{{f.reader, ai, "ui-source"}, {cold, bi, "cold-source"}} {
					out, err := old.r.InboxOpenChat(old.instance, old.source, "", msg.ChatBinding{ChatID: "same-session", SourceInstance: old.instance})
					if err != nil || out.Outcome != msg.OutcomeEnded {
						t.Fatal("retired helper reclaimed receiving ownership", out, err)
					}
				}
			}
			if out, err := cold.Reply(bi, canonical, accepted.MessageID, "retired cold answer"); err == nil && out.Outcome == msg.OutcomeSent {
				t.Fatal("retired cold helper replied", out)
			}
			page, err := ui.Wait(ci, canonical, msg.ViewBody, "", "", 0)
			want := 1
			if acknowledge {
				want = 0
			}
			if err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != want || !acknowledge && page.Messages[0].Header.MessageID != accepted.MessageID {
				t.Fatal("handoff lost canonical body custody", page, err)
			}
			if out, err := ui.Reply(ci, canonical, accepted.MessageID, "fixture answer"); err != nil || out.Outcome != msg.OutcomeSent {
				t.Fatal(out, err)
			}
			if page, err := sender.Wait(oi, "other-source", msg.ViewBody, "", "", 0); err != nil || !chatPageHasReply(page, "fixture answer") {
				t.Fatal(page, err)
			}
		})
	}
}

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type chatAllocationOutcomeProbe struct {
	*statusMessagesProbe
	result        msg.ChatInboxChange
	allocationErr error
	calls         int
}

func (p *chatAllocationOutcomeProbe) InboxOpenChat(string, string, string, msg.ChatBinding) (msg.ChatInboxChange, error) {
	p.calls++
	return p.result, p.allocationErr
}

func TestChatAllocationRefusalsAndMalformedSuccessNeverBecomeAdmissionRetry(t *testing.T) {
	for outcome := msg.OutcomeSent; outcome <= msg.OutcomeEnded; outcome++ {
		for _, contextName := range []string{"", "wrong-context"} {
			p := &chatAllocationOutcomeProbe{statusMessagesProbe: new(statusMessagesProbe), result: msg.ChatInboxChange{Outcome: outcome, Context: contextName}}
			b := &messagesBackend{application: "oa-codex-integration", instance: "source-instance", contextName: "source", chatID: codexAttachThread, chatContext: fixtureNativeChatContext(codexAttachThread), native: func(context.Context) (nativeMessages, error) { return p, nil }}
			b.chatPresence = func(context.Context, string, *messagesInboxStatus, func(string), bool) error {
				t.Fatal("failed allocation advertised receiving")
				return nil
			}
			receiver, stop, err := b.newChatReceiver(t.Context())
			retry := outcome == msg.OutcomeUnavailable || outcome == msg.OutcomeUnknown
			if err == nil || errors.Is(err, errChatSourceAdmissionUnavailable) != retry || receiver != nil || stop != nil || b.chatReceiver != nil || p.calls != 1 {
				t.Fatal(outcome, contextName, receiver, err, p.calls)
			}
		}
	}
	p := &chatAllocationOutcomeProbe{statusMessagesProbe: new(statusMessagesProbe), allocationErr: &msg.Refusal{Word: "unknown_field"}}
	b := &messagesBackend{application: "oa-codex-integration", instance: "source-instance", contextName: "source", chatID: codexAttachThread, native: func(context.Context) (nativeMessages, error) { return p, nil }, chatPresence: func(context.Context, string, *messagesInboxStatus, func(string), bool) error { return nil }}
	if _, _, err := b.newChatReceiver(t.Context()); err == nil || errors.Is(err, errChatSourceAdmissionUnavailable) || p.calls != 1 {
		t.Fatal("protocol refusal entered admission retry", err, p.calls)
	}
}

func TestChatAllocationFactoryAndReplyErrorClassification(t *testing.T) {
	for _, factory := range []bool{false, true} {
		for _, tc := range []struct {
			err   error
			retry bool
		}{
			{io.EOF, true},
			{&client.ResolutionError{Status: client.RuntimeUnavailable}, true},
			{&client.ResolutionError{Status: client.ResolutionErrorStatus("forbidden")}, false},
			{&msg.Refusal{Word: "unknown_field"}, false},
			{&msg.Refusal{Word: "bad_string"}, false},
			{msg.DispatchError("mismatched_response"), false},
			{listen.ErrServerUntrusted, false},
		} {
			p := &chatAllocationOutcomeProbe{statusMessagesProbe: new(statusMessagesProbe), allocationErr: tc.err}
			b := &messagesBackend{application: "oa-codex-integration", instance: "original-source", contextName: "original-context", chatID: codexAttachThread, native: func(context.Context) (nativeMessages, error) {
				if factory {
					return nil, tc.err
				}
				return p, nil
			}, chatPresence: func(context.Context, string, *messagesInboxStatus, func(string), bool) error {
				t.Fatal("failed allocation advertised")
				return nil
			}}
			_, _, err := b.newChatReceiver(t.Context())
			if !errors.Is(err, tc.err) || errors.Is(err, errChatSourceAdmissionUnavailable) != tc.retry {
				t.Fatal(factory, tc.err, err, tc.retry)
			}
			if factory && p.calls != 0 {
				t.Fatal("factory failure dispatched", p.calls)
			}
		}
	}
	for _, sourceGate := range []bool{false, true} {
		cause := errors.New("unclassified factory failure")
		b := &messagesBackend{chatID: codexAttachThread, native: func(context.Context) (nativeMessages, error) { return nil, cause }, chatPresence: func(context.Context, string, *messagesInboxStatus, func(string), bool) error { return nil }}
		if sourceGate {
			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(time.Minute))
			b.status = &messagesInboxStatus{admission: a}
		}
		if _, _, err := b.newChatReceiver(t.Context()); !errors.Is(err, cause) || errors.Is(err, errChatSourceAdmissionUnavailable) {
			t.Fatal("generic factory error replayed", sourceGate, err)
		}
	}
}

func configureInertChatPresence(b *messagesBackend, directory *chaosRuntime) {
	b.chatPresence = func(ctx context.Context, contextName string, status *messagesInboxStatus, ready func(string), readmit bool) error {
		return agentPresenceAttempt(ctx, directory, func(ctx context.Context) nativeMessages {
			client, err := b.native(ctx)
			if err != nil {
				panic(err)
			}
			return client
		}, b.application, contextName, ready, status, readmit)
	}
}

type chatSendVersionProbe struct {
	*messagesProbe
	chatCalls int
}

func (p *chatSendVersionProbe) SendChat(string, string, msg.ChatSendRequest) (msg.SendResult, error) {
	p.chatCalls++
	return msg.SendResult{}, &msg.ServiceError{Code: msg.ServiceErrorCodeUnknownMethod}
}

func TestCodexRequiredChatSendNeverDowngradesOnOldRuntime(t *testing.T) {
	p := &chatSendVersionProbe{messagesProbe: new(messagesProbe)}
	b := &messagesBackend{instance: "source-instance", contextName: "source", requireChat: true, chatContext: "canonical", native: func(context.Context) (nativeMessages, error) { return p, nil }}
	out, err := b.MessagesSend(t.Context(), gateway.MessagesSendInput{To: "app:receiver", Context: "target", Kind: "question", Body: "question", IdempotencyKey: "key", ExpiresUnixMs: 2000, AnswerByUnixMs: 1500})
	if err != nil || out.Outcome != msg.OutcomeUnavailable || out.Reason != "dispatch_unknown_method" || p.chatCalls != 1 || p.calls != 0 {
		t.Fatal(out, err, p)
	}
	b.chatContext = ""
	out, err = b.MessagesSend(t.Context(), gateway.MessagesSendInput{To: "app:receiver", Context: "target", Kind: "note", Body: "note", IdempotencyKey: "key", ExpiresUnixMs: 2000})
	if err != nil || out.Outcome != msg.OutcomeSent || p.chatCalls != 1 || p.calls != 1 || p.instance != "source-instance" || p.contextName != "source" {
		t.Fatal("unbound helper lost actual source-only sending", out, err, p)
	}
}

func fixtureNativeChatContext(chat string) string {
	sum := sha256.Sum256([]byte("inert-runtime-store-owner\x00" + chat))
	return "chat-" + hex.EncodeToString(sum[:24])
}

func TestCodexHostChatBindingRetainsFirstIDWithoutAllocatingAddress(t *testing.T) {
	b := new(messagesBackend)
	if err := b.bindChat(codexAttachThread); err != nil {
		t.Fatal(err)
	}
	if b.chatID != codexAttachThread || b.chatContext != "" {
		t.Fatal("adapter allocated a native address locally", b.chatID, b.chatContext)
	}
	if err := b.bindChat(codexOtherThread); err == nil {
		t.Fatal("changed host ID accepted")
	}
	if err := b.bindChat("invalid"); err == nil {
		t.Fatal("invalid host ID accepted")
	}
	if b.chatID != codexAttachThread {
		t.Fatal("refusal replaced retained binding")
	}
}

func TestCodexCanonicalPresenceKeepsConcurrentSourceSeparate(t *testing.T) {
	runtime := newChaosRuntime()
	makeBackend := func(source string) *messagesBackend {
		presence, err := runtime.Announce(t.Context(), facade.ApplicationPresence{Application: "oa-codex-integration", LeaseMs: 60000, Contexts: []facade.ApplicationContext{{Name: source}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{}); err != nil {
			t.Fatal(err)
		}
		b := &messagesBackend{application: "oa-codex-integration", instance: presence.Instance, contextName: source, native: func(context.Context) (nativeMessages, error) { return runtime, nil }}
		b.chatPresence = func(ctx context.Context, contextName string, status *messagesInboxStatus, ready func(string), readmit bool) error {
			return agentPresenceAttempt(ctx, runtime, func(context.Context) nativeMessages { return runtime }, b.application, contextName, ready, status, readmit)
		}
		if err := b.bindChat(codexAttachThread); err != nil {
			t.Fatal(err)
		}
		return b
	}
	a, b := makeBackend("source-a"), makeBackend("source-b")
	first, stopA, err := a.newChatReceiver(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stopA()
	deadline := time.Now().Add(3 * time.Second)
	for first.inboxInstance() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if first.inboxInstance() == "" {
		t.Fatal("canonical presence not admitted")
	}
	second, stopB, err := b.newChatReceiver(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer stopB()
	if first.contextName != second.contextName || first.contextName == a.contextName || second.contextName == b.contextName {
		t.Fatal("source and receiving identities collapsed")
	}
	deadline = time.Now().Add(3 * time.Second)
	for second.inboxInstance() == "" && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if second.inboxInstance() == "" {
		t.Fatal("newer source did not take over canonical chat")
	}
	interfaces, _ := first.status.receivingPresence()
	if len(interfaces) != 0 {
		t.Fatal(interfaces)
	}
	if out, _ := runtime.InboxOpen("foreign", first.contextName, ""); out.Outcome != msg.OutcomeForbidden {
		t.Fatal(out)
	}
}

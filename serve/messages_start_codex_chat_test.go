package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

const codexChatThread = "019a6c1e-2b3d-7e4f-8a5b-6c7d8e9f0a1b"

// A question to a retained Codex chat is queued into that chat's own thread
// while the Codex app runs. While it is closed nothing starts and nobody is
// asked: the question waits in the chat until the person opens it.
func TestMessagesRetainedCodexChatStartsOnlyWhileCodexRuns(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixtureWith(t, "", "codex-v1", &msg.ChatBinding{ChatID: codexChatThread})
	running := false
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return running, nil }
	asked := 0
	h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
		asked++
		return "pending"
	}
	if err := h.progressMessageStartSettled(t.Context(), key, r); err != nil {
		t.Fatal(err)
	}
	if stored, err := h.messages.readRecord(t.Context(), key); err != nil || stored.Start != nil || asked != 0 || calls.Load() != 0 {
		t.Fatal("closed Codex claimed, asked or started", stored, asked, calls.Load(), err)
	}
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return true, errors.New("process list unavailable") }
	if err := h.progressMessageStartSettled(t.Context(), key, r); err != nil || asked != 0 || calls.Load() != 0 {
		t.Fatal("unknown Codex state counted as running", asked, calls.Load(), err)
	}
	h.directory.messageCodexRunning = func(context.Context) (bool, error) { return running, nil }
	running = true
	allowMessageStartFixture(t, h, key, r)
	var selected applicationMessageStartRequest
	h.directory.launchMessageStart = func(_ context.Context, request applicationMessageStartRequest, _ func(context.Context) error) error {
		calls.Add(1)
		selected = request
		return nil
	}
	if err := h.progressMessageStartSettled(t.Context(), key, r); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || selected.Cold == nil {
		t.Fatal("running Codex chat not started", calls.Load(), selected)
	}
	thread, err := selected.Cold.Thread()
	if err != nil || thread != codexChatThread || selected.Target != selected.Cold.Context || !messageCanonicalChatContext(selected.Target) {
		t.Fatal("start did not name the chat's own thread", thread, selected.Target, err)
	}
}

// Where starts are limited to existing chats, a question to any other
// context, the recipe's own included, starts nothing and asks nothing.
func TestMessagesChatsOnlyNeverStartsANewSession(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixture(t)
	h.directory.messageStartChatsOnly = true
	asked := 0
	h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
		asked++
		return "pending"
	}
	if err := h.progressMessageStartSettled(t.Context(), key, r); err != nil {
		t.Fatal(err)
	}
	if stored, err := h.messages.readRecord(t.Context(), key); err != nil || stored.Start != nil || asked != 0 || calls.Load() != 0 {
		t.Fatal("chats-only start ran for the recipe's own context", stored, asked, calls.Load(), err)
	}
}

// A retained chat is listed as starting on a question only when its
// receiver's recipe can wake it now; otherwise the message waits there.
func TestRetainedChatInboxReach(t *testing.T) {
	folder := t.TempDir()
	recipe := func(name string) applicationMessageStartRequest {
		p := startprofile.Profile{Name: name, Context: "ctx", Folder: folder, StateDir: folder, CLIFileID: "inert", Model: "inert", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 1}
		return applicationMessageStartRequest{Descriptor: wire.ApplicationDescriptor{Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}}
	}
	codex := messageChatBindingRecord{Context: "chat-codex", Chat: codexChatThread, Title: "Codex chat"}
	launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: folder, Origin: msg.ChatOriginDesktop, Mode: msg.ChatModePlan}
	key, err := messageChatLaunchKey(launch.Session, launch)
	if err != nil {
		t.Fatal(err)
	}
	claude := messageChatBindingRecord{Context: "chat-claude", Chat: launch.Session, Launch: launch, CwdKey: key}
	var probes atomic.Int32
	running := func(state bool) messageAppsRunning {
		probe := func() bool { probes.Add(1); return state }
		return messageAppsRunning{codex: probe}
	}
	reach := func(inbox msg.Inbox) msg.InboxReach { return *inbox.Reach }
	for _, c := range []struct {
		name    string
		chat    messageChatBindingRecord
		profile applicationMessageStartRequest
		err     error
		running bool
		want    msg.InboxReach
	}{
		{"codex-running", codex, recipe("codex-v1"), nil, true, msg.InboxReachStartsOnQuestion},
		{"codex-closed", codex, recipe("codex-v1"), nil, false, msg.InboxReachQueued},
		{"no-recipe", codex, applicationMessageStartRequest{}, errors.New("unregistered"), true, msg.InboxReachQueued},
		{"claude-recipe-codex-chat", codex, recipe("claude-v1"), nil, true, msg.InboxReachQueued},
		{"claude-launch", claude, recipe("claude-v1"), nil, false, msg.InboxReachStartsOnQuestion},
		{"claude-running", claude, recipe("claude-v1"), nil, true, msg.InboxReachStartsOnQuestion},
		{"codex-recipe-claude-chat", claude, recipe("codex-v1"), nil, true, msg.InboxReachQueued},
	} {
		inbox := retainedChatInbox("receiver", c.chat, c.profile, c.err, running(c.running))
		if reach(inbox) != c.want || inbox.Context != c.chat.Context || inbox.ChatTitle != c.chat.Title {
			t.Fatal(c.name, inbox)
		}
	}
	if probes.Load() != 2 {
		t.Fatal("app state probed for a chat its recipe cannot wake", probes.Load())
	}
}

// A chats-only start of a retained Claude or Codex chat writes its Activity
// row with the delivery the launcher reported and the exact start permit.
// Revoking the direction from that row refuses the next start: nothing is
// claimed and nobody is asked again.
func TestMessagesChatStartActivityRowAndRevocation(t *testing.T) {
	for _, c := range []struct {
		recipe, delivery string
		chat             func(t *testing.T) *msg.ChatBinding
	}{
		{"claude-v1", messageStartResumed, func(t *testing.T) *msg.ChatBinding {
			launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: t.TempDir(), Origin: msg.ChatOriginDesktop, Mode: msg.ChatModePlan}
			return &msg.ChatBinding{ChatID: launch.Session, Launch: launch}
		}},
		{"codex-v1", messageStartDesktopQueue, func(*testing.T) *msg.ChatBinding { return &msg.ChatBinding{ChatID: codexChatThread} }},
	} {
		t.Run(c.recipe, func(t *testing.T) {
			h, key, r, calls, _ := messagesStartProgressFixtureWith(t, "", c.recipe, c.chat(t))
			h.directory.messageStartChatsOnly = true
			h.directory.messageCodexRunning = func(context.Context) (bool, error) { return true, nil }
			sink := &messageAuditSink{}
			if err := h.configureMessageAudit(sink); err != nil {
				t.Fatal(err)
			}
			defer h.messageAudit.close()
			audited := func(key string, r *messageRecord) *messageRecord {
				t.Helper()
				if err := h.messages.updateMessageNotice(t.Context(), key, r.Token, func(current *messageRecord) {
					entry, err := h.messageAudit.prepare(current)
					if err != nil {
						t.Fatal(err)
					}
					current.Audit = entry
				}); err != nil {
					t.Fatal(err)
				}
				current, err := h.messages.readRecord(t.Context(), key)
				if err != nil {
					t.Fatal(err)
				}
				return current
			}
			current := audited(key, r)
			h.directory.launchMessageStart = func(_ context.Context, request applicationMessageStartRequest, _ func(context.Context) error) error {
				calls.Add(1)
				request.Delivered(c.delivery)
				return nil
			}
			allowMessageStartFixture(t, h, key, current)
			if err := h.progressMessageStartSettled(t.Context(), key, current); err != nil || calls.Load() != 1 {
				t.Fatal("chat start", err, calls.Load())
			}
			row := map[string]string{}
			for _, record := range sink.records {
				if record.Attrs["message_id"] == r.ID+":start" && record.Attrs["delivery"] != "" {
					row = record.Attrs
				}
			}
			if row["delivery"] != c.delivery || row["permit_resource"] == "" || row["permit_action"] == "" || row["permit_program"] == "" {
				t.Fatal("Activity row without delivery or permit", row)
			}
			// The person revokes the direction from the row.
			if err := h.messageRights.policy.Set(rights.Subject{Account: r.Actor.Account, Program: row["permit_program"]}, row["permit_action"], row["permit_resource"], false); err != nil {
				t.Fatal(err)
			}
			request, err := decodeRetainedMessageRequest(r.Spec)
			if err != nil {
				t.Fatal(err)
			}
			request.IdempotencyKey, request.Subject, request.Body = "second-question", "second", "second private body"
			if got := h.messages.accept(t.Context(), r.Actor, r.Receiver, *request, false, ""); got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			nextKey := h.messages.key(r.Actor, request.IdempotencyKey)
			next, err := h.messages.readRecord(t.Context(), nextKey)
			if err != nil {
				t.Fatal(err)
			}
			asked := 0
			h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
				asked++
				return "pending"
			}
			if err := h.progressMessageStartSettled(t.Context(), nextKey, audited(nextKey, next)); err != nil {
				t.Fatal(err)
			}
			stored, err := h.messages.readRecord(t.Context(), nextKey)
			if err != nil || stored.Start != nil || asked != 0 || calls.Load() != 1 {
				t.Fatal("a revoked direction started or asked again", stored, asked, calls.Load(), err)
			}
		})
	}
}

// T8: a discovered Claude session whose records state no origin lists queued
// and never starts; with its origin stated it starts on a question.
func TestDiscoveredClaudeWithoutOriginQueues(t *testing.T) {
	folder := t.TempDir()
	p := startprofile.Profile{Name: "claude-v1", Context: "ctx", Folder: folder, StateDir: folder, CLIFileID: "inert", Model: "inert", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 1}
	profile := applicationMessageStartRequest{Descriptor: wire.ApplicationDescriptor{Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}}
	running := messageAppsRunning{codex: func() bool { return false }}
	work := filepath.Join(folder, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	s := discoveredSessionFixture(t, applicationAttachSession{Context: "chat-discovered", Chat: "11111111-1111-4111-8111-111111111111", Cwd: work, Updated: 120000, Mode: "default"})
	if inbox := discoveredChatInbox("receiver", s, profile, nil, running); *inbox.Reach != msg.InboxReachQueued || inbox.Status != "Not open in work" || inbox.ChatTitle != "work" {
		t.Fatal("no origin", inbox)
	}
	s.Origin = "cli"
	if inbox := discoveredChatInbox("receiver", s, profile, nil, running); *inbox.Reach != msg.InboxReachStartsOnQuestion {
		t.Fatal("stated origin", inbox)
	}
}

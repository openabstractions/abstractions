//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func sameAppOn(t *testing.T) {
	prior := messageSameAppReceivers
	messageSameAppReceivers = true
	t.Cleanup(func() { messageSameAppReceivers = prior })
}

// S3 (architect ruling 2026-11-09): the person grants app:X → app:X through
// the existing request-access question. The question names only that
// direction, and its Allow writes the one exact rule.
func TestMessagePermissionSameAppGrantPath(t *testing.T) {
	sameAppOn(t)
	f, client, operator := messagePermissionFixture(t)
	ctx, cancel := context.WithTimeout(f.run.ctx, 60*time.Second)
	defer cancel()
	a := f.run.f.a
	// An excluded (shared) caller is refused with the gate on, too.
	excluded := f.run.d.agentExcluded
	f.run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return true, nil }
	shared, err := client.RequestContext(ctx, messagePermissionIntent("native-agent", "same-app-shared"))
	f.run.d.agentExcluded = excluded
	if err != nil || shared.Outcome == askswire.PermissionOutcomePending || shared.Outcome == askswire.PermissionOutcomePermitted {
		t.Fatal("a shared caller asked for the same-app direction", shared, err)
	}
	out, err := client.RequestContext(ctx, messagePermissionIntent("native-agent", "same-app-access"))
	if err != nil || out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil {
		t.Fatal("the same-app direction raised no question", out, err)
	}
	id, names := pendingMessageQuestion(t, f)
	if id != out.Answer.ID || len(names) != 1 || names[0] != "native-agent" {
		t.Fatal("a same-app question named other receivers", names)
	}
	// Only the person answers: an agent answerer is refused.
	pending, err := a.book.PendingTypedPermissions()
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if _, err := a.authorizeAnswerer(ctx, answerer{subject: f.self}, pending[0], a.scope, "allow"); err == nil {
		t.Fatal("an agent answered the same-app question")
	}
	if answered, err := operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil || answered.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(answered, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, id, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied || len(edit.Current) != 1 {
		t.Fatal("the same-app Allow did not write its rule", edit, err)
	}
	subject := edit.Current[0].Rule.Subject
	if got := a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:native-agent"); got.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal("same-app direction not permitted", got)
	}
	if got := a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:other-agent"); got.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("the same-app Allow granted another receiver", got)
	}
}

// S3/S4: the listing offers the same-app direction in needs_permission only
// to a bound sender, the same test Send applies, and never lists the
// sender's own chat as a receiver.
func TestSameAppListingOffersGrantToBoundSenderOnly(t *testing.T) {
	sameAppOn(t)
	for _, bound := range []bool{true, false} {
		f := messagesDiscoveryFixture(t)
		source := "plain-context"
		if bound {
			source = f.store.chatContextName(f.run.d.owner, f.selfProgram, "sender-chat")
			if _, err := f.store.contextForChat(f.run.ctx, f.run.d.owner, f.selfProgram, "sender-chat", &msg.ChatBinding{ChatID: "sender-chat"}); err != nil {
				t.Fatal(err)
			}
		}
		_, sender := messagesStateOpen(t, f, f.reader, "native-agent", source)
		page, err := sender.InboxesList(msg.InboxQuery{})
		if err != nil || page.Outcome != msg.OutcomePage {
			t.Fatal(page, err)
		}
		offered := false
		for _, need := range page.NeedsPermission {
			offered = offered || need.Program == "app:native-agent"
		}
		if offered != bound {
			t.Fatal("same-app grant offered", bound, page.NeedsPermission)
		}
		for _, row := range page.Inboxes {
			if row.Program == "app:native-agent" && row.Context == source && row.Mine == nil {
				t.Fatal("the sender listed its own chat as a receiver", row)
			}
		}
	}
}

// E2: a same-app sender reaches only chats of its own project (Git main
// folder, linked worktrees included); a chat of another project is neither
// listed nor sent to.
func TestSameAppReachesOnlySenderProject(t *testing.T) {
	sameAppOn(t)
	f := messagesDiscoveryFixture(t)
	p, worktree, q := discoveredFixtureProjects(t)
	owner := f.run.d.owner
	source := f.store.chatContextName(owner, f.selfProgram, "sender-chat")
	if _, err := f.store.contextForChat(f.run.ctx, owner, f.selfProgram, "sender-chat", &msg.ChatBinding{ChatID: "sender-chat", Launch: &msg.ChatLaunch{Session: "sender-chat", Origin: msg.ChatOriginDesktop, Cwd: p, Mode: msg.ChatModeDefault}}); err != nil {
		t.Fatal(err)
	}
	sibling := applicationAttachSession{Chat: "thread-sibling", Cwd: worktree, Folder: "sibling", Project: messageProject(worktree), Updated: time.Now().UnixMilli()}
	stranger := applicationAttachSession{Chat: "thread-stranger", Cwd: q, Folder: "stranger", Project: messageProject(q), Updated: time.Now().UnixMilli()}
	sibling.Context = f.store.chatContextName(owner, f.selfProgram, sibling.Chat)
	stranger.Context = f.store.chatContextName(owner, f.selfProgram, stranger.Chat)
	f.run.d.messageAttachSessions = func(_ context.Context, application string) ([]applicationAttachSession, error) {
		if application == "native-agent" {
			return []applicationAttachSession{sibling, stranger}, nil
		}
		return nil, nil
	}
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", source)
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	// The project scope under test is the whole listing Prepare and Send
	// read; the public listing counts chats with no thread (architect ruling
	// 2026-11-12).
	if public, err := sender.InboxesList(msg.InboxQuery{}); err != nil || public.Outcome != msg.OutcomePage {
		t.Fatal(public, err)
	}
	page := f.reader.inboxesList("")
	if page.Outcome != msg.OutcomePage {
		t.Fatal(page)
	}
	listed := map[string]bool{}
	for _, row := range page.Inboxes {
		listed[row.Context] = true
	}
	if !listed[sibling.Context] || listed[stranger.Context] {
		t.Fatal("same-app listing outside the sender's project", page.Inboxes)
	}
	send := func(contextName, key string) msg.SendResult {
		t.Helper()
		out, err := sender.Send(instance, source, msg.SendRequest{To: "app:native-agent", Context: contextName, Kind: msg.KindNote, Body: key, IdempotencyKey: key, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := send(stranger.Context, "to-stranger"); out.Outcome != msg.OutcomeUnavailable || out.Reason != "session_unknown" {
		t.Fatal("a same-app send left the project", out)
	}
	if out := send(sibling.Context, "to-sibling"); out.Outcome != msg.OutcomeSent {
		t.Fatal("a same-app send within the project", out)
	}
}

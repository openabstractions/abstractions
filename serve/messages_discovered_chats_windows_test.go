package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// discoveredFixtureProjects makes project P (a Git main folder with a linked
// worktree) and project Q.
func discoveredFixtureProjects(t *testing.T) (p, worktree, q string) {
	t.Helper()
	p, worktree, q = t.TempDir(), t.TempDir(), t.TempDir()
	common := filepath.Join(p, ".git")
	link := filepath.Join(common, "worktrees", "w")
	if err := os.MkdirAll(link, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(link, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.ToSlash(link)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, worktree, q
}

func TestMessageProjectResolvesLinkedWorktree(t *testing.T) {
	p, worktree, q := discoveredFixtureProjects(t)
	sub := filepath.Join(p, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if messageProject(p) == "" || messageProject(worktree) != messageProject(p) || messageProject(sub) != messageProject(p) || messageProject(q) == messageProject(p) {
		t.Fatal("project identity", messageProject(p), messageProject(worktree), messageProject(sub), messageProject(q))
	}
}

func TestMessageProjectRequiresExistingCommonDirectory(t *testing.T) {
	main, worktree, gitDir := t.TempDir(), t.TempDir(), t.TempDir()
	common := filepath.Join(main, ".git")
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+filepath.ToSlash(gitDir)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(filepath.ToSlash(common)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	independent := messageProject(worktree)
	if independent == "" || independent == messageProject(main) {
		t.Fatal("missing common directory joined an unrelated project", independent)
	}
	if err := os.WriteFile(common, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := messageProject(worktree); got != independent {
		t.Fatal("regular file changed project", got, independent)
	}
	if err := os.Remove(common); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(common, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := messageProject(worktree); got != messageProject(main) || got == independent {
		t.Fatal("existing common directory did not join the main project", got)
	}
}

// T2, T3, T4, T5, T9: discovered conversations list as chat-* rows under the
// direction's message permit, one row per conversation with the retained
// title first, scoped to the sender's project, never the sender's own.
func TestDiscoveredChatsListScopedOnceUnderPermit(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	p, worktree, q := discoveredFixtureProjects(t)
	owner := f.run.d.owner
	name := func(program, chat string) string { return f.store.chatContextName(owner, program, chat) }
	// The sender is a retained conversation in project P.
	source := name(f.selfProgram, "sender-chat")
	if _, err := f.store.contextForChat(f.run.ctx, owner, f.selfProgram, "sender-chat", &msg.ChatBinding{ChatID: "sender-chat", Launch: &msg.ChatLaunch{Session: "sender-chat", Origin: msg.ChatOriginDesktop, Cwd: p, Mode: msg.ChatModeDefault}}); err != nil {
		t.Fatal(err)
	}
	main := discoveredSessionFixture(t, applicationAttachSession{Context: name(f.otherProgram, "thread-main"), Chat: "thread-main", Cwd: p, Updated: time.Now().UnixMilli(), Title: "Vendor title"})
	linked := discoveredSessionFixture(t, applicationAttachSession{Context: name(f.otherProgram, "thread-linked"), Chat: "thread-linked", Cwd: worktree, Updated: time.Now().UnixMilli()})
	other := discoveredSessionFixture(t, applicationAttachSession{Context: name(f.otherProgram, "thread-other"), Chat: "thread-other", Cwd: q, Updated: time.Now().UnixMilli()})
	senderOwn := discoveredSessionFixture(t, applicationAttachSession{Context: source, Chat: "thread-sender", Cwd: p, Updated: time.Now().UnixMilli()})
	sameApp := discoveredSessionFixture(t, applicationAttachSession{Chat: "thread-sibling", Cwd: worktree, Updated: time.Now().UnixMilli()})
	calls := map[string]int{}
	f.run.d.messageAttachSessions = func(_ context.Context, application string) ([]applicationAttachSession, error) {
		calls[application]++
		switch application {
		case "other-agent":
			return []applicationAttachSession{main, linked, other}, nil
		case "native-agent":
			return []applicationAttachSession{senderOwn, sameApp}, nil
		}
		return nil, nil
	}
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", source)
	sameApp.Context = name(f.selfProgram, sameApp.Chat)
	// The scope under test is the whole authority-filtered listing Prepare
	// and Send read. The public listing discloses another chat only as a
	// count until the two chats share a thread (architect ruling 2026-11-12).
	list := func() []msg.Inbox {
		t.Helper()
		public, err := sender.InboxesList(msg.InboxQuery{})
		if err != nil || public.Outcome != msg.OutcomePage {
			t.Fatal(public, err)
		}
		// The test's only thread is the one its sends open with linked.
		for _, row := range public.Inboxes {
			if (row.Mine == nil || !*row.Mine) && row.Context != linked.Context {
				t.Fatal("the public listing disclosed a chat with no thread", row)
			}
		}
		page := f.reader.inboxesList("")
		if page.Outcome != msg.OutcomePage {
			t.Fatal(page)
		}
		for _, row := range page.Inboxes {
			if strings.HasPrefix(row.Context, "attach-") {
				t.Fatal("an attach-* row was listed", row)
			}
		}
		return page.Inboxes
	}
	rows := list()
	for _, row := range rows {
		if row.Context == main.Context || row.Context == other.Context {
			t.Fatal("a discovered row without the message permit", row)
		}
	}
	if chats, err := f.store.retainedChats(f.run.ctx, owner, f.otherProgram); err != nil || len(chats) != 0 {
		t.Fatal("listing wrote a retained chat", chats, err)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.contextForChat(f.run.ctx, owner, f.otherProgram, main.Chat, &msg.ChatBinding{ChatID: main.Chat, ChatTitle: "Retained title"}); err != nil {
		t.Fatal(err)
	}
	rows = list()
	count := map[string]int{}
	for _, row := range rows {
		count[row.Context]++
		if row.Context == main.Context && row.ChatTitle != "Retained title" {
			t.Fatal("the retained title did not win", row)
		}
		if row.Context == linked.Context && (row.Status != "Not open in "+linked.Folder || row.Reach == nil) {
			t.Fatal("discovered row", row)
		}
	}
	if count[main.Context] != 1 || count[linked.Context] != 1 || count[other.Context] != 0 {
		t.Fatal("scope or dedupe", count)
	}
	// Send to another project's conversation refuses; attach-* is retired.
	for _, contextName := range []string{other.Context, "attach-0123456789abcdef01234567"} {
		sent, err := sender.Send(instance, source, msg.SendRequest{To: "app:other-agent", Context: contextName, Kind: msg.KindQuestion, Body: "hello", IdempotencyKey: "refused-" + contextName[:8], ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
		if err != nil || sent.Outcome != msg.OutcomeUnavailable || sent.Reason != "session_unknown" {
			t.Fatal("send outside the scope", contextName, sent, err)
		}
	}
	// T6: a question promotes the discovered conversation; a note does not.
	sent, err := sender.Send(instance, source, msg.SendRequest{To: "app:other-agent", Context: linked.Context, Kind: msg.KindNote, Body: "fyi", IdempotencyKey: "note-linked", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal("note to a discovered conversation", sent, err)
	}
	if chats, _ := f.store.retainedChats(f.run.ctx, owner, f.otherProgram); len(chats) != 1 {
		t.Fatal("a note promoted the conversation", chats)
	}
	sent, err = sender.Send(instance, source, msg.SendRequest{To: "app:other-agent", Context: linked.Context, Kind: msg.KindQuestion, Body: "question", IdempotencyKey: "question-linked", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal("question to a discovered conversation", sent, err)
	}
	chats, err := f.store.retainedChats(f.run.ctx, owner, f.otherProgram)
	promoted := false
	for _, chat := range chats {
		promoted = promoted || chat.Context == linked.Context && chat.Chat == linked.Chat
	}
	if err != nil || !promoted {
		t.Fatal("the question did not promote its conversation", chats, err)
	}
	// T9: another conversation of the sender's own app receives; its own never.
	gate := messageSameAppReceivers
	messageSameAppReceivers = true
	t.Cleanup(func() { messageSameAppReceivers = gate })
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:native-agent", true); err != nil {
		t.Fatal(err)
	}
	rows = list()
	sibling := false
	for _, row := range rows {
		if row.Context == source && row.Program == "app:native-agent" && row.Mine == nil {
			t.Fatal("the sender listed itself as a receiver", row)
		}
		sibling = sibling || row.Program == "app:native-agent" && row.Context == sameApp.Context
	}
	if !sibling {
		t.Fatal("another conversation of the sender's own app is not listed", rows)
	}
	sent, err = sender.Send(instance, source, msg.SendRequest{To: "app:native-agent", Context: source, Kind: msg.KindQuestion, Body: "self", IdempotencyKey: "self", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil || sent.Outcome == msg.OutcomeSent {
		t.Fatal("a conversation sent to itself", sent, err)
	}
}

// T10: one snapshot per agent for 60 seconds with one refresh in flight; a
// blocked refresh answers within 2 seconds from the previous snapshot; a
// forced refresh runs once.
func TestDiscoverySnapshotBounds(t *testing.T) {
	var now, fetches atomic.Int64
	now.Store(time.Unix(1_800_000_000, 0).UnixNano())
	block := make(chan struct{})
	var blocked, unavailable atomic.Bool
	m := newMessageDiscovery(func(ctx context.Context, _ string) ([]applicationAttachSession, error) {
		fetch := fetches.Add(1)
		if unavailable.Load() {
			return nil, context.DeadlineExceeded
		}
		if blocked.Load() {
			select {
			case <-block:
			case <-ctx.Done():
			}
		}
		return []applicationAttachSession{{Context: "chat-x", Updated: fetch}}, nil
	}, func() time.Time { return time.Unix(0, now.Load()) })
	if got := m.sessions(t.Context(), "a", false); len(got) != 1 || fetches.Load() != 1 {
		t.Fatal(got, fetches.Load())
	}
	if got := m.sessions(t.Context(), "a", false); len(got) != 1 || fetches.Load() != 1 {
		t.Fatal("a fresh snapshot fetched again", fetches.Load())
	}
	// Two forced refreshes within 10 seconds fetch once.
	m.sessions(t.Context(), "a", true)
	if fetches.Load() != 1 {
		t.Fatal("a forced refresh ran within 10 seconds of the last", fetches.Load())
	}
	now.Add(int64(61 * time.Second))
	blocked.Store(true)
	started := time.Now()
	got := m.sessions(t.Context(), "a", false)
	if time.Since(started) > 3*time.Second || len(got) != 1 || got[0].Updated != 1 {
		t.Fatal("a blocked refresh did not answer from the previous snapshot", got, time.Since(started))
	}
	m.sessions(t.Context(), "a", true)
	if fetches.Load() != 2 {
		t.Fatal("a second refresh started while one was in flight", fetches.Load())
	}
	close(block)
	blocked.Store(false)
	for range 100 {
		m.mu.Lock()
		done := m.apps["a"].done == nil
		m.mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	now.Add(int64(messageDiscoveryForceGap))
	if got := m.sessions(t.Context(), "a", true); fetches.Load() != 3 || len(got) != 1 {
		t.Fatal("a forced refresh did not run once", fetches.Load())
	}
	now.Add(int64(11 * time.Minute))
	unavailable.Store(true)
	if got := m.sessions(t.Context(), "a", false); len(got) != 0 {
		t.Fatal("a snapshot older than 10 minutes was used", got)
	}
}

// T12 (in part): the composed directory names discovered conversations by the
// store's chat name function and offers a forced refresh.
func TestComposedDirectoryWiresDiscovery(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	f.run.d.messageAttachSessions, f.run.d.messageAttachRefresh = nil, nil
	composeMessageDiscovery(f.run.d, f.store, nil)
	if f.run.d.messageAttachSessions == nil || f.run.d.messageAttachRefresh == nil {
		t.Fatal("discovery not composed")
	}
	// Without a registered recipe the launcher listing is unavailable and the
	// listing carries no discovered conversations.
	if got := f.run.d.messageAttachSessionsFor(f.run.ctx, "other-agent"); len(got) != 0 {
		t.Fatal(got)
	}
}

// Fix 1 (review): an unpermitted sender addressing an unknown chat-* context
// never starts a discovery; it gets the permit refusal.
func TestUnknownChatSendWithoutPermitStartsNoDiscovery(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	fetches := 0
	f.run.d.messageAttachSessions = func(context.Context, string) ([]applicationAttachSession, error) {
		fetches++
		return nil, nil
	}
	f.run.d.messageAttachRefresh = func(context.Context, string) []applicationAttachSession {
		fetches++
		return nil
	}
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		sent, err := sender.Send(instance, "source", msg.SendRequest{To: "app:other-agent", Context: "chat-" + strings.Repeat(string(rune('a'+i)), 48), Kind: msg.KindQuestion, Body: "hello", IdempotencyKey: "unknown-" + string(rune('a'+i)), ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
		if err != nil || sent.Outcome == msg.OutcomeSent || sent.Reason == "session_unknown" {
			t.Fatal("unpermitted send", sent, err)
		}
	}
	if fetches != 0 {
		t.Fatal("an unpermitted sender started discovery", fetches)
	}
}

// Fix 2 (review): folder facts are read in the bounded refresh. A session on
// an unreachable share keeps none of them and still lists within 2 seconds;
// the listing reads no project.
func TestDiscoveredChatOnUnreachableShareListsQuickly(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	local := t.TempDir()
	page := `{"sessions":[{"chat":"thread-share","cwd":` + strconv.Quote(`\\unreachable\share\x`) + `,"updated":1},{"chat":"thread-local","cwd":` + strconv.Quote(local) + `,"updated":2}]}`
	owner := f.run.d.owner
	discovery := newMessageDiscovery(func(context.Context, string) ([]applicationAttachSession, error) {
		return discoveredSessions([]byte(page), func(chat string) string { return f.store.chatContextName(owner, f.otherProgram, chat) })
	}, nil)
	f.run.d.messageAttachSessions = func(ctx context.Context, application string) ([]applicationAttachSession, error) {
		if application != "other-agent" {
			return nil, nil
		}
		return discovery.sessions(ctx, application, false), nil
	}
	if got := discovery.sessions(t.Context(), "other-agent", false); len(got) != 2 || got[0].Project != "" || got[0].Cwd != "" || got[0].Folder != "x" || got[1].Project == "" {
		t.Fatal("refresh facts", got)
	}
	reads := 0
	prior := messageProjectOf
	messageProjectOf = func(string) string { reads++; return "" }
	t.Cleanup(func() { messageProjectOf = prior })
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	_, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	started := time.Now()
	public, err := sender.InboxesList(msg.InboxQuery{})
	if err != nil || time.Since(started) > 2*time.Second {
		t.Fatal("listing", err, time.Since(started))
	}
	counted := false
	for _, other := range public.OtherChats {
		counted = counted || other.Program == "app:other-agent" && other.Chats > 0
	}
	if !counted {
		t.Fatal("the public listing did not count the share session", public)
	}
	page2 := f.reader.inboxesList("")
	share := f.store.chatContextName(owner, f.otherProgram, "thread-share")
	listed := false
	for _, row := range page2.Inboxes {
		listed = listed || row.Context == share
	}
	if !listed || reads != 0 {
		t.Fatal("the share session did not list, or the listing read a project", listed, reads)
	}
}

// R19b: a discovery refresh that lands between a listing's two snapshots
// never turns the listing into a gap; each listing reads discovery once.
func TestListingPinsDiscoveryAcrossItsSnapshots(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	owner := f.run.d.owner
	calls := 0
	f.run.d.messageAttachSessions = func(_ context.Context, application string) ([]applicationAttachSession, error) {
		if application != "other-agent" {
			return nil, nil
		}
		calls++
		var sessions []applicationAttachSession
		for i := range calls {
			chat := "thread-" + strconv.Itoa(i)
			sessions = append(sessions, applicationAttachSession{Context: f.store.chatContextName(owner, f.otherProgram, chat), Chat: chat, Folder: "work", Updated: int64(1 + i)})
		}
		return sessions, nil
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	_, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	page, err := sender.InboxesList(msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("a refresh between the listing's snapshots made a gap", page.Outcome, err)
	}
	if calls != 1 {
		t.Fatal("one listing read discovery more than once", calls)
	}
}

// R19b: Prepare to a conversation that is open or retained, such as the chat
// that just messaged this sender, never waits for discovery.
func TestPrepareToKnownChatSkipsDiscovery(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	owner := f.run.d.owner
	calls := 0
	f.run.d.messageAttachSessions = func(context.Context, string) ([]applicationAttachSession, error) {
		calls++
		time.Sleep(3 * time.Second)
		return nil, nil
	}
	known, err := f.store.contextForChat(f.run.ctx, owner, f.otherProgram, "known-chat", &msg.ChatBinding{ChatID: "known-chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	started := time.Now()
	prepared, err := sender.Prepare(instance, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: known, Body: "pong"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != known {
		t.Fatal("prepare to a known chat", prepared, err)
	}
	if calls != 0 || time.Since(started) > 2*time.Second {
		t.Fatal("prepare to a known chat waited for discovery", calls, time.Since(started))
	}
}

// Review 2026-11-09: a Send to an unknown chat waited for the sender's
// discovery, the receiver's and a forced refresh in turn, about 6 seconds
// with a stalled launcher. The two refreshes share one 2-second wait, and a
// wait that timed out forces no third.
func TestUnknownChatSendWaitsOnceForStalledDiscovery(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	var mu sync.Mutex
	fetched := map[string]int{}
	discovery := newMessageDiscovery(func(ctx context.Context, application string) ([]applicationAttachSession, error) {
		mu.Lock()
		fetched[application]++
		mu.Unlock()
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil, ctx.Err()
	}, nil)
	d := f.run.d
	d.messageAttachSessions = func(ctx context.Context, application string) ([]applicationAttachSession, error) {
		return discovery.sessions(ctx, application, false), nil
	}
	d.messageAttachRefresh = func(ctx context.Context, application string) []applicationAttachSession {
		return discovery.sessions(ctx, application, true)
	}
	d.messageAttachWarm, d.messageAttachCurrent = discovery.warm, discovery.current
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	started := time.Now()
	sent, err := sender.Send(instance, "source", msg.SendRequest{To: "app:other-agent", Context: "chat-" + strings.Repeat("e", 48), Kind: msg.KindQuestion, Body: "hello", IdempotencyKey: "stalled-unknown", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
	elapsed := time.Since(started)
	if err != nil || sent.Outcome == msg.OutcomeSent || sent.Reason != "session_unknown" {
		t.Fatal("an unknown chat was accepted", sent, err)
	}
	if elapsed > 3500*time.Millisecond {
		t.Fatal("a Send waited for each stalled discovery in turn", elapsed)
	}
	mu.Lock()
	defer mu.Unlock()
	if fetched["other-agent"] != 1 || fetched["native-agent"] != 1 {
		t.Fatal("each agent's discovery did not start once", fetched)
	}
}

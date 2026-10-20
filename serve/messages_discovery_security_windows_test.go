package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// F3: a .git file naming a share gives no project and opens nothing there;
// neither does a commondir naming a share, nor a share folder itself.
func TestMessageProjectRefusesSharePaths(t *testing.T) {
	var opened []string
	prior := messageProjectOpen
	messageProjectOpen = func(path string) (*os.File, error) {
		opened = append(opened, path)
		return prior(path)
	}
	t.Cleanup(func() { messageProjectOpen = prior })
	share := `\\host\share\x`
	worktree := t.TempDir()
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+share+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := messageProject(worktree); got != "" {
		t.Fatal("a .git file naming a share gave a project", got)
	}
	linked, gitDir := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+filepath.ToSlash(gitDir)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte(`\\host\share\repo\.git`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := messageProject(linked); got != "" {
		t.Fatal("a commondir naming a share gave a project", got)
	}
	for _, path := range []string{share, `\\?\C:\x`, `\\.\pipe\x`, `//host/share/x`} {
		if got := messageProject(path); got != "" {
			t.Fatal("a non-local folder gave a project", path, got)
		}
	}
	for _, path := range opened {
		if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) {
			t.Fatal("discovery opened a share path", path)
		}
	}
}

// F4: discovery runs only for agents the sender may message. A sender
// without the permit lists and sends with no fetch of that agent, and a
// Send held for the person's permit starts none either.
func TestDiscoveryNeedsThePermitFirst(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	fetches := 0
	f.run.d.messageAttachSessions = func(_ context.Context, application string) ([]applicationAttachSession, error) {
		if application == "other-agent" {
			fetches++
		}
		return nil, nil
	}
	f.run.d.messageAttachRefresh = func(_ context.Context, application string) []applicationAttachSession {
		if application == "other-agent" {
			fetches++
		}
		return nil
	}
	f.run.d.messageAttachWarm = func(_ context.Context, names []string) bool {
		for _, name := range names {
			if name == "other-agent" {
				fetches++
			}
		}
		return true
	}
	instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	if page, err := sender.InboxesList(msg.InboxQuery{}); err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	if fetches != 0 {
		t.Fatal("a listing discovered an agent the sender may not message", fetches)
	}
	// Held: the person is asked for the message permit; nothing is discovered.
	f.reader.host.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
		return "pending-person-question"
	}
	sent, err := sender.Send(instance, "source", msg.SendRequest{To: "app:other-agent", Context: "chat-" + strings.Repeat("d", 48), Kind: msg.KindQuestion, Body: "hello", IdempotencyKey: "held-unknown", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()})
	if err != nil || sent.Outcome == msg.OutcomeSent {
		t.Fatal("a held send to an unknown chat was accepted", sent, err)
	}
	if fetches != 0 {
		t.Fatal("a held sender started discovery", fetches)
	}
}

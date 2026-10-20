package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func messageCanonicalChatContext(value string) bool { return strings.HasPrefix(value, "chat-") }
func validMessageStartSelectedTarget(r applicationMessageStartRequest) bool {
	if r.Cold == nil {
		return false
	}
	return r.Cold.Valid() == nil && r.Target == r.Cold.Context && messageCanonicalChatContext(r.Target)
}

func messageColdRevision(row messageChatBindingRecord) string {
	// Presence renewal and display labels do not change the selected launch.
	row.LastSeenUnixMs, row.Title = 0, ""
	raw, _ := json.Marshal(row)
	return messageDigest(string(raw))
}

// messageChatSelection selects a retained chat's own conversation: a Claude
// chat by its recorded launch facts, a Codex chat, which records none, by its
// thread ID alone. The registered recipe decides which form it accepts.
func messageChatSelection(row messageChatBindingRecord) (*startprofile.ColdResume, error) {
	if row.Launch == nil && row.CwdKey == "" {
		c := &startprofile.ColdResume{Context: row.Context, Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: row.Chat})), Revision: messageColdRevision(row)}
		if _, err := c.Thread(); err != nil {
			return nil, err
		}
		return c, nil
	}
	launch, err := row.currentLaunch()
	if err != nil {
		return nil, err
	}
	c := &startprofile.ColdResume{Context: row.Context, Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: row.Chat, Launch: launch})), CwdKey: row.CwdKey, Revision: messageColdRevision(row)}
	if _, err := c.Launch(); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *messageStore) coldResumeForChat(ctx context.Context, account, program, canonical string) (*startprofile.ColdResume, error) {
	rows, err := s.retainedChats(ctx, account, program)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		if row.Context == canonical {
			return messageChatSelection(row)
		}
	}
	return nil, errors.New("messages: retained launch unavailable")
}

func (s *messageStore) checkColdResume(ctx context.Context, account, program string, expected *startprofile.ColdResume) error {
	if expected == nil {
		return errors.New("messages: cold selection unavailable")
	}
	current, err := s.coldResumeForChat(ctx, account, program, expected.Context)
	if err != nil || *current != *expected {
		return errors.New("messages: selected launch changed")
	}
	return nil
}

// messageChatStart reports whether a registered recipe of this kind can wake
// the selected retained chat now, and the chat's folder label for Activity.
// Claude answers from the chat's recorded launch facts: its launcher
// resumes the original in place while no Claude process runs and answers in a
// copy otherwise. Codex queues the turn into the chat's thread through the
// running Codex app only.
func messageChatStart(profile string, cold *startprofile.ColdResume, running messageAppsRunning) (string, bool) {
	switch profile {
	case "claude-v1":
		launch, err := cold.Launch()
		if err != nil {
			return "", false
		}
		return filepath.Base(launch.Cwd), true
	case "codex-v1":
		if _, err := cold.Thread(); err != nil {
			return "", false
		}
		return "", running.codex()
	}
	return "", false
}

// messageAppsRunning answers, once per pass, whether each agent app runs.
type messageAppsRunning struct{ codex func() bool }

func (d *applicationDirectory) appsRunning(ctx context.Context) messageAppsRunning {
	return messageAppsRunning{codex: sync.OnceValue(func() bool { return d.codexRunning(ctx) })}
}

func (d *applicationDirectory) codexRunning(ctx context.Context) bool {
	if d.messageCodexRunning == nil {
		return false
	}
	running, err := d.messageCodexRunning(ctx)
	return err == nil && running
}

// retainedChatInbox lists a retained chat. A question starts it when the
// receiver's registered recipe can wake it now; otherwise the chat is closed
// and a message waits there until the person opens it.
func retainedChatInbox(application string, chat messageChatBindingRecord, profile applicationMessageStartRequest, profileErr error, running messageAppsRunning) msg.Inbox {
	inbox := msg.Inbox{Program: "app:" + application, Context: chat.Context, ChatTitle: chat.Title, Status: "Retained conversation", LastSeenUnixMs: chat.LastSeenUnixMs, Revision: messageDigest(chat.Context + "\x00" + chat.Title), Reach: new(msg.InboxReachQueued)}
	if profileErr != nil || profile.Descriptor.Activation == nil {
		return inbox
	}
	p, err := startprofile.Parse(profile.Descriptor.Activation.Arguments)
	if err != nil {
		return inbox
	}
	cold, err := messageChatSelection(chat)
	if err != nil {
		return inbox
	}
	if _, startable := messageChatStart(p.Name, cold, running); startable {
		inbox.Reach = new(msg.InboxReachStartsOnQuestion)
	}
	return inbox
}

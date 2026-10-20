package main

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// This is fixed native startup correlation from the admitted launch recipe.
// It grants no authority; the actual helper still proves its own native source.
type claudeColdBinding struct {
	context string
	launch  wire.ChatLaunch
	cwdKey  string
	// copy is OA's session ID of a copy of the original. The copy's helper
	// runs under that session and reads the original chat's inbox.
	copy string
}

// withCopy names the copy session this helper runs under.
func (c *claudeColdBinding) withCopy(session string) (*claudeColdBinding, error) {
	if session == "" {
		return c, nil
	}
	if c == nil || !agentSessionUUID.MatchString(session) || session == c.launch.Session {
		return nil, errors.New("Claude copy correlation unavailable")
	}
	copy := *c
	copy.copy = session
	return &copy, nil
}

type claudeColdBindingKey struct{}

func parseClaudeColdBinding(raw, cwdKey, canonical string, replyOnly bool) (*claudeColdBinding, error) {
	if raw == "" && cwdKey == "" {
		return nil, nil
	}
	if !replyOnly || len(raw) == 0 || len(raw) > 8192 || cwdKey == "" || len(cwdKey) > 4096 || !agentRoutingContext.MatchString(canonical) {
		return nil, errors.New("Claude cold binding requires a fixed reply-only startup")
	}
	claim, err := wire.DecodeChatBinding([]byte(raw))
	if err != nil || claim.Launch == nil || claim.ChatID != claim.Launch.Session || !codexQueueThread.MatchString(claim.ChatID) || claim.SourceInstance != "" || claim.ChatTitle != "" {
		return nil, errors.New("Claude cold binding correlation unavailable")
	}
	if !filepath.IsAbs(claim.Launch.Cwd) || identity.LocalPath(claim.Launch.Cwd) != nil {
		return nil, errors.New("Claude cold binding cwd unsupported")
	}
	switch claim.Launch.Origin {
	case wire.ChatOriginDesktop, wire.ChatOriginCli:
	default:
		return nil, errors.New("Claude cold binding host unsupported")
	}
	switch claim.Launch.Mode {
	case wire.ChatModePlan, wire.ChatModeReadOnly, wire.ChatModeDefault, wire.ChatModeAcceptEdits:
	default:
		return nil, errors.New("Claude cold binding mode unsupported")
	}
	return &claudeColdBinding{context: canonical, launch: *claim.Launch, cwdKey: cwdKey}, nil
}

func (c *claudeColdBinding) matches(ctx context.Context, actualSession string, startup fileid.FileID) bool {
	if c == nil {
		return false
	}
	expected := c.launch.Session
	if c.copy != "" {
		expected = c.copy
	}
	if c == nil || actualSession != expected || startup.Key() != c.cwdKey || !startup.IsDir() {
		return false
	}
	current, err := fileid.Resolve(ctx, fileid.Self, c.launch.Cwd, fileid.Options{Links: fileid.RefuseLinks})
	return err == nil && current.IsDir() && current.Equal(startup)
}

// Hooks are disabled in a restricted resume. This startup-only caller supplies
// the independently checked host correlation without exposing a model tool.
// Source admission and retirement stay with the ordinary presence owners.
func runConfiguredClaudeReceiver(ctx context.Context, b *messagesBackend, c *claudeColdBinding, actualSession string, startup fileid.FileID) {
	// A copy binds the original conversation's chat, whose inbox it answers.
	if !c.matches(ctx, actualSession, startup) || b.bindClaudeChat(c.launch.Session) != nil {
		return
	}
	b.chatMu.Lock()
	b.chatContext = c.context
	launch := c.launch
	b.chatLaunch = &launch
	b.chatMu.Unlock()
	delay := agentReadmitFirstDelay
	for ctx.Err() == nil {
		if !c.matches(ctx, actualSession, startup) {
			return
		}
		_, release, err := b.newChatReceiver(ctx)
		if err == nil {
			defer release()
			<-ctx.Done()
			return
		}
		if codexReceivingRetired(b) || agentPresenceFinal(ctx, err) {
			return
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if delay *= 2; delay > agentReadmitMaxDelay {
			delay = agentReadmitMaxDelay
		}
	}
}

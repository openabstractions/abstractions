package main

import (
	"context"
	"time"
)

// Host claims are diagnostic metadata. They never participate in admission,
// routing, inbox ownership, permission, readiness or lease decisions.
func messagesWatchHealthText(claim string) string {
	switch claim {
	case "watching":
		return "Background Messages: watching (host claim)"
	case "recovering":
		return "Background Messages: recovering (host claim)"
	case "stopped_forbidden":
		return "Background Messages stopped: admission refused (host claim)"
	case "stopped_protocol":
		return "Background Messages stopped: protocol invalid (host claim)"
	case "stopped_context":
		return "Background Messages stopped: connection changed (host claim)"
	case "paused":
		return "Background Messages: paused (host claim)"
	default:
		return ""
	}
}

func messagesWatchHealthStatus(base, claim string) string {
	text := messagesWatchHealthText(claim)
	if text == "" {
		return base
	}
	return messagesStatusSuffix(base, text)
}

// messagesWakeUnconfirmedText is the presence status a Codex receiver
// publishes while a wake it queued, or the earlier pointer that holds its
// thread, is unconfirmed. The runtime reads this exact text to tell a sender
// the message is queued; it grants and routes nothing.
const messagesWakeUnconfirmedText = "queued; Codex did not confirm the wake"

// messagesStatusSuffix appends one " | text" part within the 200-rune
// status bound, shortening the base first.
func messagesStatusSuffix(base, text string) string {
	if base == "" {
		return text
	}
	suffix := " | " + text
	runes := []rune(base)
	if limit := 200 - len([]rune(suffix)); len(runes) > limit {
		runes = runes[:max(limit, 0)]
	}
	return string(runes) + suffix
}

// Only normal scheduled status upkeep projects a claim. Recording one does
// not call InboxOpen, Announce, or signal any renewal/readiness owner.
func (s *messagesInboxStatus) recordWatchHealth(ctx context.Context, claim string) bool {
	if s == nil {
		return false
	}
	for !s.mu.TryLock() {
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	s.watchHealth = claim
	return true
}

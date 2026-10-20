package main

import (
	"crypto/subtle"
	"regexp"
	"sync"
)

// The Claude Mod and the OpenCode plugin call oa_messages_watch on the same
// MCP connection a model uses, and a model can call any listed tool. The
// watch fields that steer this inbox's wakes and turn chain (turn,
// reserve_wake, wake_ticket) therefore require a key the host adapter
// registered for this child before any model turn (AB-1s F2, 2026-11-09).
// The Mod presents its key on oa_claude_session_start; the OpenCode plugin
// passes it in its child's environment. The first key latches for the child's
// lifetime, so a later model call cannot replace it.
const openCodeAdapterEnvironment = "OA_OPENCODE_ADAPTER_KEY"

var messagesAdapterKeyPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type messagesAdapter struct {
	mu  sync.Mutex
	key string
}

// present latches the first well-formed key and reports whether key is the
// latched one: "accepted" or "refused".
func (a *messagesAdapter) present(key string) string {
	if a == nil || !messagesAdapterKeyPattern.MatchString(key) {
		return "refused"
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.key == "" {
		a.key = key
	}
	if subtle.ConstantTimeCompare([]byte(a.key), []byte(key)) == 1 {
		return "accepted"
	}
	return "refused"
}

// latched reports whether a host adapter registered a key.
func (a *messagesAdapter) latched() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.key != ""
}

// admits reports whether a watch call may set the adapter's fields. A turn
// signal always needs the latched key. A reservation needs it once one is
// latched; a child no keyed adapter registered with (an older plugin) keeps
// reserving without one, which sets only a longer chain on its own inbox.
func (a *messagesAdapter) admits(key string, turn bool) bool {
	if a == nil {
		return key == "" && !turn
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.key == "" {
		return key == "" && !turn
	}
	return subtle.ConstantTimeCompare([]byte(a.key), []byte(key)) == 1
}

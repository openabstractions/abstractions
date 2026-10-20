package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/fileid"
)

const claudeSessionStartTool = "oa_claude_session_start"
const claudeSessionEndTool = "oa_claude_session_end"

type claudeHostLaunchInput struct {
	Cwd    string `json:"cwd"`
	Origin string `json:"origin"`
	Mode   string `json:"mode"`
}

// The child fixes the session and native startup directory. Host metadata can
// corroborate those facts; it cannot select another session, program or folder.
func claudeHostLaunch(ctx context.Context, session string, startup fileid.FileID, in *claudeHostLaunchInput) *wire.ChatLaunch {
	if in == nil || !codexQueueThread.MatchString(session) {
		return nil
	}
	current, err := fileid.Resolve(ctx, fileid.Self, startup.Path(), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !current.IsDir() || !current.Equal(startup) {
		return nil
	}
	reported, err := fileid.Resolve(ctx, fileid.Self, in.Cwd, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !reported.Equal(startup) {
		return nil
	}
	raw, _ := json.Marshal(map[string]any{"chat_id": session, "launch": map[string]string{"session": session, "cwd": startup.Path(), "origin": in.Origin, "mode": in.Mode}})
	claim, err := wire.DecodeChatBinding(raw)
	if err != nil {
		return nil
	}
	switch claim.Launch.Origin {
	case wire.ChatOriginDesktop, wire.ChatOriginCli:
	default:
		return nil
	}
	switch claim.Launch.Mode {
	case wire.ChatModePlan, wire.ChatModeReadOnly, wire.ChatModeDefault, wire.ChatModeAcceptEdits:
	default:
		return nil
	}
	return claim.Launch
}

type claudeSessionEndInput struct {
	ChildInstance string `json:"child_instance"`
	SessionID     string `json:"session_id"`
	Retire        bool   `json:"retire,omitempty"`
}

// The host supplies an opaque session string. Reuse the native ChatBinding
// byte/control bounds, preserving its exact value for the Mod's comparison.
func validChatBindingID(id string) bool {
	raw, err := json.Marshal(map[string]string{"chat_id": id})
	if err != nil || id == "" {
		return false
	}
	claim, err := wire.DecodeChatBinding(raw)
	return err == nil && claim.ChatID == id
}

func validClaudeSessionID(id string) bool { return validChatBindingID(id) }

func (b *messagesBackend) bindClaudeChat(id string) error {
	if !validClaudeSessionID(id) {
		return errors.New("native Claude chat metadata unavailable")
	}
	return b.bindChatID(id)
}

func claudeWakeReceipt(ctx context.Context, application, nativeContext, session string) (*codexWakeState, error) {
	if !validClaudeSessionID(session) {
		return nil, errors.New("Claude durable wake binding unavailable")
	}
	return chatWakeReceiptWithRandom(ctx, application, nativeContext, session, rand.Reader)
}

// Claude documents CLAUDE_CODE_SESSION_ID for stdio MCP children. It is an
// untrusted launch correlation, never model tool input or vendor authority.
// Its value remains fixed for this child. The Mod independently compares it
// with $.session.id(), and refuses a stale child after clear/implicit resume.
func runClaudeChatServer(ctx context.Context, server *mcp.Server, b *messagesBackend, transport mcp.Transport) error {
	adapter := new(messagesAdapter)
	b.chatMu.Lock()
	b.requireChat = true
	b.adapter = adapter
	b.chatMu.Unlock()
	id := os.Getenv("CLAUDE_CODE_SESSION_ID")
	launchReason := "none"
	if !validClaudeSessionID(id) {
		launchReason = "invalid_child_session"
	}
	observeClaudeBinding(ctx, "awaiting_start", "none", launchReason)
	startupCwd, _ := os.Getwd()
	startupDirectory, _ := fileid.Resolve(ctx, fileid.Self, startupCwd, fileid.Options{Links: fileid.RefuseLinks})
	if cold, ok := ctx.Value(claudeColdBindingKey{}).(*claudeColdBinding); ok {
		receiving, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); runConfiguredClaudeReceiver(receiving, b, cold, id, startupDirectory) }()
		defer func() { cancel(); <-done }()
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	child := hex.EncodeToString(nonce[:])
	var mu sync.Mutex
	var stop func()
	var generation uint64
	retired := false
	// The Mod sends its title with every observation. Only a changed one is
	// applied, so a rename the transcript carries is not reverted by the
	// Mod's older title.
	modTitle := ""
	instance := func() string { return child + "-" + strconv.FormatUint(generation, 16) }
	reply := func(outcome, contextName, childInstance string, adapterOutcome ...string) *mcp.CallToolResult {
		fields := map[string]string{"outcome": outcome, "session_id": id, "context": contextName, "child_instance": childInstance, "launch_facts": "supported"}
		if len(adapterOutcome) == 1 {
			fields["adapter"] = adapterOutcome[0]
		}
		raw, _ := json.Marshal(fields)
		return &mcp.CallToolResult{IsError: outcome == "unavailable", Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
	}
	end := func() {
		mu.Lock()
		release := stop
		stop = nil
		mu.Unlock()
		if release != nil {
			release()
		}
	}
	defer end()
	server.AddTool(&mcp.Tool{Name: claudeSessionStartTool, Description: "Internal Claude host lifecycle correlation; optional host launch facts and chat title (the host session title, else the folder label) grant no identity, permissions or launch authority.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"chat_title": map[string]any{"type": "string", "maxLength": 800}, "launch": map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "object", "required": []string{"cwd", "origin", "mode"}, "properties": map[string]any{"cwd": map[string]any{"type": "string", "maxLength": 4096}, "origin": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string"}}, "additionalProperties": false}}}, "adapter_key": map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$", "description": "The Mod's own key for this child. The first key registers; a later different key is refused."}}, "additionalProperties": false}}, func(call context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			ChatTitle  string                 `json:"chat_title"`
			Launch     *claudeHostLaunchInput `json:"launch"`
			AdapterKey string                 `json:"adapter_key"`
		}
		observeClaudeBinding(ctx, "start", "pending", "none")
		if request.Params == nil {
			observeClaudeBinding(ctx, "start", "unavailable", "invalid_request")
			return reply("unavailable", "", ""), nil
		}
		decoder := json.NewDecoder(bytes.NewReader(request.Params.Arguments))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil {
			observeClaudeBinding(ctx, "start", "unavailable", "invalid_request")
			return reply("unavailable", "", ""), nil
		}
		// The Mod's chat title (host session title or folder label) is metadata. The generated
		// native claim grammar and title owner retain their byte/display bounds.
		raw, _ := json.Marshal(map[string]string{"chat_id": id, "chat_title": input.ChatTitle})
		claim, err := wire.DecodeChatBinding(raw)
		if err != nil {
			reason := "invalid_request"
			if !validClaudeSessionID(id) {
				reason = "invalid_child_session"
			}
			observeClaudeBinding(ctx, "start", "unavailable", reason)
			return reply("unavailable", "", ""), nil
		}
		title := hostChatTitle(claim.ChatTitle)
		// "supported" advertises the key input before the Mod sends it, as
		// launch_facts does; an older child refuses the unknown field.
		adapterOutcome := "supported"
		if input.AdapterKey != "" {
			adapterOutcome = adapter.present(input.AdapterKey)
		}
		launch := claudeHostLaunch(call, id, startupDirectory, input.Launch)
		outcome := "unavailable"
		reason := "retired"
		mu.Lock()
		b.chatMu.Lock()
		b.chatLaunch = launch
		if b.chatReceiver != nil {
			b.chatReceiver.status.routingMu.Lock()
			b.chatReceiver.status.chatLaunch = launch
			b.chatReceiver.status.routingMu.Unlock()
		}
		b.chatMu.Unlock()
		if stop == nil && !retired {
			reason = "binding_unavailable"
			if !validClaudeSessionID(id) {
				reason = "invalid_child_session"
			}
			if err := b.bindClaudeChat(id); err == nil {
				receiver, release, err := b.newChatReceiver(ctx, func(receiver *messagesBackend) error {
					receiver.status.setChatTitle(title)
					state, err := claudeWakeReceipt(ctx, b.application, receiver.contextName, id)
					if err != nil {
						return err
					}
					receiver.useWakeState(state)
					return nil
				})
				if err == nil {
					modTitle = title
					following, cancel := context.WithCancel(ctx)
					refresh := b.claudeTitleRefresh
					if refresh == 0 {
						refresh = claudeTitleRefresh
					}
					go followClaudeSessionTitle(following, receiver.status, claudeConfigDirectory(), id, refresh, time.After)
					stop = func() { cancel(); release() }
				} else {
					reason = "receiver_" + agentLossWord(err)
				}
			}
		}
		if stop != nil {
			outcome = "observed"
			reason = "none"
		}
		contextName := ""
		if outcome == "observed" {
			b.chatMu.Lock()
			if b.chatReceiver != nil {
				contextName = b.chatReceiver.contextName
				if input.ChatTitle != "" && title != modTitle {
					modTitle = title
					b.chatReceiver.status.setChatTitle(title)
				}
			}
			b.chatMu.Unlock()
		}
		result := reply(outcome, contextName, instance(), adapterOutcome)
		observeClaudeBinding(ctx, "start", outcome, reason)
		mu.Unlock()
		return result, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: claudeSessionEndTool, Description: "Internal Claude host lifecycle teardown. Echo only fixed metadata from this child's start result. Retirement fences a mismatched child; ordinary end permits a fresh association.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, IdempotentHint: true}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"child_instance": map[string]any{"type": "string", "minLength": 1, "maxLength": 64}, "session_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 256}, "retire": map[string]any{"type": "boolean"}}, "required": []string{"child_instance", "session_id"}, "additionalProperties": false}}, func(call context.Context, _ *mcp.CallToolRequest, in claudeSessionEndInput) (*mcp.CallToolResult, any, error) {
		mu.Lock()
		if in.ChildInstance != instance() || in.SessionID != id || !validClaudeSessionID(id) {
			observeClaudeBinding(ctx, "end", "unavailable", "metadata_mismatch")
			mu.Unlock()
			return reply("unavailable", "", ""), nil, nil
		}
		release := stop
		stop = nil
		outcome := "ended"
		if in.Retire || retired {
			retired, outcome = true, "retired"
		} else {
			generation++
			if generation == 0 {
				retired = true
			}
		}
		mu.Unlock()
		if release != nil {
			release()
		}
		observeClaudeBinding(ctx, "end", outcome, "none")
		return reply(outcome, "", ""), nil, nil
	})
	guardAgentServer(server)
	return server.Run(ctx, transport)
}

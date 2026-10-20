package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

const openCodeChatEnvironment = "OA_OPENCODE_HOST_CHAT_ID"
const openCodeChatTitleEnvironment = "OA_OPENCODE_HOST_CHAT_TITLE"
const openCodeChatStartTool = "oa_opencode_session_start"

// This correlation is fixed by the native plugin when it hosts its own child.
// It supplies no vendor/parent/model authority; OA authenticates the kept child.
func runOpenCodeChatServer(ctx context.Context, server *mcp.Server, b *messagesBackend, transport mcp.Transport) error {
	// The plugin generated this key for this child; no model sees the
	// environment it spawns with. Processes this child starts do not inherit it.
	adapter := new(messagesAdapter)
	if key := os.Getenv(openCodeAdapterEnvironment); key != "" {
		adapter.present(key)
	}
	_ = os.Unsetenv(openCodeAdapterEnvironment)
	b.chatMu.Lock()
	b.adapter = adapter
	b.chatMu.Unlock()
	return runOpenCodeChatServerMetadata(ctx, server, b, transport, os.Getenv(openCodeChatEnvironment), os.Getenv(openCodeChatTitleEnvironment))
}

func runOpenCodeChatServerID(ctx context.Context, server *mcp.Server, b *messagesBackend, transport mcp.Transport, id string) error {
	return runOpenCodeChatServerMetadata(ctx, server, b, transport, id, "")
}

// openCodeChatTitle validates a host title with the generated claim grammar
// and normalizes it with the existing title owner.
func openCodeChatTitle(id, title string) string {
	raw, _ := json.Marshal(map[string]string{"chat_id": id, "chat_title": title})
	claim, err := wire.DecodeChatBinding(raw)
	if err != nil {
		return ""
	}
	return hostChatTitle(claim.ChatTitle)
}

func runOpenCodeChatServerMetadata(ctx context.Context, server *mcp.Server, b *messagesBackend, transport mcp.Transport, id, title string) error {
	// Display starts from the host launch fact. A later start call carries the
	// session's current title when the person or OpenCode renames it.
	title = openCodeChatTitle(id, title)
	b.chatMu.Lock()
	b.requireChat = true
	adapter := b.adapter
	b.chatMu.Unlock()
	valid := validChatBindingID(id) // The one generated ChatBinding grammar owner.
	if valid {
		if err := b.bindChatID(id); err != nil {
			valid = false
		}
	}
	var mu sync.Mutex
	var stop func()
	defer func() {
		mu.Lock()
		release := stop
		stop = nil
		mu.Unlock()
		if release != nil {
			release()
		}
	}()
	server.AddTool(&mcp.Tool{Name: openCodeChatStartTool, Description: "Internal OpenCode host lifecycle correlation; the optional chat title is the session's current host title and grants no identity or permissions.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"chat_title": map[string]any{"type": "string", "maxLength": 800}}, "additionalProperties": false}}, func(call context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var arguments map[string]json.RawMessage
		if request.Params == nil || json.Unmarshal(request.Params.Arguments, &arguments) != nil || arguments == nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"invalid"}`}}}, nil
		}
		renamed, titled := "", false
		for name, value := range arguments {
			if name != "chat_title" || json.Unmarshal(value, &renamed) != nil || len(renamed) > 800 {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"invalid"}`}}}, nil
			}
			titled = true
		}
		outcome, contextName := "unavailable", ""
		if valid {
			mu.Lock()
			if titled {
				title = openCodeChatTitle(id, renamed)
			}
			if stop == nil {
				_, release, err := b.newChatReceiver(ctx, func(receiver *messagesBackend) error {
					receiver.status.setChatTitle(title)
					// The plugin wakes an idle chat with the fixed pointer this
					// ledger reserves. Receiving stays available without it.
					if state, err := chatWakeReceiptWithRandom(ctx, b.application, receiver.contextName, id, rand.Reader); err == nil {
						receiver.useWakeState(state)
					}
					return nil
				})
				if err == nil {
					stop = release
				}
			}
			if stop != nil {
				b.chatMu.Lock()
				receiver := b.chatReceiver
				b.chatMu.Unlock()
				if receiver != nil && chatReceivingPresence(call, receiver) == nil {
					outcome = "observed"
					contextName = receiver.contextName
					if titled {
						receiver.status.setChatTitle(title)
					}
				}
			}
			mu.Unlock()
		}
		fields := map[string]string{"outcome": outcome, "session_id": id, "context": contextName}
		if adapter.latched() {
			// The plugin sends adapter_key on watch only after this answer.
			fields["adapter"] = "accepted"
		}
		raw, _ := json.Marshal(fields)
		return &mcp.CallToolResult{IsError: outcome != "observed", Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
	})
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(call context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if tool, ok := request.(*mcp.CallToolRequest); ok && method == "tools/call" && strings.HasPrefix(tool.Params.Name, "oa_messages_") {
				// The chat receiver's wait and the connection's own admission
				// wait share one deadline.
				call, _ = agentAdmissionUntil(call, tool.Params.Name)
				if !valid {
					return openCodeChatUnavailable("This helper has no OpenCode chat. Use OpenAbstractions from an OpenCode conversation with the OA integration connected."), nil
				}
				if _, ready := b.chatBodyBackend(call); !ready {
					return openCodeChatUnavailable("This OpenCode conversation has not connected to OpenAbstractions. Try again shortly. If it still fails, check the OpenCode connection in the OpenAbstractions Panel."), nil
				}
			}
			return next(call, method, request)
		}
	})
	guardAgentServer(server)
	return server.Run(ctx, transport)
}

func openCodeChatUnavailable(detail string) *mcp.CallToolResult {
	raw, _ := json.Marshal(map[string]string{"outcome": "unavailable", "reason": "chat_owner_unavailable", "detail": detail})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

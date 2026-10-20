package gateway

import (
	"context"
	"encoding/json"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// This development adapter exposes only the already admitted connection's
// headers observation. It supplies no caller-selected association or body view.
type MessagesWatchInput struct {
	Revision string `json:"revision,omitempty"`
	WaitMs   int64  `json:"wait_ms"`
	// AdapterKey is the key the host adapter registered for this connection
	// before any model turn. Turn always requires it; ReserveWake and
	// WakeTicket require it once one is registered (AB-1s F2).
	AdapterKey  string `json:"adapter_key,omitempty"`
	ReserveWake string `json:"reserve_wake,omitempty"`
	WakeTicket  string `json:"wake_ticket,omitempty"`
	// Health is an untrusted diagnostic claim about this connection's host
	// observer. It supplies no target, permission or receiving readiness.
	Health string `json:"health,omitempty"`
	// Turn is the adapter's turn signal: "ended" when the host turn ended,
	// "person" when a person's turn began. Either ends this inbox's woken
	// turn chain; it clears nothing of another inbox.
	Turn string `json:"turn,omitempty"`
}

// MessagesWatchResult combines the generated native header page with
// adapter custody, health and Asks metadata.
type MessagesWatchResult struct {
	wire.Page
	WatchMetadata
}

// WatchMetadata belongs to the adapter rather than the native page.
type WatchMetadata struct {
	Context         string `json:"context"`
	WakeTicket      string `json:"wake_ticket,omitempty"`
	WakeSender      string `json:"wake_sender,omitempty"`
	WakePointer     string `json:"wake_pointer,omitempty"`
	AsksWake        string `json:"asks_wake,omitempty"`
	OldestAsksWake  string `json:"oldest_asks_wake,omitempty"`
	AnswerableAsks  *int64 `json:"answerable_asks,omitempty"`
	WaitingAsks     *int64 `json:"waiting_asks,omitempty"`
	WaitingAsksWake string `json:"waiting_asks_wake,omitempty"`
	Reason          string `json:"reason,omitempty"`
	HealthSupported bool   `json:"health_supported,omitempty"`
}

func (out MessagesWatchResult) MarshalJSON() ([]byte, error) {
	// Encode just the native summary. In particular, no native cursor or
	// content can cross this adapter even if present in the source page.
	page := out.Page
	page.Cursor, page.Headers, page.Messages = "", nil, nil
	fields := map[string]json.RawMessage{
		"pending_until_unix_ms": json.RawMessage("0"),
		"reply_until_unix_ms":   json.RawMessage("0"),
		"wake":                  json.RawMessage(`""`),
	}
	if err := json.Unmarshal(wire.EncodePage(&page), &fields); err != nil {
		return nil, err
	}
	delete(fields, "cursor")
	delete(fields, "headers")
	delete(fields, "messages")
	extra, err := json.Marshal(out.WatchMetadata)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(extra, &fields); err != nil {
		return nil, err
	}
	return json.Marshal(fields)
}

func messagesWatchOutputSchema() map[string]any {
	properties := map[string]any{}
	for _, field := range []string{"outcome", "context", "revision", "wake", "wake_ticket", "wake_sender", "wake_pointer", "asks_wake", "reason"} {
		properties[field] = map[string]any{"type": "string"}
	}
	for _, field := range []string{"pending_until_unix_ms", "reply_until_unix_ms"} {
		properties[field] = map[string]any{"type": "integer"}
	}
	for _, field := range []string{"unread", "pending_questions", "oldest_unread_unix_ms", "newest_unread_unix_ms"} {
		properties[field] = map[string]any{"type": []string{"null", "integer"}}
	}
	properties["oldest_wake"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	properties["awaited_replies"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 50}
	properties["answerable_asks"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 128}
	properties["oldest_asks_wake"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	properties["waiting_asks"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 64}
	properties["waiting_asks_wake"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 128}
	properties["health_supported"] = map[string]any{"type": "boolean"}
	return map[string]any{"type": "object", "properties": properties,
		"required":             []string{"outcome", "context", "revision", "pending_until_unix_ms", "reply_until_unix_ms", "wake"},
		"additionalProperties": false}
}

type MessagesWatchBackend interface {
	MessagesWatch(context.Context, MessagesWatchInput) (MessagesWatchResult, error)
}

func addMessagesWatch(s *mcp.Server, b MessagesWatchBackend, bounded func(context.Context, func(context.Context) error) error) {
	mcp.AddTool(s, &mcp.Tool{Name: "oa_messages_watch", Description: "Read this connection's unread message headers, without bodies. Host adapters use it to wake a chat.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolp(false), IdempotentHint: true, OpenWorldHint: boolp(false)}, InputSchema: map[string]any{
		"type": "object", "properties": map[string]any{"revision": map[string]any{"type": "string", "maxLength": 256}, "wait_ms": map[string]any{"type": "integer", "minimum": 0, "maximum": 25000}, "adapter_key": map[string]any{"type": "string", "pattern": "^[0-9a-f]{32}$", "description": "Host adapter key."}, "reserve_wake": map[string]any{"type": "string", "maxLength": 128, "description": "Wake key to reserve; needs adapter_key."}, "wake_ticket": map[string]any{"type": "string", "maxLength": 64, "description": "Reservation to refresh; needs adapter_key."}, "health": map[string]any{"type": "string", "enum": []string{"watching", "recovering", "stopped_forbidden", "stopped_protocol", "stopped_context", "paused"}}, "turn": map[string]any{"type": "string", "enum": []string{"ended", "person"}, "description": "Turn change; needs adapter_key."}}, "required": []string{"wait_ms"}, "additionalProperties": false,
	}, OutputSchema: messagesWatchOutputSchema()}, func(ctx context.Context, _ *mcp.CallToolRequest, in MessagesWatchInput) (r *mcp.CallToolResult, _ any, err error) {
		err = bounded(ctx, func(ctx context.Context) error {
			out, e := b.MessagesWatch(ctx, in)
			if e != nil {
				return e
			}
			raw, e := json.Marshal(out)
			if e != nil {
				return e
			}
			r = messageEncodedContent(raw)
			return nil
		})
		return
	})
}

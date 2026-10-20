package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type MessagesReplyInput struct {
	MessageID string `json:"message_id" jsonschema:"Message ID of an open question addressed to this chat."`
	Body      string `json:"body" jsonschema:"Your reply text. Reuse exactly the same text to reconcile an uncertain reply."`
}
type MessagesReplyBackend interface {
	MessagesReply(context.Context, MessagesReplyInput) (wire.SendResult, error)
}

func messagesInstructions(b Backend) string {
	base := messagesBaseInstructions(b)
	if _, ok := b.(MessagesReplyBackend); ok {
		return "Answer a question with oa_messages_reply, passing only its message_id and body. " + base
	}
	return base
}

// ReplyUncertainStep follows a Reply result that does not prove absence of
// acceptance; the reconcile rule lives here instead of the tool description.
const ReplyUncertainStep = "The reply may or may not have been accepted. Call oa_messages_reply again on this connection with the same message_id and body; never change the body. If this connection closed since, do not reply again; tell the person the outcome is not known."

func addMessageReplyTool(s *mcp.Server, b MessagesBackend, bounded func(context.Context, func(context.Context) error) error, views ...*messagesReplyView) {
	var cold *messagesReplyView
	if len(views) != 0 {
		cold = views[0]
	}
	reply, ok := b.(MessagesReplyBackend)
	if !ok {
		return
	}
	mcp.AddTool(s, &mcp.Tool{Name: ToolMessagesReply, Description: "Answer a question addressed to this chat with its message_id and body.", Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: boolp(true)}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"message_id": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "body": map[string]any{"type": "string", "maxLength": 8192}}, "required": []string{"message_id", "body"}, "additionalProperties": false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in MessagesReplyInput) (r *mcp.CallToolResult, _ any, err error) {
		err = bounded(ctx, func(ctx context.Context) error {
			var out wire.SendResult
			var e error
			if cold == nil {
				out, e = reply.MessagesReply(ctx, in)
			} else {
				out, e = cold.reply(ctx, in, reply.MessagesReply)
			}
			if e == nil {
				r = messageReceiptContent(out)
				if messageSendUncertain(out) {
					r.Content = append(r.Content, assistantText(ReplyUncertainStep))
				}
			}
			return e
		})
		return
	})
}

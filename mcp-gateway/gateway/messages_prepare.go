package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"strings"
)

// MessageReceiverName translates display labels only, through nativecli's
// one label table. Native enrollment, authorization and live inbox lookup
// remain the receiving service's authority.
func MessageReceiverName(label string) string { return nativecli.AgentReceiverName(label) }

// MessageAgentTitle names OA's own agent connectors as the person knows them;
// nativecli owns the connector names, including Darwin's versioned labels.
func MessageAgentTitle(name string) string {
	name = strings.TrimPrefix(name, "app:")
	if title, ok := nativecli.AgentTitle(nativecli.AgentApplicationBase(name)); ok {
		return title
	}
	return name
}

func MessageReceiverResource(label string) string {
	name := MessageReceiverName(label)
	if strings.HasPrefix(name, "app:") {
		return name
	}
	return "app:" + name
}

type MessagesPrepareInput struct {
	Receiver       string `json:"receiver"`
	Context        string `json:"context,omitempty"`
	ContextTitle   string `json:"context_title,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Subject        string `json:"subject,omitempty"`
	Body           string `json:"body"`
	TTLMs          int64  `json:"ttl_ms,omitempty"`
	AnswerWithinMs int64  `json:"answer_within_ms,omitempty"`
}
type MessagesPrepareOutput struct {
	Outcome string       `json:"outcome"`
	Choices []wire.Inbox `json:"choices,omitempty"`
	// ChatTitle and Context name the chat a prepared request reaches.
	ChatTitle string `json:"chat_title,omitempty"`
	Context   string `json:"context,omitempty"`
	// Reason names why preparation produced no request, when the native
	// reply itself failed; Reach is "queued" when the selected receiver
	// will not see the message until its conversation is next used.
	Reason        string             `json:"reason,omitempty"`
	Reach         string             `json:"reach,omitempty"`
	Guidance      string             `json:"guidance"`
	SendArguments *MessagesSendInput `json:"send_arguments,omitempty"`
	Expires       string             `json:"expires,omitempty"`
	// Next is the one step the model takes now. Models stopped after a
	// prepared outcome and reported nothing sent (owner, R20c 2026-11-10).
	Next string `json:"next,omitempty"`
	// Tell is the one sentence for the person after a sent outcome, set per
	// reach. The one-call Send appends it; the prepared JSON carries it in
	// Guidance.
	Tell string `json:"-"`
}
type MessagesPrepareBackend interface {
	MessagesPrepare(context.Context, MessagesPrepareInput) (MessagesPrepareOutput, error)
}

func addMessagePrepareTool(s *mcp.Server, b MessagesBackend, bounded func(context.Context, func(context.Context) error) error) {
	mcp.AddTool(s, &mcp.Tool{Name: ToolMessagesPrepare, OutputSchema: outputSchema[MessagesPrepareOutput](), Description: "Check that a question or note can reach its receiver. Prepare alone sends nothing; pass its send_arguments unchanged to oa_messages_send. Tell the person the chat title the result names.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: false, OpenWorldHint: boolp(false)}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"receiver": map[string]any{"type": "string", "minLength": 1, "maxLength": 68}, "context": map[string]any{"type": "string", "description": "Chat context from oa_messages_inboxes_list."}, "context_title": map[string]any{"type": "string", "maxLength": 256, "description": "Chat title as oa_messages_inboxes_list shows it."}, "kind": map[string]any{"type": "string", "enum": []string{"question", "note"}}, "subject": map[string]any{"type": "string", "maxLength": 80}, "body": map[string]any{"type": "string", "minLength": 1, "maxLength": 8192}, "ttl_ms": map[string]any{"type": "integer", "minimum": 1000, "maximum": 604800000}, "answer_within_ms": map[string]any{"type": "integer", "minimum": 1000, "maximum": 604800000}}, "required": []string{"receiver", "body"}, "additionalProperties": false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in MessagesPrepareInput) (*mcp.CallToolResult, MessagesPrepareOutput, error) {
		var out MessagesPrepareOutput
		err := bounded(ctx, func(ctx context.Context) (err error) {
			native, ok := b.(MessagesPrepareBackend)
			if !ok {
				out = MessagesPrepareOutput{Outcome: "unavailable", Guidance: "Native message preparation unavailable."}
				return nil
			}
			out, err = native.MessagesPrepare(ctx, in)
			return err
		})
		return nil, out, err
	})
}

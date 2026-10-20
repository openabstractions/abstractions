package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	asks "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	watch "github.com/openabstractions/abstraction-watch/go"
)

type AsksWatchInput struct {
	Revision string `json:"revision,omitempty"`
	WaitMs   int64  `json:"wait_ms"`
}
type AsksWatchItem struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}
type AsksWatchResult struct {
	Outcome  string          `json:"outcome"`
	Revision string          `json:"revision"`
	Access   string          `json:"access"`
	Items    []AsksWatchItem `json:"items"`
}
type AsksWatchBackend interface {
	AsksWatch(context.Context, AsksWatchInput) (AsksWatchResult, error)
}

// ObserveAsks reads at most two native pages per existing responder operation:
// Poll's initial snapshot and its read before returning the observation.
// Poll owns observation. This additive MCP surface currently accepts wait=0.
// Host adapters wake a chat from the Messages watch's asks_wake (ASK-R14).
// Only visible records and the caller's own access enter the stamp. Global
// cursor/revision and protected records never enter this projection.
func ObserveAsks(ctx context.Context, in AsksWatchInput, responder AsksResponder, access func(context.Context) (string, error)) (AsksWatchResult, error) {
	empty := func(outcome string) AsksWatchResult {
		return AsksWatchResult{Outcome: outcome, Items: []AsksWatchItem{}}
	}
	if in.WaitMs != 0 || len(in.Revision) > 128 {
		return empty("invalid"), nil
	}
	if responder == nil || access == nil {
		return empty("unavailable"), nil
	}
	if err := ctx.Err(); err != nil {
		return empty("unknown"), err
	}
	read := func() (AsksWatchResult, string, error) {
		before, err := access(ctx)
		if err != nil {
			return empty("unknown"), "", err
		}
		if before != "permitted" && before != "not_granted" && before != "denied" {
			return empty("unavailable"), "outcome:unavailable", nil
		}
		questions, err := responder.ListAnswerableContext(ctx, "", 64)
		if err != nil {
			return empty("unknown"), "", err
		}
		if questions.Outcome != asks.OperatorPageOutcomePage {
			return empty(questions.Outcome.String()), "outcome:" + questions.Outcome.String(), nil
		}
		choices, err := responder.ListChoicesContext(ctx, "", 64)
		if err != nil {
			return empty("unknown"), "", err
		}
		if choices.Outcome != asks.OperatorPageOutcomePage {
			return empty(choices.Outcome.String()), "outcome:" + choices.Outcome.String(), nil
		}
		if !questions.Complete || questions.Next != "" || !choices.Complete || choices.Next != "" {
			return empty("incomplete"), "outcome:incomplete", nil
		}
		if len(questions.Records) > 64 || len(choices.Choices) > 64 {
			return empty("unknown"), "", errors.New("asks observation page bound")
		}
		after, err := access(ctx)
		if err != nil || before != after {
			return empty("unknown"), "", errors.New("asks observation access changed")
		}
		sort.Slice(questions.Records, func(i, j int) bool { return questions.Records[i].ID < questions.Records[j].ID })
		sort.Slice(choices.Choices, func(i, j int) bool { return choices.Choices[i].ID < choices.Choices[j].ID })
		// Content and allowed options affect the opaque stamp, but no title,
		// option label, body or caller-selected request key leaves this tool.
		raw, err := json.Marshal(struct {
			Access    string
			Questions []asks.RecordMetadata
			Choices   []asks.AnswerableChoice
		}{after, questions.Records, choices.Choices})
		if err != nil {
			return empty("unknown"), "", err
		}
		hash := sha256.Sum256(raw)
		out := AsksWatchResult{Outcome: "page", Revision: hex.EncodeToString(hash[:]), Access: after, Items: []AsksWatchItem{}}
		for _, q := range questions.Records {
			out.Items = append(out.Items, AsksWatchItem{q.ID, "question"})
		}
		for _, q := range choices.Choices {
			out.Items = append(out.Items, AsksWatchItem{q.ID, "choice"})
		}
		return out, out.Revision, nil
	}
	observation := watch.Poll(read, 15*time.Second, 0)
	defer observation.Close()
	notice, err := observation.Next(ctx)
	if err != nil {
		return empty("unknown"), err
	}
	if notice.Unavailable {
		return empty("unknown"), nil
	}
	return notice.Now, nil
}

func addAsksWatch(s *mcp.Server, b AsksWatchBackend, bounded func(context.Context, func(context.Context) error) error) {
	mcp.AddTool(s, &mcp.Tool{Name: "oa_asks_watch", Description: "Check for questions and choices you may answer, by ID and revision only. wait_ms must be 0.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolp(false)}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{"revision": map[string]any{"type": "string", "maxLength": 128}, "wait_ms": map[string]any{"type": "integer", "enum": []int{0}}}, "required": []string{"wait_ms"}, "additionalProperties": false}}, func(ctx context.Context, _ *mcp.CallToolRequest, in AsksWatchInput) (r *mcp.CallToolResult, _ any, err error) {
		err = bounded(ctx, func(ctx context.Context) error {
			out, e := b.AsksWatch(ctx, in)
			if e != nil {
				return e
			}
			raw, e := json.Marshal(out)
			if e == nil {
				r = messageContent(string(raw))
			}
			return e
		})
		return
	})
}

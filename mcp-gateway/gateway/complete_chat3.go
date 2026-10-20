package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	acceptance "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type chat3CompletionPort interface {
	GetHistoryWindow(context.Context) (wire.ChatHistoryRead, error)
	Start(context.Context, wire.RequestV3) (wire.ChatAdmission, error)
	Observe(context.Context, string, int64, int64, int64, int64) (wire.ChatPage, error)
	Cancel(context.Context, string) (wire.ChatCancellation, error)
}

func startChat3Completion(ctx context.Context, c chat3CompletionPort, request wire.RequestV3) (context.Context, wire.ChatAdmission, error) {
	local := slices.Contains(request.Content.Guarantees, wire.ChatGuaranteeLocalOnly)
	return startCompletionAwaitingHost(ctx, local, func() (wire.ChatAdmission, error) {
		return c.Start(ctx, request)
	}, func(a wire.ChatAdmission) bool {
		return a.Outcome == wire.ChatStartOutcomeUnavailable && activatingCompletionHost(a.Reason)
	})
}

// Completion uses the existing gateway caller-intent CAS owner. The record
// retains identity and a body digest, never prompt/reasoning content. A lost
// Start stays unknown and cannot silently acquire a new runtime epoch.
type chat3RequestRecord struct {
	Version  int                        `json:"version"`
	Digest   string                     `json:"digest"`
	Identity acceptance.RequestIdentity `json:"identity"`
	State    string                     `json:"state"`
	Request  string                     `json:"request,omitempty"`
	Model    string                     `json:"model,omitempty"`
}

func (o *OA) completeChat3(ctx context.Context, in CompleteInput) (CompleteOutput, error) {
	scope := facade.ScopeAny
	if in.OperationID == "" {
		scope = facade.ScopeLocal
		if in.Hosting == "hosted" {
			scope = facade.ScopeRemote
		}
	}
	c, err := o.machine.ResolveInferenceChatV3(ctx, facade.Requirements{Scope: scope})
	if err != nil {
		return CompleteOutput{}, err
	}
	return o.completeWithChat3(ctx, c, in)
}

func (o *OA) completeWithChat3(ctx context.Context, c chat3CompletionPort, in CompleteInput) (CompleteOutput, error) {
	if in.OperationID != "" {
		id, marked := strings.CutPrefix(in.OperationID, chat3Handle)
		if len(id) > 128 || in.Cursor == nil || *in.Cursor < 0 || in.Model != "" || in.Prompt != "" || in.Hosting != "" || in.Credential != "" || in.MaxOutput != 0 || in.RequestKey != "" || in.Conversation != "" ||
			in.ReasoningEffort != "" || in.ReasoningBudget != 0 || in.Image != "" || in.ImagePath != "" {
			return CompleteOutput{}, errors.New("gateway: resume requires only operation_id, cursor and reasoning opt-in")
		}
		out, err := waitCompletionChat3(ctx, c, id, *in.Cursor)
		if marked {
			out.OperationID = chat3Handle + out.OperationID
		}
		return out, err
	}
	// A reasoning choice or an image without include_reasoning runs on
	// chat@3 under a fresh identity of the gateway's own; include_reasoning
	// keeps its caller's persisted request_key.
	ephemeral := !in.IncludeReasoning && in.RequestKey == ""
	if in.Cursor != nil || !ephemeral && (in.RequestKey == "" || len(in.RequestKey) > 128 || !utf8.ValidString(in.RequestKey)) || in.Model == "" || len(in.Model) > 256 || in.Prompt == "" || len(in.Prompt) > 65536 || in.MaxOutput < 0 || in.MaxOutput > 4096 || in.Conversation != "" && !validConversation(in.Conversation) {
		return CompleteOutput{}, errors.New("gateway: reasoning inference requires a stable request_key and bounded model/prompt/output")
	}
	reasoning, err := chat3Reasoning(in)
	if err != nil {
		return CompleteOutput{}, err
	}
	if in.Hosting != "local" && in.Hosting != "hosted" || in.Hosting == "hosted" && !credentialName(in.Credential) || in.Hosting == "local" && in.Credential != "" {
		return CompleteOutput{}, errors.New("gateway: inference requires explicit local or named-credential hosted routing")
	}
	if in.MaxOutput == 0 {
		in.MaxOutput = defaultMaxOutput
	}
	parts := []wire.ChatInputPart{{Kind: wire.PartKindText, Text: in.Prompt}}
	if in.Image != "" || in.ImagePath != "" {
		image, err := o.imagePart(ctx, in)
		var staging *imageStagingError
		if errors.As(err, &staging) {
			return CompleteOutput{State: "ended", Outcome: "unavailable", Reason: staging.reason,
				Message: "OpenAbstractions could not stage the image in local storage. Check the OpenAbstractions gateway log for the storage failure."}, nil
		}
		if err != nil {
			return CompleteOutput{}, err
		}
		parts = append(parts, *image)
	}
	content := wire.ChatContent{Messages: []wire.ChatMessage{{Role: wire.RoleUser, Parts: parts}}, Options: &wire.ChatOptions{MaxOutput: in.MaxOutput, Reasoning: reasoning}}
	if in.Hosting == "hosted" {
		content.Guarantees = []wire.ChatGuarantee{wire.ChatGuaranteeHostedAllowed}
		content.Credential = in.Credential
	} else {
		content.Guarantees = []wire.ChatGuarantee{wire.ChatGuaranteeLocalOnly}
	}
	if ephemeral {
		return o.completeEphemeralChat3(ctx, c, in.Model, in.Conversation, content)
	}
	// The digest uses the existing MCP input encoding after defaulting. Key and
	// presentation opt-in are outside the immutable inference body.
	canonical := in
	canonical.RequestKey = ""
	canonical.IncludeReasoning = false
	body, err := json.Marshal(canonical)
	if err != nil {
		return CompleteOutput{}, err
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	path := filepath.Join(o.stateDir, fmt.Sprintf("chat3-request-%x.json", sha256.Sum256([]byte(in.RequestKey))))
	record, base, err := loadChat3Request(path)
	if os.IsNotExist(err) {
		window, readErr := c.GetHistoryWindow(ctx)
		if readErr != nil {
			return CompleteOutput{}, readErr
		}
		if window.Outcome != wire.ChatHistoryOutcomeAvailable || window.Window == nil {
			return CompleteOutput{State: "ended", Outcome: window.Outcome.String(), Reason: "history:unavailable"}, nil
		}
		record = chat3RequestRecord{Version: 1, Digest: digest, Identity: acceptance.RequestIdentity{Key: in.RequestKey, HistoryEpoch: window.Window.HistoryEpoch}, State: "prepared"}
		data, encodeErr := json.Marshal(record)
		if encodeErr != nil {
			return CompleteOutput{}, encodeErr
		}
		if writeErr := o.state.WriteContext(ctx, path, casapi.Value{}, data); writeErr != nil {
			record, base, err = loadChat3Request(path)
			if err != nil {
				return CompleteOutput{}, writeErr
			}
		} else {
			base, err = casapi.Value{Data: data}, nil
		}
	}
	if err != nil {
		return CompleteOutput{}, err
	}
	if record.Digest != digest || record.Identity.Key != in.RequestKey {
		return CompleteOutput{}, errors.New("gateway: request_key already binds another immutable inference body")
	}
	if record.State == "accepted" {
		out, err := waitCompletionChat3(ctx, c, record.Request, 0)
		out.Model = record.Model
		return out, err
	}
	if record.State == "sending" {
		return CompleteOutput{State: "unknown", Outcome: "unknown", Reason: "start:uncertain", Message: "The original Start outcome is unknown. It was not submitted again."}, nil
	}
	record.State = "sending"
	claimed, err := json.Marshal(record)
	if err != nil {
		return CompleteOutput{}, err
	}
	if err := o.state.WriteContext(ctx, path, base, claimed); err != nil {
		return CompleteOutput{State: "unknown", Outcome: "unknown", Reason: "start:contended"}, nil
	}
	request := wire.RequestV3{Identity: record.Identity, Target: wire.ChatTarget{Model: in.Model}, Conversation: in.Conversation, Content: content}
	follow, a, err := startChat3Completion(ctx, c, request)
	if err != nil {
		return CompleteOutput{State: "unknown", Outcome: "unknown", Reason: "start:uncertain", Message: "The original Start outcome is unknown. It was not submitted again."}, nil
	}
	if a.Outcome == wire.ChatStartOutcomeAccepted {
		record.State, record.Request = "accepted", a.Request
		record.Model = a.Model
	} else if a.Outcome != wire.ChatStartOutcomeUnknown && a.Outcome != wire.ChatStartOutcomeConflict {
		record.State = "preaccept"
	}
	updated, err := json.Marshal(record)
	if err != nil {
		return CompleteOutput{}, err
	}
	if err := o.state.WriteContext(ctx, path, casapi.Value{Data: claimed}, updated); err != nil {
		// An accepted native handle remains useful even when caller-side receipt
		// persistence fails. Returning it never authorizes another Start.
		if a.Outcome == wire.ChatStartOutcomeAccepted {
			return CompleteOutput{OperationID: a.Request, Model: a.Model, State: "working", Outcome: "pending", Message: "Working…"}, nil
		}
		return CompleteOutput{}, err
	}
	if a.Outcome != wire.ChatStartOutcomeAccepted {
		return CompleteOutput{State: "ended", Outcome: a.Outcome.String(), Reason: a.Reason, RetryAfterMS: a.RetryAfterMs,
			Message: o.visionRefusal(ctx, a.Reason, in.Model)}, nil
	}
	out, err := waitCompletionChat3(follow, c, a.Request, 0)
	out.Model = a.Model
	return out, err
}

// completeEphemeralChat3 starts one chat@3 request under a fresh identity
// and follows it; its handle carries chat3Handle so a resume reads chat@3.
func (o *OA) completeEphemeralChat3(ctx context.Context, c chat3CompletionPort, model, conversation string, content wire.ChatContent) (CompleteOutput, error) {
	window, err := c.GetHistoryWindow(ctx)
	if err != nil {
		return CompleteOutput{}, err
	}
	if window.Outcome != wire.ChatHistoryOutcomeAvailable || window.Window == nil {
		return CompleteOutput{State: "ended", Outcome: window.Outcome.String(), Reason: "history:unavailable"}, nil
	}
	key := make([]byte, 18)
	if _, err := rand.Read(key); err != nil {
		return CompleteOutput{}, err
	}
	request := wire.RequestV3{Identity: acceptance.RequestIdentity{Key: "complete-" + base64.RawURLEncoding.EncodeToString(key), HistoryEpoch: window.Window.HistoryEpoch}, Target: wire.ChatTarget{Model: model}, Conversation: conversation, Content: content}
	follow, a, err := startChat3Completion(ctx, c, request)
	if err != nil {
		return CompleteOutput{State: "unknown", Outcome: "unknown", Reason: "start:uncertain", Message: "The original Start outcome is unknown. It was not submitted again."}, nil
	}
	if a.Outcome != wire.ChatStartOutcomeAccepted {
		return CompleteOutput{State: "ended", Outcome: a.Outcome.String(), Reason: a.Reason, RetryAfterMS: a.RetryAfterMs,
			Message: o.completeChat3Refusal(ctx, a.Reason, model)}, nil
	}
	out, err := waitCompletionChat3(follow, c, a.Request, 0)
	out.OperationID, out.Model = chat3Handle+out.OperationID, a.Model
	return out, err
}

func loadChat3Request(path string) (chat3RequestRecord, casapi.Value, error) {
	b, err := readBounded(path, maxRequestRecordBytes)
	if err != nil {
		return chat3RequestRecord{}, casapi.Value{}, err
	}
	var record chat3RequestRecord
	if json.Unmarshal(b, &record) != nil || record.Version != 1 || len(record.Digest) != 64 || record.Identity.Key == "" || record.Identity.HistoryEpoch == "" || record.Identity.Attempt != 0 || record.State != "prepared" && record.State != "sending" && record.State != "preaccept" && record.State != "accepted" || record.State == "accepted" && (record.Request == "" || len(record.Request) > 128) {
		return record, casapi.Value{}, errors.New("gateway: invalid persisted chat identity")
	}
	return record, casapi.Value{Data: b}, nil
}

func waitCompletionChat3(ctx context.Context, c chat3CompletionPort, id string, cursor int64) (CompleteOutput, error) {
	until, bounded := followUntil(ctx)
	started := time.Now()
	out := CompleteOutput{OperationID: id, Cursor: cursor, State: "working", Outcome: "pending", Message: "Working…"}
	progress := func() {
		var done, total int64
		unit := ""
		if p := out.Progress; p != nil {
			unit = p.Unit
			if p.Done != nil {
				done = *p.Done
			}
			if p.Total != nil {
				total = *p.Total
			}
		}
		line, d, t := progressLine("Working", done, total, unit, time.Since(started))
		out.Message = line + "."
		reportProgress(ctx, line, d, t)
	}
	indexes := map[int64]int{}
	var text, refusal strings.Builder
	contentBytes := 0
	for {
		progress()
		wait, more := observeWait(until, bounded, 25*time.Second)
		if !more {
			out.Text = text.String()
			out.ModelRefusal = refusal.String()
			return out, nil
		}
		page, err := c.Observe(ctx, id, out.Cursor, 256, 65536, wait.Milliseconds())
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				out.Text = text.String()
				out.ModelRefusal = refusal.String()
				return out, nil
			}
			return out, err
		}
		if page.Outcome != wire.PageOutcomePage {
			return out, fmt.Errorf("gateway: observe %s", page.Outcome.String())
		}
		for _, event := range page.Events {
			if event.Sequence != out.Cursor {
				return out, errors.New("gateway: chat event cursor mismatch")
			}
			out.Cursor++
			if event.Progress != nil {
				p := event.Progress
				out.Progress = &CompleteProgress{Phase: string(p.Phase), Done: p.Done, Total: p.Total, Unit: p.Unit}
			}
			if part := event.Part; part != nil && part.Text != "" {
				contentBytes += len(part.Text)
				if contentBytes > maxCompletionBytes {
					return out, errors.New("gateway: inference result exceeds 262144-byte bound")
				}
				switch part.Kind {
				case wire.ChatPartKindText:
					text.WriteString(part.Text)
				case wire.ChatPartKindModelRefusal:
					refusal.WriteString(part.Text)
				case wire.ChatPartKindReasoning, wire.ChatPartKindReasoningSummary:
					index, ok := indexes[part.Block]
					if !ok {
						if len(indexes) >= maxCompletionParts {
							return out, errors.New("gateway: too many reasoning blocks")
						}
						index = len(out.Reasoning)
						indexes[part.Block] = index
						out.Reasoning = append(out.Reasoning, CompleteReasoning{Block: part.Block, Kind: part.Kind.String()})
					}
					if out.Reasoning[index].Kind != part.Kind.String() {
						return out, errors.New("gateway: reasoning block changed kind")
					}
					out.Reasoning[index].Text += part.Text
				}
			}
			if end := event.End; end != nil {
				out.State, out.Outcome, out.Reason, out.Message = "ended", end.Outcome.String(), end.Cause, ""
				out.RetryAfterMS = end.RetryAfterMs
				if end.FinishReason != nil {
					finish := *end.FinishReason
					if !finish.Known() {
						finish = wire.FinishReasonOther
					}
					out.StopReason = string(finish)
				}
				if end.ServedBy != nil {
					out.Host = end.ServedBy.Server
				}
				out.ReasoningApplied = appliedReasoning(end.ReasoningApplied)
				out.Text, out.ModelRefusal = text.String(), refusal.String()
				if !page.AtEnd || page.Next != out.Cursor {
					return out, errors.New("gateway: terminal event cursor mismatch")
				}
				return out, nil
			}
		}
		if page.Next != out.Cursor {
			return out, errors.New("gateway: page cursor mismatch")
		}
		if page.AtEnd {
			return out, errors.New("gateway: chat stream ended without its actual terminal")
		}
	}
}

// Explicit Content prevents the SDK's default JSON text projection from
// placing provider-public reasoning in the model's answer context. Out remains
// the complete structuredContent under the generated tool output schema.
func completeChat3Content(out CompleteOutput) *mcp.CallToolResult {
	text := out.Text
	if text == "" {
		text = out.ModelRefusal
	}
	if text == "" {
		text = out.Message
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// completeLabelled runs a new local call under its conversation label on
// chat@3. Calls of one program with one label keep their engine slot, and the
// runtime folds the label into its scope digest (CONV-T3); the label grants
// no authority. Start is sent once, as for an unlabelled call: a lost Start
// reads unknown. handled is false when this runtime serves no chat@3 history,
// and the caller then runs the unlabelled chat@2 call.
func (o *OA) completeLabelled(ctx context.Context, in CompleteInput) (CompleteOutput, bool, error) {
	c, err := o.machine.ResolveInferenceChatV3(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return CompleteOutput{}, false, nil
	}
	return completeLabelledWith(ctx, c, in)
}

func completeLabelledWith(ctx context.Context, c chat3CompletionPort, in CompleteInput) (CompleteOutput, bool, error) {
	window, err := c.GetHistoryWindow(ctx)
	if err != nil || window.Outcome != wire.ChatHistoryOutcomeAvailable || window.Window == nil {
		return CompleteOutput{}, false, nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return CompleteOutput{}, true, err
	}
	request := wire.RequestV3{Identity: acceptance.RequestIdentity{Key: "mcp-" + hex.EncodeToString(nonce[:]), HistoryEpoch: window.Window.HistoryEpoch},
		Target: wire.ChatTarget{Model: in.Model}, Conversation: in.Conversation,
		Content: wire.ChatContent{Messages: []wire.ChatMessage{{Role: wire.RoleUser, Parts: []wire.ChatInputPart{{Kind: wire.PartKindText, Text: in.Prompt}}}},
			Options: &wire.ChatOptions{MaxOutput: in.MaxOutput}, Guarantees: []wire.ChatGuarantee{wire.ChatGuaranteeLocalOnly}}}
	follow, a, err := startChat3Completion(ctx, c, request)
	if err != nil {
		return CompleteOutput{State: "unknown", Outcome: "unknown", Reason: "start:uncertain", Message: "The original Start outcome is unknown. It was not submitted again."}, true, nil
	}
	if a.Outcome != wire.ChatStartOutcomeAccepted {
		return CompleteOutput{State: "ended", Outcome: a.Outcome.String(), Reason: a.Reason, RetryAfterMS: a.RetryAfterMs,
			Message: refusalSentence(ctx, a.Reason, in.Model, "chat")}, true, nil
	}
	out, err := waitCompletionChat3(follow, c, a.Request, 0)
	out.Model = a.Model
	return out, true, err
}

// resumeChat3 continues a call that completeLabelled started. ok is false for
// an operation chat@3 does not know, which the caller resumes on chat@2.
func (o *OA) resumeChat3(ctx context.Context, id string, cursor int64) (CompleteOutput, bool, error) {
	if len(id) > 128 {
		return CompleteOutput{}, false, nil
	}
	c, err := o.machine.ResolveInferenceChatV3(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return CompleteOutput{}, false, nil
	}
	return resumeChat3With(ctx, c, id, cursor)
}

func resumeChat3With(ctx context.Context, c chat3CompletionPort, id string, cursor int64) (CompleteOutput, bool, error) {
	page, err := c.Observe(ctx, id, cursor, 1, 1, 0)
	if err != nil || page.Outcome == wire.PageOutcomeUnknown {
		return CompleteOutput{}, false, nil
	}
	out, err := waitCompletionChat3(ctx, c, id, cursor)
	return out, true, err
}

// validConversation is chat@3's conversation rule: 1..128 bytes of valid
// UTF-8 with no control characters.
func validConversation(label string) bool {
	if label == "" || len(label) > 128 || !utf8.ValidString(label) {
		return false
	}
	for _, r := range label {
		if r < 0x20 || r >= 0x7f && r <= 0x9f {
			return false
		}
	}
	return true
}

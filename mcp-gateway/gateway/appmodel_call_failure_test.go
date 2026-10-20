package gateway

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

type appModelFailingFixture struct {
	*appModelFixture
	err    error
	answer *appmodel.CommandResult
	result applications.CallResult
}

func (b *appModelFailingFixture) ApplicationsCall(context.Context, appmodel.GatewayCall) (applications.CallResult, error) {
	if b.err != nil {
		return applications.CallResult{}, b.err
	}
	if b.answer == nil {
		return b.result, nil
	}
	return applications.CallResult{Outcome: applications.OutcomeApplied, Answer: &appmodel.Envelope{Result: b.answer}}, nil
}

func appModelFailingSession(t *testing.T, backend Backend) *mcp.ClientSession {
	t.Helper()
	serverRead, clientWrite := io.Pipe()
	clientRead, serverWrite := io.Pipe()
	t.Cleanup(func() { serverRead.Close(); clientWrite.Close(); clientRead.Close(); serverWrite.Close() })
	ss, err := NewServer(backend).Connect(t.Context(), &mcp.IOTransport{Reader: serverRead, Writer: serverWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "failing", Version: "1"}, nil).Connect(t.Context(), &mcp.IOTransport{Reader: clientRead, Writer: clientWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// Owner R31, 2026-11-12: workflow.list on ComfyUI answered a bare "EOF".
// A command whose connection ends, or whose wait runs out, answers one plain
// sentence naming that state, and an OpenAbstractions reason word gets its
// sentence too.
func TestAppModelCallFailureIsAPlainSentence(t *testing.T) {
	for _, c := range []struct {
		name   string
		err    error
		answer *appmodel.CommandResult
		want   string
	}{
		{"eof", io.EOF, nil, "lost its connection to comfyui"},
		{"wait", context.DeadlineExceeded, nil, "did not answer within the command's wait"},
		{"owned_host", nil, &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeUnavailable, Reason: "owned_host"}, "comfyui is not open in OpenAbstractions"},
		{"ended", nil, &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeStale, Reason: "instance:62d5"}, "That comfyui instance has ended"},
	} {
		t.Run(c.name, func(t *testing.T) {
			backend := &appModelFailingFixture{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}, err: c.err, answer: c.answer}
			cs := appModelFailingSession(t, backend)
			got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolApplicationsCall, Arguments: map[string]any{"application": "comfyui", "instance": "live", "tool": "workflow.list", "arguments": map[string]any{}}})
			if err != nil || got == nil || !got.IsError {
				t.Fatalf("%+v %v", got, err)
			}
			var text []string
			for _, content := range got.Content {
				if t, ok := content.(*mcp.TextContent); ok {
					text = append(text, t.Text)
				}
			}
			joined := strings.Join(text, "\n")
			if !strings.Contains(joined, c.want) || strings.Contains(joined, "EOF") || strings.Contains(joined, "deadline exceeded") {
				t.Fatalf("answer: %q", joined)
			}
		})
	}
}

func TestAppModelUnconfirmedCallPreservesExactReceiptGuidance(t *testing.T) {
	backend := &appModelFailingFixture{appModelFixture: &appModelFixture{fixtureBackend: &fixtureBackend{}}, err: context.DeadlineExceeded}
	args := map[string]any{"application": "comfyui", "instance": "live", "tool": "workflow.run", "arguments": map[string]any{"workflow": "example"}, "idempotency_key": "original-key"}
	call := func(backend *appModelFailingFixture, wantReason, wantSentence string) {
		t.Helper()
		cs := appModelFailingSession(t, backend)
		got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolApplicationsCall, Arguments: args})
		if err != nil || got == nil || !got.IsError {
			t.Fatalf("%+v %v", got, err)
		}
		structured, ok := got.StructuredContent.(map[string]any)
		if !ok || structured["outcome"] != "unknown" || structured["reason"] != wantReason {
			t.Fatalf("result: %+v", got.StructuredContent)
		}
		last, ok := got.Content[len(got.Content)-1].(*mcp.TextContent)
		if !ok || !strings.Contains(last.Text, "The command may have taken effect.") || !strings.Contains(last.Text, wantSentence) {
			t.Fatalf("guidance: %+v", got.Content)
		}
	}
	call(backend, "connection", "repeat the exact command with the original idempotency_key")
	receipt := &appModelFailingFixture{appModelFixture: backend.appModelFixture, result: applications.CallResult{Outcome: applications.OutcomeUnknown, Reason: "receipt"}}
	call(receipt, "receipt", "Check the application's state before submitting another command.")
}

// Owner R31, 2026-11-12: open answered ready while Comfy Desktop sat on its
// dashboard. An open the application answers not ready, or unavailable,
// relays the application's own reason in its sentence.
func TestAppModelOpenRelaysTheApplicationsReason(t *testing.T) {
	notReady := appModelOpenSentence("comfyui", applications.ActivationResult{Outcome: applications.ActivationOutcomeNotReady, Reason: "activation:refused: Comfy Desktop is open, but no ComfyUI install runs in it. Ask the person to start ComfyUI in Comfy Desktop", Title: "ComfyUI"})
	if !strings.Contains(notReady, "ComfyUI is not ready: Comfy Desktop is open, but no ComfyUI install runs in it") || strings.Contains(notReady, "activation:") {
		t.Fatalf("not ready: %q", notReady)
	}
	failed := appModelOpenSentence("comfyui", applications.ActivationResult{Outcome: applications.ActivationOutcomeUnavailable, Reason: "activation:unavailable: ComfyUI did not start"})
	if !strings.Contains(failed, "comfyui did not open: ComfyUI did not start") {
		t.Fatalf("unavailable: %q", failed)
	}
}

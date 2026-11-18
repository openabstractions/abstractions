package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

const receiverStartFrameLimit = 1 << 20

// The native runtime confirms its exact owned child before any vendor probe.
// Neither registration, the inherited environment nor the framed payload grants
// this authority. Native source fixtures replace this function explicitly.
var receiverStartConfirmation = confirmNativeReceiverStart
var receiverStartMachine = client.Discover
var receiverStartCLISelector = selectAgentStartCLI

func confirmNativeReceiverStart(ctx context.Context, p startprofile.Profile) error {
	program, err := os.Executable()
	if err != nil {
		return err
	}
	application := dedicatedAgentApplication(program)
	if application == "" {
		return errors.New("dedicated_receiver_required")
	}
	machine := receiverStartMachine()
	directory, err := machine.ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return err
	}
	page, err := directory.Observe(ctx, "", 0)
	if err != nil || page.Outcome != wire.ApplicationOutcomePage {
		return errors.New("registration_unavailable")
	}
	var descriptor *wire.ApplicationDescriptor
	for i := range page.Applications {
		entry := &page.Applications[i]
		if entry.Descriptor.Name == application && !entry.SelfRegistered {
			descriptor = &entry.Descriptor
			break
		}
	}
	if descriptor == nil || descriptor.Activation == nil {
		return errors.New("registration_unavailable")
	}
	registered, err := startprofile.Parse(descriptor.Activation.Arguments)
	if err != nil || registered != p {
		return errors.New("recipe_changed")
	}
	service, err := machine.ResolveService(ctx, "abstraction.facade/applications@3", client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return err
	}
	change, err := applications.NewApplicationsClient(service.Transport().WithContext(ctx)).ConfirmReceiverStart(p.Fingerprint(descriptor.Program))
	if err != nil || change.Outcome != wire.ApplicationOutcomeApplied || change.Instance != "" {
		return errors.New("runtime_start_admission_unavailable")
	}
	return nil
}

// A custom transport reuses the SDK's MCP semantics and OA's one frame codec.
// This private channel has no stdio-newline fallback or transport detection.
type receiverStartTransport struct{ connection *receiverStartConnection }

func (t *receiverStartTransport) Connect(context.Context) (mcp.Connection, error) {
	return t.connection, nil
}
func (t *receiverStartTransport) SupportsProtocolVersion(v string) bool { return v == "2025-11-25" }

type receiverStartConnection struct {
	reader   io.ReadCloser
	writer   io.WriteCloser
	mu       sync.Mutex
	once     sync.Once
	closeErr error
}

func (c *receiverStartConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, err := listen.ReadFrameFrom(c.reader, receiverStartFrameLimit)
	if err != nil {
		return nil, err
	}
	return jsonrpc.DecodeMessage(raw)
}
func (c *receiverStartConnection) Write(ctx context.Context, message jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return listen.WriteFrameTo(c.writer, raw, receiverStartFrameLimit)
}
func (c *receiverStartConnection) Close() error {
	c.once.Do(func() { c.closeErr = errors.Join(c.reader.Close(), c.writer.Close()) })
	return c.closeErr
}
func (c *receiverStartConnection) SessionID() string { return "" }

// receiverStartAnswer is the owned start's result and, for an addressed
// session, how that session received the turn.
type receiverStartAnswer struct {
	Result   applications.HostActivationResult
	Delivery string
}

type receiverColdSelectionKey struct{}

// receiverWakeKey carries the owned channel's wake key to the start.
type receiverWakeKey struct{}

func serveReceiverStartOwned(ctx context.Context, in io.ReadCloser, out io.WriteCloser, run func(context.Context, startprofile.CustodyBinding, string) receiverStartAnswer, list ...func(context.Context, startprofile.CustodyBinding) ([]receiverSession, error)) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "openabstractions-receiver-start", Version: "0.8"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}, Capabilities: &mcp.ServerCapabilities{}})
	var stateMu sync.Mutex
	state := 0
	s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			stateMu.Lock()
			allowed := false
			switch {
			case state == 0 && method == "initialize":
				state = 1
				allowed = true
			case state == 1 && method == "notifications/initialized":
				state = 2
				allowed = true
			case state == 2 && method == "tools/call":
				state = 3
				allowed = true
			}
			if !allowed {
				state = -1
			}
			stateMu.Unlock()
			if !allowed {
				return nil, errors.New("receiver start: private protocol sequence")
			}
			return next(ctx, method, request)
		}
	})
	if len(list) == 1 && list[0] != nil {
		// The read-only listing of the agent's existing conversations, on the
		// same confirmed owned invocation; it starts nothing.
		s.AddTool(&mcp.Tool{Name: "receiver_sessions", Description: "List the registered agent's existing conversations.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if request.Params == nil || !bytes.Equal(bytes.TrimSpace(request.Params.Arguments), []byte("{}")) {
				return nil, errors.New("receiver sessions: fixed empty payload required")
			}
			binding, err := startprofile.ParseCustodyMetadata(request.Params.GetMeta())
			if err != nil {
				return nil, err
			}
			sessions, err := list[0](ctx, binding)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(receiverSessionsPage{Sessions: sessions})
			if err != nil {
				return nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
		})
	}
	s.AddTool(&mcp.Tool{Name: "receiver_start", Description: "Execute the already confirmed registered receiver recipe.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if request.Params == nil || !bytes.Equal(bytes.TrimSpace(request.Params.Arguments), []byte("{}")) {
			return nil, errors.New("receiver start: fixed empty payload required")
		}
		binding, err := startprofile.ParseCustodyMetadata(request.Params.GetMeta())
		if err != nil {
			return nil, err
		}
		target, err := startprofile.ParseTargetMetadata(request.Params.GetMeta())
		if err != nil {
			return nil, err
		}
		cold, err := startprofile.ParseColdResumeMetadata(request.Params.GetMeta())
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(target, "chat-") && cold == nil {
			return nil, errors.New("receiver start: canonical selection required")
		}
		if cold != nil {
			if cold.Context != target {
				return nil, errors.New("receiver start: cold target mismatch")
			}
			// A Claude selection carries its launch; a Codex one its thread.
			if _, err := cold.Launch(); err == nil {
				if _, err := parseClaudeColdBinding(cold.Binding, cold.CwdKey, cold.Context, true); err != nil {
					return nil, err
				}
			}
			ctx = context.WithValue(ctx, receiverColdSelectionKey{}, cold)
		}
		wake, err := startprofile.ParseWakeMetadata(request.Params.GetMeta())
		if err != nil {
			return nil, err
		}
		if wake != "" {
			if cold == nil {
				return nil, errors.New("receiver start: wake key without a chat selection")
			}
			ctx = context.WithValue(ctx, receiverWakeKey{}, wake)
		}
		answer := run(ctx, binding, target)
		reply := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(applications.EncodeHostActivationResult(&answer.Result))}}}
		if answer.Delivery != "" {
			reply.Meta = mcp.Meta{agentDeliveryMetaKey: answer.Delivery}
		}
		return reply, nil
	})
	return s.Run(ctx, &receiverStartTransport{&receiverStartConnection{reader: in, writer: out}})
}

func ownedReceiverStartCommand(args []string, in io.ReadCloser, out io.WriteCloser, confirm func(context.Context, startprofile.Profile) error) int {
	p, err := startprofile.Parse(args)
	if err != nil || confirm == nil {
		return 3
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(p.TimeoutMS)*time.Millisecond)
	defer cancel()
	if confirm(ctx, p) != nil {
		return 3
	}
	err = serveReceiverStartOwned(ctx, in, out, func(call context.Context, binding startprofile.CustodyBinding, target string) receiverStartAnswer {
		cold, _ := call.Value(receiverColdSelectionKey{}).(*startprofile.ColdResume)
		wake, _ := call.Value(receiverWakeKey{}).(string)
		resolve := registeredAgentStartResolverCustody(args, agentStartCustody{binding: binding, admission: func(check context.Context) error { return confirm(check, p) }, target: target, cold: cold, wake: wake})
		result := runAgentStart(call, p.Name, p.Context, resolve)
		if result.Outcome == "refused" {
			return receiverStartAnswer{Result: applications.HostActivationResult{Outcome: applications.HostActivationOutcomeRefused, Reason: "start_refused"}}
		}
		if result.Outcome != "exited" || result.Capture != "retained" {
			return receiverStartAnswer{Result: applications.HostActivationResult{Outcome: applications.HostActivationOutcomeUnavailable, Reason: "execution_or_capture_incomplete"}, Delivery: result.Delivery}
		}
		return receiverStartAnswer{Result: applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied}, Delivery: result.Delivery}
	}, func(call context.Context, binding startprofile.CustodyBinding) ([]receiverSession, error) {
		sessions := []receiverSession{}
		resolve := registeredAgentStartResolverForList(args, func(check context.Context) error { return confirm(check, p) }, &sessions, binding)
		if _, err := resolve(call, p.Name, p.Context); !errors.Is(err, errReceiverSessionsListed) {
			return nil, errors.Join(errors.New("receiver sessions: listing unavailable"), err)
		}
		return sessions, nil
	})
	if err != nil {
		return 3
	}
	return 0
}

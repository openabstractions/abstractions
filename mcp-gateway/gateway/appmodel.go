package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	cas "github.com/openabstractions/abstraction-cas/go"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
)

const (
	ToolApplicationsDescribe = "oa_applications_describe"
	ToolApplicationsCommands = "oa_applications_commands_list"
	ToolApplicationsCall     = "oa_applications_command_call"
	ToolApplicationsOpen     = "oa_applications_open"
)

// AppModelBackend is optional so directory-only integrations keep their existing
// catalogue. Native applications@3 owns caller admission, policy and mediation.
type AppModelBackend interface {
	ApplicationsDescribe(context.Context, appmodel.ApplicationsQuery) (applications.DescribeResult, error)
	ApplicationsCommands(context.Context, appmodel.CommandsQuery) (applications.CommandsResult, error)
	ApplicationsCall(context.Context, appmodel.GatewayCall) (applications.CallResult, error)
	ApplicationsOpen(context.Context, applications.ActivateRequest) (applications.ActivationResult, error)
}

func (o *OA) appModelClient(ctx context.Context) (*applications.ApplicationsClient, error) {
	binding, err := o.machine.ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	return applications.NewApplicationsClient(binding.Transport().WithDefaults(appCommandBudget(ctx), 1<<20).WithContext(ctx)), nil
}

func (o *OA) ApplicationsDescribe(ctx context.Context, query appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	c, err := o.appModelClient(ctx)
	if err != nil {
		return applications.DescribeResult{}, err
	}
	return c.Describe(query)
}

func (o *OA) ApplicationsCommands(ctx context.Context, query appmodel.CommandsQuery) (applications.CommandsResult, error) {
	c, err := o.appModelClient(ctx)
	if err != nil {
		return applications.CommandsResult{}, err
	}
	return c.Commands(query)
}

func (o *OA) ApplicationsCall(ctx context.Context, call appmodel.GatewayCall) (applications.CallResult, error) {
	key, err := o.appModelCallKey(ctx, call)
	if err != nil {
		return applications.CallResult{}, err
	}
	c, err := o.appModelClient(ctx)
	if err != nil {
		return applications.CallResult{}, err
	}
	return c.Call(key, call)
}

// Opening runs the kept application's declared activation verb through the
// native activation owner, which decides the verb's class for an agent and
// reuses a running instance. A repeated open renews the same instance's page.
func (o *OA) ApplicationsOpen(ctx context.Context, request applications.ActivateRequest) (applications.ActivationResult, error) {
	key, err := applications.NewRequestKey()
	if err != nil {
		return applications.ActivationResult{}, err
	}
	c, err := o.appModelClient(ctx)
	if err != nil {
		return applications.ActivationResult{}, err
	}
	return c.Activate(key, request)
}

// Reconciliation follows the caller's inner operation identity. Excluding the
// body from the key lets the native receipt owner refuse changed arguments.
// A keyless call gets a fresh outer key: the native gateway generates its
// inner key, and two identical keyless calls are two calls (FAC-M32).
func appModelOperation(call appmodel.GatewayCall) (string, error) {
	var context appmodel.ContextRef
	var key string
	switch call.Tool {
	case "app_undo":
		v, err := appmodel.DecodeUndoRequest([]byte(call.Arguments))
		if err == nil {
			context, key = v.Context, v.IdempotencyKey
		}
	case "app_describe", "app_contexts", "app_read", "app_changes", "app_reveal", "app_job":
	default:
		// A command call names its context by name; the native gateway reads
		// the epoch, and the same key in the same context is the same call.
		context, key = appmodel.ContextRef{Instance: call.Instance, Context: call.Context}, call.IdempotencyKey
	}
	if key == "" {
		return "", nil
	}
	user, err := identity.Self()
	if err != nil {
		return "", err
	}
	account, err := user.Account()
	if err != nil {
		return "", err
	}
	program, err := os.Executable()
	if err != nil {
		return "", err
	}
	logical, _ := json.Marshal([]string{account, identity.CanonicalProgramPath(program), call.Application, call.Instance, call.Tool, context.Context, context.Epoch, key})
	sum := sha256.Sum256(logical)
	return hex.EncodeToString(sum[:]), nil
}

type appModelRequestRecord struct {
	Version int               `json:"version"`
	Keys    map[string]string `json:"keys"`
}

const maxAppModelAssociations = 1024
const maxAppModelAssociationBytes = 256 << 10

// Retain only the transport identity before dispatch, using the existing
// bounded CAS store. Native applications@3 remains the receipt/result owner.
// An expired or uncertain operation always retains its original native key.
func (o *OA) appModelCallKey(ctx context.Context, call appmodel.GatewayCall) (string, error) {
	operation, err := appModelOperation(call)
	if err != nil {
		return "", err
	}
	if operation == "" {
		return applications.NewRequestKey()
	}
	path := filepath.Join(o.stateDir, "appmodel-requests.json")
	store := casapi.BoundedFileStore{MaxBytes: maxAppModelAssociationBytes}
	for attempt := 0; attempt < 8; attempt++ {
		base, err := store.Read(path)
		if err != nil && !os.IsNotExist(err) {
			return "", err
		}
		record := appModelRequestRecord{Version: 1, Keys: map[string]string{}}
		if base.Data != nil {
			if err := json.Unmarshal(base.Data, &record); err != nil || record.Version != 1 || record.Keys == nil || len(record.Keys) > maxAppModelAssociations {
				return "", errors.New("gateway: invalid AppModel operation identities")
			}
			for logical, key := range record.Keys {
				if len(logical) != 64 || key == "" || len(key) > 128 {
					return "", errors.New("gateway: invalid AppModel operation identity")
				}
			}
		}
		if key := record.Keys[operation]; key != "" {
			return key, nil
		}
		if len(record.Keys) >= maxAppModelAssociations {
			return "", errors.New("gateway: AppModel operation identity capacity reached")
		}
		key, err := applications.NewRequestKey()
		if err != nil {
			return "", err
		}
		record.Keys[operation] = key
		raw, _ := json.Marshal(record)
		if err := store.WriteContext(ctx, path, base, raw); err != nil {
			if errors.Is(err, cas.ErrMoved) {
				continue
			}
			return "", err
		}
		return key, nil
	}
	return "", errors.New("gateway: AppModel operation identity contention")
}

func appModelResult(raw []byte, failed bool) (*mcp.CallToolResult, error) {
	if len(raw) > 1<<20 {
		return nil, errors.New("gateway: AppModel answer exceeds 1048576-byte bound")
	}
	var structured any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&structured); err != nil {
		return nil, err
	}
	if record, ok := structured.(map[string]any); ok {
		switch record["outcome"] {
		case "applied", "proposed", "accepted", "page", "ready":
		default:
			failed = true
		}
	}
	return &mcp.CallToolResult{IsError: failed, StructuredContent: structured, Content: []mcp.Content{&mcp.TextContent{Text: string(raw), Meta: mcp.Meta{"org.openabstractions/untrusted": true}, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}}}}, nil
}

func appModelNativeRefusal(outcome applications.Outcome, reason string) (*mcp.CallToolResult, error) {
	raw, err := json.Marshal(map[string]string{"outcome": outcome.String(), "reason": reason})
	if err != nil {
		return nil, err
	}
	return appModelResult(raw, true)
}

func appModelInvalid(tool string, err error) (*mcp.CallToolResult, error) {
	word := "invalid"
	var refusal *appmodel.Refusal
	if errors.As(err, &refusal) {
		word = refusal.Word
	}
	var record any
	switch tool {
	case ToolApplicationsDescribe:
		record = &appmodel.ApplicationListing{Outcome: appmodel.ReadingOutcomeInvalid, Applications: []appmodel.ListedApplication{}, Reason: word}
	case ToolApplicationsCommands:
		record = &appmodel.CommandListing{Outcome: appmodel.ReadingOutcomeInvalid, Commands: []appmodel.CommandSpec{}, Reason: word}
	case ToolApplicationsOpen:
		var input openInputError
		if errors.As(err, &input) {
			word = input.reason
		}
		out := applications.ActivationResult{Outcome: applications.ActivationOutcomeInvalid, Reason: word}
		result, err := appModelResult(applications.EncodeActivationResult(&out), true)
		if err != nil {
			return nil, err
		}
		sentence := input.message
		if sentence == "" {
			sentence = appModelOpenSentence("the application", out)
		}
		result.Content = append(result.Content, &mcp.TextContent{Text: sentence, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}})
		return result, nil
	default:
		record = &appmodel.CommandResult{Outcome: appmodel.CommandOutcomeInvalid, Reason: word, Effects: `{}`}
	}
	raw, encodeErr := appmodel.ToJSON(record)
	if encodeErr != nil {
		return nil, encodeErr
	}
	return appModelResult(raw, true)
}

func appModelAnswer(tool string, answer *appmodel.Envelope) ([]byte, error) {
	if answer == nil {
		return nil, errors.New("gateway: missing native AppModel answer")
	}
	var record any
	switch tool {
	case "app_describe":
		record = answer.Description
	case "app_contexts":
		record = answer.Contexts
	case "app_read":
		record = answer.Snapshot
	case "app_changes":
		record = answer.Changes
	default:
		record = answer.Result
	}
	raw, err := appmodel.ToJSON(record)
	if err != nil {
		return nil, fmt.Errorf("gateway: native AppModel answer: %w", err)
	}
	return raw, nil
}

func addAppModelTools(s *mcp.Server, backend AppModelBackend, bounded func(context.Context, func(context.Context) error) error) {
	stringProperty := map[string]any{"type": "string"}
	schema := func(properties map[string]any, required ...string) map[string]any {
		out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			out["required"] = required
		}
		return out
	}
	read := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: boolp(false)}
	s.AddTool(&mcp.Tool{Name: ToolApplicationsDescribe, Description: "Describe applications and their open instances. Leave application empty to list those you may read or open. See open_verbs for eligible activation verbs and their payload schemas.", Annotations: read, InputSchema: schema(map[string]any{"application": stringProperty})}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := appmodel.DecodeApplicationsQuery(request.Params.Arguments)
		if err != nil {
			return appModelInvalid(ToolApplicationsDescribe, err)
		}
		var out applications.DescribeResult
		err = bounded(ctx, func(ctx context.Context) (err error) {
			out, err = backend.ApplicationsDescribe(ctx, *query)
			return err
		})
		if err != nil {
			return nil, err
		}
		if out.Outcome != applications.OutcomePage {
			return appModelNativeRefusal(out.Outcome, out.Reason)
		}
		raw, err := appmodel.ToJSON(out.Answer)
		if err != nil {
			return nil, err
		}
		return appModelResult(raw, false)
	})
	s.AddTool(&mcp.Tool{Name: ToolApplicationsCommands, Description: "List the commands of one open application instance.", Annotations: read, InputSchema: schema(map[string]any{"application": stringProperty, "instance": stringProperty}, "application", "instance")}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := appmodel.DecodeCommandsQuery(request.Params.Arguments)
		if err != nil {
			return appModelInvalid(ToolApplicationsCommands, err)
		}
		var out applications.CommandsResult
		err = bounded(ctx, func(ctx context.Context) (err error) {
			out, err = backend.ApplicationsCommands(ctx, *query)
			return err
		})
		if err != nil {
			return nil, err
		}
		if out.Outcome != applications.OutcomePage {
			return appModelNativeRefusal(out.Outcome, out.Reason)
		}
		raw, err := appmodel.ToJSON(out.Answer)
		if err != nil {
			return nil, err
		}
		return appModelResult(raw, false)
	})
	s.AddTool(&mcp.Tool{Name: ToolApplicationsCall, Description: appModelCallDescription, Annotations: &mcp.ToolAnnotations{OpenWorldHint: boolp(true), DestructiveHint: boolp(true)}, InputSchema: schema(map[string]any{
		"application":       stringProperty,
		"instance":          stringProperty,
		"tool":              stringProperty,
		"arguments":         map[string]any{"type": "object"},
		"mode":              map[string]any{"type": "string", "enum": []string{"propose", "apply"}},
		"expected_revision": map[string]any{"type": "string", "description": "Revision your change depends on."},
		"idempotency_key":   map[string]any{"type": "string", "description": "Reuse it to retry a commit, spend, destroy or send call."},
		"context":           stringProperty,
		"proposal":          stringProperty,
		"wait_ms":           map[string]any{"type": "integer", "minimum": 0, "description": "Default 120000."},
	}, "application", "instance", "tool", "arguments")}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		arguments, wait, err := appCommandWaitArgument(request.Params.Arguments)
		if err != nil {
			return appModelInvalid(ToolApplicationsCall, err)
		}
		ctx = withProgress(ctx, request)
		stop := appCommandHeartbeat(ctx)
		defer stop()
		ctx = withCallWait(ctx, wait)
		call, err := appmodel.DecodeGatewayCall(arguments)
		if err != nil {
			return appModelInvalid(ToolApplicationsCall, err)
		}
		call.Arguments = appJobArguments(call.Tool, call.Arguments, call.Instance)
		var out applications.CallResult
		err = bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.ApplicationsCall(ctx, *call); return err })
		if err != nil {
			if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, err
			}
			return appModelCallFailure(call.Application, call.IdempotencyKey, err)
		}
		if out.Outcome != applications.OutcomeApplied {
			result, err := appModelNativeRefusal(out.Outcome, out.Reason)
			return appModelWithSentence(result, err, appModelCallSentence(call.Application, out.Outcome.String(), out.Reason))
		}
		raw, err := appModelAnswer(call.Tool, out.Answer)
		if err != nil {
			return nil, err
		}
		result, err := appModelResult(raw, false)
		if out.Answer != nil && out.Answer.Result != nil && result != nil && result.IsError {
			return appModelWithSentence(result, err, appModelCallSentence(call.Application, out.Answer.Result.Outcome.String(), out.Answer.Result.Reason))
		}
		return result, err
	})
	handles := &openHandles{}
	s.AddTool(&mcp.Tool{Name: ToolApplicationsOpen, Description: appModelOpenDescription, Annotations: &mcp.ToolAnnotations{OpenWorldHint: boolp(true), IdempotentHint: true, DestructiveHint: boolp(false)}, InputSchema: schema(map[string]any{
		"application": stringProperty, "verb": stringProperty, "payload": map[string]any{"type": "object"}, "context": stringProperty,
		"handle": stringProperty, "wait_ms": map[string]any{"type": "integer"},
	})}, func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		began := time.Now()
		arguments, handle, wait, err := openArguments(request.Params.Arguments)
		if err != nil {
			return appModelInvalid(ToolApplicationsOpen, err)
		}
		if handle != "" {
			value, ok := handles.get(handle)
			if !ok {
				return appModelInvalid(ToolApplicationsOpen, errOpenHandle)
			}
			return openFollow(ctx, backend, bounded, handles, handle, value, began.Add(wait))
		}
		open, err := applications.DecodeActivateRequest(arguments)
		if err != nil {
			return appModelInvalid(ToolApplicationsOpen, err)
		}
		var out applications.ActivationResult
		// The runtime keeps its "Open <app>?" question open while this call
		// waits, and the person gets at least openPersonFloor to answer it.
		person := openPersonWait(wait)
		err = bounded(withBoundedBudget(ctx, person), func(callCtx context.Context) (err error) {
			out, err = backend.ApplicationsOpen(withCallWait(callCtx, person), *open)
			if err != nil && callCtx.Err() != nil && ctx.Err() == nil {
				// The wait for the person ended before they answered.
				out, err = applications.ActivationResult{Outcome: applications.ActivationOutcomeNotReady, Reason: "consent"}, nil
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		if phase, starting := openStartingPhase(out); starting && out.Instance != "" {
			handle, err := handles.add(open.Application, out.Instance, out.Title, phase)
			if err != nil {
				return nil, err
			}
			value, _ := handles.get(handle)
			return openFollow(ctx, backend, bounded, handles, handle, value, began.Add(wait))
		}
		// The gateway's own sentence follows the native answer, for the agent
		// to relay to the person.
		return openAnswer(open.Application, out, "", "")
	})
}

// appModelCallDescription tells an agent the simple call shape (FAC-M32).
const appModelCallDescription = "Call an open instance's command with input_schema from oa_applications_commands_list. " +
	"OA fills in context, revision, mode and idempotency_key; wait_ms bounds the wait."

// appModelOpenDescription tells an agent that it opens apps itself and talks
// to the person in its own chat.
const appModelOpenDescription = "Open a kept app; OA may ask once. Use oa_applications_describe open_verbs; omit verb for one choice and payload for {}. " +
	"If starting, follow the exact next call with its handle; relay the closing sentence directly to the person in this chat."

// appModelOpenSentence is the plain sentence an agent relays about an open:
// what happened and what the agent can do next, with the app's display name
// and no reason codes.
func appModelOpenSentence(application string, out applications.ActivationResult) string {
	app := application
	if out.Title != "" {
		app = out.Title
	}
	switch {
	case out.Outcome == applications.ActivationOutcomeReady && out.Started:
		return "Opened " + app + ". Use instance " + out.Instance + " with oa_applications_commands_list."
	case out.Outcome == applications.ActivationOutcomeReady:
		return app + " is already open. Use instance " + out.Instance + " with oa_applications_commands_list."
	case openAwaitingPerson(out):
		return "The person has not answered OpenAbstractions' question whether to open " + app + " yet. Ask them in this chat to look for it, then call oa_applications_open again; it asks them once more."
	case out.Outcome == applications.ActivationOutcomeNotReady && strings.HasPrefix(out.Reason, "activation:refused: "):
		return app + " is not ready: " + strings.TrimPrefix(out.Reason, "activation:refused: ") + ". Call oa_applications_open again later."
	case out.Outcome == applications.ActivationOutcomeNotReady:
		return app + " is still starting. Call oa_applications_open again in a few seconds."
	case out.Outcome == applications.ActivationOutcomeUnavailable && strings.HasPrefix(out.Reason, "activation:unavailable: "):
		return app + " did not open: " + strings.TrimPrefix(out.Reason, "activation:unavailable: ") + ". Try open again."
	case out.Outcome == applications.ActivationOutcomeForbidden && out.Reason == "consent":
		return "The person chose Not now for opening " + app + ". Ask them in this chat whether to open it, and call open again only after they say yes."
	case out.Outcome == applications.ActivationOutcomeForbidden && out.Reason == "blocked":
		return "The person has blocked you from opening " + app + "."
	case out.Outcome == applications.ActivationOutcomeForbidden:
		return "You have no permission to open " + app + ". Tell the person in this chat."
	case out.Outcome == applications.ActivationOutcomeInvalid && out.Reason == "verb":
		return "The verb is not declared by " + app + "." + openVerbsHint
	case out.Outcome == applications.ActivationOutcomeInvalid && out.Reason == "missing_verb":
		return "Pass verb as a verb declared by that application." + openVerbsHint
	case out.Outcome == applications.ActivationOutcomeInvalid && out.Reason == "payload":
		return "The payload does not match the selected verb's schema for " + app + "." + openVerbsHint
	case out.Outcome == applications.ActivationOutcomeInvalid:
		return "This open request does not match " + app + "'s activation declaration." + openVerbsHint
	}
	return app + " did not open: " + appModelOpenCause(app, out.Outcome) + ". Try open again."
}

// appModelOpenCause is the plain cause of a failed open.
func appModelOpenCause(app string, outcome applications.ActivationOutcome) string {
	switch outcome {
	case applications.ActivationOutcomeIdentityRefused:
		return app + "'s program could not be started"
	case applications.ActivationOutcomeLaunchRefused:
		return app + " can start only on the person's desktop session"
	case applications.ActivationOutcomeDisabled:
		return "OpenAbstractions cannot start " + app
	case applications.ActivationOutcomeUnavailable:
		return "OpenAbstractions could not reach " + app
	}
	return "OpenAbstractions lost track of " + app + "'s start"
}

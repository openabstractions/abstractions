package main

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	facadeclient "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go/client"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

const (
	comfyApplication    = "comfyui"
	readAction          = "comfy-presentation/read"
	previewAction       = "comfy-presentation/preview"
	applicationResource = "app:" + comfyApplication
)

var presentationInterface = facade.ApplicationInterface{Name: "presentation", Protocol: "oa-local", Contract: "comfy.presentation@1"}

type applicationsAPI interface {
	Observe(context.Context, string, int64) (facade.ApplicationPage, error)
	Activate(context.Context, string) (facade.ApplicationActivationResult, error)
}

type rightsAPI interface {
	DecideContext(context.Context, string, string) (rights.Decision, error)
}

type logAPI interface {
	LogContext(context.Context, int64, string, map[string]string) error
}

type operationBinding struct {
	applicationInstance string
	context             string
	revision            string
}

type oaAdapter struct {
	bridge *bridgeClient
	apps   applicationsAPI
	rights rightsAPI
	log    logAPI
}

type oaPreviewInput struct {
	ApplicationInstance string  `json:"application_instance" jsonschema:"exact OA application instance from comfy_read_context"`
	Instance            string  `json:"instance" jsonschema:"exact browser page instance from comfy_read_context"`
	Context             string  `json:"context" jsonschema:"exact workflow context from comfy_read_context"`
	Revision            string  `json:"revision" jsonschema:"exact workflow revision from comfy_read_context"`
	NodeID              int64   `json:"node_id" jsonschema:"exact ComfyUI node id"`
	Widget              string  `json:"widget" jsonschema:"exact numeric widget name"`
	Value               float64 `json:"value" jsonschema:"proposed numeric value"`
}

type oaReadInput struct {
	ApplicationInstance string `json:"application_instance" jsonschema:"exact OA application instance from a fresh comfy_read_context after Apply"`
	Instance            string `json:"instance" jsonschema:"exact browser page instance from a fresh comfy_read_context after Apply"`
	Context             string `json:"context" jsonschema:"exact workflow context from a fresh comfy_read_context after Apply"`
	Revision            string `json:"revision" jsonschema:"exact current workflow revision from a fresh comfy_read_context after Apply"`
	Operation           string `json:"operation" jsonschema:"operation id shown after a person applies the proposal"`
}

func newOAAdapter(bridge *bridgeClient, apps applicationsAPI, decisions rightsAPI, log logAPI) *oaAdapter {
	return &oaAdapter{bridge: bridge, apps: apps, rights: decisions, log: log}
}

func (a *oaAdapter) authorize(ctx context.Context, action string) (map[string]any, bool) {
	decision, err := a.rights.DecideContext(ctx, action, applicationResource)
	if err != nil {
		return refusal("unavailable", "rights_unavailable"), false
	}
	if decision.Outcome != rightswire.DecisionOutcomePermitted {
		_ = a.audit(ctx, action, decision.Outcome.String(), operationBinding{})
		outcome := "forbidden"
		switch decision.Outcome {
		case rightswire.DecisionOutcomeInvalid:
			outcome = "invalid"
		case rightswire.DecisionOutcomeUnavailable:
			outcome = "unavailable"
		}
		return refusal(outcome, decision.Outcome.String()), false
	}
	return nil, true
}

func refusal(outcome, reason string) map[string]any {
	return map[string]any{"outcome": outcome, "reason": reason}
}

func (a *oaAdapter) audit(ctx context.Context, action, outcome string, binding operationBinding) error {
	attrs := map[string]string{"oa.action": action, "oa.resource": applicationResource, "oa.outcome": outcome}
	if binding.applicationInstance != "" {
		attrs["oa.application_instance"] = binding.applicationInstance
		attrs["oa.context"] = binding.context
		attrs["oa.revision"] = binding.revision
	}
	return a.log.LogContext(ctx, 6, "Comfy presentation mediation", attrs)
}

func (a *oaAdapter) currentBridgeContext(ctx context.Context) (map[string]any, operationBinding, error) {
	out, err := a.bridge.call(ctx, "context", struct{}{})
	if err != nil {
		return nil, operationBinding{}, err
	}
	binding := operationBinding{
		applicationInstance: textField(out, "application_instance"),
		context:             textField(out, "context"),
		revision:            textField(out, "revision"),
	}
	if binding.applicationInstance == "" || binding.context == "" || binding.revision == "" || textField(out, "instance") == "" {
		return nil, operationBinding{}, errors.New("comfy OA bridge: incomplete trusted context binding")
	}
	return out, binding, nil
}

func textField(v map[string]any, name string) string {
	s, _ := v[name].(string)
	if len(s) > 256 {
		return ""
	}
	return s
}

func publicFields(v map[string]any, names ...string) map[string]any {
	out := make(map[string]any, len(names))
	for _, name := range names {
		if value, ok := v[name]; ok {
			out[name] = value
		}
	}
	return out
}

func (a *oaAdapter) verifyPresence(ctx context.Context, want operationBinding) error {
	page, err := a.apps.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	if page.Outcome != facade.ApplicationOutcomePage {
		return fmt.Errorf("application directory: %s", page.Outcome)
	}
	for _, app := range page.Applications {
		if app.Descriptor.Name != comfyApplication {
			continue
		}
		for _, instance := range app.Instances {
			if instance.Instance != want.applicationInstance || !hasPresentationInterface(instance.Interfaces) {
				continue
			}
			for _, c := range instance.Contexts {
				if c.Name == want.context && c.Revision == want.revision {
					return nil
				}
			}
		}
	}
	return errors.New("application directory: instance context is absent or stale")
}

func hasPresentationInterface(list []facade.ApplicationInterface) bool {
	for _, candidate := range list {
		if candidate == presentationInterface {
			return true
		}
	}
	return false
}

func (a *oaAdapter) readContext(ctx context.Context) map[string]any {
	if denied, ok := a.authorize(ctx, readAction); !ok {
		return denied
	}
	out, binding, err := a.currentBridgeContext(ctx)
	if err != nil || a.verifyPresence(ctx, binding) != nil {
		return refusal("stale", "application_context_unverified")
	}
	if err := a.audit(ctx, readAction, "permitted", binding); err != nil {
		return refusal("unavailable", "audit_unavailable")
	}
	result := publicFields(out, "application_instance", "instance", "context", "revision", "title")
	result["outcome"] = "current"
	return result
}

func (a *oaAdapter) preview(ctx context.Context, input oaPreviewInput) map[string]any {
	if input.ApplicationInstance == "" || input.Instance == "" || input.Context == "" || input.Revision == "" || input.NodeID < 0 || input.Widget == "" {
		return refusal("invalid", "exact_binding_required")
	}
	if denied, ok := a.authorize(ctx, previewAction); !ok {
		return denied
	}
	current, binding, err := a.currentBridgeContext(ctx)
	if err != nil || binding.applicationInstance != input.ApplicationInstance || textField(current, "instance") != input.Instance || binding.context != input.Context || binding.revision != input.Revision || a.verifyPresence(ctx, binding) != nil {
		return refusal("stale", "application_context_changed")
	}
	if err := a.audit(ctx, previewAction, "permitted", binding); err != nil {
		return refusal("unavailable", "audit_unavailable")
	}
	out, err := a.bridge.call(ctx, "preview", input)
	if err != nil {
		return refusal("unavailable", "bridge_unavailable")
	}
	if textField(out, "outcome") == "proposed" && (textField(out, "instance") != input.Instance || textField(out, "context") != input.Context || textField(out, "revision") != input.Revision) {
		return refusal("stale", "proposal_binding_changed")
	}
	result := publicFields(out, "outcome", "instance", "context", "revision", "node", "title", "widget", "before", "after")
	result["application_instance"] = binding.applicationInstance
	return result
}

func (a *oaAdapter) readOutcome(ctx context.Context, input oaReadInput) map[string]any {
	if input.ApplicationInstance == "" || input.Instance == "" || input.Context == "" || input.Revision == "" || input.Operation == "" {
		return refusal("invalid", "exact_binding_and_operation_required")
	}
	if denied, ok := a.authorize(ctx, readAction); !ok {
		return denied
	}
	current, binding, err := a.currentBridgeContext(ctx)
	if err != nil || binding.applicationInstance != input.ApplicationInstance || textField(current, "instance") != input.Instance || binding.context != input.Context || binding.revision != input.Revision || a.verifyPresence(ctx, binding) != nil {
		return refusal("stale", "application_context_changed")
	}
	if err := a.audit(ctx, readAction, "permitted", binding); err != nil {
		return refusal("unavailable", "audit_unavailable")
	}
	out, err := a.bridge.call(ctx, "read", input)
	if err != nil {
		return refusal("unavailable", "bridge_unavailable")
	}
	if outcome := textField(out, "outcome"); outcome != "unknown" && (textField(out, "instance") != input.Instance || textField(out, "context") != input.Context || textField(out, "revision") != input.Revision) {
		return refusal("stale", "result_binding_changed")
	}
	result := publicFields(out, "outcome", "operation", "instance", "context", "revision", "node", "title", "widget", "before", "after", "actual", "evidence")
	result["application_instance"] = binding.applicationInstance
	return result
}

func (a *oaAdapter) activate(ctx context.Context) map[string]any {
	const action = "abstraction.facade/application.activate"
	if err := a.audit(ctx, action, "requested", operationBinding{}); err != nil {
		return refusal("unavailable", "audit_unavailable")
	}
	result, err := a.apps.Activate(ctx, comfyApplication)
	if err != nil {
		return refusal("unavailable", "activation_unavailable")
	}
	_ = a.audit(ctx, action, result.Outcome.String(), operationBinding{applicationInstance: result.Instance})
	return map[string]any{"outcome": result.Outcome.String(), "application_instance": result.Instance, "started": result.Started, "reason": result.Reason}
}

func newOAServer(adapter *oaAdapter) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "oa-comfy-presentation-adapter", Version: "0.1.0-dev"}, nil)
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_activate", Description: "Explicitly request OA activation of the operator-registered ComfyUI application. This does not preview or apply an edit."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			return nil, adapter.activate(ctx), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_read_context", Description: "Read the OA-bound ComfyUI browser and workflow context required by preview.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			return nil, adapter.readContext(ctx), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_preview_parameter", Description: "Preview one exact parameter through an OA-authorized application instance. A person separately authorizes apply in ComfyUI.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input oaPreviewInput) (*mcp.CallToolResult, map[string]any, error) {
			return nil, adapter.preview(ctx, input), nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "comfy_read_outcome", Description: "After a person applies a proposal, call comfy_read_context again and use that fresh exact OA/browser/workflow binding with the operation shown in the panel.", Annotations: readOnly},
		func(ctx context.Context, _ *mcp.CallToolRequest, input oaReadInput) (*mcp.CallToolResult, map[string]any, error) {
			return nil, adapter.readOutcome(ctx, input), nil
		})
	return server
}

func oaMachine(endpoint, program string) (*facadeclient.Machine, error) {
	if endpoint == "" && program == "" {
		return facadeclient.Discover(), nil
	}
	if endpoint == "" || program == "" {
		return nil, errors.New("--oa-endpoint and --oa-runtime-program must be given together")
	}
	if !filepath.IsAbs(program) {
		return nil, errors.New("--oa-runtime-program must be an absolute path")
	}
	account, err := user.Current()
	if err != nil {
		return nil, err
	}
	principal := identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}
	if runtime.GOOS != "windows" {
		uid, err := strconv.Atoi(account.Uid)
		if err != nil {
			return nil, errors.New("current POSIX account has no numeric uid")
		}
		principal = identity.User{Kind: "posix", UID: uid, GID: -1}
	}
	return facadeclient.NewVerified(endpoint, listen.ServerExpectation{Principal: principal, Program: filepath.Clean(program)}), nil
}

func resolveOAServices(ctx context.Context, endpoint, program string) (applicationsAPI, rightsAPI, logAPI, error) {
	machine, err := oaMachine(endpoint, program)
	if err != nil {
		return nil, nil, nil, err
	}
	apps, err := machine.ResolveApplications(ctx, facadeclient.Requirements{Scope: facadeclient.ScopeLocal})
	if err != nil {
		return nil, nil, nil, err
	}
	decisions, err := machine.ResolveRights(ctx, facadeclient.Requirements{Scope: facadeclient.ScopeLocal})
	if err != nil {
		return nil, nil, nil, err
	}
	log, err := machine.ResolveLog(ctx, facadeclient.Requirements{Scope: facadeclient.ScopeLocal})
	if err != nil {
		return nil, nil, nil, err
	}
	return apps, decisions, log, nil
}

var _ logAPI = (*logging.Client)(nil)

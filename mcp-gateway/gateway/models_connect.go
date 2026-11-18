package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

const ToolModelsConnect = "oa_models_connect"

type ModelsConnectInput struct {
	Provider string `json:"provider"`
}
type ModelsConnectOutput struct {
	Outcome     string   `json:"outcome"`
	Reason      string   `json:"reason,omitempty"`
	QuestionIDs []string `json:"question_ids,omitempty"`
}
type ModelsConnectBackend interface {
	ModelsConnect(context.Context, ModelsConnectInput) (ModelsConnectOutput, error)
}

func addModelsConnectTool(server *mcp.Server, backend ModelsConnectBackend, bounded func(context.Context, func(context.Context) error) error) {
	names := []string{}
	bindings := map[string]hostedcatalogue.Binding{}
	rows, err := hostedcatalogue.All()
	if err != nil {
		panic(err)
	}
	for _, row := range rows {
		if binding, err := hostedcatalogue.Select(row.ID); err == nil {
			names = append(names, row.ID)
			bindings[row.ID] = binding
		}
	}
	mcp.AddTool(server, &mcp.Tool{Name: ToolModelsConnect,
		Description: "Ask the person to connect a hosted model provider. OA's popup takes the key; never ask for, pass or return a key. If unavailable, the person finishes setup from its Questions row in the Panel.",
		Meta:        map[string]any{"org.openabstractions/provider_bindings": bindings, "org.openabstractions/required_router_guarantees": []string{router.GuaranteeProviderSetup}},
		Annotations: &mcp.ToolAnnotations{IdempotentHint: true, OpenWorldHint: boolp(true)},
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"provider": map[string]any{"type": "string", "enum": names}}, "required": []string{"provider"}, "additionalProperties": false}},
		func(ctx context.Context, _ *mcp.CallToolRequest, in ModelsConnectInput) (*mcp.CallToolResult, ModelsConnectOutput, error) {
			var out ModelsConnectOutput
			err := bounded(ctx, func(ctx context.Context) (err error) { out, err = backend.ModelsConnect(ctx, in); return err })
			return nil, out, err
		})
}

type modelsConnectPermission interface {
	permissionQuestionObserver
	RequestContext(context.Context, askswire.PermissionIntent) (askswire.PermissionObservation, error)
}

func (o *OA) ModelsConnect(ctx context.Context, in ModelsConnectInput) (ModelsConnectOutput, error) {
	client, err := o.machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal, Guarantees: []string{router.GuaranteeProviderSetup}})
	if err != nil {
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "provider_setup_unsupported"}, nil
	}
	return connectModels(ctx, in, func(ctx context.Context) (modelsConnectPermission, error) {
		return o.machine.ResolveAsksPermission(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	}, func(ctx context.Context) (routerwire.HostsSnapshot, error) {
		return client.HostsContext(ctx, true)
	})
}

func connectModels(ctx context.Context, in ModelsConnectInput, permission func(context.Context) (modelsConnectPermission, error), hosts func(context.Context) (routerwire.HostsSnapshot, error)) (ModelsConnectOutput, error) {
	binding, err := hostedcatalogue.Select(in.Provider)
	if err != nil {
		return ModelsConnectOutput{Outcome: "invalid", Reason: "provider"}, nil
	}
	client, err := permission(ctx)
	if err != nil {
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "permission_unavailable"}, nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "request_unavailable"}, nil
	}
	key := "models-connect-" + hex.EncodeToString(nonce[:])
	selection := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: credentials.ActionApply, ResourceProfile: rights.LegacyExactResourceProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindOne, Target: []byte(credentials.ResourceFor(binding.Credential))}}
	out, err := client.RequestContext(ctx, askswire.PermissionIntent{RequestKey: key, Selection: &selection})
	if err != nil {
		return ModelsConnectOutput{Outcome: "unknown", Reason: "permission_request_unknown"}, nil
	}
	ids := permissionObservationQuestionIDs(out)
	if out.Outcome == askswire.PermissionOutcomePending && len(ids) != 0 {
		out, err = waitForPermissionQuestion(ctx, client, ids[0])
	}
	if err != nil {
		if len(ids) == 0 {
			return ModelsConnectOutput{Outcome: "unavailable", Reason: "permission_unconfirmed"}, nil
		}
		return ModelsConnectOutput{Outcome: "pending", Reason: "permission_unconfirmed", QuestionIDs: ids}, nil
	}
	if out.Outcome == askswire.PermissionOutcomeAnswered && out.Answer != nil && !out.Answer.Pending && out.Answer.Option == "allow" {
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "provider_not_set_up"}, nil
	}
	switch permissionObservationVerdict(out) {
	case verdictRefused, verdictBlocked:
		return ModelsConnectOutput{Outcome: "declined"}, nil
	case verdictAllowed:
	default:
		if len(ids) == 0 {
			return ModelsConnectOutput{Outcome: "unavailable", Reason: "permission_unconfirmed"}, nil
		}
		return ModelsConnectOutput{Outcome: "pending", QuestionIDs: ids}, nil
	}
	if ctx.Err() != nil {
		if len(ids) != 0 {
			return ModelsConnectOutput{Outcome: "pending", Reason: "permission_unconfirmed", QuestionIDs: ids}, nil
		}
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "permission_unconfirmed"}, nil
	}
	page, err := hosts(ctx)
	if err != nil {
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "provider_not_set_up"}, nil
	}
	matches := 0
	for _, host := range page.Hosts {
		if host.Host == binding.Name && host.Hosted && host.Base == binding.Base && string(host.Wire) == binding.Kind && host.Credential == binding.Credential && host.Servable {
			matches++
		}
	}
	if matches != 1 {
		return ModelsConnectOutput{Outcome: "unavailable", Reason: "provider_not_set_up"}, nil
	}
	return ModelsConnectOutput{Outcome: "connected"}, nil
}

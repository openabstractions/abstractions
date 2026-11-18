package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Keep the native connection's all-tool admission wrapper around the same OA
// backend that owns the authenticated transport. MCP labels supply no identity.
func (b *messagesBackend) ApplicationsEnrollmentRead(ctx context.Context, application string) (applications.AgentEnrollmentResult, error) {
	native, ok := b.Backend.(interface {
		ApplicationsEnrollmentRead(context.Context, string) (applications.AgentEnrollmentResult, error)
	})
	if !ok {
		return applications.AgentEnrollmentResult{}, errors.New("applications: native enrollment read unavailable")
	}
	return native.ApplicationsEnrollmentRead(ctx, application)
}

func (b *messagesBackend) ApplicationsDescribe(ctx context.Context, query appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	native, ok := b.Backend.(gateway.AppModelBackend)
	if !ok {
		return applications.DescribeResult{}, errors.New("applications: native mediation unavailable")
	}
	return native.ApplicationsDescribe(ctx, query)
}

func (b *messagesBackend) ApplicationsCommands(ctx context.Context, query appmodel.CommandsQuery) (applications.CommandsResult, error) {
	native, ok := b.Backend.(gateway.AppModelBackend)
	if !ok {
		return applications.CommandsResult{}, errors.New("applications: native mediation unavailable")
	}
	return native.ApplicationsCommands(ctx, query)
}

func (b *messagesBackend) ApplicationsCall(ctx context.Context, call appmodel.GatewayCall) (applications.CallResult, error) {
	native, ok := b.Backend.(gateway.AppModelBackend)
	if !ok {
		return applications.CallResult{}, errors.New("applications: native mediation unavailable")
	}
	return native.ApplicationsCall(ctx, call)
}

func (b *messagesBackend) ApplicationsOpen(ctx context.Context, request applications.OpenRequest) (applications.OpenResult, error) {
	native, ok := b.Backend.(gateway.AppModelBackend)
	if !ok {
		return applications.OpenResult{}, errors.New("applications: native mediation unavailable")
	}
	return native.ApplicationsOpen(ctx, request)
}

func (b *messagesBackend) ApplicationsDescribeOpen(ctx context.Context, query appmodel.OpenHandleQuery) (applications.DescribeResult, error) {
	native, ok := b.Backend.(gateway.AppModelBackend)
	if !ok {
		return applications.DescribeResult{}, errors.New("applications: native mediation unavailable")
	}
	return native.ApplicationsDescribeOpen(ctx, query)
}

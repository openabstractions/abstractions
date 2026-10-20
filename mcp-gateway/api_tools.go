package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// The existing all-tool native admission remains around the underlying caller.
func (b *messagesBackend) APIRead(ctx context.Context, in gateway.APIReadInput) (gateway.APIReadResult, error) {
	native, ok := b.Backend.(gateway.APIReadBackend)
	if !ok {
		return gateway.APIReadResult{}, errors.New("api: native public API unavailable")
	}
	return native.APIRead(ctx, in)
}

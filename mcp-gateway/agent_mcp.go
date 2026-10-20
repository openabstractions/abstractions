package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// Basenames select routing labels only. Native registration/enrollment and
// opened Inbox admission are required before a Messages backend is constructed.
func dedicatedAgentApplication(program string) string {
	return nativecli.AgentApplicationName(filepath.Base(program))
}

func freshAgentContext(random io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", err
	}
	return "native-" + hex.EncodeToString(value[:]), nil
}

func newAgentContext() (string, error) { return freshAgentContext(rand.Reader) }

func agentContext(explicit string) (string, error) {
	if explicit == "" {
		return newAgentContext()
	}
	if !agentRoutingContext.MatchString(explicit) {
		return "", errors.New("invalid inbox context")
	}
	return explicit, nil
}

// One connection owns one native context for the whole life of its MCP stdio
// connection (owner, 2026-11-01). The server answers from the first byte;
// tools answer a typed unavailable until presence admits the context, and
// again after any loss until presence re-admits it. Only the host closing
// stdio, or the process being asked to stop, ends the server; presence then
// withdraws the instance. The opaque context is not a vendor session ID and
// supplies no reconnect or idle-wake guarantee.
func serveAgentConnection(ctx context.Context, presence func(context.Context), backend gateway.Backend,
	serve func(context.Context, gateway.Backend) error) error {
	if backend == nil {
		err := errors.New("native Messages backend unavailable")
		observeAgentDiagnostic(ctx, "connection_end", err)
		return err
	}
	presenceCtx, cancelPresence := context.WithCancel(ctx)
	defer cancelPresence()
	presenceDone := make(chan struct{})
	go func() {
		defer close(presenceDone)
		presence(presenceCtx)
	}()
	err := serve(ctx, backend)
	if ctx.Err() != nil {
		observeAgentDiagnostic(ctx, "parent_end", ctx.Err())
	} else {
		// A normally completed SDK Run observes stdio EOF; it supplies no
		// evidence about the host's reason for closing its input.
		observed := err
		if observed == nil {
			observed = io.EOF
		}
		observeAgentDiagnostic(ctx, "stdio_end", observed)
	}
	cancelPresence()
	<-presenceDone
	return errors.Join(err, ctx.Err())
}

// Drop only the deliberate local cancellation leaf. Preserve failed withdrawal
// and all other errors joined with it, rather than hiding unknown cleanup.
func agentPresenceCompletion(err error, intentional bool) error {
	if !intentional || err == nil {
		return err
	}
	if err == context.Canceled {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error
		for _, part := range joined.Unwrap() {
			kept = append(kept, agentPresenceCompletion(part, true))
		}
		return errors.Join(kept...)
	}
	return err
}

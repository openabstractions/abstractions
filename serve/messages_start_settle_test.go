package main

import (
	"context"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// Architect reviews 41 and 43: a claimed start is a runtime-owned task under
// its own bound. The pass that claimed it returns at once, and ending that
// pass does not end the start: the start still reaches its receiver and
// records its outcome. Before review 41 the claiming pass's deadline ended
// the start with "authorization"; before review 43 the pass was held until
// the receiver's readiness.
func TestMessageStartOutlivesClaimingPass(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixture(t)
	allowMessageStartFixture(t, h, key, r)
	pass, endPass := context.WithCancel(context.Background())
	defer endPass()
	slowed := make(chan struct{})
	profile := h.directory.messageStartProfile
	h.directory.messageStartProfile = func(ctx context.Context, d wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		target, err := profile(ctx, d)
		if err != nil {
			return target, err
		}
		check := target.Validate
		target.Validate = func(ctx context.Context) error {
			// Once the start is claimed, end the claiming pass and recheck
			// slowly, as under load.
			if current, err := h.messages.readRecord(context.Background(), key); err != nil || current == nil || current.Start == nil || current.Start.State != "unknown" {
				return check(ctx)
			}
			select {
			case <-slowed:
			default:
				endPass()
				close(slowed)
			}
			select {
			case <-time.After(150 * time.Millisecond):
			case <-ctx.Done():
				return ctx.Err()
			}
			return check(ctx)
		}
		return target, nil
	}
	began := time.Now()
	if err := h.progressMessageStart(pass, key, r); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took > 2*time.Second {
		t.Fatal("the claiming pass was held by the start", took)
	}
	h.messageStarts.wait()
	select {
	case <-slowed:
	default:
		t.Fatal("the slowed check never ran")
	}
	if pass.Err() == nil {
		t.Fatal("the claiming pass was not ended")
	}
	if calls.Load() != 1 {
		t.Fatal("a start whose claiming pass ended never reached its receiver", calls.Load())
	}
	// The fixture opens no inbox, so the readiness outcome is uncertain;
	// TestMessageStartGatewayAttachForksExistingSession reaches ready.
	stored, err := h.messages.readRecord(context.Background(), key)
	if err != nil || stored.Start == nil || stored.Start.State != "unknown" {
		t.Fatal("claimed start outcome", stored, err)
	}
}

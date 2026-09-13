package main

import (
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	"sync/atomic"
	"testing"
	"time"
)

type configObserveRequest struct {
	ctx       context.Context
	overrides wire.RunOverrides
	cursor    string
	wait      int64
}
type configObserveReply struct {
	value wire.ConfigObservation
	err   error
}
type scriptedConfigObserver struct {
	requests chan configObserveRequest
	replies  chan configObserveReply
}

func (s *scriptedConfigObserver) ObserveContext(ctx context.Context, o wire.RunOverrides, c string, w int64) (wire.ConfigObservation, error) {
	s.requests <- configObserveRequest{ctx, o, c, w}
	select {
	case r := <-s.replies:
		return r.value, r.err
	case <-ctx.Done():
		return wire.ConfigObservation{}, ctx.Err()
	}
}
func nextConfigRequest(t *testing.T, s *scriptedConfigObserver) configObserveRequest {
	t.Helper()
	select {
	case r := <-s.requests:
		return r
	case <-time.After(time.Second):
		t.Fatal("observation request missing")
		return configObserveRequest{}
	}
}
func TestPanelObserverReusesBindingResetsGapAndRetainsFailure(t *testing.T) {
	provider := &scriptedConfigObserver{requests: make(chan configObserveRequest, 8), replies: make(chan configObserveReply, 8)}
	var resolves atomic.Int32
	overrides := wire.RunOverrides{Store: "caller-store"}
	view := observeConfigurationWith(context.Background(), time.Millisecond, func(ctx context.Context) (configurationObserver, error) { resolves.Add(1); return provider, nil }, overrides)
	defer view.Close()
	first := nextConfigRequest(t, provider)
	if first.cursor != "" || first.overrides.Store != "caller-store" || first.wait != 30000 {
		t.Fatal(first)
	}
	if deadline, ok := first.ctx.Deadline(); !ok || time.Until(deadline) > 35*time.Second {
		t.Fatal("missing call budget")
	}
	provider.replies <- configObserveReply{value: wire.ConfigObservation{Outcome: "snapshot", Cursor: "first", Snapshot: &wire.Snapshot{Store: "first"}}}
	awaitConfiguration(t, view, func() bool { return view.Current().Store == "first" && view.Error() == nil })
	second := nextConfigRequest(t, provider)
	if second.cursor != "first" || resolves.Load() != 1 {
		t.Fatal("binding re-resolved", second, resolves.Load())
	}
	provider.replies <- configObserveReply{value: wire.ConfigObservation{Outcome: "gap", Cursor: "first"}}
	restart := nextConfigRequest(t, provider)
	if restart.cursor != "" || resolves.Load() != 1 {
		t.Fatal("gap not explicitly reset", restart)
	}
	provider.replies <- configObserveReply{value: wire.ConfigObservation{Outcome: "snapshot", Cursor: "new", Snapshot: &wire.Snapshot{Store: "latest"}}}
	awaitConfiguration(t, view, func() bool { return view.Current().Store == "latest" })
	nextConfigRequest(t, provider)
	provider.replies <- configObserveReply{err: errors.New("transport endpoint detail")}
	awaitConfiguration(t, view, func() bool { return view.Error() != nil })
	if view.Current().Store != "latest" || view.How() != "configuration unavailable; updates will retry" {
		t.Fatal(view.How(), view.Current())
	}
	recovery := nextConfigRequest(t, provider)
	if recovery.cursor != "new" || resolves.Load() != 2 {
		t.Fatal("failure recovery lost continuation", recovery, resolves.Load())
	}
	closed := make(chan struct{})
	go func() { view.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel long wait")
	}
}

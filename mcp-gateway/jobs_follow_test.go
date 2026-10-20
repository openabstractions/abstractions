package main

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type jobsFollowOwnerFixture struct {
	gateway.Backend
	id    wire.RequestIdentity
	after string
	wait  int64
}

func (b *jobsFollowOwnerFixture) JobsFollowWork(ctx context.Context, id wire.RequestIdentity, after string, wait int64) (wire.WorkObservation, error) {
	if err := ctx.Err(); err != nil {
		return wire.WorkObservation{}, err
	}
	b.id, b.after, b.wait = id, after, wait
	return wire.WorkObservation{Outcome: wire.WorkObservationOutcomeUnchanged, Next: after, Mechanism: wire.WorkObservationMechanismPolled}, nil
}

func TestMessagesBackendKeepsJobsFollowProjection(t *testing.T) {
	owner := &jobsFollowOwnerFixture{}
	b := &messagesBackend{Backend: owner}
	id := wire.RequestIdentity{Key: "key", HistoryEpoch: "epoch", Attempt: 1}
	page, err := b.JobsFollowWork(t.Context(), id, "opaque:é", 30000)
	if err != nil || owner.id != id || owner.after != "opaque:é" || owner.wait != 30000 || page.Next != "opaque:é" {
		t.Fatal(page, err, owner)
	}
	st, ct := mcp.NewInMemoryTransports()
	server, err := gateway.NewServer(b).Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "forged-operator", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == gateway.ToolJobsFollow {
			found = true
		}
	}
	if !found {
		t.Fatal("native helper dropped FollowWork")
	}
	unsupported, err := (&messagesBackend{}).JobsFollowWork(t.Context(), id, "", 0)
	if err != nil || unsupported.Outcome != wire.WorkObservationOutcomeUnsupported || unsupported.Snapshot != nil || unsupported.Next != "" {
		t.Fatal(unsupported, err)
	}
}

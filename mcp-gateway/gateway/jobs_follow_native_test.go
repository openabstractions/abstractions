package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	job "github.com/openabstractions/abstraction-job/go"
	wire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

func TestJobsFollowActualNativeBindingWaitAndRefusal(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof unavailable")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	expected := listen.ServerExpectation{Program: exe, Principal: identity.User{Kind: "windows", SID: current.Uid}}
	if runtime.GOOS != "windows" {
		expected.Principal = identity.User{Kind: "posix", UID: os.Geteuid(), GID: -1}
	}
	var enabled, denied atomic.Bool
	var calls atomic.Int32
	ready := make(chan struct{}, 32)
	programs := make(chan string, 32)
	ordinaryBudget := make(chan time.Duration, 2)
	cfg := fixture.NewConfig("follow-work")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.ExecutorEnabled = &enabled
	cfg.JobMethodPolicy = func(policyCtx context.Context, peer *identity.Peer, service, method string) error {
		if method == "GetHistoryWindow" {
			deadline, ok := policyCtx.Deadline()
			if !ok {
				return errors.New("ordinary method lost its deadline")
			}
			ordinaryBudget <- time.Until(deadline)
		}
		if service == facade.JobOperationsContract && method == "FollowWork" {
			program, err := peer.Path.AtLeast(listen.Program.Path)
			if err != nil {
				return err
			}
			programs <- fixture.Program(program)
			calls.Add(1)
			ready <- struct{}{}
			if denied.Load() {
				return errors.New("fixture permission withdrawn")
			}
		}
		return nil
	}
	server := fixture.Start(t, cfg)
	defer server.Stop(t)
	machine := facade.NewVerified(cfg.RuntimeEndpoint, expected)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	jobs, err := machine.ResolveJobs(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	window, err := jobs.GetHistoryWindow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if budget := <-ordinaryBudget; budget <= 0 || budget > 5*time.Second {
		t.Fatal("native ordinary budget changed", budget)
	}
	id := wire.RequestIdentity{Key: "mcp-follow", HistoryEpoch: window.HistoryEpoch}
	rawIdentity, err := wire.ToJSON(&id)
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := jobs.Submit(ctx, wire.Submission{Identity: id, Kind: "inference", Spec: []byte(`{}`), RequiredGuarantees: []string{}})
	if err != nil || accepted.Receipt == nil {
		t.Fatal(accepted, err)
	}
	backend, err := NewOA(machine, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, session := connected(t, backend, "forged-operator", "2026-07-28")
	invoke := func(callCtx context.Context, after string, waitMS int64) (*mcp.CallToolResult, wire.WorkObservation) {
		t.Helper()
		result, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: ToolJobsFollow, Arguments: map[string]any{"identity": json.RawMessage(rawIdentity), "after": after, "wait_ms": waitMS}})
		if err != nil {
			t.Fatal(err)
		}
		var decoded wire.WorkObservation
		if err := wire.FromJSON([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded); err != nil {
			t.Fatal(err, result.Content[0].(*mcp.TextContent).Text)
		}
		return result, decoded
	}
	result, first := invoke(ctx, "", 0)
	if result.IsError || first.Snapshot == nil || first.Snapshot.Receipt.OperationID != accepted.Receipt.OperationID {
		t.Fatal(result, first)
	}
	result, unchanged := invoke(ctx, first.Next, 5100)
	if result.IsError || unchanged.Outcome != wire.WorkObservationOutcomeUnchanged || unchanged.Next != first.Next || unchanged.Snapshot != nil {
		t.Fatal(result, unchanged)
	}
	denied.Store(true)
	result, refused := invoke(ctx, first.Next, 0)
	if !result.IsError || refused.Outcome != wire.WorkObservationOutcomeForbidden || refused.Next != "" || refused.Snapshot != nil {
		t.Fatal(result, refused)
	}
	denied.Store(false)
drain:
	for {
		select {
		case <-ready:
		default:
			break drain
		}
	}
	base := calls.Load()
	callCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: ToolJobsFollow, Arguments: map[string]any{"identity": json.RawMessage(rawIdentity), "after": first.Next, "wait_ms": 30000}})
		done <- err
	}()
	for calls.Load() < base+2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("native wait not entered")
		}
	}
	// The same MCP session must accept an independent native call while
	// FollowWork is still waiting on the job receiver.
	quick, endQuick := context.WithTimeout(ctx, 2*time.Second)
	other, err := session.CallTool(quick, &mcp.CallToolParams{Name: ToolModels, Arguments: map[string]any{}})
	endQuick()
	if err != nil || other == nil || other.IsError {
		t.Fatalf("models blocked behind the native job wait: %+v, %v", other, err)
	}
	select {
	case err := <-done:
		t.Fatalf("native job wait ended before cancellation: %v", err)
	default:
	}
	stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP native read did not cancel")
	}
programsRead:
	for {
		select {
		case program := <-programs:
			if program != fixture.Program(exe) {
				t.Fatal("MCP metadata selected native Program", program)
			}
		default:
			break programsRead
		}
	}
	store, err := job.NewFileStore(cfg.JobRoot)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Load(accepted.Receipt.OperationID)
	if err != nil || record.Intent != nil || record.State != job.StatePending {
		t.Fatal("read changed accepted work", record, err)
	}
	server.Stop(t)
	cfg.JobMethodPolicy = nil
	withoutOperator := fixture.Start(t, cfg)
	defer withoutOperator.Stop(t)
	// The operator profile is not advertised without a decision. Even a
	// direct generated operator frame on this trusted job endpoint refuses.
	binding, err := machine.ResolveService(ctx, facade.JobOperationsContract, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	operator := wire.NewJobOperatorClient(binding.Transport().WithContext(ctx))
	page, err := operator.ListAccountWork("", 1)
	if err != nil || page.Outcome != wire.InventoryOutcomeForbidden || len(page.Snapshots) != 0 {
		t.Fatal("budget adapter granted operator authority", page, err)
	}
}

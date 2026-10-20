package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
)

// The job tools carry video only (backlog/PICTURES-STORAGE.md ruling 4): the
// profile names video, a picture is refused with its sentence, and another
// word never reaches the backend.
func TestJobSubmitSchemaAdvertisesVideoOnly(t *testing.T) {
	b := &fixtureBackend{principal: "schema", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}
	_, session := connected(t, b, "arbitrary-client", "2025-11-25")
	// The submit tool is callable and unlisted: its schema is the handler's.
	var schema struct {
		Properties map[string]struct {
			Enum        []string
			Description string
		}
	}
	raw, err := json.Marshal(jobSubmitSchema())
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	video, _ := jobwire.ProfileVideo.WireName()
	if profile := schema.Properties["profile"]; profile.Description != video || len(profile.Enum) != 0 {
		t.Fatalf("profile schema %+v", profile)
	}
	if _, ok := schema.Properties["count"]; ok {
		t.Fatal("job submit still takes a picture count")
	}
	args := map[string]any{"request_key": "schema-check", "model": "fixture/model", "prompt": "fixture", "hosting": "local"}
	for _, profile := range []string{"image", "image_batch"} {
		args["profile"] = profile
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolJobSubmit, Arguments: args})
		if err == nil && !result.IsError {
			t.Fatal("unadvertised profile reached backend", profile)
		}
	}
	if b.providerEffects != 0 {
		t.Fatal("invalid profile dispatched backend", b.providerEffects)
	}
	args["profile"], args["duration_ms"] = video, 1000
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolJobSubmit, Arguments: args})
	if err != nil || result.IsError || b.providerEffects != 1 {
		t.Fatal("video refused", result, err, b.providerEffects)
	}
}

type fixtureLedger struct {
	mu    sync.Mutex
	owner map[string]string
	state map[string]string
}

type fixtureBackend struct {
	principal          string
	ledger             *fixtureLedger
	providerEffects    int
	applicationWait    bool
	applicationStarted chan struct{}
}

func (b *fixtureBackend) Applications(ctx context.Context, _ ApplicationsInput) (ApplicationsOutput, error) {
	if b.applicationWait {
		if b.applicationStarted != nil {
			close(b.applicationStarted)
		}
		<-ctx.Done()
		return ApplicationsOutput{}, ctx.Err()
	}
	return ApplicationsOutput{Outcome: "page", Applications: []Application{{Name: "visible-" + b.principal, Scope: "local"}}}, nil
}
func (b *fixtureBackend) Models(context.Context, ModelsInput) (ModelsOutput, error) {
	return ModelsOutput{Models: []Model{{Family: "small", Aliases: []string{"small:latest"}, Profiles: []string{"chat"}}}}, nil
}
func (b *fixtureBackend) SetupFit(_ context.Context, in SetupFitInput) (SetupFitOutput, error) {
	if b.principal == "no-inventory-read" {
		return SetupFitOutput{Outcome: "forbidden", Entries: []SetupFitEntry{}}, nil
	}
	return SetupFitOutput{Outcome: "fitted", CatalogVersion: "fixture-1", PoolBytes: 64e9,
		Entries: []SetupFitEntry{{Title: "Fixture " + in.Use, Family: "fixture-" + in.Use, Fit: "fits", DownloadBytes: 1e9, NeedBytes: 2e9, Context: 32768, Pick: true}}}, nil
}
func (b *fixtureBackend) SetupPrepare(_ context.Context, in SetupPrepareInput) (SetupPrepareOutput, error) {
	if b.principal == "no-inventory-read" {
		return SetupPrepareOutput{Outcome: "forbidden", Reason: "no person reachable on this runtime"}, nil
	}
	return SetupPrepareOutput{Outcome: "asked"}, nil
}
func (b *fixtureBackend) Complete(_ context.Context, in CompleteInput) (CompleteOutput, error) {
	if in.Prompt == "deny" {
		return CompleteOutput{Outcome: "not_permitted", Reason: "rights:refused"}, nil
	}
	b.providerEffects++
	return CompleteOutput{Outcome: "completed", Text: b.principal}, nil
}
func (b *fixtureBackend) Submit(context.Context, JobSubmitInput) (JobSubmitOutput, error) {
	b.ledger.mu.Lock()
	defer b.ledger.mu.Unlock()
	handle := "abcdefghijklmnopqrstuvwx" + b.principal
	b.ledger.owner[handle], b.ledger.state[handle] = b.principal, "running"
	b.providerEffects++
	return JobSubmitOutput{Handle: handle, Outcome: "accepted", OperationID: "operation-" + b.principal}, nil
}
func (b *fixtureBackend) Status(_ context.Context, in JobHandleInput) (JobStatusOutput, error) {
	b.ledger.mu.Lock()
	defer b.ledger.mu.Unlock()
	if b.ledger.owner[in.Handle] != b.principal {
		return JobStatusOutput{}, errors.New("unknown handle for integration principal")
	}
	return JobStatusOutput{Outcome: "observed", State: b.ledger.state[in.Handle]}, nil
}
func (b *fixtureBackend) Diagnose(_ context.Context, in JobHandleInput) (JobDiagnosisOutput, error) {
	b.ledger.mu.Lock()
	defer b.ledger.mu.Unlock()
	if b.ledger.owner[in.Handle] != b.principal {
		return JobDiagnosisOutput{}, errors.New("unknown handle for integration principal")
	}
	return JobDiagnosisOutput{Scope: "this handle", Outcome: "observed", State: b.ledger.state[in.Handle], Guidance: "Check provider status."}, nil
}
func (b *fixtureBackend) Cancel(_ context.Context, in JobHandleInput) (JobCancelOutput, error) {
	b.ledger.mu.Lock()
	defer b.ledger.mu.Unlock()
	if b.ledger.owner[in.Handle] != b.principal {
		return JobCancelOutput{}, errors.New("unknown handle for integration principal")
	}
	b.ledger.state[in.Handle] = "cancelled"
	return JobCancelOutput{Outcome: "requested"}, nil
}

func connected(t *testing.T, b Backend, clientName, version string) (*mcp.ServerSession, *mcp.ClientSession) {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: clientName, Version: "forged"}, nil)
	cs, err := client.Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		ss.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close(); ss.Close() })
	return ss, cs
}

func call[Out any](t *testing.T, cs *mcp.ClientSession, name string, args any) (*mcp.CallToolResult, Out) {
	t.Helper()
	r, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	var out Out
	raw, _ := json.Marshal(r.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return r, out
}

func TestProtocolMatrixAndCuratedToolList(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			b := &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}
			_, cs := connected(t, b, "claimed-host", version)
			info := cs.InitializeResult().ServerInfo
			if info.Title != "OpenAbstractions" || info.Name != "openabstractions-mcp" || info.Version != Version {
				t.Fatalf("server display and identity = %+v", info)
			}
			listed, err := cs.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, tool := range listed.Tools {
				names = append(names, tool.Name)
			}
			want := []string{ToolApplications, ToolComplete, ToolJobCancel, ToolJobDiagnose, ToolJobStatus, ToolModels, ToolSetupFit, ToolSetupPrepare, ToolVideoGenerate}
			slices.Sort(names)
			slices.Sort(want)
			if !slices.Equal(names, want) {
				t.Fatalf("tools = %v, want %v", names, want)
			}
			if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "oa_unimplemented", Arguments: map[string]any{}}); err == nil {
				t.Fatal("unimplemented tool was not refused")
			} else if !strings.Contains(strings.ToLower(err.Error()), "unknown tool") {
				t.Fatalf("unimplemented tool refusal is not actionable: %v", err)
			}
		})
	}
}

func TestApplicationDirectoryUsesFixedPrincipalAndHonorsCancellation(t *testing.T) {
	ledger := &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}
	_, a := connected(t, &fixtureBackend{principal: "principal-a", ledger: ledger}, "forged-b", "2026-07-28")
	_, visibleA := call[ApplicationsOutput](t, a, ToolApplications, map[string]any{})
	_, b := connected(t, &fixtureBackend{principal: "principal-b", ledger: ledger}, "forged-a", "2026-07-28")
	_, visibleB := call[ApplicationsOutput](t, b, ToolApplications, map[string]any{})
	if len(visibleA.Applications) != 1 || visibleA.Applications[0].Name != "visible-principal-a" ||
		len(visibleB.Applications) != 1 || visibleB.Applications[0].Name != "visible-principal-b" {
		t.Fatalf("cross-caller application leaked: a=%+v b=%+v", visibleA, visibleB)
	}

	started := make(chan struct{})
	_, blocked := connected(t, &fixtureBackend{principal: "blocked", ledger: ledger, applicationWait: true, applicationStarted: started}, "forged", "2026-07-28")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := blocked.CallTool(ctx, &mcp.CallToolParams{Name: ToolApplications, Arguments: map[string]any{}})
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled directory call = %v, want context canceled", err)
	}
}

func TestUnknownProtocolOfferNegotiatesExplicitSupportedVersion(t *testing.T) {
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	server := NewServer(&fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}})
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "unsupported-version", Version: "fixture"}, nil)
	cs, err := client.Connect(ctx, ct, &mcp.ClientSessionOptions{ProtocolVersion: "1900-01-01"})
	if err != nil {
		t.Fatalf("SDK could not negotiate a supported version: %v", err)
	}
	defer cs.Close()
	got := cs.InitializeResult().ProtocolVersion
	if got != "2026-07-28" && got != "2025-11-25" {
		t.Fatalf("server returned an unadvertised protocol version %q", got)
	}
}

func TestForgedClientMetadataCannotChangePrincipalOrAuthorizeEffect(t *testing.T) {
	ledger := &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}
	b := &fixtureBackend{principal: "scoped-b", ledger: ledger}
	_, cs := connected(t, b, "scoped-a", "2026-07-28")
	_, denied := call[CompleteOutput](t, cs, ToolComplete, map[string]any{"model": "small", "prompt": "deny", "hosting": "local"})
	if denied.Outcome != "not_permitted" || b.providerEffects != 0 {
		t.Fatalf("denial=%+v effects=%d", denied, b.providerEffects)
	}
	_, completed := call[CompleteOutput](t, cs, ToolComplete, map[string]any{"model": "small", "prompt": "ok", "hosting": "local"})
	if completed.Text != "scoped-b" {
		t.Fatalf("claimed client metadata selected principal: %+v", completed)
	}
}

func TestAcceptedJobSurvivesMCPDisconnectAndCancelIsExplicit(t *testing.T) {
	ledger := &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}
	b := &fixtureBackend{principal: "principal-a", ledger: ledger}
	_, first := connected(t, b, "one", "2025-11-25")
	_, submitted := call[JobSubmitOutput](t, first, ToolJobSubmit, map[string]any{"request_key": "disconnect-1", "profile": "video", "model": "video", "prompt": "draw", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000})
	if submitted.Outcome != "accepted" {
		t.Fatalf("submit: %+v", submitted)
	}
	first.Close()
	_, second := connected(t, b, "two", "2026-07-28")
	_, status := call[JobStatusOutput](t, second, ToolJobStatus, map[string]any{"handle": submitted.Handle})
	if status.State != "running" {
		t.Fatalf("status after disconnect: %+v", status)
	}
	_, cancelled := call[JobCancelOutput](t, second, ToolJobCancel, map[string]any{"handle": submitted.Handle})
	if cancelled.Outcome != "requested" {
		t.Fatalf("cancel: %+v", cancelled)
	}
}

func TestSeparatePrincipalsRejectCrossScopeHandle(t *testing.T) {
	ledger := &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}
	a := &fixtureBackend{principal: "principal-a", ledger: ledger}
	b := &fixtureBackend{principal: "principal-b", ledger: ledger}
	_, ac := connected(t, a, "claims-b", "2026-07-28")
	_, submitted := call[JobSubmitOutput](t, ac, ToolJobSubmit, map[string]any{"request_key": "scope-1", "profile": "video", "model": "video", "prompt": "draw", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000})
	_, bc := connected(t, b, "claims-a", "2026-07-28")
	result, _ := call[JobStatusOutput](t, bc, ToolJobStatus, map[string]any{"handle": submitted.Handle})
	if !result.IsError {
		t.Fatal("cross-principal handle was accepted")
	}
	if b.providerEffects != 0 {
		t.Fatalf("cross-principal refusal caused %d provider effects", b.providerEffects)
	}
	diagnosis, _ := call[JobDiagnosisOutput](t, bc, ToolJobDiagnose, map[string]any{"handle": submitted.Handle})
	if !diagnosis.IsError {
		t.Fatal("cross-principal diagnosis was accepted")
	}
	if b.providerEffects != 0 {
		t.Fatalf("cross-principal diagnosis caused %d provider effects", b.providerEffects)
	}
}

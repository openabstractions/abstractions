package gateway_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func jobCount(t *testing.T, root string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "jobs"))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			n++
		}
	}
	return n
}

// fixtureStderr reads only a bounded tail after the SDK has closed its child.
// A file keeps the child's writes independent of diagnostic reads under -race.
func fixtureStderr(t *testing.T, cmd *exec.Cmd, stderr *os.File, started time.Time) string {
	t.Helper()
	info, err := stderr.Stat()
	if err != nil {
		return fmt.Sprintf("elapsed=%s stderr stat: %v", time.Since(started), err)
	}
	const limit = int64(8192)
	offset := max(int64(0), info.Size()-limit)
	tail := make([]byte, info.Size()-offset)
	if _, err := stderr.ReadAt(tail, offset); err != nil {
		return fmt.Sprintf("elapsed=%s stderr read: %v", time.Since(started), err)
	}
	pid := 0
	if cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return fmt.Sprintf("elapsed=%s pid=%d stderr tail (%d/%d bytes):\n%s", time.Since(started), pid, len(tail), info.Size(), tail)
}

func callFixture[Out any](t *testing.T, binary, endpoint, state, claimedClient, tool string, args any) (*mcp.CallToolResult, Out) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stderr, err := os.Create(filepath.Join(t.TempDir(), "fixture-stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	started := time.Now()
	cmd := exec.CommandContext(ctx, binary, "--runtime", endpoint, "--state-dir", state)
	cmd.Stderr = stderr
	client := mcp.NewClient(&mcp.Implementation{Name: claimedClient, Version: "forged"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		cancel()
		var zero Out
		t.Fatalf("connect fixture %s: %v\n%s", filepath.Base(binary), err, fixtureStderr(t, cmd, stderr, started))
		return nil, zero
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		cancel()
		_ = session.Close()
		var zero Out
		t.Fatalf("call %s: %v\n%s", tool, err, fixtureStderr(t, cmd, stderr, started))
		return nil, zero
	}
	var out Out
	raw, _ := json.Marshal(result.StructuredContent)
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode %s: %v (%s)", tool, err, raw)
	}
	return result, out
}

func dropSubmitReply(t *testing.T, binary, endpoint, state string, args any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stderr, err := os.Create(filepath.Join(t.TempDir(), "fixture-stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	started := time.Now()
	cmd := exec.CommandContext(ctx, binary, "--runtime", endpoint, "--state-dir", state)
	cmd.Stderr = stderr
	cmd.Env = append(os.Environ(), "OA_MCP_FIXTURE_DROP_AFTER_SUBMIT=1")
	client := mcp.NewClient(&mcp.Implementation{Name: "forged-after-drop", Version: "forged"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2026-07-28"})
	if err != nil {
		cancel()
		t.Fatalf("connect drop-reply fixture %s: %v\n%s", filepath.Base(binary), err, fixtureStderr(t, cmd, stderr, started))
	}
	defer session.Close()
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolJobSubmit, Arguments: args}); err == nil {
		cancel()
		_ = session.Close()
		t.Fatalf("fixture returned the MCP reply that it was instructed to drop\n%s", fixtureStderr(t, cmd, stderr, started))
	}
}

func TestNativePrincipalsAndDurableRecoveryThroughMCP(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof is deliberately unavailable on Darwin")
	}
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skipf("native Program proof unavailable: %v", err)
	}
	dir := t.TempDir()
	a := fixture.BuildGateway(t, dir, "principal-a")
	b := fixture.BuildGateway(t, dir, "principal-b")
	denied := fixture.BuildGateway(t, dir, "denied")
	var gets, posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"fixture/chat-small"}]}`)
			return
		}
		posts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"native-ok\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	var prepares atomic.Int64
	var executorEnabled atomic.Bool
	var recordMu sync.Mutex
	var records []inference.Record
	cfg := fixture.NewConfig("native")
	cfg.JobRoot, cfg.Allowed, cfg.Prepares, cfg.ExecutorEnabled, cfg.Upstream = filepath.Join(dir, "jobs"), []string{a, b}, &prepares, &executorEnabled, upstream.URL
	cfg.Applications = map[string]string{a: "visible-a", b: "visible-b"}
	cfg.Record = func(record inference.Record) {
		recordMu.Lock()
		defer recordMu.Unlock()
		records = append(records, record)
	}
	state := filepath.Join(dir, "gateway-state")
	first := fixture.Start(t, cfg)
	defer first.Stop(t)
	_, applicationsA := callFixture[gateway.ApplicationsOutput](t, a, cfg.RuntimeEndpoint, state, "principal-b", gateway.ToolApplications, map[string]any{})
	_, applicationsB := callFixture[gateway.ApplicationsOutput](t, b, cfg.RuntimeEndpoint, filepath.Join(dir, "state-b"), "principal-a", gateway.ToolApplications, map[string]any{})
	_, deniedApplications := callFixture[gateway.ApplicationsOutput](t, denied, cfg.RuntimeEndpoint, filepath.Join(dir, "denied-applications"), "principal-a", gateway.ToolApplications, map[string]any{})
	if len(applicationsA.Applications) != 1 || applicationsA.Applications[0].Name != "visible-a" ||
		len(applicationsB.Applications) != 1 || applicationsB.Applications[0].Name != "visible-b" ||
		len(deniedApplications.Applications) != 0 {
		first.Stop(t)
		t.Fatalf("native application filtering crossed principals: a=%+v b=%+v denied=%+v", applicationsA, applicationsB, deniedApplications)
	}
	applicationsRaw, _ := json.Marshal([]any{applicationsA, applicationsB})
	if strings.Contains(string(applicationsRaw), "not-exposed") {
		first.Stop(t)
		t.Fatalf("application executable leaked through gateway: %s", applicationsRaw)
	}
	_, models := callFixture[gateway.ModelsOutput](t, a, cfg.RuntimeEndpoint, state, "principal-b", gateway.ToolModels, map[string]any{"fresh": true})
	// Setup entries name what Setup can set up; the inventory is the rest.
	served := slices.DeleteFunc(slices.Clone(models.Models), func(m gateway.Model) bool { return m.SetupNeeded })
	if len(served) != 1 || !strings.Contains(strings.Join(served[0].Aliases, ","), "fixture/chat-small") {
		first.Stop(t)
		t.Fatalf("curated native inventory: %+v", models)
	}
	listed := gets.Load()
	deniedModels, _ := callFixture[gateway.ModelsOutput](t, denied, cfg.RuntimeEndpoint, filepath.Join(dir, "denied-models"), "principal-a", gateway.ToolModels, map[string]any{"fresh": true})
	if !deniedModels.IsError || gets.Load() != listed {
		first.Stop(t)
		t.Fatalf("denied inventory caused provider effect: isError=%v gets=%d->%d", deniedModels.IsError, listed, gets.Load())
	}
	_, completed := callFixture[gateway.CompleteOutput](t, a, cfg.RuntimeEndpoint, state, "principal-b", gateway.ToolComplete, map[string]any{"model": "fixture/chat-small", "prompt": "hello", "max_output": 8, "hosting": "hosted", "credential": "fixture"})
	if completed.Outcome != "completed" || completed.Text != "native-ok" || posts.Load() != 1 {
		first.Stop(t)
		t.Fatalf("native completion: %+v posts=%d", completed, posts.Load())
	}
	deniedResult, deniedCompletion := callFixture[gateway.CompleteOutput](t, denied, cfg.RuntimeEndpoint, filepath.Join(dir, "denied-state"), "principal-a", gateway.ToolComplete, map[string]any{"model": "fixture/chat-small", "prompt": "must refuse", "hosting": "hosted", "credential": "fixture"})
	if (!deniedResult.IsError && deniedCompletion.Outcome != "not_permitted") || posts.Load() != 1 {
		first.Stop(t)
		t.Fatalf("denied inference reached provider: isError=%v posts=%d", deniedResult.IsError, posts.Load())
	}
	visible, _ := json.Marshal([]any{models, completed, deniedCompletion, deniedResult.StructuredContent})
	if strings.Contains(string(visible), fixture.Secret) {
		first.Stop(t)
		t.Fatalf("credential leaked into MCP response: %s", visible)
	}
	recordMu.Lock()
	observedRecords := append([]inference.Record(nil), records...)
	recordMu.Unlock()
	var permittedAttributed, refusedAttributed bool
	for _, record := range observedRecords {
		if fixture.Program(record.Program) == fixture.Program(a) && record.Outcome == "completed" {
			permittedAttributed = true
		}
		if fixture.Program(record.Program) == fixture.Program(denied) && record.Outcome != "completed" {
			refusedAttributed = true
		}
		raw, _ := json.Marshal(record)
		if strings.Contains(string(raw), fixture.Secret) {
			first.Stop(t)
			t.Fatalf("credential leaked into attributed audit record: %s", raw)
		}
	}
	if !permittedAttributed || !refusedAttributed {
		first.Stop(t)
		t.Fatalf("missing native attribution in audit records: permitted=%v refused=%v records=%+v", permittedAttributed, refusedAttributed, observedRecords)
	}
	cancelArgs := map[string]any{"request_key": "native-cancel-1", "profile": "video", "model": "fixture/video", "prompt": "cancel pending", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000}
	_, cancelSubmitted := callFixture[gateway.JobSubmitOutput](t, a, cfg.RuntimeEndpoint, state, "cancel-client", gateway.ToolJobSubmit, cancelArgs)
	if cancelSubmitted.Outcome != "accepted" || cancelSubmitted.Handle == "" {
		first.Stop(t)
		t.Fatalf("pending cancel submission: %+v", cancelSubmitted)
	}
	_, cancelResult := callFixture[gateway.JobCancelOutput](t, a, cfg.RuntimeEndpoint, state, "cancel-client", gateway.ToolJobCancel, map[string]any{"handle": cancelSubmitted.Handle})
	if cancelResult.Outcome != "requested" {
		first.Stop(t)
		t.Fatalf("pending cancel outcome: %+v", cancelResult)
	}
	executorEnabled.Store(true)
	var cancelStatus gateway.JobStatusOutput
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		_, cancelStatus = callFixture[gateway.JobStatusOutput](t, a, cfg.RuntimeEndpoint, state, "cancel-client", gateway.ToolJobStatus, map[string]any{"handle": cancelSubmitted.Handle})
		if cancelStatus.State == "cancelled" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cancelStatus.State != "cancelled" {
		first.Stop(t)
		t.Fatalf("pending job was not cancelled by the fixture worker: %+v", cancelStatus)
	}
	cancelJobs := jobCount(t, cfg.JobRoot)
	jobArgs := map[string]any{"request_key": "native-restart-1", "profile": "video", "model": "fixture/video", "prompt": "retain", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000}
	dropSubmitReply(t, a, cfg.RuntimeEndpoint, state, jobArgs)
	if jobCount(t, cfg.JobRoot) != cancelJobs+1 {
		first.Stop(t)
		t.Fatal("dropped reply did not leave exactly one additional accepted job")
	}
	_, submitted := callFixture[gateway.JobSubmitOutput](t, a, cfg.RuntimeEndpoint, state, "principal-b", gateway.ToolJobSubmit, jobArgs)
	if submitted.Outcome != "accepted" || submitted.Handle == "" || prepares.Load() == 0 {
		first.Stop(t)
		t.Fatalf("native submit: %+v prepares=%d", submitted, prepares.Load())
	}
	prepared := prepares.Load()
	jobs := jobCount(t, cfg.JobRoot)
	// A further fresh MCP process still reconciles the same operation.
	_, retried := callFixture[gateway.JobSubmitOutput](t, a, cfg.RuntimeEndpoint, state, "different-client-metadata", gateway.ToolJobSubmit, map[string]any{"request_key": "native-restart-1", "profile": "video", "model": "fixture/video", "prompt": "retain", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000})
	if retried.Outcome != "accepted" || retried.Handle != submitted.Handle || retried.OperationID != submitted.OperationID || jobCount(t, cfg.JobRoot) != jobs {
		first.Stop(t)
		t.Fatalf("idempotent retry: first=%+v retry=%+v prepares=%d->%d", submitted, retried, prepared, prepares.Load())
	}
	mismatch, _ := callFixture[gateway.JobSubmitOutput](t, a, cfg.RuntimeEndpoint, state, "principal-a", gateway.ToolJobSubmit, map[string]any{"request_key": "native-restart-1", "profile": "video", "model": "fixture/video", "prompt": "different", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000})
	if !mismatch.IsError || jobCount(t, cfg.JobRoot) != jobs {
		first.Stop(t)
		t.Fatalf("request-key spec mismatch was not effect-free: isError=%v prepares=%d->%d", mismatch.IsError, prepared, prepares.Load())
	}
	first.Stop(t)

	second := fixture.Start(t, cfg)
	defer second.Stop(t)
	var status gateway.JobStatusOutput
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		_, status = callFixture[gateway.JobStatusOutput](t, a, cfg.RuntimeEndpoint, state, "changed-after-restart", gateway.ToolJobStatus, map[string]any{"handle": submitted.Handle})
		if status.State == "complete" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status.Outcome != "observed" || status.State != "complete" || status.ResultOutcome != "unsupported" || status.Result != nil {
		t.Fatalf("persisted status: %+v", status)
	}
	_, diagnosis := callFixture[gateway.JobDiagnosisOutput](t, a, cfg.RuntimeEndpoint, state, "changed-after-restart", gateway.ToolJobDiagnose, map[string]any{"handle": submitted.Handle})
	if diagnosis.Outcome != "observed" || diagnosis.State != "complete" || diagnosis.FailureClass != "" || diagnosis.FailureCause != "" || diagnosis.Guidance == "" || strings.Contains(strings.ToLower(diagnosis.Guidance), "retain") {
		t.Fatalf("safe handle diagnosis: %+v", diagnosis)
	}
	cross, crossStatus := callFixture[gateway.JobStatusOutput](t, b, cfg.RuntimeEndpoint, state, "principal-a", gateway.ToolJobStatus, map[string]any{"handle": submitted.Handle})
	if !cross.IsError && crossStatus.Outcome == "observed" {
		t.Fatalf("different native principal observed handle: %+v", crossStatus)
	}
	cross, crossDiagnosis := callFixture[gateway.JobDiagnosisOutput](t, b, cfg.RuntimeEndpoint, state, "principal-a", gateway.ToolJobDiagnose, map[string]any{"handle": submitted.Handle})
	if !cross.IsError && (crossDiagnosis.Outcome == "observed" || crossDiagnosis.State != "" || crossDiagnosis.FailureClass != "" || crossDiagnosis.FailureCause != "") {
		t.Fatalf("different native principal received diagnosis details: %+v", crossDiagnosis)
	}
	before := prepares.Load()
	deniedSubmit, _ := callFixture[gateway.JobSubmitOutput](t, denied, cfg.RuntimeEndpoint, filepath.Join(dir, "denied-jobs"), "principal-a", gateway.ToolJobSubmit, map[string]any{"request_key": "native-denied-1", "profile": "video", "model": "fixture/video", "prompt": "must refuse", "hosting": "hosted", "credential": "fixture", "duration_ms": 1000})
	if !deniedSubmit.IsError || prepares.Load() != before {
		t.Fatalf("denied submit caused preparation: isError=%v prepares=%d->%d", deniedSubmit.IsError, before, prepares.Load())
	}
}

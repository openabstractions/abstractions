package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	facade "github.com/openabstractions/abstraction-facade/go/client"
	facaderuntime "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	authorization "github.com/openabstractions/abstraction-rights/go/authorization"
)

func TestAPIReadDeadlineAllowsMaximumServiceWait(t *testing.T) {
	started := time.Now()
	ctx, cancel := apiReadContext(t.Context())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || deadline.Sub(started) < 35*time.Second {
		t.Fatal("API read deadline must cover the admitted 30-second wait plus transport margin", deadline)
	}
	parentDeadline := started.Add(time.Second)
	parent, stop := context.WithDeadline(t.Context(), parentDeadline)
	defer stop()
	child, done := apiReadContext(parent)
	defer done()
	if actual, ok := child.Deadline(); !ok || !actual.Equal(parentDeadline) {
		t.Fatal("caller deadline changed", actual)
	}
	stop()
	if child.Err() != context.Canceled {
		t.Fatal("caller cancellation did not stop API read", child.Err())
	}
}

func TestAPIProjectionMatchesGeneratedCatalogue(t *testing.T) {
	raw, err := os.ReadFile("../projection.tsv")
	if err != nil {
		t.Fatal(err)
	}
	projected := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) == 4 && fields[0] == "projected" && fields[3] == ToolAPIRead {
			projected[fields[1]+"."+fields[2]] = true
		}
		if len(fields) == 4 && fields[0] == "transport_excluded" {
			if _, ok := findAPIRead(fields[1], fields[2]); ok {
				t.Fatal("excluded method exposed", line)
			}
		}
	}
	for _, method := range apiMethods() {
		key := method.Contract + "." + method.Method
		if !projected[key] {
			t.Fatal("missing inventory row", key)
		}
		delete(projected, key)
		if !json.Valid(method.ArgumentsSchema) || !json.Valid(method.ResultSchema) {
			t.Fatal("invalid generated schema", key)
		}
	}
	if len(projected) != 0 {
		t.Fatal("inventory lacks a generated dispatch", projected)
	}
}

func TestAPIRefusalMappingUsesIPCOutcomeTable(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-ipc", "CONTRACT.md"))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		columns := strings.Split(line, "|")
		if len(columns) != 6 || !strings.Contains(columns[4], "CallToolResult") || !strings.Contains(columns[4], "isError: true") {
			continue
		}
		word := strings.TrimSpace(columns[1])
		if strings.HasPrefix(word, "`") && strings.HasSuffix(word, "`") {
			want[strings.Trim(word, "`")] = true
		}
	}
	if len(want) != len(apiRefusalOutcomes) {
		t.Fatal("IPC-E2 mapping drift", want, apiRefusalOutcomes)
	}
	for word := range want {
		if !apiRefusalOutcomes[word] {
			t.Fatal("missing IPC-E2 outcome", word)
		}
		native := []byte(`{"value":{"outcome":"` + word + `"}}`)
		result, err := apiResult(APIReadResult{JSON: native})
		if err != nil || !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != word || result.Content[0].(*mcp.TextContent).Text != string(native) {
			t.Fatal(word, result, err)
		}
	}
	result, err := apiResult(APIReadResult{JSON: []byte(`{"code":"forbidden","message":"native refusal"}`), IsError: true})
	if err != nil || result.StructuredContent.(map[string]any)["outcome"] != "forbidden" {
		t.Fatal(result, err)
	}
}

type apiFixture struct {
	fixtureBackend
	admitted bool
	calls    int
	err      error
}

func (b *apiFixture) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	return ctx, func() {}, b.admitted
}
func (b *apiFixture) APIRead(_ context.Context, _ APIReadInput) (APIReadResult, error) {
	b.calls++
	return APIReadResult{JSON: []byte(`{"value":{"outcome":"page","integer":9007199254740993}}`)}, b.err
}

func TestAPIReadNativeAdmissionClassesAndExactNumbers(t *testing.T) {
	b := new(apiFixture)
	_, session := connected(t, b, "identity-spoof", "2026-07-28")
	params := &mcp.CallToolParams{Name: ToolAPIRead, Arguments: map[string]any{"contract": "abstraction.rights/authorization@1", "method": "Decide", "arguments": map[string]any{"action": "a/b", "resource": "r"}}}
	result, err := session.CallTool(t.Context(), params)
	if err != nil || !result.IsError || b.calls != 0 {
		t.Fatal(result, err, b.calls)
	}
	b.admitted = true
	result, err = session.CallTool(t.Context(), params)
	if err != nil || result.IsError || b.calls != 1 || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "9007199254740993") {
		t.Fatal(result, err, b.calls)
	}
	assertAppModelUntrusted(t, result)
	params.Arguments = map[string]any{"contract": "abstraction.rights/operator@1", "method": "SetRule", "arguments": map[string]any{}}
	result, err = session.CallTool(t.Context(), params)
	if err == nil && !result.IsError || b.calls != 1 {
		t.Fatal("effect dispatched", result, err, b.calls)
	}
	params.Name = ToolAPIDescribe
	params.Arguments = map[string]any{"contract": "abstraction.rights/authorization@1", "method": "Decide"}
	result, err = session.CallTool(t.Context(), params)
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "arguments_schema") {
		t.Fatal(result, err)
	}
	b.admitted = false
	result, err = session.CallTool(t.Context(), params)
	if err != nil || !result.IsError {
		t.Fatal("describe admission bypass", result, err)
	}
	for _, m := range apiMethods() {
		if m.Class != "read" || m.ArgumentsSchema == nil || m.ResultSchema == nil || strings.HasPrefix(m.Contract, "abstraction.credentials/") {
			t.Fatal(m)
		}
	}
}

func TestAPIPublicReadsUseNativeSubjectAndOperatorRefusals(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its Darwin ceiling")
	}
	dir := t.TempDir()
	p, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), []string{"test/read"})
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	subject := wire.Subject{Account: account.Uid, Program: identity.CanonicalProgramPath(exe)}
	if err := p.Set(subject, "test/read", "record", true); err != nil {
		t.Fatal(err)
	}
	sink, err := logging.OpenRetainedFileSink(filepath.Join(dir, "logs"), logging.LogsRetention{MaxUse: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	var observed atomic.Bool
	runtimeEndpoint := completionEndpoint()
	h, err := facaderuntime.Listen(facaderuntime.Options{Endpoint: runtimeEndpoint, LogEndpoint: completionEndpoint(), ConfigEndpoint: completionEndpoint(), RightsEndpoint: completionEndpoint(), ConfigWithoutMachine: true, ConfigStore: casapi.BoundedFileStore{MaxBytes: 1 << 20}, ConfigUserKey: filepath.Join(dir, "config.json"), Sink: sink, RightsPolicy: p, RightsOperator: func(context.Context, *identity.Peer) error { return authorization.ErrOperatorForbidden }, LogHistoryPolicy: func(_ context.Context, peer *identity.Peer) error {
		native, err := identity.SubjectOf(peer, listen.Program)
		if err != nil {
			return err
		}
		if native.Account != subject.Account || native.Program != subject.Program {
			return errors.New("foreign subject")
		}
		observed.Store(true)
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("native fixture did not join")
		}
	})
	// Resolve through the fixture's explicit endpoint, never installed discovery.
	oa, err := NewOA(facade.New(runtimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, session := connected(t, oa, "operator-spoof", "2026-07-28")
	read := func(contract, method string, args any) *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolAPIRead, Arguments: map[string]any{"contract": contract, "method": method, "arguments": args}})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := read("abstraction.rights/authorization@1", "Decide", map[string]any{"action": "test/read", "resource": "record"})
	if result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "permitted") {
		t.Fatal(result)
	}
	result = read("abstraction.logging/reader@1", "Read", map[string]any{"cursor": "end", "max_records": 1, "max_bytes": 4096})
	if result.IsError || !observed.Load() {
		t.Fatal(result, observed.Load())
	}
	result = read("abstraction.rights/operator@1", "ListDefaults", map[string]any{})
	if !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "forbidden" || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "forbidden") {
		t.Fatal(result)
	}
	binding, err := oa.machine.ResolveService(t.Context(), "abstraction.rights/operator@1", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := wire.MCPInvoke(binding.Transport().WithContext(t.Context()), "abstraction.rights/operator@1", "SetDefault", []byte(`{"expected_revision":"","name":"x","on":true}`))
	if err != nil || !strings.Contains(string(raw), "forbidden") {
		t.Fatal("operator write admitted", string(raw), err)
	}
	if _, ok := findAPIRead("abstraction.rights/operator@1", "SetDefault"); ok {
		t.Fatal("slice 1 exposed an effect")
	}
}

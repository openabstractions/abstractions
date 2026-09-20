package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

type failingInferenceDiagnosticSink struct {
	writes atomic.Int64
	called chan struct{}
}

func (s *failingInferenceDiagnosticSink) Write(logging.Record) error {
	s.writes.Add(1)
	select {
	case s.called <- struct{}{}:
	default:
	}
	return errors.New("diagnostic sink refused inference record")
}

// hostedUpstream is a fake OpenAI-compatible provider that lists one model and
// streams "Hello!" to a request carrying the bearer secret, recording the
// Authorization header of every chat request.
type hostedUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	chat []string
}

func newHostedUpstream(t *testing.T) *hostedUpstream {
	t.Helper()
	u := &hostedUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorized := r.Header.Get("Authorization") == "Bearer "+testCredentialSecret
		if r.Method == http.MethodPost {
			u.mu.Lock()
			u.chat = append(u.chat, r.Header.Get("Authorization"))
			u.mu.Unlock()
		}
		if !authorized {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"anthropic/claude-sonnet-5"}]}`))
			return
		}
		for _, c := range []string{"Hel", "lo", "!"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *hostedUpstream) authorizations() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.chat...)
}

func TestRuntimeInferenceDiagnosticSinkFailureDoesNotDuplicateWork(t *testing.T) {
	upstream, chats := fakeOllama(t)
	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`{"local":[{"kind":"ollama","base":%q}],"declared":false}`, upstream.URL)
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	rightsState, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	program := rightsState.operators[0]
	if err := rightsState.policy.RegisterAction(inference.ActionComplete); err != nil {
		t.Fatal(err)
	}
	if err := rightsState.policy.Set(rwire.Subject{Account: rightsState.owner, Program: program}, inference.ActionComplete, inference.ResourceHost("ollama"), true); err != nil {
		t.Fatal(err)
	}
	credentials := &runtimeCredentials{runtimeRights: rightsState}
	var reports chan error = make(chan error, 8)
	report := func(err error) {
		select {
		case reports <- err:
		default:
		}
	}
	sink := &failingInferenceDiagnosticSink{called: make(chan struct{}, 1)}
	runtime, err := composeInference(options, credentials, sink, report)
	if err != nil {
		t.Fatal(err)
	}
	if runtime == nil {
		t.Fatal("composeInference returned nil")
	}
	defer runtime.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	subject := inference.Subject{Account: rightsState.owner, Program: program}
	admission := runtime.provider.Start(ctx, subject, iwire.Request{Model: "fixture-chat:1b", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly}, Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hello"}}}}})
	if admission.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("admission = %+v", admission)
	}
	var end *iwire.Reply
	cursor := int64(0)
	for {
		page := runtime.provider.Observe(ctx, subject, admission.Operation, cursor, 256, 65536, 500)
		if page.Outcome != iwire.PageOutcomePage {
			t.Fatalf("observe = %+v", page)
		}
		for _, delta := range page.Deltas {
			if delta.End != nil {
				end = delta.End
			}
		}
		if page.AtEnd {
			break
		}
		cursor = page.Next
	}
	if end == nil || end.Outcome != iwire.ReplyOutcomeCompleted {
		t.Fatalf("end = %+v", end)
	}
	if got := chats.Load(); got != 1 {
		t.Fatalf("upstream chats = %d, want one accepted operation", got)
	}
	select {
	case <-sink.called:
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic sink was not called")
	}
	if sink.writes.Load() != 1 {
		t.Fatalf("diagnostic writes = %d, want one", sink.writes.Load())
	}
	select {
	case reportErr := <-reports:
		if !strings.Contains(reportErr.Error(), "diagnostic sink refused") {
			t.Fatalf("reported error = %v", reportErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("diagnostic sink failure was not reported")
	}
}

// setCompleteRule grants or denies abstraction.inference/complete on host:<name>
// to program through the runtime's rights operator.
func setCompleteRule(t *testing.T, endpoint, program, host string, permit bool) {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	rule := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: program}, Action: inference.ActionComplete, Resource: inference.ResourceHost(host), Permit: permit}
	edit, err := operator.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("set rule: %+v %v", edit, err)
	}
}

// The runtime publishes chat@1. A hosted host lists its models with the
// registered credential; a program without a complete rule is not permitted
// and nothing reaches the upstream; with the rule the reply streams, the
// upstream sees the credential once, and the secret is in no reply, record,
// log or state file.
func TestRuntimeInferenceAppliesAHostedCredential(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	upstream := newHostedUpstream(t)
	options, _ := isolatedRuntime(t)
	config := fmt.Sprintf(`{"local":[],"hosted":[{"name":"openrouter","base":%q,"wire":"openai-compatible","credential":"openrouter"}],"ceilings":{"openrouter":{"tokens_per_day":100000}}}`, upstream.URL+"/api/v1")
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), config},
	} {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Clean(exe)
	var transcript strings.Builder
	out, diag, err := runCredentials(t, testCredentialSecret+"\n", "add", "openrouter", "--target", "127.0.0.1",
		"--for", inference.Contract, "--for", "abstraction.router/router@1", "--from-stdin", "--use-by", exe, "--endpoint", options.endpoint, "--timeout", "30s")
	transcript.WriteString(out + diag)
	requireUserScopeAdd(t, err, transcript.String())

	call, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	resolver := resolution.NewUnverifiedClient(options.endpoint, 2*time.Second)
	result, err := resolver.Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference", Contracts: []string{inference.Contract}, Guarantees: []string{inference.GuaranteeHosted}, Scope: fwire.ScopeRemote})
	if err != nil || result.Status != fwire.ResolutionStatusResolved {
		t.Fatalf("resolve chat@1: %+v %v", result, err)
	}
	bound, err := resolution.BindLocal(call, *result.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	chat := iwire.NewChatClient(bound.WithDefaults(35*time.Second, 1<<20))
	request := iwire.Request{Model: "anthropic/claude-sonnet-5", Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Credential: "openrouter",
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hi"}}}}}

	refused, err := chat.Start(request)
	if err != nil || refused.Outcome != iwire.StartOutcomeNotPermitted || refused.Reason != "rights:not_granted" {
		t.Fatalf("start without a complete rule: %+v %v", refused, err)
	}
	if got := upstream.authorizations(); len(got) != 0 {
		t.Fatalf("a refused start reached the upstream: %q", got)
	}

	setCompleteRule(t, options.endpoint, exe, "openrouter", true)
	local, err := resolver.Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference", Contracts: []string{inference.Contract}, Guarantees: []string{inference.GuaranteeLocalOnly}, Scope: fwire.ScopeLocal})
	if err != nil || local.Status != fwire.ResolutionStatusResolved || local.Reference == nil || local.Reference.Endpoint == result.Reference.Endpoint {
		t.Fatalf("resolve local chat@1: %+v %v", local, err)
	}
	localTransport, err := resolution.BindLocal(call, *local.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	localAdmission, err := iwire.NewChatClient(localTransport.WithDefaults(35*time.Second, 1<<20)).Start(request)
	if err != nil || localAdmission.Outcome != iwire.StartOutcomeNoHost {
		t.Fatalf("hosted request through local binding: %+v %v", localAdmission, err)
	}
	if got := upstream.authorizations(); len(got) != 0 {
		t.Fatalf("local binding reached hosted upstream: %q", got)
	}
	remoteLocalRequest := request
	remoteLocalRequest.Guarantees = []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly}
	remoteLocalRequest.Credential = ""
	remoteLocal, err := chat.Start(remoteLocalRequest)
	if err != nil || remoteLocal.Outcome != iwire.StartOutcomeNoHost {
		t.Fatalf("local-only request through remote binding: %+v %v", remoteLocal, err)
	}
	if got := upstream.authorizations(); len(got) != 0 {
		t.Fatalf("remote binding widened local-only request: %q", got)
	}
	admission, err := chat.Start(request)
	if err != nil || admission.Outcome != iwire.StartOutcomeAccepted || admission.Host != "openrouter" {
		t.Fatalf("start with a complete rule: %+v %v", admission, err)
	}
	var text strings.Builder
	var pages []iwire.DeltaPage
	cursor := int64(0)
	for i := 0; i < 100; i++ {
		page, err := chat.Observe(admission.Operation, cursor, 256, 65536, 2000)
		if err != nil || page.Outcome != iwire.PageOutcomePage {
			t.Fatalf("observe: %+v %v", page, err)
		}
		pages = append(pages, page)
		for _, d := range page.Deltas {
			if d.Kind == iwire.DeltaKindPart {
				text.WriteString(d.Part.Text)
			}
		}
		cursor = page.Next
		if page.AtEnd {
			break
		}
	}
	end := pages[len(pages)-1].Deltas[len(pages[len(pages)-1].Deltas)-1].End
	if text.String() != "Hello!" || end == nil || end.Outcome != iwire.ReplyOutcomeCompleted || end.Usage.Input != 5 {
		t.Fatalf("reply %q %+v", text.String(), end)
	}
	if got := upstream.authorizations(); len(got) != 1 || got[0] != "Bearer "+testCredentialSecret {
		t.Fatalf("upstream chat Authorization %q", got)
	}
	replies, _ := json.Marshal([]any{refused, admission, pages})
	if bytes.Contains(replies, []byte(testCredentialSecret)) || strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("a reply or command output carries the secret")
	}
	deadline := time.Now().Add(10 * time.Second)
	var log []byte
	for time.Now().Before(deadline) {
		log, _ = os.ReadFile(options.out)
		if bytes.Contains(log, []byte(`"outcome":"completed"`)) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Contains(log, []byte("abstraction.inference/chat@1")) || !bytes.Contains(log, []byte(`"outcome":"not_permitted"`)) || !bytes.Contains(log, []byte(`"outcome":"completed"`)) {
		t.Fatalf("runtime log lacks the inference records:\n%s", log)
	}
	if bytes.Contains(log, []byte(testCredentialSecret)) {
		t.Fatal("the runtime log carries the secret")
	}
	err = filepath.WalkDir(options.stateDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Errorf("%s carries the secret", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

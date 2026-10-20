package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	download "github.com/openabstractions/abstraction-download/go"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	digestfmt "github.com/openabstractions/abstraction-storage/go/digest"
)

// TestFirstUseRefusalsNamePendingQuestions is the guard for RIGHTS-FLOW ruling
// 2 and INF-W11 across the runtime's enforcers: every refusal of an askable
// right a fresh program meets names, as rights:question_pending:<id> and in
// order, each question the book holds pending about that program, and an
// identical second call names the same ids. Each row runs with its own fresh
// program against the runtime's real policy, decision point and asks book.
// The native chat and image routes hold and name through
// inferenceservice.awaitAdmission, guarded by its own
// TestNativePendingQuestionRefusesWithinDefaultBudget and
// TestNativeAnsweredQuestionIsNotNamed.
func TestFirstUseRefusalsNamePendingQuestions(t *testing.T) {
	state := t.TempDir()
	r := testRights(t, state)
	holder, err := credentials.Open(credentials.Config{Backend: &privateReleaseMemory{values: map[string][]byte{}}, Namespace: "first-use-refusals"})
	if err != nil {
		t.Fatal(err)
	}
	c := &runtimeCredentials{runtimeRights: r, holder: holder, host: &credservice.Host{}}
	c.host.Authorization = credservice.AuthorizationConfig{Admit: c.admitScoped, Quiet: c.checkScoped, Legacy: c.checkLegacy}
	r.configure(&host.Options{Credentials: c.host, OnError: func(err error) { t.Error(err) }}, nil)
	var actions []string
	for _, action := range slices.Concat(firstUseActions, []string{contentReadAction, contentWriteAction}, cwire.ResourceActions) {
		if !slices.Contains(actions, action) {
			actions = append(actions, action)
		}
	}
	if err := r.install(actions, nil); err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	a.rights = r
	r.firstUse, r.pendingQuestions, r.pairedInput, r.typedFirstUse = a.admitFirstUse, a.pendingQuestions, a.admitPairedInput, a.admitTypedService
	content, err := composeContent(runtimeFlags{endpoint: "first-use-" + filepath.Base(state)}, state, r)
	if err != nil {
		t.Fatal(err)
	}
	operator := cwire.Subject{Account: r.owner, Program: r.operators[0]}
	for _, stored := range []cwire.Registration{
		{Name: "hf", Kind: "bearer", Scope: cwire.Scope{Targets: []string{"huggingface.co"}, Consumers: []string{downloadserve.CredentialConsumer}}, Secret: []byte("FIRST_USE_FIXTURE_HF")},
		{Name: "replicate", Kind: "bearer", Scope: cwire.Scope{Targets: []string{"api.replicate.com"}, Consumers: []string{inference.ImageContract}}, Secret: []byte("FIRST_USE_FIXTURE_REPLICATE")},
	} {
		if v := holder.Store(operator, "", stored); v.Outcome != cwire.StoreOutcomeStored {
			t.Fatalf("store %s: %s", stored.Name, v.Outcome)
		}
	}

	// One hosted OpenAI-compatible server serves every window profile, a
	// Replicate server image jobs and a native provider edit jobs. Nothing
	// refused may reach an upstream.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet && strings.HasSuffix(req.URL.Path, "/models") {
			w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o-mini"},{"id":"gpt-image-1"},{"id":"tts-1"},{"id":"whisper-1"},{"id":"text-embedding-3-small"}]}`))
			return
		}
		t.Errorf("a refused call reached the upstream: %s %s", req.Method, req.URL.Path)
		http.Error(w, "refused", http.StatusForbidden)
	}))
	t.Cleanup(upstream.Close)
	native, err := router.NewNative("native-image", "unused", listen.ServerExpectation{Principal: identity.User{Kind: "posix", UID: 1}, Program: "/provider"}, []string{"edit-model"}, []string{router.ProfileImage})
	if err != nil {
		t.Fatal(err)
	}
	routes := router.New(router.NewHosted("openai", upstream.URL, router.WireOpenAICompatible, "openai"),
		router.NewHosted("replicate", "https://api.replicate.com", router.WireReplicatePredictions, "replicate"), native)
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	routes.Survey()
	provider, err := inference.New(inference.Config{Router: routes, SurveyAge: time.Hour, Decide: c.decideInference, AskInput: r.askInput,
		ResolveContent: content.read, PrepareContentWrite: content.prepareWrite,
		Apply: func(ctx context.Context, s inference.Subject, consumer, name, target string) (map[string]string, string) {
			result := c.holder.Apply(ctx, cwire.Use{Subject: cwire.Subject{Account: s.Account, Program: s.Program}, Consumer: consumer, Name: name, Target: target}, c.decide)
			if result.Outcome == cwire.ApplyOutcomeApplied {
				t.Errorf("a refused call applied %s", name)
			}
			return nil, result.Outcome.String()
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })

	// The job executor, composed as composeInferenceJobs composes it.
	execution := inference.NewJobExecution(provider, func(context.Context, string, string) (inference.Subject, inference.ContentOutcome) {
		return inference.Subject{}, inference.ContentForbidden
	}, func(err error) { t.Error(err) })
	execution.SetPendingQuestions(r.pendingJobQuestions)
	jobs := &runtimeInferenceJobExecutor{inference: execution}

	// The gateway window, its key spending under the openai credential, its
	// hold ending at once as when the person does not answer in time.
	var boundMu sync.Mutex
	var current inference.Subject
	window, err := gateway.Listen("127.0.0.1:0", gateway.Config{Chat: provider, Account: r.owner, StoreContent: content.write, ReadContent: content.read,
		Keys: func(context.Context, inference.Subject, string) (gateway.Grant, string) {
			return gateway.Grant{Credential: "openai"}, gateway.KeyVerified
		},
		Bind: func(net.Conn, time.Time) (gateway.Bound, error) {
			boundMu.Lock()
			defer boundMu.Unlock()
			return foundingBound{current}, nil
		},
		QuestionWait: time.Second,
		Await:        func(context.Context, inference.Subject, []string) bool { return false },
		Pending: func(_ context.Context, s inference.Subject, ids []string) []string {
			pending, err := r.pendingQuestions(rwire.Subject{Account: s.Account, Program: s.Program}, ids)
			if err != nil {
				t.Error(err)
			}
			return pending
		}})
	if err != nil {
		t.Fatal(err)
	}
	go window.Serve(context.Background())
	t.Cleanup(func() { window.Close() })
	base := "http://" + window.Addr().String() + "/v1"
	post := func(program, path, contentType string, body []byte) (string, string) {
		t.Helper()
		boundMu.Lock()
		current = inference.Subject{Account: r.owner, Program: program}
		boundMu.Unlock()
		req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer placeholder")
		req.Header.Set("Content-Type", contentType)
		// The window binds a connection's program once, at accept: each call
		// opens its own connection to be bound to its row's program.
		client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var refused struct {
			Error struct{ Code, Message string }
		}
		if json.Unmarshal(raw, &refused) != nil || refused.Error.Code == "" {
			return fmt.Sprint(resp.StatusCode), string(raw)
		}
		reason, _ := strings.CutPrefix(refused.Error.Message, refused.Error.Code+": ")
		return refused.Error.Code, reason
	}
	multipartBody := func(file, name string, data []byte, fields map[string]string) (string, []byte) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, err := mw.CreateFormFile(file, name)
		if err != nil {
			t.Fatal(err)
		}
		part.Write(data)
		for key, value := range fields {
			mw.WriteField(key, value)
		}
		mw.Close()
		return mw.FormDataContentType(), body.Bytes()
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	audio := append([]byte("RIFF\x24\x00\x00\x00WAVEfmt "), make([]byte, 64)...)
	grant := func(program string, resources ...string) {
		t.Helper()
		for _, resource := range resources {
			for _, action := range []string{contentWriteAction, contentReadAction} {
				if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: program}, action, resource, true); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	submitJob := func(program string, image jobwire.ImageRequest) (string, string) {
		bound := acceptanceprovider.AuthenticatedSubject{AccountKind: "windows", Account: r.owner, Program: program}
		binding := acceptanceprovider.Binding{Scope: "agent-scope", Origin: "local", Subject: &bound}
		spec := jobwire.Encode(&jobwire.Document{Request: &jobwire.Request{Profile: jobwire.ProfileImageBatch, Image: image}})
		outcome, reason := jobs.CheckSubjectAdmission(binding, inference.InferenceJobKind, spec, []string{inference.RecoverableUpstreamGuarantee})
		return outcome.String(), reason
	}
	replicate := jobwire.ImageRequest{Model: "black-forest-labs/flux-schnell", Mode: jobwire.ModeGenerate, Prompt: "frogs", Size: "1024x1024", Count: 1,
		Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: "replicate", Extensions: map[string]string{}}
	admitDownload := func(program, route string) (string, string) {
		bound := acceptanceprovider.AuthenticatedSubject{AccountKind: "windows", Account: r.owner, Executable: program, Program: program}
		binding := acceptanceprovider.Binding{Scope: "agent-scope", Origin: "local", Subject: &bound}
		spec := request.Encode(&request.Request{Sources: []request.Source{{Scheme: "http", Locator: "https://huggingface.co/gated/model.gguf", Credential: "hf"}}})
		required := []string{downloadserve.CredentialGuarantee}
		if route == downloadserve.ScopedCredentialGuarantee {
			required = request.ScopedCredentialGuarantees
		}
		outcome, reason := downloadserve.HTTPExecution{Credentials: c}.CheckSubjectAdmission(binding, download.Kind, spec, required)
		return outcome.String(), reason
	}

	for _, row := range []struct {
		name string
		// prepare grants what the row's enforcer needs before the one
		// right it refuses; nil grants nothing.
		prepare func(program string)
		call    func(program string) (string, string)
	}{
		{name: "job complete and output", call: func(program string) (string, string) { return submitJob(program, replicate) }},
		{name: "job edit input", call: func(program string) (string, string) {
			sum := sha256.Sum256(picture.Bytes())
			return submitJob(program, jobwire.ImageRequest{Model: "edit-model", Mode: jobwire.ModeEdit, Prompt: "frogs", Size: "1024x1024", Count: 1,
				ImageDigest: digestfmt.Hex(sum[:]), ImageMediaType: "image/png", Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeLocalOnly}, Extensions: map[string]string{}})
		}},
		{name: "job credential apply", prepare: func(program string) {
			if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: program}, inference.ActionComplete, inference.ResourceHost("replicate"), true); err != nil {
				t.Fatal(err)
			}
			grant(program, "output:"+inference.OutputImage)
		}, call: func(program string) (string, string) { return submitJob(program, replicate) }},
		{name: "window chat", call: func(program string) (string, string) {
			return post(program, "/chat/completions", "application/json", []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
		}},
		{name: "window image generation", call: func(program string) (string, string) {
			return post(program, "/images/generations", "application/json", []byte(`{"model":"gpt-image-1","prompt":"frogs","size":"1024x1024"}`))
		}},
		{name: "window image edit", prepare: func(program string) { grant(program, inference.InputResource(inference.InputImage)) }, call: func(program string) (string, string) {
			kind, body := multipartBody("image", "frog.png", picture.Bytes(), map[string]string{"model": "gpt-image-1", "prompt": "a hat", "size": "1024x1024"})
			return post(program, "/images/edits", kind, body)
		}},
		// With no input profile the window's own upload store refuses
		// first and raises the paired input question.
		{name: "window image edit upload", call: func(program string) (string, string) {
			kind, body := multipartBody("image", "frog.png", picture.Bytes(), map[string]string{"model": "gpt-image-1", "prompt": "a hat", "size": "1024x1024"})
			return post(program, "/images/edits", kind, body)
		}},
		{name: "window transcription upload", call: func(program string) (string, string) {
			kind, body := multipartBody("file", "hello.wav", audio, map[string]string{"model": "whisper-1"})
			return post(program, "/audio/transcriptions", kind, body)
		}},
		{name: "window speech", call: func(program string) (string, string) {
			return post(program, "/audio/speech", "application/json", []byte(`{"model":"tts-1","voice":"alloy","input":"hello"}`))
		}},
		{name: "window transcription", prepare: func(program string) { grant(program, inference.InputResource(inference.InputAudio)) }, call: func(program string) (string, string) {
			kind, body := multipartBody("file", "hello.wav", audio, map[string]string{"model": "whisper-1"})
			return post(program, "/audio/transcriptions", kind, body)
		}},
		{name: "window embeddings", call: func(program string) (string, string) {
			return post(program, "/embeddings", "application/json", []byte(`{"model":"text-embedding-3-small","input":"hello"}`))
		}},
		{name: "download credential admission", call: func(program string) (string, string) {
			return admitDownload(program, downloadserve.CredentialGuarantee)
		}},
		{name: "download scoped credential admission", call: func(program string) (string, string) {
			return admitDownload(program, downloadserve.ScopedCredentialGuarantee)
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), "fresh-agent.exe")
			if row.prepare != nil {
				row.prepare(program)
			}
			outcome, reason := row.call(program)
			t.Logf("%s %s", outcome, reason)
			if outcome != "forbidden" && outcome != "not_permitted" {
				t.Fatalf("refusal %s %q, want forbidden or not_permitted", outcome, reason)
			}
			records, _, err := book.ApplicationHistory()
			if err != nil {
				t.Fatal(err)
			}
			var want []string
			for _, q := range records {
				if q.Pending() && samePrograms(q.About, program) {
					want = append(want, q.ID)
				}
			}
			if len(want) == 0 || !slices.Equal(asks.QuestionsPending(reason), want) {
				t.Fatalf("refusal %s %q must name the pending questions %v about %s", outcome, reason, want, program)
			}
			if again, reason2 := row.call(program); again != outcome || !slices.Equal(asks.QuestionsPending(reason2), want) {
				t.Fatalf("repeated refusal %s %q, want %s naming %v", again, reason2, outcome, want)
			}
		})
	}
}

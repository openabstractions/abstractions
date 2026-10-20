package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentialsapi "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	job "github.com/openabstractions/abstraction-job/go"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestServeInferenceWorkersCancelsAndJoinsSibling(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	want := errors.New("base failed")
	err := serveInferenceWorkers(context.Background(), func(context.Context) error {
		<-started
		return want
	}, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})
	if !errors.Is(err, want) {
		t.Fatalf("Serve error = %v, want %v", err, want)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Serve returned before sibling observed cancellation")
	}
}

// Exercise the real job admission owner with the native submitter peer. Its
// independent ten-second planning context carries no active framed collector.
func assertJobFirstUseDoesNotOwnNativeWait(t *testing.T, r *runtimeRights, subject inference.Subject, peer *identity.Peer, base string) {
	t.Helper()
	routes := router.New(router.NewHosted("native1447-job", base, router.WireReplicatePredictions, "fixture"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) { return nil, nil })
	routes.Survey() // Replicate's existing catalogue is local static DATA.
	called := false
	provider, err := inference.New(inference.Config{Router: routes, SurveyAge: time.Hour,
		Decide: func(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
			called = true
			if inference.PeerFromContext(ctx) != peer {
				t.Error("job lost bound submitter")
			}
			if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
				t.Error("job did not use its own planning budget")
			}
			if r.nativeAdmissionQuestions(ctx, rwire.Subject{Account: s.Account, Program: s.Program}) != nil {
				t.Error("job acquired framed native wait")
			}
			return r.decideInference(ctx, s, action, resource)
		}, Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			t.Error("ungranted job reached credential application")
			return nil, "not_permitted"
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	executor := inference.NewJobExecution(provider, func(context.Context, string, string) (inference.Subject, inference.ContentOutcome) {
		t.Error("bound job unexpectedly resolved caller again")
		return inference.Subject{}, inference.ContentForbidden
	}, nil)
	spec := jobwire.Encode(&jobwire.Document{Request: &jobwire.Request{Profile: jobwire.ProfileImageBatch, Image: jobwire.ImageRequest{Model: "black-forest-labs/flux-schnell", Mode: jobwire.ModeGenerate, Prompt: "inert", Size: "1024x1024", Count: 1, Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: "fixture", Extensions: map[string]string{}}}})
	outcome, reason := executor.CheckAdmissionForSubject("native1447-job", inference.InferenceJobKind, spec, []string{inference.RecoverableUpstreamGuarantee}, subject, peer)
	if !called || outcome.String() != "forbidden" || reason != "rights:not_granted" {
		t.Fatalf("job first-use admission: called=%t %s %s", called, outcome, reason)
	}
}

type preservedJobExecutor struct {
	preparedKind string
	preparedSpec []byte
}

func (*preservedJobExecutor) Profile() string { return "download-http-request-v1" }
func (e *preservedJobExecutor) Prepare(_ string, kind string, spec []byte) ([]byte, error) {
	e.preparedKind, e.preparedSpec = kind, append([]byte(nil), spec...)
	return append([]byte("base:"), spec...), nil
}
func (*preservedJobExecutor) Serve(context.Context, job.Store) error { return nil }
func (*preservedJobExecutor) ExecutionGuarantees() []string          { return []string{"download"} }
func (*preservedJobExecutor) RecoveryGuarantees() []string           { return []string{"download"} }
func (e *preservedJobExecutor) PrepareWithGuarantees(id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	return e.PrepareScoped("", id, kind, spec, required)
}
func (e *preservedJobExecutor) PrepareScoped(_ string, id, kind string, spec []byte, _ []string) ([]byte, []string, error) {
	prepared, err := e.Prepare(id, kind, spec)
	return prepared, []string{"download"}, err
}

func TestRuntimeInferenceJobsPreservesExistingPreparationAndProfile(t *testing.T) {
	base := &preservedJobExecutor{}
	combined := &runtimeInferenceJobExecutor{base: base, inference: inference.NewJobExecution(nil, nil, nil)}
	if combined.Profile() != base.Profile() {
		t.Fatalf("profile changed to %q", combined.Profile())
	}
	prepared, guarantees, err := combined.PrepareScoped("scope", "id", "download", []byte("old"), []string{"download"})
	if err != nil || string(prepared) != "base:old" || len(guarantees) != 1 || guarantees[0] != "download" {
		t.Fatalf("prepared %q guarantees %v error %v", prepared, guarantees, err)
	}
	if base.preparedKind != "download" || string(base.preparedSpec) != "old" {
		t.Fatalf("base saw %q %q", base.preparedKind, base.preparedSpec)
	}
	if got := combined.ExecutionGuarantees(); len(got) != 2 || got[0] != "download" || got[1] != inference.RecoverableUpstreamGuarantee {
		t.Fatalf("execution guarantees %v", got)
	}
	if got := combined.RecoveryGuarantees(); len(got) != 2 || got[0] != "download" || got[1] != inference.RecoverableUpstreamGuarantee {
		t.Fatalf("recovery guarantees %v", got)
	}
}

// JOB-A17: the runtime's job service names the download kind's result file
// for a hard link through the executor it wraps; an inference job's result
// document is no file, and its link is unsupported.
func TestRuntimeJobsNameTheDownloadResultForALink(t *testing.T) {
	base, err := newDownloadExecutor(downloadBackendNative, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	combined := &runtimeInferenceJobExecutor{base: base, inference: inference.NewJobExecution(nil, nil, nil)}
	var namer acceptanceprovider.ResultNamer = combined
	name, err := namer.OperationResultName(&job.Record{ID: "operation123", Kind: "download", State: job.StateComplete})
	if err != nil || name != "results/operation123" {
		t.Fatalf("download result name %q %v", name, err)
	}
	if _, err := namer.OperationResultName(&job.Record{ID: "operation123", Kind: inference.InferenceJobKind, State: job.StateComplete}); !errors.Is(err, acceptanceprovider.ErrResultNotNamed) {
		t.Fatalf("an inference job named a result file: %v", err)
	}
	unnamed := &runtimeInferenceJobExecutor{base: &preservedJobExecutor{}, inference: inference.NewJobExecution(nil, nil, nil)}
	if _, err := unnamed.OperationResultName(&job.Record{ID: "operation123", Kind: "download", State: job.StateComplete}); !errors.Is(err, acceptanceprovider.ErrResultNotNamed) {
		t.Fatalf("a base without names named a result file: %v", err)
	}
}

func TestRuntimeInferenceJobsRoutesInferencePreparationToKindOwner(t *testing.T) {
	base := &preservedJobExecutor{}
	combined := &runtimeInferenceJobExecutor{base: base, inference: inference.NewJobExecution(nil, nil, nil)}
	document := &jobwire.Document{Request: &jobwire.Request{Profile: jobwire.ProfileVideo, DurationMs: 8000, Image: jobwire.ImageRequest{
		Model: "owner/model", Mode: jobwire.ModeGenerate, Prompt: "lighthouse", Size: "1024x1024", Count: 1,
		Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: "replicate", Extensions: map[string]string{},
	}}}
	prepared, guarantees, err := combined.PrepareScoped("caller-scope", "id", inference.InferenceJobKind, jobwire.Encode(document), []string{inference.RecoverableUpstreamGuarantee})
	if err != nil || len(prepared) == 0 || len(guarantees) != 1 || guarantees[0] != inference.RecoverableUpstreamGuarantee {
		t.Fatalf("prepared %q guarantees %v error %v", prepared, guarantees, err)
	}
	if base.preparedKind != "" {
		t.Fatalf("inference preparation leaked to base kind %q", base.preparedKind)
	}
	if _, _, err := combined.PrepareScoped("caller-scope", "id", inference.InferenceJobKind, jobwire.Encode(document), nil); err == nil {
		t.Fatal("missing recovery guarantee was accepted")
	}
	var _ acceptanceprovider.Executor = combined
}

func TestRuntimeInferenceJobsRecoverLocalCallerWithoutCredentialGrant(t *testing.T) {
	rights := testRights(t, t.TempDir())
	if err := rights.policy.RegisterAction(inference.ActionComplete); err != nil {
		t.Fatal(err)
	}
	program := rights.operators[0]
	subject := rwire.Subject{Account: rights.owner, Program: program}
	if err := rights.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("lemonade"), true); err != nil {
		t.Fatal(err)
	}
	scope, err := host.OwnerProgramScope(rights.kind, rights.owner, program)
	if err != nil {
		t.Fatal(err)
	}
	credentials := &runtimeCredentials{runtimeRights: rights}
	resolved, err := credentials.localScopeSubject(scope)
	if err != nil || resolved.Account != rights.owner || resolved.Program != program {
		t.Fatalf("resolved %+v error %v", resolved, err)
	}
	if _, err := credentials.localScopeSubject(scope + "-other"); err == nil {
		t.Fatal("unknown local scope resolved")
	}
}

func TestRuntimeInferenceJobsRecoverAndRevokeHostedCallerScope(t *testing.T) {
	rights := testRights(t, t.TempDir())
	if err := rights.policy.RegisterAction(credentialsapi.ActionApply); err != nil {
		t.Fatal(err)
	}
	program := rights.operators[0]
	subject := rwire.Subject{Account: rights.owner, Program: program}
	resource := credentialsapi.ResourceFor("replicate")
	if err := rights.policy.Set(subject, credentialsapi.ActionApply, resource, true); err != nil {
		t.Fatal(err)
	}
	scope, err := host.OwnerProgramScope(rights.kind, rights.owner, program)
	if err != nil {
		t.Fatal(err)
	}
	credentials := &runtimeCredentials{runtimeRights: rights}
	resolved, err := credentials.scopeSubject(scope, "replicate")
	if err != nil || resolved.Account != rights.owner || resolved.Program != program {
		t.Fatalf("resolved %+v error %v", resolved, err)
	}
	if err := rights.policy.Revoke(subject, credentialsapi.ActionApply, resource); err != nil {
		t.Fatal(err)
	}
	if _, err := credentials.scopeSubject(scope, "replicate"); err == nil {
		t.Fatal("revoked hosted scope still resolved")
	}
}

// TestImageJobFirstUseRecordsTheSubmittersPublisher: an image job a signed
// program submits over the native job route, refused for want of a complete
// rule, admits a first-use question naming the publisher read from the peer
// its binding was derived from (ASK-A6). A remote submitter's job carries no
// peer, and its question records none.
func TestImageJobFirstUseRecordsTheSubmittersPublisher(t *testing.T) {
	_, _, peer := pythonPipePeer(t)
	bound, err := inferenceservice.SubjectFromPeer(peer)
	if err != nil {
		t.Fatal(err)
	}
	code, proof := peer.Code.Get()
	want := asks.CodePublisher(code)
	if proof <= identity.ProofNone || want.Subject == "" {
		t.Skipf("the Python interpreter %s carries no embedded signer name (%s)", bound.Program, code.Status)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), firstUseActions)
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: bound.Account, firstUse: a.admitFirstUse}
	routes := router.New(router.NewHosted("replicate", "https://api.replicate.com", router.WireReplicatePredictions, "replicate"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	routes.Survey()
	provider, err := inference.New(inference.Config{Router: routes, Decide: r.decideInference,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "not_permitted"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	executor := &runtimeInferenceJobExecutor{inference: inference.NewJobExecution(provider, func(context.Context, string, string) (inference.Subject, inference.ContentOutcome) {
		return inference.Subject{}, inference.ContentForbidden
	}, nil)}
	spec := jobwire.Encode(&jobwire.Document{Request: &jobwire.Request{Profile: jobwire.ProfileImageBatch, Image: jobwire.ImageRequest{
		Model: "black-forest-labs/flux-schnell", Mode: jobwire.ModeGenerate, Prompt: "a lighthouse", Size: "1024x1024", Count: 1,
		Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: "replicate", Extensions: map[string]string{},
	}}})
	path, err := peer.Path.AtLeast(listen.Program.Path)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := acceptanceprovider.LocalSubjectScope("windows", bound.Account, path)
	if err != nil {
		t.Fatal(err)
	}
	remoteProgram := filepath.Join(t.TempDir(), "remote.exe")
	for _, c := range []struct {
		program string
		binding acceptanceprovider.Binding
		want    asks.Publisher
	}{
		{bound.Program, acceptanceprovider.Binding{Scope: scope, Origin: "local", Peer: peer,
			Subject: &acceptanceprovider.AuthenticatedSubject{AccountKind: "windows", Account: bound.Account, Executable: path, Program: bound.Program}}, want},
		{remoteProgram, acceptanceprovider.Binding{Scope: "remote-scope", Origin: "remote", Evidence: strings.Repeat("ab", 32),
			Subject: &acceptanceprovider.AuthenticatedSubject{AccountKind: "windows", Account: bound.Account, Program: remoteProgram}}, asks.Publisher{}},
	} {
		outcome, reason := executor.CheckSubjectAdmission(c.binding, inference.InferenceJobKind, spec, []string{inference.RecoverableUpstreamGuarantee})
		if outcome.String() != "forbidden" || reason != "rights:not_granted" {
			t.Fatalf("admission for %s: %s %s", c.program, outcome, reason)
		}
		records, _, err := book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		asked := false
		for _, q := range records {
			if _, action, _, ok := asks.FirstUse(q.Key, q.About, q.Text); !ok || action != inference.ActionComplete || !samePrograms(q.About, c.program) {
				continue
			}
			asked = true
			if q.Publisher != c.want.Subject || q.PublisherTrusted != c.want.Trusted {
				t.Fatalf("the job question about %s must carry publisher %+v: %+v", c.program, c.want, q)
			}
		}
		if !asked {
			t.Fatalf("no complete question about %s: %+v", c.program, records)
		}
	}
}

// TestImageJobFirstUseRefusalNamesPendingQuestions: an agent's first image
// job is refused for want of complete on the image host and of its output
// profile, and the refusal names both first-use questions it raised, so the
// MCP gateway waits for the person's Allow and submits again (INF-W11). The
// owner's OpenCode MCP launcher read a bare "rights:not_granted
// output:forbidden" on 2026-11-10 while both popups waited.
func TestImageJobFirstUseRefusalNamesPendingQuestions(t *testing.T) {
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), append(append([]string{}, firstUseActions...), contentReadAction, contentWriteAction))
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: a.rights.owner, firstUse: a.admitFirstUse, pairedInput: a.admitPairedInput, pendingQuestions: a.pendingQuestions}
	content := &runtimeContent{rights: r}
	allowed := false
	routes := router.New(router.NewHosted("replicate", "https://api.replicate.com", router.WireReplicatePredictions, "replicate"))
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) {
		return map[string]string{"Authorization": "Bearer fixture"}, nil
	})
	routes.Survey()
	provider, err := inference.New(inference.Config{Router: routes, Decide: r.decideInference, PrepareContentWrite: content.prepareWrite,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			if !allowed {
				t.Error("an ungranted job reached credential application")
				return nil, "not_permitted"
			}
			return map[string]string{"Authorization": "Bearer fixture"}, "applied"
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { provider.Close() })
	execution := inference.NewJobExecution(provider, func(context.Context, string, string) (inference.Subject, inference.ContentOutcome) {
		return inference.Subject{}, inference.ContentForbidden
	}, func(err error) { t.Error(err) })
	execution.SetPendingQuestions(r.pendingJobQuestions)
	executor := &runtimeInferenceJobExecutor{inference: execution}
	spec := jobwire.Encode(&jobwire.Document{Request: &jobwire.Request{Profile: jobwire.ProfileImageBatch, Image: jobwire.ImageRequest{
		Model: "black-forest-labs/flux-schnell", Mode: jobwire.ModeGenerate, Prompt: "frogs", Size: "1024x1024", Count: 1,
		Guarantees: []jobwire.RequestGuarantee{jobwire.RequestGuaranteeHostedAllowed}, Credential: "replicate", Extensions: map[string]string{},
	}}})
	program := filepath.Join(t.TempDir(), "oa-opencode-integration.exe")
	binding := acceptanceprovider.Binding{Scope: "agent-scope", Origin: "local",
		Subject: &acceptanceprovider.AuthenticatedSubject{AccountKind: "windows", Account: r.owner, Program: program}}
	submit := func() (string, string) {
		outcome, reason := executor.CheckSubjectAdmission(binding, inference.InferenceJobKind, spec, []string{inference.RecoverableUpstreamGuarantee})
		return outcome.String(), reason
	}
	outcome, reason := submit()
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
	if len(want) != 2 {
		t.Fatalf("want the complete and output:image questions about %s pending, got %+v", program, records)
	}
	if outcome != "forbidden" || !strings.HasPrefix(reason, "rights:not_granted output:forbidden ") || !slices.Equal(asks.QuestionsPending(reason), want) {
		t.Fatalf("first image job refusal %s %q must name pending questions %v", outcome, reason, want)
	}
	// A second submission joins the same two questions and names them again.
	if again, reason2 := submit(); again != "forbidden" || reason2 != reason {
		t.Fatalf("repeated refusal %s %q, want %q", again, reason2, reason)
	}
	// Without the lookup the refusal is the bare grant words, as before.
	execution.SetPendingQuestions(nil)
	if _, bare := submit(); bare != "rights:not_granted output:forbidden" {
		t.Fatalf("refusal without a pending lookup %q", bare)
	}
	// The person allows both questions, and the same submission is admitted.
	execution.SetPendingQuestions(r.pendingJobQuestions)
	a.rights = r
	operator := policyRules{policy: r.policy, by: rwire.Subject{Account: r.owner, Program: appProgram("Abstraction Panel")}, asks: a}
	for _, id := range want {
		record, found, err := book.ApplicationRecord(id)
		if err != nil || !found {
			t.Fatalf("question %s: %v", id, err)
		}
		metadata := askclient.RecordMetadata{Key: record.Key, About: record.About, Text: record.Text}
		if _, err := grants.AnswerQuestion(context.Background(), pendingQuestion{record: metadata, book: book}, operator, r.owner, id, "allow"); err != nil {
			t.Fatal(err)
		}
	}
	allowed = true
	if outcome, reason := submit(); outcome != "accepted" {
		t.Fatalf("submission after Allow %s %q", outcome, reason)
	}
}

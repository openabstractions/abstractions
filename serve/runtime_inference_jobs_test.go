package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentialsapi "github.com/openabstractions/abstraction-credentials/go"
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

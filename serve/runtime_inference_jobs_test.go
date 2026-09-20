package main

import (
	"context"
	"errors"
	"testing"
	"time"

	credentialsapi "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	jobwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/job"
	job "github.com/openabstractions/abstraction-job/go"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
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

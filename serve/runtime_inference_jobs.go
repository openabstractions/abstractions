package main

import (
	"context"
	"errors"
	"slices"

	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	download "github.com/openabstractions/abstraction-download/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	job "github.com/openabstractions/abstraction-job/go"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

// runtimeInferenceJobExecutor adds kind inference to the already recorded job
// execution profile. Every existing kind delegates byte-for-byte to base, so
// reopening a mature download root retains its preparation and result behavior.
type runtimeInferenceJobExecutor struct {
	base          acceptanceprovider.Executor
	inference     *inference.JobExecution
	legacyResolve inference.JobSubjectResolver
}

func composeInferenceJobs(base acceptanceprovider.Executor, runtime *runtimeInference, credentials *runtimeCredentials, report func(error)) acceptanceprovider.Executor {
	if base == nil || runtime == nil || credentials == nil {
		return base
	}
	resolve := func(_ context.Context, scope, credential string) (inference.Subject, inference.ContentOutcome) {
		var subject cwire.Subject
		var err error
		if credential == "" {
			subject, err = credentials.localScopeSubject(scope)
		} else {
			subject, err = credentials.scopeSubject(scope, credential)
		}
		if err == nil {
			return inference.Subject{Account: subject.Account, Program: subject.Program}, inference.ContentResolved
		}
		var refusal *download.CredentialError
		if errors.As(err, &refusal) && refusal.Outcome == "unavailable" {
			return inference.Subject{}, inference.ContentUnavailable
		}
		return inference.Subject{}, inference.ContentForbidden
	}
	execution := inference.NewJobExecution(runtime.provider, resolve, report)
	// A job that has not ended holds the edit input or first frame it names
	// (INF-V4, STO-T7).
	runtime.content.addInputHolder(execution.NamedInputs)
	return &runtimeInferenceJobExecutor{base: base, inference: execution, legacyResolve: resolve}
}

func (e *runtimeInferenceJobExecutor) Profile() string { return e.base.Profile() }

func (e *runtimeInferenceJobExecutor) Prepare(id, kind string, spec []byte) ([]byte, error) {
	if kind == inference.InferenceJobKind {
		return e.inference.Prepare(id, kind, spec)
	}
	return e.base.Prepare(id, kind, spec)
}

func (e *runtimeInferenceJobExecutor) PrepareScoped(scope, id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	if kind == inference.InferenceJobKind {
		return e.inference.PrepareScoped(scope, id, kind, spec, required)
	}
	if scoped, ok := e.base.(acceptanceprovider.ScopedPreparer); ok {
		return scoped.PrepareScoped(scope, id, kind, spec, required)
	}
	prepared, err := e.base.Prepare(id, kind, spec)
	return prepared, nil, err
}

// PrepareSubjectScoped keeps a native submitter's peer for the inference
// job's decisions (inference.JobExecution.PrepareForPeer); a remote binding
// and a binding rebuilt from a journal carry none.
func (e *runtimeInferenceJobExecutor) PrepareSubjectScoped(binding acceptanceprovider.Binding, id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	if kind == inference.InferenceJobKind {
		return e.inference.PrepareForPeer(binding.Scope, id, kind, spec, required, binding.Peer)
	}
	if scoped, ok := e.base.(acceptanceprovider.SubjectScopedPreparer); ok {
		return scoped.PrepareSubjectScoped(binding, id, kind, spec, required)
	}
	return e.PrepareScoped(binding.Scope, id, kind, spec, required)
}

func (e *runtimeInferenceJobExecutor) PrepareWithGuarantees(id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	if kind == inference.InferenceJobKind {
		return e.inference.PrepareWithGuarantees(id, kind, spec, required)
	}
	if guaranteed, ok := e.base.(acceptanceprovider.GuaranteedExecutor); ok {
		return guaranteed.PrepareWithGuarantees(id, kind, spec, required)
	}
	prepared, err := e.base.Prepare(id, kind, spec)
	return prepared, nil, err
}

// PrepareLegacy preserves the distinct migration origin. Falling back to the
// current preparer would reinterpret an old sink record and fail recovery.
func (e *runtimeInferenceJobExecutor) PrepareLegacy(id, kind string, spec []byte, required []string) ([]byte, []string, error) {
	if legacy, ok := e.base.(acceptanceprovider.LegacyPreparer); ok {
		return legacy.PrepareLegacy(id, kind, spec, required)
	}
	return nil, nil, errors.New("legacy preparation unavailable")
}

func appendDistinct(values []string, more ...string) []string {
	for _, value := range more {
		if value != "" && !slices.Contains(values, value) {
			values = append(values, value)
		}
	}
	return values
}

func (e *runtimeInferenceJobExecutor) ExecutionGuarantees() []string {
	var values []string
	if guaranteed, ok := e.base.(acceptanceprovider.GuaranteedExecutor); ok {
		values = append(values, guaranteed.ExecutionGuarantees()...)
	}
	return appendDistinct(values, e.inference.ExecutionGuarantees()...)
}

func (e *runtimeInferenceJobExecutor) RecoveryGuarantees() []string {
	var values []string
	if recoverable, ok := e.base.(acceptanceprovider.RecoverableExecutor); ok {
		values = append(values, recoverable.RecoveryGuarantees()...)
	}
	return appendDistinct(values, e.inference.RecoveryGuarantees()...)
}

func (e *runtimeInferenceJobExecutor) CheckAdmission(scope, kind string, spec []byte, required []string) (api.AcceptanceOutcome, string) {
	if kind == inference.InferenceJobKind {
		return e.inference.CheckAdmission(scope, kind, spec, required)
	}
	if checker, ok := e.base.(acceptanceprovider.AdmissionChecker); ok {
		return checker.CheckAdmission(scope, kind, spec, required)
	}
	return api.AcceptanceOutcomeAccepted, ""
}

func (e *runtimeInferenceJobExecutor) CheckSubjectAdmission(binding acceptanceprovider.Binding, kind string, spec []byte, required []string) (api.AcceptanceOutcome, string) {
	if binding.Subject == nil {
		return api.AcceptanceOutcomeUnavailable, "caller:unavailable"
	}
	if kind == inference.InferenceJobKind {
		return e.inference.CheckAdmissionForSubject(binding.Scope, kind, spec, required, inference.Subject{Account: binding.Subject.Account, Program: binding.Subject.Program}, binding.Peer)
	}
	if checker, ok := e.base.(acceptanceprovider.SubjectAdmissionChecker); ok {
		return checker.CheckSubjectAdmission(binding, kind, spec, required)
	}
	return e.CheckAdmission(binding.Scope, kind, spec, required)
}

func (e *runtimeInferenceJobExecutor) Serve(ctx context.Context, store job.Store) error {
	return serveInferenceWorkers(ctx,
		func(ctx context.Context) error { return e.base.Serve(ctx, store) },
		func(ctx context.Context) error { return e.inference.Serve(ctx, store) },
	)
}

func (e *runtimeInferenceJobExecutor) ServeWithSubjectLookup(ctx context.Context, store job.Store, lookup acceptanceprovider.SubjectLookup) error {
	e.inference.SetOperationSubjectResolver(func(ctx context.Context, operationID, scope, credential string) (inference.Subject, inference.ContentOutcome) {
		subject, found, err := lookup(operationID, scope)
		if err != nil || !found {
			return inference.Subject{}, inference.ContentUnavailable
		}
		if subject == nil {
			return e.legacyResolve(ctx, scope, credential)
		}
		return inference.Subject{Account: subject.Account, Program: subject.Program}, inference.ContentResolved
	})
	return serveInferenceWorkers(ctx,
		func(ctx context.Context) error {
			if base, ok := e.base.(acceptanceprovider.SubjectServingExecutor); ok {
				return base.ServeWithSubjectLookup(ctx, store, lookup)
			}
			return e.base.Serve(ctx, store)
		},
		func(ctx context.Context) error { return e.inference.Serve(ctx, store) },
	)
}

func serveInferenceWorkers(ctx context.Context, workers ...func(context.Context) error) error {
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsOut := make(chan error, 2)
	for _, worker := range workers {
		worker := worker
		go func() { errorsOut <- worker(workerCtx) }()
	}
	first := <-errorsOut
	cancel()
	for range workers[1:] {
		<-errorsOut
	}
	if ctx.Err() != nil {
		return nil
	}
	if first == nil {
		return errors.New("runtime job executor stopped")
	}
	return first
}

func (e *runtimeInferenceJobExecutor) DeriveLabel(kind string, spec []byte) string {
	if kind == inference.InferenceJobKind {
		return "Inference"
	}
	if labels, ok := e.base.(acceptanceprovider.LabelDeriver); ok {
		return labels.DeriveLabel(kind, spec)
	}
	return ""
}

func (e *runtimeInferenceJobExecutor) OperationWaiting(record *job.Record) string {
	if record.Kind == inference.InferenceJobKind {
		return e.inference.OperationWaiting(record)
	}
	if waiting, ok := e.base.(acceptanceprovider.WaitingReporter); ok {
		return waiting.OperationWaiting(record)
	}
	return ""
}

// RefusedByRights asks the kind's own executor whether a failed record
// ended only for a refused rights decision (abstraction-job JOB-A19).
func (e *runtimeInferenceJobExecutor) RefusedByRights(record *job.Record) bool {
	if record.Kind == inference.InferenceJobKind {
		return e.inference.RefusedByRights(record)
	}
	if reporter, ok := e.base.(acceptanceprovider.RightsRefusalReporter); ok {
		return reporter.RefusedByRights(record)
	}
	return false
}

func (e *runtimeInferenceJobExecutor) OperationFailure(record *job.Record) *api.WorkFailure {
	if record.Kind == inference.InferenceJobKind {
		return e.inference.OperationFailure(record)
	}
	if failures, ok := e.base.(acceptanceprovider.FailureReporter); ok {
		return failures.OperationFailure(record)
	}
	return nil
}

func (e *runtimeInferenceJobExecutor) ResultRetentionMs() int64 {
	retention := e.inference.ResultRetentionMs()
	if retained, ok := e.base.(acceptanceprovider.ResultRetainer); ok && retained.ResultRetentionMs() > retention {
		retention = retained.ResultRetentionMs()
	}
	return retention
}

// OperationResultName names the base kind's result file for a hard link
// (job CONTRACT.md JOB-A17). An inference job's result document is no file
// to link, and a base without names links nothing.
func (e *runtimeInferenceJobExecutor) OperationResultName(record *job.Record) (string, error) {
	if record.Kind == inference.InferenceJobKind {
		return "", acceptanceprovider.ErrResultNotNamed
	}
	if namer, ok := e.base.(acceptanceprovider.ResultNamer); ok {
		return namer.OperationResultName(record)
	}
	return "", acceptanceprovider.ErrResultNotNamed
}

func (e *runtimeInferenceJobExecutor) ReadOperationResult(root string, record *job.Record, offset, maxBytes int64) ([]byte, int64, error) {
	if record.Kind == inference.InferenceJobKind {
		return e.inference.ReadOperationResult(root, record, offset, maxBytes)
	}
	if reader, ok := e.base.(acceptanceprovider.ResultReader); ok {
		return reader.ReadOperationResult(root, record, offset, maxBytes)
	}
	return nil, 0, acceptanceprovider.ErrResultLost
}

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
	base      acceptanceprovider.Executor
	inference *inference.JobExecution
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
	return &runtimeInferenceJobExecutor{base: base, inference: inference.NewJobExecution(runtime.provider, resolve, report)}
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

func (e *runtimeInferenceJobExecutor) Serve(ctx context.Context, store job.Store) error {
	return serveInferenceWorkers(ctx,
		func(ctx context.Context) error { return e.base.Serve(ctx, store) },
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

func (e *runtimeInferenceJobExecutor) ReadOperationResult(root string, record *job.Record, offset, maxBytes int64) ([]byte, int64, error) {
	if record.Kind == inference.InferenceJobKind {
		return e.inference.ReadOperationResult(root, record, offset, maxBytes)
	}
	if reader, ok := e.base.(acceptanceprovider.ResultReader); ok {
		return reader.ReadOperationResult(root, record, offset, maxBytes)
	}
	return nil, 0, acceptanceprovider.ErrResultLost
}

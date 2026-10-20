package main

import (
	"context"
	"fmt"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// standingInference is inference.Config.Standing: it decides again, asking
// nobody, whether s may still complete on the model server named host and,
// for a credential this runtime applied, use that credential (inference
// CONTRACT.md INF-A6). A remote peer's operation is decided under the subject
// its certificate mapped to, so its revoke ends it the same way. The next
// Start of a refused program is what asks again.
func (c *runtimeCredentials) standingInference(ctx context.Context, s inference.Subject, host, credential string) (string, error) {
	bound := rwire.Subject{Account: s.Account, Program: s.Program}
	if refusal, err := standingWord("rights:", c.runtimeRights.decide(ctx, bound, inference.ActionComplete, inference.ResourceHost(host)).Outcome); refusal != "" || err != nil {
		return refusal, err
	}
	if credential == "" {
		return "", nil
	}
	refusal, err := standingWord("credential:", c.useDecision(ctx, bound, credentials.ResourceFor(credential)))
	if refusal != "" {
		refusal = "credential:not_permitted"
	}
	return refusal, err
}

// standingWord reads one decision as a standing answer: "" for permitted, the
// prefix and word for a refusal Start would refuse with, and an error for a
// decision that could not be obtained.
func standingWord(prefix string, outcome rwire.DecisionOutcome) (string, error) {
	switch outcome {
	case rwire.DecisionOutcomePermitted:
		return "", nil
	case rwire.DecisionOutcomeDenied, rwire.DecisionOutcomeNotGranted, rwire.DecisionOutcomeUnknownAction:
		return prefix + outcome.String(), nil
	}
	return "", fmt.Errorf("standing decision: %s", outcome)
}

// recheckInferenceOnChange has every committed rights change end the running
// inference operations it revoked before the edit replies (inference INF-A6,
// rights RIGHTS-R23). The provider's own clock ends what an expiry revoked.
// The returned function removes the hook.
func recheckInferenceOnChange(rights *runtimeRights, provider rechecker) func() {
	if rights == nil || rights.policy == nil || provider == nil {
		return func() {}
	}
	return rights.policy.OnChange(func() { provider.Recheck(context.Background()) })
}

// A rechecker decides what it keeps running again (inference.Provider).
type rechecker interface {
	Recheck(ctx context.Context)
}

// gateJobStanding has every accepted job decided again while it runs, asking
// nobody, as Submit decided it, and rechecks unfinished jobs on each committed
// rights change (abstraction-job JOB-A18, rights RIGHTS-R23). A download the
// account-wide download default admitted keeps running until the person turns
// the default off or Blocks that program. A runtime without rights decides
// nothing: its jobs wait, as its Submit refuses.
func gateJobStanding(o *host.Options, rights *runtimeRights) {
	if o.JobRoot == "" {
		return
	}
	o.JobStanding = host.JobStandingFromRights(rights.jobSubjectDecision)
	if rights != nil && rights.policy != nil {
		o.JobStandingChanges = rights.policy.OnChange
	}
}

// jobSubjectDecision is host.SubjectDecider over the in-process policy.
func (r *runtimeRights) jobSubjectDecision(ctx context.Context, account, program, action, resource string) (string, error) {
	if r == nil || r.policy == nil {
		return rwire.DecisionOutcomeUnavailable.String(), nil
	}
	return r.decide(ctx, rwire.Subject{Account: account, Program: program}, action, resource).Outcome.String(), nil
}

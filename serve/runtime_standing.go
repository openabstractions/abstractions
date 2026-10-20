package main

import (
	"context"
	"errors"
	"fmt"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	watch "github.com/openabstractions/abstraction-watch/go"
)

// standingInference is inference.Config.Standing: it decides again, asking
// nobody, whether s may still complete on the model server named host and,
// for a credential this runtime applied, use that credential (inference
// CONTRACT.md INF-A6). A remote peer's operation is decided under the subject
// its certificate mapped to, so its revoke ends it the same way. The next
// Start of a refused program is what asks again.
func (c *runtimeCredentials) standingInference(ctx context.Context, s inference.Subject, host, credential string) (string, error) {
	call, incoming := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall)
	if incoming || remote.ValidSubjectProgram(s.Program) {
		if !incoming || c == nil || c.runtimeRights == nil || !call.matchesSubject(c.runtimeRights.owner, s.Account, s.Program) {
			return "remote:unavailable", nil
		}
		if call.admit != nil {
			if err := call.admit(call.peer); errors.Is(err, errPairingBusy) {
				// A trusted state edit temporarily denies new input. It supplies no
				// withdrawal decision for already accepted work (INF-A6).
				return "", err
			}
		}
		check := func(action, resource, prefix string) (string, error) {
			admit := call.admit
			var admissionErr error
			if admit != nil {
				admit = func(peer remote.Peer) error {
					err := call.admit(peer)
					if errors.Is(err, errPairingBusy) {
						admissionErr = err
					}
					return err
				}
			}
			decision := c.runtimeRights.decideRemote(ctx, call.peer, admit, action, resource, call.minimum)
			// SubjectOf maps admission errors to Forbidden. Retain the busy
			// observation from this exact invocation even if a later read clears.
			if admissionErr != nil {
				return "", admissionErr
			}
			if decision.Outcome == rwire.DecisionOutcomePermitted && decision.PolicyRevision != "" {
				return "", nil
			}
			// An edit can acquire the state lock after the first admission
			// above. Preserve only an actual current contention observation;
			// the owner's post-unlock event repeats this complete decision.
			if call.admit != nil {
				if err := call.admit(call.peer); errors.Is(err, errPairingBusy) {
					return "", err
				}
			}
			if decision.Outcome == rwire.DecisionOutcomeForbidden {
				// An actual removed pairing is a withdrawal, even though no
				// policy revision accompanies the failed subject admission.
				return prefix + decision.Outcome.String(), nil
			}
			if decision.PolicyRevision == "" || !rightsclient.Evaluated(decision.Outcome) {
				return "", fmt.Errorf("standing decision: %s", decision.Outcome)
			}
			return prefix + decision.Outcome.String(), nil
		}
		if refusal, err := check(inference.ActionComplete, inference.ResourceHost(host), "rights:"); refusal != "" || err != nil {
			return refusal, err
		}
		if credential != "" {
			outcome := c.useDecision(ctx, rwire.Subject{Account: s.Account, Program: s.Program}, credentials.ResourceFor(credential))
			if refusal, err := standingWord("credential:", outcome); refusal != "" || err != nil {
				return refusal, err
			}
		}
		return "", nil
	}
	bound := rwire.Subject{Account: s.Account, Program: s.Program}
	var complete rwire.DecisionOutcome
	if declared := c.hostedServerCredential(host); declared != "" {
		// A hosted server is decided through its account, as Start decided
		// it (decideHostedUse), asking nobody.
		complete = c.decideHostedUse(ctx, bound, inference.ResourceHost(host), declared, false)
	} else {
		complete = c.runtimeRights.decide(ctx, bound, inference.ActionComplete, inference.ResourceHost(host)).Outcome
	}
	if refusal, err := standingWord("rights:", complete); refusal != "" || err != nil {
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
	switch {
	case outcome == rwire.DecisionOutcomePermitted:
		return "", nil
	case rightsclient.Evaluated(outcome):
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
	return recheckRightsOnChange(rights, provider.RecheckChecked)
}

// recheckRightsOnChange keeps the edit reply joined to its own checked pass.
// Removing the subscription alone cannot stop a callback already captured by
// a committed edit: Stop also cancels and joins that pass before owners close.
func recheckRightsOnChange(rights *runtimeRights, pass func(context.Context) error) func() {
	settler := watch.NewCheckedRecheck(context.Background(), settlePassBudget, pass)
	remove := rights.policy.OnChangeChecked(settler.RequestAndWait)
	return func() { remove(); settler.Stop() }
}

// A rechecker decides what it keeps running again (inference.Provider).
type rechecker interface {
	RecheckChecked(ctx context.Context) error
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

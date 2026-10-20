package main

import (
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"strings"
)

// These are native result facts, not a person's answer. Unknown service text
// is never copied into the page or mistaken for a refusal by the person.
type codexEnrollmentFailure struct{ outcome, reason string }

func (e *codexEnrollmentFailure) Error() string {
	return "The permission request could not be completed."
}
func codexEnrollmentFailureFor(result applications.AgentEnrollmentResult) error {
	outcome := applications.OutcomeUnknown.String()
	switch result.Outcome {
	case applications.OutcomeApplied, applications.OutcomePage, applications.OutcomeUnknown,
		applications.OutcomeConflict, applications.OutcomeUnavailable, applications.OutcomeForbidden, applications.OutcomeInvalid:
		outcome = result.Outcome.String()
	}
	reason := result.Reason
	switch reason {
	case "", "application", "state", "native_identity", "agent_owner", "registration", "unconfirmed", "revision", "question", "person_answer":
	default:
		if other, ok := strings.CutPrefix(reason, "enrollment:"); ok && validApplicationName(other) {
			reason = "other_enrollment"
		} else {
			reason = "unknown"
		}
	}
	return &codexEnrollmentFailure{outcome: outcome, reason: reason}
}

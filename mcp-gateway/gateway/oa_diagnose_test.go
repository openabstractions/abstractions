package gateway

import (
	"strings"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/client"
)

func TestDiagnosisExposesTypedFailureOnly(t *testing.T) {
	const secretBearingMessage = "provider echoed credential=secret-token prompt=private user data"
	got := diagnosisFromObservation(facade.ObservationResult{
		Outcome: facade.ObservationOutcomeObserved,
		Snapshot: &facade.OperationSnapshot{
			State:    facade.WorkStateFailed,
			Progress: facade.WorkProgress{Done: 1, Total: 3},
			Failure: &facade.WorkFailure{
				Classification: facade.FailureClassPermanent,
				Cause:          facade.FailureCauseCredential,
				Message:        secretBearingMessage,
			},
		},
	})
	if got.Outcome != "observed" || got.State != "failed" || got.FailureClass != "permanent" || got.FailureCause != "credential" || got.Guidance == "" {
		t.Fatalf("typed failure diagnosis missing: %+v", got)
	}
	if strings.Contains(got.Guidance, "secret-token") || strings.Contains(got.Guidance, "private user data") || strings.Contains(got.Guidance, secretBearingMessage) {
		t.Fatalf("diagnosis included provider failure text: %+v", got)
	}
}

func TestDiagnosisMapsUnknownProviderCauseToOther(t *testing.T) {
	got := diagnosisFromObservation(facade.ObservationResult{
		Outcome: facade.ObservationOutcomeObserved,
		Snapshot: &facade.OperationSnapshot{
			State: facade.WorkStateFailed,
			Failure: &facade.WorkFailure{
				Classification: facade.FailureClassUnknown,
				Cause:          facade.FailureCause("arbitrary provider text"),
				Message:        "arbitrary provider text containing sensitive details",
			},
		},
	})
	if got.FailureClass != "unknown" || got.FailureCause != "other" || strings.Contains(got.Guidance, "arbitrary provider text") {
		t.Fatalf("unknown failure data was not normalized: %+v", got)
	}
}

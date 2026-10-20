package gateway

import (
	"context"
	"slices"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
)

func TestQuestionsPendingReadsOnlyQuestionWords(t *testing.T) {
	got := QuestionsPending("rights:not_granted content:read:forbidden rights:question_pending:a rights:question_pending:b rights:question_pending:a rights:question_pending:")
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("ids %v", got)
	}
	if got := QuestionsPending("rights:not_granted"); len(got) != 0 {
		t.Fatalf("a refusal naming no question: %v", got)
	}
}

func TestWaitForAnswerEndsAtTheDeadlineWithThePendingObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	pending := askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending, Answer: &askswire.Answer{ID: "q1", Pending: true}}
	calls := 0
	got, err := WaitForAnswer(ctx, pending, func(ctx context.Context, waitMS int64) (askswire.PermissionObservation, error) {
		calls++
		if waitMS > 150 {
			t.Errorf("wait %d ms beyond the call's budget", waitMS)
		}
		<-ctx.Done()
		return askswire.PermissionObservation{}, ctx.Err()
	})
	if err != nil || got.Outcome != askswire.PermissionOutcomePending || calls != 1 {
		t.Fatalf("%+v %v calls=%d", got, err, calls)
	}
	answered := askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered, Answer: &askswire.Answer{ID: "q1", Option: "allow", Yes: true}}
	got, err = WaitForAnswer(context.Background(), pending, func(context.Context, int64) (askswire.PermissionObservation, error) { return answered, nil })
	if err != nil || got.Outcome != askswire.PermissionOutcomeAnswered {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestPermissionObservationCurrentPermittedOverridesHistoricalNever(t *testing.T) {
	old := &askswire.Answer{ID: "q1", Option: "never", Kept: true}
	current := askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, Answer: old, PolicyRevision: "later-panel-grant"}
	if got := permissionObservationVerdict(current); got != verdictAllowed {
		t.Fatalf("current permitted with historical never: %v", got)
	}
	if got := permissionObservationVerdict(askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: "current"}); got != verdictAllowed {
		t.Fatalf("current permitted without answer: %v", got)
	}
	if got := permissionObservationVerdict(askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered, Answer: old}); got != verdictBlocked {
		t.Fatalf("recorded never without current permission: %v", got)
	}
}

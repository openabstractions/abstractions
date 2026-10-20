package main

import (
	"slices"
	"strings"

	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Keep the exported DecisionError unchanged. Owning service adapters can
// retain this optional metadata beside the same evaluated refusal.
type firstUseRefusal struct {
	cause  error
	detail rights.RefusalDetail
}

func (e *firstUseRefusal) Error() string { return e.cause.Error() }
func (e *firstUseRefusal) Unwrap() error { return e.cause }
func (e *firstUseRefusal) PermissionRefusalReason() string {
	if e.detail.Source != rights.RefusalSourceRights || e.detail.Decision != rights.RefusalDecisionNotGranted || e.detail.Ask != rights.RefusalAskAvailable || e.detail.Question == "" || strings.ContainsAny(e.detail.Question, " \t\r\n") {
		return ""
	}
	return "rights:not_granted rights:question_pending:" + e.detail.Question
}

// questionsRefusal is a refusal for want of a grant that carries, in order,
// the first-use questions the refusing check raised or joined that are still
// pending about the caller. The receiving service names each as
// rights:question_pending:<id> after its own refusal words (INF-W11,
// RIGHTS-FLOW ruling 2); download admission reads PendingQuestions.
type questionsRefusal struct {
	cause     error
	questions []string
}

func (e *questionsRefusal) Error() string              { return e.cause.Error() }
func (e *questionsRefusal) Unwrap() error              { return e.cause }
func (e *questionsRefusal) PendingQuestions() []string { return slices.Clone(e.questions) }

// refusalNamingQuestions wraps cause with the questions among asked still
// pending about subject. A lookup that fails, or finds none pending, leaves
// cause bare.
func (r *runtimeRights) refusalNamingQuestions(cause error, subject rights.Subject, asked *inference.Asked) error {
	ids := asked.IDs()
	if r == nil || r.pendingQuestions == nil || len(ids) == 0 {
		return cause
	}
	pending, err := r.pendingQuestions(subject, ids)
	if err != nil || len(pending) == 0 {
		return cause
	}
	return &questionsRefusal{cause: cause, questions: pending}
}

func pendingFirstUseRefusal(cause error, action, question string) error {
	if question == "" {
		return cause
	}
	return &firstUseRefusal{cause: cause, detail: rights.RefusalDetail{Source: rights.RefusalSourceRights, Decision: rights.RefusalDecisionNotGranted, Ask: rights.RefusalAskAvailable, Action: action, Question: question}}
}

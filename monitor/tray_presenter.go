package main

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// presenterOperator is the public asks operator surface the native prompt
// uses (ASK-O2, ASK-O3, ASK-O7). The runtime binds the Panel's program from
// its own peer proof and authorizes it as an operator (ASK-O1); the presenter
// sends no identity or authority claim of its own.
type presenterOperator interface {
	askclient.DetailsReader
	ListQuestionsContext(ctx context.Context, cursor string, limit int64) (askclient.OperatorPage, error)
	AnswerQuestionIfPendingContext(ctx context.Context, id, option string) (askclient.OperatorDecision, error)
}

// presenterSession reports the interactive session the prompt is shown in.
// Revision advances on every lock, so a prompt shown across a lock and unlock
// is recognized as interrupted even when the session is unlocked again.
type presenterSession interface {
	Revision() uint64
	Unlocked() bool
}

// errPromptNotShown is a surface's report that the prompt was never shown,
// such as when the secure desktop, a screen saver or a disconnected session
// holds the input desktop at that moment. The question is deferred.
var errPromptNotShown = errors.New("the permission prompt was not shown: the session is not on its interactive desktop")

// presenterSurface shows one native prompt and blocks until the person
// answers or closes it. An empty option is a close, never an answer. A prompt
// that could not be shown returns errPromptNotShown. Dismiss closes the
// prompt that is open, if any.
type presenterSurface interface {
	Ask(trayQuestionPresentation) (option string, err error)
	Dismiss()
}

// questionPresenter owns the decisions of the native permission prompt:
// present only in an unlocked session, defer across a lock, re-read the
// question before answering (ASK-O6), and send the person's option through
// the public operator API.
type questionPresenter struct {
	session         presenterSession
	surface         presenterSurface
	operator        func(context.Context) (presenterOperator, error)
	choices         func(context.Context) (presenterChoiceOperator, error)
	choiceNow       func() time.Time
	permissions     func(context.Context) (presenterPermissionOperator, error)
	permissionRules func() grants.PermissionRuleOperator
	rules           func() grants.RuleOperator
	// launch starts the protected-operation helper after the person's once,
	// owned by the answered prompt's window; nil uses launchSystemAccess.
	// Tests replace it.
	launch  func(owner uintptr, operation, ticket string)
	account string
	// watchEvery is how often a shown question is re-read so a prompt for a
	// question answered or dismissed elsewhere closes. Zero disables it.
	watchEvery time.Duration
	budget     time.Duration
}

// presentOutcome is what became of one pending question.
type presentOutcome int

const (
	// presentDeferred: the session was locked before or while the prompt was
	// shown. Nothing was sent; the question is presented again after unlock.
	presentDeferred presentOutcome = iota
	// presentClosed: the person closed the prompt without choosing.
	presentClosed
	// presentStale: the question was dismissed, answered elsewhere or changed
	// while shown. Nothing was sent; Stale carries the operator outcome.
	presentStale
	// presentUnavailable: the question could not be re-read. Nothing was sent.
	presentUnavailable
	// presentAnswered: the option was sent; Answered carries the service's
	// typed decision and any first-use rule edit.
	presentAnswered
	// presentFailed: the native prompt could not be shown.
	presentFailed
)

type presentResult struct {
	Outcome       presentOutcome
	Stale         askclient.OperatorDecisionOutcome
	Answered      grants.Answered
	TypedAnswered *grants.PermissionAnswered
	// ClosedElsewhere is set when the prompt closed because the question
	// changed while it was open.
	ClosedElsewhere bool
	Err             error
}

// defaultPresenterOperator resolves the asks operator of the local runtime.
func defaultPresenterOperator(ctx context.Context) (presenterOperator, error) {
	return panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// present shows record when the session allows it and reports the outcome.
// It never sends an answer for a question the person did not see as it now is.
func (p *questionPresenter) present(ctx context.Context, record askclient.RecordMetadata) presentResult {
	if record.Key == asksTypedPermissionKey {
		return p.presentPermission(ctx, record)
	}
	if !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	revision := p.session.Revision()
	asker, checked := p.readShownQuestion(ctx, record)
	if checked.Outcome != presentAnswered {
		return checked
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	details := asker.Details
	prompt := trayQuestionViewForAsker(record, publisherOfRead(details.Question.Question).forPrompt(machineProgramTitler(ctx)), details.Permission, asker.Program)
	if len(prompt.Choices) == 0 {
		return presentResult{Outcome: presentFailed, Err: errors.New("the question offers no answer")}
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	var watching sync.WaitGroup
	var staleWhileShown bool
	var staleMu sync.Mutex
	if p.watchEvery > 0 {
		watching.Add(1)
		go func() {
			defer watching.Done()
			if p.watch(watchCtx, record, details.Permission) {
				staleMu.Lock()
				staleWhileShown = true
				staleMu.Unlock()
				p.surface.Dismiss()
			}
		}()
	}
	option, err := p.surface.Ask(prompt)
	stopWatch()
	watching.Wait()

	if p.session.Revision() != revision || !p.session.Unlocked() || errors.Is(err, errPromptNotShown) {
		return presentResult{Outcome: presentDeferred}
	}
	if err != nil {
		return presentResult{Outcome: presentFailed, Err: err}
	}
	staleMu.Lock()
	stale := staleWhileShown
	staleMu.Unlock()
	if option == "" {
		if stale {
			return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}
		}
		return presentResult{Outcome: presentClosed}
	}
	if !slices.Contains(record.Options, option) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeInvalid}
	}
	return p.answer(ctx, record, details.Permission, option, revision)
}

// readShownQuestion freezes the service-authored permission targets and recorded
// publisher before display. The client chooses legacy prose only when an old
// service explicitly refuses the details method. Unavailable or malformed
// current details never become an inferred permission request.
//
// It also reads the bound program that asked (asks ASK-O11), absent from an
// older service, so the prompt names the asker by the program it keeps.
func (p *questionPresenter) readShownQuestion(ctx context.Context, record askclient.RecordMetadata) (askclient.QuestionAsker, presentResult) {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	operator, err := p.operator(ctx)
	if err != nil {
		return askclient.QuestionAsker{}, presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable, Err: err}
	}
	asker, err := askclient.ReadAskerContext(ctx, operator, p.account, record.ID)
	read := asker.Details.Question.Question
	switch {
	case err != nil:
		return askclient.QuestionAsker{}, presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable, Err: err}
	case read.Outcome == askclient.OperatorQuestionOutcomeUnknown:
		return askclient.QuestionAsker{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeUnknown, ClosedElsewhere: true}
	case read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil:
		return askclient.QuestionAsker{}, presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable}
	case read.Record.Option != "" || !sameQuestionContent(record, *read.Record):
		return askclient.QuestionAsker{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}
	}
	if asker.Details.Permission != nil {
		permission := *asker.Details.Permission
		permission.Targets = slices.Clone(permission.Targets)
		asker.Details.Permission = &permission
	}
	return asker, presentResult{Outcome: presentAnswered}
}

// answer re-reads the question and sends option only when the question is
// still pending with the content the person saw (ASK-O6).
func (p *questionPresenter) answer(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest, option string, revision uint64) presentResult {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	operator, err := p.operator(ctx)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable, Err: err}
	}
	current, err := listPresenterQuestions(ctx, operator)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable, Err: err}
	}
	if outcome, ok := revalidateQuestion(shown, current); !ok {
		return presentResult{Outcome: presentStale, Stale: outcome}
	}
	asker, checked := p.readShownQuestion(ctx, shown)
	if checked.Outcome != presentAnswered {
		checked.ClosedElsewhere = false
		return checked
	}
	if !samePermissionTargets(permission, asker.Details.Permission) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict}
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	var rules grants.RuleOperator = questionRules()
	if p.rules != nil {
		rules = p.rules()
	}
	// grants answers only while the question is pending (ASK-O8): an answer
	// that landed elsewhere after the re-read is conflict and writes no rule.
	answered, err := grants.AnswerQuestion(ctx, operator, rules, p.account, shown.ID, option)
	if err == nil {
		p.afterAnswer(answered.Decision)
	}
	return presentResult{Outcome: presentAnswered, Answered: answered, Err: err}
}

// afterAnswer raises the platform's administrator prompt for the installed
// helper right after the person's once on a system-access question, and for
// nothing else (asks ASK-S1, facade FAC-H3).
func (p *questionPresenter) afterAnswer(decision askclient.OperatorDecision) {
	record := decision.Record
	if decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || record == nil || record.Key != asks.SystemSecurityLogKey || record.Option != "once" {
		return
	}
	launch := p.launch
	if launch == nil {
		launch = launchSystemAccess
	}
	var owner uintptr
	if surface, ok := p.surface.(interface{ OwnerWindow() uintptr }); ok {
		owner = surface.OwnerWindow()
	}
	launch(owner, protected.OperationSecurityLogPage, record.ID)
}

// watch re-reads the question until ctx ends and reports true once it is no
// longer pending as shown. A read that fails keeps the prompt open; the
// answer path re-reads again before sending.
func (p *questionPresenter) watch(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest) bool {
	ticker := time.NewTicker(p.watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
		asker, checked := p.readShownQuestion(ctx, shown)
		if ctx.Err() != nil || checked.Outcome == presentUnavailable {
			continue
		}
		if checked.Outcome != presentAnswered || !samePermissionTargets(permission, asker.Details.Permission) {
			return true
		}
	}
}

func samePermissionTargets(a, b *askclient.PermissionRequest) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return slices.Equal(a.Targets, b.Targets)
}

func (p *questionPresenter) callBudget() time.Duration {
	if p.budget > 0 {
		return p.budget
	}
	return trayAnswerBudget
}

// listPresenterQuestions reads every page of the current book. A gap restarts
// once from an empty cursor (ASK-O2); any other refusal is unavailable.
func listPresenterQuestions(ctx context.Context, operator presenterOperator) ([]askclient.RecordMetadata, error) {
	for attempt := 0; attempt < 2; attempt++ {
		var records []askclient.RecordMetadata
		cursor := ""
		gap := false
		for page := 0; page < 64; page++ {
			result, err := operator.ListQuestionsContext(ctx, cursor, 64)
			if err != nil {
				return nil, err
			}
			switch result.Outcome {
			case askclient.OperatorPageOutcomePage:
			case askclient.OperatorPageOutcomeGap:
				gap = true
			default:
				return nil, &presenterRefusal{outcome: result.Outcome.String()}
			}
			if gap {
				break
			}
			records = append(records, result.Records...)
			if result.Complete {
				return records, nil
			}
			cursor = result.Next
		}
		if !gap {
			return nil, &presenterRefusal{outcome: "incomplete"}
		}
	}
	return nil, &presenterRefusal{outcome: "gap"}
}

type presenterRefusal struct{ outcome string }

func (r *presenterRefusal) Error() string { return "question list refused: " + r.outcome }

// revalidateQuestion compares the question as shown with the current book.
// ok is true only when it is still pending with identical content. Otherwise
// the outcome is the operator's own word for the case: unknown for a question
// no longer retained, conflict for one answered elsewhere or changed.
func revalidateQuestion(shown askclient.RecordMetadata, current []askclient.RecordMetadata) (askclient.OperatorDecisionOutcome, bool) {
	for _, record := range current {
		if record.ID != shown.ID {
			continue
		}
		if record.Option != "" || !sameQuestionContent(shown, record) {
			return askclient.OperatorDecisionOutcomeConflict, false
		}
		return askclient.OperatorDecisionOutcomeAnswered, true
	}
	return askclient.OperatorDecisionOutcomeUnknown, false
}

func sameQuestionContent(a, b askclient.RecordMetadata) bool {
	return a.ID == b.ID && a.Asker == b.Asker && a.Key == b.Key && a.About == b.About &&
		a.Text == b.Text && a.Asked == b.Asked && slices.Equal(a.Options, b.Options)
}

// presentFresh presents newly pending questions one at a time and applies
// each report. A deferred question and every question after it are left
// unseen, so a poll after unlock presents them again. OpenQuestions opens
// the Questions page, and show displays a result; show returns false when
// the tray is ending. presentFresh returns false when ctx ended.
func presentFresh(ctx context.Context, fresh []askclient.RecordMetadata, present func(context.Context, askclient.RecordMetadata) presentResult,
	announcer *trayAnnouncer, openQuestions func(), show func(presenterReport) bool) bool {
	for i, record := range fresh {
		result := present(ctx, record)
		if ctx.Err() != nil {
			return false
		}
		if result.Err != nil {
			log.Printf("native question prompt: %v", result.Err)
		}
		report := presenterResultReport(result)
		if report.Requeue {
			for _, rest := range fresh[i:] {
				announcer.forget(rest.ID)
			}
			return true
		}
		if report.OpenQuestions {
			openQuestions()
		}
		if report.Show && !show(report) {
			return false
		}
	}
	return true
}

// presenterReport is what the tray shows after a prompt: a result dialog when
// Show is set, and the Questions page when OpenQuestions is set.
type presenterReport struct {
	Show          bool
	Title         string
	Instruction   string
	Content       string
	OpenQuestions bool
	// Requeue means the question should be presented again later.
	Requeue bool
}

// presenterResultReport reports failed or uncertain completion. A successful
// answer finishes at the question dialog; the requesting application retries
// its action after the answer.
func presenterResultReport(result presentResult) presenterReport {
	switch result.Outcome {
	case presentDeferred:
		return presenterReport{Requeue: true}
	case presentClosed:
		return presenterReport{}
	case presentFailed:
		return presenterReport{OpenQuestions: true}
	case presentStale:
		if result.ClosedElsewhere {
			return presenterReport{}
		}
		return presenterReport{Show: true, OpenQuestions: true,
			Title:       trayDisplayName + " — No answer sent",
			Instruction: "This question changed while it was open",
			Content:     trayStaleMessage(result.Stale) + " No answer was sent."}
	case presentUnavailable:
		return presenterReport{Show: true, OpenQuestions: true,
			Title:       trayDisplayName + " — No answer sent",
			Instruction: "The question service could not be reached",
			Content:     "No answer was sent. Review the question in the Panel."}
	}
	answered := result.Answered
	if typed := result.TypedAnswered; typed != nil {
		answered.Decision = typed.Decision
		if typed.RuleOutcome != "" || typed.Effect != nil && (typed.Edit == nil || typed.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied) {
			answered.RuleOutcome = "not_written"
		}
	}
	decision := answered.Decision
	if decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || decision.Record == nil {
		content := trayQuestionOutcomeMessage(decision.Outcome.String())
		if result.Err != nil {
			content = "The Panel could not confirm whether your answer was recorded. Review the current question before retrying."
		}
		return presenterReport{Show: true, OpenQuestions: true,
			Title: trayDisplayName + " — Review question", Instruction: "Review the current question", Content: content}
	}
	if result.Err != nil || answered.RuleOutcome != "" || answered.Rule != nil && (answered.Edit == nil || answered.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied) {
		return presenterReport{Show: true, OpenQuestions: true,
			Title:       trayDisplayName + " — Answer recorded",
			Instruction: "OpenAbstractions recorded your decision",
			Content:     "Your answer was recorded, but the related permission rule was not written. Review the Questions page before retrying."}
	}
	return presenterReport{}
}

func trayStaleMessage(outcome askclient.OperatorDecisionOutcome) string {
	switch outcome {
	case askclient.OperatorDecisionOutcomeUnknown:
		return "The question was dismissed while it was open."
	case askclient.OperatorDecisionOutcomeConflict:
		return "The question was answered elsewhere while it was open."
	case askclient.OperatorDecisionOutcomeInvalid:
		return "The chosen answer is no longer offered."
	}
	return "The question could not be checked."
}

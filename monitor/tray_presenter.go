package main

import (
	"context"
	"errors"
	"log"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

// presenterOperator is the public asks operator surface the native prompt
// uses (ASK-O2, ASK-O3, ASK-O7). The runtime binds the Panel's program from
// its own peer proof and authorizes it as an operator (ASK-O1); the presenter
// sends no identity or authority claim of its own.
type presenterOperator interface {
	askclient.DetailsReader
	ReadAttributedQuestionContext(context.Context, string) (askclient.AttributedQuestion, error)
	ListAllQuestionsContext(ctx context.Context) ([]askclient.RecordMetadata, error)
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

// presenterSurface shows one Book occurrence in the native question style and
// blocks until the person answers or closes it. An empty option is a close,
// never an answer. A prompt that could not be shown returns errPromptNotShown.
// Dismiss closes the prompt that is open, if any.
type presenterSurface interface {
	Ask(bookOccurrence) (option string, err error)
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
	launch        func(owner uintptr, operation, ticket string)
	programRepair func() error
	// notice posts the sentence that confirms an answer about an unsigned
	// program; nil uses postAnswerNotice. Tests replace it.
	notice func(ctx context.Context, title, body string) error
	// spend reads a credential's service and daily limit for the
	// first-spend question (first_spend_question.go); nil keeps the
	// credential's generic words. Tests replace it.
	spend              func(ctx context.Context, credential string) (spendLimit, bool)
	providerForms      providerFormFactory
	providerComplete   func(context.Context, askclient.RecordMetadata, *askclient.PermissionRequest) error
	permissionFailures chan<- map[string]string
	account            string
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
	// Requester is the asking app's title for a permission notice.
	Requester string
}

// defaultPresenterOperator resolves the asks operator of the local runtime.
func defaultPresenterOperator(ctx context.Context) (presenterOperator, error) {
	return panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// present shows record when the session allows it and reports the outcome.
// It never sends an answer for a question the person did not see as it now is.
func (p *questionPresenter) present(ctx context.Context, record askclient.RecordMetadata) presentResult {
	return p.presentProviderMode(ctx, record, false)
}

func (p *questionPresenter) presentProviderMode(ctx context.Context, record askclient.RecordMetadata, retryMode bool) presentResult {
	if err := ctx.Err(); err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	if record.Key == asksTypedPermissionKey {
		result := p.presentPermission(ctx, record)
		if p.permissionFailures != nil {
			if attrs := permissionCompletionFailure(result); attrs != nil {
				select {
				case p.permissionFailures <- attrs:
				default: // A busy Logging sink never delays the person's report.
				}
			}
		}
		return result
	}
	if !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	revision := p.session.Revision()
	var content askclient.QuestionContent
	var checked presentResult
	if retryMode {
		content, checked = p.readProviderRetryQuestion(ctx, record)
	} else {
		content, checked = p.readShownQuestion(ctx, record)
	}
	if checked.Outcome != presentAnswered {
		return checked
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	asker := content.Question
	details := asker.Details
	prompt := trayQuestionViewForContent(record, publisherOfRead(details.Question.Question).forPrompt(machineProgramTitler(ctx)), details.Permission, asker.Program, content.Content)
	prompt = firstSpendPrompt(ctx, prompt, details.Permission, p.spend)
	var providerAnswered *presentResult
	var providerPermission *presentResult
	var providerRecorded *presentResult
	if retryMode {
		providerRecorded = &presentResult{Outcome: presentAnswered, Answered: grants.Answered{Decision: askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: content.Question.Details.Question.Question.Record}}}
	}
	var providerFailure error
	var providerOwnGrant atomic.Bool
	providerQuestion := providerPairPermission(details.Permission)
	if providerQuestion && p.providerForms == nil {
		return presentResult{Outcome: presentUnavailable, Err: errProviderConnectionChanged}
	}
	if p.providerForms != nil {
		form, err := p.providerForms(ctx, details.Permission, func(ctx context.Context) error {
			if p.session.Revision() != revision || !p.session.Unlocked() {
				return errProviderConnectionChanged
			}
			if providerRecorded != nil {
				return p.recheckProviderAnswer(ctx, record, details.Permission, revision)
			} else {
				current, checked := p.readShownQuestion(ctx, record)
				if checked.Outcome != presentAnswered || !samePermissionTargets(details.Permission, current.Question.Details.Permission) {
					return errProviderConnectionChanged
				}
			}
			return nil
		}, func(ctx context.Context) error {
			providerOwnGrant.Store(true)
			var result presentResult
			if providerRecorded == nil {
				result = p.answer(ctx, record, details.Permission, "allow", revision, true)
				if result.Outcome == presentAnswered && result.Answered.Decision.Outcome == askclient.OperatorDecisionOutcomeAnswered && result.Answered.Decision.Record != nil && result.Answered.Decision.Record.ID == record.ID && result.Answered.Decision.Record.Option == "allow" {
					providerRecorded = &result
				}
			}
			if providerRecorded != nil {
				result = p.retryProviderAllow(ctx, record, details.Permission, revision)
			}
			if result.Err != nil || result.Outcome != presentAnswered || result.Answered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || result.Answered.Edit == nil || result.Answered.Edit.Outcome.String() != "applied" && result.Answered.RuleOutcome != grants.RuleCompleted {
				return errProviderConnectionChanged
			}
			providerPermission = &result
			return nil
		})
		if err != nil {
			return presentResult{Outcome: presentUnavailable, Err: err}
		}
		if providerQuestion && form == nil {
			return presentResult{Outcome: presentUnavailable, Err: errProviderConnectionChanged}
		}
		if form != nil {
			providerQuestion = true
			choices := slices.DeleteFunc(slices.Clone(prompt.Choices), func(choice trayQuestionChoice) bool { return choice.Option != "allow" && choice.Option != "refuse" })
			for i := range choices {
				if choices[i].Option == "refuse" {
					choices[i].Label = "Not now"
				}
			}
			prompt.Choices, prompt.Default, prompt.Dismiss = choices, "refuse", "refuse"
			allow := form.Allow
			form.Allow = func(windowCtx context.Context, candidate []byte, amount int64) error {
				if allow == nil {
					clear(candidate)
					return errProviderConnectionChanged
				}
				limited, cancel := context.WithTimeout(windowCtx, 30*time.Second)
				stop := context.AfterFunc(ctx, cancel)
				defer stop()
				defer cancel()
				err := allow(limited, candidate, amount)
				if err == nil && providerPermission != nil && p.providerComplete != nil {
					err = p.providerComplete(limited, record, details.Permission)
				}
				if err == nil && providerPermission != nil {
					providerAnswered = providerPermission
				}
				providerFailure = err
				return err
			}
			if provider, ok := providerForPermission(details.Permission); ok {
				label, _ := hostedcatalogue.Lookup(provider)
				words := spendTitle(machineProgramTitler(ctx).promptName(details.Permission.Targets[0].Program), label.Label)
				prompt.Instruction = words
				if prompt.Permission != nil {
					card := *prompt.Permission
					card.Question, card.Instruction = words, words
					prompt.Permission = &card
				}
			}
			prompt.CredentialForm = form
			if surface, ok := p.surface.(interface{ CredentialFormSupported() bool }); !ok || !surface.CredentialFormSupported() {
				prompt.CredentialForm = nil
				prompt.Content += "\n\nConnection is not available on this native surface yet. Allow is disabled."
				if prompt.Permission != nil {
					card := *prompt.Permission
					card.Scope += "\n\nConnection is not available on this native surface yet. Allow is disabled."
					prompt.Permission = &card
				}
				prompt.Choices = slices.DeleteFunc(slices.Clone(prompt.Choices), func(choice trayQuestionChoice) bool { return choice.Option == "allow" })
			}
		}
	}
	if len(prompt.Choices) == 0 {
		return presentResult{Outcome: presentFailed, Err: errors.New("the question offers no answer")}
	}
	occurrence, err := questionOccurrence(record, prompt)
	if err != nil {
		return presentResult{Outcome: presentFailed, Err: err}
	}

	watchCtx, stopWatch := context.WithCancel(ctx)
	var watching sync.WaitGroup
	var staleWhileShown bool
	var staleMu sync.Mutex
	if p.watchEvery > 0 {
		watching.Add(1)
		go func() {
			defer watching.Done()
			if p.watch(watchCtx, record, details.Permission, retryMode, revision) {
				// The protected provider workflow rechecks service state after
				// its own permission commit and owns its final display outcome.
				if providerOwnGrant.Load() && !retryMode {
					return
				}
				staleMu.Lock()
				staleWhileShown = true
				staleMu.Unlock()
				p.surface.Dismiss()
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		stopWatch()
		watching.Wait()
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	option, err := p.askPopup(ctx, occurrence)
	stopWatch()
	watching.Wait()
	if providerAnswered != nil {
		return *providerAnswered
	}
	if providerPermission != nil {
		return presentResult{Outcome: presentUnavailable, Err: providerFailure}
	}
	if providerRecorded != nil && providerFailure != nil {
		return presentResult{Outcome: presentUnavailable, Err: providerFailure}
	}
	if retryMode {
		return presentResult{Outcome: presentClosed}
	}
	if p.session.Revision() != revision || !p.session.Unlocked() || errors.Is(err, errPromptNotShown) {
		return presentResult{Outcome: presentDeferred}
	}
	if err != nil {
		return presentResult{Outcome: presentFailed, Err: err}
	}
	if providerQuestion && option != "" && !slices.ContainsFunc(prompt.Choices, func(choice trayQuestionChoice) bool { return choice.Option == option }) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeInvalid}
	}
	if providerQuestion && option == "allow" {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeForbidden}
	}
	staleMu.Lock()
	stale := staleWhileShown
	staleMu.Unlock()
	if option == trayNotNow && prompt.Dismiss == trayNotNow && !slices.Contains(record.Options, option) {
		option = ""
	}
	// The watcher closes the prompt when the system withdraws, replaces or
	// answers the question elsewhere. That close returns the prompt's Dismiss
	// option, the same value Escape gives, so it is no choice of the person:
	// the prompt closes silently and nothing is sent (R23, 2026-11-10: an
	// install withdrew "Update connections" and its "not_now" was reported
	// as an answer that could not be sent).
	if stale && (option == "" || option == prompt.Dismiss) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}
	}
	if option == "" {
		return presentResult{Outcome: presentClosed}
	}
	if !slices.Contains(record.Options, option) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeInvalid}
	}
	result := p.answer(ctx, record, details.Permission, option, revision)
	if details.Permission != nil && len(details.Permission.Targets) > 0 {
		result.Requester = machineProgramTitler(ctx).promptName(details.Permission.Targets[0].Program)
	}
	if record.Key == asks.ProgramReviewKey && option == "repair" && result.Outcome == presentAnswered &&
		result.Answered.Decision.Outcome == askclient.OperatorDecisionOutcomeAnswered && result.Answered.Decision.Record != nil &&
		result.Answered.Decision.Record.ID == record.ID && result.Answered.Decision.Record.Option == "repair" {
		open := p.programRepair
		if open == nil {
			open = launchProgramRepairPanel
		}
		if err := open(); err != nil {
			result.Err = err
		}
	}
	return result
}

func (p *questionPresenter) recheckProviderAnswer(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest, revision uint64) error {
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return errProviderConnectionChanged
	}
	operator, err := p.operator(ctx)
	if err != nil {
		return errProviderConnectionChanged
	}
	content, err := readQuestionContent(ctx, operator, p.account, shown.ID)
	read := content.Question.Details.Question
	if err != nil || read.Question.Outcome != askclient.OperatorQuestionOutcomeFound || read.Question.Record == nil || read.Question.Record.Option != "allow" || !sameQuestionContent(shown, *read.Question.Record) || !samePermissionTargets(permission, content.Question.Details.Permission) || read.Question.AnsweredBy != nil && *read.Question.AnsweredBy == askclient.AnswererResponder {
		return errProviderConnectionChanged
	}
	return nil
}

func (p *questionPresenter) readProviderRetryQuestion(ctx context.Context, shown askclient.RecordMetadata) (askclient.QuestionContent, presentResult) {
	operator, err := p.operator(ctx)
	if err != nil {
		return askclient.QuestionContent{}, presentResult{Outcome: presentUnavailable, Err: err}
	}
	content, err := readQuestionContent(ctx, operator, p.account, shown.ID)
	read := content.Question.Details.Question.Question
	if err != nil || read.Outcome == askclient.OperatorQuestionOutcomeUnavailable {
		return askclient.QuestionContent{}, presentResult{Outcome: presentUnavailable, Err: errProviderConnectionChanged}
	}
	if read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil || read.Record.Key != asks.FirstUseKey || read.Record.Option != "allow" || !sameQuestionContent(shown, *read.Record) || !providerPairPermission(content.Question.Details.Permission) || read.AnsweredBy != nil && *read.AnsweredBy == askclient.AnswererResponder {
		return askclient.QuestionContent{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true, Err: errProviderConnectionChanged}
	}
	permission := *content.Question.Details.Permission
	permission.Targets = slices.Clone(permission.Targets)
	content.Question.Details.Permission = &permission
	return content, presentResult{Outcome: presentAnswered}
}

func (p *questionPresenter) reopenProvider(ctx context.Context, id string) presentResult {
	operator, err := p.operator(ctx)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	content, err := readQuestionContent(ctx, operator, p.account, id)
	read := content.Question.Details.Question.Question
	if err != nil || read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil {
		return presentResult{Outcome: presentUnavailable, Err: errProviderConnectionChanged}
	}
	return p.presentProviderMode(ctx, *read.Record, true)
}

func (p *questionPresenter) retryProviderAllow(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest, revision uint64) presentResult {
	if err := p.recheckProviderAnswer(ctx, shown, permission, revision); err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	operator, err := p.operator(ctx)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	var rules grants.RuleOperator = questionRules()
	if p.rules != nil {
		rules = p.rules()
	}
	answered, err := grants.RetryFirstUseRule(ctx, operator, rules, p.account, shown.ID)
	return presentResult{Outcome: presentAnswered, Answered: answered, Err: err}
}

// readShownQuestion freezes the service-authored permission targets and recorded
// publisher before display. The client chooses legacy prose only when an old
// service explicitly refuses the details method. Unavailable or malformed
// current details never become an inferred permission request.
//
// It also reads the bound program that asked (asks ASK-O11), absent from an
// older service, so the prompt names the asker by the program it keeps.
func (p *questionPresenter) readShownQuestion(ctx context.Context, record askclient.RecordMetadata) (askclient.QuestionContent, presentResult) {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	operator, err := p.operator(ctx)
	if err != nil {
		return askclient.QuestionContent{}, presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable, Err: err}
	}
	content, err := readQuestionContent(ctx, operator, p.account, record.ID)
	asker := &content.Question
	read := asker.Details.Question.Question
	switch {
	case err != nil:
		return askclient.QuestionContent{}, presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable, Err: err}
	case read.Outcome == askclient.OperatorQuestionOutcomeUnknown:
		return askclient.QuestionContent{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeUnknown, ClosedElsewhere: true}
	case read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil:
		return askclient.QuestionContent{}, presentResult{Outcome: presentUnavailable, Stale: askclient.OperatorDecisionOutcomeUnavailable}
	case read.Record.Option != "" || !sameQuestionContent(record, *read.Record):
		return askclient.QuestionContent{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}
	}
	if asker.Details.Permission != nil {
		permission := *asker.Details.Permission
		permission.Targets = slices.Clone(permission.Targets)
		asker.Details.Permission = &permission
	}
	if content.Content != nil {
		retained := *content.Content
		retained.Slots = maps.Clone(retained.Slots)
		content.Content = &retained
	}
	return content, presentResult{Outcome: presentAnswered}
}

// answer re-reads the question and sends option only when the question is
// still pending with the content the person saw (ASK-O6).
func (p *questionPresenter) answer(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest, option string, revision uint64, recordProviderOnly ...bool) presentResult {
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
	content, checked := p.readShownQuestion(ctx, shown)
	if checked.Outcome != presentAnswered {
		checked.ClosedElsewhere = false
		return checked
	}
	if !samePermissionTargets(permission, content.Question.Details.Permission) {
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
	var answered grants.Answered
	if len(recordProviderOnly) > 0 && recordProviderOnly[0] && providerPairPermission(permission) && option == "allow" {
		var leased bool
		leased, err = beginPopupProviderCompletion(ctx, shown.ID)
		if !leased {
			answered.Decision, err = operator.AnswerQuestionIfPendingContext(ctx, shown.ID, option)
		} else if err == nil {
			shown.Option, shown.Yes = option, true
			answered.Decision = askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &shown}
		}
	} else {
		answered, err = grants.AnswerQuestion(ctx, operator, rules, p.account, shown.ID, option)
	}
	if err == nil {
		p.afterAnswer(answered.Decision)
		if answered.Decision.Outcome == askclient.OperatorDecisionOutcomeAnswered && answered.Decision.Record != nil && answered.Decision.Record.ID == shown.ID {
			read := content.Question.Details.Question.Question
			if body, ok := unsignedAnswerNotice(shown, publisherOfRead(read), content.Content, option); ok {
				notice := p.notice
				if notice == nil {
					notice = postAnswerNotice
				}
				if noticeErr := notice(ctx, trayDisplayName, body); noticeErr != nil {
					log.Printf("answer notice: %v", noticeErr)
				}
			}
		}
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
func (p *questionPresenter) watch(ctx context.Context, shown askclient.RecordMetadata, permission *askclient.PermissionRequest, retryProvider bool, revision uint64) bool {
	ticker := time.NewTicker(p.watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
		if !p.session.Unlocked() || p.session.Revision() != revision {
			return true
		}
		var content askclient.QuestionContent
		var checked presentResult
		if retryProvider {
			content, checked = p.readProviderRetryQuestion(ctx, shown)
		} else {
			content, checked = p.readShownQuestion(ctx, shown)
		}
		if ctx.Err() != nil {
			return false
		}
		if retryProvider && checked.Outcome == presentUnavailable {
			return true
		}
		if checked.Outcome == presentUnavailable {
			continue
		}
		if checked.Outcome != presentAnswered || !samePermissionTargets(permission, content.Question.Details.Permission) {
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

// listPresenterQuestions reads every page of the current book through the
// asks client's ListAllQuestionsContext: one gap restart (ASK-O2), and a
// stalled cursor or any other refusal is unavailable.
func listPresenterQuestions(ctx context.Context, operator presenterOperator) ([]askclient.RecordMetadata, error) {
	return presenterList(operator.ListAllQuestionsContext(ctx))
}

// presenterList turns the client's list refusal into the presenter refusal.
func presenterList[R any](records []R, err error) ([]R, error) {
	var refusal *askclient.PageRefusal
	if errors.As(err, &refusal) {
		return nil, &presenterRefusal{outcome: refusal.Outcome}
	}
	return records, err
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
// the Questions page in the Panel window. presentFresh returns false when ctx
// ended.
func presentFresh(ctx context.Context, fresh []askclient.RecordMetadata, present func(context.Context, askclient.RecordMetadata) presentResult,
	announcer *trayAnnouncer, openQuestions func()) bool {
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
		if report.Notice != nil {
			if err := postPresenterNotice(ctx, *report.Notice); err != nil {
				log.Printf("presenter notice: %v", err)
			}
		}
	}
	return true
}

// presenterReport is what follows a prompt: the Questions page in the Panel
// window when OpenQuestions is set, or one OS notification. The native
// question style shows Book occurrences only and never a report.
type presenterReport struct {
	OpenQuestions bool
	// Requeue means the question should be presented again later.
	Requeue bool
	// Notice is the one OS notification posted in place of a popup.
	Notice *presenterNotice
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
		// No answer was sent; the Questions page shows the question as it
		// now is.
		return presenterReport{OpenQuestions: true}
	case presentUnavailable:
		if errors.Is(result.Err, errProviderPermissionRemoved) {
			// An Activity line, never a popup (architect popup ruling
			// 2026-11-10, row 28).
			return presenterReport{}
		}
		var native interface{ NativeCredentialMessage() string }
		if errors.As(result.Err, &native) {
			// Setup the person started didn't finish: one notification
			// that opens Setup (row 28).
			return presenterReport{Notice: &presenterNotice{Title: "Catalog didn't finish", Body: native.NativeCredentialMessage(), Action: noticeOpenSetup}}
		}
		// No answer was sent; the Questions page shows the service state.
		return presenterReport{OpenQuestions: true}
	}
	// Architect popup ruling 2026-11-10, rows 29 to 32: a confirmation, an
	// echo of the answer and a changed question close silently; the
	// Questions page shows the current state. A permission the person
	// allowed that was not saved is one notification: the app asks again.
	if notice, ok := permissionNotSavedReport(result); ok {
		return notice
	}
	answered := result.Answered
	if result.Err == nil && answered.RuleOutcome == grants.RuleCompleted {
		return presenterReport{}
	}
	unconfirmedEffect := result.Err != nil
	if typed := result.TypedAnswered; typed != nil {
		answered.Decision = typed.Decision
		unconfirmedEffect = typedPermissionEffectUnconfirmed(*typed, result.Err)
		if typed.RuleOutcome != "" || typed.Effect != nil && (typed.Edit == nil || typed.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied) {
			answered.RuleOutcome = "not_written"
		}
	}
	decision := answered.Decision
	if decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || decision.Record == nil {
		return presenterReport{}
	}
	notWritten := result.Err != nil || answered.RuleOutcome != "" || answered.Rule != nil && (answered.Edit == nil || answered.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied)
	unconfirmed := unconfirmedEffect || answered.RuleOutcome == "unavailable" || answered.Edit != nil && answered.Edit.Outcome == rightsclient.PolicyEditOutcomeUnavailable
	if notWritten && !unconfirmed && (decision.Record.Yes || decision.Record.Option == "allow") {
		return permissionNotSavedNotice(result.Requester)
	}
	return presenterReport{}
}

// permissionNotSavedReport is the one notice after an Allow whose permission
// its owner could not save and released: the app asks again.
func permissionNotSavedReport(result presentResult) (presenterReport, bool) {
	notSaved := result.Answered.NotSaved
	if typed := result.TypedAnswered; typed != nil {
		notSaved = typed.NotSaved
	}
	if !notSaved {
		return presenterReport{}, false
	}
	return permissionNotSavedNotice(result.Requester), true
}

// permissionNotSavedNotice is "Couldn't save that permission" / "<App> will
// ask again." as one OS notification (architect popup ruling 2026-11-10,
// rows 31 and 32).
func permissionNotSavedNotice(app string) presenterReport {
	if app == "" {
		app = "The app"
	}
	return presenterReport{Notice: &presenterNotice{Title: grants.PermissionNotSavedTitle, Body: grants.PermissionNotSavedBody(app)}}
}

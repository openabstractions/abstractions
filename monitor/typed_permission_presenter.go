package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

const asksTypedPermissionKey = asks.TypedPermissionKey

func typedPermissionEffectUnconfirmed(answered grants.PermissionAnswered, err error) bool {
	return err != nil || answered.RuleOutcome == "unavailable" ||
		answered.Edit != nil && answered.Edit.Outcome == rightsclient.PolicyEditOutcomeUnavailable
}

type presenterPermissionOperator interface {
	ReadPermissionDetailsContext(context.Context, string) (askclient.TypedPermissionDetails, error)
	ListPermissionsContext(context.Context, string, int64) (askclient.OperatorPage, error)
	ListAllPermissionsContext(context.Context) ([]askclient.RecordMetadata, error)
	AnswerPermissionIfPendingContext(context.Context, string, string) (askclient.OperatorDecision, error)
}

func defaultPresenterPermissions(ctx context.Context) (presenterPermissionOperator, error) {
	return panelMachine().ResolveAsksPermissionOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

func (p *questionPresenter) permissionOperator(ctx context.Context) (presenterPermissionOperator, error) {
	if p.permissions != nil {
		return p.permissions(ctx)
	}
	return defaultPresenterPermissions(ctx)
}

// Resolve the rule owner only if the recorded answer needs an effect. A refusal
// remains an Asks decision, and carries no caller-built Rights selector.
type panelPermissionRules struct{ operator grants.PermissionRuleOperator }

func (r *panelPermissionRules) resolve(ctx context.Context) (grants.PermissionRuleOperator, error) {
	if r.operator == nil {
		operator, err := panelMachine().ResolveRightsSelectorOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return nil, err
		}
		r.operator = operator
	}
	return r.operator, nil
}

func (r *panelPermissionRules) ListSelectorRulesContext(ctx context.Context, cursor string, limit int64) (rightsclient.SelectorRulePage, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.SelectorRulePage{}, err
	}
	return operator.ListSelectorRulesContext(ctx, cursor, limit)
}

func (r *panelPermissionRules) ApplyPermissionAllowContext(ctx context.Context, expected, id string) (rightsclient.SelectorEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.SelectorEdit{}, err
	}
	return operator.ApplyPermissionAllowContext(ctx, expected, id)
}

func (r *panelPermissionRules) UndoPermissionEffectContext(ctx context.Context, expected, id string) (rightsclient.SelectorEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.SelectorEdit{}, err
	}
	undo, ok := operator.(interface {
		UndoPermissionEffectContext(context.Context, string, string) (rightsclient.SelectorEdit, error)
	})
	if !ok {
		return rightsclient.SelectorEdit{}, errors.New("rights: permission Undo unavailable")
	}
	return undo.UndoPermissionEffectContext(ctx, expected, id)
}

// The strict public reader validates the full effect, digest, display alignment
// and receiving account. This route never attempts a legacy question method.
func (p *questionPresenter) readShownPermission(ctx context.Context, shown askclient.RecordMetadata) (askclient.TypedPermissionDetails, presentResult) {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	operator, err := p.permissionOperator(ctx)
	if err != nil {
		return askclient.TypedPermissionDetails{}, presentResult{Outcome: presentUnavailable, Err: err}
	}
	details, err := readTypedPermissionDetails(ctx, operator, p.account, shown.ID)
	read := details.Question.Question
	if err != nil {
		return askclient.TypedPermissionDetails{}, presentResult{Outcome: presentUnavailable, Err: err}
	}
	if read.Outcome == askclient.OperatorQuestionOutcomeUnknown {
		return askclient.TypedPermissionDetails{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeUnknown, ClosedElsewhere: true}
	}
	if read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil {
		return askclient.TypedPermissionDetails{}, presentResult{Outcome: presentUnavailable}
	}
	if read.Record.Option != "" || !sameQuestionContent(shown, *read.Record) {
		return askclient.TypedPermissionDetails{}, presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}
	}
	return details, presentResult{Outcome: presentAnswered}
}

func (p *questionPresenter) presentPermission(ctx context.Context, record askclient.RecordMetadata) presentResult {
	record.Options = slices.Clone(record.Options)
	if !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	revision := p.session.Revision()
	shown, checked := p.readShownPermission(ctx, record)
	if checked.Outcome != presentAnswered {
		return checked
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	names := machineProgramTitler(ctx)
	prompt := trayTypedPermissionView(record, shown, names)
	if len(prompt.Choices) == 0 {
		return presentResult{Outcome: presentFailed, Err: errors.New("the typed permission cannot be presented")}
	}
	occurrence, err := questionOccurrence(record, prompt)
	if err != nil {
		return presentResult{Outcome: presentFailed, Err: err}
	}
	watchCtx, stopWatch := context.WithCancel(ctx)
	var watching sync.WaitGroup
	var mu sync.Mutex
	stale := false
	if p.watchEvery > 0 {
		watching.Add(1)
		go func() {
			defer watching.Done()
			if p.watchPermission(watchCtx, record, shown) {
				mu.Lock()
				stale = true
				mu.Unlock()
				p.surface.Dismiss()
			}
		}()
	}
	option, err := p.askPopup(ctx, occurrence)
	stopWatch()
	watching.Wait()
	if p.session.Revision() != revision || !p.session.Unlocked() || errors.Is(err, errPromptNotShown) {
		return presentResult{Outcome: presentDeferred}
	}
	if err != nil {
		return presentResult{Outcome: presentFailed, Err: err}
	}
	mu.Lock()
	interrupted := stale
	mu.Unlock()
	if interrupted {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}
	}
	if option == "" {
		return presentResult{Outcome: presentClosed}
	}
	if !slices.Contains(record.Options, option) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeInvalid}
	}
	result := p.answerPermission(ctx, record, shown, option, revision)
	result.Requester = permissionRequesterTitle(names, shown)
	return result
}

// permissionRequesterTitle is the asking app's title: a message question's
// sender, else the effect's subject program.
func permissionRequesterTitle(names *programTitler, shown askclient.TypedPermissionDetails) string {
	if shown.Effect == nil {
		return ""
	}
	if _, captured, ok := grants.AgentMessageLegacyEffect(*shown.Effect); ok {
		sender, _ := messagePermissionTitles(names, captured)
		return sender
	}
	return names.promptName(shown.Effect.Subject.Program)
}

// messagePermissionAnsweredSentence is what the person's click did to a
// message question, in its own titles; "" for any other permission.
func messagePermissionAnsweredSentence(names *programTitler, shown askclient.TypedPermissionDetails, option string) string {
	if shown.Effect == nil {
		return ""
	}
	_, captured, ok := grants.AgentMessageLegacyEffect(*shown.Effect)
	if !ok {
		return ""
	}
	sender, receivers := messagePermissionTitles(names, captured)
	return grants.MessagePermissionAnswered(sender, receivers, option == "allow")
}

func (p *questionPresenter) answerPermission(ctx context.Context, record askclient.RecordMetadata, shown askclient.TypedPermissionDetails, option string, revision uint64) presentResult {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	current, checked := p.readShownPermission(ctx, record)
	if checked.Outcome != presentAnswered {
		checked.ClosedElsewhere = false
		return checked
	}
	if !reflect.DeepEqual(shown, current) {
		return presentResult{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict}
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	operator, err := p.permissionOperator(ctx)
	if err != nil {
		return presentResult{Outcome: presentUnavailable, Err: err}
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return presentResult{Outcome: presentDeferred}
	}
	var rules grants.PermissionRuleOperator = &panelPermissionRules{}
	if p.permissionRules != nil {
		rules = p.permissionRules()
	}
	answered, err := grants.AnswerPermissionQuestion(ctx, operator, rules, p.account, record.ID, option)
	return presentResult{Outcome: presentAnswered, TypedAnswered: &answered, Err: err}
}

func (p *questionPresenter) watchPermission(ctx context.Context, record askclient.RecordMetadata, shown askclient.TypedPermissionDetails) bool {
	ticker := time.NewTicker(p.watchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-ticker.C:
		}
		current, checked := p.readShownPermission(ctx, record)
		if ctx.Err() != nil || checked.Outcome == presentUnavailable {
			continue
		}
		if checked.Outcome != presentAnswered || !reflect.DeepEqual(shown, current) {
			return true
		}
	}
}

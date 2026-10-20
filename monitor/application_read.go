package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// Registration grants nobody read or activation on the registered entry
// (openabstractions-flat/abstraction-facade/APPLICATIONS.md). The person's
// confirmed Register click in the Panel also lets the Panel list and open what
// the person registered: the Panel writes two permit rules in one conditional
// edit, application read and application activate on app:<name> for its own
// program, through the rights service as the rules' writer. A later revoke
// stays revoked until the person confirms another registration of that name;
// a confirmed Remove clears both rules.

const (
	// applicationReadAction is the Applications service's Observe permission.
	applicationReadAction = "abstraction.facade/application.read"
	// applicationActivateAction is its Activate permission, which the Apps
	// page's Open uses.
	applicationActivateAction = "abstraction.facade/application.activate"
	// applicationReadWhy is the reason the rule records.
	applicationReadWhy = "registered by the person in the Panel"
	// applicationReadAttempts bounds retries after another writer moved the
	// policy revision between the read and the conditional edit.
	applicationReadAttempts = 3
)

// applicationResource is the rights resource of one registered name.
func applicationResource(name string) string { return "app:" + name }

// applicationReadRules writes and clears the Panel's own read rule for one
// registered name. servicePanel.appReadRules replaces it in tests.
type applicationReadRules interface {
	allowPanelRead(ctx context.Context, name string) error
	clearPanelRead(ctx context.Context, name string) error
}

// applicationReads is the rule writer the confirm handlers use.
func (p *servicePanel) applicationReads() applicationReadRules {
	if p.appReadRules != nil {
		return p.appReadRules
	}
	return panelApplicationReads{}
}

// applicationReadOperator is the rights operator surface the rule needs.
type applicationReadOperator interface {
	ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error)
	ReadRuleContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error)
	SetRulesForContext(ctx context.Context, expected string, rules []rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error)
	RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error)
}

// panelApplicationReads writes through the runtime's rights operator for the
// subject the runtime observes on the Panel's own connection, the subject the
// Applications service decides Observe for.
type panelApplicationReads struct{}

func (panelApplicationReads) resolve(ctx context.Context) (applicationReadOperator, rightsclient.Subject, error) {
	machine := panelMachine()
	caller, err := machine.ObserveCaller(ctx)
	if err != nil {
		return nil, rightsclient.Subject{}, err
	}
	if caller.Outcome != fwire.CallerOutcomeObserved || caller.Account == "" || caller.Program == "" {
		return nil, rightsclient.Subject{}, errors.New("the runtime did not name the Panel's own account and program")
	}
	operator, err := machine.ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, rightsclient.Subject{}, err
	}
	return operator, rightsclient.Subject{Account: caller.Account, Program: caller.Program}, nil
}

func (r panelApplicationReads) allowPanelRead(ctx context.Context, name string) error {
	operator, subject, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	return allowApplicationRead(ctx, operator, subject, name)
}

func (r panelApplicationReads) clearPanelRead(ctx context.Context, name string) error {
	operator, subject, err := r.resolve(ctx)
	if err != nil {
		return err
	}
	return clearApplicationRead(ctx, operator, subject, name)
}

// panelEntryActions are the two permissions a confirmed registration gives the
// Panel on the entry: list it and open it.
var panelEntryActions = []string{applicationReadAction, applicationActivateAction}

// allowApplicationRead sets both permits in one edit at the current revision,
// retrying only when another edit moved the revision first.
func allowApplicationRead(ctx context.Context, operator applicationReadOperator, subject rightsclient.Subject, name string) error {
	rules := make([]rightsclient.PolicyRule, 0, len(panelEntryActions))
	for _, action := range panelEntryActions {
		rules = append(rules, rightsclient.PolicyRule{Subject: subject, Action: action, Resource: applicationResource(name), Permit: true})
	}
	for attempt := 0; attempt < applicationReadAttempts; attempt++ {
		page, err := operator.ListPolicyContext(ctx, "", 1)
		if err != nil {
			return err
		}
		if page.Outcome != rightsclient.PolicyPageOutcomePage {
			return fmt.Errorf("the permission list is %s", page.Outcome)
		}
		edit, err := operator.SetRulesForContext(ctx, page.Revision, rules, 0, applicationReadWhy)
		if err != nil {
			return err
		}
		switch edit.Outcome {
		case rightsclient.PolicyEditOutcomeApplied:
			return nil
		case rightsclient.PolicyEditOutcomeConflict:
			continue
		default:
			return fmt.Errorf("the permission service answered %s", edit.Outcome)
		}
	}
	return errors.New("the permissions kept changing while the Panel wrote its rule")
}

// clearApplicationRead revokes the Panel's read and activate rules on the
// name, whichever way each decides; a name without a rule is already clear.
func clearApplicationRead(ctx context.Context, operator applicationReadOperator, subject rightsclient.Subject, name string) error {
	for _, action := range panelEntryActions {
		if err := clearApplicationRule(ctx, operator, subject, action, name); err != nil {
			return err
		}
	}
	return nil
}

func clearApplicationRule(ctx context.Context, operator applicationReadOperator, subject rightsclient.Subject, action, name string) error {
	for attempt := 0; attempt < applicationReadAttempts; attempt++ {
		read, err := operator.ReadRuleContext(ctx, subject, action, applicationResource(name))
		if err != nil {
			return err
		}
		switch read.Outcome {
		case rightsclient.RuleReadOutcomeUnknown:
			return nil
		case rightsclient.RuleReadOutcomeFound, rightsclient.RuleReadOutcomeExpired:
		default:
			return fmt.Errorf("the permission service answered %s", read.Outcome)
		}
		edit, err := operator.RevokeRuleContext(ctx, read.Revision, subject, action, applicationResource(name))
		if err != nil {
			return err
		}
		switch edit.Outcome {
		case rightsclient.PolicyEditOutcomeApplied:
			return nil
		case rightsclient.PolicyEditOutcomeConflict:
			continue
		default:
			return fmt.Errorf("the permission service answered %s", edit.Outcome)
		}
	}
	return errors.New("the permissions kept changing while the Panel removed its rule")
}

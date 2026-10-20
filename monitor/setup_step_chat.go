package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The setup step's chat permission (owner, 2026-11-01: "yes setup should
// also grant model that we included"; VISION 2026-11-01). Setup's one
// answered question lets each app it sets up for code chat with the coding
// model Setup included, and nothing beyond it. The rule is complete on the
// model host's inference host only: the rights service decides complete per
// host (inference.ResourceHost), so no narrower model scope exists. It
// names the app's confirmed registration's program, the subject the
// first-use question would name, and is written with reason
// setupChatProvenance after the model is in place, never at install and
// never on Not now.

// setupChatProvenance is the reason the rights service records for the
// chat permission Setup writes (RuleRecord.Why); Activity lists Setup's
// permissions by it, with Undo.
const setupChatProvenance = "setup"

// chatPermissionRules is the one rule the chat permission writes.
func chatPermissionRules() []setup.ApprovedRule {
	return []setup.ApprovedRule{{Action: inference.ActionComplete, Resource: inference.ResourceHost(setup.ChatHost)}}
}

// stepChatOwners is what the chat permission reads and writes; tests
// replace each.
type stepChatOwners struct {
	apps func(context.Context) (setupApplications, error)
	// grant settles rules for program (writeSetupChatRules); previous are
	// the rules Setup wrote before for the same answer.
	grant func(ctx context.Context, program string, rules, previous []setup.ApprovedRule) ([]setup.ApprovedRule, error)
	// signer is the publisher the Panel verifies on a program file
	// (officialSigner).
	signer func(program string) (string, bool)
}

func (p *servicePanel) stepChat() *setup.StepChat {
	return stepChatOwners{apps: p.setupApplicationsForPanel, grant: setupChatGrant, signer: officialSigner}.bind()
}

// setupChatRules is the rights operator surface the chat permission reads
// and writes, one exact rule at a time (grants.ProductRemover).
type setupChatRules = grants.ProductRemover

// setupChatRights resolves the rights operator and this Panel's own
// subject as the runtime observes it, the setter the rights service records
// for the Panel's edits. Tests replace it.
var setupChatRights = func(ctx context.Context) (setupChatRules, rightsclient.Subject, error) {
	caller, err := panelMachine().ObserveCaller(ctx)
	if err != nil || caller.Outcome.String() != "observed" || caller.Account == "" || caller.Program == "" {
		return nil, rightsclient.Subject{}, errors.New("the Panel's own account could not be verified")
	}
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, rightsclient.Subject{}, err
	}
	return operator, rightsclient.Subject{Account: caller.Account, Program: caller.Program}, nil
}

// setupChatGrant settles the chat permission through the rights operator.
var setupChatGrant = func(ctx context.Context, program string, rules, previous []setup.ApprovedRule) ([]setup.ApprovedRule, error) {
	ops, self, err := setupChatRights(ctx)
	if err != nil {
		return nil, err
	}
	return writeSetupChatRules(ctx, ops, self, rightsclient.Subject{Account: self.Account, Program: program}, rules, previous)
}

// setupWrote reports a standing permit this Panel set with Setup's
// provenance (grants.ProductWrote): the only rule Setup may count as its
// own, list with Undo or remove.
func setupWrote(record *rightsclient.RuleRecord, self rightsclient.Subject) bool {
	return grants.ProductWrote(record, self, setupChatProvenance)
}

// writeSetupChatRules settles rules for subject as product grants
// (grants.WriteProduct) and answers the rules Setup holds afterwards: those
// it wrote now, and those it wrote before for the same answer. A rule the
// person holds stays exactly as they set it. A previous rule the offer no
// longer names (the model's host changed) is removed while it is still
// Setup's own (grants.RemoveProduct).
func writeSetupChatRules(ctx context.Context, ops setupChatRules, self, subject rightsclient.Subject, rules, previous []setup.ApprovedRule) ([]setup.ApprovedRule, error) {
	result, err := grants.WriteProduct(ctx, ops, subject, grantRules(rules), 0, setupChatProvenance)
	if err != nil {
		return nil, err
	}
	var written []setup.ApprovedRule
	for _, rule := range rules {
		r := grants.Rule{Action: rule.Action, Resource: rule.Resource}
		if slices.Contains(result.Written, r) || slices.Contains(result.Held, r) && slices.Contains(previous, rule) {
			written = append(written, rule)
		}
	}
	var gone []setup.ApprovedRule
	for _, rule := range previous {
		if !slices.Contains(rules, rule) {
			gone = append(gone, rule)
		}
	}
	if err := grants.RemoveProduct(ctx, ops, self, subject, grantRules(gone), setupChatProvenance); err != nil {
		return nil, err
	}
	return written, nil
}

// grantRules is approved as grants rules.
func grantRules(approved []setup.ApprovedRule) []grants.Rule {
	rules := make([]grants.Rule, 0, len(approved))
	for _, rule := range approved {
		rules = append(rules, grants.Rule{Action: rule.Action, Resource: rule.Resource})
	}
	return rules
}

func (o stepChatOwners) bind() *setup.StepChat {
	return &setup.StepChat{Offer: o.offer, Grant: o.grantOffer}
}

// offer reads the chat permission app gets: the one rule, for the program
// its registration names. A program the rights cannot name as the app gets
// none: no program, a shared interpreter (VISION 2026-10-07), or, for an app
// the catalogue names an official program for, a file without that
// publisher's signature.
func (o stepChatOwners) offer(_ context.Context, app setup.StepApp, pick setup.StepPick) (setup.StepChatOffer, error) {
	if reason := o.programReason(app); reason != "" {
		return setup.StepChatOffer{Reason: reason}, nil
	}
	rules := chatPermissionRules()
	for _, rule := range rules {
		if _, err := permissionSentence(rule, useChat); err != nil {
			return setup.StepChatOffer{}, err
		}
	}
	return setup.StepChatOffer{App: app.Name, Program: app.Program, Rules: rules,
		Permission: fmt.Sprintf("%s can use %s on this computer.", app.Title, pick.Title)}, nil
}

// programReason says why app's program cannot carry the permission; "" when
// it can.
func (o stepChatOwners) programReason(app setup.StepApp) string {
	if app.Program == "" {
		return app.Title + "'s registration names no program"
	}
	if identity.SharedInterpreterProgram(app.Program, identity.Code{}) {
		return app.Title + "'s registered program is a shared interpreter"
	}
	if official, ok := setup.OfficialProgramOf(setup.AppID(app.Name)); ok {
		if subject, signed := o.signer(app.Program); !signed || subject != official.Signer {
			return app.Title + "'s program does not carry its publisher's signature"
		}
	}
	return ""
}

// grantOffer settles exactly the offered rule for the app's confirmed
// registration of the offered program (writeSetupChatRules). A registration
// not confirmed yet, or not made yet, writes nothing and waits; a changed
// program or rule set writes nothing and asks again.
func (o stepChatOwners) grantOffer(ctx context.Context, offer setup.StepChatOffer, previous []setup.ApprovedRule) ([]setup.ApprovedRule, bool, error) {
	if setup.RulesDigest(offer.Rules) != setup.RulesDigest(chatPermissionRules()) {
		return nil, false, errors.New("the chat permission changed; set up again")
	}
	apps, err := o.apps(ctx)
	if err != nil {
		return nil, false, err
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return nil, false, err
	}
	for _, entry := range page.Applications {
		if entry.Descriptor.Name != offer.App {
			continue
		}
		if entry.SelfRegistered {
			return nil, false, nil
		}
		if entry.Descriptor.Program != offer.Program {
			return nil, false, errors.New("the app's registration changed; set up again")
		}
		if reason := o.programReason(setup.StepApp{Name: offer.App, Title: offer.App, Program: offer.Program}); reason != "" {
			return nil, false, errors.New(reason)
		}
		written, err := o.grant(ctx, offer.Program, offer.Rules, previous)
		return written, err == nil, err
	}
	return nil, false, nil
}

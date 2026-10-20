package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
)

var errPersonalReviewDismissed = errors.New("permission review closed without a decision; no permissions were changed")

// Successful decisions and dismissal close silently. Only an actual failure
// needs a report; Windows uses its existing fail(native) dialog at the caller.
func finishPersonalPermissionReview(err error, report func(presenterReport) bool) error {
	if errors.Is(err, errPersonalReviewDismissed) || errors.Is(err, errPromptNotShown) {
		return nil
	}
	if err != nil && report != nil {
		report(presenterReport{Title: trayDisplayName, Instruction: "Permission review needs attention", Content: err.Error()})
	}
	return err
}

// This is PERSONAL management consent through the existing native permission
// renderer. It creates no Asks occurrence or application permission effect.
func personalReviewPrompt(v permissionReviewView, publisher trayPublisher) trayQuestionPresentation {
	instruction := "Allow " + v.Title + " to use these features for one hour?"
	operations := strings.Join(v.Sentences, "\n")
	if v.Host != nil {
		operations += "\nSelected image server: " + v.HostLabel
	}
	effects := "Permissions expire after one hour. Refuse changes no permissions. Closing this window makes no decision."
	content := strings.TrimSpace(v.Disclosure + "\n\n" + operations + "\n\nLifetime: One hour.\n" + effects)
	publisherText := "Publisher: not verified."
	if publisher.Trusted {
		publisherText = "Publisher: verified; name unavailable."
		if publisher.Subject != "" {
			publisherText = "Publisher: " + publisher.Subject
		}
	}
	details := "Program: " + v.Program.Program + "\n" + publisherText + "\n"
	for _, rule := range v.Rules {
		details += rule.Action + " — " + rule.Resource + "\n"
	}
	warning := v.Disclosure
	tier := asks.TierRoutine
	for _, rule := range v.Rules {
		tier = higherTier(tier, promptTier(rule.Action, rule.Resource))
	}
	return trayQuestionPresentation{Title: "Permission review", Instruction: instruction,
		Content: content, Details: details, Footer: effects, Warning: warning, PermissionContent: operations + "\n\nLifetime: One hour.",
		Choices: []trayQuestionChoice{{Label: "Allow for one hour", Option: "allow"}, {Label: "Refuse", Option: "refuse"}}, Default: "refuse",
		Level: promptLevelFor(promptEvidence{Program: v.Subject.Program, Publisher: publisher, Tier: tier}),
		Permission: &trayPermissionPresentation{Instruction: instruction, Content: content, Details: details, Footer: effects,
			Warning: &warning, Question: instruction, Requester: v.Title, Publisher: publisherText,
			RequesterProgram: permissionIconProgram("", v.Subject.Program, publisher), Scope: operations, Effects: "One hour, then access expires."}}
}

// Signature evidence supplies presentation only. The compiled review and its
// original file identity remain the authority checked again before any edit.
func personalReviewPublisher(v permissionReviewView) trayPublisher {
	code, _, err := identity.VerifyFile(v.Program.Program, nil)
	if err != nil {
		return trayPublisher{}
	}
	evidence := asks.CodePublisher(code)
	return trayPublisher{Subject: evidence.Subject, Trusted: evidence.Trusted, Description: evidence.Description, OriginalName: evidence.OriginalName}
}

func (p *servicePanel) askPersonalPermissionReview(ctx context.Context, session presenterSession, surface presenterSurface) error {
	if !session.Unlocked() {
		return errPromptNotShown
	}
	revision := session.Revision()
	source := p.permissionReviewSource()
	view, _, _, err := buildPermissionReview(ctx, source, startPermissionReview)
	if err != nil {
		return err
	}
	publisher := personalReviewPublisher(view)
	current, err := source.Program(view.Program.Program)
	if err != nil || current != view.Program {
		return errors.New("the program changed; open a fresh permission review")
	}
	option, err := surface.Ask(personalReviewPrompt(view, publisher))
	if err != nil {
		return err
	}
	if !session.Unlocked() || session.Revision() != revision {
		return errPromptNotShown
	}
	if option == "" {
		return errPersonalReviewDismissed
	}
	if option != "allow" && option != "refuse" {
		return errors.New("invalid native permission review choice")
	}
	result, _, err := p.decidePermissionReview(ctx, permissionReviewChoice{Context: view.Context, Shown: view.ID, Allow: option == "allow"})
	if err != nil || (result.Outcome != "applied" && result.Outcome != "refused") {
		saved := []string{}
		for _, record := range result.Landed {
			for index, rule := range view.Rules {
				if record.Rule.Action == rule.Action && record.Rule.Resource == rule.Resource {
					saved = append(saved, view.Sentences[index])
				}
			}
		}
		message := result.Message
		if err != nil {
			message = err.Error()
		}
		return fmt.Errorf("%s\nSaved permissions: %s", message, strings.Join(saved, "; "))
	}
	return nil
}

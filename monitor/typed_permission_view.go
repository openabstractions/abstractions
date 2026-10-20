package main

import (
	"fmt"
	"slices"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// Only the trusted capability projection supplies words for an owning target.
// The renderer keeps the owning document opaque and displays the retained,
// finite operation list without deriving authority from labels or app purpose.
func trayTypedPermissionView(record askclient.RecordMetadata, details askclient.TypedPermissionDetails, names *programTitler) trayQuestionPresentation {
	effect, display := details.Effect, details.Display
	if effect == nil || display == nil || len(effect.Selections) == 0 || len(display.Selections) != len(effect.Selections) {
		return trayQuestionPresentation{}
	}
	publisher := publisherOfRead(details.Question.Question).forPrompt(names)
	name := programDisplayName(publisher, effect.Subject.Program)
	publisherText := "Publisher: not verified."
	if publisher.Trusted && publisher.Subject != "" {
		publisherText = "Publisher: " + publisher.Subject + " (verified at request)."
	} else if publisher.Trusted {
		publisherText = "Publisher: verified signature; name unavailable."
	}
	if script := scriptHostSentence(publisher, effect.Subject.Program); script != "" {
		publisherText += "\n" + script
	}
	var scopes, operations, windowsOperations, warnings []string
	var tier asks.Tier
	allItems := false
	exact := []string{"Program: " + effect.Subject.Program, "Account: " + effect.Subject.Account, publisherText,
		"Origin: " + details.Origin}
	if display.BundleLabel != "" {
		scopes = append(scopes, display.BundleLabel)
	}
	if display.BundleDisclosure != "" {
		scopes = append(scopes, display.BundleDisclosure)
	}
	for index, selection := range effect.Selections {
		words := display.Selections[index]
		operation := words.Operation
		tier = higherTier(tier, promptTier(selection.Permission.Action, ""))
		if selection.Selector.Kind == rightsclient.ResourceSelectorKindAll {
			allItems = true
			appendPermissionLine(&scopes, "All: "+words.Collection)
			appendPermissionLine(&warnings, words.AllEffect)
		} else {
			appendPermissionLine(&scopes, "One: "+words.Target)
		}
		// Every operation and its owning disclosure stays visible. In particular,
		// the owner's raw Read disclosure describes exporting the value to the app.
		if words.Disclosure != "" {
			operation += " — " + words.Disclosure
		}
		operations = append(operations, "• "+operation)
		windowsOperation := operation
		if len(effect.Selections) == 1 && words.Disclosure != "" {
			windowsOperation = words.Disclosure
		}
		// This closed action/profile supplies a display abbreviation only. The
		// retained owning disclosure and every operation remain in Details.
		if selection.Permission.Action == credentials.ActionSecretRead && selection.Permission.ResourceProfile == credentials.NamedSecretResourceProfile {
			windowsOperation = "This app can view, copy or use the secret value anywhere."
			if len(effect.Selections) > 1 {
				windowsOperation = words.Operation + " — " + windowsOperation
			}
		}
		if len(effect.Selections) > 1 {
			windowsOperation = "• " + windowsOperation
		}
		if windowsOperation != "" {
			windowsOperations = append(windowsOperations, windowsOperation)
		}
		exact = append(exact, "", fmt.Sprintf("Operation %d: %s", index+1, words.Operation),
			"Action: "+selection.Permission.Action, "Resource profile: "+selection.Permission.ResourceProfile,
			"Selector: "+selection.Selector.Kind.String())
		if selection.Selector.Kind == rightsclient.ResourceSelectorKindAll {
			exact = append(exact, "Collection: "+words.Collection, "All effect: "+words.AllEffect)
		} else {
			exact = append(exact, "Target: "+words.Target)
		}
		if words.Disclosure != "" {
			exact = append(exact, "Disclosure: "+words.Disclosure)
		}
	}
	if display.RequiresOSAuthorization {
		warnings = append(warnings, "This access also requires operating system authorization.")
	}
	if bundle := effect.Bundle; bundle != nil {
		exact = append(exact, "", "Bundle version: "+bundle.BundleRef,
			"Retained operation count: "+fmt.Sprint(len(bundle.Selections)), "Bundle disclosure: "+display.BundleDisclosure)
	}
	if details.Purpose != "" {
		exact = append(exact, "", "Purpose supplied by the requester: "+details.Purpose)
	}
	if record.Asked != "" {
		exact = append(exact, "Asked: "+record.Asked)
	}
	exact = append(exact, "Question: "+record.ID)
	scope, operationsText := strings.Join(scopes, "\n"), strings.Join(operations, "\n")
	warning := strings.Join(warnings, "\n")
	if warning != "" {
		warning = "Review this scope carefully. " + warning
	}
	answerEffects := "Allow saves ongoing access for this app. Refuse changes no permissions."
	footer := "Cancel or Escape closes this window without answering."
	exact = append(exact, "", "Lifetime: Ongoing, until removed.", "Answer effects: "+answerEffects, footer)
	body := []string{publisherText, scope, operationsText}
	permissionContent := strings.Join(slices.DeleteFunc(body, func(s string) bool { return s == "" }), "\n\n")
	content := permissionContent
	if warning != "" {
		content += "\n\n" + warning
	}
	instruction := "Allow " + name + " access?"
	detailsText := strings.Join(exact, "\n")
	windowsQuestion := "Allow this app access?"
	if display.BundleLabel != "" {
		windowsQuestion = "Allow this app " + strings.ToLower(display.BundleLabel) + "?"
	} else if len(display.Selections) == 1 {
		windowsQuestion = "Allow this app to " + lowerPermissionInitial(display.Selections[0].Operation) + "?"
		key := effect.Selections[0].Permission
		if key.Action == credentials.ActionSecretRead && key.ResourceProfile == credentials.NamedSecretResourceProfile {
			windowsQuestion = "Allow this app to read the secret value?"
		}
	}
	windowsPublisher := "Publisher: not verified."
	if publisher.Trusted {
		windowsPublisher = "Publisher: " + publisher.Subject
		if publisher.Subject == "" {
			windowsPublisher = "Publisher: verified; name unavailable."
		}
	}
	if script := scriptHostSentence(publisher, effect.Subject.Program); script != "" {
		windowsPublisher += "\n" + script
	}
	return trayQuestionPresentation{Title: trayDisplayName + " — Permission request", Instruction: instruction,
		Content: content, Details: detailsText, Footer: answerEffects, Warning: warning, PermissionContent: permissionContent,
		Choices: questionChoices(record.Options), Default: trayDefaultChoice(record.Options),
		Level: promptLevelFor(promptEvidence{Program: effect.Subject.Program, Publisher: publisher, Tier: tier, AllItems: allItems}),
		Permission: &trayPermissionPresentation{Instruction: instruction, Content: content, Details: detailsText,
			Footer: footer, Warning: &warning, Question: windowsQuestion, Requester: name,
			Publisher: windowsPublisher, RequesterProgram: permissionIconProgram("", effect.Subject.Program, publisher),
			Scope: strings.TrimSpace(scope + "\n\n" + strings.Join(windowsOperations, "\n")), Effects: "Ongoing access until removed."}}
}

func lowerPermissionInitial(label string) string {
	if label == "" {
		return label
	}
	runes := []rune(label)
	return strings.ToLower(string(runes[0])) + string(runes[1:])
}

func appendPermissionLine(lines *[]string, line string) {
	if line != "" && !slices.Contains(*lines, line) {
		*lines = append(*lines, line)
	}
}

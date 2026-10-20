package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// Only the trusted capability projection supplies words for an owning target.
// The renderer keeps the owning document opaque and displays the retained,
// finite operation list without deriving authority from labels or app purpose.
//
// Architect popup ruling 2026-11-10, row 6: the heading names the app and its
// operations, the inline lines are the publisher, the script host and two
// warnings, and the scope lines, operations and publisher sit in details.
func trayTypedPermissionView(record askclient.RecordMetadata, details askclient.TypedPermissionDetails, names *programTitler) trayQuestionPresentation {
	effect, display := details.Effect, details.Display
	if effect == nil || display == nil || len(effect.Selections) == 0 || len(display.Selections) != len(effect.Selections) {
		return trayQuestionPresentation{}
	}
	if _, captured, ok := grants.AgentMessageLegacyEffect(*effect); ok {
		return trayMessagePermissionView(record, details, names, captured)
	}
	publisher := publisherOfRead(details.Question.Question).forPrompt(names)
	name := programDisplayName(publisher, effect.Subject.Program)
	ownProgram := names.oaOwnProgram(effect.Subject.Program)
	inline := []string{}
	if line := promptPublisherSummary(publisher); line != "" && !ownProgram {
		inline = append(inline, line)
	}
	script := scriptHostSentence(publisher, effect.Subject.Program)
	if script != "" {
		inline = append(inline, promptScriptHostLine)
	}
	var scopes, operations, phrases, warnings []string
	object := ""
	var tier asks.Tier
	allItems, secretRead := false, false
	applicationRules, _, application := grants.ApplicationPermissionRules(*effect)
	_, graphEdit := grants.KeptGraphEditTarget(*effect)
	exact := []string{"Program: " + effect.Subject.Program, "Account: " + effect.Subject.Account}
	if !ownProgram {
		exact = append(exact, promptPublisherDetail(publisher))
	}
	if script != "" {
		exact = append(exact, script)
	}
	exact = append(exact, "Origin: "+details.Origin)
	if display.BundleLabel != "" {
		scopes = append(scopes, display.BundleLabel)
	}
	if display.BundleDisclosure != "" {
		scopes = append(scopes, display.BundleDisclosure)
	}
	for index, selection := range effect.Selections {
		words := display.Selections[index]
		operation := words.Operation
		resource := ""
		if application {
			resource = applicationRules[index].Resource
		}
		tier = higherTier(tier, promptTier(selection.Permission.Action, resource))
		if selection.Selector.Kind == rightsclient.ResourceSelectorKindAll {
			allItems = true
			appendPermissionLine(&scopes, "All: "+words.Collection)
			appendPermissionLine(&scopes, words.AllEffect)
		} else {
			appendPermissionLine(&scopes, "One: "+words.Target)
		}
		appendPermissionLine(&phrases, typedOperationPhrase(words, selection.Selector.Kind == rightsclient.ResourceSelectorKindAll))
		if object == "" && selection.Selector.Kind == rightsclient.ResourceSelectorKindAll && words.Collection != "" {
			object = "all " + lowerPermissionInitial(words.Collection)
		} else if object == "" && words.Target != "" {
			object = lowerPermissionInitial(words.Target)
		}
		// Every operation and its owning disclosure stays in the details. In
		// particular, the owner's raw Read disclosure describes exporting the
		// value to the app.
		if words.Disclosure != "" {
			operation += " — " + words.Disclosure
		}
		operations = append(operations, "• "+operation)
		// Any raw Read exports the value; its one inline warning says so.
		if selection.Permission.Action == credentials.ActionSecretRead {
			secretRead = true
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
	if secretRead {
		warnings = append(warnings, "It can copy and use the key anywhere.")
	}
	if display.RequiresOSAuthorization {
		warnings = append(warnings, "Windows asks next.")
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
	exact = append([]string{strings.Join(scopes, "\n"), strings.Join(operations, "\n"), ""}, exact...)
	choices := questionChoices(record.Options)
	if graphEdit && slices.Contains(record.Options, "once") {
		for i := range choices {
			if choices[i].Option == "allow" {
				choices[i].Label = "Always allow"
			}
		}
		exact = append(exact, "", "Allow once applies to this request.", "Always allow saves access for this connection and kept application until removed.", promptCloseDetail)
	} else {
		exact = append(exact, "", "Lifetime: Ongoing, until removed.", "Allow saves ongoing access for this app.", promptCloseDetail)
	}
	question := "Allow " + name + " to " + typedPermissionPhrase(display.BundleLabel, phrases, secretRead, object) + "?"
	shown := strings.Join(append(inline, warnings...), "\n")
	warning := ""
	detailsText := strings.TrimSpace(strings.Join(exact, "\n"))
	notNow := trayDefaultChoice(record.Options)
	return trayQuestionPresentation{presentation: catalogueQuestionFamily(record.Key), Title: popupTitle, Instruction: question,
		Content: shown, Details: detailsText, PermissionContent: shown,
		Choices: choices, Default: notNow, Dismiss: notNow,
		Level: promptLevelFor(promptEvidence{Program: effect.Subject.Program, Publisher: publisher, Tier: tier, AllItems: allItems}),
		Permission: &trayPermissionPresentation{Instruction: question, Content: shown, Details: detailsText,
			Warning: &warning, Question: question, Requester: name,
			Publisher: strings.Join(inline, "\n"), RequesterProgram: permissionIconProgram("", effect.Subject.Program, publisher),
			Scope: strings.Join(warnings, "\n")}}
}

// typedOperationPhrase is one operation as a heading names it: a phrase as
// the owner words it ("make pictures on this computer"), and a bare verb
// ("Use") with what it acts on ("use “deepseek”", "use all saved app
// secrets").
func typedOperationPhrase(words askclient.PermissionDisplaySelection, all bool) string {
	phrase := lowerPermissionInitial(words.Operation)
	if strings.Contains(phrase, " ") {
		return phrase
	}
	if all && words.Collection != "" {
		return phrase + " all " + lowerPermissionInitial(words.Collection)
	}
	if !all && words.Target != "" {
		return phrase + " " + lowerPermissionInitial(words.Target)
	}
	return phrase
}

// typedPermissionPhrase is what a typed permission's heading asks for: an
// application's own bundle as the app it reaches, another bundle as its
// label over what it reaches ("have full control of all saved app
// secrets"), the saved key's read, or every operation in one list. It is
// never a bare "access".
func typedPermissionPhrase(bundleLabel string, operations []string, secretRead bool, object string) string {
	switch {
	case strings.HasPrefix(bundleLabel, "Access to "):
		// An application's own permission bundle uses the application title.
		return "use " + strings.TrimPrefix(bundleLabel, "Access to ")
	case bundleLabel != "":
		label := lowerPermissionInitial(bundleVersion.ReplaceAllString(bundleLabel, ""))
		if object == "" {
			return "have " + label
		}
		return "have " + label + " of " + object
	case secretRead && len(operations) == 1 && !strings.HasPrefix(object, "all "):
		return "read the saved key"
	case len(operations) == 0:
		return "use this capability"
	}
	return grants.MessagePermissionNames(operations)
}

// bundleVersion is a bundle label's trailing version ("Full control v1").
var bundleVersion = regexp.MustCompile(` v[0-9]+$`)

// messagePartyTitle names a message sender or receiver by the program titler:
// its program's title (an OA connector by its agent), else the title
// registered under its name, else its registered name. A party with a
// program never reads as an agent by its connector name alone: a registry
// read that timed out, a paired party or a program that registered itself
// under that name leaves the bare name. It never reads as a file name.
func messagePartyTitle(names *programTitler, program, name string) string {
	if program == "" {
		if title, ok := names.applicationTitle(name); ok {
			return title
		}
		return name
	}
	if title, ok := names.title(program); ok {
		return title
	}
	if title, ok := names.registeredApplicationTitle(name); ok {
		return title
	}
	return name
}

// messagePermissionTitles are the sender's and receivers' titles of one
// message question. It orders captured in place by receiver title, so the
// heading and details list the receivers in the same order.
func messagePermissionTitles(names *programTitler, captured []applications.AgentMessageResource) (string, []string) {
	sender := messagePartyTitle(names, captured[0].Sender.Program, captured[0].Sender.Name)
	title := func(pair applications.AgentMessageResource) string {
		return messagePartyTitle(names, pair.Receiver.Program, pair.Receiver.Name)
	}
	slices.SortStableFunc(captured, func(a, b applications.AgentMessageResource) int {
		return strings.Compare(strings.ToLower(title(a)), strings.ToLower(title(b)))
	})
	receivers := make([]string, 0, len(captured))
	for _, pair := range captured {
		receivers = append(receivers, title(pair))
	}
	return sender, receivers
}

// trayMessagePermissionView is one sending agent's question about every
// receiver it names, worded by the Facade's message templates. OA's own
// connectors show no Publisher line.
func trayMessagePermissionView(record askclient.RecordMetadata, details askclient.TypedPermissionDetails, names *programTitler, captured []applications.AgentMessageResource) trayQuestionPresentation {
	effect := details.Effect
	sender, receivers := messagePermissionTitles(names, captured)
	question := grants.MessagePermissionQuestion(sender, receivers)
	body := grants.MessagePermissionBody(receivers)
	publisher := publisherOfRead(details.Question.Question).forPrompt(names)
	ownProgram := names.oaOwnProgram(effect.Subject.Program)
	publisherText := ""
	if !ownProgram {
		publisherText = promptPublisherSummary(publisher)
	}
	exact := []string{grants.MessagePermissionDetails(receivers), "", "Program: " + effect.Subject.Program, "Account: " + effect.Subject.Account}
	if !ownProgram {
		exact = append(exact, promptPublisherDetail(publisher))
	}
	exact = append(exact, "Origin: "+details.Origin)
	for i, pair := range captured {
		exact = append(exact, "", fmt.Sprintf("Receiver %d: %s", i+1, receivers[i]), "Action: "+grants.ApplicationMessageAction, "Resource: app:"+pair.Receiver.Name)
	}
	if record.Asked != "" {
		exact = append(exact, "Asked: "+record.Asked)
	}
	exact = append(exact, "Question: "+record.ID, "", "Lifetime: Ongoing, until removed in Permissions, one permission per agent.", promptCloseDetail)
	detailsText := strings.Join(exact, "\n")
	var choices []trayQuestionChoice
	for _, c := range []trayQuestionChoice{{Label: "Allow", Option: "allow"}, {Label: "Not now", Option: "refuse"}} {
		if slices.Contains(record.Options, c.Option) {
			choices = append(choices, c)
		}
	}
	empty := ""
	visible := strings.TrimSpace(body + "\n" + publisherText)
	notNow := trayDefaultChoice(record.Options)
	// Architect popup ruling 2026-11-10, row 4: one heading, one body
	// sentence, the inline publisher line only when it is not verified.
	return trayQuestionPresentation{presentation: catalogueQuestionFamily(record.Key), Title: popupTitle, Instruction: question,
		Content: visible, Details: detailsText, PermissionContent: visible,
		Choices: choices, Default: notNow, Dismiss: notNow,
		Level: promptLevelFor(promptEvidence{Program: effect.Subject.Program, Publisher: publisher, Tier: asks.TierSensitive}),
		Permission: &trayPermissionPresentation{Instruction: question, Content: visible, Details: detailsText, Warning: &empty,
			Question: question, Requester: sender, Publisher: publisherText,
			RequesterProgram: permissionIconProgram("", effect.Subject.Program, publisher), Scope: body}}
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

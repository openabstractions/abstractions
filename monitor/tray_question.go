package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-asks/go/catalogue"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/remote"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// trayAnswerBudget bounds one re-read and answer of a shown question.
const trayAnswerBudget = 5 * time.Second

type trayQuestionChoice struct {
	Label  string `json:"label"`
	Option string `json:"option"`
}

// trayQuestionPresentation is the native prompt's text. Instruction and
// Content are visible. Typed permissions keep technical identities in Details;
// reserved pairing keeps its human agreement in Content and exact original in Details.
type trayQuestionPresentation struct {
	presentation nativeQuestionFamily
	Title        string
	Instruction  string
	Content      string
	Details      string
	Footer       string
	// Warning is a trusted permission disclosure. The Mac launcher selects the
	// native caution style when it is present; Content retains the whole text.
	Warning string `json:",omitempty"`
	// PermissionContent selects the bounded AppKit permission body. Warning and
	// answer effects stay visible beside it. Empty keeps the legacy frame shape.
	PermissionContent string `json:",omitempty"`
	Choices           []trayQuestionChoice
	// Default is the option Enter selects: the refusing one, or none.
	Default string
	// Dismiss is the option Escape and the close box answer when one of the
	// choices already declines; the Windows dialog then draws no separate
	// Cancel (win.QuestionPrompt.Dismiss).
	Dismiss string `json:"-"`
	// Permission carries consent display content after Asks selects its family.
	// Its presence supplies neither the family nor authority.
	Permission *trayPermissionPresentation `json:"-"`
	// CredentialForm is owned by this native presenter process. It is never
	// serialized across the Asks or native bridge boundary.
	CredentialForm *providerKeyForm `json:"-"`
	// AppChoice is set by the typed ordinary-record projection. Application
	// titles and option words never choose native permission presentation.
	AppChoice bool `json:",omitempty"`
	// PermissionPrompt and IconProgram are derived by the Mac bridge from the
	// validated Asks family: the launcher plays the alert sound for consent
	// and shows IconProgram's app bundle icon (permissionIconProgram's choice;
	// empty keeps OA's own icon).
	PermissionPrompt bool   `json:",omitempty"`
	IconProgram      string `json:",omitempty"`
	// PairingPrompt only names the unanswered dismissal separately from the
	// pairing refusal. It carries no permission or answer authority.
	PairingPrompt bool `json:",omitempty"`
	// Level is the prompt's header level, chosen by promptLevelFor alone.
	// Both native renderers color by it and decide nothing from the text.
	Level promptLevel `json:",omitempty"`
}

// promptLevel is a native prompt's header level, after the Windows UAC
// banners: blue for OpenAbstractions' own Panel or report, gray for a verified
// publisher's ordinary request, yellow for an unverified requester and orange
// for a high-risk request whatever its publisher. It is presentation only: it
// changes no tier, eligibility or authority.
type promptLevel string

const (
	promptLevelBlue   promptLevel = "blue"
	promptLevelGray   promptLevel = "gray"
	promptLevelYellow promptLevel = "yellow"
	promptLevelOrange promptLevel = "orange"
)

// trayNotNow is the Dismiss value of a question whose Escape and close box
// decide nothing. It is no record option; the presenter reads it as a close
// and the question stays pending.
const trayNotNow = "not_now"

// promptEvidence is everything promptLevelFor reads. Each field comes from the
// service's record of the request; none comes from question text, option
// words or an app's labels.
type promptEvidence struct {
	// OpenAbstractions is set for a prompt OpenAbstractions words on its own
	// behalf with no requesting app, such as the outcome of an answer.
	OpenAbstractions bool
	// Program is the service-bound program the prompt concerns: the subject
	// whose permission changes, or the asker of a question. Empty when the
	// service recorded none.
	Program string
	// Publisher is the publisher recorded at admission (ASK-A6).
	Publisher trayPublisher
	// Tier is the highest first-use tier among the permissions the answer
	// changes (promptTier); empty for a question that changes none.
	Tier asks.Tier
	// AllItems is set when a permission's selector reaches every item of a
	// collection, such as every saved secret.
	AllItems bool
}

// promptLevelFor is the one choice of a native prompt's header level. The
// first matching row decides:
//
//	orange  administrative tier, every item of a collection, or a script host,
//	        whoever asks
//	blue    a verified publisher, the Panel's own program, or
//	        OpenAbstractions' own words about no program
//	yellow  (amber) everything else: an unverified or unsigned program, as
//	        Windows' UAC banner shows an unknown publisher
//
// The runtime programs (openabstractions, openabstractionsw) are a CLI any
// process or agent can drive, so a request through them is judged like any
// other requester's.
func promptLevelFor(e promptEvidence) promptLevel {
	switch {
	case e.Tier == asks.TierAdministrative || e.AllItems || asks.ScriptHostSubject(rwire.Subject{Program: e.Program}, e.Publisher.asksPublisher()):
		return promptLevelOrange
	case e.Publisher.Trusted || oaPanelProgram(e.Program) || (e.OpenAbstractions && e.Program == ""):
		return promptLevelBlue
	}
	return promptLevelYellow
}

// reportPromptLevel is the level of the report OpenAbstractions shows after
// an answer: its own words.
func reportPromptLevel() promptLevel {
	return promptLevelFor(promptEvidence{OpenAbstractions: true})
}

// oaPresenterFile reports whether program is the Asks presenter's file name.
// The presenter raises the sign-in duties' questions as OA's own asker.
func oaPresenterFile(program string) bool {
	return strings.EqualFold(strings.TrimSuffix(filepath.Base(program), filepath.Ext(program)), presenterFileStem)
}

// oaPanelProgram reports whether program is the Panel's own program, by the
// program-title rule's list (program_titles.go). The runtime programs on that
// list are excluded: others can drive them.
func oaPanelProgram(program string) bool {
	if program == "" {
		return false
	}
	for _, own := range ownProgramTitles() {
		if (own.title == oaPanelTitle || oaPresenterFile(own.program)) && product.SameProgram(own.program, program) {
			return true
		}
	}
	return false
}

// promptTierFacts stands in for the runtime's locality and registry facts,
// which the Panel does not hold: a tier row that needs them reads as its
// routine alternative. A hosted model therefore takes its publisher's level;
// the runtime's answerer check keeps the exact tier (ASK-R6).
var promptTierFacts = grants.TierFacts{
	LocalServer:    func(string) bool { return true },
	ListedRegistry: func(string) bool { return true },
}

// promptTier is the first-use tier the prompt's level reads, from the one
// tier table (grants.FirstUseTier).
func promptTier(action, resource string) asks.Tier {
	return grants.FirstUseTier(action, resource, promptTierFacts)
}

// higherTier is the stricter of two tiers; empty is below routine.
func higherTier(a, b asks.Tier) asks.Tier {
	rank := map[asks.Tier]int{asks.TierRoutine: 1, asks.TierSensitive: 2, asks.TierAdministrative: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// asksPublisher is the publisher in the asks package's form.
func (pub trayPublisher) asksPublisher() asks.Publisher {
	return asks.Publisher{Subject: pub.Subject, Trusted: pub.Trusted, Description: pub.Description, OriginalName: pub.OriginalName}
}

type trayPermissionPresentation struct {
	Instruction string
	Content     string
	Details     string
	Footer      string
	// Warning is explicit Windows-only visible warning text. Nil keeps the
	// composed legacy footer; empty omits its fixed dismissal instruction.
	Warning *string
	// Separate plain-text blocks for Windows 11. The native
	// renderer never extracts identity or permission scope from application prose.
	Question  string
	Requester string
	Publisher string
	Scope     string
	Effects   string
	// RequesterProgram is permissionIconProgram's choice, solely for the icon;
	// empty shows the generic application icon. The header color is the
	// prompt's Level.
	RequesterProgram string
}

// trayPublisher is the requester's publisher the runtime recorded when it
// admitted the question (ASK-A6), set only for a trusted signature. Trusted
// with an empty Subject is a verified signature whose signer name was not
// recorded (one too long, or carrying a control or format character).
// Description and OriginalName are the program's signed version resource.
type trayPublisher struct {
	Subject      string
	Trusted      bool
	Description  string
	OriginalName string
	// Names titles the program (program_titles.go); nil knows only
	// OpenAbstractions' own programs.
	Names *programTitler
	// Prompt is the native prompt's order (forPrompt): a verified signed
	// description first, the title only for an unsigned program.
	Prompt bool
}

// withNames is the publisher with the request's or prompt's titler.
func (pub trayPublisher) withNames(names *programTitler) trayPublisher {
	pub.Names = names
	return pub
}

// forPrompt is the publisher for the native prompt and its notification,
// which name a program by its verified signed description first.
func (pub trayPublisher) forPrompt(names *programTitler) trayPublisher {
	pub.Names, pub.Prompt = names, true
	return pub
}

// trayQuestionViewForPermission words one pending record for the native prompt. The
// application is named by its signed description when one was recorded, by
// its file name otherwise, and by the publisher the runtime recorded when it
// admitted the question (ASK-A6). A script host adds the one sentence asks
// gives for it (ASK-A8). The action and resource are in plain words; the
// program's path and the exact values the service recorded stay in the
// details.
func trayQuestionViewForPermission(record askclient.RecordMetadata, publisher trayPublisher, permission *askclient.PermissionRequest) trayQuestionPresentation {
	return trayQuestionViewForAsker(record, publisher, permission, "")
}

// selfRegisteredWords reads the runtime's pending PROGRAM Keep question
// (asks SelfRegisteredKey, ASK-R11): the program its registration
// bound in retained catalogue slots, and the sentence a person reads, which
// names the app by its title and the program by its file stem. ok is false
// for any other record and for one that names an asker program, which only an application's own question
// does; such a record keeps the generic wording.
func selfRegisteredWords(record askclient.RecordMetadata, askerProgram string, content *askclient.CatalogueContent) (program, sentence string, ok bool) {
	program, title, ok := selfRegisteredTarget(record.Key, record.About, askerProgram, content)
	if !ok {
		return "", "", false
	}
	// A title that is the program's path names nothing more than its stem
	// (architect ruling 2026-11-09, second block).
	if title == programStem(program) {
		return program, programStem(program) + " was added to OpenAbstractions. Keep it?", true
	}
	return program, title + " (" + programStem(program) + ") was added to OpenAbstractions. Keep it?", true
}

func selfRegisteredTarget(key, about, askerProgram string, content *askclient.CatalogueContent) (program, title string, ok bool) {
	if askerProgram != "" || key != asks.SelfRegisteredKey || content == nil {
		return "", "", false
	}
	program, title = content.Slots["program"], content.Slots["title"]
	_, titled := content.Slots["title"]
	// An empty title, or one that is the program's path, reads as the
	// program's file name without its extension.
	if title == "" || title == program || strings.ContainsAny(title, `\/`) {
		title = programStem(program)
	}
	return program, title, program != "" && program == about && titled
}

// OA's own programs, the agent connectors included, show no Publisher line.
func selfRegisteredPublisherLine(publisher trayPublisher, program string) string {
	if (*programTitler)(nil).oaOwnProgram(program) {
		return ""
	}
	switch {
	case publisher.Trusted && publisher.Subject != "":
		return "Publisher: " + publisher.Subject + " (signature verified at registration)."
	case publisher.Trusted:
		return "Publisher: signature verified at registration; no publisher name was recorded."
	default:
		return "Publisher: not verified. No trusted signature was recorded at registration."
	}
}

// keepRegisteredView words the Keep question about a program that added
// itself. The runtime asks it only after the person acts on that program in
// Apps or presses Connect for it in the Catalog without a matching Connect
// expectation (VISION 2026-10-06, amended 2026-11-10; architect ruling
// 2026-11-11): one plain question naming the app, Allow and Don't allow, and
// the path, the publisher and why an unsigned program needs the person
// behind Show details. Escape and the close box decide nothing: the question
// stays pending.
func keepRegisteredView(record askclient.RecordMetadata, publisher trayPublisher, program, title string, choices []trayQuestionChoice) trayQuestionPresentation {
	instruction, content := "Let "+title+" work with OpenAbstractions?", ""
	details := []string{"Program: " + program}
	line := ""
	if publisher.Trusted {
		if line = selfRegisteredPublisherLine(publisher, program); line != "" {
			details = append(details, line)
		}
		if publisher.Description != "" {
			details = append(details, "Description: "+publisher.Description)
		}
		if publisher.OriginalName != "" {
			details = append(details, "Original file name: "+publisher.OriginalName)
		}
	} else {
		details = append(details, "This program is unsigned. Allow it only if you installed it yourself.",
			"Windows cannot confirm who made this program: it carries no signature Windows trusts. "+
				"A signature names the publisher and shows the file is unchanged since its release; without one, only the person who installed it can vouch for it.")
	}
	labelled := make([]trayQuestionChoice, len(choices))
	for i, choice := range choices {
		labelled[i] = choice
		switch choice.Option {
		case "keep":
			labelled[i].Label = "Allow"
		case "remove":
			labelled[i].Label = "Don't allow"
		}
	}
	choices = labelled
	view := consentDisplayView(trayQuestionPresentation{
		presentation: catalogueQuestionFamily(record.Key),
		Title:        "OpenAbstractions",
		Instruction:  instruction,
		Content:      content,
		Details:      strings.Join(details, "\n"),
		Choices:      choices,
		Default:      trayDefaultChoice(record.Options),
		Dismiss:      trayNotNow,
		Level:        promptLevelFor(promptEvidence{OpenAbstractions: true, Program: program, Publisher: publisher}),
	}, publisher)
	view.Permission.Publisher = line
	return view
}

// unsignedAnswerNotice is the sentence that confirms the person's answer
// about an unsigned program: its Keep question or the review of its changed
// image. ok is false for every other question and answer.
func unsignedAnswerNotice(record askclient.RecordMetadata, publisher trayPublisher, retained *askclient.CatalogueContent, option string) (string, bool) {
	if retained == nil {
		return "", false
	}
	if _, title, ok := selfRegisteredTarget(record.Key, record.About, "", retained); ok && !publisher.Trusted {
		switch option {
		case "keep":
			return title + " stays in OpenAbstractions.", true
		case "remove":
			return title + " was removed from OpenAbstractions.", true
		}
		return "", false
	}
	if record.Key != asks.ProgramReviewKey {
		return "", false
	}
	title, ok := asks.ProgramReviewUnsigned(retained.Slots)
	if !ok {
		return "", false
	}
	switch option {
	case "keep":
		return title + " will keep working.", true
	case "not_now":
		return title + " is paused until you review it.", true
	}
	return "", false
}

// consentDisplayView fills the existing themed consent display content after
// Asks selects consent. It preserves the recorded words and options;
// this private display projection grants no right and records no answer.
func consentDisplayView(view trayQuestionPresentation, publisher trayPublisher) trayQuestionPresentation {
	view.PermissionContent = view.Content
	view.Permission = &trayPermissionPresentation{
		Instruction: view.Instruction, Content: view.Content, Details: view.Details, Footer: view.Footer,
		Question: view.Instruction, Scope: view.Content, Publisher: promptPublisherSummary(publisher),
	}
	// A blue header without a verified publisher is OpenAbstractions asking
	// on its own behalf (promptLevelFor): like Windows' Settings dialogs it
	// shows no publisher line and no program icon.
	if view.Level == promptLevelBlue && !publisher.Trusted {
		view.Permission.Publisher = ""
		view.IconProgram = ""
	}
	if view.Warning != "" {
		warning := view.Warning
		view.Permission.Warning = &warning
	}
	return view
}

// popupTitle is every native popup's window title (architect popup ruling
// 2026-11-10): the heading names the app and the action.
const popupTitle = "OpenAbstractions"

// promptPublisherSummary is the one inline publisher line a popup shows,
// only for a publisher that is not verified. A verified publisher goes to
// the details (promptPublisherDetail).
func promptPublisherSummary(publisher trayPublisher) string {
	if publisher.Trusted {
		return ""
	}
	return "Publisher not verified."
}

// promptPublisherDetail is the publisher line in a popup's details.
func promptPublisherDetail(publisher trayPublisher) string {
	switch {
	case publisher.Trusted && publisher.Subject != "":
		return "Publisher: " + publisher.Subject + " (verified)"
	case publisher.Trusted:
		return "Publisher: verified; no name was recorded"
	default:
		return "Publisher: not verified"
	}
}

// promptScriptHostLine is the one inline line a popup shows for a shared
// interpreter that runs an unverified script; the interpreter's own words
// (scriptHostSentence) go to the details.
const promptScriptHostLine = "It runs scripts that aren't verified."

// promptCloseDetail is the details' one statement of what closing means.
const promptCloseDetail = "Closing this window means Not now."

// trayQuestionViewForAsker is trayQuestionViewForPermission with the bound
// program that asked (asks ASK-A2, ASK-O11), empty when the service recorded
// none. The prompt names the asker by the display name derived from that
// program, and its details carry the program, so two programs with one file
// name read apart.
func trayQuestionViewForAsker(record askclient.RecordMetadata, publisher trayPublisher, permission *askclient.PermissionRequest, askerProgram string) trayQuestionPresentation {
	return trayQuestionViewForContent(record, publisher, permission, askerProgram, nil)
}

// trayQuestionViewForContent adds the service's retained display slots. The
// current typed permission remains the separate permission argument.
func trayQuestionViewForContent(record askclient.RecordMetadata, publisher trayPublisher, permission *askclient.PermissionRequest, askerProgram string, retained *askclient.CatalogueContent) trayQuestionPresentation {
	choices := questionChoices(record.Options)
	if view, handled := retainedCatalogueQuestionView(record, publisher, retained); handled {
		return view
	}
	details := []string{}
	if permission == nil {
		if program, title, ok := selfRegisteredTarget(record.Key, record.About, askerProgram, retained); ok {
			return keepRegisteredView(record, publisher, program, title, choices)
		}
		if record.Key == asks.AgentUpdateKey {
			if view, ok := agentUpdateQuestionView(record, publisher, choices, time.Now()); ok {
				return consentDisplayView(view, publisher)
			}
		}
		asker := record.Asker
		if askerProgram != "" {
			asker = asks.AskerDisplay(askerProgram)
		}
		name := questionAskerName(publisher, askerProgram, asker)
		// The heading is the asker's catalogue question; a question whose
		// catalogue entry has popup words reads them from its retained slots
		// (architect popup ruling 2026-11-10, rows 1, 7 and 24 to 26).
		heading, content := record.Text, ""
		// The catalogue text opens with the asker's recorded file name; the
		// heading names it as the person knows it.
		if rest, ok := strings.CutPrefix(record.Text, record.Asker+" "); ok && record.Asker != "" && name != "" {
			heading = name + " " + rest
		}
		if retained != nil && name != "" {
			if words, ok := asks.QuestionPresentation(record.Key, name, retained.Slots); ok {
				heading, content = words.Heading, words.Body
				if words.Details != "" {
					details = append(details, strings.Split(words.Details, "\n")...)
				}
			}
		}
		if asker != "" {
			details = append(details, "Requesting application: "+asker)
		}
		if askerProgram != "" {
			details = append(details, "Program: "+askerProgram)
		}
		if publisher.Trusted {
			details = append(details, promptPublisherDetail(publisher))
		}
		if record.Asked != "" {
			details = append(details, "Asked: "+record.Asked)
		}
		details = append(details, "Question: "+record.ID)
		choices, notNow := projectCataloguePopupChoices(record.Key, record.Options, choices)
		view := trayQuestionPresentation{
			presentation: catalogueQuestionFamily(record.Key),
			Title:        popupTitle,
			Instruction:  heading,
			Content:      content,
			Details:      strings.Join(details, "\n"),
			Choices:      choices,
			Default:      notNow,
			// Escape and the close box mean Not now; no Cancel duplicates it.
			Dismiss: notNow,
			Level:   promptLevelFor(promptEvidence{Program: askerProgram, Publisher: publisher}),
		}
		if record.Key == asks.PairingKey {
			// Runtime-only occurrence; exact directional wording is supplied
			// by the frozen agreement under the owner's approved copy.
			view.Instruction = "Pair these computers?"
			view.Content = ""
			view.Default = "refuse"
			view.Dismiss = "" // closing a pairing leaves it unanswered
			for i := range view.Choices {
				switch view.Choices[i].Option {
				case "once":
					view.Choices[i].Label = "Pair with these permissions"
				case "refuse":
					view.Choices[i].Label = "Cancel"
				}
			}
			// This runtime-only key already binds the exact frozen text and
			// occurrence. Reuse native consent appearance without constructing
			// a typed Rights request or deriving facts from question prose.
			body, rest, technical, canonical := pairingDisplayText(record.Text)
			view.Content = body
			if canonical {
				view.Details += "\n\n" + rest + "\n\n" + technical
			}
			view.Details += "\n\nEach computer approves separately. A lost reply can leave the outcome uncertain. Cancel refuses this pairing attempt. Closing this question leaves it unanswered."
			view.Details += "\n\nFull pairing agreement:\n" + record.Text
			view.PermissionContent = view.Content
			empty := ""
			view.Permission = &trayPermissionPresentation{
				Instruction: view.Instruction, Content: view.Content,
				Details: view.Details, Warning: &empty,
				Question: view.Instruction, Scope: view.Content,
			}
		}
		if view.presentation == nativeQuestionConsent {
			return consentDisplayView(view, publisher)
		}
		return view
	}
	primary := permission.Targets[0]
	program, action, resource := primary.Program, primary.Action, primary.Resource
	// A paired computer's send permit and its permit to wake existing
	// conversations both name that computer (VISION 2026-10-31).
	remoteMessage := remote.ValidSubjectProgram(program) && (action == grants.ApplicationMessageAction || action == grants.ApplicationMessageStartAction)
	// Registered names stay available for the receiver's display name even
	// when a paired sender's publisher is withheld.
	names := publisher.Names
	if remoteMessage {
		publisher = trayPublisher{Names: names}
	}

	// Architect popup ruling 2026-11-10, row 2: the heading is one plain
	// question, the body only the inline publisher and script-host lines,
	// and everything else sits behind Show details.
	name := programDisplayName(publisher, program)
	remoteDisplay := ""
	if remoteMessage {
		name = "Paired computer"
		remoteDisplay = "Paired computer (display unavailable)."
		if record.Asker != "" && len(record.Asker) <= 1024 {
			remoteDisplay = strings.Map(func(r rune) rune {
				if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
					return -1
				}
				return r
			}, record.Asker)
		}
	}
	question := "Allow " + name + " to " + firstUseHeadingPhrase(names, action, resource) + "?"
	// OpenAbstractions' own programs, the agent connectors included, carry no
	// Publisher line (architect ruling C, 2026-11-08).
	ownProgram := !remoteMessage && names.oaOwnProgram(program)
	visible := []string{}
	if remoteDisplay != "" {
		visible = append(visible, remoteDisplay)
	}
	if line := promptPublisherSummary(publisher); line != "" && !ownProgram {
		visible = append(visible, line)
	}
	scriptSentence := scriptHostSentence(publisher, program)
	if scriptSentence != "" {
		visible = append(visible, promptScriptHostLine)
	}
	details = append(details, "Request: "+firstUseRequestNamed(names, action, resource)+".", "", "Program: "+program)
	if !ownProgram {
		details = append(details, promptPublisherDetail(publisher))
	}
	if publisher.Trusted && publisher.Description != "" {
		details = append(details, "Description: "+publisher.Description)
	}
	if publisher.Trusted && publisher.OriginalName != "" {
		details = append(details, "Original file name: "+publisher.OriginalName)
	}
	if scriptSentence != "" {
		details = append(details, scriptSentence)
	}
	details = append(details, "Account: "+primary.Account, "Action: "+action, "Resource: "+resource)
	for _, target := range permission.Targets[1:] {
		details = append(details, "", "Also applies to:", "Account: "+target.Account, "Program: "+target.Program,
			"Action: "+target.Action, "Resource: "+target.Resource)
	}
	extras := []string{}
	if _, providerPair := asks.ProviderPermissionName(*permission); providerPair {
		extras = append(extras, "This covers applying the saved credential and using the model server.")
	} else if len(permission.Targets) > 1 {
		extras = append(extras, "This also covers reading this app's content back.")
	}
	if strings.ContainsAny(resource, "*?") {
		extras = append(extras, "Target: "+quoteName(resource)+".")
	}
	if disclosure := grants.FirstUseDisclosure(action, resource); disclosure != "" {
		extras = append(extras, disclosure)
	}
	if definitionDetails := grants.FirstUseDefinitionDetails(action, resource); definitionDetails != "" {
		extras = append(extras, definitionDetails)
	}
	// A start permit is bound to its body sentence (serve's
	// messageStartWording): the popup shows that sentence, and its details
	// say the receiver reads any of its conversations (VISION 2026-10-31,
	// 2026-10-23).
	scope := ""
	if display, ok := messageStartDisplay(publisher.Names, action, resource); ok {
		scope = grants.MessageStartBody(display)
		extras = append(extras, grants.MessageStartDetails(display))
	}
	if len(extras) > 0 {
		details = append(append(details, ""), extras...)
	}
	if record.Asked != "" {
		details = append(details, "Asked: "+record.Asked)
	}
	details = append(details, "Question: "+record.ID, "", "Scope: Applies to this app, this action and this resource.")
	if remoteMessage {
		details = append(details, "This question concerns this paired computer and this local receiver.")
	}
	never := hasTrayOption(record.Options, "never")
	if hasTrayOption(record.Options, "allow") {
		if remoteMessage {
			details = append(details, "Allow saves ongoing access for the paired computer to this receiver.")
		} else {
			details = append(details, "Allow adds an ongoing permission for this exact application, action and resource.")
		}
	}
	if never {
		details = append(details, "Never allow adds an ongoing block for this exact application, action and resource.")
	}
	if hasTrayOption(record.Options, "refuse") {
		details = append(details, "Not now saves no permission.")
	}
	details = append(details, promptCloseDetail)
	var tier asks.Tier
	for _, target := range permission.Targets {
		tier = higherTier(tier, promptTier(target.Action, target.Resource))
	}
	iconProgram := permissionIconProgram(askerProgram, program, publisher)
	if remoteMessage {
		iconProgram = ""
	}
	warning := ""
	publisherLines := strings.Join(visible, "\n")
	body := strings.TrimSpace(scope + "\n" + publisherLines)
	detailText := strings.Join(details, "\n")
	notNow := trayDefaultChoice(record.Options)
	return trayQuestionPresentation{
		presentation: catalogueQuestionFamily(record.Key),
		Level:        promptLevelFor(promptEvidence{Program: program, Publisher: publisher, Tier: tier}),
		Title:        popupTitle,
		Instruction:  question,
		Content:      body,
		Details:      detailText,
		Choices:      choices,
		Default:      notNow,
		// Escape and the close box mean Not now.
		Dismiss: notNow,
		Permission: &trayPermissionPresentation{
			Instruction:      question,
			Content:          body,
			Details:          detailText,
			Warning:          &warning,
			Question:         question,
			Requester:        name,
			Publisher:        publisherLines,
			Scope:            scope,
			RequesterProgram: iconProgram,
		},
	}
}

// messageStartDisplay is a start permit's receiver as its question names it:
// the registered display name, else the quoted registered name.
func messageStartDisplay(names *programTitler, action, resource string) (string, bool) {
	if action != grants.ApplicationMessageStartAction {
		return "", false
	}
	application, ok := grants.MessageStartResource(resource)
	if !ok {
		return "", false
	}
	if title, ok := names.applicationTitle(application); ok {
		return title, true
	}
	return quoteName(application), true
}

// firstUseHeadingPhrase is what a first-use heading asks for: the action in
// plain words, and its object only where the object is the person's own
// words for it, so no internal server, host or registry name reaches the
// heading (architect popup ruling 2026-11-10). The complete request stays
// in the details.
func firstUseHeadingPhrase(names *programTitler, action, resource string) string {
	verb, object := plainFirstUseNamed(names, action, resource)
	switch action {
	case grants.ApplicationMessageStartAction:
		return verb + " " + object
	case grants.ApplicationMessageAction:
		// "to “codex”, which receives them as tasks …" reads up to its comma.
		if to, _, ok := strings.Cut(object, ","); ok && !strings.Contains(to, " on paired computer ") {
			return verb + " " + to
		}
	}
	return verb
}

// pairingDisplayText only formats the runtime's known question template. It
// reads fixed prose boundaries, never authority or values from the prose. A
// shape it does not recognize stays wholly visible; the caller also retains
// the exact original agreement in Details in every case.
//
// The visible body is the owner-approved check sentence and both allows
// lists; rest is the remainder of the agreement, from "Permissions stay until
// removed in the Panel.", which the details show.
func pairingDisplayText(text string) (body, rest, technical string, canonical bool) {
	const title = "Pair these computers? "
	const boundary = " Details: Verified peer key "
	const stay = " Permissions stay until removed in the Panel."
	if !strings.HasPrefix(text, title+"Check that both computers show ") || strings.Count(text, boundary) != 1 {
		return text, "", "", false
	}
	body, tail, _ := strings.Cut(text, boundary)
	ordered := func(value string, markers ...string) bool {
		after := 0
		for _, marker := range markers {
			at := strings.Index(value, marker)
			if strings.Count(value, marker) != 1 || at < after {
				return false
			}
			after = at + len(marker)
		}
		return true
	}
	if !strings.HasSuffix(body, " Each computer approves separately.") ||
		!ordered(body, " This computer allows: ", " The other computer allows: ", " Permissions stay until removed in the Panel.") ||
		!strings.HasSuffix(tail, ".") ||
		!ordered(tail, ". Local binding ", ". This computer's source: ", ". The other computer's source: ", ". Request ", " expires ") {
		return text, "", "", false
	}
	technical = strings.TrimPrefix(boundary, " ") + tail
	body = strings.TrimPrefix(body, title)
	body, rest, _ = strings.Cut(body, stay)
	rest = strings.TrimPrefix(stay, " ") + rest
	for _, marker := range []string{" This computer allows: ", " The other computer allows: "} {
		body = strings.Replace(body, marker, "\n\n"+strings.TrimPrefix(marker, " "), 1)
	}
	return body, rest, technical, true
}

// questionChoices labels a question's options the way every visible surface
// shows them: the native prompt's buttons and the Questions page's. Refuse
// reads Not now everywhere (vocabulary §8 V6).
func questionChoices(options []string) []trayQuestionChoice {
	choices := make([]trayQuestionChoice, 0, len(options))
	for _, option := range options {
		if option == "" {
			continue
		}
		choices = append(choices, trayQuestionChoice{Label: trayQuestionOptionLabel(option), Option: option})
	}
	return choices
}

// projectCataloguePopupChoices applies catalogue display policy to the
// options present in this occurrence, retaining their order.
func projectCataloguePopupChoices(key string, options []string, choices []trayQuestionChoice) ([]trayQuestionChoice, string) {
	policy, ok := catalogue.QuestionPopupChoices(key)
	if !ok {
		return choices, trayDefaultChoice(options)
	}
	out := []trayQuestionChoice{}
	for _, choice := range choices {
		if label, ok := policy.Labels[choice.Option]; ok {
			out = append(out, trayQuestionChoice{Label: label, Option: choice.Option})
		}
	}
	if policy.CloseLabel != "" {
		return append(out, trayQuestionChoice{Label: policy.CloseLabel, Option: trayNotNow}), trayNotNow
	}
	return out, policy.DefaultOption
}

// firstUseRequest words what a first-use question asks for: plainFirstUse's
// verb and, when the resource is a plain name, its object.
func firstUseRequest(action, resource string) string {
	return firstUseRequestNamed(nil, action, resource)
}

// firstUseRequestNamed is firstUseRequest with registered names at hand, so
// a start permit names its receiver by display name.
func firstUseRequestNamed(names *programTitler, action, resource string) string {
	verb, object := plainFirstUseNamed(names, action, resource)
	if object != "" {
		return verb + " " + object
	}
	return verb
}

// firstUseSentence is the one sentence that names a first-use question
// wherever a person reads it: the native prompt, its notification and the
// Panel's Questions page (live walkthrough 2026-10-02, D5). It names the
// program as programDisplayName does and carries no path, id or hash.
func firstUseSentence(publisher trayPublisher, program, action, resource string) string {
	sentence := programDisplayName(publisher, program) + " wants to " + firstUseRequestNamed(publisher.Names, action, resource) + "."
	if disclosure := grants.FirstUseDisclosure(action, resource); disclosure != "" {
		sentence += " " + disclosure
	}
	return sentence
}

// publisherOfRead is the publisher a read question carries: the zero value
// unless the runtime recorded a trusted signature at admission. The Tray
// prompt, the Questions page and the notification balloon all build the
// requester's publisher here.
func publisherOfRead(read askclient.OperatorQuestion) trayPublisher {
	if !read.PublisherTrusted {
		return trayPublisher{}
	}
	return trayPublisher{Subject: read.Publisher, Trusted: true,
		Description: read.ProgramDescription, OriginalName: read.ProgramOriginalName}
}

// maxProgramName bounds a signed description shown as a program's name, in
// runes, as plainName bounds a resource name. The full description stays in
// the details.
const maxProgramName = 64

// programDisplayName names the requesting program in visible prompt and page
// text by the program-title rule (program_titles.go). Panel pages put the
// title first: OpenAbstractions' own or a registered app's title, else the
// recorded signed description when the signature was trusted. The native
// prompt (Prompt) puts a verified signed description first and uses the
// title only for an unsigned program. Otherwise a Panel page reads a program
// as its file stem and "(not registered)", never its file name. A popup or
// notification (Prompt) names it as questionAskerName does, by its file stem
// alone: no heading or identity row carries "(not registered)" (owner, popup
// wording 2026-11-10). A description is shortened to maxProgramName runes.
func programDisplayName(publisher trayPublisher, program string) string {
	signed := publisher.Trusted && publisher.Description != ""
	if !(publisher.Prompt && signed) {
		if title, ok := publisher.Names.title(program); ok {
			return title
		}
	}
	if !signed {
		if publisher.Prompt {
			return questionAskerName(publisher, program, "")
		}
		return unregisteredProgramName(program)
	}
	runes := []rune(publisher.Description)
	if len(runes) <= maxProgramName {
		return publisher.Description
	}
	return string(runes[:maxProgramName-1]) + "…"
}

// permissionIconProgram is the program whose own icon the native permission
// prompt shows beside the requester's name, or "" for the generic application
// icon. Both inputs are bound by the service from platform proof, never from
// question text: askerProgram is the record's bound asker (ASK-A2, ASK-O11),
// absent for the runtime's own permission questions, and subject is the
// permission's retained subject program (ASK-A1), the program the prompt
// names. An asker that is not that subject gets the generic icon. A script
// host's icon would show the interpreter, not the unverified script that asks
// (ASK-A8), so it gets the generic icon too.
func permissionIconProgram(askerProgram, subject string, publisher trayPublisher) string {
	program := subject
	if askerProgram != "" {
		if subject != "" && !identity.SameSubjectProgram(askerProgram, subject) {
			return ""
		}
		program = askerProgram
	}
	// A UNC, device or network-drive program is never read for an icon: the
	// read would contact that host with the account's credentials.
	if program == "" || !filepath.IsAbs(program) || identity.LocalPath(program) != nil {
		return ""
	}
	if asks.ScriptHostSubject(rwire.Subject{Program: program}, publisher.asksPublisher()) {
		return ""
	}
	return program
}

// scriptHostSentence is the sentence asks gives a question whose recorded
// program is a shared interpreter with a trusted publisher (ASK-A8), naming
// the host as programDisplayName does, or "". It reads the same
// ScriptHostSubject rule as the prompt's level and icon, so the three agree.
func scriptHostSentence(publisher trayPublisher, program string) string {
	evidence := publisher.asksPublisher()
	if evidence.Description != "" {
		evidence.Description = programDisplayName(publisher, program)
	}
	return asks.ScriptHostSentence(program, evidence)
}

// trayLendAction is serve's ActionLend (runtime_lending.go), named by its exact
// string because serve is a main package the monitor cannot import.
const trayLendAction = "abstraction.storage/lend"

// plainFirstUseNamed fills a start permit's receiver in by its registered
// display name, at the moment the question is shown (review 31): "ask Codex
// to read any of your Codex conversations ...". Without a registered title it
// is plainFirstUse.
func plainFirstUseNamed(names *programTitler, action, resource string) (verb, object string) {
	if action == grants.ApplicationMessageStartAction {
		if application, ok := grants.MessageStartResource(resource); ok {
			if title, ok := names.applicationTitle(application); ok {
				return grants.MessageStartWords(title)
			}
		}
	}
	return plainFirstUse(action, resource)
}

// plainFirstUse uses the shared compiled definition projection. Unknown
// definitions retain the existing Rights page display phrase.
func plainFirstUse(action, resource string) (verb, object string) {
	if verb, object, ok := grants.FirstUseWords(action, resource); ok {
		return verb, object
	}
	verb = "use an OpenAbstractions capability"
	if phrase := rightsActionPlain[action]; phrase != "" {
		verb = phrase
	}
	if plainName(resource) {
		return verb, "on " + quoteName(resource)
	}
	return verb, ""
}

// plainName accepts a short name a person can read: no path separator, no
// long hexadecimal run and no character outside letters, digits and . _ - :
func plainName(value string) bool {
	if value == "" || len(value) > 64 || strings.ContainsAny(value, `/\`) {
		return false
	}
	hex := 0
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F':
			hex++
			if hex >= 16 {
				return false
			}
		default:
			hex = 0
		}
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_' || r == '-' || r == ':' || r == ' ') {
			return false
		}
	}
	return true
}

func quoteName(value string) string { return "“" + value + "”" }

// trayDefaultChoice is the option Enter or Space selects in the prompt:
// refuse, which writes no rule, or no. Any other question has no default
// answer and Enter selects Cancel. allow, once and never are never defaults,
// since each writes or grants something.
func trayDefaultChoice(options []string) string {
	for _, safe := range []string{"refuse", "no", "not_now"} {
		if hasTrayOption(options, safe) {
			return safe
		}
	}
	return ""
}

func hasTrayOption(options []string, want string) bool {
	for _, option := range options {
		if option == want {
			return true
		}
	}
	return false
}

func trayQuestionOptionLabel(option string) string {
	switch strings.ToLower(option) {
	case "allow":
		return "Allow"
	case "once":
		return "Allow once"
	case "never":
		return "Never allow"
	case "refuse":
		return "Not now"
	case "yes":
		return "Yes"
	case "no":
		return "No"
	case "keep":
		return "Keep"
	case "update":
		return "Update"
	case "setup":
		return "Set up"
	case "not_now":
		return "Not now"
	case "remove":
		return "Remove"
	default:
		return option
	}
}

// trayDialogText makes control and bidi-format characters visible in native
// controls. TaskDialog renders plain text, but hidden direction overrides can
// still make a verified executable path appear to name another program.
func trayDialogText(value string) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' || unicode.Is(unicode.Cf, r) {
			fmt.Fprintf(&b, "\\u%04X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

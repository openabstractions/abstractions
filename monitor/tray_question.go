package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// trayAnswerBudget bounds one re-read and answer of a shown question.
const trayAnswerBudget = 5 * time.Second

type trayQuestionChoice struct {
	Label  string `json:"label"`
	Option string `json:"option"`
}

// trayQuestionPresentation is the native prompt's text. Instruction and
// Content are visible and name no path, id or hash; Details sits behind the
// prompt's expander and carries the exact program, action and resource.
type trayQuestionPresentation struct {
	Title       string
	Instruction string
	Content     string
	Details     string
	Footer      string
	// Warning is a trusted permission disclosure. The Mac launcher selects the
	// native caution style when it is present; Content retains the whole text.
	Warning string `json:",omitempty"`
	// PermissionContent selects the bounded AppKit permission body. Warning and
	// answer effects stay visible beside it. Empty keeps the legacy frame shape.
	PermissionContent string `json:",omitempty"`
	Choices           []trayQuestionChoice
	// Default is the option Enter selects: the refusing one, or none.
	Default string
	// Permission is set only by the typed permission projection. Windows uses
	// this private display view; the native Mac bridge retains its frame shape.
	Permission *trayPermissionPresentation `json:"-"`
	// AppChoice is set by the typed ordinary-record projection. Application
	// titles and option words never choose native permission presentation.
	AppChoice bool `json:",omitempty"`
	// PermissionPrompt and IconProgram are set by the Mac bridge from
	// Permission: the launcher plays the alert sound for a permission prompt
	// and shows IconProgram's app bundle icon (permissionIconProgram's choice;
	// empty keeps OA's own icon).
	PermissionPrompt bool   `json:",omitempty"`
	IconProgram      string `json:",omitempty"`
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
//	blue    OpenAbstractions' own words, or the Panel's own program
//	gray    a verified publisher
//	yellow  everything else: an unverified or unsigned requester, or none
//
// The runtime programs (openabstractions, openabstractionsw) are a CLI any
// process or agent can drive, so a request through them is judged like any
// other requester's.
func promptLevelFor(e promptEvidence) promptLevel {
	switch {
	case e.Tier == asks.TierAdministrative || e.AllItems || asks.ScriptHostSubject(rwire.Subject{Program: e.Program}, e.Publisher.asksPublisher()):
		return promptLevelOrange
	case e.OpenAbstractions || oaPanelProgram(e.Program):
		return promptLevelBlue
	case e.Publisher.Trusted:
		return promptLevelGray
	}
	return promptLevelYellow
}

// reportPromptLevel is the level of the report OpenAbstractions shows after
// an answer: its own words.
func reportPromptLevel() promptLevel {
	return promptLevelFor(promptEvidence{OpenAbstractions: true})
}

// oaPanelProgram reports whether program is the Panel's own program, by the
// program-title rule's list (program_titles.go). The runtime programs on that
// list are excluded: others can drive them.
func oaPanelProgram(program string) bool {
	if program == "" {
		return false
	}
	for _, own := range ownProgramTitles() {
		if own.title == oaPanelTitle && identity.SameSubjectProgram(own.program, program) {
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

// selfRegisteredWords reads the runtime's question about an app that added
// itself (asks SelfRegisteredKey, ASK-R11): the program its registration
// bound, and the sentence a person reads, which names the app by its title
// and the program by its file stem. ok is false for any other record and for
// one that names an asker program, which only an application's own question
// does; such a record keeps the generic wording.
func selfRegisteredWords(record askclient.RecordMetadata, askerProgram string) (program, sentence string, ok bool) {
	if askerProgram != "" {
		return "", "", false
	}
	program, title, ok := asks.SelfRegistered(record.Key, record.About, record.Text)
	if !ok {
		return "", "", false
	}
	return program, title + " (" + programStem(program) + ") added itself to OpenAbstractions. Keep it?", true
}

// selfRegisteredView words the runtime's question about an app that added
// itself: the title and program stem in the question, the program's path and
// the publisher bound from its registration in the details. Closing the
// window leaves the entry unconfirmed.
func selfRegisteredView(record askclient.RecordMetadata, publisher trayPublisher, program, sentence string, choices []trayQuestionChoice) trayQuestionPresentation {
	publisherLine := "Publisher: not verified. No trusted signature was recorded when the app added itself."
	switch {
	case publisher.Trusted && publisher.Subject != "":
		publisherLine = "Publisher: " + publisher.Subject + " (signature verified when the app added itself)."
	case publisher.Trusted:
		publisherLine = "Publisher: signature verified when the app added itself; no publisher name was recorded."
	}
	details := []string{"Program: " + program, publisherLine}
	if publisher.Trusted && publisher.Description != "" {
		details = append(details, "Description: "+publisher.Description)
	}
	if publisher.Trusted && publisher.OriginalName != "" {
		details = append(details, "Original file name: "+publisher.OriginalName)
	}
	if record.Asked != "" {
		details = append(details, "Asked: "+record.Asked)
	}
	details = append(details, "Question: "+record.ID)
	return trayQuestionPresentation{
		Title:       trayDisplayName + " — Question",
		Instruction: "An app added itself",
		Content:     sentence,
		Details:     strings.Join(details, "\n"),
		Footer:      "Keep confirms it. Remove takes it off and stops it adding itself again. Closing this window leaves it unconfirmed.",
		Choices:     choices,
		Default:     trayDefaultChoice(record.Options),
		// OpenAbstractions asks this in its own words: blue, unless a row
		// above blue matches the program it names.
		Level: promptLevelFor(promptEvidence{OpenAbstractions: true, Program: program, Publisher: publisher}),
	}
}

// trayQuestionViewForAsker is trayQuestionViewForPermission with the bound
// program that asked (asks ASK-A2, ASK-O11), empty when the service recorded
// none. The prompt names the asker by the display name derived from that
// program, and its details carry the program, so two programs with one file
// name read apart.
func trayQuestionViewForAsker(record askclient.RecordMetadata, publisher trayPublisher, permission *askclient.PermissionRequest, askerProgram string) trayQuestionPresentation {
	choices := questionChoices(record.Options)
	details := []string{}
	if permission == nil {
		if program, sentence, ok := selfRegisteredWords(record, askerProgram); ok {
			return selfRegisteredView(record, publisher, program, sentence, choices)
		}
		asker := record.Asker
		if askerProgram != "" {
			asker = asks.AskerDisplay(askerProgram)
		}
		name := programStem(asker)
		if askerProgram != "" {
			name = publisher.Names.name(askerProgram)
		}
		instruction := "A question needs your answer"
		if name != "" {
			instruction = name + " needs your answer"
		}
		if asker != "" {
			details = append(details, "Requesting application: "+asker)
		}
		if askerProgram != "" {
			details = append(details, "Program: "+askerProgram)
		}
		if record.Asked != "" {
			details = append(details, "Asked: "+record.Asked)
		}
		details = append(details, "Question: "+record.ID)
		footer := "Your answer is recorded by OpenAbstractions. The requesting application must retry its refused action."
		view := trayQuestionPresentation{
			Title:       trayDisplayName + " — Question",
			Instruction: instruction,
			Content:     record.Text,
			Details:     strings.Join(details, "\n"),
			Footer:      footer,
			Choices:     choices,
			Default:     trayDefaultChoice(record.Options),
			Level:       promptLevelFor(promptEvidence{Program: askerProgram, Publisher: publisher}),
		}
		if record.Key == asks.SystemSecurityLogKey {
			// ASK-S1: the service-owned key, never the text, selects this.
			view.Title = trayDisplayName + " — Administrator access"
			view.Warning = "Windows asks for administrator approval next."
			view.Footer = "Allow once reads one page after Windows approves. Refuse and Never allow read nothing."
		}
		return view
	}
	primary := permission.Targets[0]
	program, action, resource := primary.Program, primary.Action, primary.Resource

	name := programDisplayName(publisher, program)
	verb, _ := plainFirstUse(action, resource)
	content := firstUseSentence(publisher, program, action, resource)
	publisherSummary := "Publisher: not verified."
	publisherDetails := "Publisher: not verified. No trusted signature was recorded when the application asked."
	switch {
	case publisher.Trusted && publisher.Subject != "":
		publisherSummary = "Publisher: " + publisher.Subject + " (verified at request)."
		publisherDetails = "Publisher: " + publisher.Subject + " (signature verified when the application asked)."
	case publisher.Trusted:
		publisherSummary = "Publisher: verified signature; name unavailable."
		publisherDetails = "Publisher: signature verified when the application asked; no publisher name was recorded."
	}
	content += "\n\n" + publisherDetails
	scriptSentence := scriptHostSentence(publisher, program)
	if sentence := scriptSentence; sentence != "" {
		content += "\n\n" + sentence
	}
	details = append(details, "Program: "+program)
	if publisher.Trusted && publisher.Description != "" {
		details = append(details, "Description: "+publisher.Description)
	}
	if publisher.Trusted && publisher.OriginalName != "" {
		details = append(details, "Original file name: "+publisher.OriginalName)
	}
	details = append(details, "Account: "+primary.Account, "Action: "+action, "Resource: "+resource)
	for _, target := range permission.Targets[1:] {
		details = append(details, "", "Also applies to:", "Account: "+target.Account, "Program: "+target.Program,
			"Action: "+target.Action, "Resource: "+target.Resource)
	}
	if record.Asked != "" {
		details = append(details, "Asked: "+record.Asked)
	}
	details = append(details, "Question: "+record.ID)
	footer := "The requesting application was refused and must retry after you answer."
	if hasTrayOption(record.Options, "allow") {
		footer += " Allow adds an ongoing permission for this exact application, action and resource."
	}
	if hasTrayOption(record.Options, "never") {
		footer += " Never allow adds an ongoing block for this exact application, action and resource."
	}
	if hasTrayOption(record.Options, "refuse") {
		footer += " Refuse records no permission rule."
	}
	if len(permission.Targets) > 1 {
		footer += " The permission and block also cover reading this application's content back, as listed in Details."
	}
	visible := []string{publisherSummary}
	if scriptSentence != "" {
		visible = append(visible, scriptSentence)
	}
	requesterPublisher := strings.Join(visible, "\n\n")
	effects := []string{}
	if hasTrayOption(record.Options, "allow") {
		effects = append(effects, "Allow saves ongoing access for this app.")
	}
	if hasTrayOption(record.Options, "never") {
		effects = append(effects, "Never allow saves an ongoing block.")
	}
	if hasTrayOption(record.Options, "refuse") {
		effects = append(effects, "Refuse changes no permissions.")
	}
	if len(permission.Targets) > 1 {
		effects = append(effects, "This also covers reading this app's content back.")
	}
	if len(effects) > 0 {
		visible = append(visible, strings.Join(effects, " "))
	}
	scopeSentence := "Applies to this app, this action and this resource."
	if _, object, defined := grants.FirstUseWords(action, resource); defined && object != "" {
		scopeSentence = ""
	}
	if disclosure := grants.FirstUseDisclosure(action, resource); disclosure != "" {
		scopeSentence = disclosure
		visible = append(visible, disclosure)
	}
	if definitionDetails := grants.FirstUseDefinitionDetails(action, resource); definitionDetails != "" {
		details = append(details, "", definitionDetails)
	}
	warning := ""
	var tier asks.Tier
	for _, target := range permission.Targets {
		tier = higherTier(tier, promptTier(target.Action, target.Resource))
	}
	return trayQuestionPresentation{
		Level:       promptLevelFor(promptEvidence{Program: program, Publisher: publisher, Tier: tier}),
		Title:       trayDisplayName + " — Permission request",
		Instruction: "Allow " + name + " to " + verb + "?",
		Content:     content,
		Details:     strings.Join(details, "\n"),
		Footer:      footer,
		Choices:     choices,
		Default:     trayDefaultChoice(record.Options),
		Permission: &trayPermissionPresentation{
			Instruction:      "Allow " + name + " to " + firstUseRequest(action, resource) + "?",
			Content:          strings.Join(visible, "\n\n"),
			Details:          strings.Join(details, "\n") + "\n\n" + publisherDetails + "\n\nAnswer effects:\n" + footer,
			Footer:           "Cancel or Escape closes this window without answering.",
			Warning:          &warning,
			Question:         "Allow this app to " + firstUseRequest(action, resource) + "?",
			Requester:        name,
			Publisher:        requesterPublisher,
			RequesterProgram: permissionIconProgram(askerProgram, program, publisher),
			Scope:            scopeSentence,
			Effects:          strings.Join(effects, " "),
		},
	}
}

// questionChoices labels a question's options the way every visible surface
// shows them: the native prompt's buttons and the Questions page's.
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

// firstUseRequest words what a first-use question asks for: plainFirstUse's
// verb and, when the resource is a plain name, its object.
func firstUseRequest(action, resource string) string {
	verb, object := plainFirstUse(action, resource)
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
	sentence := programDisplayName(publisher, program) + " wants to " + firstUseRequest(action, resource) + "."
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
// title only for an unsigned program. Otherwise a program reads as its file
// stem and "(not registered)", never its file name. A description is
// shortened to maxProgramName runes.
func programDisplayName(publisher trayPublisher, program string) string {
	signed := publisher.Trusted && publisher.Description != ""
	if !(publisher.Prompt && signed) {
		if title, ok := publisher.Names.title(program); ok {
			return title
		}
	}
	if !signed {
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

// scriptHostSentence is the sentence asks gives a trusted script host
// (ASK-A8), naming the host as programDisplayName does, or "".
func scriptHostSentence(publisher trayPublisher, program string) string {
	if !publisher.Trusted || !asks.ScriptHost(publisher.OriginalName) {
		return ""
	}
	host := ""
	if publisher.Description != "" {
		host = programDisplayName(publisher, program)
	}
	return asks.ScriptHostSentence(host, publisher.OriginalName)
}

// trayLendAction is serve's ActionLend (runtime_lending.go), named by its exact
// string because serve is a main package the monitor cannot import.
const trayLendAction = "abstraction.storage/lend"

// plainFirstUse uses the shared compiled definition projection. Unknown
// definitions retain the existing Rights page display phrase.
func plainFirstUse(action, resource string) (verb, object string) {
	if verb, object, ok := grants.FirstUseWords(action, resource); ok {
		return verb, object
	}
	verb = "use an OpenAbstractions capability"
	if phrase := rightsActionPlain[action]; phrase != "" {
		verb = "use " + quoteName(phrase)
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
	for _, safe := range []string{"refuse", "no"} {
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
		return "Refuse"
	case "yes":
		return "Yes"
	case "no":
		return "No"
	case "keep":
		return "Keep"
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

func trayQuestionOutcomeMessage(outcome string) string {
	switch outcome {
	case "conflict":
		return "A different answer is already recorded. Review the current question in the Panel."
	case "unknown":
		return "This question is no longer available. Review the Questions page."
	case "forbidden":
		return "The Panel was not authorized to answer this question. No answer is assumed."
	case "unavailable":
		return "The question service is unavailable. No answer is assumed."
	case "invalid":
		return "The question service rejected this answer. Review the current question."
	case "gap":
		return "The question list changed. Refresh the Questions page before answering."
	default:
		return "The answer was not recorded. Review the current question in the Panel."
	}
}

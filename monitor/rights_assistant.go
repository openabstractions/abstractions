package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Assistants section of the Questions page: the person's one yes that
// lets a program answer every app's routine questions, Stop, and "Answered
// for you" with Undo (VISION 2026-10-03, "The 0.4.0 rights slice"; asks
// ASK-R5; rights RIGHTS-R16). The runtime enforces every rule here; this
// Panel words the yes, refuses what the runtime would refuse before it
// writes, and reads the record of each answer from asks (ASK-O7).

// assistantRules is the rights operator surface the section uses.
type assistantRules interface {
	ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error)
	SetRuleForContext(ctx context.Context, expected string, rule rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error)
	RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error)
	StopAnswererContext(ctx context.Context, expected string, answerer rightsclient.Subject, alsoRevokeGrants bool) (rightsclient.PolicyEdit, error)
	RevokeAnswerPermitsContext(ctx context.Context, expected string, answerer, target rightsclient.Subject, actions []string, resource, questionID string) (rightsclient.PolicyEdit, error)
	ReadRuleContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error)
}

// assistantRuleOperator resolves the rights operator per request. Tests
// replace it.
var assistantRuleOperator = func() assistantRules { return &panelRules{} }

func (r *panelRules) RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.RevokeRuleContext(ctx, expected, subject, action, resource)
}
func (r *panelRules) StopAnswererContext(ctx context.Context, expected string, answerer rightsclient.Subject, alsoRevokeGrants bool) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.StopAnswererContext(ctx, expected, answerer, alsoRevokeGrants)
}
func (r *panelRules) RevokeAnswerPermitsContext(ctx context.Context, expected string, answerer, target rightsclient.Subject, actions []string, resource, questionID string) (rightsclient.PolicyEdit, error) {
	operator, err := r.resolve(ctx)
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return operator.RevokeAnswerPermitsContext(ctx, expected, answerer, target, actions, resource, questionID)
}

// answeredReader is the asks operator surface "Answered for you" reads.
type answeredReader interface {
	askclient.DetailsReader
	ListQuestionsContext(ctx context.Context, cursor string, limit int64) (askclient.OperatorPage, error)
	ReadAttributedQuestionContext(ctx context.Context, id string) (askclient.AttributedQuestion, error)
	ListAnswersContext(ctx context.Context, cursor string, limit int64) (askclient.AnswerEntryPage, error)
}

// answeredQuestionOperator resolves the asks operator per request. Tests
// replace it.
var answeredQuestionOperator = func(ctx context.Context) (answeredReader, error) {
	return panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// assistantWarning is the text of the one yes (PROPOSAL §8.1, without the OS
// proof the owner deferred). Its lists come from the first-use tier table
// (grants.FirstUseTierClasses), so the Panel keeps no copy of the tiers.
type assistantWarning struct {
	Answers      []string `json:"answers"`
	StaysWithYou []string `json:"staysWithYou"`
	// Choices says the yes also covers the options apps share in their own
	// choices, and that such an answer has no Undo (asks ASK-R9).
	Choices string `json:"choices"`
	Caution string `json:"caution"`
	Ends    string `json:"ends"`
}

// stayWithYouAlways are the classes outside the first-use table that never
// reach an answerer (PROPOSAL §7.1, N1 to N8).
const stayWithYouAlways = "passwords and keys, new apps, network sharing, deleting your files, and anything marked sensitive"

func newAssistantWarning() assistantWarning {
	w := assistantWarning{Answers: []string{}, StaysWithYou: []string{},
		Choices: "It also picks among the options an app shares with it in the app's own question, such as which model to use. Its answer to an app's question cannot be undone, since the app may already have acted on it.",
		Caution: "runs an AI model. It can make mistakes, and text in a web page or a file could try to steer it. If it started the work that led an app to ask, it can still answer that app. You can see every answer it gives below, and undo the permissions it gave.",
		Ends:    "It stays on until you stop it here, and ends by itself after 30 days without an answer."}
	for _, class := range grants.FirstUseTierClasses {
		if class.Tier.Answerable() {
			w.Answers = append(w.Answers, class.Words)
		} else {
			w.StaysWithYou = append(w.StaysWithYou, class.Words)
		}
	}
	w.StaysWithYou = append(w.StaysWithYou, stayWithYouAlways)
	return w
}

// assistantEntry is one enrolled answerer.
type assistantEntry struct {
	Program string `json:"program"`
	Name    string `json:"name"`
	Expires string `json:"expires,omitempty"`
}

type assistantPage struct {
	Outcome    string           `json:"outcome"`
	Revision   string           `json:"revision,omitempty"`
	Assistants []assistantEntry `json:"assistants"`
	Candidates []string         `json:"candidates"`
	// Names titles every program the section shows (program_titles.go).
	Names   map[string]string `json:"names,omitempty"`
	Warning assistantWarning  `json:"warning"`
}

type assistantEdit struct {
	Action           string `json:"action"`
	Program          string `json:"program"`
	Revision         string `json:"revision"`
	Confirm          bool   `json:"confirm"`
	AlsoRevokeGrants bool   `json:"alsoRevokeGrants"`
	// DontAskAgain is "Stop and don't ask again": the deny Stop writes on
	// the enrollment stands until the person removes it (ASK-R10).
	DontAskAgain bool `json:"dontAskAgain"`
}

type assistantResult struct {
	Outcome string `json:"outcome"`
	Message string `json:"message"`
}

// policyPages bounds how many pages of rules the section reads.
const policyPages = 64

// listRules reads every retained rule at one revision.
func listRules(ctx context.Context, rules assistantRules) (string, []rightsclient.PolicyRule, string, error) {
	var all []rightsclient.PolicyRule
	cursor, revision := "", ""
	for range policyPages {
		page, err := rules.ListPolicyContext(ctx, cursor, 64)
		if err != nil {
			return "", nil, "", err
		}
		if page.Outcome.String() != "page" {
			return "", nil, page.Outcome.String(), nil
		}
		if revision != "" && page.Revision != revision {
			return "", nil, "gap", nil
		}
		revision = page.Revision
		all = append(all, page.Rules...)
		if page.Complete {
			return revision, all, "page", nil
		}
		cursor = page.Next
	}
	return "", nil, "gap", nil
}

// assistantName is how the Answering access sentences name a program
// (programTitle): OpenAbstractions' own and registered apps by title, any
// other by its file stem and "(not registered)".
func (p *servicePanel) assistantName(ctx context.Context, program string) string {
	return p.programTitle(ctx, program)
}

// assistant answers GET with the enrolled answerers and the yes's words,
// and POST with an enrollment or a Stop.
func (p *servicePanel) assistant(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var edit assistantEdit
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&edit); err != nil ||
			(edit.Action != "enroll" && edit.Action != "stop") || !identity.ValidSubjectProgram(edit.Program) || !validDelegationWord(edit.Revision, 128) {
			http.Error(w, "choose a program and Let it answer, or Stop", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	account := probe.Self().Account
	if !validDelegationWord(account, 128) {
		http.Error(w, "Panel account identity is unavailable", http.StatusServiceUnavailable)
		return
	}
	rules := assistantRuleOperator()
	if r.Method == http.MethodGet {
		revision, all, outcome, err := listRules(ctx, rules)
		if err != nil {
			panelError(w, err)
			return
		}
		page := assistantPage{Outcome: outcome, Revision: revision, Assistants: []assistantEntry{}, Candidates: []string{}, Warning: newAssistantWarning()}
		names := p.programTitler(ctx)
		for _, rule := range all {
			if rule.Action == asks.AnswerAction && rule.Resource == rights.MetaScopeApps && rule.Permit && rule.Subject.Account == account {
				entry := assistantEntry{Program: rule.Subject.Program, Name: names.name(rule.Subject.Program)}
				if read, err := rules.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource); err == nil && read.Outcome.String() == "found" && read.Record != nil {
					entry.Expires = read.Record.Expires
				} else {
					continue // expired or gone since the listing
				}
				page.Assistants = append(page.Assistants, entry)
			}
		}
		page.Candidates = p.assistantCandidates(ctx, all)
		shown := slices.Clone(page.Candidates)
		for _, a := range page.Assistants {
			shown = append(shown, a.Program)
		}
		page.Names = names.names(shown)
		panelJSON(w, page)
		return
	}
	subject := rightsclient.Subject{Account: account, Program: edit.Program}
	name := p.assistantName(ctx, edit.Program)
	if edit.Action == "stop" {
		// Stop is a narrowing: one click, no confirmation (PROPOSAL §6.4).
		p.logAction(ctx, "rights.assistant_stop", map[string]string{"program": edit.Program})
		result, err := rules.StopAnswererContext(ctx, edit.Revision, subject, edit.AlsoRevokeGrants)
		if err != nil {
			panelError(w, err)
			return
		}
		if result.Outcome.String() != "applied" {
			panelJSON(w, assistantResult{Outcome: result.Outcome.String(), Message: assistantOutcomeSentence(result.Outcome.String())})
			return
		}
		// Stop keeps the next enrollment question away (asks ASK-R10): a
		// plain Stop for the first-use refuse interval, "don't ask again"
		// until the person removes the block, with a deny on the enrollment.
		ttl, why, again := grants.FirstUseRefuseInterval, grants.EnrollmentStoppedWhy, " It may ask again in 10 minutes."
		if edit.DontAskAgain {
			ttl, why, again = 0, grants.EnrollmentNeverAgainWhy, " It will not ask again."
		}
		deny := rightsclient.PolicyRule{Subject: subject, Action: asks.AnswerAction, Resource: rights.MetaScopeApps, Permit: false}
		quiet, err := rules.SetRuleForContext(ctx, result.Revision, deny, ttl, why)
		if err != nil {
			panelError(w, err)
			return
		}
		message := name + " can no longer answer new questions for you. Existing permissions keep their expiry."
		if edit.AlsoRevokeGrants {
			message = name + " can no longer answer new questions for you. Its current permissions were revoked."
		}
		message += again
		if quiet.Outcome.String() != "applied" {
			message = name + " can no longer answer new questions for you, but the request to keep it from asking again was not saved: " + assistantOutcomeSentence(quiet.Outcome.String())
		}
		panelJSON(w, assistantResult{Outcome: result.Outcome.String(), Message: message})
		return
	}
	if !edit.Confirm {
		http.Error(w, "read the warning and confirm before letting a program answer for you", http.StatusBadRequest)
		return
	}
	if err := p.refuseAssistant(ctx, edit.Program); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p.logAction(ctx, "rights.assistant_enroll", map[string]string{"program": edit.Program})
	rule := rightsclient.PolicyRule{Subject: subject, Action: asks.AnswerAction, Resource: rights.MetaScopeApps, Permit: true}
	result, err := rules.SetRuleForContext(ctx, edit.Revision, rule, rights.MetaRuleLapse, grants.EnrollmentWhy(programReasonName(p.programTitler(ctx), edit.Program)))
	if err != nil {
		panelError(w, err)
		return
	}
	message := name + " can now answer routine questions for you."
	if result.Outcome.String() != "applied" {
		message = assistantOutcomeSentence(result.Outcome.String())
	}
	panelJSON(w, assistantResult{Outcome: result.Outcome.String(), Message: message})
}

func assistantOutcomeSentence(outcome string) string {
	switch outcome {
	case "conflict":
		return "Permissions changed while this page was open; nothing was changed. The list is fresh now."
	case "invalid":
		return "The runtime refused this program as an assistant; nothing was changed."
	case "forbidden":
		return "This Panel is not authorized as an operator of this runtime; nothing was changed."
	}
	return "The permissions service could not complete this; nothing is assumed changed."
}

// refuseAssistant refuses what the runtime refuses (asks ASK-R7, rights
// RIGHTS-R16) before anything is written: a script host, the runtime and
// this Panel.
func (p *servicePanel) refuseAssistant(ctx context.Context, program string) error {
	if asks.ScriptHostSubject(rwire.Subject{Program: program}, asks.Publisher{}) {
		return errors.New(p.programTitle(ctx, program) + " runs scripts, and every script it runs would answer; choose the app's own program")
	}
	for _, own := range []string{panelRuntimeProgram(ctx), probe.Self().Program} {
		if own != "" && identity.SameSubjectProgram(program, own) {
			return errors.New("OpenAbstractions' own programs do not answer for you; choose an app")
		}
	}
	return nil
}

// assistantCandidates are the programs the chooser offers: each registered
// local application's program first, as its own descriptor names it, then
// every program a rule names. A script host is left out.
func (p *servicePanel) assistantCandidates(ctx context.Context, rules []rightsclient.PolicyRule) []string {
	var out []string
	add := func(program string) {
		if !identity.ValidSubjectProgram(program) || asks.ScriptHostSubject(rwire.Subject{Program: program}, asks.Publisher{}) ||
			slices.ContainsFunc(out, func(seen string) bool { return identity.SameSubjectProgram(seen, program) }) {
			return
		}
		out = append(out, program)
	}
	if apps, err := p.setupApplicationsForPanel(ctx); err == nil {
		if page, err := apps.Observe(ctx, "", 0); err == nil && (page.Outcome == fwire.ApplicationOutcomePage || page.Outcome == fwire.ApplicationOutcomeStale) {
			for _, listed := range page.Applications {
				if listed.Scope == fwire.ScopeLocal {
					add(listed.Descriptor.Program)
				}
			}
		}
	}
	for _, rule := range rules {
		add(rule.Subject.Program)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

// answeredEntry is one answer an assistant gave for the person, read from
// the asks record (ASK-O7) and the rule it wrote.
type answeredEntry struct {
	ID           string `json:"id"`
	Sentence     string `json:"sentence"`
	Option       string `json:"option"`
	Answered     string `json:"answered"`
	Answerer     string `json:"answerer"`
	AnswererName string `json:"answererName"`
	// Program is the asking program, the subject of the permission the answer
	// wrote; Activity matches it exactly against registered apps.
	Program string `json:"program,omitempty"`
	// ProgramName titles Program (program_titles.go).
	ProgramName string `json:"programName,omitempty"`
	// State is in_force (Undo is offered), changed (someone changed the
	// permission since), ended (it expired or was removed), refused, or
	// chosen for an app's own question, which has no Undo.
	State string `json:"state"`
	// Kind is "choice" for an answer to an app's shared question (asks
	// ASK-O13); OptionLabel is the label the app gave the chosen option.
	Kind        string `json:"kind,omitempty"`
	OptionLabel string `json:"optionLabel,omitempty"`
}

type answeredPage struct {
	Outcome string          `json:"outcome"`
	Answers []answeredEntry `json:"answers"`
	// CompleteSince is set when the runtime evicted older answers to apps'
	// questions: answers at or before it may be missing.
	CompleteSince string `json:"completeSince,omitempty"`
	// Choices is the outcome of reading answers to apps' questions when it
	// is not page; permission answers are listed regardless.
	Choices string `json:"choices,omitempty"`
}

// choiceAnswers reads the answers assistants gave to apps' shared questions
// (asks ASK-O13), newest first, at most answeredShown.
func choiceAnswers(ctx context.Context, operator interface {
	ListAnswersContext(context.Context, string, int64) (askclient.AnswerEntryPage, error)
}, names *programTitler) ([]answeredEntry, int64, string, error) {
	var out []answeredEntry
	var since int64
	cursor := ""
	for range policyPages {
		page, err := operator.ListAnswersContext(ctx, cursor, 64)
		if err != nil {
			return nil, 0, "", err
		}
		if page.Outcome != askclient.OperatorPageOutcomePage {
			return nil, 0, page.Outcome.String(), nil
		}
		since = page.CompleteSinceMs
		for _, a := range page.Answers {
			if a.Kind != askclient.AnswerKindChoice || a.AnsweredBy != askclient.AnswererResponder || len(out) == answeredShown {
				continue
			}
			asker := a.Asker
			if a.AskerProgram != "" {
				asker = names.name(a.AskerProgram)
			}
			out = append(out, answeredEntry{ID: a.ID, Kind: "choice", Sentence: asker + " asked: “" + a.Title + "”", Option: a.Option, OptionLabel: a.Label,
				Answered: time.UnixMilli(a.AnsweredMs).UTC().Format(time.RFC3339Nano), Answerer: a.Answerer, AnswererName: names.name(a.Answerer),
				Program: a.AskerProgram, ProgramName: names.name(a.AskerProgram), State: "chosen"})
		}
		if page.Complete {
			return out, since, "page", nil
		}
		if page.Next == "" || page.Next == cursor {
			return nil, 0, "", errors.New("asks: answer audit cursor did not advance")
		}
		cursor = page.Next
	}
	return nil, 0, "", errors.New("asks: answer audit page bound exceeded")
}

// answeredShown bounds how many recent answers the section lists.
const answeredShown = 20

// answeredRules are the rules a first-use answer wrote: the paired read
// beside a paired write (INF-V4).
func answeredRules(action, resource string) []string {
	if grants.Paired(action, resource) {
		return []string{grants.ContentWriteAction, grants.ContentReadAction}
	}
	return []string{action}
}

// answeredState reads what became of the permits an assistant's allow wrote.
func answeredState(ctx context.Context, rules assistantRules, account, answerer, program, action, resource string) (string, []rightsclient.RuleRead, error) {
	var reads []rightsclient.RuleRead
	changed := false
	for _, act := range answeredRules(action, resource) {
		read, err := rules.ReadRuleContext(ctx, rightsclient.Subject{Account: account, Program: program}, act, resource)
		if err != nil {
			return "", nil, err
		}
		switch {
		case read.Outcome.String() != "found" || read.Record == nil:
			continue
		case !read.Record.Rule.Permit || !identity.SameSubjectProgram(read.Record.SetBy.Program, answerer):
			changed = true
			continue
		}
		reads = append(reads, read)
	}
	if len(reads) > 0 {
		return "in_force", reads, nil
	}
	if changed {
		return "changed", nil, nil
	}
	return "ended", nil, nil
}

// answeredRecord reads one record as an assistant's answer: false for any
// other record.
func answeredRecord(details askclient.QuestionDetails, names *programTitler) (entry answeredEntry, program, action, resource string, ok bool) {
	attributed := details.Question
	read := attributed.Question
	if read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil || read.AnsweredBy == nil ||
		read.AnsweredBy.String() != "responder" || attributed.Answerer == "" {
		return entry, "", "", "", false
	}
	record := *read.Record
	if details.Permission == nil || record.Option == "" {
		return entry, "", "", "", false
	}
	primary := details.Permission.Targets[0]
	program, action, resource = primary.Program, primary.Action, primary.Resource
	entry = answeredEntry{ID: record.ID, Sentence: firstUseSentence(publisherOfRead(read).withNames(names), program, action, resource),
		Option: record.Option, Answered: record.Answered, Answerer: attributed.Answerer, AnswererName: names.name(attributed.Answerer), Program: program, ProgramName: names.name(program)}
	return entry, program, action, resource, true
}

// answered answers GET with the recent answers assistants gave for the
// person, and POST {id} with Undo: only the still-current permits this exact
// answer wrote are removed. Older, replacement and person-owned rules stay.
func (p *servicePanel) answered(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	var undo struct {
		ID string `json:"id"`
	}
	if r.Method == http.MethodPost {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&undo); err != nil || !questionWord(undo.ID) {
			http.Error(w, "choose one answer to undo", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	account := probe.Self().Account
	if !validDelegationWord(account, 128) {
		http.Error(w, "Panel account identity is unavailable", http.StatusServiceUnavailable)
		return
	}
	operator, err := answeredQuestionOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	rules := assistantRuleOperator()
	if r.Method == http.MethodPost {
		p.undoAnswer(ctx, w, operator, rules, account, undo.ID)
		return
	}
	var answeredRecords []askclient.RecordMetadata
	cursor := ""
	for range policyPages {
		page, err := operator.ListQuestionsContext(ctx, cursor, 64)
		if err != nil {
			panelError(w, err)
			return
		}
		if page.Outcome != askclient.OperatorPageOutcomePage {
			panelJSON(w, answeredPage{Outcome: page.Outcome.String(), Answers: []answeredEntry{}})
			return
		}
		for _, record := range page.Records {
			if record.Key == asks.FirstUseKey && record.Option != "" {
				answeredRecords = append(answeredRecords, record)
			}
		}
		if page.Complete {
			break
		}
		cursor = page.Next
	}
	slices.SortFunc(answeredRecords, func(a, b askclient.RecordMetadata) int { return strings.Compare(b.Answered, a.Answered) })
	out := answeredPage{Outcome: "page", Answers: []answeredEntry{}}
	names := p.programTitler(ctx)
	// ?app=<registered name> keeps the answers that app gave, compared by
	// program identity (the app page, app_facts.go).
	var answerer *sameProgram
	if name := r.URL.Query().Get("app"); name != "" {
		apps, err := p.setupApplicationsForPanel(ctx)
		if err != nil {
			panelError(w, err)
			return
		}
		entry, _ := appPageEntry(ctx, apps, name)
		if entry == nil {
			panelJSON(w, out)
			return
		}
		answerer = &sameProgram{program: entry.Descriptor.Program, seen: map[string]bool{}}
	}
	choices, since, choicesOutcome, err := choiceAnswers(ctx, operator, names)
	switch {
	case ordinaryUnsupported(err):
		choices, choicesOutcome = nil, "unsupported"
	case err != nil:
		panelError(w, err)
		return
	}
	if choicesOutcome != "page" {
		out.Choices = choicesOutcome
	}
	if since > 0 {
		out.CompleteSince = time.UnixMilli(since).UTC().Format(time.RFC3339Nano)
	}
	for _, entry := range choices {
		if answerer == nil || answerer.is(entry.Answerer) {
			out.Answers = append(out.Answers, entry)
		}
	}
	for _, record := range answeredRecords {
		if len(out.Answers) == 2*answeredShown {
			break
		}
		read, err := askclient.ReadDetailsContext(ctx, operator, account, record.ID)
		if err != nil {
			panelError(w, err)
			return
		}
		entry, program, action, resource, ok := answeredRecord(read, names)
		if !ok || answerer != nil && !answerer.is(entry.Answerer) {
			continue
		}
		entry.State = "refused"
		if entry.Option == "allow" {
			if entry.State, _, err = answeredState(ctx, rules, account, entry.Answerer, program, action, resource); err != nil {
				panelError(w, err)
				return
			}
		}
		out.Answers = append(out.Answers, entry)
	}
	slices.SortStableFunc(out.Answers, func(a, b answeredEntry) int { return strings.Compare(answeredSortKey(b), answeredSortKey(a)) })
	if len(out.Answers) > answeredShown {
		out.Answers = out.Answers[:answeredShown]
	}
	panelJSON(w, out)
}

// answeredSortKey orders entries by answer time; both kinds carry RFC 3339
// UTC times, normalized here to one fixed width.
func answeredSortKey(e answeredEntry) string {
	at, err := time.Parse(time.RFC3339Nano, e.Answered)
	if err != nil {
		return ""
	}
	return at.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// undoAnswer removes only the exact current permit effects of this answer.
// It captures policy revision before reading the retained question, then the
// rights owner checks the private question identity and all paired effects
// together under that revision. The historical answer is left intact.
func (p *servicePanel) undoAnswer(ctx context.Context, w http.ResponseWriter, operator answeredReader, rules assistantRules, account, id string) {
	revision, _, outcome, err := listRules(ctx, rules)
	if err != nil {
		panelError(w, err)
		return
	}
	if outcome != "page" {
		panelJSON(w, assistantResult{Outcome: outcome, Message: assistantOutcomeSentence(outcome)})
		return
	}
	read, err := askclient.ReadDetailsContext(ctx, operator, account, id)
	if err != nil {
		panelError(w, err)
		return
	}
	entry, program, action, resource, ok := answeredRecord(read, nil)
	if !ok || entry.Option != "allow" {
		http.Error(w, "that answer was not an assistant's allow", http.StatusBadRequest)
		return
	}
	p.logAction(ctx, "rights.assistant_undo", map[string]string{"question.id": id})
	edit, err := rules.RevokeAnswerPermitsContext(ctx, revision,
		rightsclient.Subject{Account: account, Program: entry.Answerer},
		rightsclient.Subject{Account: account, Program: program}, answeredRules(action, resource), resource, id)
	if err != nil {
		panelError(w, err)
		return
	}
	message := "Undone: the permission from this answer was removed. The app may ask again."
	if edit.Outcome.String() != "applied" {
		message = assistantOutcomeSentence(edit.Outcome.String())
	}
	panelJSON(w, assistantResult{Outcome: edit.Outcome.String(), Message: message})
}

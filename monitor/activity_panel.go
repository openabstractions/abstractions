package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// The Activity timeline's answers and app starts (research/panel-redesign/
// SPEC.md §4). Each event kind has one owner, and this Panel only reads it:
// answers from the asks answer audit (ASK-O15), which holds the person's own
// answers from this Panel and the native prompt, an enrolled assistant's
// answers and the Keep or Remove of an app that added itself; app starts
// from the application directory's start log (APPLICATIONS.md, "Start
// log"). Both owners serve their operator only, this Panel, and page by
// cursor; each route here passes one page through, worded.

// activityPage is one page of timeline events. Next continues it; Complete
// means the owner listed everything it holds; CompleteSince is the time at
// or before which the owner may have dropped events past its bound.
type activityPage struct {
	Outcome       string          `json:"outcome"`
	Events        []activityEvent `json:"events"`
	Next          string          `json:"next,omitempty"`
	Complete      bool            `json:"complete"`
	CompleteSince int64           `json:"completeSince,omitempty"`
}

// activityEvent is one timeline row. App names the program by the shared
// naming rule (program_titles.go); Program is matched exactly against the
// registered apps by the page's app filter and is never shown.
type activityEvent struct {
	At      int64  `json:"at"`
	App     string `json:"app"`
	Program string `json:"program,omitempty"`
	// Also is the other program a line is about, matched by the app filter
	// like Program (a Memory line names the asker and the holder).
	Also   string `json:"also,omitempty"`
	What   string `json:"what"`
	Result string `json:"result"`
	// Answer audit evidence, separate from the timeline's display words.
	QuestionID string `json:"questionId,omitempty"`
	AnsweredBy string `json:"answeredBy,omitempty"`
	// Undo names the rule the row's Undo removes (activity_setup_grants.go).
	Undo    *activityUndo `json:"undo,omitempty"`
	Restore string        `json:"restore,omitempty"`
}

// activityPageSize is the owner page this Panel asks for (1..64 at both
// owners).
const activityPageSize = 64

// answerAuditReader is the asks operator surface the timeline reads.
type answerAuditReader interface {
	ListAnswerContentsContext(ctx context.Context, cursor string, limit int64) (askclient.AnswerContentPage, error)
}

// answerAuditOperator resolves the asks operator per request. Tests replace it.
var answerAuditOperator = func(ctx context.Context) (answerAuditReader, error) {
	return panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// startLogReader is the application directory surface the timeline reads.
type startLogReader interface {
	ListStarts(ctx context.Context, cursor string, limit int64) (fwire.ApplicationStartPage, error)
}

// startLogOperator resolves the application directory per request. Tests
// replace it.
var startLogOperator = func(ctx context.Context) (startLogReader, error) {
	return panelMachine().ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// activityCursor reads ?cursor=, an owner's opaque continuation.
func activityCursor(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return "", false
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "the list changed; read it again from the start", http.StatusBadRequest)
		return "", false
	}
	return cursor, true
}

// unknownMethod reports an owner too old to serve the call.
func unknownMethod(err error) bool {
	var service *fwire.ServiceError
	return ordinaryUnsupported(err) || errors.As(err, &service) && service.Code == fwire.ServiceErrorCodeUnknownMethod
}

// activityAnswers answers GET with one page of the answer audit.
func (p *servicePanel) activityAnswers(w http.ResponseWriter, r *http.Request) {
	cursor, ok := activityCursor(w, r)
	if !ok {
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := answerAuditOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := operator.ListAnswerContentsContext(ctx, cursor, activityPageSize)
	switch {
	case unknownMethod(err):
		panelJSON(w, activityPage{Outcome: "unsupported", Events: []activityEvent{}})
		return
	case err != nil:
		panelError(w, err)
		return
	}
	out := activityPage{Outcome: page.Outcome.String(), Events: []activityEvent{}, Next: page.Next, Complete: page.Complete, CompleteSince: page.CompleteSinceMs}
	names := p.programTitler(ctx)
	for _, a := range page.Answers {
		out.Events = append(out.Events, answerEvent(a, names))
	}
	panelJSON(w, out)
}

// answerOptionWords word a catalogue question's options.
var answerOptionWords = map[string]string{"allow": "Allowed", "once": "Allowed once", "refuse": "Refused", "never": "Refused for good", "keep": "Kept", "remove": "Removed"}

// answerEvent words one audited answer from its immutable retained content:
// the app it concerns, what was asked,
// and the answer. Only the retained responder program supplies an actor name;
// the operator route alone supplies no person or automatic-actor provenance.
func answerEvent(content askclient.AnswerContent, names *programTitler) activityEvent {
	a := content.Answer
	by := ""
	if a.AnsweredBy == askclient.AnswererResponder {
		by = names.name(a.Answerer)
	}
	program := a.AskerProgram
	e := activityEvent{At: a.AnsweredMs, QuestionID: a.ID}
	switch a.AnsweredBy {
	case askclient.AnswererOperator, askclient.AnswererResponder:
		e.AnsweredBy = a.AnsweredBy.String()
	}
	if a.Kind == askclient.AnswerKindChoice {
		e.What = "Asked: “" + a.Title + "”"
		e.Result = a.Label + ", chosen"
		if by != "" {
			e.Result += " by " + by
		}
	} else {
		word := answerOptionWords[a.Option]
		if word == "" {
			word = a.Option
		}
		e.Result = word
		if by != "" {
			e.Result += " by " + by
		}
		switch {
		case a.Key == asks.SetupModelsKey:
			if content.Content != nil {
				facts, err := asks.SetupFactsFromSlots(content.Content.Slots)
				if err == nil && facts.Occurrence != "" && facts.Occurrence == a.About {
					if body, err := asks.SetupQuestionPresentation(content.Content.Slots); err == nil {
						e.What = body.Title + "\n" + body.Instruction + "\n" + body.Details
					}
				}
			}
		case a.Key == asks.FirstUseKey:
			// This immutable permission snapshot supplies historical display
			// details after retirement. Rule writes use fresh occurrence reads.
			if content.Content != nil && content.Content.Permission != nil && len(content.Content.Permission.Targets) > 0 {
				target := content.Content.Permission.Targets[0]
				program = target.Program
				e.What = firstUseSentence(trayPublisher{Subject: a.Publisher, Trusted: a.PublisherTrusted, Names: names}, target.Program, target.Action, target.Resource)
				if _, providerPair := asks.ProviderPermissionName(*content.Content.Permission); providerPair {
					for _, companion := range content.Content.Permission.Targets[1:] {
						e.What += "\n" + firstUseSentence(trayPublisher{Subject: a.Publisher, Trusted: a.PublisherTrusted, Names: names}, companion.Program, companion.Action, companion.Resource)
					}
				}
			}
		case a.Key == asks.SelfRegisteredKey:
			if registered, _, ok := selfRegisteredTarget(a.Key, a.About, a.AskerProgram, content.Content); ok {
				program = registered
				e.What = "Added itself to OpenAbstractions"
			}
		case a.Key == asks.TypedPermissionKey:
			// The trusted bundle label names the permission; one that reads
			// as a path or a qualified name is left out.
			program = a.About
			e.What = "Asked for permissions"
			if a.Title != "" && !strings.ContainsAny(a.Title, `/\:`) {
				e.What = "Asked for permission: " + a.Title
			}
		default:
			e.What = catalogueWords(a.Key, a.About)
		}
		if e.What == "" {
			e.What = "Asked a question"
		}
	}
	e.Program = program
	if program != "" {
		e.App = names.name(program)
	} else {
		e.App = programStem(a.Asker)
	}
	return e
}

// catalogueWords reads the shared short display projection. Trusted first-use,
// self-registration and typed-permission projections remain above.
func catalogueWords(key, about string) string {
	q, _ := asks.Find(key)
	return q.Summary(about)
}

// activityStarts answers GET with one page of the application start log.
func (p *servicePanel) activityStarts(w http.ResponseWriter, r *http.Request) {
	cursor, ok := activityCursor(w, r)
	if !ok {
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	directory, err := startLogOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	page, err := directory.ListStarts(ctx, cursor, activityPageSize)
	switch {
	case unknownMethod(err):
		panelJSON(w, activityPage{Outcome: "unsupported", Events: []activityEvent{}})
		return
	case err != nil:
		panelError(w, err)
		return
	}
	out := activityPage{Outcome: page.Outcome.String(), Events: []activityEvent{}, Next: page.Cursor, Complete: page.Complete, CompleteSince: page.CompleteSinceUnixMs}
	names := p.programTitler(ctx)
	for _, s := range page.Starts {
		result := ""
		if s.Launched {
			result = "Open requested"
		}
		out.Events = append(out.Events, activityEvent{At: s.StartedUnixMs, App: names.name(s.Program), Program: s.Program, What: "Became available", Result: result})
	}
	panelJSON(w, out)
}

func (p *servicePanel) activityRoutes(mux *http.ServeMux, key string) {
	p.messageActivityRoutes(mux, key)
	mux.HandleFunc("/activity/agent-exits", guard(key, p.activityAgentExits))
	mux.HandleFunc("/activity/answers", guard(key, p.activityAnswers))
	mux.HandleFunc("/activity/storage", guard(key, p.activityStorage))
	mux.HandleFunc("/activity/starts", guard(key, p.activityStarts))
	mux.HandleFunc("/activity/setup-grants", guard(key, p.activitySetupGrants))
	mux.HandleFunc("/activity/app-requests", guard(key, p.activityAppRequests))
	mux.HandleFunc("/activity/elevated", guard(key, p.activityElevated))
	mux.HandleFunc("/activity/rights-changes", guard(key, p.activityRightsChanges))
	mux.HandleFunc("/activity/memory", guard(key, p.activityMemory))
}

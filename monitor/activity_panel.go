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
	What    string `json:"what"`
	Result  string `json:"result"`
}

// activityPageSize is the owner page this Panel asks for (1..64 at both
// owners).
const activityPageSize = 64

// answerAuditReader is the asks operator surface the timeline reads.
type answerAuditReader interface {
	ListAnswersContext(ctx context.Context, cursor string, limit int64) (askclient.AnswerEntryPage, error)
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
	page, err := operator.ListAnswersContext(ctx, cursor, activityPageSize)
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

// answerEvent words one audited answer: the app it concerns, what was asked,
// and the answer with who gave it, "you" for the person's own answer through
// this Panel or the native prompt.
func answerEvent(a askclient.AnswerEntry, names *programTitler) activityEvent {
	by := "you"
	if a.AnsweredBy == askclient.AnswererResponder {
		by = names.name(a.Answerer)
	}
	program := a.AskerProgram
	e := activityEvent{At: a.AnsweredMs}
	if a.Kind == askclient.AnswerKindChoice {
		e.What = "Asked: “" + a.Title + "”"
		e.Result = a.Label + ", chosen by " + by
	} else {
		word := answerOptionWords[a.Option]
		if word == "" {
			word = a.Option
		}
		e.Result = word + " by " + by
		switch {
		case a.Key == asks.FirstUseKey:
			if target, action, resource, ok := asks.FirstUse(a.Key, a.About, a.Title); ok {
				program = target
				e.What = firstUseSentence(trayPublisher{Subject: a.Publisher, Trusted: a.PublisherTrusted, Names: names}, target, action, resource)
			}
		case a.Key == asks.SelfRegisteredKey:
			if registered, _, ok := asks.SelfRegistered(a.Key, a.About, a.Title); ok {
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

// catalogueWords is the sentence for a catalogue question other than first
// use, self-registration and typed permission, chosen by the catalogue's own
// slots and built from its About slot only: the recorded text carries
// request codes, digests, full paths and raw permission lists, none of which
// a row shows. A folder is named by its last element (oaShortName). The
// catalogue names no app here, so an adopter's question keeps its own key.
// An unknown key reads as a question.
func catalogueWords(key, about string) string {
	if key == asks.SystemSecurityLogKey {
		return "Asked to read the Windows Security log"
	}
	q, ok := asks.Find(key)
	if !ok {
		return "Asked a question"
	}
	switch {
	case q.About == "folder":
		if leaf := programFileName(strings.TrimRight(about, `/\`)); leaf != "" {
			return "Asked to edit files in " + leaf
		}
		return "Asked to edit files in a folder"
	case q.About == "target":
		return "Asked to replace stored content once"
	case q.About == "host":
		if about != "" {
			return "Asked to fetch from " + about
		}
		return "Asked to fetch from the internet"
	case strings.Contains(q.Text, "{rights}"):
		return "Asked for permissions"
	}
	return "Asked a question"
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
		result := "On its own"
		if s.Launched {
			result = "By OpenAbstractions"
		}
		out.Events = append(out.Events, activityEvent{At: s.StartedUnixMs, App: names.name(s.Program), Program: s.Program, What: "App started", Result: result})
	}
	panelJSON(w, out)
}

func (p *servicePanel) activityRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/activity/answers", guard(key, p.activityAnswers))
	mux.HandleFunc("/activity/starts", guard(key, p.activityStarts))
}

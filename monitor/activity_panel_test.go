package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
)

// Every catalogue question reads as its own sentence built from its About
// slot: no row shows a request code, a digest, a full path or a raw
// permission list (panel_rules.js raw-value shapes), and an unknown key
// reads as a question.
func TestActivityWordsEveryCatalogueQuestionWithoutRawValues(t *testing.T) {
	shapes := loadRawValueShapes(t)
	folder, program := `C:\Users\me\Projects\site`, `C:\Apps\Coder\coder.exe`
	code, digest := "7f3a9c2e5b1d4e6f8a0b", "sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	about := map[string]string{"content.replace": "output:image/" + digest, "opencode.project.edit": folder, "download.reach": "huggingface.co",
		asks.FirstUseKey: program, asks.SelfRegisteredKey: program, asks.SystemSecurityLogKey: code, asks.TypedPermissionKey: program,
		asks.ApplicationCommandOnceKey: program, asks.AgentEnrollmentKey: program, asks.AgentUpdateKey: code, asks.SetupModelsKey: code}
	title := map[string]string{
		asks.PairingKey:                "Pair these computers? Check code " + code,
		"content.replace":              "coder.exe requests a one-time replacement of output:image/" + digest + " with " + digest + ". Request " + code + " expires 10:00.",
		"rights.register":              "coder.exe wants to: abstraction.storage/content.write on output:image, abstraction.model/lookup on hf",
		"opencode.project.edit":        "OpenCode requests permission to read, run, create, edit and delete files throughout " + folder + ", including its subfolders, until this sandbox session exits. Request " + code + " expires 10:00.",
		"download.reach":               "coder.exe wants to fetch from huggingface.co",
		asks.FirstUseKey:               program + " wants to abstraction.storage/content.write on output:image",
		asks.SelfRegisteredKey:         program + " added itself to OpenAbstractions as Coder. Keep it?",
		asks.SystemSecurityLogKey:      "coder.exe wants to read one page of the Windows Security log. Windows asks for administrator approval next. Request " + code + " expires 10:00.",
		asks.TypedPermissionKey:        "Read your notes",
		asks.ApplicationCommandOnceKey: "Read your notes? " + program + " requests this once. Command " + digest + ". Request " + code + " expires 10:00.",
		asks.AgentEnrollmentKey:        "Connect Coder?",
		asks.AgentUpdateKey:            "Update the Coder connections? Keep these updated connections as your assistants. Restart Coder to finish. Request " + code + ".",
		asks.SetupModelsKey:            "Set up models for Coder. " + digest,
		asks.ProgramReviewKey:          "Review updated programs? " + program + " " + digest,
	}
	keys := []string{asks.TypedPermissionKey, "unknown.question"}
	for _, q := range asks.Questions {
		keys = append(keys, q.Key)
	}
	names := newProgramTitler(nil)
	for _, key := range keys {
		e := answerEvent(askclient.AnswerContent{Answer: askclient.AnswerEntry{ID: "q", Kind: askclient.AnswerKindQuestion, Key: key, Asker: "coder.exe", About: about[key],
			Title: title[key] + " " + folder, Option: "allow", Label: "allow", AnsweredBy: askclient.AnswererOperator, AnsweredMs: 1}}, names)
		for _, shown := range []string{e.App, e.What, e.Result} {
			for _, s := range shapes {
				if m := s.matches(shown); len(m) > 0 {
					t.Errorf("%s: %q shows the %s %q", key, shown, s.name, m)
				}
			}
			if strings.Contains(shown, code) || strings.Contains(shown, folder) {
				t.Errorf("%s: %q shows a code or a path", key, shown)
			}
		}
		if _, known := title[key]; !known && key != "unknown.question" {
			t.Errorf("catalogue key %s has no test entry", key)
		}
	}
	for key, want := range map[string]string{"opencode.project.edit": "Asked to edit files in site", "content.replace": "Asked to replace stored content once",
		"download.reach": "Asked to fetch from huggingface.co", "rights.register": "Asked for permissions", asks.PairingKey: "Asked to pair computers", "unknown.question": "Asked a question",
		asks.ApplicationCommandOnceKey: "Asked a question", asks.AgentEnrollmentKey: "Asked a question", asks.AgentUpdateKey: "Asked a question", asks.SetupModelsKey: "Asked a question", asks.ProgramReviewKey: "Asked to review updated programs"} {
		if got := answerEvent(askclient.AnswerContent{Answer: askclient.AnswerEntry{Key: key, Asker: "coder.exe", About: about[key], Title: title[key], Option: "once", AnsweredBy: askclient.AnswererOperator}}, names).What; got != want {
			t.Errorf("%s: %q, want %q", key, got, want)
		}
	}
}

type answerAuditFake struct {
	page     askclient.AnswerEntryPage
	contents map[string]*askclient.CatalogueContent
	err      error
	cursors  []string
}

func TestCatalogueSummaryRejectsUnsafeDisplaySlots(t *testing.T) {
	for _, about := range []string{"https://person:secret@example.com/private?token=secret", "person:secret@example.com", "example.com/private", "example.com?token=secret", "example.com\nsecret"} {
		if got := catalogueWords("download.reach", about); got != "Asked to fetch from the internet" {
			t.Errorf("unsafe host %q reached summary %q", about, got)
		}
	}
	for _, about := range []string{"/", `C:\`, `\\`, "folder\nsecret"} {
		if got := catalogueWords("opencode.project.edit", about); got != "Asked to edit files in a folder" {
			t.Errorf("unsafe folder %q reached summary %q", about, got)
		}
	}
}

func (f *answerAuditFake) ListAnswerContentsContext(_ context.Context, cursor string, limit int64) (askclient.AnswerContentPage, error) {
	f.cursors = append(f.cursors, cursor)
	page := askclient.AnswerContentPage{Outcome: f.page.Outcome, Next: f.page.Next, Complete: f.page.Complete, CompleteSinceMs: f.page.CompleteSinceMs}
	for _, answer := range f.page.Answers {
		page.Answers = append(page.Answers, askclient.AnswerContent{Answer: answer, Content: f.contents[answer.ID]})
	}
	return page, f.err
}

type startLogFake struct {
	page    fwire.ApplicationStartPage
	cursors []string
}

func (f *startLogFake) ListStarts(_ context.Context, cursor string, limit int64) (fwire.ApplicationStartPage, error) {
	f.cursors = append(f.cursors, cursor)
	return f.page, nil
}

// The current IPC audit proves the operator role, rather than who clicked.
// Each permission/self-registration names its own structured target program.
func TestActivityCurrentIPCAuditNamesEachAppAndOnlyProvenAnswerers(t *testing.T) {
	book, _, _ := panelFirstUseRuntime(t, "abstraction.model/lookup")
	withoutOwnPrograms(t)
	dir := t.TempDir()
	appA, appB, assistant := filepath.Join(dir, "editor.exe"), filepath.Join(dir, "viewer.exe"), filepath.Join(dir, "helper.exe")
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{
		{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "editor", Program: appA, Title: "Editor"}},
		{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "viewer", Program: appB, Title: "Viewer"}},
		{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "helper", Program: assistant, Title: "Helper"}},
	}}}
	op, err := panelMachine().ResolveAsksOperator(t.Context(), facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	actors := map[string]string{}
	for i, program := range []string{appA, appB, appA} {
		r, _, err := book.AskApplication("runtime-scope", "runtime", []string{"person", "automatic", "assistant"}[i], asks.FirstUseKey,
			map[string]string{"program": program, "action": "abstraction.model/lookup", "resource": []string{"hf", "ollama", "lmstudio"}[i]}, listen.Seen{})
		if err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			actors[r.ID] = "responder"
			if _, outcome, err := book.AnswerApplicationAsResponder(r.ID, "allow", assistant, nil); err != nil || outcome != "answered" {
				t.Fatal(outcome, err)
			}
		} else {
			actors[r.ID] = "operator"
			if decision, err := op.AnswerQuestionIfPendingContext(t.Context(), r.ID, "refuse"); err != nil || decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
				t.Fatal(decision, err)
			}
		}
		if retired, err := op.RetireQuestionContext(t.Context(), r.ID); err != nil || retired.Outcome != askclient.OperatorRetirementOutcomeRetired {
			t.Fatal(retired, err)
		}
	}
	for _, option := range []string{"keep", "remove"} {
		registration, _, err := book.AskApplicationFrom("runtime-scope", "OpenAbstractions", "self-registration-"+option, asks.SelfRegisteredKey,
			map[string]string{"program": appB, "title": "Viewer"}, listen.Seen{}, asks.Publisher{})
		if err != nil {
			t.Fatal(err)
		}
		actors[registration.ID] = "operator"
		if decision, err := op.AnswerQuestionIfPendingContext(t.Context(), registration.ID, option); err != nil || decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
			t.Fatal(decision, err)
		}
		if retired, err := op.RetireQuestionContext(t.Context(), registration.ID); err != nil || retired.Outcome != askclient.OperatorRetirementOutcomeRetired {
			t.Fatal(retired, err)
		}
	}
	response := panelRequest(t, (&servicePanel{comfyApps: apps}).handler("test-key"), "/activity/answers", nil)
	page := decodePanel[activityPage](t, response.Code, response.Body.Bytes())
	if len(page.Events) != 5 {
		t.Fatal(page)
	}
	want := map[string]int{"Editor|Refused": 1, "Viewer|Refused": 1, "Editor|Allowed by Helper": 1, "Viewer|Kept": 1, "Viewer|Removed": 1}
	for _, event := range page.Events {
		key := event.App + "|" + event.Result
		if want[key] == 0 || strings.Contains(event.Result, "you") {
			t.Fatal("unproven actor or wrong application", event)
		}
		if actor := actors[event.QuestionID]; actor == "" || event.AnsweredBy != actor {
			t.Fatal("answer occurrence or actor differs from the audit", event)
		}
		delete(actors, event.QuestionID)
		want[key]--
	}
}

// The Activity timeline reads the person's own answers, an assistant's
// answers, a self-registration's Keep and app starts from their owners and
// words each row with the app's registered title, what was asked and who
// decided (SPEC §4). The owner's cursor passes through; no row shows a path,
// an id or a hash.
func TestActivityWordsAnswersAndStartsFromTheirOwners(t *testing.T) {
	dir := t.TempDir()
	comfy, opencode, stray := filepath.Join(dir, "ComfyUI.exe"), filepath.Join(dir, "opencode.exe"), filepath.Join(dir, "stray.exe")
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{
		{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Program: comfy, Title: "ComfyUI"}},
		{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "opencode", Program: opencode, Title: "OpenCode"}},
	}}}
	answers := &answerAuditFake{page: askclient.AnswerEntryPage{Outcome: askclient.OperatorPageOutcomePage, Next: "owner-cursor", Answers: []askclient.AnswerEntry{
		{ID: "c1", Kind: askclient.AnswerKindChoice, Asker: "ComfyUI.exe", AskerProgram: comfy, Title: "Which model?", Option: "small", Label: "Small", AnsweredBy: askclient.AnswererResponder, Answerer: opencode, AnsweredMs: 5000},
		{ID: "q1", Kind: askclient.AnswerKindQuestion, Key: asks.SelfRegisteredKey, Asker: "openabstractions", About: stray, Title: "Changed catalogue wording", Option: "keep", Label: "keep", AnsweredBy: askclient.AnswererOperator, AnsweredMs: 4000},
		{ID: "q2", Kind: askclient.AnswerKindQuestion, Key: asks.FirstUseKey, Asker: "openabstractions", About: comfy, Title: "Changed permission wording", Option: "refuse", Label: "refuse", AnsweredBy: askclient.AnswererOperator, AnsweredMs: 3000},
		{ID: "q3", Kind: askclient.AnswerKindQuestion, Key: asks.SystemSecurityLogKey, Asker: "opencode.exe", AskerProgram: opencode, Title: "opencode.exe wants to read one page of the Windows Security log. Windows asks for administrator approval next. Request 4F2A expires 10:00.", Option: "once", Label: "once", AnsweredBy: askclient.AnswererOperator, AnsweredMs: 2000},
	}}}
	permission, err := asks.NewPermissionRequest(askclient.PermissionTarget{Account: "account", Program: comfy, Action: asks.PermissionContentWrite, Resource: "output:image"})
	if err != nil {
		t.Fatal(err)
	}
	if len(permission.Targets) != 2 {
		t.Fatal("paired history fixture must retain both canonical targets", permission)
	}
	answers.contents = map[string]*askclient.CatalogueContent{
		"q1": {Slots: map[string]string{"program": stray, "title": "Stray"}},
		"q2": {Slots: map[string]string{}, Permission: &permission},
	}
	starts := &startLogFake{page: fwire.ApplicationStartPage{Outcome: fwire.ApplicationOutcomePage, Complete: true, CompleteSinceUnixMs: 900, Starts: []fwire.ApplicationStart{
		{Application: "opencode", Program: opencode, Title: "OpenCode", StartedUnixMs: 1500, Launched: true},
		{Application: "comfyui", Program: comfy, Title: "ComfyUI", StartedUnixMs: 1000},
	}}}
	restoreAnswers, restoreStarts := answerAuditOperator, startLogOperator
	t.Cleanup(func() { answerAuditOperator, startLogOperator = restoreAnswers, restoreStarts })
	answerAuditOperator = func(context.Context) (answerAuditReader, error) { return answers, nil }
	startLogOperator = func(context.Context) (startLogReader, error) { return starts, nil }
	panel := (&servicePanel{comfyApps: apps}).handler("test-key")

	r := panelRequest(t, panel, "/activity/answers?cursor=earlier", nil)
	page := decodePanel[activityPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || page.Next != "owner-cursor" || page.Complete || len(page.Events) != 4 || len(answers.cursors) != 1 || answers.cursors[0] != "earlier" {
		t.Fatalf("%+v %v", page, answers.cursors)
	}
	want := []activityEvent{
		{At: 5000, App: "ComfyUI", Program: comfy, What: "Asked: “Which model?”", Result: "Small, chosen by OpenCode", QuestionID: "c1", AnsweredBy: "responder"},
		{At: 4000, App: "stray (not registered)", Program: stray, What: "Added itself to OpenAbstractions", Result: "Kept", QuestionID: "q1", AnsweredBy: "operator"},
		{At: 3000, App: "ComfyUI", Program: comfy, Result: "Refused", QuestionID: "q2", AnsweredBy: "operator"},
		{At: 2000, App: "OpenCode", Program: opencode, What: "Asked to read the Windows Security log", Result: "Allowed once", QuestionID: "q3", AnsweredBy: "operator"},
	}
	for i, w := range want {
		got := page.Events[i]
		if w.What == "" {
			w.What = got.What
			if !strings.HasPrefix(got.What, "ComfyUI wants to ") {
				t.Fatalf("a first-use answer reads as its question: %+v", got)
			}
		}
		if samePanelProgram(got.Program, w.Program) {
			w.Program = got.Program
		}
		if got != w {
			t.Fatalf("event %d: %+v, want %+v", i, got, w)
		}
		for _, shown := range []string{got.App, got.What, got.Result} {
			if strings.Contains(shown, dir) || strings.Contains(shown, "4F2A") || strings.Contains(shown, ".exe") {
				t.Fatalf("a row shows a path or a code: %q", shown)
			}
		}
	}

	r = panelRequest(t, panel, "/activity/starts", nil)
	page = decodePanel[activityPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || !page.Complete || page.CompleteSince != 900 || len(page.Events) != 2 ||
		page.Events[0] != (activityEvent{At: 1500, App: "OpenCode", Program: opencode, What: "Became available", Result: "Open requested"}) ||
		page.Events[1] != (activityEvent{At: 1000, App: "ComfyUI", Program: comfy, What: "Became available", Result: ""}) {
		t.Fatalf("%+v", page)
	}

	// An owner's refusal passes through with no events.
	answers.page = askclient.AnswerEntryPage{Outcome: askclient.OperatorPageOutcomeForbidden}
	r = panelRequest(t, panel, "/activity/answers", nil)
	if refused := decodePanel[activityPage](t, r.Code, r.Body.Bytes()); refused.Outcome != "forbidden" || len(refused.Events) != 0 {
		t.Fatalf("%+v", refused)
	}
	// An older runtime without the audit reads as unsupported.
	answers.err = &askclient.ServiceError{Code: askclient.ServiceErrorCodeUnknownMethod}
	r = panelRequest(t, panel, "/activity/answers", nil)
	if old := decodePanel[activityPage](t, r.Code, r.Body.Bytes()); old.Outcome != "unsupported" || len(old.Events) != 0 {
		t.Fatalf("%+v", old)
	}
	if r := panelRequest(t, panel, "/activity/starts?cursor="+strings.Repeat("x", 257), nil); r.Code != 400 {
		t.Fatalf("an oversized cursor: %d", r.Code)
	}
}

func TestActivityAnswersOmitAbsentAndUnknownActor(t *testing.T) {
	for _, actor := range []askclient.Answerer{0, 99} {
		event := answerEvent(askclient.AnswerContent{Answer: askclient.AnswerEntry{
			ID: "original", Option: "allow", AnsweredBy: actor, Answerer: "unproven",
		}}, nil)
		raw, err := json.Marshal(event)
		if err != nil || event.QuestionID != "original" || event.Result != "Allowed" ||
			strings.Contains(string(raw), "answeredBy") || strings.Contains(string(raw), "unproven") {
			t.Fatal(string(raw), err)
		}
	}
}

func TestActivityDoesNotRecoverMissingContentFromRenderedEnglish(t *testing.T) {
	program := `C:\Apps\editor.exe`
	for _, answer := range []askclient.AnswerEntry{
		{Key: asks.FirstUseKey, About: program, Title: program + " wants to abstraction.model/lookup on hf"},
		{Key: asks.SelfRegisteredKey, About: program, Title: program + " added itself to OpenAbstractions as Editor. Keep it?"},
	} {
		for _, content := range []*askclient.CatalogueContent{nil, {Slots: map[string]string{"program": program, "action": "abstraction.model/lookup", "resource": "hf"}}} {
			got := answerEvent(askclient.AnswerContent{Answer: answer, Content: content}, newProgramTitler(nil))
			if got.What != "Asked a question" || got.Program != "" {
				t.Fatalf("absent typed history recovered from prose or slots: %+v", got)
			}
		}
	}
}

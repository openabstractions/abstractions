package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksclient "github.com/openabstractions/abstraction-asks/go/client"
	projectwire "github.com/openabstractions/abstractions/serve/go/opencode/session"
)

func TestOpenCodeProjectRefusesNativeAuthorityAncestor(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "project")
	control := filepath.Join(root, "control")
	inside := filepath.Join(control, "nested")
	for _, dir := range []string{project, control, inside} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	code := filepath.Join(control, "owner.exe")
	if err := os.WriteFile(code, []byte("fixture code identity"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{root, control, inside} {
		if err := rejectOpenCodeAuthorityOverlap(folder, []string{control, code}); err == nil {
			t.Fatalf("authority-containing folder accepted: %s", folder)
		}
	}
	if err := rejectOpenCodeAuthorityOverlap(project, []string{control, code}); err != nil {
		t.Fatalf("separate project refused: %v", err)
	}
}

func TestOpenCodeFolderEffectRequiresItsManualOccurrence(t *testing.T) {
	now := time.Now().UTC()
	owner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	question := asksclient.ApplicationQuestion{RequestKey: "native-request", Key: "opencode.project.edit", Slots: map[string]string{"folder": "selected folder", "code": "native-code", "until": now.Add(time.Minute).Format(time.RFC3339)}}
	template, _ := asks.Find(question.Key)
	// The asks book renders and records the display name derived from the
	// bound program (asks.AskerDisplay), never the full path.
	text, err := template.Render(asks.Ask{Asker: asks.AskerDisplay(owner), Slots: question.Slots})
	if err != nil {
		t.Fatal(err)
	}
	manual := asksclient.AnswererOperator
	record := asksclient.RecordMetadata{ID: "question", Asker: asks.AskerDisplay(owner), Key: question.Key, About: question.Slots["folder"], Text: text, Option: "once", Yes: true, Answered: now.Format(time.RFC3339Nano)}
	read := asksclient.OperatorQuestion{Outcome: asksclient.OperatorQuestionOutcomeFound, Record: &record, AnsweredBy: &manual}
	check := func(r asksclient.OperatorQuestion) bool {
		return approvedOpenCodeFolder(r, "question", owner, question, now.Add(-time.Second), now.Add(time.Minute), now)
	}
	if !check(read) {
		t.Fatal("exact retained manual answer refused")
	}
	for _, alter := range []func(*asksclient.OperatorQuestion){
		func(r *asksclient.OperatorQuestion) { r.Record.ID = "other" }, func(r *asksclient.OperatorQuestion) { r.Record.Key = "rights.register" },
		func(r *asksclient.OperatorQuestion) { r.Record.About = "other folder" }, func(r *asksclient.OperatorQuestion) { r.Record.Text = "displayed yes" },
		func(r *asksclient.OperatorQuestion) { r.Record.Asker = filepath.Join(t.TempDir(), "other.exe") }, func(r *asksclient.OperatorQuestion) { r.Record.Kept = true },
		func(r *asksclient.OperatorQuestion) { r.Record.Option = "allow" }, func(r *asksclient.OperatorQuestion) { r.Record.Yes = false },
		func(r *asksclient.OperatorQuestion) { r.AnsweredBy = nil }, func(r *asksclient.OperatorQuestion) {
			responder := asksclient.AnswererResponder
			r.AnsweredBy = &responder
		},
		func(r *asksclient.OperatorQuestion) {
			r.Record.Answered = now.Add(-time.Minute).Format(time.RFC3339Nano)
		},
	} {
		copy := read
		metadata := record
		copy.Record = &metadata
		alter(&copy)
		if check(copy) {
			t.Fatalf("unbound folder answer accepted: %+v", copy)
		}
	}
	if approvedOpenCodeFolder(read, "question", owner, question, now.Add(-time.Second), now, now) {
		t.Fatal("expired approval accepted")
	}
}

func TestOpenCodeCompletionRequiresExactCommittedToolReceipt(t *testing.T) {
	now := time.Now()
	request := projectwire.ProjectRequest{RequestKey: "request", SessionID: "session", MessageID: "message"}
	completion := projectwire.ProjectCompletion{RequestKey: "request", SessionID: "session", MessageID: "message", Receipt: "receipt",
		PartID: "part", ToolCallID: "tool-call", CompletedAt: now.UnixMilli()}
	if !validOpenCodeCompletion(completion, request, "receipt", now.Add(-time.Minute), now.Add(time.Minute), now) {
		t.Fatal("matching committed tool receipt refused")
	}
	for _, alter := range []func(*projectwire.ProjectCompletion){
		func(c *projectwire.ProjectCompletion) { c.Receipt = "different" }, func(c *projectwire.ProjectCompletion) { c.MessageID = "different" },
		func(c *projectwire.ProjectCompletion) { c.SessionID = "different" }, func(c *projectwire.ProjectCompletion) { c.PartID = "" },
		func(c *projectwire.ProjectCompletion) { c.ToolCallID = "" }, func(c *projectwire.ProjectCompletion) { c.CompletedAt = now.Add(-time.Hour).UnixMilli() },
		func(c *projectwire.ProjectCompletion) { c.CompletedAt = now.Add(time.Second).UnixMilli() },
	} {
		changed := completion
		alter(&changed)
		if validOpenCodeCompletion(changed, request, "receipt", now.Add(-time.Minute), now.Add(time.Minute), now) {
			t.Fatalf("unbound completion accepted: %+v", changed)
		}
	}
	if validOpenCodeCompletion(completion, request, "receipt", now.Add(-time.Minute), now, now) {
		t.Fatal("expired approval accepted")
	}
}

func TestOpenCodeFolderReceiptDescribesItsActualDecision(t *testing.T) {
	for _, outcome := range []projectwire.ProjectOutcome{projectwire.ProjectOutcomePending, projectwire.ProjectOutcomeDenied, projectwire.ProjectOutcomeExpired, projectwire.ProjectOutcomeConflict, projectwire.ProjectOutcomeUnavailable} {
		if message := openCodeProjectReceiptMessage(outcome); strings.Contains(message, "approval is bound") || !strings.Contains(message, "Access has not changed") && !strings.Contains(message, "access has not changed") {
			t.Fatalf("refusal claimed approval/effect: %s", message)
		}
	}
	if !strings.Contains(openCodeProjectReceiptMessage(projectwire.ProjectOutcomeApprovedPendingClose), "approval is bound") {
		t.Fatal("actual manual approval omitted")
	}
}

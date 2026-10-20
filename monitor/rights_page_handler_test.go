package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// rightsPageReport is what testdata/rights_page_handler.js prints: the text
// the Questions and permissions page shows after its arrival loads.
type rightsPageReport struct {
	TypedJourney []struct {
		Stage   string `json:"stage"`
		Preview string `json:"preview"`
		Outcome string `json:"outcome"`
		Writes  int    `json:"writes"`
	} `json:"typedJourney"`
	ChoicesStatus   string          `json:"choicesStatus"`
	Choices         []choicePageRow `json:"choices"`
	ChoiceHistory   []choicePageRow `json:"choiceHistory"`
	QuestionsStatus string          `json:"questionsStatus"`
	Questions       []struct {
		Lines   []string `json:"lines"`
		Buttons []string `json:"buttons"`
	} `json:"questions"`
	AssistantStatus     string           `json:"assistantStatus"`
	Assistants          [][]string       `json:"assistants"`
	AssistantWarning    []string         `json:"assistantWarning"`
	AssistantButton     string           `json:"assistantButton"`
	Defaults            []defaultPageRow `json:"defaults"`
	DefaultsStatus      string           `json:"defaultsStatus"`
	DefaultsAfter       []defaultPageRow `json:"defaultsAfter"`
	DefaultsAfterStatus string           `json:"defaultsAfterStatus"`
	// Agents is the "Ask me first" switch of Messages between agents: on
	// arrival, then after each turn; AgentsSaved is the default read back
	// after the first turn.
	Agents []struct {
		On       bool   `json:"on"`
		Disabled bool   `json:"disabled"`
		Hidden   bool   `json:"hidden"`
		Status   string `json:"status"`
	} `json:"agents"`
	AgentsSaved    *defaultView `json:"agentsSaved"`
	AnsweredStatus string       `json:"answeredStatus"`
	Answered       []struct {
		Lines   []string `json:"lines"`
		Buttons []string `json:"buttons"`
	} `json:"answered"`
}

// defaultPageRow is one "Defaults for all apps" switch as the page shows it.
type defaultPageRow struct {
	Lines    []string `json:"lines"`
	On       bool     `json:"on"`
	Disabled bool     `json:"disabled"`
}

type choicePageRow struct {
	Lines   []string `json:"lines"`
	Buttons []string `json:"buttons"`
}

// runRightsPage serves the real Panel handler over loopback HTTP and runs the
// page's own script against it in node (testdata/rights_page_handler.js).
// The script reads rights_page.go, service_page.go and panel_shell.go from
// disk, so a `go test -overlay` replacement of those files reaches the
// handler but not the page script node runs.
func runRightsPage(t *testing.T) rightsPageReport {
	return runRightsPageMode(t, "")
}

func runRightsPageMode(t *testing.T, mode string) rightsPageReport {
	t.Helper()
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the page script cannot run")
	}
	server := httptest.NewServer((&servicePanel{}).handler("test-key"))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodePath, "testdata/rights_page_handler.js", server.URL, "test-key", mode)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("page script: %v\n%s", err, stderr.String())
	}
	var report rightsPageReport
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("page report %q: %v", out, err)
	}
	return report
}

func TestRightsPageFixtureFailureDrainsPendingArrivals(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; the page script cannot run")
	}
	server := httptest.NewServer((&servicePanel{}).handler("test-key"))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nodePath, "testdata/rights_page_handler.js", server.URL, "test-key", "fixture-error")
	output, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 1 || !strings.Contains(string(output), "fixture failure while page arrivals are pending") || strings.Contains(string(output), "Assertion failed") {
		t.Fatalf("page failure must drain I/O and retain its error: %v\n%s", err, output)
	}
}

func TestRightsPageTypedReviewReachesNativePublicHandler(t *testing.T) {
	own(t)
	var allow, outage atomic.Bool
	allow.Store(true)
	panelSelectorRuntime(t, &allow, &outage)
	report := runRightsPageMode(t, "typed")
	want := []string{"all-unconfirmed", "all", "one-block", "one-remove", "bundle", "read-block", "origin-remove"}
	if len(report.TypedJourney) != len(want) {
		t.Fatalf("typed page journey %+v", report.TypedJourney)
	}
	for i, stage := range report.TypedJourney {
		if stage.Stage != want[i] || stage.Writes != i || i > 0 && stage.Outcome != "applied" {
			t.Fatalf("typed stage %+v, index %d", stage, i)
		}
	}
	if p := report.TypedJourney[0].Preview; !strings.Contains(p, "current and future") || !strings.Contains(p, "Runtime-managed local keys are excluded") {
		t.Fatalf("All pre-submit effect %q", p)
	}
	if p := report.TypedJourney[4].Preview; !strings.Contains(p, "full-control@1") || !strings.Contains(p, "secret.read") || !strings.Contains(p, "copy") || !strings.Contains(p, "Definition digest:") {
		t.Fatalf("bundle pre-submit effect %q", p)
	}
	if p := report.TypedJourney[6].Preview; !strings.Contains(p, "5 of 6") || !strings.Contains(p, "partial/changed") {
		t.Fatalf("remaining origin pre-submit effect %q", p)
	}
	h := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, h, "/rights?cursor=", nil)
	page := decodePanel[policyPageView](t, r.Code, r.Body.Bytes())
	if len(page.Rules) != 2 {
		t.Fatalf("page left %d rules, want independent All Use and One Read Block", len(page.Rules))
	}
	for _, row := range page.Rules {
		if row.Selector.Origin != nil || row.Action == "abstraction.credentials/secret.read" && (row.Permit || row.Selector.Name != "alpha") || row.Action == "abstraction.credentials/apply" && (!row.Permit || row.Selector.Scope != wire.ResourceSelectorKindAll.String()) {
			t.Fatalf("page changed an independently edited rule %+v", row)
		}
	}
}

// Answering access replaces the historical five-minute setup. The native
// exact-delegation API remains available to existing clients.
func TestRightsPageUsesEnrolledAnsweringAccess(t *testing.T) {
	if strings.Contains(rightsPage, `id="delegation-form"`) || strings.Contains(rightsPage, `call('/rights/delegation'`) {
		t.Fatal("Panel still offers exact-question delegation setup")
	}
	if !strings.Contains(rightsPage, `id="assistant-form"`) || !strings.Contains(rightsPage, `id="answered-list"`) {
		t.Fatal("Panel lacks enrolled answering access and attributed answers")
	}
}

// The Questions page words a first-use question with the native prompt's
// sentence and the prompt's button labels (live walkthrough 2026-10-02, D5):
// the page said "wants to storage content write on output:video" with raw
// option names while the prompt said "save the videos it makes and read them
// back".
func TestRightsPageQuestionReadsAsTheNativePrompt(t *testing.T) {
	const action, resource = "abstraction.storage/content.write", "output:video"
	book, _, program := panelFirstUseRuntime(t, action)
	slots := map[string]string{"program": program, "action": action, "resource": resource}
	record, outcome, err := book.AskApplication("runtime-scope", "runtime", "first-use:panel", asks.FirstUseKey, slots, listen.Seen{Why: "test"})
	if err != nil || outcome != "pending" {
		t.Fatalf("first-use admission %q %v", outcome, err)
	}
	prompt := trayQuestionView(askclient.RecordMetadata{ID: record.ID, Key: record.Key, About: record.About, Text: record.Text, Options: record.Options}, trayPublisher{})
	// The popup's heading asks the page's sentence as a question
	// (architect popup ruling 2026-11-10).
	name := newProgramTitler(nil).name(program)
	sentence := name + " wants to save the videos it makes and read them back."
	if prompt.Instruction != "Allow "+name+" to save the videos it makes and read them back?" {
		t.Fatalf("native prompt heading %q", prompt.Instruction)
	}
	var labels []string
	for _, choice := range prompt.Choices {
		labels = append(labels, choice.Label)
	}
	report := runRightsPage(t)
	if len(report.Questions) != 1 || len(report.Questions[0].Lines) == 0 {
		t.Fatalf("questions shown %+v; status %q", report.Questions, report.QuestionsStatus)
	}
	shown := report.Questions[0]
	// The row label is the prompt's sentence without its final period
	// (architect ruling 2026-11-09: a label never ends in a period).
	if line, _, _ := strings.Cut(shown.Lines[0], "\n"); line != strings.TrimSuffix(sentence, ".") {
		t.Fatalf("Questions page reads %q, the native prompt %q", line, sentence)
	}
	if want := append(labels, "Dismiss"); strings.Join(shown.Buttons, "|") != strings.Join(want, "|") {
		t.Fatalf("Questions page buttons %q, want the prompt's %q", shown.Buttons, want)
	}
}

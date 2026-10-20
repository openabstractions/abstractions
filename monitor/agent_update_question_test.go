package main

import (
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// The connection update reads as a short native popup: its request code and
// local time sit in Details, and no refused-action footer is shown.
func TestAgentUpdatePopupIsShortAndKeepsCodeInDetails(t *testing.T) {
	code := strings.Repeat("c", 48)
	now := time.Date(2026, 11, 10, 15, 0, 0, 0, time.Local)
	record := askclient.RecordMetadata{ID: "q1", Key: asks.AgentUpdateKey, Asked: now.Add(-time.Hour).UTC().Format(time.RFC3339Nano), Options: []string{"update", "not_now"},
		Text: "Update the Claude integration, Codex integration and OpenCode connections? Keep these updated connections as your assistants. Each access permission is reviewed separately. Restart Claude integration, Codex integration and OpenCode to finish. Request " + code + "."}
	view, ok := agentUpdateQuestionView(record, trayPublisher{}, questionChoices(record.Options), now)
	if !ok {
		t.Fatal("sentence not recognised")
	}
	if view.Title != "OpenAbstractions" || view.Instruction != "Update OpenAbstractions in Claude, Codex and OpenCode?" || view.Content != "Restart them afterwards." ||
		view.Footer != "" || view.Level != promptLevelBlue || view.Default != "not_now" || view.Dismiss != "not_now" {
		t.Fatalf("%+v", view)
	}
	if view.Details != "Asked: Today 14:00\nRequest: "+code+"\nQuestion: q1" {
		t.Fatalf("details %q", view.Details)
	}
	if len(view.Choices) != 2 || view.Choices[0].Label != "Update" || view.Choices[1].Label != "Not now" {
		t.Fatalf("choices %+v", view.Choices)
	}
}

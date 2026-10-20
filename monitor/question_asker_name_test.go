package main

import (
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

func commandOnceFixture(program string) (askclient.RecordMetadata, *askclient.CatalogueContent) {
	record := askclient.RecordMetadata{ID: "q-open", Key: asks.ApplicationCommandOnceKey, About: program, Asker: programFileName(program), Options: []string{"once", "refuse"},
		Text: "Open ComfyUI? " + program + " requests this once. Command open. Request 95bfb139f3d46ef94b4b expires 2026-11-10T12:01:26Z."}
	retained := &askclient.CatalogueContent{Slots: map[string]string{"program": program, "operation": "Open ComfyUI", "operationmore": "",
		"command": "open", "code": "95bfb139f3d46ef94b4b", "until": "2026-11-10T12:01:26Z"}}
	return record, retained
}

// A connection program's question names its host app, never its file, in
// one heading (architect popup ruling 2026-11-10, row 7).
func TestCommandOnceQuestionNamesConnectionHost(t *testing.T) {
	const name = "oa-opencode-integration"
	program := installedConnectorImages(t, name)[name]
	signedByPanelSigner(t)
	record, retained := commandOnceFixture(program)
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, program, retained)
	if view.Title != "OpenAbstractions" || view.Instruction != "Allow OpenCode to open ComfyUI this once?" || view.Content != "" {
		t.Fatalf("title %q heading %q body %q", view.Title, view.Instruction, view.Content)
	}
	shown := view.Instruction + view.Content + view.Footer
	for _, gone := range []string{"not registered", "oa-opencode", "must retry", "Request"} {
		if strings.Contains(shown, gone) {
			t.Fatalf("visible text holds %q: %q", gone, shown)
		}
	}
	if view.Permission == nil || view.Permission.Publisher != "Publisher not verified." {
		t.Fatal("publisher fact lost", view.Permission)
	}
	labels := []string{}
	for _, choice := range view.Choices {
		labels = append(labels, choice.Label+"="+choice.Option)
	}
	if strings.Join(labels, ",") != "Allow once=once,Not now=refuse" || view.Dismiss != "refuse" || view.Default != "refuse" || view.Level != promptLevelYellow {
		t.Fatal("buttons or header", labels, view.Dismiss, view.Level)
	}
}

// Details hold the full path, the command, the request and the expiry.
func TestCommandOnceQuestionDetails(t *testing.T) {
	program := `C:\Other\tool.exe`
	record, retained := commandOnceFixture(program)
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, program, retained)
	if view.Instruction != "Allow tool to open ComfyUI this once?" {
		t.Fatal(view.Instruction)
	}
	for _, want := range []string{"Program: " + program, "Command: open", "Request: 95bfb139f3d46ef94b4b", "Expires: 2026-11-10T12:01:26Z"} {
		if !strings.Contains(view.Details, want) {
			t.Fatalf("details miss %q: %q", want, view.Details)
		}
	}
	if strings.Contains(view.Instruction+view.Content, program) {
		t.Fatal("path visible", view.Content)
	}
}

package main

import (
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

func fitTestCommands(specs ...appmodel.CommandSpec) applications.CommandsResult {
	return applications.CommandsResult{Outcome: applications.OutcomePage, Answer: &appmodel.CommandListing{
		Outcome: appmodel.ReadingOutcomePage, Application: "app", Instance: "one", Commands: specs}}
}

func fitTestSpec(name, schema, description string) appmodel.CommandSpec {
	return appmodel.CommandSpec{Name: name, Class: appmodel.CommandClassRead, Title: name, Description: description,
		InputSchema: schema, OutputSchema: `{}`, Targets: []string{}}
}

// One command whose schema the generated writer refuses leaves the listing:
// the other commands are still listed, whole.
func TestApplicationV3FitDropsOneBadCommand(t *testing.T) {
	result, err := fitApplicationV3Reply("Commands", fitTestCommands(
		fitTestSpec("node.read", `{}`, ""), fitTestSpec("node.bad", `{not json`, ""), fitTestSpec("node.list", `{"type":"object"}`, "")))
	if err != nil || result.Outcome != applications.OutcomePage || result.Answer == nil {
		t.Fatalf("result %+v err %v", result, err)
	}
	got := result.Answer.Commands
	if len(got) != 2 || got[0].Name != "node.read" || got[1].Name != "node.list" || got[1].InputSchema != `{"type":"object"}` {
		t.Fatalf("commands %+v", got)
	}
}

// Commands that fit alone but overflow the owned frame together are cut at
// the frame; the kept commands are complete.
func TestApplicationV3FitCutsAtTheFrame(t *testing.T) {
	big := strings.Repeat("d", ownedApplicationFrameLimit/3)
	result, err := fitApplicationV3Reply("Commands", fitTestCommands(
		fitTestSpec("node.a", `{}`, big), fitTestSpec("node.b", `{}`, big), fitTestSpec("node.c", `{}`, big), fitTestSpec("node.d", `{}`, big)))
	if err != nil || result.Answer == nil {
		t.Fatalf("result %+v err %v", result, err)
	}
	got := result.Answer.Commands
	if len(got) == 0 || len(got) >= 4 || got[0].Description != big {
		t.Fatalf("kept %d commands", len(got))
	}
	if !applicationV3ReplyFits(result) {
		t.Fatal("fitted reply exceeds the frame")
	}
}

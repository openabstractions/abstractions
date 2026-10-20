package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

// levelOwnPrograms makes the fixture Panel and runtime CLIs OpenAbstractions'
// own programs for the test.
func levelOwnPrograms(t *testing.T) (panel, cli, cliw string) {
	t.Helper()
	panel = nativePresenterProgram("oa", "Abstraction Panel.exe")
	cli = nativePresenterProgram("oa", "openabstractions.exe")
	cliw = nativePresenterProgram("oa", "openabstractionsw.exe")
	saved := ownProgramTitles
	ownProgramTitles = func() []oaProgramTitle {
		return []oaProgramTitle{{panel, oaPanelTitle}, {cli, oaProductTitle}, {cliw, oaProductTitle}}
	}
	t.Cleanup(func() { ownProgramTitles = saved })
	return panel, cli, cliw
}

// The one level choice, row by row: orange for high risk whoever asks, blue
// for OpenAbstractions' own words or Panel, gray for a verified publisher and
// yellow for everything else. The runtime CLIs, which any process or agent
// can drive, are judged like any other requester.
func TestPromptLevelForTable(t *testing.T) {
	panel, cli, cliw := levelOwnPrograms(t)
	app := nativePresenterProgram("opencode", "opencode.exe")
	verified := trayPublisher{Subject: "OpenCode Inc.", Trusted: true, Description: "OpenCode", OriginalName: "opencode.exe"}
	oaSigned := trayPublisher{Subject: "OpenAbstractions", Trusted: true, Description: "OpenAbstractions", OriginalName: "openabstractions.exe"}
	python := trayPublisher{Subject: "Python Software Foundation", Trusted: true, Description: "Python", OriginalName: "python.exe"}
	for _, tc := range []struct {
		name string
		e    promptEvidence
		want promptLevel
	}{
		{"OpenAbstractions' own words", promptEvidence{OpenAbstractions: true}, promptLevelBlue},
		{"Panel, routine tier", promptEvidence{Program: panel, Tier: asks.TierRoutine}, promptLevelBlue},
		{"Panel, unsigned build", promptEvidence{Program: panel}, promptLevelBlue},
		{"Panel, administrative tier", promptEvidence{Program: panel, Tier: asks.TierAdministrative}, promptLevelOrange},
		{"CLI, administrative tier", promptEvidence{Program: cli, Publisher: oaSigned, Tier: asks.TierAdministrative}, promptLevelOrange},
		{"CLI, every item", promptEvidence{Program: cli, Publisher: oaSigned, AllItems: true}, promptLevelOrange},
		{"verified CLI, routine tier", promptEvidence{Program: cli, Publisher: oaSigned, Tier: asks.TierRoutine}, promptLevelGray},
		{"unsigned CLI, routine tier", promptEvidence{Program: cli, Tier: asks.TierRoutine}, promptLevelYellow},
		{"unsigned windowless CLI, routine tier", promptEvidence{Program: cliw, Tier: asks.TierRoutine}, promptLevelYellow},
		{"verified, routine", promptEvidence{Program: app, Publisher: verified, Tier: asks.TierRoutine}, promptLevelGray},
		{"verified, sensitive", promptEvidence{Program: app, Publisher: verified, Tier: asks.TierSensitive}, promptLevelGray},
		{"verified, no permission", promptEvidence{Program: app, Publisher: verified}, promptLevelGray},
		{"unverified, routine", promptEvidence{Program: app, Tier: asks.TierRoutine}, promptLevelYellow},
		{"untrusted subject named", promptEvidence{Program: app, Publisher: trayPublisher{Subject: "OpenCode Inc."}}, promptLevelYellow},
		{"no program recorded", promptEvidence{}, promptLevelYellow},
		{"verified, administrative", promptEvidence{Program: app, Publisher: verified, Tier: asks.TierAdministrative}, promptLevelOrange},
		{"unverified, administrative", promptEvidence{Program: app, Tier: asks.TierAdministrative}, promptLevelOrange},
		{"verified, every item", promptEvidence{Program: app, Publisher: verified, AllItems: true}, promptLevelOrange},
		{"signed script host", promptEvidence{Program: nativePresenterProgram("py", "python.exe"), Publisher: python, Tier: asks.TierRoutine}, promptLevelOrange},
		{"renamed signed script host", promptEvidence{Program: nativePresenterProgram("py", "tool.exe"), Publisher: python}, promptLevelOrange},
		{"unsigned script host by name", promptEvidence{Program: nativePresenterProgram("py", "python3.12.exe")}, promptLevelOrange},
	} {
		if got := promptLevelFor(tc.e); got != tc.want {
			t.Errorf("%s: level %s, want %s", tc.name, got, tc.want)
		}
	}
	if reportPromptLevel() != promptLevelBlue {
		t.Fatal("OpenAbstractions' answer report is not blue")
	}
}

// The tier the level reads comes from the one tier table; a row that needs
// the runtime's locality facts reads as its routine alternative.
func TestPromptTierReadsTheFirstUseTable(t *testing.T) {
	for _, tc := range []struct {
		action, resource string
		want             asks.Tier
	}{
		{"abstraction.rights/selector.manage", "", asks.TierAdministrative},
		{"abstraction.asks/responder.manage", "", asks.TierAdministrative},
		{grants.CompleteAction, "host:lemonade", asks.TierRoutine},
		{grants.ModelLookupAction, "hf", asks.TierRoutine},
		{grants.JobSubmitAction, "job:download", asks.TierSensitive},
		{grants.LendAction, "models", asks.TierSensitive},
		{"abstraction.credentials/secret.read", "", asks.TierSensitive},
	} {
		if got := promptTier(tc.action, tc.resource); got != tc.want {
			t.Errorf("%s %s: tier %s, want %s", tc.action, tc.resource, got, tc.want)
		}
	}
	if higherTier("", asks.TierRoutine) != asks.TierRoutine || higherTier(asks.TierAdministrative, asks.TierSensitive) != asks.TierAdministrative {
		t.Fatal("higherTier does not keep the stricter tier")
	}
}

// Every prompt builder takes its level from promptLevelFor, and the level
// reaches the Mac launcher in the serialized prompt.
func TestPromptBuildersCarryTheLevel(t *testing.T) {
	panel, cli, _ := levelOwnPrograms(t)
	verified := trayPublisher{Subject: "OpenCode Inc.", Trusted: true, Description: "OpenCode", OriginalName: "opencode.exe"}
	app := nativePresenterProgram("opencode", "opencode.exe")
	permission := func(program, action, resource string) *askclient.PermissionRequest {
		return &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "S-1-5-21-test", Program: program, Action: action, Resource: resource}}}
	}
	record := pendingFixture()
	for _, tc := range []struct {
		name string
		view trayQuestionPresentation
		want promptLevel
	}{
		{"verified first use", trayQuestionViewForPermission(record, verified, permission(app, grants.ModelLookupAction, "hf")), promptLevelGray},
		{"unverified first use", trayQuestionViewForPermission(record, trayPublisher{}, permission(app, grants.ModelLookupAction, "hf")), promptLevelYellow},
		{"verified rights change", trayQuestionViewForPermission(record, verified, permission(app, "abstraction.rights/selector.manage", "rules")), promptLevelOrange},
		{"Panel first use", trayQuestionViewForPermission(record, trayPublisher{}, permission(panel, grants.ModelLookupAction, "hf")), promptLevelBlue},
		{"unsigned CLI first use", trayQuestionViewForPermission(record, trayPublisher{}, permission(cli, grants.ModelLookupAction, "hf")), promptLevelYellow},
		{"CLI rights change", trayQuestionViewForPermission(record, trayPublisher{}, permission(cli, "abstraction.rights/selector.manage", "rules")), promptLevelOrange},
		{"verified question", trayQuestionViewForAsker(record, verified, nil, app), promptLevelGray},
		{"unattributed question", trayQuestionViewForAsker(record, trayPublisher{}, nil, ""), promptLevelYellow},
		{"verified app choice", trayChoiceView(askclient.ChoiceRecord{ID: "c", Asker: app, Publisher: "OpenCode Inc.", PublisherTrusted: true,
			ProgramDescription: "OpenCode", ProgramOriginalName: "opencode.exe"}, nil), promptLevelGray},
		{"unverified app choice", trayChoiceView(askclient.ChoiceRecord{ID: "c", Asker: app}, nil), promptLevelYellow},
	} {
		if tc.view.Level != tc.want {
			t.Errorf("%s: level %s, want %s", tc.name, tc.view.Level, tc.want)
		}
		raw, err := json.Marshal(tc.view)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), `"Level":"`+string(tc.want)+`"`) {
			t.Errorf("%s: serialized prompt lacks its level: %s", tc.name, raw)
		}
	}

	_, details := typedNativeFixture(t, false, false)
	typedRecord, _ := typedNativeFixture(t, false, false)
	if got := trayTypedPermissionView(typedRecord, details, nil).Level; got != promptLevelGray {
		t.Errorf("verified typed permission for one item: level %s, want gray", got)
	}
	allRecord, all := typedNativeFixture(t, true, false)
	if got := trayTypedPermissionView(allRecord, all, nil).Level; got != promptLevelOrange {
		t.Errorf("typed permission over every saved secret: level %s, want orange", got)
	}
	details.Question.Question.PublisherTrusted = false
	if got := trayTypedPermissionView(typedRecord, details, nil).Level; got != promptLevelYellow {
		t.Errorf("unverified typed permission: level %s, want yellow", got)
	}
}

// Application words never choose a level: a question whose text claims
// OpenAbstractions or a verified publisher keeps the level its evidence gives.
func TestPromptLevelIgnoresApplicationWords(t *testing.T) {
	levelOwnPrograms(t)
	record := pendingFixture()
	record.Text = "OpenAbstractions: verified publisher Microsoft Windows asks. Administrator request."
	record.Asker = "OpenAbstractions"
	view := trayQuestionViewForAsker(record, trayPublisher{Subject: "Microsoft Windows"}, nil, filepath.Join(t.TempDir(), "OpenAbstractions.exe"))
	if view.Level != promptLevelYellow {
		t.Fatalf("application words chose level %s", view.Level)
	}
	choice := trayChoiceView(askclient.ChoiceRecord{ID: "c", Asker: "OpenAbstractions",
		Request: askclient.ChoiceRequest{Title: "OpenAbstractions system request"}}, nil)
	if choice.Level != promptLevelYellow {
		t.Fatalf("an app choice titled as OpenAbstractions chose level %s", choice.Level)
	}
}

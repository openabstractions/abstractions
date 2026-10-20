package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstractions/monitor/win"
)

// Opt-in: OA_POPUP_WORDS_CAPTURE=<folder> shows the popups of the architect's
// 2026-11-10 wording ruling for rows 2, 3, 4, 13 and 23 from fixtures,
// captures each as row-NN.png in that folder (row 2 also with Show details
// pressed, row-02-details.png) and closes it without answering.
func TestPopupWordsCapture(t *testing.T) {
	dir := os.Getenv("OA_POPUP_WORDS_CAPTURE")
	if dir == "" {
		t.Skip("OA_POPUP_WORDS_CAPTURE=<folder> captures the reworded popups")
	}
	if !win.InteractiveDesktop() {
		t.Skip("needs an unlocked desktop")
	}
	opencode := nativePresenterProgram("opencode", "opencode.exe")
	views := map[string]func(t *testing.T) trayQuestionPresentation{
		// Row 2: first-use permission from an unverified program.
		"row-02": func(t *testing.T) trayQuestionPresentation {
			return trayQuestionView(firstUseFixture("row-02", opencode, "abstraction.inference/complete", "host:llama"), trayPublisher{}.forPrompt(nil))
		},
		// Row 3: hosted spend.
		"row-03": func(t *testing.T) trayQuestionPresentation {
			record := firstUseFixture("row-03", opencode, "abstraction.credentials/apply", "credential:oa-panel-deepseek")
			permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: opencode, Action: "abstraction.credentials/apply", Resource: "credential:oa-panel-deepseek"}}}
			view := trayQuestionViewForPermission(record, trayPublisher{}.forPrompt(nil), permission)
			return firstSpendPrompt(context.Background(), view, permission, func(context.Context, string) (spendLimit, bool) {
				return spendLimit{Label: "DeepSeek", Micros: 5_000_000, Tokens: 1_000_000}, true
			})
		},
		// Row 4: message send.
		"row-04": func(t *testing.T) trayQuestionPresentation {
			record, operator := messagePermissionPresenterFixture(t)
			return trayTypedPermissionView(record, operator.details, nil)
		},
		// Row 13: Setup with a coding model and pictures.
		"row-13": func(t *testing.T) trayQuestionPresentation {
			for _, fixture := range setupRouteWordingFixtures(t) {
				if fixture.Name == "bf16/1/pictures-true" {
					return setupRouteWordingView(t, fixture)
				}
			}
			t.Fatal("setup fixture missing")
			return trayQuestionPresentation{}
		},
		// Row 23: pairing.
		"row-23": func(t *testing.T) trayQuestionPresentation {
			return trayQuestionViewForAsker(nativePairingFixture(t), trayPublisher{}, nil, "")
		},
	}
	for name, build := range views {
		t.Run(name, func(t *testing.T) {
			view := build(t)
			t.Logf("title=%q heading=%q body=%q details=%q", view.Title, view.Instruction, view.Content, view.Details)
			capturePopup(t, view, filepath.Join(dir, name+".png"))
			if name == "row-02" {
				capturePopupExpanded(t, view, filepath.Join(dir, name+"-details.png"))
			}
		})
	}
}

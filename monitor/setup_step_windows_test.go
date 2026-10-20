//go:build windows

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/openabstractions/abstractions/monitor/win"
)

// The one question is OpenAbstractions' own, in the presenter's native
// dialog: Set up or Not now, with licences behind Show details.
func TestSetupStepNativeQuestion(t *testing.T) {
	fixture := setupRouteWordingFixtures(t)[0]
	q, err := trayWindowsQuestionPrompt(setupRouteWordingView(t, fixture))
	if err != nil {
		t.Fatal(err)
	}
	if q.Level != win.PromptLevelBlue || q.Default != "setup" || len(q.Choices) != 2 ||
		q.Choices[0] != (win.QuestionChoice{Label: "Set up", Value: "setup"}) || q.Choices[1] != (win.QuestionChoice{Label: "Not now", Value: "not_now"}) {
		t.Fatalf("prompt = %+v", q)
	}
	if q.Title != "OpenAbstractions" || q.Instruction != fixture.Title || q.Content != fixture.Instruction || q.Details != fixture.Details || !q.Permission || q.PermissionView == nil || q.AppChoice {
		t.Fatalf("prompt shape = %+v", q)
	}
}

func TestSetupStepClaimIsOnePerSession(t *testing.T) {
	// A test-scoped name: an installed Panel running in this session holds
	// the real one.
	saved := setupStepClaimName
	setupStepClaimName = fmt.Sprintf(`Local\OpenAbstractions.SetupStep.test-%d`, os.Getpid())
	defer func() { setupStepClaimName = saved }()
	release, acquired, err := claimSetupStep()
	if err != nil || !acquired {
		t.Fatalf("first claim = %v, %v", acquired, err)
	}
	defer release()
	if _, again, err := claimSetupStep(); err != nil || again {
		t.Fatalf("second claim = %v, %v", again, err)
	}
}

func TestComfyMaintenanceSharesOneNativeClaim(t *testing.T) {
	saved := officialPicture.claimName
	officialPicture.claimName = fmt.Sprintf(`Local\OpenAbstractions.ComfyMaintenance.test-%d`, os.Getpid())
	t.Cleanup(func() { officialPicture.claimName = saved })
	release, acquired, err := claimNamed(officialPicture.claimName)
	if err != nil || !acquired {
		t.Fatalf("native claim = %v, %v", acquired, err)
	}
	defer release()
	if _, again, err := officialPicture.claim(); err != nil || again {
		t.Fatalf("maintenance passed the held claim = %v, %v", again, err)
	}
}

// A Finish while another process holds this session's step (the background
// presenter resuming work, or an open question) opens the Panel window
// instead of ending with nothing on screen (preview-1031l); --setup keeps
// leaving that step to its holder.
func TestSetupStepFinishOpensThePanelWhileTheStepIsHeld(t *testing.T) {
	// The claim is held elsewhere; the fake never lets this run reach the
	// account's installed runtime.
	held := setupStepClaim
	defer func() { setupStepClaim = held }()
	setupStepClaim = func() (func(), bool, error) { return nil, false, nil }
	if open, err := runSetupStep(nil, nil, true); err != nil || !open {
		t.Fatalf("Finish while held = %v, %v", open, err)
	}
	if open, err := runSetupStep(nil, nil, false); err != nil || open {
		t.Fatalf("--setup while held = %v, %v", open, err)
	}
}

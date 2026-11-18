//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
	"golang.org/x/sys/windows"
)

// setupStepClaimName is the session's setup-step mutex; a test claims its own
// name so a running Panel's claim never meets it.
var setupStepClaimName = `Local\OpenAbstractions.SetupStep`

// claimSetupStep holds this session's one setup step, so a finish-page
// launch, a presenter resume and a Settings Change never ask twice.
func claimSetupStep() (release func(), acquired bool, err error) {
	return claimNamed(setupStepClaimName)
}

func claimNamed(claim string) (release func(), acquired bool, err error) {
	name, err := windows.UTF16PtrFromString(claim)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(handle)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("hold the setup step: %w", err)
	}
	return func() { windows.CloseHandle(handle) }, true, nil
}

// processElevated reports an elevated token. The step runs in the person's
// own unelevated session; a bundle started with Run as administrator would
// otherwise launch it elevated.
func processElevated() (bool, error) {
	var elevated uint32
	var size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &size); err != nil {
		return false, err
	}
	return elevated != 0, nil
}

// runSetupStepNative is the --setup mode on Windows. The runtime raises the
// step's one question in the Book and the presenter draws it; this Panel
// process waits for the presenter's outcome and reads the answer from the
// Book.
func runSetupStepNative(requester *setup.StepRequester, finish bool, update ...bool) (open bool, err error) {
	session := &setupQuestionSession{report: exchangeSetupStep}
	if requester != nil {
		session.after = requester.Request
	}
	elevated, err := processElevated()
	if err != nil {
		return false, err
	}
	if elevated {
		return false, stepNotify(context.Background(), setup.StepNotice{Title: "Set up from Models",
			Body: "Setup runs with your own rights. Open Models and choose Set up.", Tag: "setup"})
	}
	account := probe.Self().Account
	session.present = func(ctx context.Context, id string, facts map[string]string) (bool, error) {
		return handSetupQuestion(ctx, defaultPresenterOperator, account, id, facts, showSetupThroughPresenter)
	}
	open, err = runSetupStep(session.ask, requester, finish, update...)
	if err == nil {
		err = session.completed(context.Background())
	}
	return open, err
}

// showSetupThroughPresenter hands Setup's question to the presenter and
// returns the option the person chose there, read back from the Book.
func showSetupThroughPresenter(ctx context.Context, operator presenterOperator, id string) (string, error) {
	outcome, err := presentThroughPresenter(ctx, id)
	if err != nil || outcome != occurrenceAnswered {
		return "", err
	}
	records, err := operator.ListAllQuestionsContext(ctx)
	if err != nil {
		return "", err
	}
	for _, record := range records {
		if record.ID == id {
			return record.Option, nil
		}
	}
	return "", nil
}

// providerSetupNativeSupported reports whether Finish setup can hand its form
// to the presenter installed beside this Panel.
func providerSetupNativeSupported() bool {
	program, err := presenterExecutable()
	if err != nil {
		return false
	}
	_, err = os.Stat(program)
	return err == nil
}

// runProviderSetupNative is Finish setup: the presenter draws the provider's
// credential form for the Book occurrence id. The Questions page that started
// it shows the outcome on its status line.
func runProviderSetupNative(ctx context.Context, id string) (string, error) {
	return presentThroughPresenter(ctx, id)
}

// runBackground is the Panel's -background and -tray alias, kept for the
// sign-in entry and notification registration an earlier version wrote: it
// starts the presenter beside this Panel and exits.
func runBackground() error {
	if err := launchPresenter(); err != nil {
		var missing *exec.Error
		if errors.As(err, &missing) || errors.Is(err, os.ErrNotExist) {
			log.Printf("the presenter is not installed beside this Panel: %v", err)
			return nil
		}
		return err
	}
	return nil
}

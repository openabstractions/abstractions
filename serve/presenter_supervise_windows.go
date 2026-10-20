package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"
)

// The user-mode supervisor runs in the person's signed-in session, and it
// keeps the Asks presenter running there beside the runtime it supervises.
// The presenter is the one program that draws native questions and
// notifications; with the Panel closed, an app's first-use request still
// reaches the person through it. The sign-in entry starts the presenter at
// sign-in; this restarts it after it exited. The presenter's session claim
// keeps one instance, so a start that meets a running one exits quietly.

// presenterSupervision holds the presenter's supervision operations; tests
// replace each.
type presenterSupervision struct {
	// alive reports whether a presenter holds this session's claim.
	alive func() bool
	// image is the presenter installed beside this runtime; os.ErrNotExist
	// when the installation has none.
	image func() (string, error)
	// launch starts image in the person's desktop session.
	launch func(image, dir string, args ...string) error
	// guard refuses a launch while an installer replaces this installation.
	guard func() error
	// every is the check interval; a presenter that keeps exiting is
	// checked at most every most.
	every, most time.Duration
	sleep       func(context.Context, time.Duration) bool
	logf        func(string, ...any)
}

// installedPresenterSupervision supervises the installed presenter.
func installedPresenterSupervision(logf func(string, ...any)) presenterSupervision {
	return presenterSupervision{
		alive: installedPresenterAlive,
		image: func() (string, error) {
			self, err := os.Executable()
			if err != nil {
				return "", err
			}
			image := filepath.Join(filepath.Dir(self), presenterImage)
			info, err := os.Stat(image)
			if err != nil {
				return "", err
			}
			if !info.Mode().IsRegular() {
				return "", errors.New("installed presenter is not a regular file")
			}
			return image, nil
		},
		launch: launchThroughShell,
		guard:  hostUpgradeGuard,
		every:  10 * time.Second,
		most:   10 * time.Minute,
		logf:   logf,
	}
}

// installedPresenterClaim is the session-local name the installed presenter
// claims (monitor/presenter_instance_windows.go claimQuestionPresenter with
// no explicit runtime endpoint).
var installedPresenterClaim = fmt.Sprintf(`Local\OpenAbstractions.QuestionPresenter.%x`, sha256.Sum256(nil))

// installedPresenterAlive opens the presenter's claim without holding it:
// the claim exists exactly while a presenter runs in this session.
func installedPresenterAlive() bool {
	name, err := windows.UTF16PtrFromString(installedPresenterClaim)
	if err != nil {
		return false
	}
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		return false
	}
	windows.CloseHandle(handle)
	return true
}

// run checks the presenter every interval until ctx ends and starts it when
// none runs. A presenter that exits again before the next check doubles the
// interval up to most; one that stays up resets it.
func (s presenterSupervision) run(ctx context.Context) {
	sleep := s.sleep
	if sleep == nil {
		sleep = sleepContext
	}
	logf := s.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	wait, launched := s.every, false
	for sleep(ctx, wait) {
		if s.alive() {
			wait, launched = s.every, false
			continue
		}
		if launched {
			wait = min(wait*2, s.most)
		}
		image, err := s.image()
		if errors.Is(err, os.ErrNotExist) {
			// A runtime-only package has no presenter to supervise.
			launched = false
			continue
		}
		if err == nil && s.guard != nil {
			err = s.guard()
		}
		if err == nil {
			err = s.launch(image, filepath.Dir(image), presenterArguments)
		}
		if err != nil {
			logf("presenter not started: %v", err)
		} else {
			logf("presenter started")
		}
		launched = true
	}
}

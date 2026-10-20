//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"golang.org/x/sys/windows"
)

// claimQuestionPresenter holds a session-local kernel object for the presenter's
// lifetime. Repeated startup exits quietly; closing the last handle permits a
// later start. This is lifecycle exclusion and conveys no identity or authority.
// Explicit runtime endpoints get separate presenters for isolated development.
func claimQuestionPresenter(endpoint string) (release func(), acquired bool, err error) {
	return claimPresenterObject(fmt.Sprintf(`Local\OpenAbstractions.QuestionPresenter.%x`, sha256.Sum256([]byte(endpoint))))
}

// A popup slot belongs to the interactive session, including separate
// presenter processes handling explicit Panel handoffs.
func claimQuestionPopup(ctx context.Context) (func(), error) {
	path, err := questionPopupPath()
	if err != nil {
		return nil, err
	}
	if err := privatedir.Ensure(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return cas.LockContext(ctx, path, false)
}

func questionPopupPath() (string, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return "", err
	}
	data, err := layout.LocalAppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(data, "OpenAbstractions", "presenter", fmt.Sprintf("question-%d", session)), nil
}

func claimPresenterObject(object string) (release func(), acquired bool, err error) {
	name, err := windows.UTF16PtrFromString(object)
	if err != nil {
		return nil, false, err
	}
	handle, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(handle)
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("claim native question presenter: %w", err)
	}
	return func() { windows.CloseHandle(handle) }, true, nil
}

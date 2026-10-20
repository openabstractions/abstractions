//go:build windows

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// claimQuestionPresenter holds a session-local kernel object for the presenter's
// lifetime. Repeated startup exits quietly; closing the last handle permits a
// later start. This is lifecycle exclusion and conveys no identity or authority.
// Explicit runtime endpoints get separate presenters for isolated development.
func claimQuestionPresenter(endpoint string) (release func(), acquired bool, err error) {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Local\OpenAbstractions.QuestionPresenter.%x`, sha256.Sum256([]byte(endpoint))))
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

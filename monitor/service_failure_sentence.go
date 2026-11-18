package main

import (
	"errors"
	"strings"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Plain sentences for a step that stopped because OpenAbstractions itself
// could not be reached. The resolution chain behind them, with contract and
// capability names, belongs in Technical details.
const (
	sentenceRuntimeTwice       = "OpenAbstractions is installed twice on this PC. Remove one copy in Windows Settings, then retry."
	sentenceRuntimeUnavailable = "OpenAbstractions isn't running right now. Retry in a moment."
	sentenceDownloads          = "Downloads are unavailable. Check the internet connection."
	sentenceNotPublished       = "This download isn't published yet. Try again later."
)

// serviceFailureSentence says in one plain sentence why a step that needed an
// OpenAbstractions service stopped. It answers "" for any other failure. The
// text checks cover a chain that crossed a process boundary as text.
func serviceFailureSentence(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	var resolved *resolution.Error
	switch {
	case errors.Is(err, bootstrap.ErrAmbiguousInstallation), strings.Contains(text, bootstrap.ErrAmbiguousInstallation.Error()):
		return sentenceRuntimeTwice
	case errors.As(err, &resolved) && resolved.Status == resolution.RuntimeUnavailable, strings.Contains(text, "service resolution: "+string(resolution.RuntimeUnavailable)):
		return sentenceRuntimeUnavailable
	case errors.Is(err, setup.ErrDownloadsUnavailable):
		return sentenceDownloads
	case errors.Is(err, setup.ErrNotPublished):
		return sentenceNotPublished
	}
	return ""
}

// stepFailureReason is the row text for a failed Catalog step: the plain
// service sentence when OpenAbstractions could not be reached, otherwise the
// failure's first line.
func stepFailureReason(failure error) string {
	if sentence := serviceFailureSentence(failure); sentence != "" {
		return sentence
	}
	return removalReason(failure)
}

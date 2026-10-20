package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// Only an unreachable download service or its refusal to accept a download
// is "downloads are unavailable"; a refused pin is Setup's own check and
// stays an ordinary failure.
func TestOnlyServiceRefusalsAreDownloadsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pin := PinnedDownload{URL: "https://github.com/openabstractions/redist/releases/download/v0.8.0/a.zip",
		SHA256: strings.Repeat("ab", 32), Bytes: 1, Label: "Fixture package"}
	unapproved := pin
	unapproved.URL = "https://example.com/a.zip"
	if _, err := (PublicTransfer{}).Reserve(ctx, unapproved, "key"); err == nil || errors.Is(err, ErrDownloadsUnavailable) {
		t.Fatalf("an unapproved source host reads as downloads unavailable: %v", err)
	}
	if err := (PublicTransfer{}).Submit(ctx, unapproved, WorkIdentity{Key: "key"}); err == nil || errors.Is(err, ErrDownloadsUnavailable) {
		t.Fatalf("an unapproved source host reads as downloads unavailable at submit: %v", err)
	}
	if _, err := (PublicTransfer{}).Reserve(ctx, pin, ""); err == nil || errors.Is(err, ErrDownloadsUnavailable) {
		t.Fatalf("an empty job key reads as downloads unavailable: %v", err)
	}
	// TestMain points discovery at an endpoint nobody serves.
	if _, err := (PublicTransfer{}).Reserve(ctx, pin, "key"); !errors.Is(err, ErrDownloadsUnavailable) {
		t.Fatalf("an unreachable download service is not downloads unavailable: %v", err)
	}
}

func TestOnlyUnavailableAcceptanceIsDownloadsUnavailable(t *testing.T) {
	for _, outcome := range []api.AcceptanceOutcome{api.AcceptanceOutcomeKeyConflict, api.AcceptanceOutcomeForbidden, api.AcceptanceOutcomeInvalid} {
		if err := acceptanceError(outcome, "r"); err == nil || errors.Is(err, ErrDownloadsUnavailable) {
			t.Fatalf("%s reads as downloads unavailable: %v", outcome, err)
		}
	}
	for _, outcome := range []api.AcceptanceOutcome{api.AcceptanceOutcomeUnavailable, api.AcceptanceOutcomeUnknown, api.AcceptanceOutcomeDefinitelyNotAccepted} {
		if err := acceptanceError(outcome, "r"); !errors.Is(err, ErrDownloadsUnavailable) {
			t.Fatalf("%s is not downloads unavailable: %v", outcome, err)
		}
	}
	if err := acceptanceError(api.AcceptanceOutcomeAccepted, ""); err != nil {
		t.Fatal(err)
	}
}

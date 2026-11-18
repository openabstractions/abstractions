package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The OpenCode row on the owner's PC on 2026-11-22 showed this chain as its
// caption. Each cause reads as one plain sentence; the chain stays the detail.
func TestServiceFailureSentenceNamesTheCausePlainly(t *testing.T) {
	ambiguous := &resolution.Error{Status: resolution.RuntimeUnavailable, Capability: "abstraction.job", Contract: "abstraction.job/acceptance@1", LookedFor: "the installed runtime", Err: bootstrap.ErrAmbiguousInstallation}
	unreachable := &resolution.Error{Status: resolution.RuntimeUnavailable, Capability: "abstraction.job", Contract: "abstraction.job/acceptance@1", LookedFor: "the installed runtime", Err: errors.New("dial: refused")}
	for _, tc := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("%w: %w", setup.ErrDownloadsUnavailable, ambiguous), sentenceRuntimeTwice},
		{fmt.Errorf("%w: %w", setup.ErrDownloadsUnavailable, unreachable), sentenceRuntimeUnavailable},
		{errors.New(fmt.Errorf("%w: %w", setup.ErrDownloadsUnavailable, unreachable).Error()), sentenceRuntimeUnavailable},
		{fmt.Errorf("%w: %w", setup.ErrDownloadsUnavailable, errors.New("tls handshake timeout")), sentenceDownloads},
		{errors.New("the installer exited with 1603"), ""},
		// CATALOG-UNPUBLISHED: a pinned asset its release has not published yet.
		{fmt.Errorf("desktop integration download failed: permanent: download attempt failed: not found: %w", setup.ErrNotPublished), sentenceNotPublished},
	} {
		if got := serviceFailureSentence(tc.err); got != tc.want {
			t.Fatalf("%v: got %q, want %q", tc.err, got, tc.want)
		}
	}
	if got := stepFailureReason(fmt.Errorf("%w: %w", setup.ErrDownloadsUnavailable, ambiguous)); got != sentenceRuntimeTwice || strings.Contains(got, "abstraction.job") {
		t.Fatal(got)
	}
	if got := stepFailureReason(errors.New("the installer exited with 1603.\nmore")); got != "the installer exited with 1603" {
		t.Fatal(got)
	}
	for _, sentence := range []string{sentenceRuntimeTwice, strings.ReplaceAll(sentenceRuntimeUnavailable, "'", `\'`)} {
		if !strings.Contains(setupPage, sentence) {
			t.Fatal("Catalog page lacks the Go sentence", sentence)
		}
	}
}

// The Catalog row renders the plain sentence with Retry and keeps the chain
// in Technical details (setup_service_failure_page_test.js).
func TestCatalogRowShowsServiceFailurePlainly(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for the Panel receiver")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if output, err := exec.CommandContext(ctx, node, "setup_service_failure_page_test.js").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

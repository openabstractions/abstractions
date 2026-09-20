package main

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// A virtualized or unknown view refuses with exit status 4 and names the
// package; a real view passes.
func TestVirtualizedProfileRefusesWithExitFour(t *testing.T) {
	real := func() (bootstrap.ProfileView, error) { return bootstrap.ProfileView{}, nil }
	if err := refuseVirtualizedProfile("serve runtime", real); err != nil {
		t.Fatalf("real view refused: %v", err)
	}
	for name, probe := range map[string]func() (bootstrap.ProfileView, error){
		"virtualized": func() (bootstrap.ProfileView, error) {
			return bootstrap.ProfileView{Virtualized: true, Family: "Claude_pzs8sxrjxfjjc"}, nil
		},
		"unknown": func() (bootstrap.ProfileView, error) { return bootstrap.ProfileView{}, errors.New("probe failed") },
	} {
		err := refuseVirtualizedProfile("serve runtime", probe)
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitVirtualizedProfile || !strings.Contains(err.Error(), "virtualized_profile") {
			t.Fatalf("%s view: %v", name, err)
		}
		if name == "virtualized" && !strings.Contains(err.Error(), "Claude_pzs8sxrjxfjjc") {
			t.Fatalf("refusal names no package: %v", err)
		}
	}
}

// From a contained shell, serve runtime on the account's default state exits 4
// before it opens any listener. Elsewhere the check would start a real runtime,
// so it is skipped.
func TestServeRuntimeRefusesAVirtualizedDefaultState(t *testing.T) {
	view, err := bootstrap.CurrentProfileView()
	if err != nil || !view.Virtualized {
		t.Skipf("profile view %s %v: not inside a packaged app", view, err)
	}
	err = serveRuntime([]string{})
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitVirtualizedProfile || !strings.Contains(err.Error(), view.Family) {
		t.Fatalf("serve runtime: %v", err)
	}
}

// From a contained shell, a CLI command that writes the account's default state
// directly exits 4 before it writes anything.
func TestDefaultStateWritersRefuseAVirtualizedView(t *testing.T) {
	view, err := bootstrap.CurrentProfileView()
	if err != nil || !view.Virtualized {
		t.Skipf("profile view %s %v: not inside a packaged app", view, err)
	}
	for name, run := range map[string]func() error{
		"credentials backend": func() error { return credentialsBackend(io.Discard, "file", "") },
	} {
		err := run()
		var exit *exitError
		if !errors.As(err, &exit) || exit.code != exitVirtualizedProfile {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

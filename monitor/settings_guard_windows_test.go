package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

func TestPanelLifecycleSelectsPrivateStateBeforeAnyAccountLock(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "local")
	roaming := filepath.Join(root, "roaming")
	state := filepath.Join(root, "private-state")
	for _, p := range []string{local, roaming, state} {
		if err := privatedir.Ensure(p); err != nil {
			t.Fatal(err)
		}
	}
	platform := layout.Platform{GOOS: "windows", Known: func(name string) (string, error) {
		switch name {
		case "LocalAppData":
			return local, nil
		case "RoamingAppData":
			return roaming, nil
		}
		return "", fmt.Errorf("unexpected folder %s", name)
	}}
	endpoint := fixtureEndpoint(t, "panel-lifecycle")
	installed := fixtureEndpoint(t, "installed-reference")
	installedEndpoint := func(name string) (string, error) {
		if name != "runtime-v1" {
			t.Fatal(name)
		}
		return installed, nil
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binding := explicitRuntime{Endpoint: endpoint, Program: program, StateDir: state}
	release, err := panelSettingsGuardForEndpoint(context.Background(), &binding, platform, listen.SameEndpointName, installedEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(filepath.Join(state, ".openabstractions-settings-lifecycle.lock")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{local, roaming} {
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) != 0 {
			t.Fatal("private Panel reached account lifecycle/state", p, entries, err)
		}
	}
	if close, err := panelSettingsGuardFor(context.Background(), &binding, platform, listen.SameEndpointName); !errors.Is(err, bootstrap.ErrNoTrustedInstallation) {
		if close != nil {
			close()
		}
		t.Fatalf("default wrapper refusal: %v", err)
	}
	uncertainState := filepath.Join(root, "uncertain-state")
	if err := privatedir.Ensure(uncertainState); err != nil {
		t.Fatal(err)
	}
	uncertain := binding
	uncertain.StateDir = uncertainState
	comparisonFailure := errors.New("native namespace comparison unavailable")
	if release, err := panelSettingsGuardForEndpoint(context.Background(), &uncertain, platform, func(string, string) (bool, error) { return false, comparisonFailure }, installedEndpoint); !errors.Is(err, comparisonFailure) {
		if release != nil {
			release()
		}
		t.Fatal("uncertain endpoint comparison did not refuse", err)
	}
	if entries, err := os.ReadDir(uncertainState); err != nil || len(entries) != 0 {
		t.Fatal("uncertainty acquired a private lock", entries, err)
	}
	for _, bad := range []explicitRuntime{
		{Endpoint: endpoint, Program: program},
		{Endpoint: endpoint, Program: program, StateDir: "relative"},
		{Endpoint: installed, Program: program, StateDir: state},
		{Endpoint: strings.ToUpper(installed), Program: program, StateDir: state},
		{Endpoint: `\\server\pipe\runtime`, Program: program, StateDir: state},
		{Endpoint: `\\.\pipe\`, Program: program, StateDir: state},
		{Endpoint: endpoint, Program: "relative", StateDir: state},
		{Endpoint: endpoint, Program: root, StateDir: state},
		{Endpoint: endpoint, Program: program, StateDir: filepath.Join(local, layout.Name)},
		{Endpoint: endpoint, Program: program, StateDir: filepath.Join(roaming, "Abstraction Panel")},
		{Endpoint: endpoint, Program: program, StateDir: root},
	} {
		if release, err := panelSettingsGuardForEndpoint(context.Background(), &bad, platform, listen.SameEndpointName, installedEndpoint); err == nil {
			release()
			t.Fatal("unsafe private binding admitted", bad)
		}
	}
	alias := filepath.Join(root, "alias")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, local).CombinedOutput(); err != nil {
		t.Fatal(err, string(out))
	}
	bad := binding
	bad.StateDir = filepath.Join(alias, layout.Name)
	if release, err := panelSettingsGuardForEndpoint(context.Background(), &bad, platform, listen.SameEndpointName, installedEndpoint); err == nil {
		release()
		t.Fatal("aliased installed root admitted")
	}
	for _, p := range []string{local, roaming} {
		entries, _ := os.ReadDir(p)
		if len(entries) != 0 {
			t.Fatal("refusal reached account state", p, entries)
		}
	}
	// The same owner retains the ordinary installed path, using fake account
	// metadata here; the real person's folders are never passed to this test.
	release, err = panelSettingsGuardForEndpoint(context.Background(), nil, platform, listen.SameEndpointName, installedEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	release()
	entries, err := os.ReadDir(local)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".openabstractions-settings-lifecycle") {
			found = true
		} else {
			t.Fatal("unexpected installed target", e.Name())
		}
	}
	if !found {
		t.Fatal("installed lifecycle path changed")
	}
}

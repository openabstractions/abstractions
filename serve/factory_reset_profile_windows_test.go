package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFactoryResetProcessNamesUseNativeNameIdentity(t *testing.T) {
	for _, name := range []string{"OPENABSTRACTIONS.EXE", "OpenAbstractionsW.exe", "ABSTRACTION PANEL.EXE"} {
		if !resetProcessName(name) {
			t.Fatalf("missed native process name %q", name)
		}
	}
	for _, name := range []string{"other.exe", "openabstractions-helper.exe", "Abstraction Panel.exe.backup"} {
		if resetProcessName(name) {
			t.Fatalf("unrelated process name %q matched", name)
		}
	}
}

func TestFactoryResetProfileGuardAcceptsInactiveTreeAndExcludesBusyFile(t *testing.T) {
	r, _ := resetFixture(t)
	p := filepath.Join(r.Roaming, "Abstraction Panel", "EBWebView", "Default", "Preferences")
	resetWrite(t, p, "profile")
	release, err := factoryResetProfileGuard(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	release()
	busy, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if release, err := factoryResetProfileGuard(context.Background(), r); err == nil {
		release()
		t.Fatal("active exact OA profile admitted")
	}
}

func TestFactoryResetInactiveProfileMovesUnderExclusiveLifecycle(t *testing.T) {
	r, _ := resetFixture(t)
	profile := filepath.Join(r.Roaming, "Abstraction Panel")
	resetWrite(t, filepath.Join(profile, "EBWebView", "Default", "Preferences"), "profile")
	lifecycle, err := resetGuard(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lifecycle()
	release, err := factoryResetProfileGuard(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	release()
	roots, err := allSettingsRoots(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := moveSettings(context.Background(), roots, "profile-held", os.Rename, io.Discard); err != nil {
		t.Fatal(err)
	}
	resetRead(t, filepath.Join(profile+".oa-reset-profile-held", "EBWebView", "Default", "Preferences"), "profile")
}

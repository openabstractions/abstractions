package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Providers, Credentials and Settings call oaReady from their own scripts,
// so their served HTML declares data-ready-owner="page" in markup and the
// shell's load fallback leaves readiness to them (S12 readiness rule).
func TestS8S9PagesDeclareReadyOwnerInMarkup(t *testing.T) {
	for name, fragment := range map[string]string{"registry": registryPage, "credentials": credentialsPage, "settings": settingsPage} {
		doc := panelPage(httptest.NewRequest("GET", "/x?k=k", nil), "k", name, "", fragment)
		if !strings.Contains(doc, `<div data-ready-owner="page" hidden></div>`) {
			t.Errorf("%s: served HTML declares no data-ready-owner=\"page\" element", name)
		}
		if strings.Contains(doc, "readyOwner=") {
			t.Errorf("%s: readiness ownership is still set from script", name)
		}
	}
}

// About names no endpoint as text, and the identity verdict renders inside
// the This Panel row, not after the group's rows (Settings shots, S12).
func TestSettingsAboutKeepsVerdictInRowAndNoEndpoint(t *testing.T) {
	if !strings.Contains(identitySection, `<div id="identity-view"></div></div></section>`) {
		t.Error("the identity verdict sits outside the This Panel row")
	}
	if strings.Contains(runtimeSupportScript, "connected to the runtime at") {
		t.Error("the runtime sentence names the endpoint")
	}
}

// Developer tools are expert-only: on arrival only the Show developer tools
// switch row is in the page; the forms wait in a <template> until it is on.
func TestSettingsDeveloperToolsWaitForTheSwitch(t *testing.T) {
	i, j := strings.Index(settingsPage, `<template id="developer-template">`), strings.Index(settingsPage, `</template>`)
	if !strings.Contains(settingsPage, `id="developer-switch"`) || i < 0 || j < i {
		t.Fatal("Settings has no Show developer tools switch with its forms in a template")
	}
	outside := settingsPage[:i] + settingsPage[j:]
	for _, id := range []string{`id="explore-program"`, `id="status"`, `id="explore-load"`} {
		if strings.Contains(outside, id) {
			t.Errorf("%s is in the page before the switch is on", id)
		}
	}
	if strings.Contains(explorePage, "sha256:") {
		t.Error("Explore's placeholders name a hash shape")
	}
}

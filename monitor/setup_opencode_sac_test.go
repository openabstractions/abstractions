package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// SAC-OPENCODE-INSTALL: OpenCode's own installer reported success under
// Smart App Control, and OpenCode.exe never reached the disk. The Catalog
// row says what happened in one plain sentence, offers Retry, and keeps the
// Windows records for Technical details.
func TestCatalogSaysSmartAppControlBlockedOpenCodesInstaller(t *testing.T) {
	stubOverviewMachine(t)
	read, present := readOfficialInstall, desktopProgramPresent
	t.Cleanup(func() { readOfficialInstall, desktopProgramPresent = read, present })
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	doc := `{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"opencode","file":"opencode-0.8.0.msi","sha256":"` + strings.Repeat("a", 64) + `","bytes":100,"version":"0.8.0","product_code":"{01234567-89AB-CDEF-0123-456789ABCDEF}"}]}`
	journal, err := journal.WithAdopterAssets([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if journal.Catalog().DesktopIntegration == nil {
		t.Skip("OpenCode integration is a Windows x64 catalog asset")
	}
	p := &servicePanel{setupJournal: &journal}
	desktopProgramPresent = func(setup.AppID) bool { return false }
	written := time.Date(2026, 11, 22, 10, 56, 50, 0, time.UTC)
	block := setup.CodeIntegrityBlock{At: written.Add(4 * time.Second), File: "nsis7z.dll", Policy: "VerifiedAndReputableDesktop", Signatures: "0"}
	const sentence = "Windows Smart App Control blocked OpenCode's installer. Turn off Smart App Control in Windows Security, install OpenCode again, then select Retry."

	cases := []struct {
		name     string
		evidence setup.OfficialInstallEvidence
		blocked  bool
	}{
		{"enforced with its 3077", setup.OfficialInstallEvidence{Registered: true, Version: "1.18.34", Written: written, SmartAppControl: "on", Blocks: []setup.CodeIntegrityBlock{block}}, true},
		{"enforced with an unreadable log", setup.OfficialInstallEvidence{Registered: true, Version: "1.18.34", SmartAppControl: "on", EventsUnread: "EvtQuery: Access is denied."}, true},
		{"3077 with an unreadable policy state", setup.OfficialInstallEvidence{Registered: true, Version: "1.18.34", Blocks: []setup.CodeIntegrityBlock{block}}, true},
		{"never installed", setup.OfficialInstallEvidence{SmartAppControl: "on"}, false},
		{"Smart App Control off", setup.OfficialInstallEvidence{Registered: true, Version: "1.18.34", SmartAppControl: "off"}, false},
		{"evaluation without a block", setup.OfficialInstallEvidence{Registered: true, Version: "1.18.34", SmartAppControl: "evaluation"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			readOfficialInstall = func(app setup.AppID) setup.OfficialInstallEvidence {
				if app != setup.OpenCode {
					t.Fatalf("read the installer records of %q", app)
				}
				return tc.evidence
			}
			facts := readOverview(t, p).DesktopIntegration
			if facts.Installed || facts.Action != "" || facts.Blocked != tc.blocked {
				t.Fatalf("facts: %+v", facts)
			}
			if !tc.blocked {
				if facts.Reason != "Get opens opencode.ai, where you download and install OpenCode yourself. Then Connect links it to OpenAbstractions." || facts.Detail != "" {
					t.Fatalf("an ordinary missing OpenCode changed its row: %+v", facts)
				}
				return
			}
			if facts.Reason != sentence {
				t.Fatalf("reason: %q", facts.Reason)
			}
			for _, part := range []string{"registered version 1.18.34", "OpenCode.exe is missing"} {
				if !strings.Contains(facts.Detail, part) {
					t.Fatalf("detail %q lacks %q", facts.Detail, part)
				}
			}
			if len(tc.evidence.Blocks) > 0 && !strings.Contains(facts.Detail, "Code Integrity 3077 at 2026-11-22T10:56:54Z blocked nsis7z.dll under VerifiedAndReputableDesktop (3089 signatures: 0).") {
				t.Fatalf("detail: %q", facts.Detail)
			}
			if tc.evidence.EventsUnread != "" && !strings.Contains(facts.Detail, "Access is denied") {
				t.Fatalf("detail: %q", facts.Detail)
			}
		})
	}
}

//go:build windows

package setup

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The R60 Sandbox's Code Integrity 3077 for the vendor's nsis7z.dll
// (.forks/q/r60-release-preparation/nsis-policy-probe), trimmed to the
// fields Setup reads.
const sandbox3077 = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-CodeIntegrity' Guid='{4ee76bd8-3cf4-44a0-a0ac-3937643e37a3}'/><EventID>3077</EventID><TimeCreated SystemTime='2026-11-22T10:56:54.2002345Z'/><Correlation ActivityID='{206d69a2-7a70-0002-19c1-6d20707add01}'/></System><EventData><Data Name='File Name'>\Device\HarddiskVolume2\Users\WDAGUtilityAccount\AppData\Local\Temp\nsx1.tmp\nsis7z.dll</Data><Data Name='PolicyName'>VerifiedAndReputableDesktop</Data></EventData></Event>`

func TestCodeIntegrityEventReadsTheBlockedFile(t *testing.T) {
	var event codeIntegrityEvent
	if err := xml.Unmarshal([]byte(sandbox3077), &event); err != nil {
		t.Fatal(err)
	}
	if event.System.EventID != 3077 || event.System.TimeCreated.SystemTime != "2026-11-22T10:56:54.2002345Z" ||
		event.System.Correlation.ActivityID != "{206d69a2-7a70-0002-19c1-6d20707add01}" ||
		event.field("PolicyName") != "VerifiedAndReputableDesktop" || !strings.HasSuffix(event.field("File Name"), `\nsis7z.dll`) {
		t.Fatalf("event: %+v", event)
	}
}

// The real reader runs read-only on this host and degrades instead of failing.
func TestReadOfficialInstallIsReadOnlyAndDegrades(t *testing.T) {
	e := ReadOfficialInstall(OpenCode)
	if !e.Registered && (e.SmartAppControl != "" || len(e.Blocks) != 0) {
		t.Fatalf("records read without the installer's registration: %+v", e)
	}
	if got := ReadOfficialInstall("not-an-app"); got.Registered {
		t.Fatalf("an app without an official program: %+v", got)
	}
}

// The Catalog reads the same uninstall registration as the adopter's native
// preflight.
func TestOfficialUninstallKeyMatchesTheAdopterPreflight(t *testing.T) {
	official, ok := OfficialProgramOf(OpenCode)
	if !ok || official.UninstallKey == "" {
		t.Fatalf("official program: %+v", official)
	}
	header, err := os.ReadFile(filepath.Join("..", "..", "..", "adopters", "opencode", "installer", "desktop_state.h"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `#define OA_DESKTOP_ARP L"Software\\Microsoft\\Windows\\CurrentVersion\\Uninstall\\` + official.UninstallKey + `"`; !strings.Contains(string(header), want) {
		t.Fatalf("desktop_state.h lacks %s", want)
	}
}

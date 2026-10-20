package setup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func TestDesktopStagingPreservesThePreviousPackageOnRefusal(t *testing.T) {
	dir := t.TempDir()
	pin := DesktopIntegration{File: "opencode-0.8.0.msi", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("correct"))), Bytes: 7}
	source := filepath.Join(t.TempDir(), pin.File)
	target := filepath.Join(dir, pin.File)
	write := func(path, body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(target, "previous")
	write(source, "changed")
	reviewTarget := func(want string) {
		t.Helper()
		raw, err := os.ReadFile(target)
		if err != nil || string(raw) != want {
			t.Fatalf("published bytes %q (%v), want %q", raw, err, want)
		}
		staged, err := filepath.Glob(filepath.Join(dir, pin.File+".*.tmp"))
		if err != nil || len(staged) != 0 {
			t.Fatalf("temporary files remain: %v (%v)", staged, err)
		}
	}
	if err := pin.publishStaged(t.Context(), dir, source); err == nil {
		t.Fatal("a changed stream was published")
	}
	reviewTarget("previous")
	write(source, "correct")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := pin.publishStaged(ctx, dir, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled staging returned %v", err)
	}
	reviewTarget("previous")
	if err := pin.publishStaged(t.Context(), dir, source); err != nil {
		t.Fatal(err)
	}
	reviewTarget("correct")
}

func TestDesktopPackageSelectionUsesNativePackageIdentity(t *testing.T) {
	const productCode = "{667063CC-A19F-4D30-A3A5-6AF67081776A}"
	const current = "{84D0E6D2-7BBB-47DB-9C4B-4133D0D26D10}"
	const earlier = "{A4EB3891-F0BC-45BF-A60A-741255BBDEAD}"
	const otherProduct = "{7A829572-0B2A-4BEF-83E8-9223E900DECF}"
	for _, tc := range []struct {
		name, product, assignment, observed, pin, action string
		reinstall, refused                               bool
	}{
		{"absent", "", "", "", current, "install", false, false},
		{"current package", productCode, "0", current, current, "repair", true, false},
		{"same-product rebuild", productCode, "0", earlier, current, "update", true, false},
		{"same-version replacement product", otherProduct, "0", earlier, current, "update", false, false},
		{"unrecognizable registration", productCode, "0", "not a code", current, "", false, true},
		{"machine-wide package", productCode, "1", current, current, "", false, true},
		{"legacy Catalog", productCode, "0", "", "", "repair", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pin := DesktopIntegration{ProductCode: productCode, PackageCode: tc.pin, UpgradeCode: openCodeWatcherFixup.RepairUpgradeCode}
			related := func(upgrade string) ([]string, error) {
				if upgrade != pin.UpgradeCode {
					t.Fatalf("unexpected family %q", upgrade)
				}
				if tc.product == "" {
					return nil, nil
				}
				return []string{tc.product}, nil
			}
			info := func(product, property string) (string, error) {
				if product != tc.product {
					t.Fatalf("unexpected registration %q", product)
				}
				switch property {
				case "AssignmentType":
					return tc.assignment, nil
				case "PackageCode":
					if tc.pin == "" || product != productCode || tc.assignment != "0" {
						t.Fatal("unsafe or unnecessary package identity lookup")
					}
					return tc.observed, nil
				default:
					t.Fatalf("unexpected property %q", property)
					return "", nil
				}
			}
			action, reinstall, err := pin.installerSelection(related, info)
			if action != tc.action || reinstall != tc.reinstall || (err != nil) != tc.refused {
				t.Fatalf("selection = %q %t %v", action, reinstall, err)
			}
		})
	}
}

func installedDesktopDiagnostics(t *testing.T) installerDiagnostics {
	t.Helper()
	spec, ok := product.DesktopInstallerLog(string(OpenCode))
	if !ok {
		t.Fatal("the desktop installer has no diagnostic grammar")
	}
	return newInstallerDiagnostics(spec)
}

func TestOpenCodeGuardMessage(t *testing.T) {
	diagnostics := installedDesktopDiagnostics(t)
	const start = "Action start 11:02:34: CheckOpenCodeClosed.\r\n"
	const failure = "\r\nAction ended 11:02:34: CheckOpenCodeClosed. Return value 3.\r\n"
	const unread = "OA OpenCode guard stage=desktop_process_identity_unread win32=50 hresult=-2147024846 exception=0"
	const busy = "OA OpenCode guard stage=desktop_process_running win32=170 hresult=-2147024726 exception=0"
	for _, tc := range []struct{ name, input, want string }{
		{"r45", start + unread + failure, "could not verify"},
		{"pid", start + unread + " pid=1234" + failure, "process ID 1234"},
		{"running", start + busy + failure, "Close OpenCode completely"},
		{"plain1603", start + failure, ""},
		{"wrong action", strings.ReplaceAll(start+unread+failure, "CheckOpenCodeClosed", "OtherAction"), ""},
		{"success", start + unread + strings.ReplaceAll(failure, "value 3", "value 1"), ""},
		{"truncated", start + unread, ""},
		{"unexpected suffix", start + unread + " C:\\personal.exe" + failure, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := diagnostics.guardMessage(tc.input)
			if tc.want == "" && got != "" || tc.want != "" && (!strings.Contains(got, tc.want) || !strings.Contains(got, "retry from Catalog in Panel")) {
				t.Fatalf("guard message = %q, wanted %q", got, tc.want)
			}
		})
	}
}

func TestOpenCodeInstallerFailureProjectsOnlyOwnedDiagnostics(t *testing.T) {
	diagnostics := installedDesktopDiagnostics(t)
	base := errors.New("Windows Installer could not apply the desktop integration (Windows Installer code 1603)")
	log := strings.Join([]string{
		`Property(S): UserSID = S-1-5-21-123-456-789-1001`,
		`CustomActionData C:\Users\private\config.json`,
		`Action ended 21:11:41: CheckOpenCodeClosed. Return value 1.`,
		`OA OpenCode child stage=configure win32=0 wait=258 exit=259`,
		`OA OpenCode child stage=unexpected win32=9 wait=0 exit=1`,
		`OA OpenCode child stage=prepare_integration win32=1 wait=0 exit=0 private`,
		`OA OpenCode guard stage=trusted_payload win32=2 hresult=-2147024894 exception=0`,
		`OA OpenCode guard stage=guard_exception win32=574 hresult=-2147418113 exception=-1073741819`,
		`OA OpenCode guard stage=configuration_request win32=2 hresult=-2147024894 exception=0 path=private`,
		`Action ended 21:12:03: InstallOpenCodeIntegration. Return value 8.`,
		`Action ended 21:12:04: UnknownAction. Return value 3.`,
	}, "\r\n")
	got := diagnostics.failure(base, log, "")
	want := base.Error() + "\nOpenCode MSI: CheckOpenCodeClosed return=1; child configure win32=0 wait=258 exit=259; " +
		"guard trusted_payload win32=2 hresult=-2147024894 exception=0; " +
		"guard guard_exception win32=574 hresult=-2147418113 exception=-1073741819; InstallOpenCodeIntegration return=8"
	if !errors.Is(got, base) || got.Error() != want {
		t.Fatalf("got %q, wanted %q", got, want)
	}
	guard := "Action start 21:11:41: CheckOpenCodeClosed.\n" +
		"OA OpenCode guard stage=desktop_process_running win32=170 hresult=-2147024726 exception=0 pid=1234\n" +
		"Action ended 21:11:41: CheckOpenCodeClosed. Return value 3."
	got = diagnostics.failure(base, guard, "")
	want = "Close OpenCode completely, then retry from Catalog in Panel.\n" +
		"OpenCode MSI: guard desktop_process_running win32=170; CheckOpenCodeClosed return=3"
	if got.Error() != want {
		t.Fatalf("got %q, wanted %q", got, want)
	}
	if got := diagnostics.failure(base, strings.Repeat("Action ended 21:11:41: CheckOpenCodeClosed. Return value 1.\n", 16), ""); strings.Count(got.Error(), "CheckOpenCodeClosed") != 8 {
		t.Fatalf("unbounded diagnostics: %q", got)
	}
	got = diagnostics.failure(base, `OA OpenCode guard stage=other win32=2 hresult=-2147024894 exception=0`, "log tail only")
	if want := base.Error() + "\nOpenCode MSI: no recognized events; log tail only"; got.Error() != want {
		t.Fatalf("got %q, wanted %q", got, want)
	}
}

func TestOpenCodeSettingsConflictRequiresFailedConfigureAction(t *testing.T) {
	diagnostics := installedDesktopDiagnostics(t)
	base := errors.New("Windows Installer code 1603")
	const start = "Action start 15:51:49: InstallOpenCodeIntegration.\n"
	const child = "OA OpenCode child stage=configure win32=0 wait=0 exit=80\n"
	const end = "Action ended 15:52:27: InstallOpenCodeIntegration. Return value 3.\n"
	for _, tc := range []struct {
		name, transcript string
		conflict         bool
	}{
		{"configure conflict", start + child + end, true},
		{"outside action", child + start + end, false},
		{"other child stage", start + strings.Replace(child, "configure", "prepare_integration", 1) + end, false},
		{"timeout", start + strings.Replace(child, "wait=0", "wait=258", 1) + end, false},
		{"successful action", start + child + strings.Replace(end, "value 3", "value 1", 1), false},
		{"rollback record", strings.ReplaceAll(start+child+end, "InstallOpenCodeIntegration", "RollbackOpenCodeInstall"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := diagnostics.failure(base, tc.transcript, "")
			var projected *DesktopInstallerFailure
			if !errors.As(failure, &projected) || !errors.Is(failure, base) {
				t.Fatalf("missing public reason or diagnostic: %v", failure)
			}
			if strings.Contains(projected.Reason, "Remove only the OpenAbstractions entry from each affected plugin list") != tc.conflict ||
				strings.Contains(projected.Reason, "1603") {
				t.Fatalf("unexpected Panel reason %q", projected.Reason)
			}
		})
	}
}

func TestDesktopInstallerDiagnosticsAreBoundToTheSelectedGrammar(t *testing.T) {
	if _, ok := product.DesktopInstallerLog("unrelated"); ok {
		t.Fatal("an unrelated Catalog entry received installer diagnostics")
	}
	diagnostics := newInstallerDiagnostics(product.DesktopInstallerDiagnostics{
		Title: "Example + Editor", Prefix: "OA Example + Editor", ProcessAction: "CheckExampleClosed",
		Actions: []string{"CheckExampleClosed"}, FailureStages: []string{"example_failure"}, ChildStages: []string{"example_child"},
	})
	log := strings.Join([]string{
		"Action start 21:11:41: CheckExampleClosed.",
		"OA Example + Editor guard stage=desktop_process_running win32=170 hresult=-2147024726 exception=0",
		"Action ended 21:11:41: CheckExampleClosed. Return value 3.",
		"OA OpenCode guard stage=trusted_payload win32=2 hresult=-2147024894 exception=0",
		"OA Example  Editor guard stage=example_failure win32=2 hresult=-2147024894 exception=0",
		"Action ended 21:11:42: InstallOpenCodeIntegration. Return value 3.",
	}, "\n")
	want := "Close Example + Editor completely, then retry from Catalog in Panel.\n" +
		"Example + Editor MSI: guard desktop_process_running win32=170; CheckExampleClosed return=3"
	if got := diagnostics.failure(errors.New("desktop installer failed"), log, ""); got.Error() != want {
		t.Fatalf("got %q, wanted %q", got, want)
	}
}

func TestOpenCodeInstallerLogReadsBoundedUTF16Tail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install.log")
	if text, status := desktopInstallerLog(path); text != "" || status != "log unavailable" {
		t.Fatalf("missing log returned (%q, %q)", text, status)
	}
	line := "OA OpenCode child stage=configure win32=1460 wait=258 exit=-1\r\n"
	raw := []byte{0xff, 0xfe}
	for _, unit := range utf16.Encode([]rune(strings.Repeat("x", int(desktopInstallLogLimit)) + "\r\n" + line)) {
		raw = binary.LittleEndian.AppendUint16(raw, unit)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if text, status := desktopInstallerLog(path); text != line || status != "log tail only" {
		t.Fatalf("unexpected tail or status %q, tail length %d", status, len(text))
	}
}

func TestPrivateInstallerLogDir(t *testing.T) {
	parent := t.TempDir()
	first, release, err := privateInstallerLogDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if release != nil {
			release()
		}
	})
	second, releaseSecond, err := privateInstallerLogDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSecond()
	if first == second || filepath.Dir(first) != parent || filepath.Dir(second) != parent {
		t.Fatalf("log directories escaped or shared their leaf: %q and %q", first, second)
	}
	if err := privatedir.CheckDir(first); err != nil {
		t.Fatal(err)
	}
	release()
	release = nil
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("log directory remained after release: %v", err)
	}
}

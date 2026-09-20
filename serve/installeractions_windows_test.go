package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func fakeIdentityOps(inspectErr error) identityOps {
	return identityOps{
		self:    func() uint32 { return 40 },
		parent:  func(pid uint32) (uint32, string, error) { return 30, "msiexec.exe", nil },
		created: func() (int64, error) { return 5000, nil },
		inspect: func(pid uint32) (int64, string, error) {
			if inspectErr != nil {
				return 0, "", inspectErr
			}
			return 4000, `C:\Windows\System32\msiexec.exe`, nil
		},
	}
}

func TestInstallerIdentityRecordsAnUnreadableParent(t *testing.T) {
	got, err := installerIdentityWith(fakeIdentityOps(&openProcessError{pid: 30, err: windows.ERROR_ACCESS_DENIED}))
	if err != nil || got != (processIdentity{PID: 30, Created: 0, Image: "msiexec.exe"}) {
		t.Fatalf("access-denied parent: %+v %v", got, err)
	}
	record := upgradeExclusion{Version: 1, Scope: "user", Folders: []string{testFolder}, Installer: got, Begun: time.Now().UTC()}
	if err := record.validate(); err != nil {
		t.Fatalf("record with an unreadable holder refused: %v", err)
	}
	data, _ := json.Marshal(record)
	if _, err := decodeExclusion(data); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got, err = installerIdentityWith(fakeIdentityOps(nil))
	if err != nil || got != (processIdentity{PID: 30, Created: 4000, Image: `C:\Windows\System32\msiexec.exe`}) {
		t.Fatalf("readable parent: %+v %v", got, err)
	}
	for name, inspectErr := range map[string]error{
		"another open failure": &openProcessError{pid: 30, err: windows.ERROR_INVALID_PARAMETER},
		"denied after opening": windows.ERROR_ACCESS_DENIED,
	} {
		if _, err := installerIdentityWith(fakeIdentityOps(inspectErr)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	reused := fakeIdentityOps(nil)
	reused.created = func() (int64, error) { return 3000, nil }
	if _, err := installerIdentityWith(reused); err == nil || !strings.Contains(err.Error(), "reused") {
		t.Fatalf("a parent younger than this process was accepted: %v", err)
	}
}

// A standard token cannot open a SYSTEM service host, as the impersonated
// begin-upgrade cannot open the SYSTEM msiexec server above it.
func TestInstallerIdentityOfARealDeniedSystemParent(t *testing.T) {
	if windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("elevated: SYSTEM processes are readable, so no open is denied")
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(snap)
	var pid uint32
	var name string
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	for err = windows.Process32First(snap, &entry); err == nil && pid == 0; err = windows.Process32Next(snap, &entry) {
		image := windows.UTF16ToString(entry.ExeFile[:])
		if !strings.EqualFold(image, "svchost.exe") && !strings.EqualFold(image, "spoolsv.exe") {
			continue
		}
		var open *openProcessError
		if _, _, inspectErr := inspectProcess(entry.ProcessID); errors.As(inspectErr, &open) && errors.Is(open, windows.ERROR_ACCESS_DENIED) {
			pid, name = entry.ProcessID, image
		}
	}
	if pid == 0 {
		t.Skip("no service host denied this token")
	}
	ops := systemIdentityOps()
	ops.parent = func(uint32) (uint32, string, error) { return pid, name, nil }
	got, err := installerIdentityWith(ops)
	if err != nil || got != (processIdentity{PID: pid, Created: 0, Image: name}) {
		t.Fatalf("denied SYSTEM parent %d: %+v %v", pid, got, err)
	}
	begun := time.Now()
	if !processAlive(got, begun, begun) || processAlive(got, begun, begun.Add(exclusionUnverifiedLimit)) {
		t.Fatalf("denied holder %d liveness is not bounded by the unverified limit", pid)
	}
}

func TestUnreadableHolderIsHonouredOnlyWithinTheLimit(t *testing.T) {
	self := processIdentity{PID: uint32(os.Getpid()), Created: 0}
	begun := time.Now()
	if !processAlive(self, begun, begun.Add(exclusionUnverifiedLimit-time.Minute)) {
		t.Fatal("a present unreadable holder was not honoured within the limit")
	}
	if processAlive(self, begun, begun.Add(exclusionUnverifiedLimit)) {
		t.Fatal("an unreadable holder was honoured after the limit")
	}
	if processAlive(processIdentity{PID: 0xFFFFFFF1, Created: 0}, begun, begun) {
		t.Fatal("a gone unreadable holder was honoured")
	}
}

func TestInstallerActionFailureIsRecordedBestEffort(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 15, 12, 0, 0, 0, time.FixedZone("x", 3600))
	path := func(scope string) (string, error) {
		return filepath.Join(dir, scope, "upgrade-v1", "installer-actions.txt"), nil
	}
	failure := errors.New("open parent process 30:\nAccess is denied.")
	recordInstallerFailure(path, "user", "service begin-upgrade --user", failure, at)
	recordInstallerFailure(path, "user", "service start --related", errors.New("exit status 1"), at)
	data, err := os.ReadFile(filepath.Join(dir, "user", "upgrade-v1", "installer-actions.txt"))
	want := "2026-09-15T11:00:00Z service begin-upgrade --user: open parent process 30: Access is denied.\n" +
		"2026-09-15T11:00:00Z service start --related: exit status 1\n"
	if err != nil || string(data) != want {
		t.Fatalf("recorded %q err=%v", data, err)
	}
	blocker := filepath.Join(dir, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Neither an unwritable nor an unknown location panics or reports.
	recordInstallerFailure(func(string) (string, error) {
		return filepath.Join(blocker, "upgrade-v1", "installer-actions.txt"), nil
	}, "user", "host register", failure, at)
	recordInstallerFailure(func(string) (string, error) { return "", errors.New("no known folder") }, "user", "host register", failure, at)

	for _, tc := range []struct {
		request        serviceRequest
		command, scope string
	}{
		{serviceRequest{command: "begin-upgrade", scope: "user"}, "begin-upgrade --user", "user"},
		{serviceRequest{command: "end-upgrade", scope: "machine"}, "end-upgrade --machine", "machine"},
		{serviceRequest{command: "start", scope: "user", related: "{X}"}, "start --related", "user"},
		{serviceRequest{command: "start", scope: "machine"}, "start --machine", "machine"},
		{serviceRequest{command: "upgrade-check"}, "", ""},
	} {
		if command, scope := installerAction(tc.request); command != tc.command || scope != tc.scope {
			t.Fatalf("%+v: %q %q", tc.request, command, scope)
		}
	}
	for _, scope := range []string{"user", "machine"} {
		actions, err := installerActionsPath(scope)
		record, recordErr := exclusionPath(scope)
		if err != nil || recordErr != nil || filepath.Dir(actions) != filepath.Dir(record) || filepath.Base(actions) != "installer-actions.txt" {
			t.Fatalf("%s: actions %q beside record %q (%v %v)", scope, actions, record, err, recordErr)
		}
	}
}

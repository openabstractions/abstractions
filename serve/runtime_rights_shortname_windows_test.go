package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows"
)

func shortPathName(t *testing.T, path string) (string, bool) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || n >= uint32(len(buf)) {
		t.Fatalf("short path of %q: %v", path, err)
	}
	short := windows.UTF16ToString(buf[:n])
	return short, !strings.EqualFold(short, path)
}

// A person's deny for an operator program, stored under a short spelling
// before the program existed (as an older runtime stored it), survives the
// program's installation: installation finds the deny once the file exists
// and writes no permit over it.
func TestInstallationWritesNoPermitOverAShortSpelledDeny(t *testing.T) {
	state := t.TempDir()
	folder := filepath.Join(t.TempDir(), "later panel")
	panel := filepath.Join(folder, "Abstraction Panel.exe")
	install := func() {
		if err := os.MkdirAll(folder, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(panel, []byte("panel"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	install()
	short, ok := shortPathName(t, panel)
	if !ok {
		t.Skip("filesystem has no distinct DOS alias for this file")
	}

	// The person denies the Panel config edits; the state then names the
	// Panel by its short spelling, and the Panel is removed.
	r := testRights(t, state)
	subject := rwire.Subject{Account: r.owner, Program: panel}
	if err := r.policy.Set(subject, host.ConfigEditAction, host.ConfigEditResource, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	quotedLong, _ := json.Marshal(panel)
	quotedShort, _ := json.Marshal(short)
	if strings.Count(string(data), string(quotedLong)) != 1 {
		t.Fatalf("state names %q other than once: %s", panel, data)
	}
	if err := os.WriteFile(r.path, []byte(strings.Replace(string(data), string(quotedLong), string(quotedShort), 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(folder); err != nil {
		t.Fatal(err)
	}

	// The runtime starts while the Panel is missing, the Panel is installed,
	// and then installation runs for it.
	started := testRights(t, state)
	install()
	if again, ok := shortPathName(t, panel); !ok || again != short {
		t.Skipf("the reinstalled Panel has the alias %q, not %q", again, short)
	}
	started.operators = []string{started.operators[0], panel}
	if err := started.install(nil, []installationRule{{host.ConfigEditAction, host.ConfigEditResource}}); err != nil {
		t.Fatal(err)
	}
	read := started.policy.ReadRule(subject, host.ConfigEditAction, host.ConfigEditResource)
	if read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit || read.Record.Why == installationWhy {
		t.Fatalf("the person's deny after installation: %+v %+v", read, read.Record)
	}
	if d := started.policy.Decide(subject, host.ConfigEditAction, host.ConfigEditResource); d.Outcome != rwire.DecisionOutcomeDenied {
		t.Fatalf("Panel config edit after installation: %v, want denied", d.Outcome)
	}
	for _, rule := range installedRules(t, started) {
		if rule.Permit && rule.Action == host.ConfigEditAction && strings.EqualFold(filepath.Base(rule.Subject.Program), "Abstraction Panel.exe") {
			t.Fatalf("installation wrote a permit for the Panel: %+v", rule)
		}
	}
}

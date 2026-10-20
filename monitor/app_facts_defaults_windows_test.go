//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/probe"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows"
)

// The app page asks the runtime, so a Block saved for an 8.3 alias of the
// app's program is the app's own rule: the download default leaves the page,
// as it stops permitting the app (RIGHTS-O14 over the operator wire).
func TestAppPageDefaultsFollowTheRuntimeUnderAShortAlias(t *testing.T) {
	own(t)
	var allow atomic.Bool
	allow.Store(true)
	policy := panelDefaultsRuntime(t, &allow)
	if err := policy.RegisterAction(rights.ActionJobSubmit); err != nil {
		t.Fatal(err)
	}
	spaced := filepath.Join(t.TempDir(), "pretend lemonade")
	if err := os.Mkdir(spaced, 0o755); err != nil {
		t.Fatal(err)
	}
	long := filepath.Join(spaced, "lemonade.exe")
	if err := os.WriteFile(long, []byte("app"), 0o755); err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	p, _ := windows.UTF16PtrFromString(long)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		t.Fatal(err)
	}
	short := windows.UTF16ToString(buf[:n])
	if strings.EqualFold(short, long) {
		t.Skip("filesystem has no distinct DOS alias for this file")
	}
	entry := fwire.ApplicationEntry{Descriptor: fwire.ApplicationDescriptor{Name: "pretend-lemonade", Program: long, Title: "Pretend Lemonade"}}
	listed := func() []string {
		facts := appFacts{RulesOutcome: "page", Defaults: []appDefault{}}
		appDefaultFacts(context.Background(), entry, &facts)
		if facts.DefaultsOutcome != "page" {
			t.Fatalf("defaults %s", facts.DefaultsOutcome)
		}
		var names []string
		for _, d := range facts.Defaults {
			names = append(names, d.Name)
		}
		return names
	}
	if names := listed(); !contains(names, rights.DefaultDownload) {
		t.Fatalf("the download default does not read as allowed: %v", names)
	}
	if err := policy.Set(rwire.Subject{Account: probe.Self().Account, Program: short}, rights.ActionJobSubmit, rights.ResourceDownloadJobs, false); err != nil {
		t.Fatal(err)
	}
	if names := listed(); contains(names, rights.DefaultDownload) {
		t.Fatalf("a Block saved for the short alias %q still reads as allowed for all apps: %v", short, names)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

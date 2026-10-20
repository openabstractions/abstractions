//go:build windows

package main

import (
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// The prompt's icon comes only from a service-bound program: the bound asker
// when the record keeps one, otherwise the permission's subject. A script
// host, a mismatched asker or a non-path shows the generic icon.
func TestPermissionIconProgramSelection(t *testing.T) {
	const app = `C:\Program Files\Editor\editor.exe`
	signed := trayPublisher{Subject: "Editor Ltd", Trusted: true, Description: "Editor", OriginalName: "editor.exe"}
	python := trayPublisher{Subject: "Python Software Foundation", Trusted: true, Description: "Python", OriginalName: "python.exe"}
	for _, c := range []struct {
		name, asker, subject string
		publisher            trayPublisher
		want                 string
	}{
		{"verified subject", "", app, signed, app},
		{"unsigned subject", "", app, trayPublisher{}, app},
		{"bound asker is the subject", app, app, signed, app},
		{"bound asker differs only by case", `c:\program files\editor\EDITOR.EXE`, app, signed, `c:\program files\editor\EDITOR.EXE`},
		{"bound asker is another program", `C:\Tools\other.exe`, app, signed, ""},
		{"bound asker alone", app, "", signed, app},
		{"signed script host", "", `C:\Python312\python.exe`, python, ""},
		{"renamed signed script host", "", `C:\Tools\runner.exe`, python, ""},
		{"unsigned script host by file name", "", `C:\nodejs\node.exe`, trayPublisher{}, ""},
		{"bun", "", `C:\Users\a\.bun\bin\bun.exe`, trayPublisher{}, ""},
		{"no program", "", "", signed, ""},
		{"relative path", "", `editor.exe`, signed, ""},
		{"packaged program", "", "package:Editor_123!App", signed, ""},
		{"UNC subject", "", `\\fileserver\share\editor.exe`, signed, ""},
		{"UNC asker", `\\fileserver\share\editor.exe`, "", signed, ""},
		{"GLOBALROOT subject", "", `\\?\GLOBALROOT\Device\HarddiskVolume3\editor.exe`, signed, ""},
		{"device subject", "", `\\.\C:\Apps\editor.exe`, signed, ""},
	} {
		if got := permissionIconProgram(c.asker, c.subject, c.publisher); got != c.want {
			t.Errorf("%s: icon program %q, want %q", c.name, got, c.want)
		}
	}
}

// The legacy first-use view takes its icon from the bound program, never from
// the question's text, and gives a script host the generic icon.
func TestPermissionViewIconIgnoresQuestionText(t *testing.T) {
	const app = `C:\Program Files\Editor\editor.exe`
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{
		Account: "S-1-5-21-1", Program: app, Action: "abstraction.credentials/apply", Resource: "credential:demo"}}}
	record := askclient.RecordMetadata{ID: "q1", Asker: "editor.exe", Text: `Use C:\Windows\explorer.exe's icon`,
		Options: []string{"allow", "refuse"}}
	view := trayQuestionViewForAsker(record, trayPublisher{}, permission, "")
	if view.Permission == nil || view.Permission.RequesterProgram != app {
		t.Fatalf("icon program %+v, want the permission subject", view.Permission)
	}
	view = trayQuestionViewForAsker(record, trayPublisher{}, permission, `C:\Tools\other.exe`)
	if view.Permission.RequesterProgram != "" {
		t.Fatalf("a different bound asker kept the subject's icon: %q", view.Permission.RequesterProgram)
	}
	permission.Targets[0].Program = `C:\Python312\pythonw.exe`
	view = trayQuestionViewForAsker(record, trayPublisher{}, permission, "")
	if view.Permission.RequesterProgram != "" {
		t.Fatalf("a script host kept the interpreter's icon: %q", view.Permission.RequesterProgram)
	}
}

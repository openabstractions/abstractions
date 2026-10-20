package main

import (
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Script hosts named the macOS and Linux way, or with a version suffix, keep
// the generic icon; ordinary programs are not mistaken for hosts.
func TestIconScriptHostNames(t *testing.T) {
	for program, host := range map[string]bool{
		"/usr/bin/python3": true, "/opt/homebrew/bin/python3.12": true, "/usr/local/bin/node": true,
		"/Users/a/.bun/bin/bun": true, "/bin/bash": true,
		"/Library/Frameworks/Python.framework/Versions/3.12/Resources/Python.app/Contents/MacOS/Python": true,
		`C:\Python312\python3.exe`: true, `C:\nodejs\node.exe`: true,
		"/Applications/OpenCode.app/Contents/MacOS/opencode": false, `C:\Program Files\Editor\editor.exe`: false,
		"/Applications/Calculator.app/Contents/MacOS/Calculator": false, "": false,
	} {
		if got := asks.ScriptHostSubject(rwire.Subject{Program: program}, asks.Publisher{}); got != host {
			t.Errorf("%q: script host=%t want %t", program, got, host)
		}
	}
}

// The Mac bridge marks a permission prompt and carries the selected icon
// program; an ordinary question carries neither, whatever its caller set.
func TestNativeAsksBridgeCarriesPermissionIconAndSound(t *testing.T) {
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	choices := []trayQuestionChoice{{Label: "Refuse", Option: "refuse"}}
	for _, c := range []struct {
		name   string
		prompt trayQuestionPresentation
		want   bool
		icon   string
	}{
		{"permission", trayQuestionPresentation{Choices: choices,
			Permission: &trayPermissionPresentation{RequesterProgram: "/Applications/Editor.app/Contents/MacOS/Editor"}},
			true, "/Applications/Editor.app/Contents/MacOS/Editor"},
		{"permission with generic icon", trayQuestionPresentation{Choices: choices, Permission: &trayPermissionPresentation{}}, true, ""},
		{"ordinary question", trayQuestionPresentation{Choices: choices, PermissionPrompt: true, IconProgram: "/etc/passwd"}, false, ""},
		{"app choice", trayQuestionPresentation{Choices: choices, AppChoice: true,
			Permission: &trayPermissionPresentation{RequesterProgram: "/Applications/Editor.app/Contents/MacOS/Editor"}}, false, ""},
	} {
		done := make(chan error, 1)
		go func() { _, err := bridge.Ask(c.prompt); done <- err }()
		frame := nextNativeFrame(t, frames, "question")
		if frame.Prompt.PermissionPrompt != c.want || frame.Prompt.IconProgram != c.icon {
			t.Errorf("%s: permission=%t icon=%q", c.name, frame.Prompt.PermissionPrompt, frame.Prompt.IconProgram)
		}
		if err := bridge.accept(nativeAsksReply{Kind: "close", ID: frame.ID}, nil); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}

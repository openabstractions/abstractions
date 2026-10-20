package main

import (
	"reflect"
	"testing"
)

func TestQuestionPanelPreservesExplicitRuntimeOwner(t *testing.T) {
	binding := &explicitRuntime{
		Endpoint: `\\.\pipe\isolated-runtime`,
		Program:  `C:\runtime with spaces\openabstractions.exe`,
		StateDir: `C:\state with spaces`,
	}
	command := questionPanelCommand(`C:\panel with spaces\Abstraction Panel.exe`, binding)
	want := []string{command.Path, "-native", "-page", "questions", "-quiet", "-addr", "127.0.0.1:0",
		"-runtime-endpoint", binding.Endpoint, "-runtime-program", binding.Program,
		"-runtime-state-dir", binding.StateDir}
	if !reflect.DeepEqual(command.Args, want) {
		t.Fatalf("Questions window lost its selected runtime or launch arguments: %q", command.Args)
	}
	installed := questionPanelCommand(command.Path, nil)
	if !reflect.DeepEqual(installed.Args, want[:7]) {
		t.Fatalf("installed Questions window inherited an explicit runtime: %q", installed.Args)
	}
	binding.StateDir = ""
	withoutSetup := questionPanelCommand(command.Path, binding)
	if !reflect.DeepEqual(withoutSetup.Args, want[:11]) {
		t.Fatalf("Questions window supplied a state directory that was not selected: %q", withoutSetup.Args)
	}
}

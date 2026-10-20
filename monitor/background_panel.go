package main

import (
	"os"
	"os/exec"
)

// questionPanelCommand opens the normal Panel on Questions. An explicitly
// selected runtime remains the owner of every call made by the new window.
func questionPanelCommand(program string, binding *explicitRuntime) *exec.Cmd {
	// Each requested window owns its listener, including while another Panel
	// window is open. The normal Panel command's default address is unchanged.
	args := []string{"-native", "-page", "questions", "-quiet", "-addr", "127.0.0.1:0"}
	if binding != nil {
		args = append(args, "-runtime-endpoint", binding.Endpoint, "-runtime-program", binding.Program)
		if binding.StateDir != "" {
			args = append(args, "-runtime-state-dir", binding.StateDir)
		}
	}
	return exec.Command(program, args...)
}

// activityPanelCommand opens the normal Panel on Activity at one operation,
// for a notification's Open Activity action (ASK-N1).
func activityPanelCommand(program, operationID string, binding *explicitRuntime) *exec.Cmd {
	command := questionPanelCommand(program, binding)
	for i, arg := range command.Args {
		if arg == "questions" && i > 0 && command.Args[i-1] == "-page" {
			command.Args[i] = "activity"
		}
	}
	command.Args = append(command.Args, "-operation", operationID)
	return command
}

func launchActivityPanel(operationID string) error {
	program, err := os.Executable()
	if err != nil {
		return err
	}
	command := activityPanelCommand(program, operationID, panelExplicit)
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

func launchQuestionPanel() error {
	program, err := os.Executable()
	if err != nil {
		return err
	}
	command := questionPanelCommand(program, panelExplicit)
	if err := command.Start(); err != nil {
		return err
	}
	go command.Wait()
	return nil
}

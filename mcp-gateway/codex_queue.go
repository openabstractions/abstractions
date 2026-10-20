package main

import (
	"context"
	"errors"
	"strings"
	"time"
)

// The installed CLI advertises --resume as a request to resume on the shared
// local daemon. A warning can accompany exit zero, so it proves no wake.
// https://learn.chatgpt.com/docs/cli/reference documents the queue command.
func codexQueueArguments(thread, pointer string) []string {
	return []string{"queue", "--thread", thread, "--message", pointer, "--resume"}
}

// Probe the held image before any queue effect. Do not infer capabilities from
// a version or retry without --resume when the installed CLI lacks it.
func checkCodexQueueResume(ctx context.Context, cli codexReceiverCLI, directory string, run codexSetupRunner) error {
	if err := cli.Check(ctx); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := run(call, cli.Program(), directory, []string{"queue", "--help"})
	if err != nil || out.Code != 0 || len(out.Diagnostic) != 0 {
		return errors.New("Codex queue resume capability unavailable")
	}
	if err := cli.Check(ctx); err != nil {
		return err
	}
	for _, line := range strings.Split(string(out.Output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 0 && fields[0] == "--resume" {
			return nil
		}
	}
	return errors.New("Codex queue resume capability unavailable")
}

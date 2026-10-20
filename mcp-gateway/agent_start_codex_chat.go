package main

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// resolveCodexChatStart queues one turn into the existing Codex thread that a
// retained chat names (owner, 2026-11-09: dormant chats must be reachable).
// Codex Desktop must be running: it stays the thread's single writer and runs
// the turn with the thread's own access. The queued turn starts the thread's
// hook, the thread's own OA helper binds that chat, and the turn reads its
// inbox. With Codex closed the start refuses and the message waits until the
// person opens the chat. This path never creates a thread and never runs one
// headless.
func resolveCodexChatStart(ctx context.Context, p startprofile.Profile, selected agentStartCLI, environment []string, custody agentStartCustody, check func(context.Context) error, closeAll func() error) (*agentStartRecipe, error) {
	fail := func(err error) (*agentStartRecipe, error) { return nil, errors.Join(err, closeAll()) }
	thread, err := custody.cold.Thread()
	if err != nil || !codexQueueThread.MatchString(thread) || !agentRoutingContext.MatchString(custody.cold.Context) {
		return fail(sessionRefusal("cold_selection_invalid"))
	}
	if running, err := codexDesktopRunning(ctx); err != nil || !running {
		return fail(sessionRefusal("codex_closed"))
	}
	if err := codexSubscriptionSignIn(ctx, p, selected, environment); err != nil {
		return fail(err)
	}
	// The thread must still exist: a deleted thread is never queued into.
	listing, cancel := context.WithTimeout(ctx, 10*time.Second)
	threads, err := codexAttachThreads(listing, func(ctx context.Context, input *os.File, out io.Writer) error {
		return runOwnedAgentStartInput(ctx, selected.Program(), p.Folder, []string{"app-server"}, environment, input, out, io.Discard, p.MemoryBytes, nil)
	})
	cancel()
	if err != nil {
		return fail(err)
	}
	exists := false
	for _, t := range threads {
		exists = exists || t.Session == thread
	}
	if !exists {
		return fail(sessionRefusal("session_unknown"))
	}
	inbox := custody.cold.Context
	r := &agentStartRecipe{profile: p.Name, context: p.Context, folder: p.Folder, timeout: time.Duration(p.TimeoutMS) * time.Millisecond, outputBytes: int(p.OutputBytes), memoryBytes: p.MemoryBytes, approved: true, check: check, close: closeAll}
	r.execution = &agentStartExecution{}
	r.capture = submitAgentStartMetadata
	r.run = func(ctx context.Context, _ string, out io.Writer, memory uint64) error {
		if err := check(ctx); err != nil {
			return err
		}
		probe := func(call context.Context, program, directory string, args []string) (codexCLIResult, error) {
			var output, diagnostic claudeSetupOutput
			err := runOwnedAgentStartStreams(call, program, directory, args, environment, &output, &diagnostic, memory, nil)
			return codexCLIResult{Output: output.Bytes(), Diagnostic: diagnostic.Bytes()}, err
		}
		if err := checkCodexQueueResume(ctx, selected, p.Folder, probe); err != nil {
			return sessionRefusal("native_configuration_unavailable")
		}
		if err := check(ctx); err != nil {
			return err
		}
		if running, err := codexDesktopRunning(ctx); err != nil || !running {
			return sessionRefusal("codex_closed")
		}
		capture, ok := out.(*agentStartOutput)
		if !ok {
			return errors.New("capture_unavailable")
		}
		r.delivery = agentDeliveryDesktopQueue
		if err := runOwnedAgentStartStreams(ctx, selected.Program(), p.Folder, codexQueueArguments(thread, agentStartPrompt(inbox)), environment, out, agentStartStderr{capture}, memory, r.execution); err != nil {
			return err
		}
		if !r.execution.ExitKnown || r.execution.ExitCode != 0 || capture.stderrBytes != 0 {
			return sessionRefusal("session_result_unconfirmed")
		}
		return nil
	}
	return r, nil
}

package main

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// resolveCodexChatStart queues one turn into the existing Codex thread that a
// retained chat names (owner, 2026-11-09: dormant chats must be reachable).
// Codex Desktop must be running: it stays the thread's single writer and runs
// the turn with the thread's own access. The queued turn starts the thread's
// hook, the thread's own OA helper binds that chat, and the turn reads its
// inbox with the ticket its pointer carries (architect ruling 1,
// 2026-11-22). With Codex closed the start refuses and the message waits until
// the person opens the chat. This path never creates a thread and never runs
// one headless.
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
		pointer, err := reserveCodexChatWake(ctx, p.StateDir, inbox, thread, custody.wake)
		if err != nil {
			return err
		}
		defer pointer.close()
		r.delivery = agentDeliveryDesktopQueue
		if err := runOwnedAgentStartStreams(ctx, selected.Program(), p.Folder, codexQueueArguments(thread, pointer.text), environment, out, agentStartStderr{capture}, memory, r.execution); err != nil {
			return err
		}
		if !r.execution.ExitKnown || r.execution.ExitCode != 0 || capture.stderrBytes != 0 {
			// The queue's effect is unknown: the pointer keeps the slot for
			// the unknown-queue lease, as the helper's queue does.
			return sessionRefusal("session_result_unconfirmed")
		}
		accept, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		return pointer.state.AcceptQueued(accept, pointer.ticket)
	}
	return r, nil
}

// codexChatPointer is the ticketed pointer the runtime queues into a Codex
// chat's thread and the wake ledger entry that holds it. close releases the
// held state folder.
type codexChatPointer struct {
	state        *codexWakeState
	ticket, text string
	close        func() error
}

// reserveCodexChatWake reserves the thread's wake slot under the message's
// wake key in the wake ledger the chat's own helper uses, and issues the fixed
// pointer: the wake text and its ticket, never the sender or the message
// (architect ruling 1, 2026-11-22). The woken turn reads with that ticket, so
// its helper confirms the wake and the turn is reply-only (S5 ruling,
// 2026-11-22). A slot an earlier pointer holds within its lease refuses
// codex_wake_pending; a wake already queued refuses codex_wake_stale.
func reserveCodexChatWake(ctx context.Context, stateDir, inbox, thread, wake string) (codexChatPointer, error) {
	if wake == "" || !validMessageWake(wake) {
		return codexChatPointer{}, sessionRefusal("wake_key_unavailable")
	}
	root, _, err := setupIdentity(ctx, stateDir, true)
	if err != nil {
		return codexChatPointer{}, err
	}
	fail := func(err error) (codexChatPointer, error) { return codexChatPointer{}, errors.Join(err, root.Close()) }
	program, err := os.Executable()
	if err != nil {
		return fail(err)
	}
	application := dedicatedAgentApplication(program)
	if nativecli.AgentApplicationBase(application) != "oa-codex-integration" {
		application = "oa-codex-integration"
	}
	state, err := newWakeReceiptAt(root, application, inbox, thread, rand.Reader, true)
	if err != nil {
		return fail(err)
	}
	ticket, err := state.Reserve(ctx, wake)
	switch {
	case errors.Is(err, errCodexWakeBusy):
		return fail(sessionRefusal("codex_wake_pending"))
	case err != nil:
		return fail(err)
	case ticket == "":
		return fail(sessionRefusal("codex_wake_stale"))
	}
	text, err := state.Pointer(ctx, ticket, "")
	if err != nil || text != codexQueuedWakePrompt(ticket) {
		undo, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
		defer cancel()
		return fail(errors.Join(err, errors.New("Codex wake pointer unavailable"), state.UndoUnstarted(undo, ticket, wake)))
	}
	return codexChatPointer{state: state, ticket: ticket, text: text, close: root.Close}, nil
}

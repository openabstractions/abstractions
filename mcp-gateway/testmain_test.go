package main

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/personfolders"
)

// TestMain runs this package's tests with the person's per-user folders
// inside a temporary folder (personfolders.Main).
func TestMain(m *testing.M) {
	// No fixture reads the person's own Codex: its loaded threads, locks or
	// session index. Restore tests supply their own threads and folders.
	codexParentThreads = func(string) ([]string, error) { return nil, errors.New("Codex loaded threads are not read in tests") }
	codexReceivingHome = func() (string, error) { return "", errors.New("the person's Codex folder is not read in tests") }
	// No fixture reads the person's own Claude processes; a fixture that
	// needs a running Claude sets OA_CLAUDE_RUNNING_FIXTURE.
	claudeRunning = func(context.Context) (bool, error) { return os.Getenv("OA_CLAUDE_RUNNING_FIXTURE") == "1", nil }
	if journeyGatewayFixtureMain() {
		return
	}
	if codexReceivingCompositionFixtureMain() {
		return
	}
	if agentHelperChaosFixtureMain() {
		return
	}
	if os.Getenv("OA_AGENT_HELPER_PROCESS_FIXTURE") == "1" {
		// The real helper main under a dedicated name; the caller names an
		// isolated runtime endpoint and a temporary state directory. A short
		// admission wait keeps twenty refusals per helper quick.
		agentToolAdmissionWait = 200 * time.Millisecond
		// No runtime answers enrollment reads here; a handover test names
		// the kept path.
		agentEnrollmentOverride = testEnrollmentOverride()
		main()
		return
	}
	if handoverFixtureMain() {
		return
	}
	if os.Getenv("OA_RECEIVER_START_DIRECT_FIXTURE") == "1" {
		receiverStartConfirmation = nil // inert refusal; never discover the installed runtime
		main()
		return
	}
	if agentStartOwnedFixtureMain() {
		return
	}
	if agentStartFixtureMain() {
		return
	}
	if coldClaudeFixtureMain() {
		return
	}
	if codexAttachFixtureMain() {
		return
	}
	os.Exit(personfolders.Main(m))
}

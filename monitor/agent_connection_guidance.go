package main

import (
	"errors"
	"os"
	"strings"
	"sync"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The agent connection pages say in one plain sentence what stops a
// connection and what to do next; the closed stage code and the error stay
// in Technical details (agent-ux report 2026-11-22: Codex showed the code
// "installed_gateway", Claude looped on "Connection setup is unavailable").

// agentHomeIsolated is whether this Panel runs on an isolated test home,
// where connecting agents needs the installed OpenAbstractions. Tests
// replace it.
var agentHomeIsolated = func() bool {
	home, err := setup.CurrentHome()
	return err == nil && home.Isolated
}

// agentSetupBlocker is the sentence for a failure that comes from
// OpenAbstractions' own installation rather than from the agent: an
// isolated home, or the installed connection helper missing. It answers ""
// for every other failure.
func agentSetupBlocker(err error, agent string) string {
	if agentHomeIsolated() {
		return "Connecting " + agent + " needs the installed OpenAbstractions. Open OpenAbstractions from the Start menu, then select Check again."
	}
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return "OpenAbstractions is missing its connection helper. Reinstall OpenAbstractions, then select Check again."
	}
	return ""
}

// codexUnavailableSentence is the Codex page's sentence for a read that
// stopped at stage reason (codexPanelConnections.read).
func codexUnavailableSentence(reason string, err error) string {
	if reason != "unsupported_platform" {
		if sentence := agentSetupBlocker(err, codexConnectionTitle); sentence != "" {
			return sentence
		}
	}
	switch reason {
	case "unsupported_platform":
		return "OpenAbstractions can't connect Codex on this computer yet."
	case "window_closed":
		return "This page was closed. Open Codex again from Apps."
	case "cli_selection":
		return "Codex isn't installed on this computer. Install Codex, then select Check again."
	case "installed_gateway":
		return "OpenAbstractions is missing its connection helper. Reinstall OpenAbstractions, then select Check again."
	case "setup_state":
		return "OpenAbstractions couldn't read its Codex connection folder. Select Check again."
	case "command_virtualized_profile", "command_unknown_profile", "command_profile_folder", "command_sandboxed":
		return "Open OpenAbstractions from the Start menu, then select Check again."
	}
	return "Codex didn't answer when OpenAbstractions read its settings. Select Check again."
}

// agentAttention keeps, by party, the sentence of the last connection
// failure an agent page met, so the Catalog and Apps rows say Needs
// attention instead of a plain Not connected. A read that works clears it.
// It lives as long as the Panel.
var agentAttention sync.Map

// noteAgentAttention records party's failure sentence; "" clears it.
func noteAgentAttention(party, sentence string) {
	if sentence == "" {
		agentAttention.Delete(party)
		return
	}
	agentAttention.Store(party, sentence)
}

// agentAttentionFor is party's recorded failure sentence, or "".
func agentAttentionFor(party string) string {
	if value, ok := agentAttention.Load(party); ok {
		sentence, _ := value.(string)
		return sentence
	}
	return ""
}

// firstSentence is the reason part of a guidance sentence: its text up to
// the first ". ", which drops the next step a page's own button offers.
func firstSentence(text string) string {
	if at := strings.Index(text, ". "); at > 0 {
		return text[:at+1]
	}
	return text
}

// connectFailed records a Connect that failed: the Catalog and Apps rows
// read Needs attention with its reason, and Activity gets one line for the
// attempt (agent-ux 2026-11-22).
func (p *servicePanel) connectFailed(party, title, sentence string) {
	noteAgentAttention(party, sentence)
	p.appActivity.add(applicationActivity{App: title, What: "Connect stopped. " + firstSentence(sentence), Outcome: "Not connected"})
}

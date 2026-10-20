package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// Architect ruling 1 (AB-1s, 2026-11-09): the registered launcher's
// read-only listing discovers the agent's existing conversations, so a
// conversation the person never opened with OA is reachable. The listing
// travels only on the private owned channel; the runtime names each
// conversation by its chat-* context and never shows the vendor ID.

// receiverSession is one existing conversation of the registered agent.
// Origin and Mode are the launch facts its records state, empty when they
// state none.
type receiverSession struct {
	Chat    string `json:"chat"`
	Cwd     string `json:"cwd,omitempty"`
	Title   string `json:"title,omitempty"`
	Updated int64  `json:"updated"`
	Origin  string `json:"origin,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

type receiverSessionsPage struct {
	Sessions []receiverSession `json:"sessions"`
}

// errReceiverSessionsListed ends a resolution that served a listing.
var errReceiverSessionsListed = errors.New("receiver sessions listed")

// listAgentSessions lists the registered agent's conversations through the
// held, verified CLI. Sessions OA holds (its recipe conversation and Claude
// copies) are never listed. Each agent keeps its newest 256.
func listAgentSessions(ctx context.Context, p startprofile.Profile, selected agentStartCLI, environment []string) ([]receiverSession, error) {
	switch p.Name {
	case "claude-v1":
		var auth agentStartOutput
		auth.max = 64 << 10
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		auth.cancel = cancel
		err := runOwnedAgentStart(call, selected.Program(), p.Folder, []string{"auth", "status"}, environment, &auth, p.MemoryBytes)
		cancel()
		var status struct {
			ConfigDirectory string `json:"configDirectory"`
		}
		if err != nil || json.Unmarshal(auth.data, &status) != nil || !filepath.IsAbs(status.ConfigDirectory) {
			return nil, sessionRefusal("native_configuration_unavailable")
		}
		found, err := claudeSessionFiles(status.ConfigDirectory, agentHeldSessions(p.StateDir))
		if err != nil {
			return nil, err
		}
		sessions := make([]receiverSession, 0, len(found))
		for _, s := range found {
			sessions = append(sessions, claudeReceiverSession(s))
		}
		return sessions, nil
	case "codex-v1":
		if err := codexSubscriptionSignIn(ctx, p, selected, environment); err != nil {
			return nil, err
		}
		listing, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		threads, err := codexAttachThreads(listing, func(ctx context.Context, input *os.File, out io.Writer) error {
			return runOwnedAgentStartInput(ctx, selected.Program(), p.Folder, []string{"app-server"}, environment, input, out, io.Discard, p.MemoryBytes, nil)
		})
		if err != nil {
			return nil, err
		}
		sessions := make([]receiverSession, 0, len(threads))
		for _, s := range threads {
			sessions = append(sessions, receiverSession{Chat: s.Session, Cwd: s.cwd, Title: s.Title, Updated: s.Updated})
		}
		return sessions, nil
	}
	return nil, errors.New("supported_budget_profile_unavailable")
}

// claudeReceiverSession states a discovered Claude session's launch facts as
// its records state them: its working folder, where it was started and its
// last permission mode. A session whose records name no known origin keeps
// an empty origin and is never started.
func claudeReceiverSession(s agentAttachSession) receiverSession {
	facts := readClaudeSessionFacts(s.path)
	r := receiverSession{Chat: s.Session, Updated: s.Updated, Title: facts.title, Origin: facts.origin}
	if cwd := facts.cwd; cwd != "" && filepath.IsAbs(cwd) && claudeProjectName(cwd) == filepath.Base(filepath.Dir(s.path)) {
		r.Cwd = filepath.Clean(cwd)
	}
	switch facts.mode {
	case "plan":
		r.Mode = string(wire.ChatModePlan)
	case "acceptEdits":
		r.Mode = string(wire.ChatModeAcceptEdits)
	default:
		r.Mode = string(wire.ChatModeDefault)
	}
	return r
}

// claudeSessionFacts are the facts a discovered session's records state.
type claudeSessionFacts struct{ cwd, origin, mode, title string }

// claudeFactsWindow is half the 64 KiB a listing reads per kept session: the
// first records name the folder and entrypoint, the last ones the newest
// permission mode and title.
const claudeFactsWindow = 32 << 10

// readClaudeSessionFacts reads a session's first and last 32 KiB once each
// and keeps no message text.
func readClaudeSessionFacts(path string) claudeSessionFacts {
	var facts claudeSessionFacts
	file, err := os.Open(path)
	if err != nil {
		return facts
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return facts
	}
	head := make([]byte, min(info.Size(), claudeFactsWindow))
	n, _ := io.ReadFull(file, head)
	head = head[:n]
	offset := max(info.Size()-claudeFactsWindow, int64(n))
	tail, _ := io.ReadAll(io.NewSectionReader(file, offset, info.Size()-offset))
	scan := func(chunk []byte, first bool) {
		for _, line := range bytes.Split(chunk, []byte("\n")) {
			var record struct {
				Type           string `json:"type"`
				Cwd            string `json:"cwd"`
				Entrypoint     string `json:"entrypoint"`
				PermissionMode string `json:"permissionMode"`
				CustomTitle    string `json:"customTitle"`
			}
			if json.Unmarshal(line, &record) != nil {
				continue
			}
			if first && facts.cwd == "" && record.Cwd != "" && len(record.Cwd) <= 32768 && !strings.ContainsFunc(record.Cwd, unicode.IsControl) {
				facts.cwd = record.Cwd
			}
			if first && facts.origin == "" && record.Entrypoint != "" {
				switch record.Entrypoint {
				case "claude-desktop":
					facts.origin = string(wire.ChatOriginDesktop)
				case "cli":
					facts.origin = string(wire.ChatOriginCli)
				default:
					facts.origin = "-"
				}
			}
			if record.PermissionMode != "" {
				facts.mode = record.PermissionMode
			}
			if record.Type == "custom-title" {
				facts.title = hostChatTitle(record.CustomTitle)
			}
		}
	}
	scan(head, true)
	scan(tail, false)
	if facts.origin == "-" {
		facts.origin = ""
	}
	return facts
}

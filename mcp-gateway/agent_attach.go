package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// Owner requirement, 2026-10-31: "tbh opening session already is too much imho
// and we should automatically be able to attach to all sessions." A sender
// addresses one of an agent's existing sessions through an attach context; the
// registered recipe then delivers OA's fixed pointer into that session without
// the person opening it and without a tool call from the session first.

const (
	agentAttachPrefix   = "attach-"
	agentAttachSessions = 256
	// A bound on directory entries read, far above any person's history.
	agentAttachScanned = 1 << 17
)

// One existing vendor session. Folder is the last segment of its working
// folder, for display on this machine only; discovery reads no message text.
type agentAttachSession struct {
	Agent string `json:"agent"`

	Session string `json:"-"`
	Folder  string `json:"folder"`
	Title   string `json:"title,omitempty"`
	Updated int64  `json:"updated"`
	// path is the vendor's session record and cwd the working folder it
	// names; both stay in this process and never reach a listing.
	path, cwd string
}

// claudeAttachSessions lists the newest top-level sessions in the Claude Code
// configuration folder that `claude auth status` reports. It scans every
// project folder before sorting by time, skips links and every session OA
// holds in custody (its own session and its forks), and labels each session
// by the last segment of the `cwd` its first records name. It reads that one
// field, within the first 64 KiB of the kept sessions only, and keeps no message text.
func claudeAttachSessions(configuration string, held map[string]bool) ([]agentAttachSession, error) {
	found, err := claudeSessionFiles(configuration, held)
	for i := range found {
		found[i].Folder = claudeSessionFolder(found[i].path)
	}
	return found, err
}

// claudeSessionFiles finds the newest sessions without reading them.
func claudeSessionFiles(configuration string, held map[string]bool) ([]agentAttachSession, error) {
	if !filepath.IsAbs(configuration) {
		return nil, sessionRefusal("native_configuration_unavailable")
	}
	root := filepath.Join(configuration, "projects")
	projects, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, sessionRefusal("session_discovery_unavailable")
	}
	var found []agentAttachSession
	scanned := 0
	for _, project := range projects {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, project.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if scanned++; scanned > agentAttachScanned {
				return nil, sessionRefusal("session_discovery_unavailable")
			}
			id, ok := strings.CutSuffix(entry.Name(), ".jsonl")
			if !ok || !entry.Type().IsRegular() || !agentSessionUUID.MatchString(id) || held[id] {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			found = append(found, agentAttachSession{Agent: "claude", Session: id, path: filepath.Join(root, project.Name(), entry.Name()), Updated: info.ModTime().UnixMilli()})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Updated != found[j].Updated {
			return found[i].Updated > found[j].Updated
		}
		return found[i].Session < found[j].Session
	})
	if len(found) > agentAttachSessions {
		found = found[:agentAttachSessions]
	}
	return found, nil
}

// claudeSessionCwd returns the `cwd` field of the session's first records,
// bounded to the first 64 KiB and 16 records. It keeps no message text.
func claudeSessionCwd(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	lines := bufio.NewScanner(io.LimitReader(file, 64<<10))
	lines.Buffer(make([]byte, 0, 4096), 64<<10)
	for count := 0; count < 16 && lines.Scan(); count++ {
		var record struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(lines.Bytes(), &record) != nil || record.Cwd == "" {
			continue
		}
		if len(record.Cwd) > 32768 || strings.IndexFunc(record.Cwd, unicode.IsControl) >= 0 {
			return ""
		}
		return record.Cwd
	}
	return ""
}

// claudeSessionFolder keeps the last segment of the session's working folder.
// The encoded project folder name would disclose the whole path.
func claudeSessionFolder(path string) string {
	cwd := claudeSessionCwd(path)
	if cwd == "" {
		return ""
	}
	base := filepath.Base(filepath.Clean(strings.ReplaceAll(cwd, "/", string(filepath.Separator))))
	if base == "." || base == string(filepath.Separator) || len(base) > 128 {
		return ""
	}
	return base
}

// The permission modes Claude Code records for a session. Anything else, or
// none, runs as "default".
var claudePermissionModes = map[string]bool{"default": true, "acceptEdits": true, "plan": true, "dontAsk": true, "bypassPermissions": true}

const claudeModeTail = 256 << 10

// claudeSessionMode returns the permission mode the session last recorded:
// the last `"permissionMode":"<mode>"` within the final 256 KiB of its
// records. A session that recorded none, or a mode this build does not know,
// answers in "default", where a tool the person has not allowed needs an
// approval, and a headless approval fails closed.
func claudeSessionMode(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return "default"
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "default"
	}
	offset := max(info.Size()-claudeModeTail, 0)
	tail, err := io.ReadAll(io.NewSectionReader(file, offset, info.Size()-offset))
	if err != nil {
		return "default"
	}
	const key = `"permissionMode":"`
	at := bytes.LastIndex(tail, []byte(key))
	if at < 0 {
		return "default"
	}
	value := tail[at+len(key):]
	end := bytes.IndexByte(value, '"')
	if end < 0 || !claudePermissionModes[string(value[:end])] {
		return "default"
	}
	return string(value[:end])
}

// claudeProjectName is the project folder name Claude Code keeps a working
// folder's sessions under: every character other than an ASCII letter or digit
// becomes a hyphen.
func claudeProjectName(cwd string) string {
	return strings.Map(func(r rune) rune {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return '-'
	}, cwd)
}

// claudeSessionAccess is the access a woken copy of a session runs with: the
// session's own working folder, where its project settings apply, and the
// permission mode it last recorded (VISION 2026-10-31: never raised).
type claudeSessionAccess struct{ Folder, Mode string }

// claudeAttachAccess reads a discovered session's access. The working folder
// must be absolute and must be the folder Claude Code files the session under,
// so the copy resumes that very session; otherwise it refuses.
func claudeAttachAccess(s agentAttachSession) (claudeSessionAccess, error) {
	cwd := claudeSessionCwd(s.path)
	if cwd == "" || !filepath.IsAbs(cwd) || claudeProjectName(cwd) != filepath.Base(filepath.Dir(s.path)) {
		return claudeSessionAccess{}, sessionRefusal("session_folder_unavailable")
	}
	return claudeSessionAccess{Folder: filepath.Clean(cwd), Mode: claudeSessionMode(s.path)}, nil
}

// claudeAttachRunArguments runs the copy with the session's own permission
// mode and the person's own tools, settings and MCP servers; it adds no
// bypass, accept-edits or tool restriction the session does not have.
// Approvals fail closed headless (`--permission-prompts none`); neither OA
// nor the sender answers them. OA's receiver server, bound to the session's
// attach inbox and pre-allowed, takes the name of the person's own OA
// connection ("OpenAbstractions", the only name setup registers), so it
// replaces that duplicate and nothing else. The copy runs on the start
// recipe's model, never the session's.
func claudeAttachRunArguments(prompt, mode, config, budget, model string) []string {
	return []string{"-p", prompt, "--permission-mode", mode, "--permission-prompts", "none", "--mcp-config", config, "--allowedTools", messagesReceiverAllowedTools(), "--max-budget-usd", budget, "--output-format", "json", "--model", model}
}

// agentHeldSessions is every vendor session OA holds in custody in state:
// OA's own session, every current fork and every fork a later one replaced.
// Those are never offered for attach.
func agentHeldSessions(state string) map[string]bool {
	held := map[string]bool{}
	entries, err := os.ReadDir(state)
	if err != nil {
		return held
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), "claude-session-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := cas.ReadLimitContext(context.Background(), filepath.Join(state, entry.Name()), agentSessionLimit)
		if err != nil {
			continue
		}
		if record, err := decodeAgentSession(raw); err == nil {
			held[record.Session] = true
			for _, old := range record.Superseded {
				held[old] = true
			}
		}
	}
	return held
}

// The first delivery forks the person's session into an OA-held session with
// the same history, so OA never writes into a conversation the person may have
// open. Later deliveries to the same attach context resume that fork.
func agentAttachArguments(lease *agentSessionLease, source string) []string {
	if source == "" || lease.resume {
		return lease.arguments()
	}
	return []string{"--resume", source, "--fork-session", "--session-id", lease.record.Session}
}

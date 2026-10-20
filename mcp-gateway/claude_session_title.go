package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
)

// Claude Code appends the session's title to its transcript as
// {"type":"custom-title"} when the person renames the session and as
// {"type":"ai-title"} when Claude names it, and repeats both lines as the
// session goes on. No hook fires on a rename, so the bound helper rereads the
// transcript's tail and carries a new title to OA's listing.

// claudeTitleRefresh is how often a bound helper rereads its session's title.
const claudeTitleRefresh = 30 * time.Second

// The title lines recur every few transcript lines. The tail is read first;
// a session whose recent lines hold none reads a longer tail once.
var claudeTranscriptTails = []int64{1 << 20, 16 << 20}

// claudeConfigDirectory is Claude Code's configuration folder: the
// CLAUDE_CONFIG_DIR it was started with, else .claude in the home folder.
func claudeConfigDirectory() string {
	config, err := setupio.ClaudeConfigDir()
	if err != nil {
		return ""
	}
	return config
}

// claudeTranscriptPath finds the session's transcript in its project folder.
// The session ID is a UUID, so it selects one file name exactly.
func claudeTranscriptPath(config, session string) string {
	if config == "" || !codexQueueThread.MatchString(session) {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(config, "projects", "*", session+".jsonl"))
	if err != nil || len(matches) != 1 {
		return ""
	}
	return matches[0]
}

// claudeTranscriptTitle returns the session's newest custom title, else its
// newest Claude-made title, normalized by the chat-title owner.
func claudeTranscriptTitle(path, session string) string {
	custom, made, _, _ := claudeTranscriptTitles(path, session, 0)
	if custom != "" {
		return custom
	}
	return made
}

// claudeTranscriptTitles reads the transcript after offset from, its tail
// first, and returns the session's newest custom and Claude-made titles there
// and the offset after the last whole line it read. A tail without a title
// line reads the longer tail. Lines copied from a forked session carry that
// session's ID and are skipped.
func claudeTranscriptTitles(path, session string, from int64) (custom, made string, end int64, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", 0, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < from {
		return "", "", 0, false
	}
	customLine, madeLine := []byte(`{"type":"custom-title"`), []byte(`{"type":"ai-title"`)
	end = from
	for _, tail := range claudeTranscriptTails {
		start := max(info.Size()-tail, from)
		raw := make([]byte, info.Size()-start)
		if _, err := f.ReadAt(raw, start); err != nil && err != io.EOF {
			return "", "", 0, false
		}
		if last := bytes.LastIndexByte(raw, '\n'); last >= 0 {
			end = start + int64(last) + 1
			raw = raw[:last]
		} else {
			raw = nil
		}
		lines := bytes.Split(raw, []byte("\n"))
		if start > from {
			lines = lines[1:]
		}
		for _, line := range lines {
			if !bytes.HasPrefix(line, customLine) && !bytes.HasPrefix(line, madeLine) {
				continue
			}
			var entry struct {
				Type        string `json:"type"`
				CustomTitle string `json:"customTitle"`
				AITitle     string `json:"aiTitle"`
				SessionID   string `json:"sessionId"`
			}
			if json.Unmarshal(line, &entry) != nil || entry.SessionID != session {
				continue
			}
			switch {
			case entry.Type == "custom-title" && hostChatTitle(entry.CustomTitle) != "":
				custom = hostChatTitle(entry.CustomTitle)
			case entry.Type == "ai-title" && hostChatTitle(entry.AITitle) != "":
				made = hostChatTitle(entry.AITitle)
			}
		}
		if custom != "" || made != "" || start == from {
			break
		}
	}
	return custom, made, end, true
}

// followClaudeSessionTitle sets the chat's title from the transcript and
// again each time the transcript's title changes. Each read starts where the
// last one ended, so a transcript without title lines is read in full once
// and afterwards only as it grows. A custom title stays ahead of a later
// Claude-made one.
func followClaudeSessionTitle(ctx context.Context, status *messagesInboxStatus, config, session string, refresh time.Duration, after func(time.Duration) <-chan time.Time) {
	shown, path, custom, made := "", "", "", ""
	var from int64
	var seen [2]int64
	for {
		if path == "" {
			path = claudeTranscriptPath(config, session)
		}
		if path != "" {
			if info, err := os.Stat(path); err != nil {
				path, seen = "", [2]int64{}
			} else if stamp := [2]int64{info.Size(), info.ModTime().UnixNano()}; stamp != seen {
				seen = stamp
				if info.Size() < from {
					// A rewritten transcript is read again from its start.
					from, custom, made = 0, "", ""
				}
				if c, m, end, ok := claudeTranscriptTitles(path, session, from); ok {
					from = end
					if c != "" {
						custom = c
					}
					if m != "" {
						made = m
					}
				}
				title := custom
				if title == "" {
					title = made
				}
				if title != "" && title != shown {
					status.setChatTitle(title)
					shown = title
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-after(refresh):
		}
	}
}

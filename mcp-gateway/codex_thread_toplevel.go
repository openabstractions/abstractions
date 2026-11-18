package main

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Architect ruling 3, 2026-11-22 (decisions/messaging-wake-0-8-1-proposed-rulings.md):
// only a thread proven top-level binds receiving. A thread is proven when the
// helper's parent app-server holds that thread's writer lock and no other
// (the restore rule), or when Codex's own session record names it top-level.
// A thread that fails both leaves the connection unbound, and the main chat
// binds on its next call.
//
// Codex writes one session record per thread,
// CODEX_HOME/sessions/YYYY/MM/DD/rollout-<time>-<thread>.jsonl. Its first line
// is {"type":"session_meta","payload":{...}}. For a worker thread the payload
// names its parent: "source" is {"subagent":{"thread_spawn":{
// "parent_thread_id":...}}}, "thread_source" is "subagent" and
// "parent_thread_id" is set (read on the owner's machine, 2026-11-22, Codex
// 0.171.0-alpha.13). A top-level thread's "source" is a plain string such as
// "vscode" or "cli", with no parent.

// codexThreadTopLevel reports a thread proven top-level. Tests replace it.
var codexThreadTopLevel = codexProvenTopLevel

func codexProvenTopLevel(home, thread string) bool {
	if !filepath.IsAbs(home) || !codexQueueThread.MatchString(thread) {
		return false
	}
	if threads, err := codexParentThreads(home); err == nil && len(threads) == 1 && threads[0] == thread {
		return true
	}
	return codexSessionRecordTopLevel(home, thread)
}

// codexSessionMetaBytes bounds the first line read; it carries the thread's
// base instructions.
const codexSessionMetaBytes = 1 << 20

// codexSessionDayFiles bounds one day folder's listing.
const codexSessionDayFiles = 4096

// codexSessionRecordTopLevel reads, read-only, the first line of the thread's
// session record. A missing, unreadable or unfamiliar record proves nothing.
func codexSessionRecordTopLevel(home, thread string) bool {
	path := codexSessionRecordPath(home, thread)
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(io.LimitReader(f, codexSessionMetaBytes), 64<<10).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return false
	}
	return codexSessionMetaTopLevel(line, thread)
}

// codexSessionMetaTopLevel decides from one session_meta line.
func codexSessionMetaTopLevel(line []byte, thread string) bool {
	var record struct {
		Type    string `json:"type"`
		Payload struct {
			ID           string          `json:"id"`
			Source       json.RawMessage `json:"source"`
			Parent       *string         `json:"parent_thread_id"`
			ThreadSource string          `json:"thread_source"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil || record.Type != "session_meta" || record.Payload.ID != thread {
		return false
	}
	var source string
	if json.Unmarshal(record.Payload.Source, &source) != nil || source == "" || strings.EqualFold(source, "subagent") {
		return false
	}
	return (record.Payload.Parent == nil || *record.Payload.Parent == "") && record.Payload.ThreadSource != "subagent"
}

// codexSessionRecordPath finds the thread's one record in the day folders
// around the time its UUIDv7 ID names. Other ID forms, and a thread with more
// than one record, find none.
func codexSessionRecordPath(home, thread string) string {
	if !codexQueueThread.MatchString(thread) || thread[14] != '7' {
		return ""
	}
	raw, err := hex.DecodeString(thread[0:8] + thread[9:13])
	if err != nil {
		return ""
	}
	var ms int64
	for _, b := range raw {
		ms = ms<<8 | int64(b)
	}
	at := time.UnixMilli(ms)
	suffix := "-" + thread + ".jsonl"
	// More than one record for the thread proves nothing.
	found := ""
	seen := map[string]bool{}
	for _, day := range []time.Time{at.Local(), at.UTC(), at.Add(-24 * time.Hour).Local(), at.Add(24 * time.Hour).Local()} {
		dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
		if seen[dir] {
			continue
		}
		seen[dir] = true
		folder, err := os.Open(dir)
		if err != nil {
			continue
		}
		names, err := folder.Readdirnames(codexSessionDayFiles)
		folder.Close() //unchecked: a read-only directory handle; its names are already read
		// A day folder read only in part cannot show the thread's record is
		// the only one. io.EOF is an empty folder.
		if err != nil && err != io.EOF {
			return ""
		}
		for _, name := range names {
			if strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, suffix) {
				if found != "" {
					return ""
				}
				found = filepath.Join(dir, name)
			}
		}
	}
	return found
}

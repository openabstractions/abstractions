package main

import (
	"context"
	"strings"
	"unicode/utf8"
)

// Owner requirement, 2026-10-31: "tbh opening session already is too much imho
// and we should automatically be able to attach to all sessions." An attach
// context names one of an agent's existing sessions. The registered launcher
// derives it and delivers into that session; the runtime only routes it.

const (
	messageAttachPrefix = "attach-"
	messageAttachLimit  = 256 // per agent; InboxesList pages them 64 at a time
)

// One existing conversation of a registered agent, as its launcher reports
// it. Context is its chat-* name. Chat (the vendor ID), Cwd, Origin and Mode
// stay in this process and never reach a listing row.
type applicationAttachSession struct {
	Context string
	Folder  string
	Updated int64
	Title   string
	Chat    string
	Cwd     string
	Origin  string
	Mode    string
	// Project and CwdKey are the folder's project and identity, read in the
	// bounded refresh so that a listing or Send reads no file.
	Project string
	CwdKey  string
}

func messageAttachContext(name string) bool {
	return strings.HasPrefix(name, messageAttachPrefix) && applicationContextName.MatchString(name)
}

// messageAttachSessionsFor returns at most messageAttachLimit well-formed
// sessions, newest first as the launcher reported them.
func (d *applicationDirectory) messageAttachSessionsFor(ctx context.Context, application string) []applicationAttachSession {
	if d.messageAttachSessions == nil {
		return nil
	}
	sessions, err := d.messageAttachSessions(ctx, application)
	if err != nil {
		return nil
	}
	return d.messageAttachValid(sessions)
}

// messageAttachValid keeps at most messageAttachLimit well-formed sessions.
func (d *applicationDirectory) messageAttachValid(sessions []applicationAttachSession) []applicationAttachSession {
	var kept []applicationAttachSession
	for _, s := range sessions {
		if len(kept) == messageAttachLimit {
			break
		}
		if messageCanonicalChatContext(s.Context) && applicationContextName.MatchString(s.Context) && utf8.ValidString(s.Folder) && s.Updated > 0 {
			kept = append(kept, s)
		}
	}
	return kept
}

// messageContextOpenLocked reports a live inbox holding this exact context.
// An attach start waits for that session's own inbox to close, while other
// sessions of the same agent may stay open.
func (d *applicationDirectory) messageContextOpenLocked(application, contextName string) bool {
	d.expireLocked()
	return d.messageContextLiveLocked(application, contextName)
}

func (d *applicationDirectory) messageContextLiveLocked(application, contextName string) bool {
	if d.nativeInboxes == nil || d.nativeInboxes.closed {
		return false
	}
	row := d.nativeInboxes.rows[application+"\x00"+contextName]
	if row == nil {
		return false
	}
	return row.application == application && d.nativeInboxLiveLocked(row, d.now())
}

func (d *applicationDirectory) messageContextOpen(ctx context.Context, application, contextName string) (bool, error) {
	if !d.lockRead(ctx) {
		return false, ctx.Err()
	}
	defer d.mu.RUnlock()
	return d.messageContextLiveLocked(application, contextName), nil
}

// inbox is the context the started receiver opens: the attach target when
// set, otherwise the recipe's own context.
func (r applicationMessageStartRequest) inbox() string {
	if r.Target != "" {
		return r.Target
	}
	return r.Context
}

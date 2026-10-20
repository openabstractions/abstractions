package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// Architect ruling 1 (AB-1s, 2026-11-09): every existing conversation of an
// agent is reachable, also one the person never opened with OA. The agent's
// registered launcher lists its conversations; each is named by the chat-*
// context the bind path derives for it (messageStore.chatContextName), so an
// open inbox, a retained chat and a discovered conversation of one thread are
// one row. Listing writes nothing; a question to a discovered conversation
// promotes it to a retained chat, whose existing start path then runs.

const (
	messageDiscoveryFresh   = 60 * time.Second
	messageDiscoveryWait    = 2 * time.Second
	messageDiscoveryStale   = 10 * time.Minute
	messageDiscoveryRefresh = 15 * time.Second
	// messageDiscoveryForceGap is the least time between two refreshes a Send
	// to an unknown conversation may force for one agent.
	messageDiscoveryForceGap = 10 * time.Second
)

// messageDiscovery keeps one snapshot of each agent's conversations with at
// most one refresh in flight per agent.
type messageDiscovery struct {
	mu    sync.Mutex
	apps  map[string]*messageDiscoverySnapshot
	fetch func(context.Context, string) ([]applicationAttachSession, error)
	now   func() time.Time
}

type messageDiscoverySnapshot struct {
	at, tried time.Time
	sessions  []applicationAttachSession
	done      chan struct{}
}

func newMessageDiscovery(fetch func(context.Context, string) ([]applicationAttachSession, error), now func() time.Time) *messageDiscovery {
	if now == nil {
		now = time.Now
	}
	return &messageDiscovery{apps: map[string]*messageDiscoverySnapshot{}, fetch: fetch, now: now}
}

// start begins a refresh of the agent's snapshot when it is older than 60
// seconds, or when forced and the last attempt is at least 10 seconds old,
// and returns the channel that closes when the refresh in flight ends.
func (m *messageDiscovery) start(application string, force bool) <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.apps[application]
	if snap == nil {
		snap = &messageDiscoverySnapshot{}
		m.apps[application] = snap
	}
	if snap.done != nil {
		return snap.done
	}
	since := m.now().Sub(snap.tried)
	if !snap.tried.IsZero() && (since < messageDiscoveryFresh && !force || since < messageDiscoveryForceGap) {
		return nil
	}
	done := make(chan struct{})
	snap.done, snap.tried = done, m.now()
	go m.refresh(application, snap, done)
	return done
}

// current answers from the snapshot while it is at most 10 minutes old.
func (m *messageDiscovery) current(application string) []applicationAttachSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := m.apps[application]
	if snap == nil || snap.at.IsZero() || m.now().Sub(snap.at) > messageDiscoveryStale {
		return nil
	}
	return snap.sessions
}

// wait waits for every given refresh until one shared 2-second deadline,
// and reports whether every refresh ended in time.
func messageDiscoveryAwait(ctx context.Context, pending []<-chan struct{}) bool {
	timer := time.NewTimer(messageDiscoveryWait)
	defer timer.Stop()
	for _, done := range pending {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-timer.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
	return true
}

// sessions answers one agent's snapshot, refreshing it as start allows.
func (m *messageDiscovery) sessions(ctx context.Context, application string, force bool) []applicationAttachSession {
	messageDiscoveryAwait(ctx, []<-chan struct{}{m.start(application, force)})
	return m.current(application)
}

// warm starts every agent's refresh first and then waits once, so a listing
// over several agents waits at most 2 seconds in all. It reports whether
// every refresh ended within that wait.
func (m *messageDiscovery) warm(ctx context.Context, applications []string) bool {
	pending := make([]<-chan struct{}, 0, len(applications))
	for _, application := range applications {
		pending = append(pending, m.start(application, false))
	}
	return messageDiscoveryAwait(ctx, pending)
}

func (m *messageDiscovery) refresh(application string, snap *messageDiscoverySnapshot, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), messageDiscoveryRefresh)
	defer cancel()
	sessions, err := m.fetch(ctx, application)
	m.mu.Lock()
	if err == nil {
		snap.sessions, snap.at = sessions, m.now()
	}
	snap.done = nil
	m.mu.Unlock()
	close(done)
}

// fetchAgentSessions runs the agent's registered launcher for its read-only
// listing and names each conversation by its chat-* context. While the
// person's Agent starts setting is off for the agent, no launcher runs.
func (d *applicationDirectory) fetchAgentSessions(name func(program, chat string) string, enabled func(context.Context, string) bool) func(context.Context, string) ([]applicationAttachSession, error) {
	return func(ctx context.Context, application string) ([]applicationAttachSession, error) {
		b := d.messageStartOwned
		if b == nil || name == nil {
			return nil, errors.New("messages: discovery unavailable")
		}
		if enabled != nil && !enabled(ctx, application) {
			return nil, nil
		}
		target, err := d.prepareMessageStart(ctx, application)
		if err != nil {
			return nil, err
		}
		raw, err := b.list(ctx, target)
		if err != nil {
			return nil, err
		}
		return discoveredSessions(raw, func(chat string) string { return name(target.Program, chat) })
	}
}

// messageProjectOf is the project function discovery uses; tests observe it.
var messageProjectOf = messageProject

// discoveredSessions decodes the launcher's listing page. The folder facts
// that need the filesystem (project and folder identity) are read here, in
// the bounded refresh, never on a listing or Send; a folder that is not a
// local path keeps none of them.
func discoveredSessions(raw []byte, name func(string) string) ([]applicationAttachSession, error) {
	var page struct {
		Sessions []struct {
			Chat    string `json:"chat"`
			Cwd     string `json:"cwd"`
			Title   string `json:"title"`
			Updated int64  `json:"updated"`
			Origin  string `json:"origin"`
			Mode    string `json:"mode"`
		} `json:"sessions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 4<<20 || decoder.Decode(&page) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("messages: discovery page invalid")
	}
	var out []applicationAttachSession
	for _, s := range page.Sessions {
		if len(out) == messageAttachLimit {
			break
		}
		if s.Updated <= 0 || !validChatID(s.Chat) {
			continue
		}
		session := applicationAttachSession{Context: name(s.Chat), Updated: s.Updated, Chat: s.Chat, Origin: s.Origin, Mode: s.Mode}
		session.Title, _ = client.NormalizeChatTitle(s.Title)
		if filepath.IsAbs(s.Cwd) && utf8.ValidString(s.Cwd) && len(s.Cwd) <= 4096 {
			cwd := filepath.Clean(s.Cwd)
			session.Folder = filepath.Base(cwd)
			if identity.LocalPath(cwd) == nil {
				session.Cwd = cwd
				session.Project = messageProjectOf(cwd)
				if info, err := os.Stat(cwd); err == nil && info.IsDir() {
					session.CwdKey, _ = identity.FileKey(cwd)
				}
			}
		}
		out = append(out, session)
	}
	return out, nil
}

func validChatID(chat string) bool {
	if chat == "" || len(chat) > 128 {
		return false
	}
	_, err := msg.DecodeChatBinding(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: chat}))
	return err == nil
}

// discoveredChatRecord states a discovered conversation as the retained
// binding a bind would write. A Claude session gets launch facts only when
// its records state its origin and its folder's identity was read; without
// them it never starts.
func discoveredChatRecord(s applicationAttachSession) messageChatBindingRecord {
	row := messageChatBindingRecord{Context: s.Context, Chat: s.Chat, Title: s.Title}
	switch msg.ChatOrigin(s.Origin) {
	case msg.ChatOriginDesktop, msg.ChatOriginCli:
		if s.Cwd != "" && s.CwdKey != "" {
			row.Launch = &msg.ChatLaunch{Session: s.Chat, Origin: msg.ChatOrigin(s.Origin), Cwd: s.Cwd, Mode: msg.ChatMode(s.Mode)}
			row.CwdKey = s.CwdKey
		}
	}
	return row
}

// discoveredChatSelection is the start selection of a discovered
// conversation from its stored facts; it reads no file.
func discoveredChatSelection(row messageChatBindingRecord) *startprofile.ColdResume {
	c := &startprofile.ColdResume{Context: row.Context, Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: row.Chat, Launch: row.Launch})), CwdKey: row.CwdKey, Revision: messageColdRevision(row)}
	if c.Valid() != nil {
		return nil
	}
	return c
}

// discoveredChatInbox lists a discovered conversation as a chat that is not
// open: a question starts it when its recipe can wake it now.
func discoveredChatInbox(application string, s applicationAttachSession, profile applicationMessageStartRequest, profileErr error, running messageAppsRunning) msg.Inbox {
	title := s.Title
	if title == "" {
		title = s.Folder
	}
	row := discoveredChatRecord(s)
	seen := s.Updated - s.Updated%60000
	status := "Not open"
	if s.Folder != "" {
		status = "Not open in " + s.Folder
	}
	inbox := msg.Inbox{Program: "app:" + application, Context: s.Context, ChatTitle: title, Status: nativeMessageHeader(status), LastSeenUnixMs: seen, Revision: "chat." + strconv.FormatInt(seen, 10), Reach: new(msg.InboxReachQueued)}
	if profileErr != nil || profile.Descriptor.Activation == nil {
		return inbox
	}
	p, err := startprofile.Parse(profile.Descriptor.Activation.Arguments)
	if err != nil {
		return inbox
	}
	if cold := discoveredChatSelection(row); cold != nil {
		if _, startable := messageChatStart(p.Name, cold, running); startable {
			inbox.Reach = new(msg.InboxReachStartsOnQuestion)
		}
	}
	return inbox
}

// messageProject names the project holding a folder: the main working folder
// of its Git repository (a linked worktree resolves through its .git file and
// commondir), or the folder itself outside Git. Projects compare by file
// identity; an unreadable folder has none.
func messageProject(folder string) string {
	if !messageLocalFolder(folder) {
		return ""
	}
	dir := filepath.Clean(folder)
	for range 64 {
		git := filepath.Join(dir, ".git")
		if info, err := os.Lstat(git); err == nil {
			if info.Mode().IsRegular() {
				main, ok := messageWorktreeMain(dir, git)
				if !ok {
					// A .git file naming a share or device path gives no project.
					return ""
				}
				if main != "" {
					dir = main
				}
			}
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			dir = filepath.Clean(folder)
			break
		}
		dir = parent
	}
	if !messageLocalFolder(dir) {
		return ""
	}
	key, err := identity.FileKey(dir)
	if err != nil {
		return ""
	}
	return key
}

// messageLocalFolder admits an absolute local path only: never a share, a
// \\?\ or \\.\ device form, or another non-local path, which opening would
// reach over the network with the account's credentials.
func messageLocalFolder(path string) bool {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) {
		return false
	}
	return identity.LocalPath(path) == nil
}

// messageProjectOpen opens a file discovery reads; tests observe it.
var messageProjectOpen = os.Open

// messageWorktreeMain resolves a linked worktree's main working folder: its
// .git file names the worktree's git folder, whose commondir names the shared
// repository folder inside the main working folder. ok is false when either
// names a path that is not local; nothing is opened there.
func messageWorktreeMain(dir, gitFile string) (string, bool) {
	read := func(path string) string {
		file, err := messageProjectOpen(path)
		if err != nil {
			return ""
		}
		defer file.Close()
		raw, _ := io.ReadAll(io.LimitReader(file, 4096))
		return strings.TrimSpace(string(raw))
	}
	gitDir, found := strings.CutPrefix(read(gitFile), "gitdir:")
	if !found {
		return "", true
	}
	gitDir = strings.TrimSpace(gitDir)
	if !filepath.IsAbs(gitDir) && !strings.HasPrefix(gitDir, `\\`) && !strings.HasPrefix(gitDir, `//`) {
		gitDir = filepath.Join(dir, filepath.FromSlash(gitDir))
	}
	if !messageLocalFolder(gitDir) {
		return "", false
	}
	gitDir = filepath.Clean(gitDir)
	common := read(filepath.Join(gitDir, "commondir"))
	if common == "" {
		return "", true
	}
	if !filepath.IsAbs(common) && !strings.HasPrefix(common, `\\`) && !strings.HasPrefix(common, `//`) {
		common = filepath.Join(gitDir, filepath.FromSlash(common))
	}
	if !messageLocalFolder(common) {
		return "", false
	}
	common = filepath.Clean(common)
	main := filepath.Dir(common)
	ctx := context.Background()
	options := fileid.Options{Links: fileid.FollowLocalLinks}
	commonID, err := fileid.Resolve(ctx, fileid.Self, common, options)
	if err != nil || !commonID.IsDir() {
		return "", true
	}
	gitID, err := fileid.Resolve(ctx, fileid.Self, filepath.Join(main, ".git"), options)
	if err != nil || !commonID.Equal(gitID) {
		return "", true
	}
	return main, true
}

// messageSenderProject is the project of the sender's own conversation, as
// its own app's discovery stored it. Unknown is empty, and the listing then
// carries every discovered conversation.
func (h *applicationsHost) messageSenderProject(ctx context.Context, actor messageActor, owned ...map[string]bool) string {
	if h.directory == nil {
		return ""
	}
	return h.messageSenderProjectIn(actor, func(application string) []applicationAttachSession {
		return h.directory.messageAttachSessionsFor(ctx, application)
	}, owned...)
}

// messageSenderProjectIn reads the sender's own discovered row through the
// caller's discovery read.
func (h *applicationsHost) messageSenderProjectIn(actor messageActor, sessions func(string) []applicationAttachSession, owned ...map[string]bool) string {
	isOwn := func(contextName string) bool {
		if contextName == actor.returnContext() {
			return true
		}
		for _, m := range owned {
			if m[contextName] {
				return true
			}
		}
		return false
	}
	if actor.Application != "" {
		for _, s := range sessions(actor.Application) {
			if isOwn(s.Context) {
				return s.Project
			}
		}
	}
	return ""
}

// messageChatKnown resolves a chat-* context a sender addresses: an open
// inbox or a retained chat is known; otherwise it must be a discovered
// conversation in the sender's project, found after at most one refresh.
func (h *applicationsHost) messageChatKnown(ctx context.Context, actor messageActor, application, contextName string, discover bool) (bool, *applicationAttachSession, error) {
	if open, err := h.directory.messageContextOpen(ctx, application, contextName); err != nil || open {
		return open, nil, err
	}
	var receiver applicationAgentImage
	if view := h.directory.currentAgentEnrollmentImage(ctx, application, &receiver); view.Enrollment == nil || receiver.Program == "" {
		return false, nil, nil
	}
	rows, err := h.messages.retainedChats(ctx, h.directory.owner, receiver.Program)
	if err != nil {
		return false, nil, err
	}
	for _, row := range rows {
		if row.Context == contextName {
			return true, nil, nil
		}
	}
	if !discover {
		return false, nil, nil
	}
	// The sender's and the receiver's refreshes start together under one
	// 2-second wait, as a listing's do. A forced refresh runs only when that
	// wait ended in time: a Send waits about 2 seconds at most.
	d := h.directory
	var project string
	var sessions []applicationAttachSession
	settled := true
	if d.messageAttachWarm != nil && d.messageAttachCurrent != nil {
		applications := []string{application}
		if actor.Application != "" && actor.Application != application {
			applications = append(applications, actor.Application)
		}
		settled = d.messageAttachWarm(ctx, applications)
		current := func(application string) []applicationAttachSession {
			return d.messageAttachValid(d.messageAttachCurrent(application))
		}
		project = h.messageSenderProjectIn(actor, current)
		sessions = current(application)
	} else {
		project = h.messageSenderProject(ctx, actor)
		sessions = d.messageAttachSessionsFor(ctx, application)
	}
	find := func(sessions []applicationAttachSession) *applicationAttachSession {
		for _, s := range sessions {
			if s.Context == contextName && (project == "" || s.Project == project) {
				found := s
				return &found
			}
		}
		return nil
	}
	if found := find(sessions); found != nil {
		return false, found, nil
	}
	if settled && d.messageAttachRefresh != nil {
		return false, find(d.messageAttachRefresh(ctx, application)), nil
	}
	return false, nil, nil
}

// promoteDiscoveredChat writes the retained binding a bind would write for a
// discovered conversation, from its discovered facts. Its name stays the one
// the listing showed.
func (h *applicationsHost) promoteDiscoveredChat(ctx context.Context, program string, s applicationAttachSession) error {
	row := discoveredChatRecord(s)
	claim := &msg.ChatBinding{ChatID: s.Chat, ChatTitle: s.Title, Launch: row.Launch}
	name, err := h.messages.contextForChat(ctx, h.directory.owner, program, s.Chat, claim)
	if err != nil {
		return err
	}
	if name != s.Context {
		return errors.New("messages: discovered chat name changed")
	}
	return nil
}

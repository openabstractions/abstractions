package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// Architect ruling 2026-11-12 (messages CONTRACT, "Program-only routing and
// chat disclosure"): a local listing shows the caller's own rows and the chats
// it has a thread with; other chats are counted per program. A thread is any
// accepted message between two chats, in either direction. Each chat keeps a
// small index of the chats it exchanged messages with, newest first. The index
// is display data: it selects no receiver and grants nothing.

const (
	messageThreadPeers = 64
	messageThreadLimit = 64 << 10
)

// messageChat names one chat as a listing row does: its registered program
// (app:<name>) and its context.
type messageChat struct {
	Program, Context string
}

type messageThreadPeer struct {
	Program, Context string
	At               int64
}

type messageThreadIndex struct {
	Profile string
	Peers   []messageThreadPeer
}

func (s *messageStore) threadPath(account string, chat messageChat) string {
	return filepath.Join(s.dir, "threads", messageDigest(account+"\x00"+chat.Program+"\x00"+chat.Context)+".json")
}

func readMessageThreads(raw []byte) messageThreadIndex {
	index := messageThreadIndex{Profile: "messages-threads@1"}
	if raw == nil {
		return index
	}
	var stored messageThreadIndex
	if messageStoredJSON(raw, &stored) != nil || stored.Profile != index.Profile || len(stored.Peers) > messageThreadPeers {
		// A damaged index restarts empty; it only decides which titles list.
		return index
	}
	return stored
}

// noteThread records that two chats exchanged a message at the given time.
func (s *messageStore) noteThread(ctx context.Context, account string, a, b messageChat, at int64) error {
	if a.Program == "" || a.Context == "" || b.Program == "" || b.Context == "" || a == b {
		return nil
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "threads"), 0700); err != nil {
		return err
	}
	var errs []error
	for _, side := range [][2]messageChat{{a, b}, {b, a}} {
		own, peer := side[0], side[1]
		errs = append(errs, cas.ChangeLimitContext(ctx, s.threadPath(account, own), messageThreadLimit, func(raw []byte) ([]byte, error) {
			index := readMessageThreads(raw)
			kept := []messageThreadPeer{{Program: peer.Program, Context: peer.Context, At: at}}
			for _, row := range index.Peers {
				if row.Program == peer.Program && row.Context == peer.Context {
					if row.At > at {
						kept[0].At = row.At
					}
					continue
				}
				kept = append(kept, row)
			}
			sort.SliceStable(kept, func(i, j int) bool { return kept[i].At > kept[j].At })
			if len(kept) > messageThreadPeers {
				kept = kept[:messageThreadPeers]
			}
			index.Peers = kept
			return json.Marshal(index)
		}))
	}
	return errors.Join(errs...)
}

// threads returns the chats one chat exchanged messages with, newest first.
func (s *messageStore) threads(ctx context.Context, account string, chat messageChat) ([]messageThreadPeer, error) {
	raw, err := cas.ReadLimitContext(ctx, s.threadPath(account, chat), messageThreadLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return readMessageThreads(raw).Peers, nil
}

// disclosedInboxes is the local listing an agent reads: its own rows and the
// chats it has a thread with keep their rows and titles. Every other chat on
// the page is counted per program in other_chats, with no title or context.
// Prepare and Send read the whole listing directly; this projection only
// limits what the listing discloses.
func (r *messagesReceiver) disclosedInboxes(page msg.InboxPage) msg.InboxPage {
	if page.Outcome != msg.OutcomePage {
		return page
	}
	threads := map[messageChat]bool{}
	if r.host != nil && r.host.messages != nil && r.host.directory != nil {
		for _, row := range page.Inboxes {
			if row.Mine == nil || !*row.Mine {
				continue
			}
			peers, err := r.host.messages.threads(r.ctx, r.host.directory.owner, messageChat{Program: row.Program, Context: row.Context})
			if err != nil {
				r.reportMessageError("thread index read", err)
			}
			for _, peer := range peers {
				threads[messageChat{Program: peer.Program, Context: peer.Context}] = true
			}
		}
	}
	kept := []msg.Inbox{}
	counts := map[string]int32{}
	var programs []string
	for _, row := range page.Inboxes {
		if row.Mine != nil && *row.Mine || threads[messageChat{Program: row.Program, Context: row.Context}] {
			kept = append(kept, row)
			continue
		}
		if counts[row.Program] == 0 {
			programs = append(programs, row.Program)
		}
		counts[row.Program]++
	}
	page.Inboxes = kept
	page.OtherChats = nil
	for _, program := range programs {
		page.OtherChats = append(page.OtherChats, msg.ProgramChats{Program: program, Chats: counts[program]})
	}
	return page
}

// messageChatTitle reuses the list's current disclosure checks and title
// formatting for the accepted application and its same-app siblings.
func (r *messagesReceiver) messageChatTitle(program, contextName string) string {
	application, ok := strings.CutPrefix(program, "app:")
	if !ok || !providerName.MatchString(application) {
		return ""
	}
	observer, _ := r.messageObserver("")
	if observer == nil {
		return ""
	}
	defer observer.pin.Close()
	observer.titleApplication = application
	for _, discover := range []bool{false, true} {
		observer.noDiscovery = !discover
		rows, _, err := observer.inboxSnapshotWithDiscovery(new([]msg.AgentApplication), nil)
		if err != nil {
			return ""
		}
		for _, row := range rows {
			if row.Program == program && row.Context == contextName {
				return row.ChatTitle
			}
		}
	}
	return ""
}

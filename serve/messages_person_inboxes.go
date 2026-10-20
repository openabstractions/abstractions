package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Metadata only, under the same native operator fence as actual message
// inspection. Ordinary application enrollment supplies none of this exception.
func (r *messagesReceiver) InboxesForPerson(cursor string) (msg.InboxPage, error) {
	if len(cursor) > 256 {
		return messageEmptyInboxes(msg.OutcomeInvalid), nil
	}
	check, release, outcome := r.personOperator()
	if outcome != msg.OutcomePage {
		return messageEmptyInboxes(outcome), nil
	}
	defer release()
	pin, err := r.call.RetainProcess()
	if err != nil {
		return messageEmptyInboxes(msg.OutcomeForbidden), nil
	}
	defer pin.Close()
	_, caller, _, process, err := nativeInboxPeer(pin, r.host.directory.now())
	if err != nil {
		return messageEmptyInboxes(msg.OutcomeForbidden), nil
	}
	binding := fmt.Sprintf("%s\x00%s\x00%d\x00%s", caller.Account, caller.Program, process.PID, process.StartTime)
	snapshot := func() ([]msg.Inbox, string, error) {
		if check() != msg.OutcomePage {
			return nil, "", errors.New("messages: operator admission ended")
		}
		d := r.host.directory
		if !d.lock(r.ctx) {
			return nil, "", r.ctx.Err()
		}
		d.expireLocked()
		var rows []nativeInbox
		var err error
		if d.nativeInboxesClosed {
			d.unlock()
			return nil, "", errors.New("messages: inbox owner closed")
		}
		if book := d.nativeInboxes; book != nil {
			rows, err = book.snapshotLocked("")
			if err == nil {
				rows = d.receivingNativeSnapshotsLocked(rows)
			}
		}
		epoch := d.epoch
		d.unlock()
		if err != nil {
			return nil, "", err
		}
		defer closeNativeInboxSnapshots(rows)
		visible := []msg.Inbox{}
		starts := map[string]messageInboxStart{}
		for _, row := range rows {
			if row.chatID == "" || row.subject.Account != caller.Account {
				continue
			}
			peer, subject, session, current, err := nativeInboxPeer(row.pin, d.now())
			if err != nil {
				return nil, "", err
			}
			if subject.Account != row.subject.Account || session != row.session || !sameInboxProcess(current, row.process) {
				return nil, "", errors.New("messages: chat owner changed")
			}
			admission, allowed, err := d.captureApplicationAgent(r.ctx, peer, row.application)
			if err != nil {
				return nil, "", err
			}
			if !allowed || row.admission == nil || !sameNativeInboxAdmission(*row.admission, admission) {
				continue
			}
			starts[messageInboxStartKey("app:"+row.application, row.context)] = messageInboxStart{at: row.opened, word: messageStartedWord}
			visible = append(visible, msg.Inbox{Program: "app:" + row.application, Context: row.context, ChatTitle: row.chatTitle, Status: nativeMessageHeader(row.status), Revision: row.revision, LastSeenUnixMs: row.seen.UnixMilli(), Reach: new(msg.InboxReachOpen)})
		}
		sort.Slice(visible, func(i, j int) bool {
			if visible[i].Program != visible[j].Program {
				return visible[i].Program < visible[j].Program
			}
			return visible[i].Context < visible[j].Context
		})
		visible = messageListedInboxTitles(visible, starts, d.now())
		raw, err := json.Marshal(messageInboxSnapshotRows(visible))
		if err != nil {
			return nil, "", err
		}
		if check() != msg.OutcomePage {
			return nil, "", errors.New("messages: operator admission ended")
		}
		return visible, epoch + "." + messageDigest(binding+"\x00"+string(raw)), nil
	}
	rows, digest, err := snapshot()
	if err != nil {
		if check() != msg.OutcomePage {
			return messageEmptyInboxes(msg.OutcomeForbidden), nil
		}
		return messageEmptyInboxes(msg.OutcomeUnavailable), nil
	}
	page := messageInboxesPage(rows, digest, cursor)
	if page.Outcome != msg.OutcomePage {
		return page, nil
	}
	current, currentDigest, err := snapshot()
	if err != nil || check() != msg.OutcomePage {
		return messageEmptyInboxes(msg.OutcomeForbidden), nil
	}
	if !messageInboxDisclosureEqual(rows, current) {
		return messageEmptyInboxes(msg.OutcomeGap), nil
	}
	return messageInboxesPage(current, currentDigest, cursor), nil
}

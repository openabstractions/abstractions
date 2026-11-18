package main

import (
	"context"
	"sort"
	"strings"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Canonical chat routing is a typed correlation attached to a live native
// source and receiving owner. The native process and enrollment supply authority.
func (d *applicationDirectory) currentChatRoute(ctx context.Context, actor messageActor) bool {
	if !d.lockRead(ctx) {
		return false
	}
	defer d.mu.RUnlock()
	return d.currentChatRouteLocked(actor)
}

func (d *applicationDirectory) currentChatRouteLocked(actor messageActor) bool {
	if actor.Chat == nil || d.nativeInboxes == nil || d.nativeInboxes.closed {
		return false
	}
	row := d.nativeInboxes.rows[actor.Application+"\x00"+actor.Chat.ReturnContext]
	if row == nil || row.subject.Account != actor.Account || row.admission == nil || !samePrograms(row.admission.Image.Program, actor.Program) || row.sourceOnly || row.chatID == "" || row.sourceInstance != actor.Chat.SourceInstance || row.sourceContext != actor.Context {
		return false
	}
	source := d.nativeInboxes.rows[row.application+"\x00"+row.sourceContext]
	now := d.now()
	return source != nil && source.sourceOnly && source.instance == row.sourceInstance && source.chatID == row.chatID && sameInboxProcess(source.process, row.process) && d.nativeInboxLiveLocked(row, now) && d.nativeInboxLiveLocked(source, now)
}

// Receiving advertisement is a typed property of the admitted inbox row.
// ApplicationPresence.Interfaces supplies no chat binding or suppression.
func (d *applicationDirectory) receivingNativeInboxLocked(row nativeInbox) bool {
	lease, ok := d.instances[row.instance]
	if !ok || !d.now().Before(lease.deadline) || lease.application != row.application || lease.session != row.session || lease.owner.Account != row.subject.Account || !samePrograms(lease.owner.Program, row.subject.Program) {
		return false
	}
	if row.chatID != "" && !row.sourceOnly {
		source := d.nativeInboxes.rows[row.application+"\x00"+row.sourceContext]
		if source == nil || !source.sourceOnly || source.instance != row.sourceInstance || source.chatID != row.chatID || !sameInboxProcess(source.process, row.process) {
			return false
		}
		sourceLease, live := d.instances[source.instance]
		if !live || !d.now().Before(sourceLease.deadline) {
			return false
		}
	}
	for _, context := range lease.value.Contexts {
		if context.Name == row.context {
			return !row.sourceOnly
		}
	}
	return false
}

// The caller holds the directory lock and owns these independent snapshot pins.
func (d *applicationDirectory) receivingNativeSnapshotsLocked(rows []nativeInbox) []nativeInbox {
	kept := rows[:0]
	for _, row := range rows {
		if d.receivingNativeInboxLocked(row) {
			row.title = row.chatTitle
			kept = append(kept, row)
		} else {
			row.pin.Close()
		}
	}
	return kept
}

// liveReceivingCandidates keeps the rows whose chat has a live receiving
// inbox now, most recently opened first, using context order to break equal
// open times. The caller has already restricted the listed candidates to one
// authorized application.
func (d *applicationDirectory) liveReceivingCandidates(ctx context.Context, candidates []msg.Inbox) ([]msg.Inbox, error) {
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	defer d.unlock()
	d.expireLocked()
	if d.nativeInboxes == nil || d.nativeInboxes.closed {
		return nil, nil
	}
	type liveRow struct {
		inbox  msg.Inbox
		opened time.Time
	}
	var live []liveRow
	for _, candidate := range candidates {
		application := strings.TrimPrefix(candidate.Program, "app:")
		row := d.nativeInboxes.rows[application+"\x00"+candidate.Context]
		if row == nil || !d.receivingNativeInboxLocked(*row) {
			continue
		}
		live = append(live, liveRow{inbox: candidate, opened: row.opened})
	}
	sort.SliceStable(live, func(i, j int) bool {
		if !live[i].opened.Equal(live[j].opened) {
			return live[i].opened.After(live[j].opened)
		}
		return live[i].inbox.Context < live[j].inbox.Context
	})
	rows := make([]msg.Inbox, len(live))
	for i, row := range live {
		rows[i] = row.inbox
	}
	return rows, nil
}

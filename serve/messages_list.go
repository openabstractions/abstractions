package main

import (
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func messageEmptyInboxes(outcome msg.Outcome) msg.InboxPage {
	return msg.InboxPage{Outcome: outcome, Inboxes: []msg.Inbox{}}
}

func (r *messagesReceiver) inboxesList(cursor string) msg.InboxPage {
	page, _ := r.inboxesListNamed(cursor)
	return page
}

// inboxesListNamed also returns what each listed name stands for, keyed by
// program and context, from the same snapshot as the page.
func (r *messagesReceiver) inboxesListNamed(cursor string) (msg.InboxPage, map[string]messageListedName) {
	return r.inboxesListScoped(cursor, true)
}

// inboxesListScoped lists with or without discovered conversations; without
// them it lists open and retained conversations only and waits for nothing.
func (r *messagesReceiver) inboxesListScoped(cursor string, discover bool) (msg.InboxPage, map[string]messageListedName) {
	return r.inboxesListRead(cursor, discover, false)
}

// inboxesListWhole is one read of every listed conversation as one page
// without a cursor. Prepare matches its receiver over the whole listing, so
// it never pages and no change between two pages can leave it incomplete.
func (r *messagesReceiver) inboxesListWhole(discover bool) (msg.InboxPage, map[string]messageListedName) {
	return r.inboxesListRead("", discover, true)
}

func (r *messagesReceiver) inboxesListRead(cursor string, discover, whole bool) (msg.InboxPage, map[string]messageListedName) {
	if len(cursor) > 256 {
		return messageEmptyInboxes(msg.OutcomeInvalid), nil
	}
	if r.host == nil || r.host.messageRights == nil {
		return messageEmptyInboxes(msg.OutcomeUnavailable), nil
	}
	observer, outcome := r.messageObserver("")
	if observer == nil {
		return messageEmptyInboxes(outcome), nil
	}
	defer observer.pin.Close()
	observer.noDiscovery = !discover
	rows, needs, _, digest, err := observer.inboxNamedSnapshot()
	if err != nil {
		return messageEmptyInboxes(msg.OutcomeUnavailable), nil
	}
	page := messageInboxesPage(rows, digest, cursor, needs...)
	if page.Outcome != msg.OutcomePage {
		return page, nil
	}
	current, currentNeeds, names, currentDigest, err := observer.inboxNamedSnapshot()
	if err != nil {
		return messageEmptyInboxes(msg.OutcomeUnavailable), nil
	}
	if cursor != "" && digest != currentDigest {
		return messageEmptyInboxes(msg.OutcomeGap), nil
	}
	// A first page and a whole listing hold no earlier position that a
	// change could invalidate. Their second read rechecks disclosure: the
	// same conversations and the same names to ask permission for. A changed
	// status, title, revision, watch or reach is current content of those
	// same rows, and the page answers it from the second read. With several
	// live chats, requiring mutable fields to match between reads made listing
	// return gap and Send return discovery_incomplete (owner, 2026-11-10).
	if cursor == "" && !messageInboxMembershipEqual(rows, needs, current, currentNeeds) {
		return messageEmptyInboxes(msg.OutcomeGap), nil
	}
	if whole {
		return msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: current, NeedsPermission: currentNeeds}, names
	}
	return messageInboxesPage(current, currentDigest, cursor, currentNeeds...), names
}

// messageInboxMembershipEqual reports whether two reads disclose the same
// conversations, in the same order, with the same own rows, and the same
// agents to ask permission for.
func messageInboxMembershipEqual(before []msg.Inbox, beforeNeeds []msg.AgentApplication, after []msg.Inbox, afterNeeds []msg.AgentApplication) bool {
	if len(before) != len(after) || len(beforeNeeds) != len(afterNeeds) {
		return false
	}
	for i := range before {
		mine := before[i].Mine != nil && *before[i].Mine
		if before[i].Program != after[i].Program || before[i].Context != after[i].Context || mine != (after[i].Mine != nil && *after[i].Mine) {
			return false
		}
	}
	for i := range beforeNeeds {
		if beforeNeeds[i] != afterNeeds[i] {
			return false
		}
	}
	return true
}

// LastSeenUnixMs is observation telemetry. Snapshot identity and final disclosure
// recheck every other field; the returned rows keep their fresh observation time.
func messageInboxSnapshotRows(rows []msg.Inbox) []msg.Inbox {
	stable := append([]msg.Inbox{}, rows...)
	for i := range stable {
		stable[i].LastSeenUnixMs = 0
	}
	return stable
}

func messageInboxDisclosureEqual(before, after []msg.Inbox) bool {
	x, ex := json.Marshal(messageInboxSnapshotRows(before))
	y, ey := json.Marshal(messageInboxSnapshotRows(after))
	return ex == nil && ey == nil && string(x) == string(y)
}

func messageInboxesPage(rows []msg.Inbox, digest, cursor string, needs ...msg.AgentApplication) msg.InboxPage {
	offset := 0
	if cursor != "" {
		parts := strings.Split(cursor, ".")
		if len(parts) != 3 || parts[0]+"."+parts[1] != digest {
			return messageEmptyInboxes(msg.OutcomeGap)
		}
		var err error
		offset, err = strconv.Atoi(parts[2])
		if err != nil || offset < 0 || offset > len(rows) || strconv.Itoa(offset) != parts[2] {
			return messageEmptyInboxes(msg.OutcomeGap)
		}
	}
	end := offset + 64
	if end > len(rows) {
		end = len(rows)
	}
	page := msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: append([]msg.Inbox{}, rows[offset:end]...)}
	if cursor == "" {
		page.NeedsPermission = append([]msg.AgentApplication(nil), needs...)
	}
	if end < len(rows) {
		page.Cursor = digest + "." + strconv.Itoa(end)
	}
	return page
}

func (o *messageObserver) inboxSnapshot() ([]msg.Inbox, string, error) {
	rows, _, digest, err := o.inboxDiscoverySnapshot()
	return rows, digest, err
}

func (o *messageObserver) inboxDiscoverySnapshot() ([]msg.Inbox, []msg.AgentApplication, string, error) {
	rows, needs, _, digest, err := o.inboxNamedSnapshot()
	return rows, needs, digest, err
}

func (o *messageObserver) inboxNamedSnapshot() ([]msg.Inbox, []msg.AgentApplication, map[string]messageListedName, string, error) {
	// Keep the existing row reader's compact error returns while capturing the
	// names-only discovery and listed-name facts in the same authority and
	// snapshot boundary.
	var needs []msg.AgentApplication
	names := map[string]messageListedName{}
	rows, digest, err := o.inboxSnapshotWithDiscovery(&needs, names)
	return rows, needs, names, digest, err
}

type messageInboxRosterFact struct {
	Name, Program string
	State         uint32
	Decision      uint32
}

func (o *messageObserver) inboxSnapshotWithDiscovery(needs *[]msg.AgentApplication, names map[string]messageListedName) ([]msg.Inbox, string, error) {
	if err := o.check(); err != nil {
		return nil, "", err
	}
	r, d := o.r, o.r.host.directory
	if r.host.messageRights == nil || r.host.messageRights.policy == nil {
		return nil, "", errors.New("messages: list permission unavailable")
	}
	// A shared policy outage still refuses the caller's whole view. A target's
	// unavailable eligibility below supplies no disclosure for that target.
	if err := r.host.messageRights.policy.CheckState(r.ctx); err != nil {
		return nil, "", err
	}
	if !d.lockRead(r.ctx) {
		return nil, "", r.ctx.Err()
	}
	if d.nativeInboxesClosed {
		d.mu.RUnlock()
		return nil, "", errors.New("messages: inbox owner closed")
	}
	var rows []nativeInbox
	if b := d.nativeInboxes; b != nil {
		if b.closed {
			d.mu.RUnlock()
			return nil, "", errors.New("messages: inbox owner closed")
		}
		var err error
		rows, err = b.snapshotLocked("")
		if err != nil {
			d.mu.RUnlock()
			return nil, "", err
		}
		rows = d.receivingNativeSnapshotsLocked(rows)
	}
	d.mu.RUnlock()
	defer closeNativeInboxSnapshots(rows)
	visible := []msg.Inbox{}
	// The caller's own conversations: its own native rows and its context.
	ownContexts := map[string]bool{}
	isOwn := func(contextName string) bool {
		return ownContexts[contextName] || messageOwnContext(o.actor, contextName)
	}
	starts := map[string]messageInboxStart{}
	var roster []messageInboxRosterFact
	// The caller is a bound conversation when Send's test holds for every
	// context it can send from (messageSendTargetEligibleTo): Send needs an
	// inbox the caller owns, so every owned inbox must be a chat-* name.
	senderBound := messageCanonicalChatContext(o.actor.returnContext())
	ownedRows := map[string]bool{}
	if _, ownSubject, ownSession, ownProcess, ownErr := inspectNativeInboxPeer(o.pin, d.now()); ownErr == nil {
		unbound := 0
		for _, row := range rows {
			if ownSubject == row.subject && ownSession == row.session && sameInboxProcess(ownProcess, row.process) {
				ownedRows[row.context] = true
				if o.titleApplication != "" {
					ownContexts[row.context] = true
				}
				if !messageCanonicalChatContext(row.context) {
					unbound++
				}
			}
		}
		senderBound = senderBound || len(ownedRows) > 0 && unbound == 0
	}
	// A same-app row is listed only in the sender's project (coordinator E2),
	// as Send admits it (messageSameAppInProject).
	var sameAppProjects map[string]string
	sameAppSender := ""
	inSenderProject := func(contextName string) bool {
		if sameAppProjects == nil {
			sameAppProjects = map[string]string{}
			for _, s := range o.discovered(o.actor.Application) {
				if s.Project != "" {
					sameAppProjects[s.Context] = s.Project
				}
			}
			if kept, err := r.host.messages.retainedChats(r.ctx, o.actor.Account, o.actor.Program); err == nil {
				for _, chat := range kept {
					if chat.Launch != nil && chat.Launch.Cwd != "" {
						if project := messageProject(chat.Launch.Cwd); project != "" {
							sameAppProjects[chat.Context] = project
						}
					}
				}
			}
			sameAppSender = messageSameAppSenderProject(o.actor, sameAppProjects, ownedRows)
		}
		return sameAppSender != "" && sameAppProjects[contextName] == sameAppSender
	}
	for _, row := range rows {
		if o.titleApplication != "" && row.application != o.titleApplication {
			continue
		}
		// The exact retained native owner can inspect its own receiving row.
		// Other sessions of the same executable keep the existing disclosure gate.
		_, ownSubject, ownSession, ownProcess, ownErr := inspectNativeInboxPeer(o.pin, d.now())
		if ownErr != nil {
			return nil, "", ownErr
		}
		own := ownSubject == row.subject && ownSession == row.session && sameInboxProcess(ownProcess, row.process)
		if own {
			ownContexts[row.context] = true
		}
		if !own {
			decision := r.host.messageRights.decide(r.ctx, o.subject, ActionApplicationMessage, "app:"+row.application)
			if decision.Outcome == rights.DecisionOutcomeUnavailable {
				return nil, "", errors.New("messages: list permission unavailable")
			}
			if decision.Outcome != rights.DecisionOutcomePermitted {
				continue
			}
		}
		peer, subject, session, process, err := nativeInboxPeer(row.pin, d.now())
		if err != nil {
			return nil, "", err
		}
		if subject.Account != row.subject.Account || session != row.session || !sameInboxProcess(process, row.process) {
			return nil, "", errors.New("messages: list native owner changed")
		}
		info, allowed, err := d.captureApplicationAgent(r.ctx, peer, row.application)
		if err != nil {
			return nil, "", err
		}
		if !allowed {
			continue
		}
		if row.admission != nil && !sameNativeInboxAdmission(*row.admission, info) {
			continue
		}
		// Another conversation of the sender's own app is a receiver (owner,
		// 2026-11-09); other inboxes of the same executable stay hidden.
		if !own && (info.Image.Key == o.admission.Image.Key || samePrograms(info.Image.Program, o.actor.Program)) && (!messageSameAppReceivers || !senderBound || !messageCanonicalChatContext(row.context) || isOwn(row.context) || row.application != o.actor.Application || !inSenderProject(row.context)) {
			continue
		}
		watching := r.host.messageIsWatching(r.ctx, subject.Account, info.Image.Program, row.context)
		inbox := msg.Inbox{Program: "app:" + row.application, Context: row.context, ChatTitle: row.title, Status: nativeMessageHeader(row.status), Revision: row.revision, Watching: watching, LastSeenUnixMs: row.seen.UnixMilli(), Reach: new(r.host.messageInboxReach(row.application, info.Image.Program, row.context, row.status, watching))}
		if own {
			inbox.Mine = new(true)
		}
		starts[messageInboxStartKey(inbox.Program, inbox.Context)] = messageInboxStart{at: row.opened, word: messageStartedWord}
		visible = append(visible, inbox)
	}
	// An approved registered recipe supplies an inactive routing context.
	// This projection performs no start or permission request.
	if roles := d.roles.Load(); roles != nil && roles.messaging != nil {
		if err := roles.messaging.current(r.ctx); err != nil {
			return nil, "", err
		}
		running := d.appsRunning(r.ctx)
		// Every agent's discovery refresh starts before any is waited for.
		// Discovery runs only for agents this sender may message: their
		// decisions come first, and only those agents' refreshes start.
		if o.discoverable == nil && !o.noDiscovery && o.titleApplication == "" {
			o.discoverable = map[string]bool{}
			// The sender's own app tells its own folder for the project scope;
			// its listing is the sender's own conversations.
			if o.actor.Application != "" {
				o.discoverable[o.actor.Application] = true
			}
			for _, agent := range roles.messaging.agents {
				if r.host.messageRights.decide(r.ctx, o.subject, ActionApplicationMessage, "app:"+agent.name).Outcome == rights.DecisionOutcomePermitted {
					o.discoverable[agent.name] = true
				}
			}
		}
		if d.messageAttachWarm != nil && !o.warmed && !o.noDiscovery && o.titleApplication == "" {
			o.warmed = true
			names := make([]string, 0, len(o.discoverable))
			for _, agent := range roles.messaging.agents {
				if o.discoverable[agent.name] {
					names = append(names, agent.name)
				}
			}
			d.messageAttachWarm(r.ctx, names)
		}
		var scope *string
		for _, agent := range roles.messaging.agents {
			if o.titleApplication != "" && agent.name != o.titleApplication {
				continue
			}
			var receiver applicationAgentImage
			target := d.currentAgentEnrollmentImage(r.ctx, agent.name, &receiver)
			fact := messageInboxRosterFact{Name: agent.name, Program: agent.program}
			if target.Enrollment != nil {
				fact.State = uint32(target.Enrollment.State)
			}
			if target.Outcome != applications.OutcomePage || target.Enrollment == nil {
				roster = append(roster, fact)
				continue
			}
			sameAppListed := messageSameAppReceivers && senderBound && target.Enrollment.State == applications.AgentEnrollmentStateEnrolled && receiver.Program != ""
			if !messageSendTargetEligible(o.actor, o.admission, *target.Enrollment, receiver) && !sameAppListed {
				roster = append(roster, fact)
				continue
			}
			sameApp := !messageSendTargetEligible(o.actor, o.admission, *target.Enrollment, receiver)
			decision := r.host.messageRights.decide(r.ctx, o.subject, ActionApplicationMessage, "app:"+agent.name)
			fact.Decision = uint32(decision.Outcome)
			roster = append(roster, fact)
			if decision.Outcome == rights.DecisionOutcomeUnavailable {
				continue
			}
			// The same-app direction is offered too: the person grants it
			// through the same request-access question (ruling 2026-11-09).
			if decision.Outcome == rights.DecisionOutcomeNotGranted {
				descriptor, _, err := d.agentSnapshot(r.ctx, agent.name)
				if err != nil {
					return nil, "", err
				}
				*needs = append(*needs, msg.AgentApplication{Program: "app:" + agent.name, Title: nativeMessageHeader(descriptor.Title)})
			}
			if decision.Outcome != rights.DecisionOutcomePermitted {
				continue
			}
			retained, err := r.host.messages.retainedChats(r.ctx, o.actor.Account, agent.program)
			if err != nil {
				return nil, "", err
			}
			profile, profileErr := d.prepareMessageStart(r.ctx, agent.name)
			for _, chat := range retained {
				if isOwn(chat.Context) || sameApp && !inSenderProject(chat.Context) {
					continue
				}
				found := false
				for _, row := range visible {
					found = found || row.Program == "app:"+agent.name && row.Context == chat.Context
				}
				if !found {
					starts[messageInboxStartKey("app:"+agent.name, chat.Context)] = messageInboxStart{at: time.UnixMilli(chat.LastSeenUnixMs), word: messageLastSeenWord}
					visible = append(visible, retainedChatInbox(agent.name, chat, profile, profileErr, running))
				}
			}
			// Discovered conversations of the sender's project (architect
			// ruling 1): one row per chat-* name, never the sender's own.
			if scope == nil {
				project := r.host.messageSenderProjectIn(o.actor, o.discovered, ownContexts)
				scope = &project
			}
			for _, s := range o.discovered(agent.name) {
				if !messageCanonicalChatContext(s.Context) || isOwn(s.Context) || *scope != "" && s.Project != *scope || sameApp && !inSenderProject(s.Context) {
					continue
				}
				listed := false
				for _, row := range visible {
					listed = listed || row.Program == "app:"+agent.name && row.Context == s.Context
				}
				if !listed {
					inbox := discoveredChatInbox(agent.name, s, profile, profileErr, running)
					starts[messageInboxStartKey(inbox.Program, inbox.Context)] = messageInboxStart{at: time.UnixMilli(inbox.LastSeenUnixMs), word: messageLastActiveWord}
					visible = append(visible, inbox)
				}
			}
			// A same-app sender reaches only existing chats; Send refuses
			// the recipe's start context to it.
			if sameApp || profileErr != nil || !applicationContextName.MatchString(profile.Context) || d.messageStartChatsOnly {
				// Where starts are limited to existing chats, the recipe's own
				// context is no receiver: a question there would only wait.
				continue
			}
			found := false
			for _, row := range visible {
				found = found || row.Program == "app:"+agent.name && row.Context == profile.Context
			}
			if !found {
				starts[messageInboxStartKey("app:"+agent.name, profile.Context)] = messageInboxStart{}
				visible = append(visible, msg.Inbox{Program: "app:" + agent.name, Context: profile.Context, Status: "Inactive registered receiver", Revision: profile.RegistrationBinding, Reach: new(msg.InboxReachStartsOnQuestion)})
			}
		}
		if err := roles.messaging.current(r.ctx); err != nil {
			return nil, "", err
		}
	}
	sort.Slice(*needs, func(i, j int) bool { return (*needs)[i].Program < (*needs)[j].Program })
	if len(*needs) > 64 {
		*needs = (*needs)[:64]
	}
	sort.Slice(visible, func(i, j int) bool {
		if visible[i].Program != visible[j].Program {
			return visible[i].Program < visible[j].Program
		}
		return visible[i].Context < visible[j].Context
	})
	visible, facts := messageListedInboxNames(visible, starts, d.now())
	if o.titleApplication != "" {
		// A receipt reads one target-app snapshot without minting a public
		// page or cursor. Recheck current sender admission and target rights
		// after collecting and normalizing the candidate titles.
		if err := o.check(); err != nil {
			return nil, "", err
		}
		if r.host.messageRights.decide(r.ctx, o.subject, ActionApplicationMessage, "app:"+o.titleApplication).Outcome != rights.DecisionOutcomePermitted {
			return nil, "", errors.New("messages: receipt title permission unavailable")
		}
		return visible, "", nil
	}
	for i, row := range visible {
		names[messageInboxStartKey(row.Program, row.Context)] = facts[i]
	}
	raw, err := json.Marshal(struct {
		Epoch, Account, Program, Image string
		Rows                           []msg.Inbox
		NeedsPermission                []msg.AgentApplication
		Roster                         []messageInboxRosterFact
	}{d.epoch, o.actor.Account, o.actor.Program, o.admission.Image.Key, messageInboxSnapshotRows(visible), *needs, roster})
	if err != nil {
		return nil, "", err
	}
	if err := o.check(); err != nil {
		return nil, "", err
	}
	if err := r.host.messageRights.policy.CheckState(r.ctx); err != nil {
		return nil, "", err
	}
	return visible, d.epoch + "." + messageDigest(string(raw)), nil
}

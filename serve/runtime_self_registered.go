package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A program that registered itself under the account-wide registration
// default (rights RIGHTS-R22) is kept unconfirmed, and reads "(added itself)",
// until the person answers the runtime's own question about it,
// asks.SelfRegisteredKey, in the native prompt:
//   - Keep marks the entry confirmed;
//   - Remove removes every unconfirmed entry of its program and blocks the
//     program from registering itself again;
//   - closing the prompt or its expiry answers nothing.
//
// The question is asked once when the program registers itself, and again when
// the person asks for it from Apps (AskConfirmation). Each registration has
// its own question: the directory records the question's id with the entry,
// and only an answer to that question acts on it, so an answer to an earlier
// registration's question never confirms or removes a later one. The question
// names the program by the path the registration bound and shows the
// publisher bound from the registering connection. Only the person answers it:
// it vouches for app-written title text, and the answerer check admits
// nothing but first-use permission questions (asks ASK-R11).

// selfRegisteredWhy is the reason on the Block a Remove writes.
const selfRegisteredWhy = "removed by the person when it added itself"

// selfRegisteredQuestions admits and follows the questions for one runtime.
type selfRegisteredQuestions struct {
	a   *runtimeAsks
	d   *applicationDirectory
	ctx context.Context
	// following holds the ids whose decision a goroutine waits for, so a
	// question is followed once however often it is asked again.
	mu        sync.Mutex
	following map[string]bool
}

// keepSelfRegistered connects the directory's self-registrations to the
// person's question and resumes following the questions a restart left.
func (a *runtimeAsks) keepSelfRegistered(ctx context.Context, d *applicationDirectory) *selfRegisteredQuestions {
	if a == nil || d == nil {
		return nil
	}
	k := &selfRegisteredQuestions{a: a, d: d, ctx: ctx, following: map[string]bool{}}
	d.askKeep = k.ask
	k.resume()
	return k
}

// selfRegisteredRequestKey is a fresh request key for one question: each
// registration, and each question asked again after one expired, is its own
// admission.
func selfRegisteredRequestKey() (string, error) {
	var raw [20]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "self-registered:" + hex.EncodeToString(raw[:]), nil
}

// selfRegisteredSlot bounds one line of text to the question's slot bound;
// a longer one keeps its end, where a path keeps its file name.
func selfRegisteredSlot(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= 200 {
		return text
	}
	runes := []rune(text)
	return "…" + string(runes[len(runes)-199:])
}

// selfRegisteredTitle is the title the question names: the registered title
// on one line, shortened to the question's slot bound.
func selfRegisteredTitle(v wire.ApplicationDescriptor) string {
	title := strings.Join(strings.Fields(v.Title), " ")
	if title == "" {
		title = v.Name
	}
	if utf8.RuneCountInString(title) > 200 {
		runes := []rune(title)
		title = string(runes[:199]) + "…"
	}
	return title
}

// ask makes sure the person is asked about the unconfirmed entry name: the
// question recorded for its registration is followed while pending and
// applied when answered; with none, or one that expired, a new question is
// admitted and recorded with the entry.
func (k *selfRegisteredQuestions) ask(name string) error {
	v, mark, ok := k.d.selfEntry(name)
	if !ok {
		return nil
	}
	if mark.Question != "" {
		record, found, err := k.a.book.ApplicationRecord(mark.Question)
		if err != nil {
			return err
		}
		if found && record.Pending() {
			k.follow(record.ID, name, mark.boundProgram(v))
			return nil
		}
		if found {
			k.apply(record, name, mark.boundProgram(v))
			return nil
		}
	}
	requestKey, err := selfRegisteredRequestKey()
	if err != nil {
		return err
	}
	slots := map[string]string{"program": selfRegisteredSlot(mark.boundProgram(v)), "title": selfRegisteredTitle(v)}
	via := listen.Seen{Why: "admitted by the runtime after a program registered itself"}
	record, outcome, err := k.a.book.AskApplicationFrom(k.a.scope, k.a.display, requestKey, asks.SelfRegisteredKey, slots, via, mark.publisher())
	if err != nil {
		return err
	}
	if outcome != "pending" {
		return fmt.Errorf("asks: self-registration question %s", outcome)
	}
	if !k.d.setQuestion(k.ctx, name, mark.boundProgram(v), record.ID) {
		return errors.New("asks: the self-registered entry changed while it was asked about")
	}
	k.follow(record.ID, name, mark.boundProgram(v))
	return nil
}

// resume follows the question recorded for each unconfirmed entry after a
// restart, and applies an answer given while the runtime was stopped. Only
// the recorded question is read: an earlier registration's answered question
// never acts again.
func (k *selfRegisteredQuestions) resume() {
	for name, mark := range k.d.selfEntries() {
		if mark.Question == "" {
			continue
		}
		v, _, ok := k.d.selfEntry(name)
		if !ok {
			continue
		}
		record, found, err := k.a.book.ApplicationRecord(mark.Question)
		if err != nil || !found {
			continue
		}
		if record.Pending() {
			k.follow(record.ID, name, mark.boundProgram(v))
		} else {
			k.apply(record, name, mark.boundProgram(v))
		}
	}
}

// follow waits, in the background, for the question id to be decided, and
// applies a Keep or Remove answer. An expired or retired question changes
// nothing. A question already followed is not followed twice.
func (k *selfRegisteredQuestions) follow(id, name, program string) {
	k.mu.Lock()
	if k.following[id] {
		k.mu.Unlock()
		return
	}
	k.following[id] = true
	k.mu.Unlock()
	decided := k.a.book.Decided(id)
	go func() {
		defer func() {
			k.mu.Lock()
			delete(k.following, id)
			k.mu.Unlock()
		}()
		select {
		case <-k.ctx.Done():
			return
		case <-decided:
		}
		record, found, err := k.a.book.ApplicationRecord(id)
		if err != nil {
			k.a.report(fmt.Errorf("asks: reading the self-registration answer: %w", err))
			return
		}
		if found {
			k.apply(record, name, program)
		}
	}()
}

// followed reports whether a goroutine waits for the question id.
func (k *selfRegisteredQuestions) followed(id string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.following[id]
}

// apply carries out one answered question, when it is the question of the
// entry's current registration.
func (k *selfRegisteredQuestions) apply(record asks.Record, name, program string) {
	if record.Key != asks.SelfRegisteredKey || record.Pending() {
		return
	}
	switch record.Option {
	case "keep":
		if !k.d.keepOwned(k.ctx, name, program, record.ID) {
			k.a.report(fmt.Errorf("asks: the Keep answer %s is not the question of %s's current registration", record.ID, name))
		}
	case "remove":
		if !k.d.owned(name, program, record.ID) {
			k.a.report(fmt.Errorf("asks: the Remove answer %s is not the question of %s's current registration", record.ID, name))
			return
		}
		// The Block lands first, so the program cannot register itself again
		// between the removal and the Block.
		if err := k.a.rights.blockRegisterSelf(program); err != nil {
			k.a.report(fmt.Errorf("asks: blocking %s from registering itself: %w", program, err))
		}
		k.d.removeOwned(k.ctx, name, program, record.ID)
	}
}

// blockRegisterSelf writes a Block on application.register_self for program,
// so the program does not register itself straight back.
func (r *runtimeRights) blockRegisterSelf(program string) error {
	subject := rwire.Subject{Account: r.owner, Program: program}
	deny := false
	for attempt := 0; attempt < installationAttempts; attempt++ {
		read := r.policy.ReadRule(subject, ActionApplicationRegisterSelf, "account")
		if read.Outcome != rwire.RuleReadOutcomeFound && read.Outcome != rwire.RuleReadOutcomeExpired && read.Outcome != rwire.RuleReadOutcomeUnknown {
			return fmt.Errorf("read %s", read.Outcome)
		}
		if read.Outcome == rwire.RuleReadOutcomeFound && !read.Record.Rule.Permit {
			return nil
		}
		edit, err := r.policy.ChangeRule(read.Revision, subject, ActionApplicationRegisterSelf, "account",
			rights.RuleEdit{Permit: &deny, By: r.self(), Why: selfRegisteredWhy}, func() error { return nil })
		if err != nil {
			return err
		}
		switch edit.Outcome {
		case rwire.PolicyEditOutcomeApplied:
			return nil
		case rwire.PolicyEditOutcomeConflict:
			continue
		}
		return fmt.Errorf("edit %s", edit.Outcome)
	}
	return errors.New("the policy kept changing")
}

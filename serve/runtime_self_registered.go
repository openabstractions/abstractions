package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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

// A pending program registration, including a legacy application registered
// under the account-wide default (rights RIGHTS-R22), stays unconfirmed
// until the person answers the runtime's own question about it,
// asks.SelfRegisteredKey, in the native prompt:
//   - Keep marks the entry confirmed;
//   - Remove removes the current pending PROGRAM (or the legacy unconfirmed
//     entries) and blocks that program from registering itself again;
//   - closing the prompt or its expiry answers nothing.
//
// Registration asks nothing (VISION 2026-10-06, amended 2026-11-10): the
// question is asked only on a person's action, when the person acts on the
// entry in Apps or presses Connect for it in the Catalog (AskConfirmation),
// reviews a pending program in the Panel, or an operator program registers a
// program for the person's Setup. Once asked, a Keep stays asked: a runtime
// start finds the entry still pending with its question expired or gone, and
// asks again. Each registration has
// its own question: the directory records the question's id with the entry,
// and only an answer to that question acts on it, so an answer to an earlier
// registration's question never confirms or removes a later one. The question
// names the program by the path the registration bound and shows the
// publisher from the captured program image or legacy registering connection.
// Only the person answers it:
// it vouches for app-written title text, and the answerer check admits
// nothing but first-use permission questions (asks ASK-R11).

// selfRegisteredWhy is the reason on the Block a Remove writes.
const selfRegisteredWhy = "removed by the person when it added itself"

// selfRegisteredQuestions admits and follows the questions for one runtime.
type selfRegisteredQuestions struct {
	programReview *shippedProgramReview
	unsigned      *unsignedImageReviews
	a             *runtimeAsks
	d             *applicationDirectory
	ctx           context.Context
	// following holds the ids whose decision a goroutine waits for, so a
	// question is followed once however often it is asked again.
	mu        sync.Mutex
	following map[string]bool
}

func selfRegistrationScope(runtimeScope, revision string) string {
	sum := sha256.Sum256([]byte(runtimeScope + "\x00self-registration\x00" + revision))
	return "self-registration:" + hex.EncodeToString(sum[:])
}

// keepSelfRegistered connects the directory's self-registrations to the
// person's question and resumes following the questions a restart left.
func (a *runtimeAsks) keepSelfRegistered(ctx context.Context, d *applicationDirectory) *selfRegisteredQuestions {
	return a.keepSelfRegisteredWithProgramReview(ctx, d, nil)
}

func (a *runtimeAsks) keepSelfRegisteredWithProgramReview(ctx context.Context, d *applicationDirectory, review *shippedProgramReview) *selfRegisteredQuestions {
	if a == nil || d == nil {
		return nil
	}
	k := &selfRegisteredQuestions{a: a, d: d, ctx: ctx, following: map[string]bool{}}
	d.askKeep = k.ask
	d.keepConnected = k.keepConnected
	k.unsigned = newUnsignedImageReviews(k)
	d.reviewUnsigned = k.unsigned.review
	d.unsignedHeldBack = k.unsigned.heldBack
	d.unsignedLaunch = k.unsigned.launchAttempt
	a.keepAgentEnrollments(ctx, d)
	if review != nil {
		k.programReview = review
		review.attach(k)
	}
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
// on one line, shortened to the question's slot bound. A title is a display
// name: an empty or path-like one reads as the program's file name without
// its extension.
func selfRegisteredTitle(v wire.ApplicationDescriptor) string {
	title := strings.Join(strings.Fields(v.Title), " ")
	if title == "" {
		title = v.Name
	}
	if title == "" || strings.ContainsAny(title, `\/`) {
		program := v.Program
		if program == "" {
			program = title
		}
		title = programFileStem(program)
	}
	if utf8.RuneCountInString(title) > 200 {
		runes := []rune(title)
		title = string(runes[:199]) + "…"
	}
	return title
}

// programFileStem is a program path's file name without its extension.
func programFileStem(program string) string {
	name := program[strings.LastIndexAny(program, `\/`)+1:]
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	return name
}

// ask makes sure the person is asked about the unconfirmed entry name: the
// question recorded for its registration is followed while pending and
// applied when answered; with none, or one that expired, a new question is
// admitted and recorded with the entry.
func (k *selfRegisteredQuestions) ask(name string) error {
	name = k.selfName(name)
	v, mark, ok := k.selfEntry(name)
	if !ok {
		return nil
	}
	if strings.HasPrefix(name, selfRegisteredProgramPrefix) {
		if owned, err := k.programReview.reviewRegistered(k.ctx, strings.TrimPrefix(name, selfRegisteredProgramPrefix), mark.registrationRevision()); owned || err != nil {
			return err
		}
	}
	if strings.HasPrefix(name, selfRegisteredProgramPrefix) && k.programReview.captures(strings.TrimPrefix(name, selfRegisteredProgramPrefix), mark.registrationRevision()) {
		return nil
	}
	if mark.Question != "" {
		record, found, err := k.a.book.ApplicationRecord(mark.Question)
		if err != nil {
			return err
		}
		if found && record.Pending() {
			if mark.Removing {
				return errors.New("asks: Remove intent has no retained Remove answer")
			}
			k.follow(record.ID, name, mark.boundProgram(v))
			return nil
		}
		if found {
			if mark.Removing && record.Option != "remove" {
				return errors.New("asks: Remove intent has no retained Remove answer")
			}
			k.apply(record, name, mark.boundProgram(v))
			return nil
		}
	}
	if mark.Removing {
		return errors.New("asks: retained Remove answer unavailable")
	}
	requestKey, err := selfRegisteredRequestKey()
	if err != nil {
		return err
	}
	slots := map[string]string{"program": selfRegisteredSlot(mark.boundProgram(v)), "title": selfRegisteredTitle(v)}
	via := listen.Seen{Why: "admitted by the runtime after a program registered itself"}
	if strings.HasPrefix(name, selfRegisteredProgramPrefix) {
		via.Why = "admitted by the runtime for the current pending program registration"
	}
	scope := k.a.scope
	if revision := mark.registrationRevision(); revision != "" {
		scope = selfRegistrationScope(k.a.scope, revision)
	}
	record, outcome, err := k.a.book.AskApplicationFrom(scope, k.a.display, requestKey, asks.SelfRegisteredKey, slots, via, mark.publisher())
	if err != nil {
		return err
	}
	if outcome != "pending" {
		return fmt.Errorf("asks: self-registration question %s", outcome)
	}
	if !k.setQuestion(name, mark.boundProgram(v), record.ID, mark.registrationRevision()) {
		return errors.New("asks: the self-registered entry changed while it was asked about")
	}
	k.follow(record.ID, name, mark.boundProgram(v))
	return nil
}

// resume follows the question recorded for each unconfirmed entry after a
// restart, and applies an answer given while the runtime was stopped. Only
// the recorded question is read: an earlier registration's answered question
// never acts again. A recorded question that expired, or that the Book no
// longer holds, is asked again: the person's action that raised it still
// waits for an answer, and a restart or an upgrade never drops it.
func (k *selfRegisteredQuestions) resume() {
	for name, mark := range k.selfEntries() {
		if strings.HasPrefix(name, selfRegisteredProgramPrefix) && k.programReview.captures(strings.TrimPrefix(name, selfRegisteredProgramPrefix), mark.registrationRevision()) {
			continue
		}
		if mark.Question == "" {
			// Nobody asked about it yet: it waits for the person's action in
			// Apps, and a restart asks nothing.
			continue
		}
		v, _, ok := k.selfEntry(name)
		if !ok {
			continue
		}
		record, found, err := k.a.book.ApplicationRecord(mark.Question)
		if err != nil {
			continue
		}
		if !found {
			if !mark.Removing {
				if err := k.ask(name); err != nil {
					k.a.report(fmt.Errorf("asks: asking again about %s after a restart: %w", name, err))
				}
			}
			continue
		}
		if mark.Removing && (record.Pending() || record.Option != "remove" || record.Key != asks.SelfRegisteredKey) {
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
		if !k.keepOwned(name, program, record.ID) {
			k.a.report(fmt.Errorf("asks: the Keep answer %s is not the question of %s's current registration", record.ID, name))
		}
	case "remove":
		if err := k.removeOwned(name, program, record.ID, func() error { return k.blockRegisterSelf(name, program) }); err != nil {
			k.a.report(fmt.Errorf("asks: applying the current Remove answer %s: %w", record.ID, err))
		}
	}
}

// blockRegisterSelf writes a Block on application.register_self for program,
// so the program does not register itself straight back.
func (r *runtimeRights) blockRegisterSelf(program string) error {
	return r.blockRegisterSelfBecause(program, selfRegisteredWhy)
}

func (r *runtimeRights) blockRegisterSelfBecause(program, why string) error {
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
			rights.RuleEdit{Permit: &deny, By: r.self(), Why: why}, func() error { return nil })
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

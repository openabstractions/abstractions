//go:build windows

package main

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/fileid"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// A kept provider that serves chat is declared again with video, and the
// change waits for the person's Keep. Chat keeps its declaration and binding
// unchanged; the video model answers approval:pending. Keeping the change
// serves both (owner's R31 PC, 2026-11-12: `models host --get` disabled
// every chat model while its Keep question waited).
func TestRegistryV3ChangeWaitsWhileLastKeptDeclarationServes(t *testing.T) {
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	answer(t, run.f, programKeepQuestion(t, run, record.Key).ID, "keep")
	waitDirectory(t, run.d, func() bool {
		v, ok := programRegistrationNow(run.d, record.Key)
		return ok && v.State == registration.StateEnabled
	})
	served := func() providerFile {
		t.Helper()
		files, _, _, err := run.d.programProviderSnapshot(run.ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range servingView(files) {
			if f.Declaration.Name == "unsigned" {
				return f
			}
		}
		t.Fatal("provider not projected", files)
		return providerFile{}
	}
	binding := func() string {
		r.providers.mu.Lock()
		defer r.providers.mu.Unlock()
		bindings, _ := r.providers.registrationBindingsLocked()
		return bindings["unsigned"]
	}
	kept := served()
	keptBinding := binding()
	if kept.Disabled || keptBinding == "" || keptBinding != kept.bindingID() {
		t.Fatal("kept provider does not serve", kept.Disabled, keptBinding)
	}

	current, _ := programRegistrationNow(run.d, record.Key)
	spec := current.Spec
	spec.Providers = slices.Clone(spec.Providers)
	spec.Providers[0].Services = append(slices.Clone(spec.Providers[0].Services), inference.VideoContract)
	spec.Providers[0].Models = append(slices.Clone(spec.Providers[0].Models), registration.ScopedModel{Modality: "video", Model: "ltx-2.5"})
	out := r.registerProgram(run.ctx, caller, current.Revision, false, spec, fileid.Self)
	if out.Outcome != registration.OutcomeApplied || out.Reason != approvalPendingReason {
		t.Fatal("re-declaration", out)
	}
	waiting := served()
	if waiting.Disabled || waiting.bindingID() != kept.bindingID() || binding() != keptBinding ||
		!slices.Equal(waiting.Declaration.Models, kept.Declaration.Models) || !slices.Equal(waiting.waiting, []string{"video:ltx-2.5"}) {
		t.Fatal("the kept declaration stopped serving while the change waits", waiting.Disabled, waiting.Declaration.Models, waiting.waiting)
	}
	// Listings and edits read the change as declared, waiting.
	listed, _, _, err := run.d.programProviderSnapshot(run.ctx)
	if err != nil || !slices.ContainsFunc(listed, func(f providerFile) bool {
		return f.Declaration.Name == "unsigned" && f.Disabled && f.disabledReason() == approvalPendingReason && slices.Contains(f.Declaration.Models, "video:ltx-2.5")
	}) {
		t.Fatal("the listing does not show the waiting change", listed, err)
	}
	if why := r.providers.waitingReason(run.ctx, "literal:model", "chat"); why != "" {
		t.Fatal("chat reads as waiting", why)
	}
	if why := r.providers.waitingReason(run.ctx, "ltx-2.5", "video"); why != approvalPendingReason {
		t.Fatal("video's wait", why)
	}

	answer(t, run.f, programKeepQuestion(t, run, record.Key).ID, "keep")
	waitDirectory(t, run.d, func() bool {
		v, ok := programRegistrationNow(run.d, record.Key)
		return ok && v.State == registration.StateEnabled
	})
	after := served()
	if after.Disabled || !slices.Contains(after.Declaration.Models, "video:ltx-2.5") || len(after.waiting) != 0 {
		t.Fatal("the kept change does not serve", after.Disabled, after.Declaration.Models, after.waiting)
	}
	if why := r.providers.waitingReason(run.ctx, "ltx-2.5", "video"); why != "" {
		t.Fatal("video still waits after Keep", why)
	}
}

// The model host's Keep question expired unanswered (15 minutes) and the
// changed declaration stayed approval:pending with no question to answer
// (owner's R31 PC, 2026-11-12). The kept declaration serves throughout, and
// the next need for the waiting change raises exactly one new question.
func TestRegistryV3ExpiredKeepIsAskedAgainAtTheNextNeed(t *testing.T) {
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	answer(t, run.f, programKeepQuestion(t, run, record.Key).ID, "keep")
	waitDirectory(t, run.d, func() bool {
		v, ok := programRegistrationNow(run.d, record.Key)
		return ok && v.State == registration.StateEnabled
	})
	current, _ := programRegistrationNow(run.d, record.Key)
	spec := current.Spec
	spec.Providers = slices.Clone(spec.Providers)
	spec.Providers[0].Services = append(slices.Clone(spec.Providers[0].Services), inference.VideoContract)
	spec.Providers[0].Models = append(slices.Clone(spec.Providers[0].Models), registration.ScopedModel{Modality: "video", Model: "ltx-2.5"})
	if out := r.registerProgram(run.ctx, caller, current.Revision, false, spec, fileid.Self); out.Outcome != registration.OutcomeApplied || out.Reason != approvalPendingReason {
		t.Fatal("re-declaration", out)
	}
	first := programKeepQuestion(t, run, record.Key)

	if err := run.f.a.book.SetPendingLifetime(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if _, found, err := run.f.a.book.ApplicationRecord(first.ID); err != nil || found {
		t.Fatal("question did not expire", found, err)
	}
	files, _, _, err := run.d.programProviderSnapshot(run.ctx)
	if err != nil || !slices.ContainsFunc(servingView(files), func(f providerFile) bool { return f.Declaration.Name == "unsigned" && !f.Disabled }) {
		t.Fatal("the kept declaration stopped serving when the question expired", files, err)
	}
	// The need: a video request for the waiting model. The Book's clock
	// stays past the first question's lifetime until the new one is written.
	if why := r.providers.waitingReason(run.ctx, "ltx-2.5", "video"); why != approvalPendingReason {
		t.Fatal("video's wait", why)
	}
	if err := run.f.a.book.SetPendingLifetime(asks.DefaultPendingLifetime); err != nil {
		t.Fatal(err)
	}
	_, _, mark, ok := run.d.programSelfEntry(record.Key)
	if !ok || mark.Question == "" || mark.Question == first.ID {
		t.Fatal("the need did not ask again", ok, mark)
	}
	if q, found, err := run.f.a.book.ApplicationRecord(mark.Question); err != nil || !found || !q.Pending() {
		t.Fatal("the question asked again is not pending", found, err)
	}
	second := mark.Question
	if why := r.providers.waitingReason(run.ctx, "ltx-2.5", "video"); why != approvalPendingReason {
		t.Fatal("video's wait", why)
	}
	if _, _, mark, _ := run.d.programSelfEntry(record.Key); mark.Question != second {
		t.Fatal("a second need asked another question", mark.Question, second)
	}
	pending := 0
	for _, q := range run.f.a.book.List(true) {
		if q.Key == asks.SelfRegisteredKey {
			pending++
		}
	}
	if pending != 1 {
		t.Fatal("pending Keep questions", pending)
	}
}

// redeclareWithVideo keeps the fixture's provider, then declares it again
// with a video model, so the change waits for the person's Keep while the
// kept declaration serves.
func redeclareWithVideo(t *testing.T) (*runtimeRegistry, *selfRegisteredRun, registration.Registration) {
	t.Helper()
	r, run, caller, program := registry3UnsignedPersonFixture(t)
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	answer(t, run.f, programKeepQuestion(t, run, record.Key).ID, "keep")
	waitDirectory(t, run.d, func() bool {
		v, ok := programRegistrationNow(run.d, record.Key)
		return ok && v.State == registration.StateEnabled
	})
	current, _ := programRegistrationNow(run.d, record.Key)
	spec := current.Spec
	spec.Providers = slices.Clone(spec.Providers)
	spec.Providers[0].Services = append(slices.Clone(spec.Providers[0].Services), inference.VideoContract)
	spec.Providers[0].Models = append(slices.Clone(spec.Providers[0].Models), registration.ScopedModel{Modality: "video", Model: "ltx-2.5"})
	if out := r.registerProgram(run.ctx, caller, current.Revision, false, spec, fileid.Self); out.Outcome != registration.OutcomeApplied || out.Reason != approvalPendingReason {
		t.Fatal("re-declaration", out)
	}
	return r, run, record
}

// keptServes reports whether the fixture's provider serves.
func keptServes(t *testing.T, run *selfRegisteredRun) bool {
	t.Helper()
	files, _, _, err := run.d.programProviderSnapshot(run.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(servingView(files), func(f providerFile) bool { return f.Declaration.Name == "unsigned" && !f.Disabled })
}

// Needs that arrive together after the Keep question expired raise one
// question between them: the Book joins an ask to the pending question of
// the same scope and content.
func TestRegistryV3ExpiredKeepIsAskedOnceForNeedsTogether(t *testing.T) {
	r, run, record := redeclareWithVideo(t)
	first := programKeepQuestion(t, run, record.Key)
	if err := run.f.a.book.SetPendingLifetime(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if _, found, err := run.f.a.book.ApplicationRecord(first.ID); err != nil || found {
		t.Fatal("question did not expire", found, err)
	}
	if err := run.f.a.book.SetPendingLifetime(asks.DefaultPendingLifetime); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.providers.waitingReason(run.ctx, "ltx-2.5", "video")
		}()
	}
	wg.Wait()
	pending := 0
	for _, q := range run.f.a.book.List(true) {
		if q.Key == asks.SelfRegisteredKey {
			pending++
		}
	}
	if pending != 1 {
		t.Fatal("pending Keep questions", pending)
	}
}

// The last served copy serves only with the image it served with, and the
// person's Remove ends it.
func TestRegistryV3ServedCopyEndsOnAnotherImageAndOnRemove(t *testing.T) {
	_, run, record := redeclareWithVideo(t)
	if !keptServes(t, run) {
		t.Fatal("the kept declaration does not serve while the change waits")
	}
	if !run.d.lock(run.ctx) {
		t.Fatal("directory lock")
	}
	var other bool
	err := run.d.withRegistrationReadLocked(run.ctx, func(map[string]legacy.ApplicationDescriptor) error {
		s := run.d.v3
		source := s.programSources[record.Key]
		moved := source
		moved.FileKey = "another-image"
		s.programSources[record.Key] = moved
		current, _ := s.programRecord(record.Key)
		_, other = s.servedWhilePending(record.Key, current)
		s.programSources[record.Key] = source
		return nil
	})
	run.d.unlock()
	if err != nil || other {
		t.Fatal("the served copy serves with another program image", other, err)
	}
	reserved, finish := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(finish) })
	defer release()
	run.d.programConfigRemove = func(ctx context.Context, _ string) error {
		close(reserved)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	answer(t, run.f, programKeepQuestion(t, run, record.Key).ID, "remove")
	select {
	case <-reserved:
	case <-time.After(10 * time.Second):
		t.Fatal("Remove did not reserve the registration")
	}
	if keptServes(t, run) {
		t.Fatal("the reserved removal still serves")
	}
	if !run.d.lock(run.ctx) {
		t.Fatal("directory lock")
	}
	approval := run.d.v3.programApprovals[record.Key]
	run.d.unlock()
	if !approval.Removing || approval.Served == "" {
		t.Fatal("reservation must retain the served copy until Remove commits")
	}
	release()
	waitDirectory(t, run.d, func() bool {
		_, ok := programRegistrationNow(run.d, record.Key)
		return !ok
	})
	if keptServes(t, run) {
		t.Fatal("the removed program still serves")
	}
	if !run.d.lock(run.ctx) {
		t.Fatal("directory lock")
	}
	served := run.d.v3.programApprovals[record.Key].Served
	run.d.unlock()
	if served != "" {
		t.Fatal("Remove kept the served copy")
	}
}

//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// Owner's PC, 2026-11-12: the model host read program:changed, its review
// question had expired, and nothing raised it again: chat refused for hours
// with no question to answer. A need for a model of a changed program now
// answers program:changed and raises its review again when the one asked
// expired.
func TestChangedProgramIsAskedAgainAtTheNextNeed(t *testing.T) {
	r, run, caller, _ := registry3UnsignedPersonFixture(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	// The review reads only programs in the runtime's own folder.
	file, err := os.CreateTemp(filepath.Dir(self), "oa-changed-need-*.exe")
	if err != nil {
		t.Fatal(err)
	}
	program := file.Name()
	t.Cleanup(func() { os.Remove(program) })
	if _, err := file.Write(data); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	record := registry3RegisterUnsigned(t, r, run, caller, program)
	answer(t, run.f, programKeepQuestion(t, run, record.Key).ID, "keep")
	waitDirectory(t, run.d, func() bool {
		v, ok := programRegistrationNow(run.d, record.Key)
		return ok && v.State == registration.StateEnabled
	})
	out := run.d.editProgramRegistration(run.ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		v := s.programs[record.Key]
		v.Verification, v.VerificationReason = registration.VerificationChanged, "identity:changed"
		s.programs[record.Key] = v
		source := s.programSources[record.Key]
		source.FileKey = "the-image-before"
		s.programSources[record.Key] = source
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		t.Fatal(out)
	}
	group := &shippedProgramReview{report: func(err error) { t.Log(err) }}
	group.attach(run.k)
	r.providers.programReviewNeed = group.need

	if why := r.providers.waitingReason(run.ctx, "literal:model", "chat"); why != programChangedReason {
		t.Fatal("chat's wait", why)
	}
	asked := group.question
	if q, found, err := run.f.a.book.ApplicationRecord(asked); asked == "" || err != nil || !found || !q.Pending() || q.Key != asks.ProgramReviewKey {
		t.Fatal("the need raised no review", asked, found, err)
	}
	r.providers.waitingReason(run.ctx, "literal:model", "chat")
	if group.question != asked {
		t.Fatal("a second need asked another question", group.question, asked)
	}

	if err := run.f.a.book.SetPendingLifetime(time.Nanosecond); err != nil {
		t.Fatal(err)
	}
	if _, found, err := run.f.a.book.ApplicationRecord(asked); err != nil || found {
		t.Fatal("question did not expire", found, err)
	}
	// The need comes while the Book's clock stays past the first question's
	// lifetime, as in TestRegistryV3ExpiredKeepIsAskedAgainAtTheNextNeed.
	if why := r.providers.waitingReason(run.ctx, "literal:model", "chat"); why != programChangedReason {
		t.Fatal("chat's wait after expiry", why)
	}
	if err := run.f.a.book.SetPendingLifetime(asks.DefaultPendingLifetime); err != nil {
		t.Fatal(err)
	}
	again := group.question
	if q, found, err := run.f.a.book.ApplicationRecord(again); again == asked || err != nil || !found || !q.Pending() {
		t.Fatal("the need after expiry did not ask again", again, asked, found, err)
	}
	pending := 0
	for _, q := range run.f.a.book.List(true) {
		if q.Key == asks.ProgramReviewKey {
			pending++
		}
	}
	if pending != 1 {
		t.Fatal("pending reviews", pending)
	}
}

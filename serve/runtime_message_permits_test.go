package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestMessageBindingLegacyRetiresAnsweredOccurrenceAndExactPermits(t *testing.T) {
	for _, lostReply := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "restart"}[lostReply], func(t *testing.T) {
			run := messagePartiesFixture(t)
			f, r := run.f, run.f.a.rights
			if err := r.configureMessagePermits(run.d); err != nil {
				t.Fatal(err)
			}
			if err := r.policy.Set(f.answerer, ActionApplicationMessage, "app:receiver", true); err != nil {
				t.Fatal(err)
			}
			if err := r.policy.Set(f.cli, ActionApplicationMessage, "app:receiver", false); err != nil {
				t.Fatal(err)
			}
			if err := r.policy.Set(f.answerer, ActionApplicationMessage, "app:unrelated", true); err != nil {
				t.Fatal(err)
			}
			pending := f.a.admit(f.answerer, nil, ActionApplicationMessage, "app:receiver")
			answered := f.a.admit(f.cli, nil, ActionApplicationMessage, "app:receiver")
			if pending == "" || answered == "" {
				t.Fatal("question admission", pending, answered)
			}
			if _, _, err := f.a.book.AnswerApplication(answered, "allow"); err != nil {
				t.Fatal(err)
			}
			if lostReply {
				before := run.d.changed
				block := run.d.messageBlock
				run.d.messageBlock = func(ctx context.Context, name string) error {
					if err := block(ctx, name); err != nil {
						return err
					}
					return errors.New("lost invalidation reply")
				}
				if got := run.d.remove(run.ctx, f.panel, "receiver"); got.Outcome != wire.ApplicationOutcomeUnavailable {
					t.Fatal(got)
				}
				select {
				case <-before:
				default:
					t.Fatal("durable removal reservation was not observed")
				}
				snapshot, err := r.policy.OperatorSnapshot()
				if err != nil {
					t.Fatal(err)
				}
				policy, err := rights.LoadDecisionPolicy(filepath.Join(filepath.Dir(f.panel.Program), "decisions.json"), snapshot.Catalog)
				if err != nil {
					t.Fatal(err)
				}
				r.policy = policy
				policy.MetaHolder, policy.RefreshMetaHolder = r.refuseAnswerer, r.refreshApplicationRoles
				run.start(t)
				agent1478Attach(t, run)
				run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
				if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
					t.Fatal(err)
				}
				r.applications = run.d
				if err := r.configureMessagePermits(run.d); err != nil {
					t.Fatal(err)
				}
			}
			if got := run.d.remove(run.ctx, f.panel, "receiver"); got.Outcome != wire.ApplicationOutcomeApplied {
				t.Fatal(got)
			}
			for _, id := range []string{pending, answered} {
				if _, found, err := f.a.book.ApplicationRecord(id); err != nil || found {
					t.Fatal("old occurrence retained", id, found, err)
				}
			}
			if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:receiver"); got.Outcome == rwire.DecisionOutcomePermitted {
				t.Fatal(got)
			}
			if got := r.policy.Decide(f.cli, ActionApplicationMessage, "app:receiver"); got.Outcome != rwire.DecisionOutcomeDenied {
				t.Fatal("Block removed", got)
			}
			if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:unrelated"); got.Outcome != rwire.DecisionOutcomePermitted {
				t.Fatal("other target changed", got)
			}
			if err := r.policy.Set(f.answerer, ActionApplicationMessage, "app:receiver", true); err == nil {
				t.Fatal("absent target permit accepted")
			}
			agent1478Register(t, run, "receiver", f.lemonade.Program)
			if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:receiver"); got.Outcome == rwire.DecisionOutcomePermitted {
				t.Fatal("permit followed recreated receiver", got)
			}
		})
	}
}

func TestMessageBindingLegacyInitialPublicationInvalidatesRetainedPermit(t *testing.T) {
	run := messagePartiesFixture(t)
	f, r := run.f, run.f.a.rights
	// Retained state may predate provider admission and name an absent app.
	if err := r.policy.Set(f.answerer, ActionApplicationMessage, "app:fresh", true); err != nil {
		t.Fatal(err)
	}
	if err := r.configureMessagePermits(run.d); err != nil {
		t.Fatal(err)
	}
	block := run.d.messageBlock
	failed := true
	run.d.messageBlock = func(ctx context.Context, name string) error {
		if err := block(ctx, name); err != nil {
			return err
		}
		if failed {
			return errors.New("lost initial publication reply")
		}
		return nil
	}
	descriptor := wire.ApplicationDescriptor{Name: "fresh", Program: f.lemonade.Program, Title: "New receiver"}
	before := run.d.changed
	if got := run.d.register(run.ctx, f.panel, descriptor); got.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal(got)
	}
	select {
	case <-before:
	default:
		t.Fatal("durable publication reservation was not observed")
	}
	if mark := run.d.unconfirmed["fresh"]; !mark.Removing {
		t.Fatal("no durable publication reservation")
	}
	if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:fresh"); got.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("old permit inherited", got)
	}
	failed = false
	if got := run.d.register(run.ctx, f.panel, descriptor); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	agent1478Set(t, run, "fresh", "", true)
	answer(t, f, agent1478Question(t, run, "fresh").ID, "keep")
	agent1478Wait(t, run, "fresh", "enrolled")
	if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:fresh"); got.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("old permit survived enrollment", got)
	}
}

func TestMessageBindingConcurrentPreparedPermitCannotFollowRemoval(t *testing.T) {
	run := messagePartiesFixture(t)
	f, r := run.f, run.f.a.rights
	if err := r.configureMessagePermits(run.d); err != nil {
		t.Fatal(err)
	}
	prepared, release := make(chan struct{}), make(chan struct{})
	oldRefresh := r.policy.RefreshMetaHolder
	defer func() { r.policy.RefreshMetaHolder = oldRefresh }()
	r.policy.RefreshMetaHolder = func(ctx context.Context) error {
		if err := r.refreshApplicationRoles(ctx); err != nil {
			return err
		}
		close(prepared)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan error, 1)
	go func() { done <- r.policy.Set(f.answerer, ActionApplicationMessage, "app:receiver", true) }()
	<-prepared
	if got := run.d.remove(run.ctx, f.panel, "receiver"); got.Outcome != wire.ApplicationOutcomeApplied {
		close(release)
		t.Fatal(got)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("prepared permit followed removed receiver")
	}
	if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:receiver"); got.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal(got)
	}
}

func TestMessageBindingPersonAllowPreparesBeforeBookAndCannotReplayAfterRemoval(t *testing.T) {
	run := messagePartiesFixture(t)
	f, r := run.f, run.f.a.rights
	if err := r.configureMessagePermits(run.d); err != nil {
		t.Fatal(err)
	}
	id := f.a.admit(f.answerer, nil, ActionApplicationMessage, "app:receiver")
	if id == "" {
		t.Fatal("question unavailable")
	}
	if _, _, err := f.a.book.AnswerApplication(id, "allow"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	out, err := f.a.applyOperatorAllow(run.ctx, f.panel, snapshot.Revision, id, func() error { return nil })
	if err != nil || out.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(out, err)
	}
	if got := r.policy.Decide(f.answerer, ActionApplicationMessage, "app:receiver"); got.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal(got)
	}
	if got := run.d.remove(run.ctx, f.panel, "receiver"); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	snapshot, err = r.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := f.a.applyOperatorAllow(run.ctx, f.panel, snapshot.Revision, id, func() error { return nil }); err != nil || out.Outcome == rwire.PolicyEditOutcomeApplied {
		t.Fatal("retired Allow replayed", out, err)
	}
}

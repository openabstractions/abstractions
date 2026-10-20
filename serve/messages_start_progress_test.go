package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func messagesStartProgressFixture(t *testing.T) (*applicationsHost, string, *messageRecord, *atomic.Int32, *bool) {
	t.Helper()
	return messagesStartProgressFixtureFor(t, "ctx")
}

// questionContext is the queued question's receiver context; the recipe's own
// context stays "ctx".
func messagesStartProgressFixtureFor(t *testing.T, questionContext string, cold ...*msg.ChatLaunch) (*applicationsHost, string, *messageRecord, *atomic.Int32, *bool) {
	t.Helper()
	if len(cold) > 0 {
		var binding *msg.ChatBinding
		if cold[0] != nil {
			binding = &msg.ChatBinding{ChatID: cold[0].Session, Launch: cold[0]}
		}
		return messagesStartProgressFixtureWith(t, questionContext, "claude-v1", binding)
	}
	return messagesStartProgressFixtureWith(t, questionContext, "", nil)
}

// messagesStartProgressFixtureWith registers a recipe of the named kind, when
// one is named, and addresses the question to the retained chat binding, when
// one is given.
func messagesStartProgressFixtureWith(t *testing.T, questionContext, recipe string, chat *msg.ChatBinding) (*applicationsHost, string, *messageRecord, *atomic.Int32, *bool) {
	t.Helper()
	run := agent1478Fixture(t)
	descriptor := wire.ApplicationDescriptor{Name: "receiver", Title: "Receiver", Program: run.f.answerer.Program, Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"start", "--profile", "fixture", "--inbox-context", "ctx"}, Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "fixture/messages@1"}, ReadinessTimeoutMs: 100}}
	if recipe != "" {
		folder := t.TempDir()
		profile := startprofile.Profile{Name: recipe, Context: "ctx", Folder: folder, StateDir: folder, CLIFileID: "inert-fixture", Model: "inert-fixture", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 1}
		descriptor.Activation.Arguments = profile.Arguments()
	}
	if got := run.d.register(run.ctx, run.f.panel, descriptor); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, "sender", program)
	for _, name := range []string{"sender", "receiver"} {
		agent1478Set(t, run, name, "", true)
		q := agent1478Question(t, run, name)
		answer(t, run.f, q.ID, "keep")
		agent1478Wait(t, run, name, "enrolled")
	}
	valid := true
	run.d.messageStartProfile = func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		return applicationMessageStartRequest{Session: "fixture", Context: "ctx", ProfileFingerprint: "approved-folder-and-budget", Validate: func(context.Context) error {
			if !valid {
				return errors.New("folder or profile revoked")
			}
			return nil
		}}, nil
	}
	policy := run.f.a.rights
	for _, action := range []string{ActionApplicationMessage, grants.ApplicationMessageStartAction} {
		if err := policy.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	actor := messageActor{Account: policy.owner, Program: program, Application: "sender", Context: "source"}
	subject := rights.Subject{Account: actor.Account, Program: actor.Program}
	if err := policy.policy.Set(subject, ActionApplicationMessage, "app:receiver", true); err != nil {
		t.Fatal(err)
	}
	s, err := openMessageStore(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if chat != nil {
		questionContext, err = s.contextForChat(run.ctx, run.d.owner, descriptor.Program, chat.ChatID, chat)
		if err != nil {
			t.Fatal(err)
		}
	}
	request := msg.SendRequest{To: "app:receiver", Context: questionContext, Kind: msg.KindQuestion, Subject: "question", Body: "private body", IdempotencyKey: "question", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()}
	if got := s.accept(run.ctx, actor, descriptor.Program, request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	key := s.key(actor, request.IdempotencyKey)
	r, err := s.readRecord(run.ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	calls := &atomic.Int32{}
	run.d.launchMessageStart = func(context.Context, applicationMessageStartRequest, func(context.Context) error) error {
		calls.Add(1)
		return nil
	}
	h := &applicationsHost{directory: run.d, messages: s, messageRights: policy}
	return h, key, r, calls, &valid
}

func TestMessagesStartProgressRechecksCanonicalAttempt(t *testing.T) {
	for _, field := range []string{"attempt", "resource", "state"} {
		t.Run(field, func(t *testing.T) {
			h, key, r, calls, _ := messagesStartProgressFixture(t)
			allowMessageStartFixture(t, h, key, r)
			profile := h.directory.messageStartProfile
			changed := false
			h.directory.messageStartProfile = func(ctx context.Context, d wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
				target, err := profile(ctx, d)
				if err != nil {
					return target, err
				}
				check := target.Validate
				target.Validate = func(ctx context.Context) error {
					if err := check(ctx); err != nil {
						return err
					}
					if !changed {
						return h.messages.updateMessageNotice(ctx, key, r.Token, func(record *messageRecord) {
							if record.Start == nil || record.Start.State != "unknown" {
								return
							}
							changed = true
							switch field {
							case "attempt":
								record.Start.Attempt = strings.Repeat("b", 64)
							case "resource":
								record.Start.Resource = "app:receiver/start:" + strings.Repeat("b", 64)
							case "state":
								record.Start.State = "refused"
							}
						})
					}
					return nil
				}
				return target, nil
			}
			if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
				t.Fatal(err)
			}
			if !changed || calls.Load() != 0 {
				t.Fatal("changed canonical start reached dispatch", field, changed, calls.Load())
			}
		})
	}
}

func allowMessageStartFixture(t *testing.T, h *applicationsHost, key string, r *messageRecord) string {
	t.Helper()
	resource := ""
	h.messageRights.firstUse = func(subject rights.Subject, _ func() asks.Publisher, action, target string) string {
		if action != grants.ApplicationMessageStartAction {
			t.Fatal(action)
		}
		resource = target
		return "pending-native-person-question"
	}
	if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
		t.Fatal(err)
	}
	if resource == "" {
		t.Fatal("no startup consent")
	}
	if err := h.messageRights.policy.Set(rights.Subject{Account: r.Actor.Account, Program: r.Actor.Program}, grants.ApplicationMessageStartAction, resource, true); err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestMessagesStartProgressAfterPermissionAndRestartNeverDuplicatesUnknown(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixture(t)
	allowMessageStartFixture(t, h, key, r)
	if calls.Load() != 0 {
		t.Fatal("permission request launched")
	}
	if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("missing start", calls.Load())
	}
	current, err := h.messages.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if current.Start == nil || current.Start.State != "unknown" {
		t.Fatalf("%+v", current.Start)
	}
	if err := h.progressMessageStartSettled(context.Background(), key, current); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("uncertain start duplicated")
	}
}

func TestMessagesStartProgressRechecksQuestionAndConsent(t *testing.T) {
	for _, change := range []string{"denied", "profile", "cancel", "expire", "direction-revoked"} {
		t.Run(change, func(t *testing.T) {
			h, key, r, calls, valid := messagesStartProgressFixture(t)
			resource := allowMessageStartFixture(t, h, key, r)
			subject := rights.Subject{Account: r.Actor.Account, Program: r.Actor.Program}
			switch change {
			case "denied":
				if err := h.messageRights.policy.Set(subject, grants.ApplicationMessageStartAction, resource, false); err != nil {
					t.Fatal(err)
				}
			case "profile":
				*valid = false
			case "cancel":
				if err := h.messages.transitionState(context.Background(), key, r, "cancel"); err != nil {
					t.Fatal(err)
				}
			case "expire":
				h.messages.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
			case "direction-revoked":
				if err := h.messageRights.policy.Set(subject, ActionApplicationMessage, "app:receiver", false); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 {
				t.Fatal("invalidated startup launched", calls.Load())
			}
		})
	}
}

func TestMessagesStartProgressNativePersonFirstUseContinuesAfterRPC(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixture(t)
	a := h.directory.agentQuestions.a
	id := ""
	h.messageRights.firstUse = func(subject rights.Subject, publisher func() asks.Publisher, action, resource string) string {
		id = a.admit(subject, publisher, action, resource)
		return id
	}
	if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
		t.Fatal(err)
	}
	if id == "" || calls.Load() != 0 {
		t.Fatal("native startup question unavailable", id, calls.Load())
	}
	record, found, err := a.book.ApplicationRecord(id)
	if err != nil || !found || !record.Pending() {
		t.Fatal("native consent", record, found, err)
	}
	if _, outcome, err := a.book.AnswerApplication(id, "allow"); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	snapshot, err := a.rights.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.applyOperatorAllow(context.Background(), a.rights.self(), snapshot.Revision, id, func() error { return nil }); err != nil || out.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal(out, err)
	}
	if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("person allow failed to progress queued question", calls.Load())
	}
}

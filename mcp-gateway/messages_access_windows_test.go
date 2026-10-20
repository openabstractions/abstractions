//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msgwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

var messagesAccessEndpoints atomic.Uint64

// This fixture uses the real native permission receiver, generated client,
// Book replay/coalescing and Rights receipt owner. Native-image classification
// is covered by serve's fresh bootstrap tests; these captures are inert inputs.
func newMessageAccessFixture(t *testing.T, roots ...string) (*asks.Book, *rights.DecisionPolicy, *askclient.PermissionClient, *askclient.PermissionOperator, *atomic.Int64) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	if len(roots) != 0 {
		root = roots[0]
	}
	book, err := asks.LoadApplicationBook(filepath.Join(root, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(root, "rights.json"), []string{grants.ApplicationMessageAction})
	if err != nil {
		t.Fatal(err)
	}
	policy.Definitions, err = grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	var captureRevision atomic.Int64
	captureRevision.Store(1)
	endpoint := listen.Endpoint(fmt.Sprintf("messages-access-key-%d-%d", os.Getpid(), messagesAccessEndpoints.Add(1)))
	host, err := asksservice.Listen(endpoint, book)
	if err != nil {
		t.Fatal(err)
	}
	err = host.EnablePermission(func(ctx context.Context, peer *identity.Peer, intent askswire.PermissionIntent) (asksservice.PreparedPermission, error) {
		subject, err := rightsclient.SubjectFromPeer(peer)
		if err != nil {
			return asksservice.PreparedPermission{}, err
		}
		name, err := grants.DecodeNamedApplicationTarget(intent.Selection.Selector.Target)
		if err != nil {
			return asksservice.PreparedPermission{}, err
		}
		p, err := grants.AgentMessagePermission(subject, applications.CapturedAgentApplication{Name: "sender", Program: subject.Program, FileKey: "fixture-sender-key", EnrollmentRevision: "sender-1"}, []applications.CapturedAgentApplication{{Name: name, Program: filepath.Join(root, "receiver.exe"), FileKey: "fixture-receiver-key", EnrollmentRevision: fmt.Sprint(captureRevision.Load())}})
		if err != nil {
			return asksservice.PreparedPermission{}, err
		}
		projected, _, _ := grants.AgentMessageLegacyEffect(p.Effect)
		decision := policy.CheckLegacyPermissionEffectContext(ctx, projected, "")
		outcome := askswire.PermissionOutcomePending
		if len(decision.Outcomes) != 1 {
			return asksservice.PreparedPermission{}, errors.New("missing fixture decision")
		}
		switch decision.Outcomes[0] {
		case rwire.DecisionOutcomePermitted:
			outcome = askswire.PermissionOutcomePermitted
		case rwire.DecisionOutcomeDenied:
			outcome = askswire.PermissionOutcomeDenied
		}
		return asksservice.PreparedPermission{Permission: p, Outcome: outcome, Revision: decision.Revision}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = host.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- host.Serve(ctx) }()
	t.Cleanup(func() {
		host.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("permission fixture did not drain")
		}
	})
	client := askclient.NewPermission(endpoint)
	operator := askclient.NewPermissionOperator(endpoint)
	return book, policy, client, operator, &captureRevision
}

func TestMessagesAccessSameBackendReconciliationAndFreshReview(t *testing.T) {
	ctx := context.Background()
	book, policy, client, operator, captureRevision := newMessageAccessFixture(t)
	lostReply := true
	lostID := ""
	backend := &messagesBackend{contextName: "same-native-context", requestAccess: func(ctx context.Context, intent askswire.PermissionIntent) (askswire.PermissionObservation, error) {
		out, err := client.RequestContext(ctx, intent)
		if err == nil && lostReply && out.Answer != nil {
			lostReply = false
			lostID = out.Answer.ID
			return askswire.PermissionObservation{}, errors.New("fixture lost reply after durable permission admission")
		}
		return out, err
	}}
	request := func(key, receiver string) askswire.PermissionObservation {
		t.Helper()
		out, err := backend.MessagesRequestAccess(ctx, gateway.MessagesAccessInput{Receiver: receiver, IdempotencyKey: key})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if _, err := backend.MessagesRequestAccess(ctx, gateway.MessagesAccessInput{Receiver: "receiver", IdempotencyKey: "first"}); err == nil || lostID == "" {
		t.Fatal("lost-reply fixture did not reach Book", err)
	}
	first := request("first", "receiver")
	if first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first)
	}
	if first.Answer.ID != lostID {
		t.Fatal("unknown reply reconciliation created another occurrence", first, lostID)
	}
	if pending, err := book.PendingTypedPermissions(); err != nil || len(pending) != 1 {
		t.Fatal("lost reply duplicated pending consent", len(pending), err)
	}
	for _, key := range []string{"first", "another-explicit-key"} {
		out := request(key, "receiver")
		if out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil || out.Answer.ID != first.Answer.ID {
			t.Fatal("pending/reply reconciliation duplicated consent", out)
		}
	}
	if out := request("first", "different-receiver"); out.Outcome != askswire.PermissionOutcomeConflict {
		t.Fatal("changed intent reused key", out)
	}
	if _, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	record, found, err := book.ApplicationRecord(first.Answer.ID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	projected, _, ok := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
	if !ok {
		t.Fatal("projection")
	}
	revision, _, err := policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := policy.ApplyLegacyPermissionEffect(ctx, revision, projected, rights.RuleEdit{By: projected.Subject, Exact: true}, record.ID, []string{record.ID}, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	undo, err := policy.UndoLegacyPermissionEffect(ctx, edit.Revision, projected, nil, record.ID, func() error { return nil })
	if err != nil || undo.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(undo, err)
	}
	// The undone Allow stays recorded but is not in force, so it never reads
	// as an answer the agent could act on.
	if replay := request("first", "receiver"); replay.Outcome != askswire.PermissionOutcomeUnavailable || replay.Answer != nil {
		t.Fatal("undone Allow reported as an answer", replay)
	}
	if pending, err := book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
		t.Fatal("same-key replay asked again", pending, err)
	}
	fresh := request("after-undo", "receiver")
	if fresh.Outcome != askswire.PermissionOutcomePending || fresh.Answer == nil || fresh.Answer.ID == first.Answer.ID {
		t.Fatal("fresh explicit review replayed old Allow", fresh)
	}
	if decision := policy.Decide(projected.Subject, grants.ApplicationMessageAction, "app:receiver"); decision.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("request regranted without person", decision)
	}
	if _, err = operator.AnswerPermissionIfPendingContext(ctx, fresh.Answer.ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	if paced := request("fresh-refused-key", "receiver"); paced.Outcome != askswire.PermissionOutcomeAnswered || paced.Answer == nil || paced.Answer.ID != fresh.Answer.ID {
		t.Fatal("refusal pacing bypassed", paced)
	}
	captureRevision.Store(2)
	if conflict := request("after-undo", "receiver"); conflict.Outcome != askswire.PermissionOutcomeConflict {
		t.Fatal("same key changed captured intent", conflict)
	}
	changed := request("new-native-enrollment", "receiver")
	if changed.Outcome != askswire.PermissionOutcomePending || changed.Answer == nil || changed.Answer.ID == fresh.Answer.ID {
		t.Fatal("new capture could not request fresh review", changed)
	}
	if replay := request("new-native-enrollment", "receiver"); replay.Outcome != askswire.PermissionOutcomePending || replay.Answer == nil || replay.Answer.ID != changed.Answer.ID {
		t.Fatal("changed capture pending duplicate", replay)
	}
}

type countedMessagePermission struct {
	*askclient.PermissionClient
	requests atomic.Int64
	observes atomic.Int64
}

func (p *countedMessagePermission) RequestContext(ctx context.Context, intent askswire.PermissionIntent) (askswire.PermissionObservation, error) {
	p.requests.Add(1)
	return p.PermissionClient.RequestContext(ctx, intent)
}
func (p *countedMessagePermission) ObserveContext(ctx context.Context, key string, wait int64) (askswire.PermissionObservation, error) {
	p.observes.Add(1)
	return p.PermissionClient.ObserveContext(ctx, key, wait)
}

func TestMessagesAccessWaitsForDelayedPersonAndCurrentEffect(t *testing.T) {
	for _, mode := range []string{"allow", "allow-without-effect", "revoke", "refuse", "cancel", "deadline", "check-deadline", "check-cancel"} {
		t.Run(mode, func(t *testing.T) {
			book, policy, client, operator, _ := newMessageAccessFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			counted := &countedMessagePermission{PermissionClient: client}
			var checked atomic.Int64
			var questionID atomic.Value
			initialCheck := make(chan struct{})
			effectReady := make(chan struct{})
			backend := &messagesBackend{contextName: "active-native-call", permission: func(context.Context) (messagePermissionClient, error) { return counted, nil },
				checkAccess: func(ctx context.Context, receiver string) (askswire.PermissionObservation, error) {
					check := checked.Add(1)
					if mode == "check-deadline" || mode == "check-cancel" {
						if check == 1 {
							close(initialCheck)
						}
						<-ctx.Done()
						return askswire.PermissionObservation{}, ctx.Err()
					}
					if mode == "revoke" && check > 1 {
						select {
						case <-effectReady:
						case <-ctx.Done():
							return askswire.PermissionObservation{}, ctx.Err()
						}
					}
					// Read the actual Rights owner after the separate person answer
					// and rule-effect commits; no answer flag supplies permission.
					id := questionID.Load()
					if id == nil {
						return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}, nil
					}
					record, found, err := book.ApplicationRecord(id.(string))
					if err != nil || !found {
						return askswire.PermissionObservation{}, fmt.Errorf("fixture record: %v", err)
					}
					projected, _, ok := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
					if !ok {
						return askswire.PermissionObservation{}, errors.New("fixture projection")
					}
					decision := policy.CheckLegacyPermissionEffectContext(ctx, projected, "")
					if check == 1 {
						close(initialCheck)
					}
					if len(decision.Outcomes) == 1 && decision.Outcomes[0] == rwire.DecisionOutcomePermitted {
						return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePermitted, PolicyRevision: decision.Revision}, nil
					}
					return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
				}}
			type result struct {
				out askswire.PermissionObservation
				err error
			}
			finished := make(chan result, 1)
			go func() {
				var out askswire.PermissionObservation
				var err error
				if mode == "allow" {
					// Exercise the actual advertised MCP handler, its active-call
					// budget and friendly receiver schema before native Permission IPC.
					st, ct := mcp.NewInMemoryTransports()
					server, e := gateway.NewServer(backend).Connect(ctx, st, nil)
					if e != nil {
						finished <- result{err: e}
						return
					}
					defer server.Close()
					client, e := mcp.NewClient(&mcp.Implementation{Name: "inert-person-delay", Version: "test"}, nil).Connect(ctx, ct, nil)
					if e != nil {
						finished <- result{err: e}
						return
					}
					defer client.Close()
					reply, e := client.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_request_access", Arguments: map[string]any{"receiver": "Claude", "idempotency_key": "same-explicit-operation"}})
					if e != nil {
						err = e
					} else if reply.IsError || len(reply.Content) != 1 {
						err = errors.New("MCP request failed")
					} else {
						decoded, e := askswire.DecodePermissionObservation([]byte(reply.Content[0].(*mcp.TextContent).Text))
						err = e
						if e == nil {
							out = *decoded
						}
					}
				} else {
					out, err = backend.MessagesRequestAccess(ctx, gateway.MessagesAccessInput{Receiver: "Claude", IdempotencyKey: "same-explicit-operation"})
				}
				finished <- result{out, err}
			}()
			var record asks.Record
			for {
				pending, err := book.PendingTypedPermissions()
				if err != nil {
					t.Fatal(err)
				}
				if len(pending) == 1 {
					record = pending[0]
					questionID.Store(record.ID)
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("request not admitted")
				case <-time.After(5 * time.Millisecond):
				}
			}
			select {
			case early := <-finished:
				t.Fatal("active call returned before person decision", early)
			case <-time.After(30 * time.Millisecond):
			}
			if mode == "cancel" {
				cancel()
			}
			if mode != "cancel" && mode != "deadline" {
				option := "allow"
				if mode == "refuse" {
					option = "refuse"
				}
				if _, err := operator.AnswerPermissionIfPendingContext(ctx, record.ID, option); err != nil {
					t.Fatal(err)
				}
				if mode == "check-cancel" {
					select {
					case <-initialCheck:
						cancel()
					case <-ctx.Done():
						t.Fatal("confirmation not reached")
					}
				}
				if mode == "allow" || mode == "revoke" {
					// Ensure the active call observes the answer before the effect
					// owner commits its separate result.
					select {
					case <-initialCheck:
					case <-ctx.Done():
						t.Fatal("answer not observed")
					}
					select {
					case early := <-finished:
						t.Fatal("Allow alone claimed permission", early)
					default:
					}
					projected, _, _ := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
					revision, _, _ := policy.SelectorSnapshotContext(ctx)
					edit, err := policy.ApplyLegacyPermissionEffect(ctx, revision, projected, rights.RuleEdit{By: projected.Subject, Exact: true}, record.ID, []string{record.ID}, func() error { return nil })
					if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
						t.Fatal(edit, err)
					}
					if mode == "revoke" {
						undo, err := policy.UndoLegacyPermissionEffect(ctx, edit.Revision, projected, nil, record.ID, func() error { return nil })
						if err != nil || undo.Outcome != rwire.PolicyEditOutcomeApplied {
							t.Fatal(undo, err)
						}
						close(effectReady)
					}
				}
			}
			got := <-finished
			if counted.requests.Load() != 1 || counted.observes.Load() == 0 {
				t.Fatal("re-admission or no native observation", counted.requests.Load(), counted.observes.Load())
			}
			if mode == "cancel" || mode == "check-cancel" {
				if !errors.Is(got.err, context.Canceled) {
					t.Fatal(got)
				}
				return
			}
			if got.err != nil {
				t.Fatal(got.err)
			}
			switch mode {
			case "allow":
				if got.out.Outcome != askswire.PermissionOutcomePermitted || got.out.PolicyRevision == "" {
					t.Fatal(got.out)
				}
			case "refuse":
				if got.out.Outcome != askswire.PermissionOutcomeAnswered || got.out.Answer == nil || got.out.Answer.Option != "refuse" {
					t.Fatal(got.out)
				}
			case "deadline":
				if got.out.Outcome != askswire.PermissionOutcomePending {
					t.Fatal(got.out)
				}
			default:
				if got.out.Outcome != askswire.PermissionOutcomeUnavailable || got.out.Answer != nil {
					t.Fatal("unapplied/revoked effect reported Allow", got.out)
				}
			}
		})
	}
}

func TestMessagesAccessAlreadyPermittedStillConfirmsLiveInbox(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(fmt.Sprintf("live-%t", live), func(t *testing.T) {
			book, policy, client, operator, _ := newMessageAccessFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			prime := &messagesBackend{contextName: "prior-consent", requestAccess: client.RequestContext}
			first, err := prime.MessagesRequestAccess(ctx, gateway.MessagesAccessInput{Receiver: "Claude", IdempotencyKey: "prior-person-consent"})
			if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
				t.Fatal(first, err)
			}
			if _, err = operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil {
				t.Fatal(err)
			}
			record, found, err := book.ApplicationRecord(first.Answer.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			projected, _, _ := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
			revision, _, _ := policy.SelectorSnapshotContext(ctx)
			edit, err := policy.ApplyLegacyPermissionEffect(ctx, revision, projected, rights.RuleEdit{By: projected.Subject, Exact: true}, record.ID, []string{record.ID}, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal(edit, err)
			}
			p := &accessInboxProbe{messagesProbe: &messagesProbe{}, page: msgwire.InboxPage{Outcome: msgwire.OutcomePage, Inboxes: []msgwire.Inbox{}}}
			if live {
				p.page.Inboxes = []msgwire.Inbox{{Program: "app:oa-claude-integration", Context: "opaque-live-context", Revision: "r"}}
			}
			counted := &countedMessagePermission{PermissionClient: client}
			b := &messagesBackend{contextName: "new-active-call", permission: func(context.Context) (messagePermissionClient, error) { return counted, nil }, native: func(context.Context) (nativeMessages, error) {
				return msgwire.NewMessagesClient(&msgwire.MessagesDispatcher{Handler: p}), nil
			}}
			b.checkAccess = func(ctx context.Context, receiver string) (askswire.PermissionObservation, error) {
				return b.confirmMessageAccess(ctx, receiver, policy.Decide(projected.Subject, grants.ApplicationMessageAction, "app:"+receiver))
			}
			wait, stop := context.WithTimeout(ctx, 150*time.Millisecond)
			defer stop()
			b.application = "oa-codex-integration"
			got, notice, err := b.MessagesRequestAccessReport(wait, gateway.MessagesAccessInput{Receiver: "Claude", IdempotencyKey: "new-current-permission"})
			if err != nil || counted.requests.Load() != 1 || counted.observes.Load() != 0 || p.calls == 0 {
				t.Fatal(got, err, counted.requests.Load(), counted.observes.Load(), p.calls)
			}
			if got.Outcome != askswire.PermissionOutcomePermitted || got.PolicyRevision == "" {
				t.Fatal(got)
			}
			// A saved permission whose receiver is not live is still saved;
			// the person learns the receiver reads it when its chat is open.
			if live && notice != "" || !live && notice != "Codex can send messages to Claude. Claude reads them when its chat is open." {
				t.Fatal("saved-permission notice", live, notice)
			}
			pending, err := book.PendingTypedPermissions()
			if err != nil || len(pending) != 0 {
				t.Fatal("new admission while already permitted", pending, err)
			}
		})
	}
}

type durableMessagePermissionProbe struct {
	*accessInboxProbe
	permitted func(string) bool
	live      bool
}

func (p *durableMessagePermissionProbe) Prepare(_, _ string, in msgwire.PrepareRequest) (msgwire.PrepareResult, error) {
	if !p.permitted(in.Receiver) {
		return msgwire.PrepareResult{Outcome: msgwire.OutcomeForbidden, Reason: "access:oa-claude-integration"}, nil
	}
	if !p.live {
		return msgwire.PrepareResult{Outcome: msgwire.OutcomeUnavailable, Reason: "receiver_lapsed"}, nil
	}
	return msgwire.PrepareResult{Outcome: msgwire.OutcomePage, SendRequest: &msgwire.SendRequest{
		To: in.Receiver, Context: "kept-receiver", Kind: msgwire.KindNote, Body: in.Body,
		IdempotencyKey: "native-prepared", ExpiresUnixMs: time.Now().Add(time.Minute).UnixMilli()}}, nil
}

func TestMessagesAccessAllowPersistsAndPrepareUsesSameTarget(t *testing.T) {
	for _, tc := range []struct{ receiver, name string }{
		{"app:opencode", "opencode"},
		{"OpenCode", "oa-opencode-integration"},
		{"app:oa-claude-integration", "oa-claude-integration"},
	} {
		t.Run(tc.receiver, func(t *testing.T) {
			program := "app:" + tc.name
			root := t.TempDir()
			book, policy, permission, person, _ := newMessageAccessFixture(t, root)
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			counted := &countedMessagePermission{PermissionClient: permission}
			prime := &messagesBackend{contextName: "opencode-chat", requestAccess: permission.RequestContext}
			// Exercise the real post-admission budget once, with no answer
			// arriving until the initial caller has already received pending.
			if tc.receiver == "OpenCode" {
				prime.permission = func(context.Context) (messagePermissionClient, error) { return counted, nil }
			}
			in := gateway.MessagesAccessInput{Receiver: tc.receiver, IdempotencyKey: "person-message-claude"}
			started := time.Now()
			first, err := prime.MessagesRequestAccess(ctx, in)
			if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
				t.Fatal(first, err)
			}
			if tc.receiver == "OpenCode" && (time.Since(started) < 3*time.Second ||
				ctx.Err() != nil || counted.requests.Load() != 1 || counted.observes.Load() == 0) {
				t.Fatal("initial access did not return pending within its own post-admission wait", first)
			}
			if _, err = person.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil {
				t.Fatal(err)
			}
			record, found, err := book.ApplicationRecord(first.Answer.ID)
			if err != nil || !found {
				t.Fatal(found, err)
			}
			effect, _, ok := grants.AgentMessageLegacyEffect(record.TypedPermission.Effect)
			if !ok {
				t.Fatal("directional message effect missing")
			}
			revision, _, _ := policy.SelectorSnapshotContext(ctx)
			edit, err := policy.ApplyLegacyPermissionEffect(ctx, revision, effect, rights.RuleEdit{By: effect.Subject, Exact: true}, record.ID, []string{record.ID}, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal(edit, err)
			}
			// Reload the durable owner: an Allow answer or an in-memory flag cannot
			// supply the permission used by the subsequent generated Prepare adapter.
			persisted, err := rights.LoadDecisionPolicy(filepath.Join(root, "rights.json"), []string{grants.ApplicationMessageAction})
			if err != nil {
				t.Fatal(err)
			}
			probe := &durableMessagePermissionProbe{accessInboxProbe: &accessInboxProbe{messagesProbe: &messagesProbe{},
				page: msgwire.InboxPage{Outcome: msgwire.OutcomePage, Inboxes: []msgwire.Inbox{{Program: program, Context: "kept-receiver", Revision: "r"}}}}, live: true}
			probe.permitted = func(target string) bool {
				return persisted.Decide(effect.Subject, grants.ApplicationMessageAction, target).Outcome == rwire.DecisionOutcomePermitted
			}
			backend := &messagesBackend{contextName: "opencode-chat", instance: "kept-open-code", permission: func(context.Context) (messagePermissionClient, error) { return counted, nil },
				native: func(context.Context) (nativeMessages, error) {
					return msgwire.NewMessagesClient(&msgwire.MessagesDispatcher{Handler: probe}), nil
				}}
			backend.checkAccess = func(ctx context.Context, receiver string) (askswire.PermissionObservation, error) {
				resolved, _, err := backend.resolveMessageReceiver(ctx, receiver, "")
				if err != nil {
					return askswire.PermissionObservation{}, err
				}
				if "app:"+resolved != program {
					t.Errorf("confirmation changed the selected program: %s", resolved)
				}
				return backend.confirmMessageAccess(ctx, resolved, persisted.Decide(effect.Subject, grants.ApplicationMessageAction, "app:"+resolved))
			}
			access, err := backend.MessagesRequestAccess(ctx, in)
			if err != nil || access.Outcome != askswire.PermissionOutcomePermitted || access.PolicyRevision == "" {
				t.Fatal(access, err)
			}
			history, _, err := book.ApplicationHistory()
			if err != nil || len(history) != 1 || history[0].ID != first.Answer.ID {
				t.Fatal("same-key continuation changed the question", history, err)
			}
			if tc.receiver == "OpenCode" && counted.requests.Load() != 2 {
				t.Fatal("each explicit access call should send one Request", counted.requests.Load())
			}
			prepared, err := backend.MessagesPrepare(ctx, gateway.MessagesPrepareInput{Receiver: tc.receiver, Body: "hello"})
			if err != nil || prepared.Outcome != "prepared" || prepared.SendArguments == nil || prepared.SendArguments.To != program {
				t.Fatal(prepared, err)
			}
			probe.live = false
			lapsed, err := backend.MessagesPrepare(ctx, gateway.MessagesPrepareInput{Receiver: tc.receiver, Body: "hello"})
			if err != nil || lapsed.Outcome != "receiver_lapsed" || !probe.permitted(program) {
				t.Fatal("lapsed receiver changed durable consent", lapsed, err)
			}
		})
	}
}

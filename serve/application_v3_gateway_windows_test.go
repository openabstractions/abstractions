package main

import (
	"context"
	"encoding/json"
	"errors"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The fixture uses real native Program/process evidence, generated native RPC,
// the actual Rights policy and durable receipt store. The declaration/held-image
// seam is synthetic; these tests make no signed executable or GUI claim.
type applicationV3GatewayFixture struct {
	g *applicationV3Gateway
	c *applications.ApplicationsClient
	s *ownedApplicationSession
	// server is the application's end of the owned pipe: a test writes the
	// application's notifications there between calls.
	server        net.Conn
	subject       rwire.Subject
	manifest      *registrationwire.Manifest
	description   *appmodel.Description
	instance      string
	announceKey   string
	effects       atomic.Int32
	mu            sync.Mutex
	answer        func(string, json.RawMessage) []byte
	failAuthority atomic.Bool
	records       []appmodel.CallRecord
	raw           func([]byte) ([]byte, error)
	state         string
	// client is a caller of this host whose calls wait at most timeout.
	client func(timeout time.Duration) *applications.ApplicationsClient
}

func newApplicationV3GatewayFixture(t *testing.T) *applicationV3GatewayFixture {
	t.Helper()
	peer, recheck := applicationV3NativePeer(t)
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	actions := append([]string(nil), appmodel.CommandActions...)
	actions = append(actions, ActionApplicationRead, ActionApplicationAnnounce, ActionApplicationActivate)
	policy, err := rights.LoadDecisionPolicy(filepath.Join(state, "rights.json"), actions)
	if err != nil {
		t.Fatal(err)
	}
	d, err := openApplications(state, subject.Account, func(context.Context, rwire.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if out := d.register(context.Background(), subject, legacy.ApplicationDescriptor{Name: "fixture", Title: "Fixture", Program: subject.Program}); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	store, err := openApplicationV3Receipts(state)
	if err != nil {
		t.Fatal(err)
	}
	f := &applicationV3GatewayFixture{subject: subject, state: "enabled"}
	f.manifest = &registrationwire.Manifest{Version: "applications@3", Name: "fixture", Title: "Fixture", Host: registrationwire.HostRecipe{ReadinessDeadlineMs: 1000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1", Commands: []appmodel.DeclaredCommand{{Name: "note.read", Class: appmodel.CommandClassRead}}}}
	f.description = &appmodel.Description{Version: "appmodel@1", Kinds: []appmodel.Kind{{Name: "note", Schema: `{}`, Fields: []appmodel.KindField{}}}, Commands: []appmodel.CommandSpec{{Name: "note.read", Class: appmodel.CommandClassRead, Title: "Read", InputSchema: `{}`, OutputSchema: `{}`, Targets: []string{}}}, Extensions: []string{}, Bounds: appmodel.ContextBounds{SnapshotBytes: 4096, ChangeBytes: 1024, RetainedRevisions: 16, PendingProposals: 1, CallsInFlight: 1}}
	g := &applicationV3Gateway{directory: d, policy: &runtimeRights{owner: subject.Account, policy: policy}, receipts: store, epoch: d.epoch, owned: map[string]*ownedApplicationSession{}}
	f.g = g
	g.audit = func(_ context.Context, record appmodel.CallRecord, _ string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.records = append(f.records, record)
		return nil
	}
	g.load = func(ctx context.Context, _ *identity.Peer, name string) (*applicationV3Kept, error) {
		if name != "fixture" {
			return nil, errApplicationV3NotFound
		}
		live := map[string]bool{}
		d.mu.Lock()
		for id, lease := range d.instances {
			if lease.application == name {
				live[id] = true
			}
		}
		d.mu.Unlock()
		return &applicationV3Kept{manifest: f.manifest, program: subject.Program, revision: "manifest1", state: f.state, digest: applicationV3BodyDigest([]byte("fixture declaration")), live: live, recheck: func(context.Context) error {
			if f.failAuthority.Load() {
				return errors.New("manifest changed")
			}
			return nil
		}, close: func() error { return nil }}, nil
	}
	g.list = func(ctx context.Context, p *identity.Peer) ([]*applicationV3Kept, error) {
		k, err := g.load(ctx, p, "fixture")
		if err != nil {
			return nil, err
		}
		return []*applicationV3Kept{k}, nil
	}
	s, server := applicationV3PendingSession(t, peer)
	s.binding.runtimeEpoch = g.epoch
	f.s, f.server = s, server
	reader := ownedFixtureInitialize(t, s, server, "2025-11-25")
	g.owned["fixture"] = s
	go func() {
		for {
			request := ownedFixtureRequest(t, reader)
			if request == nil {
				return
			}
			var params struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(request["params"], &params) != nil {
				return
			}
			f.mu.Lock()
			answer := f.answer
			f.mu.Unlock()
			var raw []byte
			if answer != nil {
				raw = answer(params.Name, params.Arguments)
			}
			if raw == nil {
				switch params.Name {
				case "app_describe":
					raw, _ = appmodel.ToJSON(&appmodel.DescriptionResult{Outcome: appmodel.ReadingOutcomePage, Description: f.description})
				case "app_contexts":
					raw, _ = appmodel.ToJSON(&appmodel.ContextPage{Outcome: appmodel.ReadingOutcomePage, Contexts: []appmodel.ContextEntry{{Context: appmodel.ContextRef{Instance: f.instance, Context: "view", Epoch: "e1"}, Revision: "r1"}}})
				default:
					f.effects.Add(1)
					raw, _ = appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Revision: "r1", Effects: `{ "value": 1e2 }`})
				}
			}
			ownedFixtureReply(server, request["id"], map[string]any{"structuredContent": json.RawMessage(`{}`), "content": []map[string]string{{"type": "text", "text": string(raw)}}})
		}
	}()
	for _, action := range actions {
		if err := policy.Set(subject, action, "app:fixture", true); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := shortMediationEndpoint(t)
	h, err := listenApplications(endpoint, d, subject.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	h.v3 = g
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("native host did not join")
		}
	})
	expected := runtimeExpectation(t)
	transport := listen.FrameClient{Endpoint: endpoint, Server: &expected, Timeout: 3 * time.Second, MaxFrame: 1 << 20}.WithContext(ctx)
	f.raw = transport.ExchangeFrame
	f.c = applications.NewApplicationsClient(transport)
	f.client = func(timeout time.Duration) *applications.ApplicationsClient {
		return applications.NewApplicationsClient(listen.FrameClient{Endpoint: endpoint, Server: &expected, Timeout: timeout, MaxFrame: 1 << 20}.WithContext(ctx))
	}
	f.announceKey = receiptKey(1, time.Now())
	change, err := f.c.Announce(f.announceKey, legacy.ApplicationPresence{Application: "fixture", LeaseMs: 30000, Interfaces: []legacy.ApplicationInterface{}, Contexts: []legacy.ApplicationContext{{Name: "view", Revision: "r1"}}})
	if err != nil || change.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatalf("native initial Announce: %+v %v", change, err)
	}
	f.instance = change.Instance
	if s.bindingValue().instance != change.Instance {
		t.Fatal("service-issued handle not bound to the actual native process")
	}
	if err := recheck(); err != nil {
		t.Fatalf("recheck initial native peer: %v", err)
	}
	return f
}

func TestApplicationV3GatewayNativeAnnounceReturnedHandleReplay(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	presence := legacy.ApplicationPresence{Application: "fixture", LeaseMs: 30000, Interfaces: []legacy.ApplicationInterface{}, Contexts: []legacy.ApplicationContext{{Name: "view", Revision: "r1"}}}
	again, err := f.c.Announce(f.announceKey, presence)
	if err != nil || again.Outcome != legacy.ApplicationOutcomeApplied || again.Instance != f.instance {
		t.Fatalf("initial reply replay: %+v %v", again, err)
	}
	refused, err := f.c.Announce(receiptKey(5, time.Now()), presence)
	if err != nil || refused.Outcome != legacy.ApplicationOutcomeStale {
		t.Fatalf("fresh empty rebind: %+v %v", refused, err)
	}
	presence.Instance = f.instance
	renewed, err := f.c.Announce(receiptKey(6, time.Now()), presence)
	if err != nil || renewed.Outcome != legacy.ApplicationOutcomeApplied || renewed.Instance != f.instance {
		t.Fatalf("original handle heartbeat: %+v %v", renewed, err)
	}
	f.g.directory.mu.Lock()
	count := len(f.g.directory.instances)
	f.g.directory.mu.Unlock()
	if count != 1 || f.s.bindingValue().instance != f.instance {
		t.Fatalf("Core/lease handle changed; leases=%d", count)
	}
}

func (f *applicationV3GatewayFixture) call(t *testing.T, n int) (applications.CallResult, string) {
	t.Helper()
	key := receiptKey(n, time.Now())
	result, err := f.c.Call(key, appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	return result, key
}

func TestApplicationV3GatewayNativeCallReplayAndRevoke(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	result, key := f.call(t, 2)
	if result.Outcome != applications.OutcomeApplied || result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied || result.Answer.Result.Effects != `{ "value": 1e2 }` {
		t.Fatalf("exact original canonical answer: %+v", result)
	}
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`}
	again, err := f.c.Call(key, call)
	if err != nil || again.Answer == nil || f.effects.Load() != 1 {
		t.Fatalf("replay dispatched: %+v %v effects=%d", again, err, f.effects.Load())
	}
	if err := f.g.policy.policy.Set(f.subject, "abstraction.facade/command.read", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	denied, err := f.c.Call(key, call)
	if err != nil || denied.Answer == nil || denied.Answer.Result.Outcome != appmodel.CommandOutcomeNotPermitted || f.effects.Load() != 1 {
		t.Fatalf("revoked replay leaked/effected: %+v %v", denied, err)
	}
}

func TestApplicationV3GatewayNativeAdmissionRefusals(t *testing.T) {
	for _, variant := range []string{"no-owned", "wrong-instance", "class", "answer", "source-changed"} {
		t.Run(variant, func(t *testing.T) {
			f := newApplicationV3GatewayFixture(t)
			switch variant {
			case "no-owned":
				f.g.mu.Lock()
				f.g.owned = map[string]*ownedApplicationSession{}
				f.g.mu.Unlock()
			case "wrong-instance":
				f.instance = "foreign"
			case "class":
				f.description.Commands[0].Class = appmodel.CommandClassEdit
			case "answer":
				f.mu.Lock()
				f.answer = func(name string, _ json.RawMessage) []byte {
					if name == "note.read" {
						f.effects.Add(1)
						raw, _ := appmodel.ToJSON(&appmodel.ContextPage{Outcome: appmodel.ReadingOutcomePage, Contexts: []appmodel.ContextEntry{}})
						return raw
					}
					return nil
				}
				f.mu.Unlock()
			case "source-changed":
				f.failAuthority.Store(true)
			}
			result, _ := f.call(t, 3)
			if result.Answer != nil && result.Answer.Result != nil && result.Answer.Result.Outcome == appmodel.CommandOutcomeApplied {
				t.Fatalf("unsafe native success: %+v", result)
			}
			want := int32(0)
			if variant == "answer" {
				want = 1
				if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeUnknown || result.Answer.Result.Reason != "answer:bad_enum" {
					t.Fatalf("custom read-class command lost uncertainty: %+v answer=%+v", result, result.Answer)
				}
			}
			if f.effects.Load() != want {
				t.Fatalf("effects=%d want %d", f.effects.Load(), want)
			}
		})
	}
}

func TestApplicationV3GatewayNativeDescriptionRefusals(t *testing.T) {
	for _, variant := range []string{"version", "schema"} {
		t.Run(variant, func(t *testing.T) {
			f := newApplicationV3GatewayFixture(t)
			want := appmodel.CommandOutcomeInvalid
			if variant == "version" {
				f.description.Version = "appmodel@9"
				want = appmodel.CommandOutcomeUnsupported
			} else {
				f.description.Commands[0].InputSchema = `{"unknownKeyword":true}`
			}
			result, _ := f.call(t, 70)
			if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != want || f.effects.Load() != 0 {
				t.Fatalf("description %s: %+v", variant, result)
			}
			if variant == "version" && result.Answer.Result.Reason != "version:appmodel@9" {
				t.Fatal(result.Answer.Result.Reason)
			}
			listed, err := f.c.Commands(appmodel.CommandsQuery{Application: "fixture", Instance: f.instance})
			if err != nil || listed.Answer == nil || listed.Answer.Outcome.String() != want.String() {
				t.Fatalf("same Commands boundary: %+v %v", listed, err)
			}
		})
	}
}

func TestApplicationV3GatewayNativeActivationDeliveryAndReplay(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	f.manifest.Verbs = []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassRead, InputSchema: `{"type":"object","properties":{"page":{"const":"credentials"}},"required":["page"],"additionalProperties":false}`}}
	var received string
	f.mu.Lock()
	f.answer = func(name string, payload json.RawMessage) []byte {
		if name != "open" {
			return nil
		}
		f.effects.Add(1)
		received = string(payload)
		return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied})
	}
	f.mu.Unlock()
	request := applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{ "page": "credentials" }`}
	key := receiptKey(7, time.Now())
	result, err := f.c.Activate(key, request)
	if err != nil || result.Outcome != applications.ActivationOutcomeReady || result.Started || result.Instance != f.instance || received != string(request.Payload) {
		t.Fatalf("owned exact delivery: %+v %v payload=%q", result, err, received)
	}
	again, err := f.c.Activate(key, request)
	if err != nil || again.Outcome != applications.ActivationOutcomeReady || f.effects.Load() != 1 {
		t.Fatalf("activation replay dispatched: %+v %v effects=%d", again, err, f.effects.Load())
	}
	f.s.Close()
	closed, err := f.c.Activate(key, request)
	if err != nil || closed.Outcome == applications.ActivationOutcomeReady || f.effects.Load() != 1 {
		t.Fatalf("lost owned channel replayed ready: %+v %v", closed, err)
	}
	f.g.directory.mu.Lock()
	f.g.directory.instances = map[string]applicationLease{}
	f.g.directory.mu.Unlock()
	fresh, err := f.c.Activate(receiptKey(9, time.Now()), request)
	if err != nil || fresh.Outcome != applications.ActivationOutcomeUnknown || f.effects.Load() != 1 {
		t.Fatalf("retired channel permitted a duplicate start: %+v %v", fresh, err)
	}
}

func TestApplicationV3GatewayNativeRemoveRejectsStaleStart(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	ctx := context.Background()
	f.manifest.Verbs = []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassRead, InputSchema: `{"type":"object","additionalProperties":false}`}}
	f.mu.Lock()
	f.answer = func(name string, _ json.RawMessage) []byte {
		if name != "open" {
			return nil
		}
		f.effects.Add(1)
		return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied})
	}
	f.mu.Unlock()
	request := applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`}
	key := receiptKey(1794, time.Now())
	first, err := f.c.Activate(key, request)
	if err != nil || first.Outcome != applications.ActivationOutcomeReady || f.effects.Load() != 1 {
		t.Fatalf("initial generic native Start: %+v %v", first, err)
	}
	// Reuse the storage fixture's synthetic signed-declaration seam. Real native
	// RPC, Rights, revision-CAS removal and the original activation receipt remain
	// in use; this test makes no signed-image or installed-Comfy claim.
	d := f.g.directory
	if out := d.remove(ctx, f.subject, "fixture"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if err := d.attachApplicationV3(ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	revision := registration1459Publish(t, d, "fixture", f.subject.Program, "Fixture", "operator", "")
	d.operator = func(program string) bool { return program == f.subject.Program }
	d.v3Changed = f.g.invalidate
	f.s.bindMu.Lock()
	f.s.binding.manifestRevision = revision
	f.s.bindMu.Unlock()
	f.g.load = f.g.loadDeclaration
	change, err := f.c.Remove(applications.ChangeRequest{Application: "fixture", ExpectedRevision: revision})
	if err != nil || change.Outcome != applications.OutcomeApplied {
		t.Fatalf("native Remove: %+v %v", change, err)
	}
	if _, found, err := d.applicationV3Declaration(ctx, "fixture"); err != nil || found {
		t.Fatalf("native Remove retained its declaration: found%v err%v", found, err)
	}
	if hold, err := d.revalidateApplicationV3(ctx, fileid.Self, "fixture", revision); err == nil || hold != nil {
		t.Fatal("removed registration revalidated")
	}
	for _, attempt := range []string{key, receiptKey(1795, time.Now())} {
		result, err := f.c.Activate(attempt, request)
		if err != nil || result.Outcome == applications.ActivationOutcomeReady || f.effects.Load() != 1 {
			t.Fatalf("stale Start restored authority: %+v %v effects%d", result, err, f.effects.Load())
		}
	}
}

func TestApplicationV3GatewayNativeActivationRefusals(t *testing.T) {
	for _, variant := range []string{"refused", "unavailable", "malformed", "invalid-payload", "unowned", "cancel"} {
		t.Run(variant, func(t *testing.T) {
			f := newApplicationV3GatewayFixture(t)
			f.manifest.Verbs = []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassRead, InputSchema: `{"type":"object","additionalProperties":false}`}}
			f.mu.Lock()
			f.answer = func(name string, _ json.RawMessage) []byte {
				if name != "open" {
					return nil
				}
				f.effects.Add(1)
				switch variant {
				case "refused":
					return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeRefused, Reason: "window closed"})
				case "unavailable":
					return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeUnavailable, Reason: "data unavailable"})
				case "cancel":
					<-f.s.done
					return []byte(`{}`)
				default:
					return []byte(`{"outcome":"applied","reason":"not empty"}`)
				}
			}
			f.mu.Unlock()
			request := applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`}
			if variant == "invalid-payload" {
				request.Payload = `{"unexpected":true}`
			}
			if variant == "unowned" {
				f.g.mu.Lock()
				f.g.owned = map[string]*ownedApplicationSession{}
				f.g.mu.Unlock()
			}
			result, err := f.c.Activate(receiptKey(8, time.Now()), request)
			if err == nil && result.Outcome == applications.ActivationOutcomeReady {
				t.Fatalf("invalid activation ready: %+v", result)
			}
			if variant == "refused" && (err != nil || result.Outcome != applications.ActivationOutcomeNotReady || result.Reason != "activation:refused: window closed") {
				t.Fatalf("refusal mapping: %+v %v", result, err)
			}
			if variant == "unavailable" && (err != nil || result.Outcome != applications.ActivationOutcomeUnavailable || result.Reason != "activation:unavailable: data unavailable") {
				t.Fatalf("unavailable mapping: %+v %v", result, err)
			}
			want := int32(1)
			if variant == "invalid-payload" || variant == "unowned" {
				want = 0
			}
			if f.effects.Load() != want {
				t.Fatalf("effects=%d want %d", f.effects.Load(), want)
			}
		})
	}
}

func TestApplicationV3GatewaySensitiveSharedSubjectAsksOnceWithoutPolicyWrite(t *testing.T) {
	for _, class := range []appmodel.CommandClass{appmodel.CommandClassEdit, appmodel.CommandClassCommit, appmodel.CommandClassSpend, appmodel.CommandClassDestroy, appmodel.CommandClassSend} {
		t.Run(class.String(), func(t *testing.T) {
			f := newApplicationV3GatewayFixture(t)
			f.manifest.Offers.Commands[0].Class = class
			f.description.Commands[0].Class = class
			if class == appmodel.CommandClassSpend {
				f.manifest.Title = strings.Repeat("T", 256)
			}
			a, book := firstUseBook(t)
			a.rights = f.g.policy
			f.g.asks = a
			a.keepAgentEnrollments(t.Context(), f.g.directory)
			before, err := a.rights.policy.OperatorSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			key := receiptKey(11, time.Now())
			call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`, ExpectedRevision: "r1", IdempotencyKey: "inner1"}
			type received struct {
				result applications.CallResult
				err    error
			}
			// The durable answer and its native call share the test's caller budget.
			caller := f.client(openTestWait(t))
			done := make(chan received, 1)
			go func() { result, err := caller.Call(key, call); done <- received{result, err} }()
			ctx, cancel := context.WithTimeout(t.Context(), openTestWait(t))
			defer cancel()
			var question asks.Record
			// Book.Gate observes settled answers; admitting a new question is
			// observed through the existing history read in this inert fixture.
			tick := time.NewTicker(5 * time.Millisecond)
			defer tick.Stop()
			for question.ID == "" && ctx.Err() == nil {
				records, _, readErr := book.ApplicationHistory()
				if readErr != nil {
					err = readErr
					break
				}
				for _, r := range records {
					if r.Pending() {
						question = r
						break
					}
				}
				if question.ID == "" {
					select {
					case <-tick.C:
					case <-ctx.Done():
					}
				}
			}
			if err == nil {
				err = ctx.Err()
			}
			if err != nil || question.ID == "" || question.Key != asks.ApplicationCommandOnceKey {
				t.Fatalf("person occurrence missing: %+v %v", question, err)
			}
			if f.effects.Load() != 0 {
				t.Fatal("stored permit bypassed person occurrence")
			}
			if !strings.Contains(question.Text, f.manifest.Title) {
				t.Fatal("person sentence lost the held application title")
			}
			if _, err := a.authorizeAnswerer(ctx, answerer{subject: rwire.Subject{Account: f.subject.Account, Program: "nonparty-assistant"}}, question, a.scope, "once"); err == nil {
				t.Fatal("assistant could answer person-only occurrence")
			}
			_, state, err := book.AnswerApplicationScopedAuthorized(question.ID, "once", func(r asks.Record, scope string) error {
				if scope != a.scope || r.ID != question.ID {
					return errors.New("operator fixture scope")
				}
				return nil
			})
			if err != nil || state != "answered" {
				t.Fatal(state, err)
			}
			select {
			case got := <-done:
				if got.err != nil || got.result.Answer == nil || got.result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
					t.Fatalf("answered call: %+v %v", got.result, got.err)
				}
			case <-ctx.Done():
				t.Fatal("answered call did not join")
			}
			if f.effects.Load() != 1 {
				t.Fatal("one answer did not yield exactly one effect")
			}
			again, err := caller.Call(key, call)
			if err != nil || again.Answer == nil || f.effects.Load() != 1 {
				t.Fatalf("same occurrence replay: %+v %v", again, err)
			}
			after, err := a.rights.policy.OperatorSnapshot()
			if err != nil || before.Revision != after.Revision {
				t.Fatalf("one-call changed policy: %s→%s %v", before.Revision, after.Revision, err)
			}
		})
	}
}

func TestApplicationV3GatewayNativeAgentClassRecheckedForReplay(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	permission := applicationPermissionRuntime(t)
	f.g.policy = permission.a.rights
	f.g.asks = permission.a
	f.g.consent = f.g.bundleConsent
	run := &selfRegisteredRun{f: permission, d: f.g.directory, ctx: t.Context()}
	run.k = permission.a.keepSelfRegistered(t.Context(), f.g.directory)
	if got := f.g.directory.askConfirmation(t.Context(), permission.panel, "fixture"); got.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	registrationQuestion := selfRegisteredQuestion(t, permission, f.g.directory, "fixture")
	answer(t, permission, registrationQuestion.ID, "keep")
	waitDirectory(t, f.g.directory, func() bool {
		registered, unconfirmed := unconfirmedNow(f.g.directory, "fixture")
		return registered && !unconfirmed
	})
	agent1478Attach(t, run)
	f.g.directory.agentImage = nil // actual native image/FileID owner, no fake class
	pending := agent1478Set(t, run, "fixture", "", true)
	if pending.State != applications.AgentEnrollmentStatePending {
		t.Fatal(pending)
	}
	question := agent1478Question(t, run, "fixture")
	answer(t, permission, question.ID, "keep")
	agent1478Wait(t, run, "fixture", "enrolled")
	f.manifest.Offers.Commands[0].Class = appmodel.CommandClassEdit
	f.description.Commands[0].Class = appmodel.CommandClassEdit
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.edit", "app:fixture", true); err != nil {
		t.Fatal(err)
	}
	key := receiptKey(90, time.Now())
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`, ExpectedRevision: "r1", IdempotencyKey: "agent-effect"}
	result, err := f.c.Call(key, call)
	if err != nil || result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied || f.effects.Load() != 1 {
		t.Fatalf("person-kept native agent: %+v %v", result, err)
	}
	current := f.g.directory.readAgentEnrollment(t.Context(), permission.panel, "fixture")
	if current.Enrollment == nil {
		t.Fatal(current)
	}
	agent1478Set(t, run, "fixture", current.Enrollment.Revision, false)
	before, _, err := permission.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.c.Call(key, call)
	if err != nil || replay.Answer == nil || replay.Answer.Result == nil || replay.Answer.Result.Outcome == appmodel.CommandOutcomeApplied || f.effects.Load() != 1 {
		t.Fatalf("lost class leaked protected replay: %+v %v", replay, err)
	}
	after, _, err := permission.a.book.ApplicationHistory()
	if err != nil || len(after) != len(before) {
		t.Fatal("protected replay admitted a fresh question")
	}
	if d := permission.a.rights.decide(t.Context(), f.subject, "abstraction.facade/command.edit", "app:fixture"); d.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal("fixture did not preserve the standing permit")
	}
	read, err := f.c.ReadAgentEnrollment("fixture")
	if err != nil || read.Outcome != applications.OutcomePage || read.Enrollment == nil || read.Enrollment.Program != f.subject.Program {
		t.Fatalf("native nonoperator read forwarding: %+v %v", read, err)
	}
	set, err := f.c.SetAgentEnrollment(applications.AgentEnrollmentRequest{Application: "fixture", Enabled: true})
	if err != nil || set.Outcome != applications.OutcomeForbidden {
		t.Fatalf("native self-enrollment forwarding: %+v %v", set, err)
	}
}

func TestApplicationV3GatewayNativeRoutinePermissionDispatchReplay(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	permission := applicationPermissionRuntime(t)
	f.g.policy = permission.a.rights
	f.g.asks = permission.a
	f.g.consent = f.g.bundleConsent
	key := receiptKey(80, time.Now())
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`}
	type received struct {
		result applications.CallResult
		err    error
	}
	done := make(chan received, 1)
	go func() { result, err := f.c.Call(key, call); done <- received{result, err} }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var pending asks.Record
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for pending.ID == "" && ctx.Err() == nil {
		rows, _, err := permission.a.book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.Pending() && row.TypedPermission != nil {
				pending = row
				break
			}
		}
		if pending.ID == "" {
			select {
			case <-tick.C:
			case <-ctx.Done():
			}
		}
	}
	if pending.ID == "" || len(pending.TypedPermission.Effect.Selections) != 3 || f.effects.Load() != 0 {
		t.Fatalf("real native finite permission: %+v", pending)
	}
	if pending.TypedPermission.Effect.Subject != f.subject {
		t.Fatal("request data replaced native beneficiary")
	}
	if _, _, err := permission.a.book.AnswerTypedPermissionIfPendingAuthorized(pending.ID, "allow", func(asks.Record, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	edit, err := permission.a.applyPermissionAllow(ctx, permission.panel, permission.revision(t), pending.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("existing operator atomic completion: %+v %v", edit, err)
	}
	select {
	case got := <-done:
		if got.err != nil || got.result.Answer == nil || got.result.Answer.Result == nil || got.result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied || f.effects.Load() != 1 {
			t.Fatalf("native permission dispatch: %+v %v", got.result, got.err)
		}
	case <-ctx.Done():
		t.Fatal("native permission dispatch did not join")
	}
	for _, class := range []string{"read", "reveal", "propose"} {
		if d := permission.a.rights.decide(ctx, f.subject, "abstraction.facade/command."+class, "app:fixture"); d.Outcome != rwire.DecisionOutcomePermitted {
			t.Fatalf("disclosed atomic class %s: %+v", class, d)
		}
	}
	again, err := f.c.Call(key, call)
	if err != nil || again.Answer == nil || f.effects.Load() != 1 {
		t.Fatalf("protected replay: %+v %v", again, err)
	}
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.read", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	denied, err := f.c.Call(key, call)
	if err != nil || denied.Answer == nil || denied.Answer.Result == nil || denied.Answer.Result.Outcome != appmodel.CommandOutcomeNotPermitted || f.effects.Load() != 1 {
		t.Fatalf("revoked protected replay: %+v %v", denied, err)
	}
}

func TestApplicationV3GatewayNativeDiscoveryLeavesNoCallRecord(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	listed, err := f.c.Describe(appmodel.ApplicationsQuery{})
	if err != nil || listed.Outcome != applications.OutcomePage || listed.Answer == nil || len(listed.Answer.Applications) != 1 {
		t.Fatalf("visible discovery: %+v %v", listed, err)
	}
	commands, err := f.c.Commands(appmodel.CommandsQuery{Application: "fixture", Instance: f.instance})
	if err != nil || commands.Outcome != applications.OutcomePage || commands.Answer == nil || len(commands.Answer.Commands) != 1 {
		t.Fatalf("commands: %+v %v", commands, err)
	}
	if err := f.g.policy.policy.Revoke(f.subject, "abstraction.facade/command.read", "app:fixture"); err != nil {
		t.Fatal(err)
	}
	hidden, err := f.c.Describe(appmodel.ApplicationsQuery{})
	if err != nil || hidden.Answer == nil || len(hidden.Answer.Applications) != 0 {
		t.Fatalf("hidden discovery: %+v %v", hidden, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) != 0 || f.effects.Load() != 0 {
		t.Fatal("discovery produced a CallRecord or command effect")
	}
}

func TestApplicationV3GatewayNativeMalformedCallAudit(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	reply, err := f.raw([]byte(`{"version":1,"service":"abstraction.facade/applications@3","method":"Call","arguments":{"key":"PRIVATE-UNREAD-MARKER","call":42}}`))
	if err != nil || !strings.Contains(string(reply), "wrong_type") {
		t.Fatalf("malformed Call: %s %v", reply, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.records) != 1 {
		t.Fatalf("records: %+v", f.records)
	}
	r := f.records[0]
	if r.Outcome != "invalid" || r.Subject != f.subject.Program || r.ArgumentsDigest != "" || r.Command != "" || r.Application != "" || r.IdempotencyKey != "" || r.ResultDigest == "" {
		t.Fatalf("unread audit: %+v", r)
	}
	encoded, _ := appmodel.ToJSON(&r)
	if strings.Contains(string(reply)+string(encoded), "PRIVATE-UNREAD-MARKER") || f.effects.Load() != 0 {
		t.Fatal("malformed Call leaked unread data or dispatched")
	}
}

func TestApplicationV3GatewayNativeAuditBoundBeforeSend(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, closeSink, err := runtimeLogSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closeSink.Close()
	write, check, err := applicationV3AuditWriter(sink, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	f.g.auditCheck = check
	f.g.audit = write
	// The largest expected_revision a call carries, written to the audit as
	// six bytes per character: too large for the history view.
	revision := strings.Repeat("<>&", 262144/3)
	result, err := f.c.Call(receiptKey(60, time.Now()), appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`, ExpectedRevision: revision})
	if err != nil || result.Outcome != applications.OutcomeApplied || f.effects.Load() != 1 {
		t.Fatalf("exact native audit: %+v %v", result, err)
	}
	record, emitter := applicationV3ReadPhysicalAudit(t, path)
	if record.RevisionBefore != revision || record.ResultDigest == "" || emitter.Program == "" {
		t.Fatal("native audit lost complete record or emitter")
	}
	history, err := sink.(*logging.FileSink).ScanApplicationHistory("")
	if err != nil || history.Outcome.String() != "record_too_large" || len(history.Rows) != 0 {
		t.Fatalf("exceptional history availability: %+v %v", history, err)
	}
}

func TestApplicationV3GatewayNativeAuditInnerKeyAndBindingClass(t *testing.T) {
	for _, tc := range []struct {
		name, tool, inner, malformed string
		class                        appmodel.CommandClass
	}{
		{"command", "note.read", "inner-command", "", appmodel.CommandClassRead},
		{"undo-refused", "app_undo", "inner-undo", "", appmodel.CommandClassEdit},
		{"binding-no-key", "app_contexts", "", "", appmodel.CommandClassRead},
		{"invalid-read", "app_read", "", `{"context":"PRIVATE-UNDECODED"}`, appmodel.CommandClassRead},
		{"invalid-undo", "app_undo", "", `{"context":"PRIVATE-UNDECODED"}`, appmodel.CommandClassEdit},
		{"invalid-describe", "app_describe", "", `{"unexpected":"PRIVATE-UNDECODED"}`, appmodel.CommandClassRead},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApplicationV3GatewayFixture(t)
			path := filepath.Join(t.TempDir(), "audit.jsonl")
			sink, closer, err := runtimeLogSink(path)
			if err != nil {
				t.Fatal(err)
			}
			defer closer.Close()
			f.g.audit, f.g.auditCheck, err = applicationV3AuditWriter(sink, func(err error) { t.Error(err) })
			if err != nil {
				t.Fatal(err)
			}
			ref := appmodel.ContextRef{Instance: f.instance, Context: "view", Epoch: "e1"}
			arguments := []byte(`{}`)

			if tc.tool == "app_undo" {
				arguments, err = appmodel.ToJSON(&appmodel.UndoRequest{Context: ref, ExpectedRevision: "r1", UndoGroup: "group", IdempotencyKey: tc.inner})
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.malformed != "" {
				arguments = []byte(tc.malformed)
			}
			outer := receiptKey(71, time.Now())
			call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: tc.tool, Arguments: string(arguments)}
			if tc.tool == "note.read" {
				call.IdempotencyKey = tc.inner
			}
			for i := 0; i < 2; i++ {
				if _, err = f.c.Call(outer, call); err != nil {
					t.Fatal(err)
				}
			}
			rows := applicationV3ReadPhysicalAudits(t, path)
			if len(rows) != 2 {
				t.Fatalf("audit rows %d", len(rows))
			}
			for _, row := range rows {
				r := row.record
				if r.IdempotencyKey != tc.inner || r.IdempotencyKey == outer || r.Class == nil || *r.Class != tc.class || row.emitter.Program == "" {
					t.Fatalf("audit projection: %+v", r)
				}
				if tc.malformed != "" {
					encoded, _ := appmodel.ToJSON(&r)
					if r.Context != nil || r.RevisionBefore != "" || strings.Contains(string(encoded), "PRIVATE-UNDECODED") || f.effects.Load() != 0 {
						t.Fatal("undecoded fields reached audit/effect")
					}
				}
			}
			if tc.tool == "note.read" && f.effects.Load() != 1 {
				t.Fatal("receipt replay redispatched command")
			}
		})
	}
}

func TestApplicationV3GatewayNativeObserveCompleteFrameBound(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	ctx := context.Background()
	// This fixture starts with a legacy registration for its native Announce.
	// Replace that through the real owner before publishing the distinct @3 row.
	if out := f.g.directory.remove(ctx, f.subject, "fixture"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if err := f.g.directory.attachApplicationV3(ctx, f.g.decoder); err != nil {
		t.Fatal(err)
	}
	manifest := *f.manifest
	_, file := registration1459File(t, "fixture", f.subject.Program, "Fixture")
	// Inert receiving facts for this CAS/frame test; no platform signature claim.
	file.Accepted, file.Verified = registrationTime(f.g.directory.now()), registrationTime(f.g.directory.now())
	file.Raw = registrationwire.EncodeManifest(&manifest)
	file.Digest = applicationV3Hash(file.Raw)
	out := f.g.directory.publishApplicationV3(ctx, legacy.ApplicationDescriptor{Name: "fixture", Title: "Fixture", Program: f.subject.Program}, file, "operator", "")
	if out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	page, err := f.c.Observe(applications.Query{Limit: 64})
	if err != nil || page.Outcome != applications.OutcomePage || len(page.Registrations) != 1 || page.Registrations[0].Revision != out.Revision {
		t.Fatalf("native retained registration: %+v %v", page, err)
	}
	if !applicationV3ReplyFits(page) {
		t.Fatal("page exceeds actual generated native reply")
	}
	stale, err := f.c.Observe(applications.Query{Limit: 64, Cursor: "foreign.cursor.0"})
	if err != nil || stale.Outcome != applications.OutcomeStale || len(stale.Registrations) != 0 {
		t.Fatalf("foreign cursor: %+v %v", stale, err)
	}
	f.g.directory.mu.Lock()
	f.g.directory.instances = map[string]applicationLease{}
	for n := 0; n < 128; n++ {
		id := "instance-" + strconv.Itoa(n)
		contexts := make([]legacy.ApplicationContext, 32)
		for j := range contexts {
			contexts[j] = legacy.ApplicationContext{Name: "c" + strconv.Itoa(j), Title: strings.Repeat("t", 256), Revision: strings.Repeat("r", 64)}
		}
		due := time.Now().Add(time.Minute)
		f.g.directory.instances[id] = applicationLease{application: "fixture", owner: f.subject, session: "fixture-session", value: legacy.ApplicationInstance{Instance: id, Contexts: contexts, Interfaces: []legacy.ApplicationInterface{}, ExpiresUnixMs: due.UnixMilli()}, deadline: due}
	}
	f.g.directory.mu.Unlock()
	bounded, err := f.c.Observe(applications.Query{Limit: 64})
	if err != nil || bounded.Outcome != applications.OutcomeUnavailable || bounded.Reason != "bound:reply_bytes" || len(bounded.Registrations) != 0 {
		t.Fatalf("oversize registration looked empty/successful: %+v %v", bounded, err)
	}
}

func TestApplicationV3GatewayNativeSecretRefusalAndAudit(t *testing.T) {
	for _, tool := range []string{"app_read", "note.read"} {
		t.Run(tool, func(t *testing.T) {
			f := newApplicationV3GatewayFixture(t)
			f.description.Kinds[0].Schema = `{"type":"object","properties":{"api_key":{"type":"string"}}}`
			f.description.Kinds[0].Fields = []appmodel.KindField{{Name: "api_key", Role: appmodel.FieldRoleSecret}}
			f.mu.Lock()
			f.answer = func(name string, _ json.RawMessage) []byte {
				if name != tool {
					return nil
				}
				if name == "app_read" {
					raw, _ := appmodel.ToJSON(&appmodel.ContextSnapshot{Outcome: appmodel.ReadingOutcomePage, Context: appmodel.ContextRef{Instance: f.instance, Context: "view", Epoch: "e1"}, Revision: "r1", Entities: []appmodel.Entity{{ID: "n1", Kind: "note", Properties: `{"api_key":"PRIVATE-KEY-CONTROL"}`}}, Collections: []appmodel.Collection{}, Selections: []appmodel.Selection{}})
					return raw
				}
				raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Revision: "r1", Effects: `{"nested":{"api_key":"PRIVATE-KEY-CONTROL"}}`})
				return raw
			}
			f.mu.Unlock()
			var arguments []byte
			if tool == "app_read" {
				arguments, _ = appmodel.ToJSON(&appmodel.ReadRequest{Context: appmodel.ContextRef{Instance: f.instance, Context: "view", Epoch: "e1"}, Entities: []string{}, Limit: 1})
			} else {
				arguments = []byte(`{}`)
			}
			result, err := f.c.Call(receiptKey(12, time.Now()), appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: tool, Arguments: string(arguments)})
			if err != nil || result.Answer == nil {
				t.Fatalf("secret refusal: %+v %v", result, err)
			}
			if tool == "app_read" {
				if result.Answer.Snapshot == nil || result.Answer.Snapshot.Outcome != appmodel.ReadingOutcomeInvalid {
					t.Fatalf("read secret outcome: %+v", result)
				}
			} else if result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeUnknown {
				t.Fatalf("command secret outcome: %+v", result)
			}
			f.mu.Lock()
			records := append([]appmodel.CallRecord(nil), f.records...)
			f.mu.Unlock()
			if len(records) != 1 || records[0].Reason != "secret:note.api_key" || records[0].ArgumentsDigest != "sha256:"+applicationV3BodyDigest(arguments) || records[0].ResultDigest == "" {
				t.Fatalf("secret audit: %+v", records)
			}
			encoded, _ := json.Marshal(struct {
				Result  applications.CallResult
				Records []appmodel.CallRecord
			}{result, records})
			if strings.Contains(string(encoded), "PRIVATE-KEY-CONTROL") {
				t.Fatal("secret value reached answer or audit")
			}
		})
	}
}

func TestApplicationV3SharedObservationRequiresRegistrationDirectory(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	page, err := f.c.Observe(applications.Query{Limit: 1})
	if err != nil || page.Outcome != applications.OutcomeUnavailable || page.Reason != "directory" || len(page.Registrations) != 0 || page.Cursor != "" {
		t.Fatalf("missing shared registration directory was presented as observed: %+v %v", page, err)
	}
	invalid, err := f.c.Observe(applications.Query{Limit: 1, Application: "invalid/name"})
	if err != nil || invalid.Outcome != applications.OutcomeInvalid || len(invalid.Registrations) != 0 {
		t.Fatalf("missing directory bypassed existing validation: %+v %v", invalid, err)
	}
}

func TestApplicationV3ObserveWholeProgramWithholdsDeniedSiblingFacet(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	ctx := context.Background()
	d := f.g.directory
	if out := d.remove(ctx, f.subject, "fixture"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if err := d.attachApplicationV3(ctx, f.g.decoder); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"fixture", "hidden"} {
		manifest := *f.manifest
		manifest.Name = name
		manifest.Title = name + " private manifest"
		display, file := registration1459File(t, name, f.subject.Program, manifest.Title)
		file.Raw = registrationwire.EncodeManifest(&manifest)
		file.Digest = applicationV3Hash(file.Raw)
		file.Accepted, file.Verified = registrationTime(d.now()), registrationTime(d.now())
		if out := d.publishApplicationV3(ctx, display, file, "operator", ""); out.Outcome != legacy.ApplicationOutcomeApplied {
			t.Fatal(out)
		}
	}
	visible, err := f.c.Observe(applications.Query{Limit: 64})
	if err != nil || len(visible.Registrations) != 1 || len(visible.Registrations[0].Spec.Applications) != 2 {
		t.Fatalf("whole program %+v %v", visible, err)
	}
	d.mu.Lock()
	d.decide = func(_ context.Context, _ rwire.Subject, action, resource string) (bool, error) {
		return action != ActionApplicationRead || resource == "app:fixture", nil
	}
	d.mu.Unlock()
	withheld, err := f.c.Observe(applications.Query{Limit: 64})
	if err != nil || withheld.Outcome != applications.OutcomePage || len(withheld.Registrations) != 0 {
		t.Fatalf("denied sibling leaked %+v %v", withheld, err)
	}
	raw, err := json.Marshal(withheld)
	if err != nil || strings.Contains(string(raw), "hidden") || strings.Contains(string(raw), "private manifest") {
		t.Fatalf("manifest disclosure %s %v", raw, err)
	}
	legacyPage := d.observe(ctx, f.subject, "", 0)
	if len(legacyPage.Applications) != 1 || legacyPage.Applications[0].Descriptor.Name != "fixture" {
		t.Fatalf("legacy per-app scope %+v", legacyPage)
	}
}

func TestApplicationV3ObserveChoosersOnlyChangeIsStale(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	ctx := context.Background()
	d := f.g.directory
	if out := d.remove(ctx, f.subject, "fixture"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if err := d.attachApplicationV3(ctx, f.g.decoder); err != nil {
		t.Fatal(err)
	}
	manifest := *f.manifest
	manifest.Choosers = &registrationwire.Choosers{Entries: []registrationwire.Chooser{{Name: "first", Modality: "chat"}}}
	display, file := registration1459File(t, "fixture", f.subject.Program, "Fixture")
	file.Accepted, file.Verified = registrationTime(d.now()), registrationTime(d.now())
	file.Raw = registrationwire.EncodeManifest(&manifest)
	file.Digest = applicationV3Hash(file.Raw)
	out := d.publishApplicationV3(ctx, display, file, "operator", "")
	if out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	before := d.observe(ctx, f.subject, "", 0)
	page, err := f.c.Observe(applications.Query{Limit: 1})
	if err != nil || page.Outcome != applications.OutcomePage || len(page.Registrations) != 1 {
		t.Fatalf("first %+v %v", page, err)
	}
	manifest.Choosers.Entries[0].Name = "second"
	file.Raw = registrationwire.EncodeManifest(&manifest)
	file.Digest = applicationV3Hash(file.Raw)
	out = d.publishApplicationV3(ctx, display, file, "operator", out.Revision)
	if out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	after := d.observe(ctx, f.subject, "", 0)
	if before.Cursor != after.Cursor {
		t.Fatal("fixture changed legacy display cursor")
	}
	stale, err := f.c.Observe(applications.Query{Limit: 1, Cursor: page.Cursor})
	if err != nil || stale.Outcome != applications.OutcomeStale || len(stale.Registrations) != 0 || stale.Cursor != "" {
		t.Fatalf("changed terminal %+v %v", stale, err)
	}
	fresh, err := f.c.Observe(applications.Query{Limit: 1})
	if err != nil || len(fresh.Registrations) != 1 || fresh.Registrations[0].Spec.Applications[0].Manifest.Choosers.Entries[0].Name != "second" {
		t.Fatalf("restart %+v %v", fresh, err)
	}
	// Hidden registrations do not invalidate a caller-filtered cursor.
	hidden, other := registration1459File(t, "hidden", registration1459Program("hidden-observe.exe"), "Hidden")
	other.Accepted, other.Verified = registrationTime(d.now()), registrationTime(d.now())
	m := manifest
	m.Name = "hidden"
	m.Title = "Hidden"
	other.Raw = registrationwire.EncodeManifest(&m)
	other.Digest = applicationV3Hash(other.Raw)
	if out := d.publishApplicationV3(ctx, hidden, other, "operator", ""); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	d.mu.Lock()
	d.decide = func(_ context.Context, _ rwire.Subject, action, resource string) (bool, error) {
		return action != ActionApplicationRead || resource == "app:fixture", nil
	}
	d.mu.Unlock()
	stable, err := f.c.Observe(applications.Query{Limit: 1, Cursor: fresh.Cursor})
	if err != nil || stable.Outcome != applications.OutcomePage || len(stable.Registrations) != 0 || stable.Cursor != fresh.Cursor {
		t.Fatalf("hidden disclosed %+v %v", stable, err)
	}
}

func TestApplicationV3QuietReadAfterDiscoveryRevocation(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	listed, err := f.c.Describe(appmodel.ApplicationsQuery{})
	if err != nil || listed.Answer == nil || len(listed.Answer.Applications) != 1 ||
		len(listed.Answer.Applications[0].Instances) != 1 ||
		len(listed.Answer.Applications[0].Instances[0].Contexts) != 1 {
		t.Fatalf("describe before revocation: %+v %v", listed, err)
	}
	ref := listed.Answer.Applications[0].Instances[0].Contexts[0].Context
	if err := f.g.policy.policy.Revoke(f.subject, "abstraction.facade/command.read", "app:fixture"); err != nil {
		t.Fatal(err)
	}
	var asked atomic.Int32
	f.g.consent = func(context.Context, applicationV3ConsentRequest) applicationV3ConsentResult {
		asked.Add(1)
		return applicationV3ConsentResult{outcome: "refused"}
	}
	quiet := true
	call := appmodel.GatewayCall{
		Application: "fixture", Instance: ref.Instance, Tool: "app_read",
		Arguments:         string(appmodel.EncodeReadRequest(&appmodel.ReadRequest{Context: ref, Entities: []string{}, Limit: 16})),
		RequireReadPermit: &quiet,
	}
	out, err := f.c.Call(receiptKey(201, time.Now()), call)
	if err != nil || out.Answer == nil || out.Answer.Snapshot == nil ||
		out.Answer.Snapshot.Outcome != appmodel.ReadingOutcomeNotPermitted || out.Answer.Snapshot.Reason != "rights:not_granted" ||
		asked.Load() != 0 || f.effects.Load() != 0 {
		t.Fatalf("quiet snapshot after revocation: %+v %v, questions %d dispatches %d", out, err, asked.Load(), f.effects.Load())
	}
	call.RequireReadPermit = nil
	if _, err := f.c.Call(receiptKey(202, time.Now()), call); err != nil || asked.Load() != 1 || f.effects.Load() != 0 {
		t.Fatalf("normal read after revocation: %v, questions %d dispatches %d", err, asked.Load(), f.effects.Load())
	}
}

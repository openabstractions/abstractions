package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

const typedTestProfile = "example.resources/named@1"
const typedTestAction = "example.resources/read"

func applicationPermissionRuntime(t *testing.T) *answererFixture {
	t.Helper()
	f := newAnswererFixture(t)
	configureApplicationPermissionRuntime(t, f)
	return f
}

func configureApplicationPermissionRuntime(t *testing.T, f *answererFixture) {
	t.Helper()
	var definitions []rwire.PermissionDefinition
	for _, definition := range grants.PermissionDefinitions() {
		if definition.ResourceProfile == rights.LegacyExactResourceProfile {
			definitions = append(definitions, definition)
		}
	}
	catalog, err := rights.NewDefinitionCatalog(definitions, map[string]func(string) bool{rights.LegacyExactResourceProfile: func(resource string) bool { return rights.ValidDecisionQuery("example/use", resource) }})
	if err != nil {
		t.Fatal(err)
	}
	f.a.rights.policy.Definitions = catalog
	for _, class := range []string{"read", "reveal", "propose", "edit", "commit", "spend", "destroy", "send"} {
		action, _ := grants.ApplicationCommandAction(class, "app:editor")
		if err := f.a.rights.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeDeclaredHostTypedAnswerReadReplayAndCASGuard(t *testing.T) {
	f := applicationPermissionRuntime(t)
	f.enroll(t, f.answerer)
	p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "read")
	if err != nil {
		t.Fatal(err)
	}
	r, outcome, err := f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "declared-role", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	d := f.a.rights.applications
	prepared, err := f.a.rights.policy.PrepareMetaHolderContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	effect, err := f.a.authorizeAnswerer(prepared, f.me(), r, f.a.scope, "allow")
	if err != nil || effect == nil {
		t.Fatal(err)
	}
	// This seam supplies immutable role facts directly after outer preparation.
	f.a.rights.policy.RefreshMetaHolder = nil
	d.roles.Store(&applicationRoles{hosts: []asks.DeclaredHost{{Program: f.answerer.Program}}})
	// The guard runs under the real policy CAS with the directory locked.
	d.mu.Lock()
	result, _ := effect(prepared, func() error { return nil }, true)
	d.mu.Unlock()
	if result != askswire.ResponderEffectOutcomeForbidden {
		t.Fatal("late typed effect", result)
	}
	for _, selection := range p.Effect.Selections {
		if got := f.decide(f.comfy, selection.Permission.Action, "app:editor"); got == rwire.DecisionOutcomePermitted {
			t.Fatal("late typed effect committed")
		}
	}
	if f.answerable(f.me(), r, "allow") {
		t.Fatal("typed read listed declared host")
	}
	if _, _, err := f.a.book.AnswerTypedPermissionAsResponderAuthorized(r.ID, "allow", f.answerer.Program, func(record asks.Record, scope string) error {
		_, err := f.a.authorizeAnswerer(t.Context(), f.me(), record, scope, "allow")
		return err
	}); !errors.Is(err, asks.ErrAnswererForbidden) {
		t.Fatal("typed answer/replay", err)
	}
	d.roles.Store(nil)
	if _, err := f.a.authorizeAnswerer(t.Context(), f.me(), r, f.a.scope, "allow"); !errors.Is(err, rights.ErrMetaHolderUnavailable) {
		t.Fatal("typed unavailable", err)
	}
}

func TestRuntimeApplicationPermissionRoutineEnrollmentAndAtomicEffect(t *testing.T) {
	f := applicationPermissionRuntime(t)
	p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "read")
	if err != nil {
		t.Fatal(err)
	}
	r, outcome, err := f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "routine-1", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	if f.answerable(f.me(), r, "allow") {
		t.Fatal("unenrolled assistant offered routine bundle")
	}
	name, err := asks.FirstUseAnswerResource(f.comfy.Program, p.Effect.Selections[1].Permission.Action, "app:editor", "allow")
	if err != nil {
		t.Fatal(err)
	}
	permit := true
	if _, err := f.a.rights.policy.ChangeRule(f.revision(t), f.answerer, asks.AnswerAction, name, rights.RuleEdit{Permit: &permit, By: f.panel, TTL: time.Minute}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if f.answerable(f.me(), r, "allow") {
		t.Fatal("exact delegation bypassed required enrollment")
	}
	f.enroll(t, f.answerer)
	generic := r
	gp := asks.TypedPermission{Origin: "service", Effect: rwire.PermissionEffect{Subject: f.comfy, Selections: []rwire.PermissionSelection{typedTestSelection(false)}}, Display: askswire.PermissionDisplay{Selections: []askswire.PermissionDisplaySelection{{Operation: "Read saved value", Target: "test saved value"}}}}
	gp.Digest = asks.PermissionEffectDigest(gp.Effect)
	generic.TypedPermission = &gp
	if f.answerable(f.me(), generic, "allow") {
		t.Fatal("generic typed effect offered to responder")
	}
	if !f.answerable(f.me(), r, "allow") {
		t.Fatal("enrolled nonparty could not answer")
	}
	for _, who := range []answerer{{subject: f.comfy}, {subject: f.panel}, {subject: rwire.Subject{Account: "foreign", Program: f.answerer.Program}}} {
		if f.answerable(who, r, "allow") {
			t.Fatal("ineligible answerer offered", who)
		}
	}
	var effect asksservice.ResponderEffect
	answered, outcome, err := f.a.book.AnswerTypedPermissionAsResponderAuthorized(r.ID, "allow", f.answerer.Program, func(record asks.Record, scope string) error {
		var e error
		effect, e = f.a.authorizeAnswerer(t.Context(), f.me(), record, scope, "allow")
		return e
	})
	if err != nil || outcome != "answered" || effect == nil || answered.Answerer != f.answerer.Program {
		t.Fatal(answered, outcome, err)
	}
	result, decision := effect(t.Context(), func() error { return nil }, true)
	if result != askswire.ResponderEffectOutcomeApplied || decision != "permitted" {
		t.Fatal(result, decision)
	}
	for _, s := range p.Effect.Selections {
		if got := f.decide(f.comfy, s.Permission.Action, "app:editor"); got != rwire.DecisionOutcomePermitted {
			t.Fatal(got)
		}
	}
	// The saved responder Allow is already applied. Operator retry never turns
	// it into person-owned grants; by-ID Undo retains the recorded answerer.
	edit, err := f.a.applyPermissionAllow(t.Context(), f.panel, f.revision(t), r.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeInvalid {
		t.Fatal(edit, err)
	}
	edit, err = f.a.undoPermissionEffect(t.Context(), f.panel, f.revision(t), r.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	for _, s := range p.Effect.Selections {
		if got := f.decide(f.comfy, s.Permission.Action, "app:editor"); got != rwire.DecisionOutcomeNotGranted {
			t.Fatal(got)
		}
	}
}

func TestRuntimeApplicationPermissionStandingPersonOnlyAndRetainedOperatorCompletion(t *testing.T) {
	f := applicationPermissionRuntime(t)
	f.enroll(t, f.answerer)
	p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "edit")
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "standing-1", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if f.answerable(f.me(), r, "allow") || f.answerable(f.me(), r, "refuse") {
		t.Fatal("standing effect offered to assistant")
	}
	if _, _, err := f.a.book.AnswerTypedPermissionIfPendingAuthorized(r.ID, "allow", func(asks.Record, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	edit, err := f.a.applyPermissionAllow(t.Context(), f.panel, f.revision(t), r.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied || len(edit.Current) != 3 {
		t.Fatal(edit, err)
	}
	for _, s := range p.Effect.Selections {
		if got := f.decide(f.comfy, s.Permission.Action, "app:editor"); got != rwire.DecisionOutcomePermitted {
			t.Fatal(got)
		}
	}
}

func TestRuntimeApplicationPermissionResponderRefuseRenewsWithoutGrant(t *testing.T) {
	f := applicationPermissionRuntime(t)
	f.enroll(t, f.answerer)
	before := f.a.rights.policy.ReadRule(f.answerer, asks.AnswerAction, rights.MetaScopeApps)
	f.advance(24 * time.Hour)
	p, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "reveal")
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "routine-refuse", p, listen.Seen{}, asks.Publisher{}, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var effect asksservice.ResponderEffect
	_, outcome, err := f.a.book.AnswerTypedPermissionAsResponderAuthorized(r.ID, "refuse", f.answerer.Program, func(record asks.Record, scope string) error {
		var e error
		effect, e = f.a.authorizeAnswerer(t.Context(), f.me(), record, scope, "refuse")
		return e
	})
	if err != nil || outcome != "answered" || effect == nil {
		t.Fatal(outcome, err)
	}
	result, decision := effect(t.Context(), func() error { return nil }, true)
	if result != askswire.ResponderEffectOutcomeNone || decision != "not_granted" {
		t.Fatal(result, decision)
	}
	after := f.a.rights.policy.ReadRule(f.answerer, asks.AnswerAction, rights.MetaScopeApps)
	if before.Record == nil || after.Record == nil || after.Record.Expires <= before.Record.Expires {
		t.Fatal(before, after)
	}
	for _, s := range p.Effect.Selections {
		if got := f.decide(f.comfy, s.Permission.Action, "app:editor"); got != rwire.DecisionOutcomeNotGranted {
			t.Fatal("Refuse wrote grant", got)
		}
	}
}

func TestRuntimeApplicationPermissionNativeConsentLifecycle(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("fixture uses native Program IPC; no XPC proof")
	}
	for _, scenario := range []string{"allow", "reveal_allow", "propose_allow", "standing_allow", "refuse", "deadline", "caller_changed", "rights_unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			f := applicationPermissionRuntime(t)
			endpoint := testPipe("application-consent", "asks")
			h, err := asksservice.Listen(endpoint, f.a.book)
			if err != nil {
				t.Fatal(err)
			}
			type consentResult struct {
				outcome, reason string
				quiet           func(context.Context) error
			}
			result := make(chan consentResult, 1)
			changed := make(chan struct{})
			if err := h.EnableAnsweringRequest(func(ctx context.Context, peer *identity.Peer) rwire.Decision {
				budget := 3 * time.Second
				if scenario == "deadline" {
					budget = 150 * time.Millisecond
				}
				ctx, cancel := context.WithTimeout(ctx, budget)
				defer cancel()
				recheck := func(ctx context.Context) error {
					if err := ctx.Err(); err != nil {
						return err
					}
					select {
					case <-changed:
						return errors.New("captured caller changed")
					default:
						return nil
					}
				}
				class := "read"
				switch scenario {
				case "reveal_allow":
					class = "reveal"
				case "propose_allow":
					class = "propose"
				case "standing_allow":
					class = "edit"
				}
				outcome, reason, quiet := f.a.applicationPermissionConsent(ctx, peer, "app:editor", "Editor", class, "complete-native-scope-key", false, recheck)
				result <- consentResult{outcome, reason, quiet}
				return rwire.Decision{Outcome: rwire.DecisionOutcomeNotGranted}
			}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- h.Serve(t.Context()) }()
			t.Cleanup(func() {
				h.Close()
				select {
				case err := <-done:
					if err != nil {
						t.Error(err)
					}
				case <-time.After(3 * time.Second):
					t.Error("consent host did not join")
				}
			})
			called := make(chan error, 1)
			go func() { _, err := askclient.NewResponder(endpoint).RequestAnsweringContext(t.Context()); called <- err }()
			var pending asks.Record
			until := time.After(2 * time.Second)
			for pending.ID == "" {
				entries, _, err := f.a.book.ApplicationHistoryScoped()
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if entry.Record.TypedPermission != nil && entry.Record.Pending() {
						pending = entry.Record
						break
					}
				}
				if pending.ID != "" {
					break
				}
				select {
				case <-until:
					t.Fatal("no native application question")
				case <-time.After(time.Millisecond):
				}
			}
			if len(pending.TypedPermission.Effect.Selections) != 3 {
				t.Fatal("incomplete native request", pending)
			}
			if scenario == "caller_changed" {
				close(changed)
			}
			if scenario == "rights_unavailable" {
				if err := os.WriteFile(filepath.Join(filepath.Dir(f.panel.Program), "decisions.json"), []byte("broken policy"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario != "deadline" {
				option := "allow"
				if scenario == "refuse" || scenario == "rights_unavailable" {
					option = "refuse"
				}
				if _, _, err := f.a.book.AnswerTypedPermissionIfPendingAuthorized(pending.ID, option, func(asks.Record, string) error { return nil }); err != nil {
					t.Fatal(err)
				}
				if option == "allow" {
					edit, err := f.a.applyPermissionAllow(t.Context(), f.panel, f.revision(t), pending.ID, func() error { return nil })
					if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
						t.Fatal(edit, err)
					}
				}
			}
			var got consentResult
			select {
			case got = <-result:
			case <-time.After(4 * time.Second):
				t.Fatal("native consent did not settle")
			}
			want := "permitted"
			if scenario == "refuse" {
				want = "refused"
			}
			if scenario == "deadline" || scenario == "caller_changed" || scenario == "rights_unavailable" {
				want = "unavailable"
			}
			if got.outcome != want || (got.quiet != nil) != (want == "permitted") {
				t.Fatal(got.outcome, got.reason)
			}
			if err := <-called; err != nil {
				t.Fatal(err)
			}
			if got.quiet != nil {
				deny := false
				subject := pending.TypedPermission.Effect.Subject
				class := "read"
				switch scenario {
				case "reveal_allow":
					class = "reveal"
				case "propose_allow":
					class = "propose"
				case "standing_allow":
					class = "edit"
				}
				action, _ := grants.ApplicationCommandAction(class, "app:editor")
				if _, err := f.a.rights.policy.ChangeRule(f.revision(t), subject, action, "app:editor", rights.RuleEdit{Permit: &deny, By: f.panel, Exact: true}, func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if got.quiet(t.Context()) == nil {
					t.Fatal("quiet dispatch check ignored current deny")
				}
			}
		})
	}
}

func typedRuntime(t *testing.T) (*runtimeAsks, rwire.Subject) {
	t.Helper()
	a, _ := firstUseBook(t)
	base, err := rights.NewDefinitionCatalog(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	definition := rwire.SelectorPermissionDefinition{Definition: rwire.PermissionDefinition{Action: typedTestAction, OwnerContract: "example.resources/reader@1", Operations: []string{"example.resources/reader@1#Read"}, ResourceProfile: typedTestProfile, ResourceType: "example.Target", EnforcedBy: []string{"example.resources/reader@1"}, Disclosure: "Returns raw values to this application", Lifecycle: "Account-owned name survives rotation", Served: true}, Label: "Read saved values", SupportedSelectors: []rwire.ResourceSelectorKind{rwire.ResourceSelectorKindOne, rwire.ResourceSelectorKindAll}, FirstUseSelectors: []rwire.ResourceSelectorKind{rwire.ResourceSelectorKindOne, rwire.ResourceSelectorKindAll}, Collection: "saved values in this account", AllEffect: "All present and future saved values", RequestType: "example.Target"}
	valid := func(b []byte) bool {
		v, e := rwire.DecodeRequest(b)
		return e == nil && v.Op == "named-target" && v.Name != "" && bytes.Equal(b, rwire.EncodeRequest(v))
	}
	catalog, err := base.WithSelectors([]rwire.SelectorPermissionDefinition{definition}, map[string]rights.ResourceProfile{typedTestProfile: {Prepare: func(b []byte) ([]byte, error) {
		if !valid(b) {
			return nil, errors.New("invalid owning target")
		}
		return slices.Clone(b), nil
	}, Validate: valid, Equal: bytes.Equal, AllowAll: true, Describe: func(b []byte) (string, error) {
		v, e := rwire.DecodeRequest(b)
		if e != nil {
			return "", e
		}
		return v.Name, nil
	}}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicyWithDefinitions(filepath.Join(t.TempDir(), "rights.json"), []string{typedTestAction}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: a.rights.owner, Program: identity.CanonicalProgramPath(program)}
	a.rights.policy, a.rights.typedFirstUse = policy, a.admitTypedService
	return a, subject
}
func typedTestSelection(all bool) rwire.PermissionSelection {
	s := rwire.PermissionSelection{Permission: rwire.PermissionKey{Action: typedTestAction, ResourceProfile: typedTestProfile}, Selector: rwire.ResourceSelector{Kind: rwire.ResourceSelectorKindAll}}
	if !all {
		s.Selector.Kind = rwire.ResourceSelectorKindOne
		s.Selector.Target = rwire.EncodeRequest(&rwire.Request{Op: "named-target", Name: "test saved value"})
	}
	return s
}

func TestRuntimeTypedPermissionNativeRequestAllowAtomicRetryUndo(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("fixture uses native non-XPC Program transport; no Mac proof claimed")
	}
	a, subject := typedRuntime(t)
	endpoint := listen.Endpoint(fmt.Sprintf("runtime-typed-%d-%d", os.Getpid(), time.Now().UnixNano()))
	rightsHost, err := rightsservice.Listen(endpoint+"-rights", a.rights.policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = rightsHost.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = rightsHost.EnablePermissionEffects(a.applyPermissionAllow, a.undoPermissionEffect); err != nil {
		t.Fatal(err)
	}
	rightsDone := make(chan error, 1)
	go func() { rightsDone <- rightsHost.Serve(context.Background()) }()
	t.Cleanup(func() {
		rightsHost.Close()
		select {
		case err := <-rightsDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("Rights host did not drain")
		}
	})
	h, err := asksservice.Listen(endpoint, a.book)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermission(a.preparePermission); err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePermissionObservation(a.observePermission); err != nil {
		t.Fatal(err)
	}
	stopFollowing := a.followRights(t.Context())
	defer stopFollowing()
	if err = h.EnableOperator(func(context.Context, *identity.Peer) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err = h.EnableAskPolicy(func(context.Context, *identity.Peer, string) error { return errors.New("no ordinary asking grant") }); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(context.Background()) }()
	t.Cleanup(func() {
		h.Close()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(2 * time.Second):
			t.Error("typed host did not drain")
		}
	})
	ctx := context.Background()
	app, operator := askclient.NewPermission(endpoint), askclient.NewPermissionOperator(endpoint)
	selection := typedTestSelection(true)
	intent := askswire.PermissionIntent{RequestKey: "typed-all", Selection: &selection, Purpose: "Read saved values"}
	observation, err := app.RequestContext(ctx, intent)
	if err != nil || observation.Outcome != askswire.PermissionOutcomePending || observation.Answer == nil {
		t.Fatalf("request %+v %v", observation, err)
	}
	id := observation.Answer.ID
	details, err := askclient.ReadPermissionDetailsContext(ctx, operator, subject.Account, id)
	if err != nil || details.Effect.Subject != subject || details.Display.Selections[0].AllEffect != "All present and future saved values" {
		t.Fatalf("details %+v %v", details, err)
	}
	if _, err = operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil {
		t.Fatal(err)
	}
	beforeCommit, err := app.ObserveQuestionContext(ctx, id, 0)
	if err != nil || beforeCommit.Observation.Outcome != askswire.PermissionOutcomePending {
		t.Fatal("recorded Allow supplied authority", beforeCommit, err)
	}
	observed := make(chan askswire.PendingQuestion, 1)
	go func() {
		out, e := app.ObserveQuestionContext(ctx, id, 1000)
		if e != nil {
			t.Error(e)
		}
		observed <- out
	}()
	waitUntil := time.Now().Add(time.Second)
	for a.book.Gate().Waiting() == 0 {
		if time.Now().After(waitUntil) {
			t.Fatal("permission observation did not wait")
		}
		time.Sleep(time.Millisecond)
	}
	policyClient := rightsclient.NewSelectorOperator(endpoint + "-rights")
	result, err := grants.RetryPermissionEffect(ctx, operator, policyClient, subject.Account, id)
	if err != nil || result.Edit == nil || result.Edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("effect %+v %v", result, err)
	}
	select {
	case out := <-observed:
		if out.Observation.Outcome != askswire.PermissionOutcomePermitted || out.Observation.PolicyRevision == "" {
			t.Fatal(out)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("committed effect failed to wake permission observer")
	}
	current := a.rights.policy.CheckPermissionEffectContext(ctx, *details.Effect, "")
	if permissionDisposition(current, 1) != askswire.PermissionOutcomePermitted {
		t.Fatalf("retry not permitted: %+v", current)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := policyClient.ApplyPermissionAllowContext(ctx, revision, id)
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeConflict {
		t.Fatalf("receipt replay %+v %v", edit, err)
	}
	revision, _, _ = a.rights.policy.SelectorSnapshotContext(ctx)
	edit, err = policyClient.UndoPermissionEffectContext(ctx, revision, id)
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("Undo %+v %v", edit, err)
	}
	current = a.rights.policy.CheckPermissionEffectContext(ctx, *details.Effect, "")
	if permissionDisposition(current, 1) != askswire.PermissionOutcomePending {
		t.Fatalf("Undo left permission %+v", current)
	}
	afterUndo, err := app.ObserveQuestionContext(ctx, id, 0)
	if err != nil || afterUndo.Observation.Outcome != askswire.PermissionOutcomePending {
		t.Fatal("old Allow survived Undo", afterUndo, err)
	}
	revision, _, _ = a.rights.policy.SelectorSnapshotContext(ctx)
	edit, err = policyClient.ApplyPermissionAllowContext(ctx, revision, id)
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeConflict || permissionDisposition(a.rights.policy.CheckPermissionEffectContext(ctx, *details.Effect, ""), 1) != askswire.PermissionOutcomePending {
		t.Fatalf("completed occurrence revived Undo: %+v %v", edit, err)
	}
	observation, err = app.RequestContext(ctx, intent)
	if err != nil || observation.Outcome != askswire.PermissionOutcomeAnswered || observation.Answer.ID != id {
		t.Fatalf("immutable request replay %+v %v", observation, err)
	}
}

func TestRuntimeTypedServiceAdmissionPreservesCapturedTarget(t *testing.T) {
	a, subject := typedRuntime(t)
	selection := typedTestSelection(false)
	operation := rwire.ConcreteResource{Permission: selection.Permission, Target: selection.Selector.Target}
	decision := a.rights.decideTypedAsking(context.Background(), subject, operation)
	if decision.Outcome != rwire.BatchCheckOutcomeChecked || decision.Outcomes[0] != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision %+v", decision)
	}
	records, _, err := a.book.ApplicationHistory()
	if err != nil || len(records) != 1 || records[0].TypedPermission == nil || records[0].TypedPermission.Origin != "service" || records[0].Publisher != "" || !bytes.Equal(records[0].TypedPermission.Effect.Selections[0].Selector.Target, operation.Target) {
		t.Fatalf("service admission %+v %v", records, err)
	}
	if _, _, err = a.book.AnswerTypedPermissionIfPendingAuthorized(records[0].ID, "refuse", func(asks.Record, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	a.rights.decideTypedAsking(context.Background(), subject, operation)
	records, _, err = a.book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("refusal pacing %+v %v", records, err)
	}
}

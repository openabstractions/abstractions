package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

func TestNativeGraphEditAllowAndRevocation(t *testing.T) {
	ctx := t.Context()
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, false)
	d, a := f.g.directory, permission.a
	a.rights.applications = d
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	a.rights.policy.Definitions = catalog
	d.mu.Lock()
	lease := d.instances[f.instance]
	d.mu.Unlock()
	if out := d.remove(ctx, f.subject, "fixture"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if err := d.attachApplicationV3(ctx, f.g.decoder); err != nil {
		t.Fatal(err)
	}
	run := &selfRegisteredRun{f: permission, d: d, ctx: ctx}
	run.k = a.keepSelfRegistered(ctx, d)
	agent1478Attach(t, run)
	a.keepAgentEnrollments(ctx, d)

	agent1478Register(t, run, catalogueAgentApplication, f.subject.Program)
	enrolled := agent1478Set(t, run, catalogueAgentApplication, "", true)
	keepQuestion := agent1478Question(t, run, catalogueAgentApplication)
	answer(t, permission, keepQuestion.ID, "keep")
	agent1478Wait(t, run, catalogueAgentApplication, "enrolled")
	local := t.TempDir()
	official, _ := product.OfficialAgentProgram(catalogueHostParty)
	hostProgram := filepath.Join(local, filepath.FromSlash(official.LocalAppData))
	if err := os.MkdirAll(filepath.Dir(hostProgram), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostProgram, []byte("official fixture image"), 0o600); err != nil {
		t.Fatal(err)
	}
	agent1478Register(t, run, catalogueHostParty, hostProgram)
	open := func(ctx context.Context, peer fileid.Peer, path string) (*heldApplicationManifest, error) {
		h, err := openHeldApplicationImage(ctx, peer, path)
		if err == nil {
			h.Code, h.Proof = identity.Code{Subject: official.Signer, Trusted: true}, identity.ProofBound
		}
		return h, err
	}
	held, source, err := holdCatalogueHost(ctx, fileid.Self, catalogueHostParty, local, open)
	if err != nil {
		t.Fatal(err)
	}
	_ = held.Close()
	ref, err := applicationToken()
	if err != nil {
		t.Fatal(err)
	}
	host := catalogueHostRecord{Reference: ref, Source: catalogueHostSource, Image: source}
	d.catalogueHostHold = func(ctx context.Context, host catalogueHostRecord) (*heldApplicationManifest, error) {
		return holdKeptCatalogueHost(ctx, fileid.Self, catalogueHostParty, local, host.Reference, host, open)
	}
	d.mu.Lock()
	mark := d.unconfirmed[catalogueAgentApplication]
	d.mu.Unlock()
	if err := writeCatalogueCompanion(ctx, d.dir, catalogueEnrollmentCompanion{Version: 1, App: catalogueAgentApplication, Revision: enrolled.Revision, Question: keepQuestion.ID, Image: mark.ProgramKey, Host: host}, mark); err != nil {
		t.Fatal(err)
	}

	spec := workflowApplicationPackage()
	program := filepath.Join(t.TempDir(), "picture-fixture.exe")
	f.manifest.Name, f.manifest.Title = spec.Registration, spec.Title
	f.manifest.Offers.Commands = []appmodel.DeclaredCommand{{Name: grants.GraphEditCommand, Class: appmodel.CommandClassEdit}}
	f.description.Kinds = []appmodel.Kind{{Name: "graph", Schema: `{}`, Fields: []appmodel.KindField{}}}
	f.description.Commands = []appmodel.CommandSpec{{Name: grants.GraphEditCommand, Title: "Edit", Class: appmodel.CommandClassEdit,
		InputSchema: `{"type":"object","properties":{"ops":{"type":"array","minItems":1,"maxItems":64,"items":{"type":"object"}}},"required":["ops"],"additionalProperties":false}`, OutputSchema: `{}`, Targets: []string{}}}
	if err := os.WriteFile(program, manifestPE(registration.EncodeManifest(f.manifest), false), 0o600); err != nil {
		t.Fatal(err)
	}
	picture := &unsignedRun{selfRegisteredRun: run, program: program, subject: rights.Subject{Account: d.owner, Program: identity.NormalizeSubjectProgram(program)}}
	picture.register(t)
	kept := picture.keep(t)
	if kept.KeepIdentity == "" || kept.KeepIdentity != kept.FileRevision {
		t.Fatalf("initial Keep did not retain an identity: %+v", kept)
	}
	bind := func(revision string) {
		f.s.bindMu.Lock()
		f.s.binding.application, f.s.binding.manifestRevision = spec.Registration, revision
		f.s.bindMu.Unlock()
		lease.application = spec.Registration
		d.mu.Lock()
		d.instances[f.instance] = lease
		d.mu.Unlock()
	}
	bind(kept.Revision)
	f.g.load = f.g.loadDeclaration
	f.mu.Lock()
	f.answer = func(name string, _ json.RawMessage) []byte {
		if name == "app_contexts" {
			revision := "r" + strconv.Itoa(int(f.effects.Load())+1)
			raw, _ := appmodel.ToJSON(&appmodel.ContextPage{Outcome: appmodel.ReadingOutcomePage, Contexts: []appmodel.ContextEntry{{Context: appmodel.ContextRef{Instance: f.instance, Context: "view", Epoch: "e1"}, Revision: revision}}})
			return raw
		}
		if name == grants.GraphEditCommand {
			revision := "r" + strconv.Itoa(int(f.effects.Add(1))+1)
			raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Revision: revision, Effects: `{}`})
			return raw
		}
		return nil
	}
	f.mu.Unlock()
	if err := a.rights.policy.Set(f.subject, "abstraction.facade/command.edit", "app:"+spec.Registration, true); err != nil {
		t.Fatal(err)
	}
	questions, policy := graphEditTestOperators(t, a)
	caller := f.client(12 * time.Second)
	call := appmodel.GatewayCall{Application: spec.Registration, Instance: f.instance, Tool: grants.GraphEditCommand,
		Arguments: `{"ops":[{"op":"set_mode","node":"7","mode":"mute"}]}`}
	start := func(n int) (string, <-chan applications.CallResult) {
		done, key := make(chan applications.CallResult, 1), receiptKey(n, time.Now())
		go func() {
			result, err := caller.Call(key, call)
			if err != nil {
				t.Error(err)
			}
			done <- result
		}()
		return key, done
	}
	question := func(done <-chan applications.CallResult) asks.Record {
		until := time.NewTimer(6 * time.Second)
		defer until.Stop()
		for {
			if rows := pendingApplicationQuestions(t, permission); len(rows) == 1 {
				return rows[0]
			}
			select {
			case result := <-done:
				t.Fatalf("no question before result: %+v, answer %+v", result, result.Answer)
			case <-time.After(5 * time.Millisecond):
			case <-until.C:
				t.Fatal("no graph permission question")
			}
		}
	}
	requireApplied := func(result applications.CallResult) {
		t.Helper()
		if result.Answer != nil && result.Answer.Result != nil && result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
			t.Fatalf("native graph edit refused: %+v", *result.Answer.Result)
		}
		if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied {
			t.Fatalf("native graph edit failed: %+v, answer %+v", result, result.Answer)
		}
	}
	onceKey, oncePending := start(903)
	onceQuestion := question(oncePending)
	if !slices.Equal(onceQuestion.Options, []string{"once", "allow", "refuse"}) {
		t.Fatalf("graph consent options: %v", onceQuestion.Options)
	}
	onceAnswer, err := grants.AnswerPermissionQuestion(ctx, questions, policy, d.owner, onceQuestion.ID, "once")
	if err != nil || onceAnswer.Decision.Outcome != askswire.OperatorDecisionOutcomeAnswered || onceAnswer.Edit != nil {
		t.Fatalf("Allow once wrote a rule or failed: %+v %v", onceAnswer, err)
	}
	requireApplied(<-oncePending)
	onceReplay, err := caller.Call(onceKey, call)
	if err != nil {
		t.Fatal(err)
	}
	requireApplied(onceReplay)
	if f.effects.Load() != 1 {
		t.Fatal("Allow once replay dispatched again")
	}
	key, pending := start(904)
	request := question(pending)
	if request.TypedPermission == nil || request.TypedPermission.Effect.Subject.Program != host.Image.Program {
		t.Fatalf("question is not for the proved host: %+v", request)
	}
	effect := request.TypedPermission.Effect
	target, ok := grants.KeptGraphEditTarget(effect)
	if !ok || target.Keep != kept.KeepIdentity || target.Enrollment != enrolled.Revision || target.Application != spec.Registration || target.Command != grants.GraphEditCommand || target.Class != "edit" {
		t.Fatalf("wrong permission scope: %+v", target)
	}
	peer, _ := applicationV3NativePeer(t)
	intent := askswire.PermissionIntent{RequestKey: "app-direct-request", Selection: &effect.Selections[0]}
	if result, err := a.preparePermission(ctx, peer, intent); err != nil || result.Outcome == askswire.PermissionOutcomePending {
		t.Fatalf("direct Asks request was admitted: %+v %v", result, err)
	}
	// The Allow verifies the requester's image in full, with an unchanged
	// file and a fresh cached verdict for it.
	read := d.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	var requesterReads atomic.Int64
	d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		if identity.SameSubjectProgram(program, mark.Program) {
			requesterReads.Add(1)
		}
		return read(ctx, program, binding)
	}
	if _, err := d.admittedAgentImage(ctx, mark.Program, mark.Agent.Binding); err != nil {
		t.Fatal(err)
	}
	cached := requesterReads.Load()
	allow, err := grants.AnswerPermissionQuestion(ctx, questions, policy, d.owner, request.ID, "allow")
	if err != nil || allow.Edit == nil || allow.Edit.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatalf("save graph Allow: %+v %v", allow, err)
	}
	if requesterReads.Load() == cached {
		t.Fatal("graph Allow trusted the cached requester image verdict")
	}
	requireApplied(<-pending)
	replay, err := caller.Call(key, call)
	if err != nil {
		t.Fatal(err)
	}
	requireApplied(replay)
	if f.effects.Load() != 2 {
		t.Fatal("receipt replay dispatched again")
	}
	for _, class := range []string{"edit", "commit", "spend", "destroy"} {
		if got := a.rights.decide(ctx, effect.Subject, "abstraction.facade/command."+class, "app:"+spec.Registration); got.Outcome == rights.DecisionOutcomePermitted {
			t.Fatalf("the lasting edit added a legacy %s grant: %+v", class, got)
		}
	}
	disabled := d.changeApplicationV3(ctx, permission.panel, spec.Registration, kept.Revision, "disabled")
	enabled := d.changeApplicationV3(ctx, permission.panel, spec.Registration, disabled.Revision, "enabled")
	if disabled.Outcome != legacy.ApplicationOutcomeApplied || enabled.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(disabled, enabled)
	}
	bind(enabled.Revision)
	_, next := start(905)
	requireApplied(<-next)
	if edited, err := grants.UndoPermissionQuestion(ctx, questions, policy, d.owner, request.ID); err != nil || edited.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatalf("revoke: %+v %v", edited, err)
	}
	_, revoked := start(906)
	again := question(revoked)
	if again.ID == request.ID || again.TypedPermission.Digest != request.TypedPermission.Digest {
		t.Fatalf("revocation did not ask afresh for the same scope: %+v", again)
	}
	allow, err = grants.AnswerPermissionQuestion(ctx, questions, policy, d.owner, again.ID, "allow")
	if err != nil || allow.Edit == nil || allow.Edit.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatalf("save new Allow after revocation: %+v %v", allow, err)
	}
	requireApplied(<-revoked)
	if out := d.changeApplicationV3(ctx, permission.panel, spec.Registration, enabled.Revision, "remove"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	picture.register(t)
	replacement := picture.keep(t)
	if replacement.KeepIdentity == kept.KeepIdentity {
		t.Fatal("replacement inherited the removed Keep")
	}
	bind(replacement.Revision)
	_, rebound := start(907)
	fresh := question(rebound)
	newTarget, ok := grants.KeptGraphEditTarget(fresh.TypedPermission.Effect)
	if !ok || newTarget.Keep != replacement.KeepIdentity || fresh.TypedPermission.Digest == request.TypedPermission.Digest {
		t.Fatalf("replacement did not ask for a new Keep scope: %+v", fresh)
	}
	if _, err := grants.AnswerPermissionQuestion(ctx, questions, policy, d.owner, fresh.ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	<-rebound
	if f.effects.Load() != 4 {
		t.Fatalf("replacement inherited a previous Allow: %d", f.effects.Load())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]int{}
	for _, record := range f.records {
		if record.Command == grants.GraphEditCommand && record.Outcome == "applied" && record.RevisionBefore != "" && record.RevisionAfter != record.RevisionBefore {
			seen[record.RevisionAfter]++
		}
	}
	if len(seen) != 4 || seen["r2"] != 1 || seen["r3"] != 1 || seen["r4"] != 1 || seen["r5"] != 1 {
		t.Fatalf("activity revisions: %v, records: %+v", seen, f.records)
	}
}

func graphEditTestOperators(t *testing.T, a *runtimeAsks) (*askclient.PermissionOperator, *rightsclient.SelectorOperator) {
	t.Helper()
	endpoint := testPipe("kept-graph-edit", "asks")
	h, err := asksservice.Listen(endpoint, a.book)
	if err != nil {
		t.Fatal(err)
	}
	operator := func(context.Context, *identity.Peer) error { return nil }
	if err := h.EnableOperator(operator); err != nil {
		t.Fatal(err)
	}
	policyEndpoint := testPipe("kept-graph-edit", "rights")
	r, err := rightsservice.Listen(policyEndpoint, a.rights.policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.EnableOperator(operator); err != nil {
		t.Fatal(err)
	}
	if err := r.EnablePermissionEffects(a.applyPermissionAllow, a.undoPermissionEffect); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	go func() { done <- h.Serve(t.Context()) }()
	go func() { done <- r.Serve(t.Context()) }()
	t.Cleanup(func() {
		h.Close()
		r.Close()
		for range 2 {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	})
	return askclient.NewPermissionOperator(endpoint), rightsclient.NewSelectorOperator(policyEndpoint)
}

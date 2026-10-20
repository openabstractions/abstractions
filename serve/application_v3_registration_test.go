package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	asks "github.com/openabstractions/abstraction-asks/go"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// These fixtures exercise persistence through real raw CAS and native files.
// Synthetic code provenance supplies no signed native positive or trust claim.
func registration1459Program(name string) string {
	return filepath.Join(os.TempDir(), "Registration1459-fixture", name)
}

func registration1459Decoder(raw []byte, program string, _ *heldApplicationManifest) (wire.ApplicationDescriptor, string, error) {
	var v wire.ApplicationDescriptor
	err := json.Unmarshal(raw, &v)
	if err != nil || !validApplicationDescriptor(v) || v.Program != program {
		return v, "", errors.New("fixture manifest")
	}
	return v, applicationV3Hash(raw), nil
}

func registration1459Directory(t *testing.T, state string) *applicationDirectory {
	t.Helper()
	d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := d.attachApplicationV3(context.Background(), registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	d.operator = func(p string) bool { return p == "operator" }
	return d
}

func TestApplicationV3DeclaredRoleProjectionCommitReloadAndRemoval(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	// The production decoder reads the validated Manifest.ScriptHost; this
	// inert storage fixture uses its existing title field as that pure bit.
	d.roleDecode = func(raw []byte, _ string) (bool, error) {
		var v wire.ApplicationDescriptor
		err := json.Unmarshal(raw, &v)
		return v.Title == "Host", err
	}
	if d.roles.Load() != nil {
		t.Fatal("unattached role owner looked available")
	}
	if err := d.refreshRoles(ctx); err != nil {
		t.Fatal(err)
	}
	if roles := d.roles.Load(); roles == nil || len(roles.hosts) != 0 {
		t.Fatal("verified empty", roles)
	}
	program := registration1459Program("role.exe")
	revision := registration1459Publish(t, d, "role", program, "Host", "operator", "")
	if roles := d.roles.Load(); roles == nil || len(roles.hosts) != 1 || roles.hosts[0].FileKey == "" {
		t.Fatal("host publication", roles)
	}
	old := d.roles.Load()
	revision = registration1459Publish(t, d, "role", program, "Ordinary", "operator", revision)
	if roles := d.roles.Load(); roles == nil || len(roles.hosts) != 0 || len(old.hosts) != 1 {
		t.Fatal("false update or immutable prior view", roles)
	}
	registration1459Publish(t, d, "alias", program, "Host", "operator", "")
	if roles := d.roles.Load(); roles == nil || len(roles.hosts) != 1 {
		t.Fatal("true alias", roles)
	}
	// A failed callback and a corrupt reload invalidate the complete view.
	d.mu.Lock()
	err := d.withRegistrationStorageLocked(ctx, func(map[string]wire.ApplicationDescriptor) error { return errors.New("failed commit") })
	d.mu.Unlock()
	if err == nil || d.roles.Load() != nil {
		t.Fatal("failed commit retained safe-looking facts")
	}
	if err := d.refreshRoles(ctx); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(d.v3.dir, "index.json")
	bytes, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(index, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshRoles(ctx); err == nil || d.roles.Load() != nil {
		t.Fatal("corrupt reload retained facts", err)
	}
	if err := os.WriteFile(index, bytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.refreshRoles(ctx); err != nil {
		t.Fatal(err)
	}
	aliasRevision := d.v3.entries["alias"].Revision
	s := rights.Subject{Account: "account", Program: "operator"}
	if got := d.changeApplicationV3(ctx, s, "alias", aliasRevision, "remove"); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	if roles := d.roles.Load(); roles == nil || len(roles.hosts) != 0 {
		t.Fatal("remove", roles)
	}
	_ = revision
}

func TestApplicationV3DeclaredRoleExternalCommitReachesQuietAndAnswerUse(t *testing.T) {
	ctx := context.Background()
	f := newAnswererFixture(t)
	f.enroll(t, f.answerer)
	question := f.ask(t, f.comfy, "abstraction.model/lookup", "hf")
	state := t.TempDir()
	first := registration1459Directory(t, state)
	second := registration1459Directory(t, state)
	decodeRole := func(raw []byte, _ string) (bool, error) {
		var v wire.ApplicationDescriptor
		err := json.Unmarshal(raw, &v)
		return v.Title == "Host", err
	}
	first.roleDecode, second.roleDecode = decodeRole, decodeRole
	if err := first.refreshRoles(ctx); err != nil {
		t.Fatal(err)
	}
	f.a.rights.applications = first
	f.a.rights.policy.RefreshMetaHolder = first.refreshRoles
	revision := registration1459Publish(t, second, "external", f.answerer.Program, "Host", "operator", "")
	// No intervening Applications read may be required for a current quiet
	// query or actual answer-use to observe the committed conservative veto.
	got := f.a.rights.policy.ExplainForContext(ctx, f.answerer, asks.AnswerAction, rightspolicy.MetaScopeApps)
	if got.Outcome != rights.DecisionOutcomeForbidden || got.PolicyRevision != "" || got.Source != nil {
		t.Errorf("external commit quiet query: %+v", got)
	}
	if f.answerable(f.me(), question, "allow") {
		t.Error("external commit still answered through retained enrollment")
	}
	// Independent operations share their original parent, never the returned
	// preparation context. A later false and true each reach the next operation.
	revision = registration1459Publish(t, second, "external", f.answerer.Program, "Ordinary", "operator", revision)
	if got := f.a.rights.policy.ExplainForContext(ctx, f.answerer, asks.AnswerAction, rightspolicy.MetaScopeApps); got.Outcome != rights.DecisionOutcomePermitted {
		t.Fatal("same parent false update", got)
	}
	registration1459Publish(t, second, "external", f.answerer.Program, "Host", "operator", revision)
	if f.answerable(f.me(), question, "allow") {
		t.Error("independent answer-use missed later true without a preceding quiet query")
	}
}

func TestApplicationV3DeclaredRoleCancelledOuterReloadInvalidatesFacts(t *testing.T) {
	f := newAnswererFixture(t)
	d := registration1459Directory(t, t.TempDir())
	d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := d.refreshRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.a.rights.applications = d
	entered := make(chan struct{})
	f.a.rights.policy.RefreshMetaHolder = func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		return d.refreshRoles(ctx)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan rights.DecisionExplanation, 1)
	go func() {
		done <- f.a.rights.policy.ExplainForContext(ctx, f.answerer, asks.AnswerAction, rightspolicy.MetaScopeApps)
	}()
	<-entered
	cancel()
	select {
	case got := <-done:
		if got.Outcome != rights.DecisionOutcomeUnavailable || got.PolicyRevision != "" || got.Source != nil || d.roles.Load() != nil {
			t.Fatal("cancelled refresh retained facts", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled refresh did not join")
	}
}

func registration1459File(t *testing.T, name, program, title string) (wire.ApplicationDescriptor, applicationV3ManifestFile) {
	t.Helper()
	v := wire.ApplicationDescriptor{Name: name, Program: program, Title: title}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return v, applicationV3ManifestFile{Raw: raw, Digest: applicationV3Hash(raw), Source: applicationV3Source{Program: program, FileKey: applicationV3Hash([]byte(program)), Code: identity.Code{Trusted: true, Subject: "synthetic test provenance"}, Proof: identity.ProofBound}}
}

func TestApplicationV3RegistrationRetainedIdentityUsesCapturedKeys(t *testing.T) {
	_, f := registration1459File(t, "captured", registration1459Program("absent.exe"), "Captured")
	alias := f.Source
	alias.Program = registration1459Program("also-absent.exe")
	if !f.Source.valid() || !alias.valid() || !f.Source.sameProgram(alias) {
		t.Fatal("captured identity must remain usable as provenance without reopening absent programs")
	}
	alias.FileKey = "different-captured-key"
	if f.Source.sameProgram(alias) {
		t.Fatal("distinct captured identities matched")
	}
}

func registration1459SetRaw(t *testing.T, f *applicationV3ManifestFile, v wire.ApplicationDescriptor) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	f.Raw = raw
	f.Digest = applicationV3Hash(raw)
}

func registration1459Publish(t *testing.T, d *applicationDirectory, name, program, title, source, revision string) string {
	t.Helper()
	v, f := registration1459File(t, name, program, title)
	out := d.publishApplicationV3(context.Background(), v, f, source, revision)
	if out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("publish: %+v", out)
	}
	return d.v3.entries[name].Revision
}

func TestApplicationV3RegistrationRestartAndLegacyFence(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	revision := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "operator", "")
	reopened := registration1459Directory(t, state)
	if reopened.v3.entries["native"].Revision != revision || reopened.descriptors["native"].Title != "Native" {
		t.Fatal("restart lost projection/revision")
	}
	if len(reopened.v3.files["native"].Raw) == 0 {
		t.Fatal("raw missing")
	}
	b, err := cas.ReadLimit(filepath.Join(d.dir, "descriptors.json"), 600<<10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "native") {
		t.Fatal("@3 data leaked into legacy store")
	}
	s := rights.Subject{Account: "account", Program: "operator"}
	if out := reopened.remove(ctx, s, "native"); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatalf("legacy remove: %+v", out)
	}
	if out := reopened.addDescriptor(ctx, wire.ApplicationDescriptor{Name: "native", Program: registration1459Program("other.exe"), Title: "Other"}, nil); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatalf("legacy add: %+v", out)
	}
	if out := reopened.activateInSession(ctx, s, "session", "native"); out.Outcome != wire.ApplicationActivationOutcomeDisabled {
		t.Fatalf("legacy activation: %+v", out)
	}
}

func TestApplicationV3RegistrationLegacyCreateCollision(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	v := wire.ApplicationDescriptor{Name: "legacy", Program: registration1459Program("legacy.exe"), Title: "Legacy"}
	if out := d.addDescriptor(ctx, v, nil); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	native, f := registration1459File(t, "legacy", registration1459Program("native.exe"), "Native")
	if out := d.publishApplicationV3(ctx, native, f, "operator", ""); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal(out)
	}
	if len(d.v3.entries) != 0 || d.descriptors["legacy"].Program != v.Program {
		t.Fatal("collision mutated state")
	}
}

func TestApplicationV3RegistrationTwoOwnersSharedCapacity(t *testing.T) {
	for _, first := range []string{"legacy", "v3"} {
		t.Run(first, func(t *testing.T) {
			ctx := context.Background()
			state := t.TempDir()
			a := registration1459Directory(t, state)
			for i := 0; i < 63; i++ {
				if out := a.addDescriptor(ctx, wire.ApplicationDescriptor{Name: fmt.Sprintf("app%d", i), Program: registration1459Program(fmt.Sprintf("program%d.exe", i)), Title: "App"}, nil); out.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(out)
				}
			}
			b := registration1459Directory(t, state)
			v, f := registration1459File(t, "native", registration1459Program("native.exe"), "Native")
			legacy := wire.ApplicationDescriptor{Name: "last", Program: registration1459Program("last.exe"), Title: "Last"}
			if first == "legacy" {
				if out := a.addDescriptor(ctx, legacy, nil); out.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(out)
				}
				if out := b.publishApplicationV3(ctx, v, f, "operator", ""); out.Outcome == wire.ApplicationOutcomeApplied {
					t.Fatal("65th admitted")
				}
			} else {
				if out := b.publishApplicationV3(ctx, v, f, "operator", ""); out.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(out)
				}
				if out := a.addDescriptor(ctx, legacy, nil); out.Outcome == wire.ApplicationOutcomeApplied {
					t.Fatal("65th admitted")
				}
			}
			reopened := registration1459Directory(t, state)
			if len(reopened.descriptors) != 64 {
				t.Fatalf("count=%d", len(reopened.descriptors))
			}
		})
	}
}

func TestApplicationV3RegistrationPendingAndQuestionRevisions(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	r1 := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "First", "self", "")
	if !d.setQuestionForRevision(ctx, "native", registration1459Program("native.exe"), "old-question", r1) {
		t.Fatal("question refused")
	}
	r2 := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Second", "self", r1)
	if d.setQuestionForRevision(ctx, "native", registration1459Program("native.exe"), "late-old-question", r1) || d.keepOwned(ctx, "native", registration1459Program("native.exe"), "old-question") {
		t.Fatal("stale question attached/enabled new manifest")
	}
	if d.v3.entries["native"].State != "pending" {
		t.Fatal("update promoted authority")
	}
	if d.setQuestion(ctx, "native", registration1459Program("native.exe"), "unfenced") {
		t.Fatal("legacy wrapper attaches @3 question")
	}
	if !d.setQuestionForRevision(ctx, "native", registration1459Program("native.exe"), "new-question", r2) {
		t.Fatal("new question refused")
	}
	reopened := registration1459Directory(t, state)
	if !reopened.keepOwned(ctx, "native", registration1459Program("native.exe"), "new-question") || reopened.v3.entries["native"].State != "enabled" {
		t.Fatal("actual current answer lost across restart")
	}
	if reopened.keepOwned(ctx, "native", registration1459Program("native.exe"), "new-question") {
		t.Fatal("answer applied twice")
	}
}

func TestApplicationV3RegistrationSharedPendingAcrossOwners(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	a := registration1459Directory(t, state)
	b := registration1459Directory(t, state)
	registration1459Publish(t, a, "native", registration1459Program("same.exe"), "Native", "self", "")
	mark := newSelfMark(registration1459Program("same.exe"), asks.Publisher{})
	if out := b.addDescriptor(ctx, wire.ApplicationDescriptor{Name: "legacy", Program: registration1459Program("same.exe"), Title: "Legacy"}, &mark); out.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("legacy second pending admitted")
	}
	reopened := registration1459Directory(t, state)
	if len(reopened.descriptors) != 1 {
		t.Fatal("partial legacy mark became entry")
	}
	other, f := registration1459File(t, "other", registration1459Program("same.exe"), "Other")
	if out := reopened.publishApplicationV3(ctx, other, f, "self", ""); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal(out)
	}
}

func TestApplicationV3RegistrationRetainedCorruptionAndOrphan(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	r := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "operator", "")
	// A crash before index publication leaves inert immutable bytes.
	_, orphan := registration1459File(t, "orphan", registration1459Program("orphan.exe"), "Orphan")
	encoded, _ := json.Marshal(orphan)
	orphanPath := filepath.Join(d.v3.dir, strings.Repeat("a", 48)+".json")
	if err := os.WriteFile(orphanPath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.withRegistrationStorageLocked(ctx, func(map[string]wire.ApplicationDescriptor) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("bounded recovery did not reclaim orphan", err)
	}
	if err := os.WriteFile(d.v3.filePath(r), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := d.withRegistrationStorageLocked(ctx, func(map[string]wire.ApplicationDescriptor) error { return nil }); err == nil {
		t.Fatal("corrupt retained bytes accepted")
	}
	if got := d.changeApplicationV3(ctx, rights.Subject{Account: "account", Program: "operator"}, "native", r, "enabled"); got.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("corruption promoted")
	}
}

func TestApplicationV3RegistrationRemovalFencesAndNoImplicitAuthority(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	events := []string{}
	d.v3Changed = func(name, revision string) {
		if !d.mu.TryLock() {
			t.Fatal("mediation callback under directory lock")
		}
		d.mu.Unlock()
		events = append(events, name+":"+revision)
	}
	r := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "self", "")
	if !d.setQuestionForRevision(ctx, "native", registration1459Program("native.exe"), "question", r) {
		t.Fatal("current Remove failed")
	}
	blocks := 0
	if err := d.removeSelfRegistered(ctx, "native", registration1459Program("native.exe"), "question", func() error { blocks++; return nil }); err != nil {
		t.Fatal(err)
	}
	if blocks != 1 || len(events) != 2 || len(d.v3.entries) != 0 || len(d.descriptors) != 0 {
		t.Fatal("removal projection/ownership fence failed")
	}
	registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Replacement", "self", "")
	if d.keepOwned(ctx, "native", registration1459Program("native.exe"), "question") {
		t.Fatal("old question enabled replacement")
	}
	if out := d.registerApplicationV3(ctx, rights.Subject{Account: "account", Program: registration1459Program("native.exe")}, fileid.Self, registration1459Program("missing-native.exe"), ""); out.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("fake manifest seam became native signed positive")
	}
}

func TestApplicationV3RegistrationPublicationInterruptedBeforeIndex(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	r1 := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "First", "operator", "")
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- cas.ChangeLimitContext(ctx, filepath.Join(d.v3.dir, "index.json"), applicationV3IndexLimit, func(b []byte) ([]byte, error) { close(entered); <-release; return b, nil })
	}()
	<-entered
	short, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	v, f := registration1459File(t, "native", registration1459Program("native.exe"), "Second")
	out := d.publishApplicationV3(short, v, f, "operator", r1)
	cancel()
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if out.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatalf("interrupted index write: %+v", out)
	}
	reopened := registration1459Directory(t, state)
	if reopened.v3.entries["native"].Revision != r1 || reopened.descriptors["native"].Title != "First" {
		t.Fatal("prepublication bytes promoted or old record lost")
	}
}

func TestApplicationV3RegistrationOrphanAdmissionAndRecovery(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []string{"unexpected", "malformed", "hardlink", "overbound"} {
		t.Run(kind, func(t *testing.T) {
			d := registration1459Directory(t, t.TempDir())
			r := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "First", "operator", "")
			_, f := registration1459File(t, "orphan", registration1459Program("orphan.exe"), "Orphan")
			b, _ := json.Marshal(f)
			p := filepath.Join(d.v3.dir, strings.Repeat("a", 48)+".json")
			switch kind {
			case "unexpected":
				p = filepath.Join(d.v3.dir, "unowned.bin")
			case "malformed":
				b = []byte("unknown bytes")
			case "hardlink":
				outside := filepath.Join(t.TempDir(), "protected.json")
				if err := os.WriteFile(outside, b, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, p); err != nil {
					t.Fatal(err)
				}
			case "overbound":
				for i := 0; i <= registrationDirectoryEntryLimit; i++ {
					if err := os.WriteFile(filepath.Join(d.v3.dir, fmt.Sprintf("%048x.json.lock", i)), nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if kind != "hardlink" && kind != "overbound" {
				if err := os.WriteFile(p, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadDir(d.v3.dir)
			v, next := registration1459File(t, "native", registration1459Program("native.exe"), "Second")
			if got := d.publishApplicationV3(ctx, v, next, "operator", r); got.Outcome != wire.ApplicationOutcomeUnavailable {
				t.Fatal("unsafe inventory admitted", got)
			}
			after, _ := os.ReadDir(d.v3.dir)
			if len(before) != len(after) {
				t.Fatal("refusal changed inventory")
			}
		})
	}
}

func TestApplicationV3RegistrationSnapshotSurvivesOtherOwnerReclamation(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	a := registration1459Directory(t, state)
	r := registration1459Publish(t, a, "native", registration1459Program("native.exe"), "First", "operator", "")
	b := registration1459Directory(t, state)
	entered, release := make(chan struct{}), make(chan struct{})
	a.decide = func(context.Context, rights.Subject, string, string) (bool, error) {
		close(entered)
		<-release
		return true, nil
	}
	finished := make(chan []applicationV3RegistrationView, 1)
	go func() {
		views, _, _, _ := a.applicationV3Snapshot(ctx, rights.Subject{Account: "account", Program: "reader"}, "native")
		finished <- views
	}()
	<-entered // The storage callback has captured the raw manifest and released CAS.
	registration1459Publish(t, b, "native", registration1459Program("native.exe"), "Second", "operator", r)
	if err := b.withRegistrationStorageLocked(ctx, func(map[string]wire.ApplicationDescriptor) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.v3.filePath(r)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old file not retired", err)
	}
	close(release)
	views := <-finished
	if len(views) != 1 || views[0].Revision != r || !bytes.Contains(views[0].Raw, []byte("First")) {
		t.Fatal("promised snapshot lost captured bytes", views)
	}
}

func TestApplicationV3RegistrationBoundsUnknownAndPrecedence(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	v, f := registration1459File(t, "native", registration1459Program("native.exe"), "Native")
	if out := d.publishApplicationV3(ctx, v, f, "operator", "nonexistent"); out.Outcome != wire.ApplicationOutcomeUnknown {
		t.Fatal(out)
	}
	oversized := f
	oversized.Raw = make([]byte, 262145)
	if out := d.publishApplicationV3(ctx, v, oversized, "operator", ""); out.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal(out)
	}
	oversized = f
	oversized.Source.Code.Subject = strings.Repeat("x", applicationV3ManifestFileLimit)
	if out := d.publishApplicationV3(ctx, v, oversized, "operator", ""); out.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("oversize encoded manifest published")
	}
	if len(d.v3.entries) != 0 {
		t.Fatal("refusal mutated index")
	}
	r := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "operator", "")
	if out := d.publishApplicationV3(ctx, v, f, "self", r); out.Outcome != wire.ApplicationOutcomeForbidden {
		t.Fatal("self overwrote operator", out)
	}
	if out := d.publishApplicationV3(ctx, v, f, "operator", "stale"); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal(out)
	}
}

func TestApplicationV3RegistrationCrossOwnerRemovalRefreshesProjection(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	a := registration1459Directory(t, state)
	r := registration1459Publish(t, a, "native", registration1459Program("native.exe"), "Native", "operator", "")
	b := registration1459Directory(t, state)
	if out := b.changeApplicationV3(ctx, rights.Subject{Account: "account", Program: "operator"}, "native", r, "remove"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if out := a.addDescriptor(ctx, wire.ApplicationDescriptor{Name: "legacy", Program: registration1459Program("legacy.exe"), Title: "Legacy"}, nil); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if _, ok := a.descriptors["native"]; ok {
		t.Fatal("removed @3 projection survived fresh CAS mutation")
	}
}

func TestApplicationV3RegistrationStateCASAdvancesRevision(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	a := registration1459Directory(t, state)
	r := registration1459Publish(t, a, "native", registration1459Program("native.exe"), "Native", "operator", "")
	b := registration1459Directory(t, state)
	operator := rights.Subject{Account: "account", Program: "operator"}
	first := a.changeApplicationV3(ctx, operator, "native", r, "disabled")
	if first.Outcome != wire.ApplicationOutcomeApplied || first.Revision == "" || first.Revision == r {
		t.Fatalf("state change did not advance revision: %+v", first)
	}
	if second := b.changeApplicationV3(ctx, operator, "native", r, "enabled"); second.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("stale second state writer applied")
	}
	reopened := registration1459Directory(t, state)
	if reopened.v3.entries["native"].State != "disabled" || reopened.v3.entries["native"].Revision != first.Revision || reopened.v3.entries["native"].FileRevision != r {
		t.Fatal("state revision or immutable raw reference changed incorrectly")
	}
}

func TestApplicationV3RegistrationPendingProjectionCannotEstablishLoginChoice(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	r := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "self", "")
	state, err := d.loginState(ctx)
	if err != nil || state.enabled {
		t.Fatal("pending registration established person choice", err)
	}
	page, _, _ := d.snapshot(ctx, rights.Subject{Account: "account", Program: "operator"})
	if len(page.Applications) != 1 || !page.Applications[0].SelfRegistered || page.Applications[0].Descriptor.Activation != nil {
		t.Fatal("pending safe projection missing")
	}
	if !d.setQuestionForRevision(ctx, "native", registration1459Program("native.exe"), "question", r) || !d.keepOwned(ctx, "native", registration1459Program("native.exe"), "question") {
		t.Fatal("Keep failed")
	}
	state, err = d.loginState(ctx)
	if err != nil || !state.enabled {
		t.Fatal("actual Keep lost person choice", err)
	}
	current := d.v3.entries["native"].Revision
	if current == r {
		t.Fatal("Keep did not fence pending revision")
	}
	if out := d.changeApplicationV3(ctx, rights.Subject{Account: "account", Program: "operator"}, "native", current, "disabled"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	state, err = d.loginState(ctx)
	if err != nil || !state.enabled {
		t.Fatal("disabling mediation erased retained Keep choice", err)
	}
}

func TestApplicationV3RegistrationExistingQuestionOwnerAcrossUpdateAndRestart(t *testing.T) {
	run := newSelfRegisteredRun(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	v, f := registration1459File(t, "native", run.f.comfy.Program, "Same title")
	v.StartGuidance = "first declaration"
	registration1459SetRaw(t, &f, v)
	first := run.d.publishApplicationV3(run.ctx, v, f, "self", "")
	if first.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(first)
	}
	r1 := first.Revision
	old := selfRegisteredQuestion(t, run.f, run.d, "native")
	v.StartGuidance = "changed declaration"
	registration1459SetRaw(t, &f, v)
	second := run.d.publishApplicationV3(run.ctx, v, f, "self", r1)
	if second.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(second)
	}
	r2 := second.Revision
	current := selfRegisteredQuestion(t, run.f, run.d, "native")
	if current.ID == old.ID || r1 == r2 {
		t.Fatal("updated declaration reused prior question/revision")
	}
	run.cancel()
	waitUnfollowed(t, run.k, old.ID)
	waitUnfollowed(t, run.k, current.ID)
	run.start(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	run.k.resume()
	answer(t, run.f, old.ID, "keep")
	if run.d.v3.entries["native"].State != "pending" {
		t.Fatal("old answer enabled restarted new declaration")
	}
	answer(t, run.f, current.ID, "keep")
	waitDirectory(t, run.d, func() bool {
		run.d.mu.Lock()
		defer run.d.mu.Unlock()
		return run.d.v3.entries["native"].State == "enabled"
	})
	if run.d.v3.entries["native"].Revision == r2 {
		t.Fatal("Keep retained pending effect binding")
	}
}

func TestApplicationV3RegistrationEqualKeptInputPreservesRevision(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	notifications := 0
	d.registeredSelf = func(string) { notifications++ }
	d.askKeep = func(string) error { notifications++; return nil }
	v, f := registration1459File(t, "native", registration1459Program("native.exe"), "Native")
	first := d.publishApplicationV3(ctx, v, f, "self", "")
	if first.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(first)
	}
	if !d.setQuestionForRevision(ctx, "native", v.Program, "question", first.Revision) || !d.keepOwned(ctx, "native", v.Program, "question") {
		t.Fatal("Keep failed")
	}
	current := d.v3.entries["native"].Revision
	changed := d.changed
	replay := d.publishApplicationV3(ctx, v, f, "self", current)
	if replay.Outcome != wire.ApplicationOutcomeApplied || replay.Revision != current || d.v3.entries["native"].State != "enabled" || d.changed != changed || notifications != 1 { // notified once, asked nothing
		t.Fatal("equal input changed approval/generation", replay, notifications)
	}
	if stale := d.publishApplicationV3(ctx, v, f, "self", first.Revision); stale.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal("equal body bypassed stale revision", stale)
	}
}

func TestApplicationV3RegistrationCommandAndObserveReadOwners(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "operator", "")
	d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { return false, nil }
	subject := rights.Subject{Account: "account", Program: "reader"}
	view, found, err := d.applicationV3Declaration(ctx, "native")
	if err != nil || !found {
		t.Fatal("internal declaration unavailable before rights", err)
	}
	view.Raw[0] = 'X'
	again, _, err := d.applicationV3Declaration(ctx, "native")
	if err != nil || again.Raw[0] == 'X' {
		t.Fatal("declaration exposed mutable store bytes")
	}
	d.decide = func(_ context.Context, _ rights.Subject, action, _ string) (bool, error) {
		return action == "abstraction.facade/command.read", nil
	}
	observed, _, _, out := d.applicationV3Snapshot(ctx, subject, "")
	if out != wire.ApplicationOutcomePage || len(observed) != 0 {
		t.Fatal("Observe bypassed application.read")
	}
	commands, _, _, out := d.applicationV3CommandSnapshot(ctx, subject, "")
	if out != wire.ApplicationOutcomePage || len(commands) != 1 {
		t.Fatal("command.read-only caller lost declarations")
	}
	if _, _, _, out := d.applicationV3SnapshotForAction(ctx, subject, "", "abstraction.facade/command.write"); out != wire.ApplicationOutcomeForbidden {
		t.Fatal("arbitrary action admitted")
	}
}

func TestApplicationV3RegistrationLegacyStaleRemoveAndQuestionABA(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	v := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Same title"}
	if out := run.d.register(run.ctx, f.comfy, v); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	old := selfRegisteredQuestion(t, f, run.d, "editor")
	_, oldMark, _ := run.d.selfEntry("editor")
	b, err := openApplications(run.state, f.answerer.Account, func(ctx context.Context, s rights.Subject, a, r string) (bool, error) {
		return f.a.rights.decide(ctx, s, a, r).Outcome == rights.DecisionOutcomePermitted, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keeper := f.a.keepSelfRegistered(ctx, b)
	if out := b.remove(ctx, f.panel, "editor"); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if out := b.register(ctx, f.comfy, v); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	current := selfRegisteredQuestion(t, f, b, "editor")
	if old.ID == current.ID {
		t.Fatal("same-title replacement coalesced old occurrence")
	}
	if run.d.setQuestionForRevision(ctx, "editor", v.Program, "late-old-question", oldMark.registrationRevision()) {
		t.Fatal("stale admission attached to replacement")
	}
	answer(t, f, old.ID, "remove")
	waitUnfollowed(t, run.k, old.ID)
	waitUnfollowed(t, keeper, old.ID)
	if got := f.a.rights.decide(ctx, f.comfy, ActionApplicationRegisterSelf, "account").Outcome; got != rights.DecisionOutcomePermitted {
		t.Fatal("stale Remove re-blocked replacement", got)
	}
	fresh, err := openApplications(run.state, f.answerer.Account, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := fresh.descriptors["editor"]; !exists {
		t.Fatal("stale Remove deleted replacement")
	}
	answer(t, f, current.ID, "remove")
	waitDirectory(t, b, func() bool { registered, _ := unconfirmedNow(b, "editor"); return !registered })
	if got := f.a.rights.decide(ctx, f.comfy, ActionApplicationRegisterSelf, "account").Outcome; got != rights.DecisionOutcomeDenied {
		t.Fatal("valid Remove lost program-wide Block", got)
	}
}

func TestApplicationV3RegistrationRemoveIntentUnknownPolicyAndRestart(t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, wrote := range []bool{false, true} {
			t.Run(fmt.Sprintf("native_%v_wrote_%v", native, wrote), func(t *testing.T) {
				run := newSelfRegisteredRun(t)
				f := run.f
				if native {
					if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
						t.Fatal(err)
					}
					registration1459Publish(t, run.d, "editor", f.comfy.Program, "Editor", "self", "")
				} else {
					if out := run.d.register(run.ctx, f.comfy, wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor"}); out.Outcome != wire.ApplicationOutcomeApplied {
						t.Fatal(out)
					}
				}
				q := selfRegisteredQuestion(t, f, run.d, "editor")
				run.cancel()
				waitUnfollowed(t, run.k, q.ID)
				answer(t, f, q.ID, "remove")
				ctx := context.Background()
				calls := 0
				uncertain := errors.New("injected uncertain Rights outcome")
				err := run.d.removeSelfRegistered(ctx, "editor", f.comfy.Program, q.ID, func() error {
					calls++
					if wrote {
						if err := f.a.rights.blockRegisterSelf(f.comfy.Program); err != nil {
							return err
						}
					}
					return uncertain
				})
				if !errors.Is(err, uncertain) || calls != 1 {
					t.Fatal("unknown policy outcome finalized/retried", err, calls)
				}
				_, mark, exists := run.d.selfEntry("editor")
				if !exists || !mark.Removing {
					t.Fatal("intent was not retained")
				}
				if run.d.keepOwned(ctx, "editor", f.comfy.Program, q.ID) {
					t.Fatal("Keep bypassed Remove reservation")
				}
				if native {
					v, file := registration1459File(t, "editor", f.comfy.Program, "Changed")
					if out := run.d.publishApplicationV3(ctx, v, file, "self", run.d.v3.entries["editor"].Revision); out.Outcome != wire.ApplicationOutcomeConflict {
						t.Fatal("update bypassed intent", out)
					}
				} else {
					if out := run.d.remove(ctx, f.panel, "editor"); out.Outcome == wire.ApplicationOutcomeApplied {
						t.Fatal("legacy writer bypassed intent")
					}
				}
				// Missing authenticated evidence cannot reconcile or invent a Block.
				book := filepath.Join(filepath.Dir(f.comfy.Program), "application.json")
				saved := book + ".retained"
				if err := os.Rename(book, saved); err != nil {
					t.Fatal(err)
				}
				run.start(t)
				if native {
					if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
						t.Fatal(err)
					}
					run.k.resume()
				}
				_, mark, exists = run.d.selfEntry("editor")
				if !exists || !mark.Removing {
					t.Fatal("missing answer finalized intent")
				}
				if !wrote && f.a.rights.decide(ctx, f.comfy, ActionApplicationRegisterSelf, "account").Outcome != rights.DecisionOutcomePermitted {
					t.Fatal("missing answer invented policy Block")
				}
				if err := os.Rename(saved, book); err != nil {
					t.Fatal(err)
				}
				run.k.resume()
				if _, _, exists := run.d.selfEntry("editor"); exists {
					t.Fatal("actual retained Remove did not finish")
				}
				if f.a.rights.decide(ctx, f.comfy, ActionApplicationRegisterSelf, "account").Outcome != rights.DecisionOutcomeDenied {
					t.Fatal("restart did not reconcile existing Rights owner")
				}
			})
		}
	}
}

func TestApplicationV3RegistrationUnboundLegacyOccurrenceCannotPromote(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	v := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor"}
	if out := run.d.register(run.ctx, f.comfy, v); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	old := selfRegisteredQuestion(t, f, run.d, "editor")
	run.cancel()
	waitUnfollowed(t, run.k, old.ID)
	run.d.mu.Lock()
	marks := map[string]selfMark{}
	for name, mark := range run.d.unconfirmed {
		mark.RegistrationRevision = ""
		marks[name] = mark
	}
	run.d.mu.Unlock()
	raw, err := json.Marshal(marks)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run.d.dir, selfRegisteredFile), raw, 0600); err != nil {
		t.Fatal(err)
	}
	book := filepath.Join(filepath.Dir(f.comfy.Program), "application.json")
	before, err := os.ReadFile(book)
	if err != nil {
		t.Fatal(err)
	}
	run.start(t)
	after, err := os.ReadFile(book)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("migration rewrote retained occurrence IDs/scopes", err)
	}
	_, mark, ok := run.d.selfEntry("editor")
	if !ok || mark.RegistrationRevision == "" || mark.Question != "" || mark.UnboundQuestion != old.ID {
		t.Fatal("legacy generation uncertainty was promoted")
	}
	answer(t, f, old.ID, "keep")
	record, found, err := f.a.book.ApplicationRecord(old.ID)
	if err != nil || !found {
		t.Fatal(err)
	}
	run.k.apply(record, "editor", f.comfy.Program)
	if _, pending := unconfirmedNow(run.d, "editor"); !pending {
		t.Fatal("unbound old answer confirmed current registration")
	}
	if err := run.k.ask("editor"); err != nil {
		t.Fatal(err)
	}
	current := selfRegisteredQuestion(t, f, run.d, "editor")
	if current.ID == old.ID {
		t.Fatal("new generation reused unbound occurrence")
	}
	answer(t, f, current.ID, "keep")
	waitDirectory(t, run.d, func() bool { _, pending := unconfirmedNow(run.d, "editor"); return !pending })
}

func TestApplicationV3RegistrationFinalizedLegacyOrphanDoesNotReblock(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	v := wire.ApplicationDescriptor{Name: "editor", Program: f.comfy.Program, Title: "Editor"}
	if out := run.d.register(run.ctx, f.comfy, v); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	q := selfRegisteredQuestion(t, f, run.d, "editor")
	run.cancel()
	waitUnfollowed(t, run.k, q.ID)
	answer(t, f, q.ID, "remove")
	var residue []byte
	if err := run.d.removeSelfRegistered(context.Background(), "editor", f.comfy.Program, q.ID, func() error {
		var err error
		residue, err = os.ReadFile(filepath.Join(run.d.dir, selfRegisteredFile))
		if err != nil {
			return err
		}
		return f.a.rights.blockRegisterSelf(f.comfy.Program)
	}); err != nil {
		t.Fatal(err)
	}
	// Reconstruct the allowed crash boundary after descriptor fsync and before
	// cleanup using the exact bytes captured from the real durable intent.
	if err := os.WriteFile(filepath.Join(run.d.dir, selfRegisteredFile), residue, 0600); err != nil {
		t.Fatal(err)
	}
	policy := f.a.rights.policy
	read := policy.ReadRule(f.comfy, ActionApplicationRegisterSelf, "account")
	if edit, err := policy.ChangeRule(read.Revision, f.comfy, ActionApplicationRegisterSelf, "account", rightspolicy.RuleEdit{By: f.panel}, func() error { return nil }); err != nil || edit.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatal("lifting Block", edit, err)
	}
	run.start(t)
	if f.a.rights.decide(run.ctx, f.comfy, ActionApplicationRegisterSelf, "account").Outcome != rights.DecisionOutcomePermitted {
		t.Fatal("completed orphan reapplied a lifted Block")
	}
	run.d.mu.Lock()
	remaining := len(run.d.unconfirmed)
	run.d.mu.Unlock()
	if remaining != 0 {
		t.Fatal("completed orphan not cleaned")
	}
}

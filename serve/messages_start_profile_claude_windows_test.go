package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

type messageStartIdentityFixture struct {
	key      string
	closes   *int
	changed  bool
	closeErr error
}

func (f *messageStartIdentityFixture) FileKey() string { return f.key }
func (f *messageStartIdentityFixture) Check(context.Context) error {
	if f.changed {
		return errors.New("image/anchor changed")
	}
	return nil
}
func (f *messageStartIdentityFixture) Close() error { *f.closes++; return f.closeErr }

func claudeMessageProfileFixture(t *testing.T) (wire.ApplicationDescriptor, startprofile.Profile, string) {
	t.Helper()
	cli := filepath.Join(t.TempDir(), "inert-cli.exe")
	if err := os.WriteFile(cli, []byte("never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, cli, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := id.Stored()
	if err != nil {
		t.Fatal(err)
	}
	p := startprofile.Profile{Name: "claude-v1", Context: "durable-mailbox", Folder: t.TempDir(), StateDir: t.TempDir(), CLIFileID: stored.String(), Model: "explicit-model", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 1 << 30, BudgetCents: 1}
	d := wire.ApplicationDescriptor{Name: "oa-claude-integration", Title: "Fixture receiver", Program: filepath.Join(t.TempDir(), "native-kept-helper.exe"), Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 1000}}
	if err := client.ValidApplicationDescriptor(d); err != nil {
		t.Fatal(err)
	}
	return d, p, id.Key()
}

func TestClaudeMessageStartProfileUsesSharedDescriptorBoundsBeforeHold(t *testing.T) {
	d, p, key := claudeMessageProfileFixture(t)
	holds, closes := 0, 0
	hold := func(context.Context, []string) (messageStartCLIIdentity, error) {
		holds++
		return &messageStartIdentityFixture{key: key, closes: &closes}, nil
	}
	for _, budget := range []int64{100, 30000} {
		current := d
		activation := *d.Activation
		current.Activation = &activation
		current.Activation.ReadinessTimeoutMs = budget
		if err := validateClaudeMessageStartProfile(t.Context(), current, p, hold); err != nil {
			t.Fatalf("valid readiness %d: %v", budget, err)
		}
	}
	before := holds
	for _, mutate := range []func(*wire.ApplicationDescriptor){
		func(d *wire.ApplicationDescriptor) { d.Activation.ReadinessTimeoutMs = 99 },
		func(d *wire.ApplicationDescriptor) { d.Activation.ReadinessTimeoutMs = 30001 },
		func(d *wire.ApplicationDescriptor) { d.Activation.Readiness.Name = "invalid name" },
		func(d *wire.ApplicationDescriptor) { d.Activation.Readiness.Protocol = "" },
		func(d *wire.ApplicationDescriptor) { d.Program = "relative-helper.exe" },
	} {
		current := d
		activation := *d.Activation
		current.Activation = &activation
		mutate(&current)
		if err := validateClaudeMessageStartProfile(t.Context(), current, p, hold); err == nil {
			t.Fatal("malformed descriptor accepted")
		}
	}
	if holds != before || holds != closes {
		t.Fatalf("invalid descriptor reached native hold: holds=%d closes=%d", holds, closes)
	}
}

func TestClaudeMessageStartProfileEffectFreeIdentityAndBounds(t *testing.T) {
	d, p, key := claudeMessageProfileFixture(t)
	holds, closes := 0, 0
	hold := func(context.Context, []string) (messageStartCLIIdentity, error) {
		holds++
		return &messageStartIdentityFixture{key: key, closes: &closes}, nil
	}
	approve := func(ctx context.Context, d wire.ApplicationDescriptor, p startprofile.Profile) error {
		return validateClaudeMessageStartProfile(ctx, d, p, hold)
	}
	resolve := messageStartProfileResolver(approve)
	r, err := resolve(t.Context(), d)
	if err != nil || r.Validate(t.Context()) != nil || holds != 2 || closes != 2 {
		t.Fatalf("effect-free resolver: %v holds=%d closes=%d", err, holds, closes)
	}
	for _, mutate := range []func(*wire.ApplicationDescriptor, *startprofile.Profile){
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) {
			p.Name = "codex-v1"
			d.Activation.Arguments = p.Arguments()
		},
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) {
			p.TimeoutMS = 0
			d.Activation.Arguments = p.Arguments()
		},
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) {
			p.MemoryBytes = 25 << 30
			d.Activation.Arguments = p.Arguments()
		},
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) {
			p.OutputBytes = 2 << 20
			d.Activation.Arguments = p.Arguments()
		},
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) {
			p.BudgetCents = 0
			d.Activation.Arguments = p.Arguments()
		},
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) {
			p.Model = ""
			d.Activation.Arguments = p.Arguments()
		},
		func(d *wire.ApplicationDescriptor, p *startprofile.Profile) { d.Name = "foreign-helper" },
	} {
		current, profile := d, p
		activation := *d.Activation
		current.Activation = &activation
		mutate(&current, &profile)
		before := holds
		if err := approve(t.Context(), current, profile); err == nil || holds != before {
			t.Fatal("invalid profile reached native CLI hold")
		}
	}
	stored, _ := fileid.ParseStoredFileID(p.CLIFileID)
	if err := os.Rename(stored.Path(), stored.Path()+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stored.Path(), []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if r.Validate(t.Context()) == nil {
		t.Fatal("CLI replacement accepted")
	}
	if holds != closes {
		t.Fatal("CLI holds leaked")
	}
}

func TestClaudeMessageStartProfileNativeHoldRefusalsClose(t *testing.T) {
	d, p, key := claudeMessageProfileFixture(t)
	for _, fixture := range []messageStartIdentityFixture{{key: "wrong"}, {key: key, changed: true}, {key: key, closeErr: errors.New("close")}} {
		closes := 0
		fixture.closes = &closes
		if err := validateClaudeMessageStartProfile(t.Context(), d, p, func(context.Context, []string) (messageStartCLIIdentity, error) { return &fixture, nil }); err == nil || closes != 1 {
			t.Fatalf("hold refusal: %v closes=%d", err, closes)
		}
	}
}

func TestClaudeMessageStartProfileRechecksNativeFolders(t *testing.T) {
	for _, which := range []string{"working", "state"} {
		t.Run(which, func(t *testing.T) {
			d, p, key := claudeMessageProfileFixture(t)
			closes := 0
			resolve := messageStartProfileResolver(func(ctx context.Context, d wire.ApplicationDescriptor, p startprofile.Profile) error {
				return validateClaudeMessageStartProfile(ctx, d, p, func(context.Context, []string) (messageStartCLIIdentity, error) {
					return &messageStartIdentityFixture{key: key, closes: &closes}, nil
				})
			})
			r, err := resolve(t.Context(), d)
			if err != nil {
				t.Fatal(err)
			}
			path := p.Folder
			if which == "state" {
				path = p.StateDir
			}
			if err := os.Rename(path, path+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if r.Validate(t.Context()) == nil {
				t.Fatal("folder replacement accepted")
			}
		})
	}
}

func TestClaudeMessageStartConfiguredNativeOwnerRechecksDescriptor(t *testing.T) {
	run := agent1478Fixture(t)
	d, p, key := claudeMessageProfileFixture(t)
	// register stores the subject spelling of the program; the expected
	// descriptor of an activation edit is the one the directory holds.
	d.Program = fileid.NormalizeSubjectProgram(run.f.answerer.Program)
	initial := d
	initial.Activation = nil
	if got := run.d.register(run.ctx, run.f.panel, initial); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	agent1478Set(t, run, d.Name, "", true)
	question := agent1478Question(t, run, d.Name)
	answer(t, run.f, question.ID, "keep")
	agent1478Wait(t, run, d.Name, "enrolled")
	if got := run.d.setActivation(run.ctx, run.f.panel, applications.ActivationConfiguration{Expected: initial, Activation: d.Activation}, nil); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	holds, closes, invocations := 0, 0, 0
	run.d.launch = func(string, []string) error { invocations++; return nil }
	run.d.messageStartProfile = messageStartProfileResolver(func(ctx context.Context, d wire.ApplicationDescriptor, p startprofile.Profile) error {
		return validateClaudeMessageStartProfile(ctx, d, p, func(context.Context, []string) (messageStartCLIIdentity, error) {
			holds++
			return &messageStartIdentityFixture{key: key, closes: &closes}, nil
		})
	})
	request, err := run.d.prepareMessageStart(run.ctx, d.Name)
	if err != nil || request.Validate(run.ctx) != nil {
		t.Fatalf("configured resolver: %v", err)
	}
	binding := func() error {
		if !run.d.lock(run.ctx) {
			return run.ctx.Err()
		}
		defer run.d.mu.Unlock()
		return run.d.withRegistrationReadLocked(run.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
			return run.d.messageStartBindingLocked(run.ctx, request, legacy)
		})
	}
	if err := binding(); err != nil {
		t.Fatal(err)
	}
	p.TimeoutMS++
	changed := *d.Activation
	changed.Arguments = p.Arguments()
	if got := run.d.setActivation(run.ctx, run.f.panel, applications.ActivationConfiguration{Expected: d, Activation: &changed}, nil); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	if binding() == nil {
		t.Fatal("captured descriptor accepted changed recipe")
	}
	if holds != closes || invocations != 0 {
		t.Fatalf("discovery effects: holds=%d closes=%d invocations=%d", holds, closes, invocations)
	}
}

// Claude Code keeps an older version for a while after updating itself. The
// recipe stays approved while the saved image is still present when the hold
// followed it to the newest version in the same claude-code folder, or held
// the saved image itself. Every other image is refused.
func TestClaudeMessageStartProfileFollowsUpdateWhileSavedImageKept(t *testing.T) {
	d, p, _ := claudeMessageProfileFixture(t)
	root := filepath.Join(t.TempDir(), "Claude-Data", "Claude_pzs8sxrjxfjjc", "claude-code")
	saved := filepath.Join(root, "2.1.3", "b1", "claude.exe")
	newest := filepath.Join(root, "2.1.9", "b1", "claude.exe")
	for _, path := range []string{saved, newest} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("never executed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, saved, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := id.Stored()
	if err != nil {
		t.Fatal(err)
	}
	p.CLIFileID = stored.String()
	activation := *d.Activation
	activation.Arguments = p.Arguments()
	d.Activation = &activation
	closes := 0
	held := func(identity messageStartCLIIdentity) func(context.Context, []string) (messageStartCLIIdentity, error) {
		return func(context.Context, []string) (messageStartCLIIdentity, error) { return identity, nil }
	}
	located := func(key, program string, changed bool) messageStartCLIIdentity {
		return &messageStartLocatedFixture{messageStartIdentityFixture{key: key, closes: &closes, changed: changed}, program}
	}
	other := filepath.Join(t.TempDir(), "Claude-Data", "Claude_pzs8sxrjxfjjc", "claude-code", "2.1.9", "b1", "claude.exe")
	codex := filepath.Join(t.TempDir(), "OpenAI.Codex_26.1103.7773.0_x64__2p2nqsd0c76g0", "app", "resources", "codex.exe")
	for name, identity := range map[string]messageStartCLIIdentity{
		"followed newest": located("new-version-key", newest, false),
		"saved image":     located(id.Key(), saved, false),
	} {
		if err := validateClaudeMessageStartProfile(t.Context(), d, p, held(identity)); err != nil {
			t.Fatal("refused:", name, err)
		}
	}
	for name, identity := range map[string]messageStartCLIIdentity{
		"other claude-code root": located("k", other, false),
		"codex install":          located("k", codex, false),
		"same path new key":      located("new-key", saved, false),
		"no location":            &messageStartIdentityFixture{key: "k", closes: &closes},
		"unverified":             located("new-version-key", newest, true),
	} {
		if err := validateClaudeMessageStartProfile(t.Context(), d, p, held(identity)); err == nil {
			t.Fatal("accepted:", name)
		}
	}
	if closes != 7 {
		t.Fatal("every hold closes", closes)
	}
}

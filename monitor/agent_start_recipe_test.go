package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// An enrolled connection without a start recipe gets the default one, saved
// conditionally on the exact descriptor read. A saved recipe is kept, and an
// unenrolled connection gets none.
func TestAgentStartRecipeRegistersDefaultOnce(t *testing.T) {
	for _, kind := range []string{"claude-v1", "codex-v1"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			command := filepath.Join(t.TempDir(), "agent.exe")
			if err := os.WriteFile(command, []byte("never executed"), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := fileid.Resolve(ctx, fileid.Self, command, fileid.Options{Links: fileid.RefuseLinks})
			if err != nil {
				t.Fatal(err)
			}
			stored, err := id.Stored()
			if err != nil {
				t.Fatal(err)
			}
			state := t.TempDir()
			descriptor := wire.ApplicationDescriptor{Name: "oa-agent-integration", Program: `C:\oa\oa-agent-integration.exe`, Title: "Agent"}
			enrolled := applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: descriptor.Name, Program: descriptor.Program, State: applications.AgentEnrollmentStateEnrolled}}
			enrollment := enrolled
			var saved []applications.ActivationConfiguration
			owner := agentStartRecipeOwner{profile: kind,
				read: func(context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
					return descriptor, enrollment, nil
				},
				set: func(_ context.Context, request applications.ActivationConfiguration) (wire.ApplicationChange, error) {
					saved = append(saved, request)
					return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, nil
				},
				cli: func(context.Context) (claudePanelImage, error) {
					return &claudePanelFixtureImage{program: command, key: id.Key()}, nil
				},
				state: func() (string, error) { return state, nil }}
			if err := owner.ensure(ctx); err != nil || len(saved) != 1 {
				t.Fatal("default recipe", err, len(saved))
			}
			request := saved[0]
			if request.Expected.Name != descriptor.Name || request.Activation == nil || request.Activation.ReadinessTimeoutMs != agentStartRecipeReadinessMS || request.Activation.Readiness.Contract != "abstraction.facade/messages@1" {
				t.Fatal("recipe shape", request)
			}
			p, err := startprofile.Parse(request.Activation.Arguments)
			if err != nil || p.Name != kind || !samePanelPath(p.Folder, state) || !samePanelPath(p.StateDir, state) || p.CLIFileID != stored.String() || len(p.Context) < 10 || p.Context[:9] != "receiver-" {
				t.Fatal("recipe profile", p, err)
			}
			descriptor.Activation = request.Activation
			if err := owner.ensure(ctx); err != nil || len(saved) != 1 {
				t.Fatal("saved recipe replaced", err, len(saved))
			}
			// A command the agent replaced by updating itself is repaired in
			// place: the same context and limits on the current command.
			gone := filepath.Join(t.TempDir(), "agent-old.exe")
			if err := os.WriteFile(gone, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			oldID, err := fileid.Resolve(ctx, fileid.Self, gone, fileid.Options{Links: fileid.RefuseLinks})
			if err != nil {
				t.Fatal(err)
			}
			oldStored, err := oldID.Stored()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(gone); err != nil {
				t.Fatal(err)
			}
			stale := p
			stale.CLIFileID, stale.TimeoutMS = oldStored.String(), 5000
			descriptor.Activation = &wire.ApplicationActivationRecipe{Arguments: stale.Arguments(), Readiness: request.Activation.Readiness, ReadinessTimeoutMs: 1000}
			if err := owner.ensure(ctx); err != nil || len(saved) != 2 {
				t.Fatal("stale command kept", err, len(saved))
			}
			repaired, err := startprofile.Parse(saved[1].Activation.Arguments)
			want := stale
			want.CLIFileID = stored.String()
			if err != nil || repaired != want || saved[1].Activation.ReadinessTimeoutMs != 1000 {
				t.Fatal("repair changed more than the command", repaired, err)
			}
			other := stale
			other.Name = map[string]string{"claude-v1": "codex-v1", "codex-v1": "claude-v1"}[kind]
			descriptor.Activation = &wire.ApplicationActivationRecipe{Arguments: other.Arguments(), Readiness: request.Activation.Readiness, ReadinessTimeoutMs: 1000}
			if err := owner.ensure(ctx); err != nil || len(saved) != 2 {
				t.Fatal("another kind of recipe replaced", err, len(saved))
			}
			saved = saved[:1]
			descriptor.Activation = nil
			enrollment = applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: descriptor.Name, Program: descriptor.Program, State: applications.AgentEnrollmentStatePending}}
			if err := owner.ensure(ctx); err != nil || len(saved) != 1 {
				t.Fatal("unenrolled connection got a recipe", err, len(saved))
			}
			enrollment = enrolled
			owner.cli = func(context.Context) (claudePanelImage, error) {
				return &claudePanelFixtureImage{program: command, key: "replaced"}, nil
			}
			if err := owner.ensure(ctx); err == nil || len(saved) != 1 {
				t.Fatal("changed command registered", err, len(saved))
			}
		})
	}
}

// codexStartRecipeFixture is a saved, enrolled Codex connection whose
// registration holds activation; saves count in sets.
func codexStartRecipeFixture(t *testing.T, activation *wire.ApplicationActivationRecipe) (*codexPanelConnections, *[]applications.ActivationConfiguration) {
	t.Helper()
	command := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(command, []byte("never executed"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, command, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	c := codexPanelFixture(t)
	helper := filepath.Join(t.TempDir(), connectorImageLeaf(t, "oa-codex-integration"))
	c.cli = func(context.Context) (claudePanelCLI, error) {
		return &claudePanelFixtureCLI{claudePanelFixtureImage{program: command, key: id.Key()}}, nil
	}
	c.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Registration: "saved", Owned: true, Name: "OpenAbstractions", Program: helper})
	}
	descriptor := wire.ApplicationDescriptor{Name: "oa-codex-integration", Program: helper, Title: "Codex", Activation: activation}
	sets := new([]applications.ActivationConfiguration)
	c.startupRead = func(context.Context, string, string) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
		return descriptor, applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: descriptor.Name, Program: descriptor.Program, State: applications.AgentEnrollmentStateEnrolled}}, nil
	}
	c.startupSet = func(_ context.Context, request applications.ActivationConfiguration) (wire.ApplicationChange, error) {
		*sets = append(*sets, request)
		descriptor.Activation = request.Activation
		return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, nil
	}
	return c, sets
}

// Reading a connection view writes nothing: the recipe is registered when
// the person saves the connection and at sign-in, never from a GET.
func TestConnectionViewsRegisterNoStartRecipe(t *testing.T) {
	codex, sets := codexStartRecipeFixture(t, nil)
	claudeSets := 0
	claude, _ := backgroundFixture(t)
	project := t.TempDir()
	saved := []claudeSavedConnection{{Kind: "user", Registration: "saved", Project: project, ProjectKey: "project-key", Program: filepath.Join(project, connectorImageLeaf(t, "oa-claude-integration")), ProgramKey: "fixture-key", Name: "OpenAbstractions"}}
	claude.command = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		return json.Marshal(map[string]any{"outcome": "page", "connections": saved})
	}
	claude.startupRead = func(context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
		t.Error("a Claude view read the start recipe")
		return wire.ApplicationDescriptor{}, applications.AgentEnrollmentResult{}, nil
	}
	claude.startupSet = func(context.Context, applications.ActivationConfiguration) (wire.ApplicationChange, error) {
		claudeSets++
		return wire.ApplicationChange{}, nil
	}
	p := &servicePanel{codexConnections: codex, claudeConnections: claude}
	mux := http.NewServeMux()
	p.codexConnectionRoutes(mux, "k")
	p.claudeConnectionRoutes(mux, "k")
	for _, path := range []string{"/connections/codex/state", "/connections/claude/state"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", path+"?k=k", nil))
		if w.Code != http.StatusOK {
			t.Fatal(path, w.Code)
		}
		if !strings.Contains(w.Body.String(), `"registration":"saved"`) || path == "/connections/claude/state" && !strings.Contains(w.Body.String(), `"cli_available":true`) {
			t.Fatal(path, "did not reach the saved row", w.Body.String())
		}
	}
	time.Sleep(100 * time.Millisecond)
	if len(*sets) != 0 || claudeSets != 0 {
		t.Fatal("a GET registered a start recipe", len(*sets), claudeSets)
	}
}

// At sign-in a saved connection whose recipe names a command the agent
// replaced by updating itself is re-pinned once; a current one is kept.
func TestSavedConnectionStartRecipeRepinsAtSignIn(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "codex-old.exe")
	if err := os.WriteFile(gone, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldID, err := fileid.Resolve(t.Context(), fileid.Self, gone, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	oldStored, err := oldID.Stored()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	stale := startprofile.Profile{Name: "codex-v1", Context: "receiver-kept", Folder: state, StateDir: state, CLIFileID: oldStored.String(), Model: "default", TimeoutMS: 120000, OutputBytes: 65536, MemoryBytes: 2 << 30, BudgetCents: 100}
	codex, sets := codexStartRecipeFixture(t, &wire.ApplicationActivationRecipe{Arguments: stale.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 30000})
	p := &servicePanel{codexConnections: codex, claudeConnections: &claudePanelConnections{}}
	_ = ensureSavedAgentStartRecipes(t.Context(), p)
	if len(*sets) != 1 {
		t.Fatal("stale pin not re-registered once", len(*sets))
	}
	repinned, err := startprofile.Parse((*sets)[0].Activation.Arguments)
	if err != nil || repinned.Context != stale.Context || repinned.CLIFileID == stale.CLIFileID {
		t.Fatal("re-pin", repinned, err)
	}
	_ = ensureSavedAgentStartRecipes(t.Context(), p)
	if len(*sets) != 1 {
		t.Fatal("a current pin was registered again", len(*sets))
	}
}

// Claude Code keeps the previous version folder after updating itself. A
// recipe pinned to that kept version is stale and is re-pinned to the newest.
func TestClaudeStartRecipeRepinsFromKeptOlderVersion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write := func(version, build string) fileid.FileID {
		directory := filepath.Join(root, "Claude-Data", "claude_pzs8sxrjxfjjc", "claude-code", version, build)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
		program := filepath.Join(directory, "claude.exe")
		if err := os.WriteFile(program, []byte("inert "+version), 0o600); err != nil {
			t.Fatal(err)
		}
		id, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	older := write("2.1.317", "aaaa")
	newest := write("2.1.321", "bbbb")
	olderStored, _ := older.Stored()
	newestStored, _ := newest.Stored()
	if savedCommandCurrent(ctx, "claude-v1", olderStored.String()) || !savedCommandCurrent(ctx, "claude-v1", newestStored.String()) {
		t.Fatal("kept older version counted current")
	}
	state := t.TempDir()
	pinned := startprofile.Profile{Name: "claude-v1", Context: "receiver-0123456789", Folder: state, StateDir: state, CLIFileID: olderStored.String(), Model: agentStartRecipeModel, TimeoutMS: agentStartRecipeTimeoutMS, OutputBytes: agentStartRecipeOutputBytes, MemoryBytes: agentStartRecipeMemoryBytes, BudgetCents: agentStartRecipeBudgetCents}
	if _, err := startprofile.Parse(pinned.Arguments()); err != nil {
		t.Fatal(err)
	}
	descriptor := wire.ApplicationDescriptor{Name: "oa-agent-integration", Program: `C:\oa\oa-agent-integration.exe`, Title: "Agent",
		Activation: &wire.ApplicationActivationRecipe{Arguments: pinned.Arguments(), Readiness: wire.ApplicationInterface{Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 1000}}
	enrollment := applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: descriptor.Name, Program: descriptor.Program, State: applications.AgentEnrollmentStateEnrolled}}
	var saved []applications.ActivationConfiguration
	owner := agentStartRecipeOwner{profile: "claude-v1",
		read: func(context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
			return descriptor, enrollment, nil
		},
		set: func(_ context.Context, request applications.ActivationConfiguration) (wire.ApplicationChange, error) {
			saved = append(saved, request)
			return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, nil
		},
		cli: func(context.Context) (claudePanelImage, error) {
			return &claudePanelFixtureImage{program: newest.Path(), key: newest.Key()}, nil
		},
		state: func() (string, error) { return state, nil }}
	if err := owner.ensure(ctx); err != nil || len(saved) != 1 {
		t.Fatal("kept older version not re-pinned", err, len(saved))
	}
	repinned, err := startprofile.Parse(saved[0].Activation.Arguments)
	if err != nil || repinned.CLIFileID != newestStored.String() {
		t.Fatal("re-pin did not name the newest version", repinned, err)
	}
}

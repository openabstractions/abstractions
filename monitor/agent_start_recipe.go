package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// The default start recipe an enrolled Claude Code or Codex connection gets,
// so that a question to one of its closed chats can wake that chat (owner,
// 2026-10-31 "attach to all sessions"; 2026-11-09 dormant chats must be
// reachable). The owner declined a model/folder/cost startup form, so the
// Panel registers it without asking. It grants no start: the runtime starts
// only an existing retained chat, after the person's per-direction start
// permit, within the Agent starts settings and their hourly limit. The model
// names no model: a woken chat answers on its own.
const (
	agentStartRecipeModel       = "default"
	agentStartRecipeTimeoutMS   = 120000
	agentStartRecipeOutputBytes = 64 << 10
	agentStartRecipeMemoryBytes = 2 << 30
	agentStartRecipeBudgetCents = 100
	agentStartRecipeReadinessMS = 30000
)

type agentStartRecipeOwner struct {
	profile string
	read    func(context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error)
	set     func(context.Context, applications.ActivationConfiguration) (wire.ApplicationChange, error)
	// cli holds the agent's own command; state is the connection's state folder.
	cli   func(context.Context) (claudePanelImage, error)
	state func() (string, error)
}

// ensure registers the default recipe on an enrolled connection that has
// none. A saved recipe is kept as it is, including one the person changed;
// only a saved command that no longer exists, as after the agent updated
// itself to a new file, is replaced by the agent's current command.
func (o agentStartRecipeOwner) ensure(ctx context.Context) (resultErr error) {
	if o.read == nil || o.set == nil || o.cli == nil || o.state == nil {
		return errors.New("start recipe owner unavailable")
	}
	descriptor, enrollment, err := o.read(ctx)
	if err != nil {
		return err
	}
	if !validClaudeStartupEnrollment(descriptor, enrollment) {
		return nil
	}
	var prior *startprofile.Profile
	if descriptor.Activation != nil {
		p, err := startprofile.Parse(descriptor.Activation.Arguments)
		if err != nil || p.Name != o.profile || savedCommandCurrent(ctx, o.profile, p.CLIFileID) {
			return nil
		}
		prior = &p
	}
	cli, err := o.cli(ctx)
	if cli != nil {
		defer func() { resultErr = errors.Join(resultErr, cli.Close()) }()
	}
	if err != nil || cli == nil {
		return errors.Join(errors.New("agent command unavailable"), err)
	}
	command, err := fileid.Resolve(ctx, fileid.Self, cli.Program(), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || command.IsDir() || command.Key() != cli.FileKey() {
		return errors.New("agent command changed")
	}
	stored, err := command.Stored()
	if err != nil {
		return err
	}
	path, err := o.state()
	if err != nil {
		return err
	}
	state, err := fileid.Resolve(ctx, fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !state.IsDir() {
		return errors.New("connection state folder unavailable")
	}
	key, err := applications.NewRequestKey()
	if err != nil {
		return err
	}
	if prior != nil {
		profile := *prior
		profile.CLIFileID = stored.String()
		activation := *descriptor.Activation
		activation.Arguments = profile.Arguments()
		return o.save(ctx, descriptor, &activation, cli)
	}
	profile := startprofile.Profile{Name: o.profile, Context: "receiver-" + key, Folder: state.Path(), StateDir: state.Path(), CLIFileID: stored.String(), Model: agentStartRecipeModel, TimeoutMS: agentStartRecipeTimeoutMS, OutputBytes: agentStartRecipeOutputBytes, MemoryBytes: agentStartRecipeMemoryBytes, BudgetCents: agentStartRecipeBudgetCents}
	activation := &wire.ApplicationActivationRecipe{Arguments: profile.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: agentStartRecipeReadinessMS}
	return o.save(ctx, descriptor, activation, cli)
}

func (o agentStartRecipeOwner) save(ctx context.Context, descriptor wire.ApplicationDescriptor, activation *wire.ApplicationActivationRecipe, cli claudePanelImage) error {
	if _, err := startprofile.Parse(activation.Arguments); err != nil {
		return err
	}
	if err := cli.Check(ctx); err != nil {
		return err
	}
	// SetActivation is conditional on this exact descriptor: a concurrent
	// change or a recipe saved meanwhile refuses and is kept.
	changed, err := o.set(ctx, applications.ActivationConfiguration{Expected: descriptor, Activation: activation})
	if err != nil {
		return err
	}
	if changed.Outcome != wire.ApplicationOutcomeApplied {
		return fmt.Errorf("start recipe not saved (%s)", changed.Outcome)
	}
	return nil
}

// agentStartRecipeBound bounds one background registration.
const agentStartRecipeBound = 30 * time.Second

// ensureStartRecipe registers Claude Code's default start recipe once a user
// connection is saved. The conditional save is the guard against a
// concurrent change; one registration runs at a time.
func (c *claudePanelConnections) ensureStartRecipe(ctx context.Context) error {
	if c.startupRead == nil || c.startupSet == nil || c.cli == nil || c.state == nil || !c.startRecipe.TryLock() {
		return nil
	}
	defer c.startRecipe.Unlock()
	// The command is selected by the same check a Connections save uses.
	return agentStartRecipeOwner{profile: "claude-v1", read: c.startupRead, set: c.startupSet, cli: recipeImage(c.cli), state: c.state}.ensure(ctx)
}

// ensureStartRecipe registers Codex's default start recipe on the saved
// connection program, the same way.
func (c *codexPanelConnections) ensureStartRecipe(ctx context.Context, program string) error {
	application := nativecli.AgentApplicationName(filepath.Base(program))
	if nativecli.AgentApplicationBase(application) != "oa-codex-integration" || c.cli == nil || c.startupRead == nil || c.startupSet == nil || c.state == nil || !c.startRecipe.TryLock() {
		return nil
	}
	defer c.startRecipe.Unlock()
	return agentStartRecipeOwner{profile: "codex-v1", read: func(ctx context.Context) (wire.ApplicationDescriptor, applications.AgentEnrollmentResult, error) {
		return c.startupRead(ctx, application, program)
	}, set: c.startupSet, cli: recipeImage(c.cli), state: c.state}.ensure(ctx)
}

// recipeImage reads a saved connection's command as the recipe owner's
// image; a missing command stays a nil image.
func recipeImage(lookup func(context.Context) (claudePanelCLI, error)) func(context.Context) (claudePanelImage, error) {
	return func(ctx context.Context) (claudePanelImage, error) {
		cli, err := lookup(ctx)
		if cli == nil {
			return nil, err
		}
		return cli, err
	}
}

// ensureSavedAgentStartRecipes keeps each saved Claude Code and Codex
// connection's start recipe pinned to the agent's current command. The
// background presenter runs it at sign-in, so a vendor self-update re-pins.
// It reads the saved connections and writes only a missing or stale recipe.
func ensureSavedAgentStartRecipes(ctx context.Context, p *servicePanel) error {
	var errs []error
	claude := p.claudeConnectionsForPanel()
	if view, err := claude.read(ctx); err != nil {
		errs = append(errs, err)
	} else {
		for _, row := range view.Connections {
			if row.Kind == "user" && row.Registration == "saved" {
				errs = append(errs, claude.ensureStartRecipe(ctx))
				break
			}
		}
	}
	codex := p.codexConnectionsForPanel()
	if !codex.supported() {
		return errors.Join(errs...)
	}
	if view, err := codex.read(ctx); err != nil {
		errs = append(errs, err)
	} else if view.Owned && view.Registration == "saved" {
		errs = append(errs, codex.ensureStartRecipe(ctx, view.Program))
	}
	return errors.Join(errs...)
}

// ensureStartRecipeLater runs a registration after a connection was saved,
// so the save's reply never waits for it.
func ensureStartRecipeLater(ensure func(context.Context) error) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), agentStartRecipeBound)
		defer cancel()
		_ = ensure(ctx)
	}()
}

// savedCommandCurrent reports whether a recipe's saved command still names
// the same existing file. A Claude Code command must also be the newest
// version installed beside it, since Claude Code keeps the previous version.
func savedCommandCurrent(ctx context.Context, profile, saved string) bool {
	stored, err := fileid.ParseStoredFileID(saved)
	if err != nil {
		return false
	}
	current, state, err := fileid.Revalidate(ctx, stored)
	if err != nil || state != fileid.StateSame || current.IsDir() {
		return false
	}
	return profile != "claude-v1" || nativecli.ClaudeCLICurrent(ctx, current.Path())
}

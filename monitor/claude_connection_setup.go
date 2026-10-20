package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

const claudeIntegrationApplication = "oa-claude-integration"

type claudeConnectionSaved struct {
	Outcome    string `json:"outcome"`
	Enrollment string `json:"enrollment"`
	Companion  string `json:"companion,omitempty"`
}

type claudeEnrollmentClient interface {
	ReadAgentEnrollment(context.Context, string) (applications.AgentEnrollmentResult, error)
	SetAgentEnrollment(applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error)
}

type claudeContextEnrollmentClient struct {
	read func(context.Context, string) (applications.AgentEnrollmentResult, error)
	set  func(applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error)
}

func (c claudeContextEnrollmentClient) ReadAgentEnrollment(ctx context.Context, name string) (applications.AgentEnrollmentResult, error) {
	return c.read(ctx, name)
}
func (c claudeContextEnrollmentClient) SetAgentEnrollment(r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
	return c.set(r)
}

// Only the installer-selected P-J15 helper uses the existing descriptor owner.
// These native message agents declare no applications@3 command provider.
// The runtime captures the current image and owns the person's Keep question.
func (p *servicePanel) registerClaudeImage(ctx context.Context, image claudePanelImage) (string, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return "unknown", err
	}
	client := claudeContextEnrollmentClient{
		read: func(readCtx context.Context, name string) (applications.AgentEnrollmentResult, error) {
			return applications.NewApplicationsClient(binding.Transport().WithContext(readCtx)).ReadAgentEnrollment(name)
		},
		set: applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).SetAgentEnrollment,
	}
	return requestClaudeEnrollmentWithWait(ctx, image, client, func(ctx context.Context, descriptor fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
		directory, err := panelMachine().ResolveApplications(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return fwire.ApplicationChange{}, err
		}
		return directory.Register(ctx, descriptor)
	}, true)
}

func requestClaudeEnrollment(ctx context.Context, image claudePanelImage, client claudeEnrollmentClient, register func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error)) (string, error) {
	return requestClaudeEnrollmentWithWait(ctx, image, client, register, false)
}
func requestClaudeEnrollmentWithWait(ctx context.Context, image claudePanelImage, client claudeEnrollmentClient, register func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error), await bool) (string, error) {
	return agentConnectionSetup(ctx, image, client, register, claudeIntegrationApplication, "Claude integration", await, nil)
}

func (c *claudePanelConnections) saveProject(ctx context.Context, project fileid.FileID) (result claudeConnectionSaved, err error) {
	result = claudeConnectionSaved{Enrollment: "unknown"}
	if c.prepare == nil || c.enroll == nil || c.cli == nil {
		return result, errors.New("native connection setup unavailable")
	}
	if c.heldImage != nil && c.heldImage.Check(ctx) != nil {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		_ = c.heldImage.Close()
		c.heldImage = nil
	}
	if c.heldImage == nil {
		image, err := c.prepare(ctx)
		if err != nil {
			return result, err
		}
		if image == nil {
			return result, errors.New("native integration image unavailable")
		}
		c.heldImage = image
	}
	image := c.heldImage
	if err := image.Check(ctx); err != nil {
		return result, err
	}
	if c.declare != nil {
		if err := c.declare(ctx, image, project.Path()); err != nil {
			return result, err
		}
	}
	enrollment, err := c.enroll(ctx, image)
	result.Enrollment = enrollment
	if err != nil {
		return result, err
	}
	if enrollment != "enrolled" {
		result.Outcome = "permission_pending"
		return result, ctx.Err()
	}
	if c.admit == nil {
		return result, errors.New("native integration admission unavailable")
	}
	admitted, err := c.admit(ctx, image)
	if err != nil {
		return result, err
	}
	if admitted == nil {
		return result, errors.New("native integration admission unavailable")
	}
	if admitted.FileKey() != image.FileKey() {
		_ = admitted.Close()
		return result, errors.New("integration changed during permission review")
	}
	if err := image.Close(); err != nil {
		_ = admitted.Close()
		c.heldImage = nil
		return result, err
	}
	image = admitted
	c.heldImage = image
	// Keep the enrolled image through this joined setup operation, not for the
	// window lifetime: another Panel must be able to update it after completion.
	defer func() {
		err = errors.Join(err, image.Close())
		c.heldImage = nil
	}()
	cli, err := c.connectionCLI(ctx)
	if err != nil {
		return result, err
	}
	if cli == nil {
		return result, errors.New("Claude Code CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	if err := cli.Check(ctx); err != nil {
		return result, err
	}
	state, err := c.state()
	if err != nil {
		return result, err
	}
	if err := image.Check(ctx); err != nil {
		return result, err
	}
	args := []string{"connect", "claude", "--state-dir", state, "--claude", cli.Program()}
	if c.projectRequired {
		args = []string{"plugin", "claude", "install", "--project", project.Path(), "--state-dir", state, "--claude", cli.Program()}
	}
	if _, err = c.command(ctx, image.Program(), args...); err != nil {
		return result, err
	}
	if err = errors.Join(image.Check(ctx), cli.Check(ctx)); err != nil {
		return result, err
	}
	result.Outcome = "saved"
	if c.userMod && !c.projectRequired {
		result.Companion = "unavailable"
		if installed, err := c.installUserMod(ctx, image, cli, state); err == nil {
			result.Companion = "disabled"
			if installed {
				result.Companion = "configured"
			}
		}
	}
	return result, nil
}

func (c *claudePanelConnections) installUserMod(ctx context.Context, image claudePanelImage, cli claudePanelCLI, state string) (bool, error) {
	if err := errors.Join(image.Check(ctx), cli.Check(ctx)); err != nil {
		return false, err
	}
	permission, err := c.watchPermission(ctx, "status", image, cli, state)
	if err != nil {
		return false, err
	}
	if permission.State == "disabled" {
		asks, err := c.watchPermissionScope(ctx, "status", image, cli, state, "asks")
		if err != nil {
			return false, err
		}
		if asks.State != "owned" && asks.State != "preexisting" {
			return false, nil
		}
	}
	_, err = c.command(ctx, image.Program(), "plugin", "claude", "install", "--scope", "user", "--state-dir", state, "--claude", cli.Program())
	err = errors.Join(err, image.Check(ctx), cli.Check(ctx), ctx.Err())
	return err == nil, err
}

func (c *claudePanelConnections) readUserModState(ctx context.Context, manager claudePanelImage, state, program string) (result string) {
	if c.cli == nil {
		return "unknown"
	}
	cli, err := c.cli(ctx)
	if err != nil || cli == nil {
		return "unknown"
	}
	defer func() {
		if cli.Close() != nil {
			result = "unknown"
		}
	}()
	if errors.Join(manager.Check(ctx), cli.Check(ctx)) != nil {
		return "unknown"
	}
	raw, err := c.command(ctx, manager.Program(), "plugin", "claude", "status", "--scope", "user", "--state-dir", state, "--claude", cli.Program(), "--registered-program", program)
	var view struct {
		Outcome string `json:"outcome"`
		State   string `json:"state"`
	}
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &view) != nil || view.Outcome != "page" || errors.Join(manager.Check(ctx), cli.Check(ctx), ctx.Err()) != nil {
		return "unknown"
	}
	switch view.State {
	case "current", "absent", "update":
		return view.State
	}
	return "unknown"
}

func (c *claudePanelConnections) updateUserMod(ctx context.Context, image claudePanelImage) (err error) {
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("Claude Code CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return err
	}
	_, err = c.installUserMod(ctx, image, cli, state)
	return err
}

func prepareClaudePanelImage(ctx context.Context) (claudePanelImage, error) {
	kept, holdErr := setup.HoldEnrolledAgentImage(ctx, panelMachine(), setup.AgentClaude)
	if holdErr == nil {
		return kept, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	created, err := setup.PrepareAgentImage(ctx, setup.AgentClaude)
	if err != nil {
		review, reviewErr := setup.HoldAgentImageForReview(ctx, setup.AgentClaude)
		if reviewErr == nil {
			return review, nil
		}
		return nil, errors.Join(holdErr, err, reviewErr)
	}
	return created, nil
}

func admitAgentImageForConnection(ctx context.Context, image claudePanelImage, party setup.AgentParty) (claudePanelImage, error) {
	admitted, err := setup.HoldEnrolledAgentImageFrom(ctx, panelMachine(), party, image.Program())
	if err != nil {
		return nil, err
	}
	if admitted.FileKey() != image.FileKey() {
		_ = admitted.Close()
		return nil, errors.New("integration image changed during native permission review")
	}
	return admitted, nil
}

type claudeDisconnectRequest struct {
	Revision   string `json:"revision"`
	ProjectKey string `json:"project_key"`
	ProgramKey string `json:"program_key"`
	Name       string `json:"name"`
	Kind       string `json:"kind,omitempty"`
}

func (c *claudePanelConnections) updatePlugin(ctx context.Context, row claudeSavedConnection, image claudePanelImage) (err error) {
	project, err := fileid.Resolve(ctx, fileid.Self, row.Project, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !project.IsDir() || project.Key() != row.ProjectKey {
		return errors.New("saved plugin project changed")
	}
	if c.cli == nil || c.state == nil || c.command == nil {
		return errors.New("plugin update unavailable")
	}
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("Claude Code CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	state, err := c.state()
	if err != nil {
		return err
	}
	if err := errors.Join(image.Check(ctx), cli.Check(ctx)); err != nil {
		return err
	}
	if _, err := c.command(ctx, image.Program(), "plugin", "claude", "update", "--project", project.Path(), "--state-dir", state, "--claude", cli.Program()); err != nil {
		return fmt.Errorf("helper checked; personal plugin update could not be confirmed: %w", err)
	}
	return errors.Join(image.Check(ctx), cli.Check(ctx), ctx.Err())
}

func (c *claudePanelConnections) disconnect(ctx context.Context, request claudeDisconnectRequest) (err error) {
	if !c.actionMu.TryLock() {
		return errors.New("connection setup is already in progress")
	}
	defer c.actionMu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.mu.Lock()
	if c.closed || request.Revision != fmt.Sprint(c.revision) {
		c.mu.Unlock()
		return errors.New("saved connection view changed; check again")
	}
	var row *claudeSavedConnection
	for _, candidate := range c.rows {
		if candidate.Kind == request.Kind && candidate.ProjectKey == request.ProjectKey && candidate.ProgramKey == request.ProgramKey && candidate.Name == request.Name {
			copy := candidate
			row = &copy
			break
		}
	}
	c.activeCancel = cancel
	c.mu.Unlock()
	if row == nil || row.Registration != "saved" {
		return errors.New("saved connection is unavailable or changed")
	}
	// Saved configuration removal always uses the installed manager. A held
	// freshly copied image proves a file identity, not current native Keep.
	if c.image == nil {
		return errors.New("installed connection management unavailable")
	}
	image, err := c.image(ctx)
	if err != nil {
		return err
	}
	if image == nil {
		return errors.New("installed connection management unavailable")
	}
	defer func() { err = errors.Join(err, image.Close()) }()
	if err := image.Check(ctx); err != nil {
		return err
	}
	project, err := fileid.Resolve(ctx, fileid.Self, row.Project, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !project.IsDir() || project.Key() != row.ProjectKey {
		return errors.New("saved project folder changed")
	}
	cli, err := c.cli(ctx)
	if err != nil {
		return err
	}
	if cli == nil {
		return errors.New("Claude Code CLI unavailable")
	}
	defer func() { err = errors.Join(err, cli.Close()) }()
	if err := cli.Check(ctx); err != nil {
		return err
	}
	state, err := c.state()
	if err != nil {
		return err
	}
	args := []string{"disconnect", "claude", "--project", project.Path(), "--state-dir", state, "--claude", cli.Program(), "--name", row.Name}
	args = append(args, "--registered-program", row.Program)
	if row.Kind == "user" {
		if c.userMod {
			if err := c.removeUserMod(ctx, image, cli, state, row.Program); err != nil {
				return err
			}
			if _, err := c.watchPermission(ctx, "disable", image, cli, state); err != nil {
				return err
			}
		}
		args = []string{"disconnect", "claude", "--state-dir", state, "--claude", cli.Program(), "--name", row.Name, "--registered-program", row.Program}
	}
	if row.Kind == "plugin" {
		args = []string{"plugin", "claude", "remove", "--project", project.Path(), "--state-dir", state, "--claude", cli.Program(), "--registered-program", row.Program}
	}
	if _, err = c.command(ctx, image.Program(), args...); err != nil {
		return err
	}
	return errors.Join(image.Check(ctx), cli.Check(ctx), ctx.Err())
}

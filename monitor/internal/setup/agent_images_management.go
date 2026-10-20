//go:build windows || darwin

package setup

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/programfiles"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// EnrolledAgentImage retains one already-kept image across a management command.
// Its native enrollment proof is reread by Check; no proof is persisted here.
type EnrolledAgentImage struct {
	mu                                  sync.Mutex
	image                               *AgentImage
	program, key, application, revision string
	read                                func(context.Context, string) (applications.AgentEnrollmentResult, error)
}

func (a *EnrolledAgentImage) Program() string { return a.program }
func (a *EnrolledAgentImage) FileKey() string { return a.key }
func (a *EnrolledAgentImage) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.image == nil {
		return nil
	}
	err := a.image.Close()
	a.image = nil
	return err
}
func (a *EnrolledAgentImage) Check(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.image == nil {
		return errors.New("integration image is closed")
	}
	view, err := a.current(ctx)
	if err != nil {
		return err
	}
	if view.Revision != a.revision {
		return errors.New("integration enrollment changed; review it again")
	}
	return a.image.Check(ctx)
}

// HoldEnrolledAgentImage never creates/adopts a file. The sealed production
// reader is the existing verified native operator Applications@3 interface.
func HoldEnrolledAgentImage(ctx context.Context, machine *client.Machine, party AgentParty) (*EnrolledAgentImage, error) {
	return HoldEnrolledAgentImageFrom(ctx, machine, party, "")
}

// HoldEnrolledAgentImageFrom binds the current positively owned configuration
// locator to the native entry. It never adopts another namespace object.
func HoldEnrolledAgentImageFrom(ctx context.Context, machine *client.Machine, party AgentParty, program string) (*EnrolledAgentImage, error) {
	if machine == nil {
		return nil, errors.New("native application reader unavailable")
	}
	home, err := CurrentHome()
	if err != nil {
		return nil, err
	}
	if home.Isolated {
		return nil, errors.New("integration management requires the installed runtime")
	}
	if _, err := bootstrap.SelectInstalled(ctx); err != nil {
		return nil, err
	}
	binding, err := machine.ResolveService(ctx, "abstraction.facade/applications@3", client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return nil, err
	}
	read := func(ctx context.Context, name string) (applications.AgentEnrollmentResult, error) {
		return applications.NewApplicationsClient(binding.Transport().WithContext(ctx)).ReadAgentEnrollment(name)
	}
	root := filepath.Join(home.Config, "agent-integrations")
	if program == "" {
		return holdEnrolledAgentImage(ctx, root, party, read)
	}
	parent, err := fileid.Resolve(ctx, fileid.Self, filepath.Dir(program), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	actualRoot, err := fileid.Resolve(ctx, fileid.Self, root, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !parent.Equal(actualRoot) {
		return nil, errors.New("selected helper is outside its owned directory")
	}
	name := filepath.Base(program)
	application, err := agentApplicationName(party, name)
	if err != nil {
		return nil, err
	}
	return holdEnrolledAgentImageNamed(ctx, root, party, name, application, read)
}

func holdEnrolledAgentImage(ctx context.Context, root string, party AgentParty, read func(context.Context, string) (applications.AgentEnrollmentResult, error)) (*EnrolledAgentImage, error) {
	return holdEnrolledAgentImageNamed(ctx, root, party, agentImageName(party), "oa-"+string(party)+"-integration", read)
}

func holdEnrolledAgentImageNamed(ctx context.Context, root string, party AgentParty, name, application string, read func(context.Context, string) (applications.AgentEnrollmentResult, error)) (*EnrolledAgentImage, error) {
	if (party != AgentClaude && party != AgentCodex && party != AgentOpenCode) || read == nil {
		return nil, errors.New("unknown integration management target")
	}
	if err := fileid.SafeName(name); err != nil {
		return nil, err
	}
	if err := privatedir.CheckDir(root); err != nil {
		return nil, err
	}
	directory, err := programfiles.HoldDirectory(root)
	if err != nil {
		return nil, err
	}
	program := filepath.Join(root, name)
	file, key, err := openAgentImage(ctx, program)
	if err != nil {
		return nil, errors.Join(err, directory.Release())
	}
	image := newAgentImage(file, directory, program, key)
	result := &EnrolledAgentImage{image: image, program: program, key: key, application: application, read: read}
	view, err := result.current(ctx)
	if err == nil {
		result.revision = view.Revision
		err = result.Check(ctx)
	}
	if err != nil {
		return nil, errors.Join(err, result.Close())
	}
	return result, nil
}

func (a *EnrolledAgentImage) current(ctx context.Context) (applications.AgentEnrollment, error) {
	if err := a.image.Check(ctx); err != nil {
		return applications.AgentEnrollment{}, err
	}
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	result, err := a.read(call, a.application)
	if err != nil {
		return applications.AgentEnrollment{}, err
	}
	if result.Outcome != applications.OutcomePage || result.Enrollment == nil {
		return applications.AgentEnrollment{}, errors.New("integration enrollment unavailable")
	}
	view := *result.Enrollment
	if view.State == applications.AgentEnrollmentStatePending {
		return view, errors.New("integration identity is waiting for your answer")
	}
	if view.State != applications.AgentEnrollmentStateEnrolled || view.Application != a.application || view.Revision == "" {
		return view, errors.New("integration identity is not currently enrolled")
	}
	named, err := fileid.Open(ctx, fileid.Self, view.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return view, err
	}
	defer named.Close()
	if named.FileID().Key() != a.key || named.FileID().IsDir() || named.FileID().Links() != 1 {
		return view, errors.New("enrollment names a different native image")
	}
	return view, a.image.Check(ctx)
}

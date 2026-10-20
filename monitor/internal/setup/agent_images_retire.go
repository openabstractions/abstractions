//go:build windows || darwin

package setup

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func RemoveAgentEnrollment(ctx context.Context, machine *client.Machine, party AgentParty, prior AgentEnrollmentCapture) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	home, err := CurrentHome()
	if err != nil || home.Isolated {
		return errors.New("prior helper removal requires installed native setup")
	}
	if err := privatedir.CheckDir(filepath.Join(home.Config, "agent-integrations")); err != nil {
		return err
	}
	root, err := fileid.Resolve(ctx, fileid.Self, filepath.Join(home.Config, "agent-integrations"), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	parent, err := fileid.Resolve(ctx, fileid.Self, filepath.Dir(prior.Program), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || !parent.Equal(root) {
		return errors.New("prior helper removal directory changed")
	}
	name, err := agentApplicationName(party, filepath.Base(prior.Program))
	if err != nil || name != prior.Application {
		return errors.New("prior helper removal label changed")
	}
	binding, err := machine.ResolveService(ctx, "abstraction.facade/applications@3", client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return err
	}
	native := applications.NewApplicationsClient(binding.Transport().WithContext(ctx))
	return removeAgentEnrollment(ctx, prior, native.ReadAgentEnrollment, native.SetAgentEnrollment)
}

func removeAgentEnrollment(ctx context.Context, prior AgentEnrollmentCapture, read func(string) (applications.AgentEnrollmentResult, error), set func(applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error)) error {
	if prior.Application == "" || prior.Program == "" || prior.FileKey == "" || prior.Revision == "" {
		return errors.New("prior helper removal capture unavailable")
	}
	current, err := read(prior.Application)
	if err != nil || current.Outcome != applications.OutcomePage || current.Enrollment == nil {
		return errors.New("prior helper enrollment unavailable; cleanup pending")
	}
	entry := current.Enrollment
	image, err := fileid.Resolve(ctx, fileid.Self, entry.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || entry.Application != prior.Application || entry.Program != prior.Program || image.Key() != prior.FileKey {
		return errors.New("prior helper image changed; cleanup pending")
	}
	if entry.State == applications.AgentEnrollmentStateNone {
		return nil
	}
	if entry.Revision != prior.Revision || entry.State != applications.AgentEnrollmentStateEnrolled {
		return errors.New("prior helper enrollment changed; cleanup pending")
	}
	result, err := set(applications.AgentEnrollmentRequest{Application: prior.Application, ExpectedRevision: prior.Revision, Enabled: false})
	if err != nil || result.Outcome != applications.OutcomeApplied || result.Enrollment == nil || result.Enrollment.State != applications.AgentEnrollmentStateNone {
		return errors.New("prior helper removal unconfirmed; cleanup pending")
	}
	confirmed, err := read(prior.Application)
	if err != nil || confirmed.Outcome != applications.OutcomePage || confirmed.Enrollment == nil || confirmed.Enrollment.Application != prior.Application || confirmed.Enrollment.Program != prior.Program || confirmed.Enrollment.State != applications.AgentEnrollmentStateNone {
		return errors.New("prior helper removal readback unavailable; cleanup pending")
	}
	actual, err := fileid.Resolve(ctx, fileid.Self, confirmed.Enrollment.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil || actual.Key() != prior.FileKey {
		return errors.New("prior helper changed after removal; cleanup pending")
	}
	return nil
}

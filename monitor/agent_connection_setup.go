package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// agentConnectionSetup owns native enrollment for Claude, Codex and OpenCode.
// Each consumer supplies its captured image and presentation of service errors;
// Applications alone creates the person's agent Keep receipt.
func agentConnectionSetup(ctx context.Context, image claudePanelImage, client claudeEnrollmentClient, register func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error), base, title string, await bool, failure func(applications.AgentEnrollmentResult) error) (string, error) {
	application := agentImageApplication(image, base)
	if err := image.Check(ctx); err != nil {
		return "unknown", err
	}
	observed, err := client.ReadAgentEnrollment(ctx, application)
	if err != nil {
		return "unknown", err
	}
	if observed.Outcome == applications.OutcomeUnknown {
		changed, err := register(ctx, fwire.ApplicationDescriptor{Name: application, Program: image.Program(), Title: title})
		if err != nil {
			return "unknown", err
		}
		if changed.Outcome != fwire.ApplicationOutcomeApplied {
			return "unknown", fmt.Errorf("native assistant registration refused (%s: %s)", changed.Outcome, changed.Reason)
		}
		if err := image.Check(ctx); err != nil {
			return "unknown", err
		}
		observed, err = client.ReadAgentEnrollment(ctx, application)
		if err != nil {
			return "unknown", err
		}
	}
	if observed.Outcome != applications.OutcomePage || observed.Enrollment == nil {
		return "unknown", errors.New("native assistant registration could not be confirmed")
	}
	entry := observed.Enrollment
	if !agentEnrollmentMatchesImage(ctx, entry.Program, image) {
		return "unknown", errors.New("native assistant registration names a different image")
	}
	if entry.State == applications.AgentEnrollmentStateEnrolled {
		return "enrolled", nil
	}
	request := applications.AgentEnrollmentRequest{Application: application, ExpectedRevision: entry.Revision, Enabled: true}
	if application == nativecli.AgentApplicationForParty(product.InstalledAgentConnectionParty()) {
		bind := true
		request.BindCatalogueHost = &bind
	}
	requested, err := client.SetAgentEnrollment(request)
	if err != nil {
		return "unknown", err
	}
	if requested.Outcome != applications.OutcomeApplied || requested.Enrollment == nil {
		if failure != nil {
			return "unknown", failure(requested)
		}
		return "unknown", fmt.Errorf("native assistant Keep request refused (%s)", requested.Outcome)
	}
	switch requested.Enrollment.State {
	case applications.AgentEnrollmentStatePending:
		if await {
			err := waitClaudeEnrollment(ctx, image, client, *requested.Enrollment, 2*time.Minute)
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				return "pending", nil
			}
			if err != nil {
				return "unknown", err
			}
			return "enrolled", nil
		}
		return "pending", nil
	case applications.AgentEnrollmentStateEnrolled:
		return "enrolled", nil
	default:
		return "none", errors.New("native assistant Keep has not been requested")
	}
}

func agentEnrollmentMatchesImage(ctx context.Context, program string, image claudePanelImage) bool {
	registered, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	return err == nil && registered.Key() == image.FileKey()
}

func waitClaudeEnrollment(ctx context.Context, image claudePanelImage, client claudeEnrollmentClient, pending applications.AgentEnrollment, limit time.Duration) error {
	wait, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	return waitCodexEnrollment(wait, pending, func(poll context.Context) (applications.AgentEnrollmentResult, error) {
		if err := image.Check(poll); err != nil {
			return applications.AgentEnrollmentResult{}, err
		}
		return client.ReadAgentEnrollment(poll, pending.Application)
	})
}

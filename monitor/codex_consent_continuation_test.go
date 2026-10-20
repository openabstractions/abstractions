package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

func TestCodexConsentOriginalAddContinuesAfterKeep(t *testing.T) {
	c := codexPanelFixture(t)
	pending := applications.AgentEnrollment{Application: "oa-codex-integration", Program: "codex-agent", Revision: "generation", State: applications.AgentEnrollmentStatePending}
	reads := 0
	c.enroll = func(ctx context.Context, _ claudePanelImage) (string, error) {
		err := waitCodexEnrollment(ctx, pending, func(context.Context) (applications.AgentEnrollmentResult, error) {
			reads++
			current := pending
			if reads > 1 {
				current.State = applications.AgentEnrollmentStateEnrolled
			}
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &current}, nil
		})
		return "enrolled", err
	}
	calls := 0
	c.command = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Registration: "saved"})
	}
	view, err := c.change(context.Background(), false, "1")
	if err != nil || view.Registration != "saved" || calls != 1 || reads != 2 {
		t.Fatal(view, err, calls, reads)
	}
	c.close()
}

func TestCodexConsentCancellationAndChangedOwnershipNeverConnect(t *testing.T) {
	for _, mode := range []string{"cancel", "closed", "changed", "refused", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			c := codexPanelFixture(t)
			pending := applications.AgentEnrollment{Application: "oa-codex-integration", Program: "codex-agent", Revision: "generation", State: applications.AgentEnrollmentStatePending}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			c.enroll = func(ctx context.Context, _ claudePanelImage) (string, error) {
				return "unknown", waitCodexEnrollment(ctx, pending, func(context.Context) (applications.AgentEnrollmentResult, error) {
					current := pending
					switch mode {
					case "cancel":
						cancel()
					case "closed":
						c.cancelAction()
					case "changed":
						current.Revision = "replacement"
					case "refused":
						current.State = applications.AgentEnrollmentStateNone
					case "unavailable":
						return applications.AgentEnrollmentResult{}, errors.New("unavailable")
					}
					return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &current}, nil
				})
			}
			c.command = func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("connect after canceled or unproved Keep")
				return nil, nil
			}
			if _, err := c.change(ctx, false, "1"); err == nil {
				t.Fatal("expected refusal")
			}
			c.close()
		})
	}
}

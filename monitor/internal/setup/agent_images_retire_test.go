//go:build windows || darwin

package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestAgentRemovalUsesOnlyExactPriorNativeCapture(t *testing.T) {
	for _, mode := range []string{"retire", "already-inactive", "revision-changed", "image-changed", "unavailable", "set-unknown", "readback-unknown"} {
		t.Run(mode, func(t *testing.T) {
			program := filepath.Join(t.TempDir(), agentImageName(AgentClaude))
			if err := os.WriteFile(program, []byte("old owned fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			id, err := fileid.Resolve(context.Background(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
			if err != nil {
				t.Fatal(err)
			}
			prior := AgentEnrollmentCapture{Application: "oa-claude-integration", Program: program, FileKey: id.Key(), Revision: "captured"}
			entry := applications.AgentEnrollment{Application: prior.Application, Program: program, Revision: prior.Revision, State: applications.AgentEnrollmentStateEnrolled}
			if mode == "already-inactive" {
				entry.State = applications.AgentEnrollmentStateNone
				entry.Revision = "revoked"
			}
			if mode == "revision-changed" {
				entry.Revision = "external"
			}
			if mode == "image-changed" {
				if err := os.Rename(program, program+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(program, []byte("unknown replacement"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			sets := 0
			read := func(name string) (applications.AgentEnrollmentResult, error) {
				if name != prior.Application {
					t.Fatal("wrong removal target")
				}
				if mode == "unavailable" || mode == "readback-unknown" && sets > 0 {
					return applications.AgentEnrollmentResult{}, errors.New("unknown")
				}
				copy := entry
				return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &copy}, nil
			}
			set := func(request applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
				sets++
				if request.Application != prior.Application || request.ExpectedRevision != prior.Revision || request.Enabled {
					t.Fatal("unfenced removal", request)
				}
				if mode == "set-unknown" {
					return applications.AgentEnrollmentResult{}, errors.New("unknown")
				}
				entry.State = applications.AgentEnrollmentStateNone
				entry.Revision = "retired"
				return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &entry}, nil
			}
			err = removeAgentEnrollment(context.Background(), prior, read, set)
			if mode == "retire" || mode == "already-inactive" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unknown removal declared success")
			}
			if mode == "revision-changed" || mode == "image-changed" || mode == "unavailable" || mode == "already-inactive" {
				if sets != 0 {
					t.Fatal("changed/inactive capture mutated")
				}
			} else if sets != 1 {
				t.Fatal(sets)
			}
		})
	}
}

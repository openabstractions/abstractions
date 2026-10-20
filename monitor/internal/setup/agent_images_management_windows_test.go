package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func enrolledImageFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := privatedir.Tighten(root); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "oa-claude-integration.exe")
	if err := os.WriteFile(program, []byte("fixture image; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	return root, program
}
func enrolledImageReply(program string) applications.AgentEnrollmentResult {
	return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "oa-claude-integration", Program: program, Revision: "kept-revision", State: applications.AgentEnrollmentStateEnrolled}}
}

func TestAgentReviewCandidateRecoversExistingDeclinedImageWithoutAdmission(t *testing.T) {
	root, program := enrolledImageFixture(t)
	for _, state := range []applications.AgentEnrollmentState{applications.AgentEnrollmentStatePending, applications.AgentEnrollmentStateNone} {
		read := func(context.Context, string) (applications.AgentEnrollmentResult, error) {
			reply := enrolledImageReply(program)
			reply.Enrollment.State = state
			return reply, nil
		}
		if image, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read); err == nil {
			image.Close()
			t.Fatal("unkept image admitted")
		}
		if image, err := copyAgentImage(context.Background(), program, root, AgentClaude); err == nil {
			image.Close()
			t.Fatal("existing candidate overwritten")
		}
		review, err := holdAgentImageForReview(context.Background(), root, AgentClaude)
		if err != nil {
			t.Fatal(err)
		}
		if err := review.Check(context.Background()); err != nil {
			t.Fatal(err)
		}
		kept, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
			return enrolledImageReply(program), nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if kept.FileKey() != review.FileKey() {
			t.Fatal("permission was for a different image")
		}
		kept.Close()
		review.Close()
	}
	if image, err := holdAgentImageForReview(context.Background(), root, AgentParty("unknown")); err == nil {
		image.Close()
		t.Fatal("unknown review target")
	}
}

func TestEnrolledAgentImageRestartHoldRechecksAndRetires(t *testing.T) {
	root, program := enrolledImageFixture(t)
	calls := 0
	read := func(ctx context.Context, name string) (applications.AgentEnrollmentResult, error) {
		calls++
		if name != "oa-claude-integration" {
			t.Fatal(name)
		}
		if err := os.WriteFile(program, []byte("replacement"), 0600); err == nil {
			t.Fatal("image was not held before authoritative read")
		}
		return enrolledImageReply(program), nil
	}
	image, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || image.FileKey() == "" {
		t.Fatal(calls)
	}
	if err := image.Check(context.Background()); err != nil || calls != 3 {
		t.Fatal(err, calls)
	}
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	if err := image.Check(context.Background()); err == nil {
		t.Fatal("closed proof remained usable")
	}
	if err := os.WriteFile(program, []byte("after join"), 0600); err != nil {
		t.Fatal("hold leaked", err)
	}
}

func TestEnrolledAgentImageRefusesUnprovenAndChangedViews(t *testing.T) {
	for _, mode := range []string{"none", "none-history", "pending", "unavailable", "foreign", "revision", "read-error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			root, program := enrolledImageFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			foreign := filepath.Join(root, "foreign.exe")
			if err := os.WriteFile(foreign, []byte("foreign"), 0600); err != nil {
				t.Fatal(err)
			}
			read := func(context.Context, string) (applications.AgentEnrollmentResult, error) {
				calls++
				result := enrolledImageReply(program)
				switch mode {
				case "none":
					result.Enrollment.State = applications.AgentEnrollmentStateNone
					result.Enrollment.Revision = ""
				case "none-history":
					result.Enrollment.State = applications.AgentEnrollmentStateNone
				case "pending":
					result.Enrollment.State = applications.AgentEnrollmentStatePending
				case "unavailable":
					result.Outcome = applications.OutcomeUnavailable
				case "foreign":
					result.Enrollment.Program = foreign
				case "revision":
					if calls == 2 {
						result.Enrollment.Revision = "changed"
					}
				case "read-error":
					return result, errors.New("native reader unavailable")
				case "cancelled":
					cancel()
				}
				return result, nil
			}
			if image, err := holdEnrolledAgentImage(ctx, root, AgentClaude, read); err == nil {
				image.Close()
				t.Fatal("unproven view admitted")
			}
			if err := os.WriteFile(program, []byte("after refusal"), 0600); err != nil {
				t.Fatal("failed admission leaked hold", err)
			}
		})
	}
}

func TestEnrolledAgentImageRevocationBeforeUseRefuses(t *testing.T) {
	root, program := enrolledImageFixture(t)
	enrolled := true
	read := func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		result := enrolledImageReply(program)
		if !enrolled {
			result.Enrollment.State = applications.AgentEnrollmentStateNone
		}
		return result, nil
	}
	image, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	enrolled = false
	if err := image.Check(context.Background()); err == nil {
		t.Fatal("revoked enrollment admitted")
	}
}

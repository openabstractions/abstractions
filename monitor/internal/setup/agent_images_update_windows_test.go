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

func TestAgentImageUpdateEqualAndForeignPreserveCurrent(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{true: "foreign", false: "equal"}[foreign], func(t *testing.T) {
			root, program := enrolledImageFixture(t)
			old, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
				return enrolledImageReply(program), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			source, err := holdAgentImageForReview(context.Background(), root, AgentClaude)
			if err != nil {
				t.Fatal(err)
			}
			defer source.Close()
			calls := 0
			image, changed, backup, err := updateHeldAgentImage(context.Background(), old, source, AgentClaude, func(context.Context, string, string) error {
				if foreign {
					return errors.New("foreign")
				}
				return nil
			}, func(context.Context, string, agentImagePin, string, agentImagePin) (agentReplacementResult, error) {
				calls++
				return agentReplacementResult{}, nil
			})
			if foreign {
				if err == nil {
					t.Fatal("foreign")
				}
			} else {
				if err != nil || changed || backup != "" {
					t.Fatal(changed, backup, err)
				}
				image.Close()
			}
			if calls != 0 {
				t.Fatal("equal/foreign invoked replacement")
			}
		})
	}
}

func TestAgentImageUpdateRetainsFormerBytesOnBusyFailure(t *testing.T) {
	testAgentImageChangedUpdate(t, false)
}

func TestAgentImageUpdatePublishesFreshIdentityAndRetainsExactOldObject(t *testing.T) {
	testAgentImageChangedUpdate(t, true)
}

func testAgentImageChangedUpdate(t *testing.T, publish bool) {
	t.Helper()
	root, program := enrolledImageFixture(t)
	original, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	old, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		return enrolledImageReply(program), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	if err := privatedir.Tighten(sourceRoot); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceRoot, agentImageName(AgentClaude))
	if err := os.WriteFile(sourcePath, []byte("new installed component"), 0600); err != nil {
		t.Fatal(err)
	}
	source, err := holdAgentImageForReview(context.Background(), sourceRoot, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	calls := 0
	image, changed, backup, err := updateHeldAgentImage(context.Background(), old, source, AgentClaude, func(context.Context, string, string) error { return nil }, func(ctx context.Context, target string, before agentImagePin, stage string, next agentImagePin) (agentReplacementResult, error) {
		calls++
		if target != program || before.Hash == next.Hash || before.ID.Key() == next.ID.Key() {
			t.Fatal("wrong replacement pins")
		}
		if publish {
			return replaceOwnedAgentImage(ctx, target, before, stage, next)
		}
		return agentReplacementResult{}, errors.New("busy")
	})
	if publish {
		if err != nil || image == nil || !changed || backup == "" || calls != 1 {
			t.Fatal(image, changed, backup, err, calls)
		}
		defer image.Close()
		previous, readErr := os.ReadFile(backup)
		current, currentErr := os.ReadFile(program)
		if readErr != nil || currentErr != nil || string(previous) != string(original) || string(current) != "new installed component" {
			t.Fatal("update bytes lost", readErr, currentErr)
		}
		return
	}
	if err == nil || image != nil || changed || calls != 1 || backup != "" {
		t.Fatal(image, changed, backup, err, calls)
	}
	current, currentErr := os.ReadFile(program)
	if currentErr != nil || string(current) != string(original) {
		t.Fatal("former image lost", currentErr)
	}
}

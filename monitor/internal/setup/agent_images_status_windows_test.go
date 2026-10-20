package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

func TestAgentImageCurrencyRequiresNativeAndConnectionOwnership(t *testing.T) {
	ctx := context.Background()
	root, program := enrolledImageFixture(t)
	read := func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		return enrolledImageReply(program), nil
	}
	old, err := holdEnrolledAgentImage(ctx, root, AgentClaude, read)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	source, err := holdAgentImageForReview(ctx, root, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if got := agentImageCurrency(ctx, old, source, program, old.FileKey()); got != "current" {
		t.Fatal(got)
	}
	for _, pair := range [][2]string{{"", ""}, {program + ".foreign", ""}, {program, "foreign-key"}} {
		if got := agentImageCurrency(ctx, old, source, pair[0], pair[1]); got != "unknown" {
			t.Fatal(pair, got)
		}
	}
	other, _ := enrolledImageFixture(t)
	if err := os.WriteFile(filepath.Join(other, agentImageName(AgentClaude)), []byte("new installed payload"), 0700); err != nil {
		t.Fatal(err)
	}
	newer, err := holdAgentImageForReview(ctx, other, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	defer newer.Close()
	if got := agentImageCurrency(ctx, old, newer, program, old.FileKey()); got != "update" {
		t.Fatal(got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := agentImageCurrency(cancelled, old, source, program, old.FileKey()); got != "unknown" {
		t.Fatal(got)
	}
	old.read = func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		result := enrolledImageReply(program)
		result.Enrollment.State = applications.AgentEnrollmentStateNone
		return result, nil
	}
	if got := agentImageCurrency(ctx, old, source, program, old.FileKey()); got != "unknown" {
		t.Fatal(got)
	}
}

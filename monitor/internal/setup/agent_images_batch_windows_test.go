package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"golang.org/x/sys/windows"
)

func batchImageFixture(t *testing.T) *StagedAgentUpdate {
	return batchImageFixtureAt(t, "", AgentClaude)
}

func batchImageFixtureAt(t *testing.T, root string, party AgentParty) *StagedAgentUpdate {
	t.Helper()
	return batchImageFixtureImage(t, root, party, []byte("fixture image; never executed"))
}

func batchImageFixtureImage(t *testing.T, root string, party AgentParty, image []byte) *StagedAgentUpdate {
	t.Helper()
	return batchImageFixtureBytes(t, root, party, image, []byte("new installed batch component"))
}

func batchImageFixtureBytes(t *testing.T, root string, party AgentParty, image, installed []byte) *StagedAgentUpdate {
	t.Helper()
	if root == "" {
		root = t.TempDir()
	}
	if err := privatedir.Tighten(root); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, agentImageName(party))
	if err := os.WriteFile(program, image, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := holdEnrolledAgentImage(context.Background(), root, party, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		reply := enrolledImageReply(program)
		reply.Enrollment.Application = "oa-" + string(party) + "-integration"
		return reply, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := t.TempDir()
	if err := privatedir.Tighten(sourceRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, agentImageName(party)), installed, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := holdAgentImageForReview(context.Background(), sourceRoot, party)
	if err != nil {
		t.Fatal(err)
	}
	s, err := stageHeldAgentUpdate(context.Background(), old, source, party, func(context.Context, string, string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestAgentUpdateStagePreservesOldAndRequiresExactResume(t *testing.T) {
	s := batchImageFixture(t)
	before, err := os.ReadFile(s.Old.Program())
	if err != nil {
		t.Fatal(err)
	}
	stageProgram, stageKey := s.Image.Program(), s.Image.FileKey()
	if s.ResumeRequired {
		t.Fatal("new stage requested resume")
	}
	if err := s.Image.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := stageHeldAgentUpdate(context.Background(), s.Old, s.Source, AgentClaude, s.verify)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !again.ResumeRequired || again.Image.Program() != stageProgram || again.Image.FileKey() != stageKey {
		t.Fatal("stage identity changed")
	}
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{again}); !errors.Is(err, ErrAgentUpdateResumeProofRequired) {
		t.Fatal("unknown existing stage lost resume refusal", err)
	}
	if err := again.ConfirmResume(stageProgram, "foreign-key"); err == nil {
		t.Fatal("foreign stage key accepted")
	}
	if err := again.ConfirmResume(stageProgram, stageKey); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Old.Program())
	if err != nil || string(after) != string(before) {
		t.Fatal("staging modified old image", err)
	}
}

func TestAgentUpdateBatchDirectoryRefusalHasOwnedReason(t *testing.T) {
	a, b := batchImageFixture(t), batchImageFixture(t)
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{a, b}); !errors.Is(err, ErrAgentUpdateDirectoriesDiffer) {
		t.Fatal("different batch roots lost directory refusal", err)
	}
}

func TestAgentUpdateStageRechecksOwnedConnectionBeforePublication(t *testing.T) {
	s := batchImageFixture(t)
	s.verify = func(context.Context, string, string) error { return errors.New("foreign row") }
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err == nil {
		t.Fatal("changed ownership published")
	}
	if _, err := os.Stat(s.Old.Program()); err != nil {
		t.Fatal("old image lost", err)
	}
	if _, err := os.Stat(s.Image.Program()); err != nil {
		t.Fatal("stage lost", err)
	}
}

func TestAgentUpdateBatchRetainsExactPriorImageLocator(t *testing.T) {
	s := batchImageFixture(t)
	previous := s.oldPin
	backup := s.Image.Program() + ".previous"
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err != nil {
		t.Fatal(err)
	}
	if got := replacementPin(t, backup); !sameAgentPin(got, previous) {
		t.Fatal("prior image locator lost native identity/content")
	}
}

func TestAgentUpdateBatchPreservesForeignBackupOccupant(t *testing.T) {
	s := batchImageFixture(t)
	backup := s.Image.Program() + ".previous"
	if err := os.WriteFile(backup, []byte("foreign occupant"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err == nil {
		t.Fatal("foreign backup overwritten")
	}
	if got := replacementPin(t, s.Old.Program()); !sameAgentPin(got, s.oldPin) {
		t.Fatal("old image moved after backup refusal")
	}
	if b, err := os.ReadFile(backup); err != nil || string(b) != "foreign occupant" {
		t.Fatal(string(b), err)
	}
}

func TestAgentUpdateBatchBusyMemberPreservesEveryOldImage(t *testing.T) {
	root := t.TempDir()
	a, b := batchImageFixtureAt(t, root, AgentClaude), batchImageFixtureAt(t, root, AgentCodex)
	busy, err := openArchiveSource(context.Background(), b.Old.Program())
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	// Another program's handle is not an app to restart; the Panel reads the
	// native sharing status as "in use by another program".
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{a, b}); !errors.Is(err, windows.STATUS_SHARING_VIOLATION) || errors.Is(err, ErrAgentUpdateRestartRequired) {
		t.Fatal("held connector not reported as in use", err)
	}
	for _, s := range []*StagedAgentUpdate{a, b} {
		pin := replacementPin(t, s.Old.Program())
		if !sameAgentPin(pin, s.oldPin) {
			t.Fatal("old image changed before complete batch custody")
		}
		stage := replacementPin(t, s.Image.Program())
		if !sameAgentPin(stage, s.imagePin) {
			t.Fatal("stage changed before complete batch custody")
		}
	}
}

// Owner R16, 2026-11-09: Claude and Codex kept their connector helpers running
// from the R15 install on, so the batch waited for all three apps to close at
// once and OpenCode restarted onto the old connector. A running helper now
// keeps its moved image and every member publishes in one batch.
func TestAgentUpdateBatchPublishesOverRunningHelpers(t *testing.T) {
	root := t.TempDir()
	running := batchImageFixtureImage(t, root, AgentClaude, testExecutableBytes(t))
	idle := batchImageFixtureAt(t, root, AgentOpenCode)
	stop := startRunningAgentImage(t, running.Old.Program())
	stages := []*StagedAgentUpdate{running, idle}
	pins := map[string]agentReplacementPin{}
	for _, s := range stages {
		pins[s.Old.Program()] = s.imagePin
	}
	if err := PublishAgentUpdates(context.Background(), stages); err != nil {
		t.Fatal("running helper held the batch back:", err)
	}
	for program, staged := range pins {
		if got := replacementPin(t, program); !sameAgentPin(got, staged) {
			t.Fatal("member not published:", program)
		}
	}
	if got := replacementPin(t, running.Image.Program()+".previous"); !sameAgentPin(got, running.oldPin) {
		t.Fatal("running helper image not retained at its backup")
	}
	if err := stop(); err != nil {
		t.Fatal("running helper stopped working:", err)
	}
}

// Undo publishes the retained backup as the staged image. The app that kept
// running the previous connector runs that backup, and Undo must not wait for
// it to close.
func TestAgentUpdateBatchPublishesRunningStagedImage(t *testing.T) {
	s := batchImageFixtureBytes(t, "", AgentCodex, []byte("current connector"), testExecutableBytes(t))
	stop := startRunningAgentImage(t, s.Image.Program())
	program, staged := s.Old.Program(), s.imagePin
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err != nil {
		t.Fatal("running staged image held Undo back:", err)
	}
	if got := replacementPin(t, program); !sameAgentPin(got, staged) {
		t.Fatal("running staged image not published")
	}
	if err := stop(); err != nil {
		t.Fatal("running staged image stopped working:", err)
	}
}

func TestAgentUpdateBatchRetainedStageResumesAfterAdmissionBlockerCloses(t *testing.T) {
	s := batchImageFixtureAt(t, "", AgentCodex)
	stagePath, stageKey := s.Image.Program(), s.Image.FileKey()
	oldPin, stagedPin := s.oldPin, s.imagePin
	busy, err := openArchiveSource(context.Background(), s.Old.Program())
	if err != nil {
		t.Fatal(err)
	}
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); !errors.Is(err, windows.STATUS_SHARING_VIOLATION) {
		t.Fatal("admission blocker lost its sharing status", err)
	}
	if err := busy.Close(); err != nil {
		t.Fatal(err)
	}
	root, program := filepath.Dir(s.Old.Program()), s.Old.Program()
	old, err := holdEnrolledAgentImage(context.Background(), root, AgentCodex, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		reply := enrolledImageReply(program)
		reply.Enrollment.Application = "oa-codex-integration"
		return reply, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	again, err := stageHeldAgentUpdate(context.Background(), old, s.Source, AgentCodex, s.verify)
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if !again.ResumeRequired || !sameAgentPin(again.imagePin, stagedPin) {
		t.Fatal("retry replaced the previously approved staged object")
	}
	if err := again.ConfirmResume(stagePath, stageKey); err != nil {
		t.Fatal(err)
	}
	// The first attempt leaves the shared CAS lock file in place. Its existence
	// supplies no ownership and must not prevent the same approved publication.
	if _, err := os.Stat(filepath.Join(root, ".oa-agent-update-batch.lock")); err != nil {
		t.Fatal(err)
	}
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{again}); err != nil {
		t.Fatal(err)
	}
	if got := replacementPin(t, program); !sameAgentPin(got, stagedPin) {
		t.Fatal("retry did not publish the exact retained stage")
	}
	if got := replacementPin(t, stagePath+".previous"); !sameAgentPin(got, oldPin) {
		t.Fatal("retry did not retain the exact previous image")
	}
}

func TestAgentUpdateConcurrentBatchesDoNotSplitCustodyBarrier(t *testing.T) {
	root := t.TempDir()
	a, b := batchImageFixtureAt(t, root, AgentClaude), batchImageFixtureAt(t, root, AgentCodex)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	go func() { results <- PublishAgentUpdates(ctx, []*StagedAgentUpdate{a, b}) }()
	go func() { results <- PublishAgentUpdates(ctx, []*StagedAgentUpdate{b, a}) }()
	success := 0
	for range 2 {
		select {
		case err := <-results:
			if err == nil {
				success++
			}
		case <-ctx.Done():
			t.Fatal("concurrent batch custody deadlocked")
		}
	}
	if success != 1 {
		t.Fatal("one exact batch should publish", success)
	}
}

func TestAgentUpdatePublishedBytesDoNotProveNativeEnrollment(t *testing.T) {
	s := batchImageFixture(t)
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err != nil {
		t.Fatal(err)
	}
	current := replacementPin(t, s.Old.Program())
	if current.Hash != s.sourcePin.Hash || current.Bytes != s.sourcePin.Bytes {
		t.Fatal("publication did not match installed source")
	}
	// ReadAgentEnrollment reports None while the retained service intent's
	// final Commit has not enrolled the new image, even with current bytes.
	held, err := holdEnrolledAgentImage(context.Background(), filepath.Dir(s.Old.Program()), AgentClaude, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		reply := enrolledImageReply(s.Old.Program())
		reply.Enrollment.State = applications.AgentEnrollmentStateNone
		return reply, nil
	})
	if held != nil {
		held.Close()
	}
	if err == nil {
		t.Fatal("installed-equivalent bytes bypassed native enrollment")
	}
}

func TestAgentUpdateSameBytesRequireBoundClaudeCompanionDrift(t *testing.T) {
	root, program := enrolledImageFixture(t)
	old, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		return enrolledImageReply(program), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	source, err := holdAgentImageForReview(context.Background(), root, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	owned := func(context.Context, string, string) error { return nil }
	if stage, err := stageHeldAgentUpdate(context.Background(), old, source, AgentClaude, owned); !errors.Is(err, ErrAgentImageCurrent) || stage != nil {
		t.Fatal("current bytes should require no staging", stage, err)
	}
	proof := func(_ context.Context, path, key string) error {
		t.Fatal("companion entered helper staging")
		return nil
	}
	if stage, err := stageHeldAgentUpdate(context.Background(), old, source, AgentCodex, owned, proof); !errors.Is(err, ErrAgentImageCurrent) || stage != nil {
		t.Fatal("Claude companion proof widened Codex staging", stage, err)
	}
	foreign := errors.New("connection ownership changed")
	if _, err := stageHeldAgentUpdate(context.Background(), old, source, AgentClaude, func(context.Context, string, string) error { return foreign }); !errors.Is(err, foreign) || errors.Is(err, ErrAgentImageCurrent) {
		t.Fatal("matching bytes hid changed connection ownership", err)
	}
	stage, err := stageHeldAgentUpdate(context.Background(), old, source, AgentClaude, owned, proof)
	if err != ErrAgentImageCurrent || stage != nil {
		t.Fatal("same-byte companion drift created helper batch", stage, err)
	}
}

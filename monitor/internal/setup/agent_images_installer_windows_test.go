package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestInstallerPhysicalPreparationDoesNotWaitForUnrelatedBatch(t *testing.T) {
	// These are the same held old/source/staged objects retained while a native
	// update question is pending. The fixture writes only inside TEMP.
	stage := batchImageFixtureAt(t, "", AgentClaude)
	root := filepath.Dir(stage.Old.Program())
	unlock, err := cas.LockContext(t.Context(), filepath.Join(root, ".oa-agent-update-batch"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	image, err := copyAgentImage(ctx, stage.Source.Program(), root, AgentOpenCode)
	if err != nil {
		t.Fatal("unrelated publication lock blocked physical preparation:", err)
	}
	defer image.Close()
	if err := sameInstalledAgentImage(ctx, image, stage.Source); err != nil {
		t.Fatal(err)
	}
	if _, err := image.InstallerReceipt(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestInstallerAgentImageKeptReceiptRequiresCurrentCustody(t *testing.T) {
	stage := batchImageFixtureAt(t, "", AgentOpenCode)
	payload, err := os.ReadFile(stage.Old.Program())
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	receipt, err := stage.Old.InstallerReceipt(t.Context())
	if err != nil || receipt != hex.EncodeToString(digest[:])+"\n"+stage.Old.Program() {
		t.Fatal(receipt, err)
	}
	stage.Old.read = func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{State: applications.AgentEnrollmentStateNone}}, nil
	}
	if _, err := stage.Old.InstallerReceipt(t.Context()); err == nil {
		t.Fatal("revoked native custody produced physical receipt")
	}
	if err := stage.Old.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := stage.Old.InstallerReceipt(t.Context()); err == nil {
		t.Fatal("closed native custody produced physical receipt")
	}
}

func TestInstallerAgentImageUsesOwnedInstalledBytes(t *testing.T) {
	ctx := context.Background()
	root, sourceRoot := filepath.Join(t.TempDir(), "private"), filepath.Join(t.TempDir(), "private")
	for _, dir := range []string{root, sourceRoot} {
		if err := privatedir.Ensure(dir); err != nil {
			t.Fatal(err)
		}
	}
	sourcePath := filepath.Join(sourceRoot, "installed.exe")
	if err := os.WriteFile(sourcePath, []byte("installed signed component fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := copyAgentImage(ctx, sourcePath, sourceRoot, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	image, err := copyAgentImage(ctx, sourcePath, root, AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	if err := sameInstalledAgentImage(ctx, image, source); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte("installed signed component fixture"))
	if receipt, err := image.InstallerReceipt(ctx); err != nil || receipt != hex.EncodeToString(digest[:])+"\n"+image.Program() {
		t.Fatal(receipt, err)
	}
	if name, err := agentApplicationName(AgentOpenCode, filepath.Base(image.Program())); err != nil || name != "oa-opencode-integration" {
		t.Fatal(name, err)
	}
	if _, err := copyAgentImage(ctx, sourcePath, root, AgentOpenCode); err == nil {
		t.Fatal("existing held image overwritten")
	}
	image.Close()
	if err := sameInstalledAgentImage(ctx, image, source); err == nil {
		t.Fatal("closed custody accepted")
	}
	if err := os.WriteFile(filepath.Join(root, agentImageName(AgentOpenCode)), []byte("foreign component"), 0700); err != nil {
		t.Fatal(err)
	}
	foreign, err := holdAgentImageForReview(ctx, root, AgentOpenCode)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Close()
	if err := sameInstalledAgentImage(ctx, foreign, source); err == nil {
		t.Fatal("foreign image accepted")
	}
}

func TestInstallerAgentImageCreatesMissingPrivateDirectory(t *testing.T) {
	stage := batchImageFixtureAt(t, "", AgentClaude)
	root := filepath.Join(t.TempDir(), "setup", "agent-integrations")
	if err := privatedir.CheckDir(root); !errors.Is(err, privatedir.ErrNotPrivate) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing-directory CheckDir result = %v", err)
	}
	image, err := prepareInstallerAgentImage(t.Context(), root, AgentOpenCode, stage.Source)
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	if err := privatedir.CheckDir(root); err != nil {
		t.Fatal(err)
	}
	if err := sameInstalledAgentImage(t.Context(), image, stage.Source); err != nil {
		t.Fatal(err)
	}

	nonPrivate := filepath.Join(t.TempDir(), "agent-integrations")
	if err := os.Mkdir(nonPrivate, 0700); err != nil {
		t.Fatal(err)
	}
	if err := privatedir.CheckDir(nonPrivate); !errors.Is(err, privatedir.ErrNotPrivate) {
		t.Fatalf("fixture expected inherited ACL refusal: %v", err)
	}
	if image, err := prepareInstallerAgentImage(t.Context(), nonPrivate, AgentOpenCode, stage.Source); image != nil || !errors.Is(err, privatedir.ErrNotPrivate) {
		t.Fatalf("existing non-private directory: image=%v err=%v", image, err)
	}
	if err := privatedir.CheckDir(nonPrivate); !errors.Is(err, privatedir.ErrNotPrivate) {
		t.Fatalf("installer changed ACL on refusal: %v", err)
	}

	target := filepath.Join(t.TempDir(), "private-target")
	if err := privatedir.Ensure(target); err != nil {
		t.Fatal(err)
	}
	junction := filepath.Join(t.TempDir(), "agent-integrations")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	if image, err := prepareInstallerAgentImage(t.Context(), junction, AgentOpenCode, stage.Source); image != nil || !errors.Is(err, privatedir.ErrNotPrivate) {
		t.Fatalf("junction root: image=%v err=%v", image, err)
	}
	if _, err := os.Lstat(filepath.Join(target, agentImageName(AgentOpenCode))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("junction target changed: %v", err)
	}
	missing := filepath.Join(junction, "setup", "agent-integrations")
	if image, err := prepareInstallerAgentImage(t.Context(), missing, AgentOpenCode, stage.Source); image != nil || !errors.Is(err, privatedir.ErrNotPrivate) {
		t.Fatalf("missing destination behind junction: image=%v err=%v", image, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "setup")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created a parent through the junction: %v", err)
	}
}

func TestInstallerAgentImageRefusesIsolatedProfile(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	if image, err := PrepareInstallerAgentImage(context.Background(), AgentOpenCode); err == nil || image != nil {
		t.Fatal(image, err)
	}
}

package setup

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func darwinUpdateFixture(t *testing.T) (string, string, *AgentImage, func(context.Context, string) (applications.AgentEnrollmentResult, error)) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := privatedir.Tighten(root); err != nil {
		t.Fatal(err)
	}
	oldProgram := filepath.Join(root, agentImageName(AgentClaude))
	body := []byte("old native identity fixture")
	if actual := os.Getenv("OA_HELPER_UPDATE_DARWIN_OLD"); actual != "" {
		body, err = os.ReadFile(actual)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(oldProgram, body, 0700); err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := privatedir.Tighten(sourceRoot); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(sourceRoot, agentImageName(AgentClaude))
	if err := os.WriteFile(source, []byte("new compiled component fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	held, err := holdAgentImageForReview(context.Background(), sourceRoot, AgentClaude)
	if err != nil {
		t.Fatal(err)
	}
	read := func(_ context.Context, name string) (applications.AgentEnrollmentResult, error) {
		if name != "oa-claude-integration" {
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnknown}, nil
		}
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: name, Program: oldProgram, Revision: "kept-old", State: applications.AgentEnrollmentStateEnrolled}}, nil
	}
	return root, oldProgram, held, read
}

func TestDarwinVersionedHelperLeavesRunningOldImageUntouched(t *testing.T) {
	root, program, source, read := darwinUpdateFixture(t)
	defer source.Close()
	var child *exec.Cmd
	if os.Getenv("OA_HELPER_UPDATE_DARWIN_OLD") != "" {
		child = exec.Command(program)
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if child.ProcessState == nil {
				_ = child.Process.Kill()
				_ = child.Wait()
			}
		}()
	}
	before, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	old, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read)
	if err != nil {
		t.Fatal(err)
	}
	key := old.FileKey()
	image, changed, retained, err := updateVersionedAgentImage(context.Background(), old, source, AgentClaude, func(_ context.Context, p, k string) error {
		if p != program || k != key {
			return errors.New("foreign connection")
		}
		return nil
	})
	if err != nil || !changed || retained != program || image == nil {
		t.Fatal(changed, retained, err)
	}
	defer image.Close()
	if image.Program() == program || image.FileKey() == key || image.PreviousEnrollment().Revision != "kept-old" {
		t.Fatal("old identity reused", image.Program())
	}
	after, err := os.ReadFile(program)
	if err != nil || string(before) != string(after) {
		t.Fatal("old image changed", err)
	}
	if child != nil {
		if err := child.Process.Signal(syscall.Signal(0)); err != nil {
			t.Fatal("old running image was stopped", err)
		}
	}
	if application, err := agentApplicationName(AgentClaude, filepath.Base(image.Program())); err != nil || application == "oa-claude-integration" {
		t.Fatal(application, err)
	}
}

func TestDarwinVersionedHelperPreservesUnknownAndChangedParents(t *testing.T) {
	for _, mode := range []string{"unknown-version", "parent-changed", "source-changed"} {
		t.Run(mode, func(t *testing.T) {
			root, program, source, read := darwinUpdateFixture(t)
			defer source.Close()
			old, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read)
			if err != nil {
				t.Fatal(err)
			}
			pin, err := pinAgentImage(context.Background(), source)
			if err != nil {
				t.Fatal(err)
			}
			name := agentImageName(AgentClaude) + "-" + hex.EncodeToString(pin.Hash[:])
			target := filepath.Join(root, name)
			if mode == "unknown-version" {
				if err := os.WriteFile(target, []byte("unknown existing object"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			_, _, _, err = updateVersionedAgentImage(context.Background(), old, source, AgentClaude, func(context.Context, string, string) error {
				calls++
				if calls == 2 {
					if mode == "parent-changed" {
						if err := os.Rename(root, root+".old"); err != nil {
							t.Fatal(err)
						}
						if err := os.Mkdir(root, 0700); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "source-changed" {
						if err := os.WriteFile(source.Program(), []byte("changed installed component"), 0700); err != nil {
							t.Fatal(err)
						}
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("unknown or changed object accepted")
			}
			if mode == "unknown-version" {
				raw, err := os.ReadFile(target)
				if err != nil || string(raw) != "unknown existing object" {
					t.Fatal("unknown object changed", err)
				}
				if _, err := os.Stat(program); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestDarwinAgentImageCurrencyUsesSelectedVersion(t *testing.T) {
	root, program, source, read := darwinUpdateFixture(t)
	defer source.Close()
	old, err := holdEnrolledAgentImage(context.Background(), root, AgentClaude, read)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if got := agentImageCurrency(context.Background(), old, source, program, old.FileKey()); got != "update" {
		t.Fatal(got)
	}
	if got := agentImageCurrency(context.Background(), old, source, program+"-stale", old.FileKey()); got != "unknown" {
		t.Fatal(got)
	}
	if got := agentImageCurrency(context.Background(), old, source, program, "foreign-key"); got != "unknown" {
		t.Fatal(got)
	}
}

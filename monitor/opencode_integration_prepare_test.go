package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestOpenCodePreparationWatchdogPreservesResultsAndDiagnosesBlockedCall(t *testing.T) {
	for _, failure := range []error{nil, errors.New("fixture preparation refusal")} {
		var diagnostics bytes.Buffer
		err := preparationWatchdog(time.Second, &diagnostics, func(int) { t.Error("completed preparation triggered watchdog") }, func(context.Context) error { return failure })
		if !errors.Is(err, failure) || diagnostics.Len() != 0 {
			t.Fatal(err, diagnostics.String())
		}
	}
	var diagnostics bytes.Buffer
	exited := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		preparationWatchdog(20*time.Millisecond, &diagnostics, func(code int) { exited <- code }, func(context.Context) error {
			if code := <-exited; code != integrationPreparationDeadlineExceeded {
				t.Errorf("watchdog exit = %d", code)
			}
			return nil
		})
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watchdog did not terminate blocked preparation")
	}
	if !strings.Contains(diagnostics.String(), "preparation deadline exceeded") || !strings.Contains(diagnostics.String(), "goroutine") || diagnostics.Len() > (1<<20)+256 {
		t.Fatal("missing or unbounded watchdog stacks", diagnostics.Len())
	}
}

func TestOpenCodePreparationDiagnosticRefusalHasPrivateExitStatus(t *testing.T) {
	receipt := filepath.Join(t.TempDir(), "prepared-integration", "program.txt")
	output, err := preparationDiagnosticFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if code := integrationPreparationExitCode(runInstalledIntegrationPreparation(receipt)); code != integrationPreparationDiagnosticsFailed {
		t.Fatalf("preparation exit = %d", code)
	}
}

func TestOpenCodePreparationOwnsFixedPrivateBoundedDiagnostics(t *testing.T) {
	receipt := filepath.Join(t.TempDir(), "prepared-integration", "program.txt")
	file, err := preparationDiagnosticFile(receipt)
	if err != nil {
		t.Fatal(err)
	}
	writer := &cappedPreparationDiagnostics{writer: file.File(), remaining: 32}
	if n, err := writer.Write(bytes.Repeat([]byte("x"), 128)); err != nil || n != 128 {
		t.Fatal(n, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(filepath.Dir(receipt), "prepare.stderr"))
	if err != nil || len(body) != 32 {
		t.Fatal(len(body), err)
	}
	if replacement, err := preparationDiagnosticFile(receipt); err == nil {
		replacement.Close()
		t.Fatal("existing diagnostic file adopted")
	}
	if _, err := os.Stat(receipt); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("diagnostics granted preparation receipt", err)
	}
}

type preparedIntegrationFixture struct {
	program, key string
	err          error
}

func (i *preparedIntegrationFixture) Program() string             { return i.program }
func (i *preparedIntegrationFixture) FileKey() string             { return i.key }
func (i *preparedIntegrationFixture) Check(context.Context) error { return i.err }
func (i *preparedIntegrationFixture) Close() error                { return nil }
func (i *preparedIntegrationFixture) InstallerReceipt(context.Context) (string, error) {
	return "test-held-image-digest\n" + i.program, i.err
}

func TestOpenCodeConnectionUsesOneNativeKeepAndSeparatePhysicalPreparation(t *testing.T) {
	root := t.TempDir()
	image := &preparedIntegrationFixture{program: filepath.Join(root, "oa-opencode-integration.exe")}
	if err := os.WriteFile(image.program, []byte("inert"), 0600); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, image.program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image.key = id.Key()
	receipt := filepath.Join(root, "physical", "program.txt")
	if err := preparePermissionReceiptPath(receipt); err != nil {
		t.Fatal(err)
	}
	if err := writePhysicalIntegration(t.Context(), receipt, image); err != nil {
		t.Fatal(err)
	}
	entry := applications.AgentEnrollment{Application: "oa-opencode-integration", Program: image.program, Revision: "native-revision", State: applications.AgentEnrollmentStateNone}
	sets := 0
	native := claudeContextEnrollmentClient{read: func(context.Context, string) (applications.AgentEnrollmentResult, error) {
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &entry}, nil
	}, set: func(request applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
		sets++
		if request.Application != entry.Application || request.ExpectedRevision != entry.Revision || !request.Enabled {
			t.Fatal(request)
		}
		entry.State = applications.AgentEnrollmentStateEnrolled
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &entry}, nil
	}}
	for range 2 {
		state, err := registerPreparedAgentIntegration(t.Context(), installedConnection(), "", image, native,
			func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
				t.Fatal("registered existing image again")
				return fwire.ApplicationChange{}, nil
			},
			func(context.Context, claudePanelImage) (claudePanelImage, error) { return image, nil }, false)
		if err != nil || state != "enrolled" {
			t.Fatal(state, err)
		}
	}
	if sets != 1 {
		t.Fatal("connection prompted again after native Keep", sets)
	}
}

// This checks Panel projection and publication conditions. The service owner's
// OpenCode case produces the actual native Keep receipt through its Book answer.
func TestOpenCodeIntegrationPreparationRequiresNativeKeep(t *testing.T) {
	for _, kind := range []string{"kept", "pending", "refused", "conflict", "changed_image"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			image := &preparedIntegrationFixture{program: filepath.Join(root, "oa-opencode-integration.exe")}
			if err := os.WriteFile(image.program, []byte("inert image"), 0600); err != nil {
				t.Fatal(err)
			}
			id, err := fileid.Resolve(t.Context(), fileid.Self, image.program, fileid.Options{Links: fileid.RefuseLinks})
			if err != nil {
				t.Fatal(err)
			}
			image.key = id.Key()
			if kind == "changed_image" {
				image.err = errors.New("image changed")
			}
			receipt := filepath.Join(root, "receipt", "prepared.txt")
			if err := preparePermissionReceiptPath(receipt); err != nil {
				t.Fatal(err)
			}
			registered, sets := false, 0
			entry := applications.AgentEnrollment{Application: "oa-opencode-integration", Program: image.program, Revision: "seen", State: applications.AgentEnrollmentStateNone}
			native := claudeContextEnrollmentClient{read: func(_ context.Context, name string) (applications.AgentEnrollmentResult, error) {
				if name != entry.Application {
					t.Fatal(name)
				}
				if !registered {
					return applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnknown}, nil
				}
				return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &entry}, nil
			}, set: func(r applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
				sets++
				if r.Application != entry.Application || r.ExpectedRevision != entry.Revision || !r.Enabled {
					t.Fatal(r)
				}
				out := entry
				switch kind {
				case "kept":
					out.State = applications.AgentEnrollmentStateEnrolled
				case "pending":
					out.State = applications.AgentEnrollmentStatePending
				case "conflict":
					return applications.AgentEnrollmentResult{Outcome: applications.OutcomeConflict}, nil
				}
				return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &out}, nil
			}}
			register := func(_ context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
				if d.Name != entry.Application || d.Program != image.program || d.Activation != nil {
					t.Fatal(d)
				}
				registered = true
				return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
			}
			admissions := 0
			state, err := registerPreparedAgentIntegration(t.Context(), installedConnection(), receipt, image, native, register, func(context.Context, claudePanelImage) (claudePanelImage, error) { admissions++; return image, nil }, false)
			if kind == "kept" {
				raw, readErr := os.ReadFile(receipt)
				if err != nil || readErr != nil || state != "enrolled" || string(raw) != "test-held-image-digest\n"+image.program || sets != 1 || admissions != 1 {
					t.Fatal(state, err, readErr, string(raw), sets, admissions)
				}
				if err := writePreparedIntegration(receipt, "foreign"); err == nil {
					t.Fatal("existing handoff overwritten")
				}
			} else {
				if admissions != 0 {
					t.Fatal("unconfirmed Keep reached admission", admissions)
				}
				if _, readErr := os.Lstat(receipt); !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("unconfirmed Keep published MCP handoff", state, err, readErr)
				}
				if kind == "pending" && (state != "pending" || err != nil) {
					t.Fatal(state, err)
				}
				if kind == "changed_image" && (registered || sets != 0) {
					t.Fatal("changed custody reached mutation", registered, sets)
				}
			}
		})
	}
}

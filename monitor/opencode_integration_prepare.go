package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// Private installer/Panel process statuses. Keep in sync with
// preparation_failure_category in the installer custom action.
const (
	integrationPreparationDiagnosticsFailed = 71
	integrationPreparationImageFailed       = 72
	integrationPreparationReceiptFailed     = 73
	integrationPreparationDeadlineExceeded  = 74
)

type integrationPreparationFailure struct {
	code int
	err  error
}

func (e *integrationPreparationFailure) Error() string { return e.err.Error() }
func (e *integrationPreparationFailure) Unwrap() error { return e.err }

func integrationPreparationExitCode(err error) int {
	var failure *integrationPreparationFailure
	if errors.As(err, &failure) {
		return failure.code
	}
	return 1
}

// Installer preparation captures physical bytes only. Native enrollment is a
// separate first-run connection operation through the shared Panel owner.
func prepareInstalledIntegration(ctx context.Context, receipt string) (err error) {
	if err := preparePermissionReceiptPath(receipt); err != nil {
		return &integrationPreparationFailure{integrationPreparationReceiptFailed, err}
	}
	image, err := preparePhysicalIntegrationImage(ctx, installedConnection().party)
	if err != nil {
		return &integrationPreparationFailure{integrationPreparationImageFailed, err}
	}
	defer func() {
		if closeErr := image.Close(); closeErr != nil {
			err = errors.Join(err, &integrationPreparationFailure{integrationPreparationImageFailed, closeErr})
		}
	}()
	return writePhysicalIntegration(ctx, receipt, image)
}

func writePhysicalIntegration(ctx context.Context, receipt string, image installerIntegrationImage) error {
	if err := image.Check(ctx); err != nil {
		return &integrationPreparationFailure{integrationPreparationImageFailed, err}
	}
	text, err := image.InstallerReceipt(ctx)
	if err != nil {
		return &integrationPreparationFailure{integrationPreparationImageFailed, err}
	}
	if err := writePreparedIntegration(receipt, text); err != nil {
		return &integrationPreparationFailure{integrationPreparationReceiptFailed, err}
	}
	return nil
}

// Both images remain held while the exact result reaches the parent. Process
// launch or an untyped exit supplies no connection proof. A first use reads
// before it prepares: a stock host already connected stays connected after an
// OA upgrade and gets no question (owner R14, 2026-11-09). The Panel's Update
// button carries the installed connector forward.
func runInstalledAgentConnection(readOnly bool) (err error) {
	if !readOnly {
		if err := launchPresenter(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	owner, err := installedAgentConnectionOwnerFor(ctx, false)
	if err != nil {
		return err
	}
	view, err := owner.read(ctx)
	if readOnly || err == nil && installedAgentConnectionReady(view) {
		if err == nil {
			err = writeInstalledAgentConnectionResult(os.Stdout, view)
		}
		return errors.Join(err, owner.close())
	}
	if err := owner.close(); err != nil {
		return err
	}
	owner, err = installedAgentConnectionOwnerFor(ctx, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, owner.close()) }()
	view, err = owner.connect(ctx, "")
	if err != nil {
		return err
	}
	return writeInstalledAgentConnectionResult(os.Stdout, view)
}

// installedAgentConnectionReady reports a registered stock host whose held helper
// is the native-enrolled image. A helper the installed connector superseded
// ("update") or whose approved update waits for a restart stays the kept,
// enrolled image until publication and keeps working.
func installedAgentConnectionReady(view installedAgentConnectionView) bool {
	switch view.HelperState {
	case "current", "update", "restart":
	default:
		return false
	}
	return view.Outcome == "page" && view.Available && view.HostRegistration == "registered" &&
		view.HelperEnrollment == "enrolled" && view.HelperProgram != ""
}

// writeInstalledAgentConnectionResult reports connect_pending only while the
// person's native Keep question waits; any other unready state is
// not_connected, which sends the person to the Panel.
func writeInstalledAgentConnectionResult(output io.Writer, view installedAgentConnectionView) error {
	result := struct {
		Outcome        string `json:"outcome"`
		HostRegistered bool   `json:"hostRegistered"`
		HelperEnrolled bool   `json:"helperEnrolled"`
		HelperProgram  string `json:"helperProgram,omitempty"`
	}{Outcome: "not_connected"}
	switch {
	case installedAgentConnectionReady(view):
		result.Outcome, result.HostRegistered, result.HelperEnrolled = "connected", true, true
		result.HelperProgram = view.HelperProgram
	case view.Outcome == "connect_pending" || view.HelperEnrollment == "pending":
		result.Outcome = "connect_pending"
	}
	return json.NewEncoder(output).Encode(result)
}

type installerIntegrationImage interface {
	claudePanelImage
	InstallerReceipt(context.Context) (string, error)
}

func registerPreparedAgentIntegration(ctx context.Context, connection installedAgentConnection, receipt string, image installerIntegrationImage, native claudeEnrollmentClient, register func(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error), admit func(context.Context, claudePanelImage) (claudePanelImage, error), await bool) (state string, err error) {
	state, err = agentConnectionSetup(ctx, image, native, register,
		nativecli.AgentApplicationForParty(string(connection.party)), connection.title, await, nil)
	if err != nil || state != "enrolled" {
		return state, err
	}
	if admit == nil {
		return "unknown", errors.New("native integration admission unavailable")
	}
	kept, err := admit(ctx, image)
	if err != nil {
		return "unknown", err
	}
	defer func() { err = errors.Join(err, kept.Close()) }()
	if kept.FileKey() != image.FileKey() {
		return "unknown", errors.New("native integration image changed during Keep")
	}
	if err := kept.Check(ctx); err != nil {
		return "unknown", err
	}
	if err := image.Check(ctx); err != nil {
		return "unknown", err
	}
	if receipt == "" {
		return state, nil
	}
	text, err := image.InstallerReceipt(ctx)
	if err != nil {
		return "unknown", err
	}
	return state, writePreparedIntegration(receipt, text)
}

func writePreparedIntegration(path, program string) error {
	if err := validatePermissionReceiptPath(path); err != nil {
		return err
	}
	parent, err := fileid.Open(context.Background(), fileid.Self, filepath.Dir(path), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return err
	}
	defer parent.Close()
	output, err := fileid.CreateExclusive(parent, filepath.Base(path))
	if err != nil {
		return err
	}
	_, writeErr := output.File().WriteString(program)
	return errors.Join(writeErr, output.File().Sync(), output.Close())
}

func runInstalledIntegrationPreparation(receipt string) error {
	output, err := preparationDiagnosticFile(receipt)
	if err != nil {
		return &integrationPreparationFailure{integrationPreparationDiagnosticsFailed, err}
	}
	defer output.Close()
	bounded := &cappedPreparationDiagnostics{writer: output.File(), remaining: 2 << 20}
	diagnostics := io.MultiWriter(bounded, os.Stderr)
	return preparationWatchdog(90*time.Second, diagnostics, os.Exit, func(ctx context.Context) error {
		err := prepareInstalledIntegration(ctx, receipt)
		if err != nil {
			fmt.Fprintln(diagnostics, "OA integration preparation failed:", err)
		}
		return err
	})
}

func preparationDiagnosticFile(receipt string) (fileid.Handle, error) {
	if err := preparePermissionReceiptPath(receipt); err != nil {
		return fileid.Handle{}, err
	}
	parent, err := fileid.Open(context.Background(), fileid.Self, filepath.Dir(receipt), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return fileid.Handle{}, err
	}
	defer parent.Close()
	output, err := fileid.CreateExclusive(parent, "prepare.stderr")
	if err != nil {
		return fileid.Handle{}, fmt.Errorf("preparation diagnostics refused: %w", err)
	}
	return output, nil
}

type cappedPreparationDiagnostics struct {
	mu        sync.Mutex
	writer    io.Writer
	remaining int
}

func (w *cappedPreparationDiagnostics) Write(body []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	size := len(body)
	if len(body) > w.remaining {
		body = body[:w.remaining]
	}
	n, err := w.writer.Write(body)
	w.remaining -= n
	if err != nil {
		return n, err
	}
	return size, nil
}

// Native installed-package discovery can block inside an uncancellable Windows
// call. Retain bounded goroutine evidence before the installer kills its child.
func preparationWatchdog(limit time.Duration, diagnostics io.Writer, exit func(int), prepare func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()
	watchdog := time.AfterFunc(limit, func() {
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		fmt.Fprintln(diagnostics, "OA integration preparation deadline exceeded; goroutine stacks (maximum 1048576 bytes):")
		diagnostics.Write(stack[:n])
		exit(integrationPreparationDeadlineExceeded)
	})
	defer watchdog.Stop()
	return prepare(ctx)
}

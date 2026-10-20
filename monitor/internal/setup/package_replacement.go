package setup

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// PackageReplacement retains the installer and native Registry proofs for a
// kept integration. Its JSON fields are shared with existing Setup receipts.
type PackageReplacement struct {
	Previous        PreviousIntegration `json:"previous"`
	Candidate       string              `json:"candidate"`
	Before          string              `json:"before"`
	After           string              `json:"after,omitempty"`
	InstallComplete bool                `json:"install_complete,omitempty"`
	BeforeDispatch  bool                `json:"before_dispatch,omitempty"`
}

// PreviousIntegration binds the retained package and its installed connector.
type PreviousIntegration struct {
	Program       string `json:"program"`
	SHA256        string `json:"sha256"`
	Bytes         int64  `json:"bytes"`
	Package       string `json:"package"`
	PackageSHA256 string `json:"package_sha256"`
	ActivityAt    int64  `json:"activity_at,omitempty"`
}

// KeptPackageTransaction supplies the existing installer, Registry client and
// receipt owner to the shared coordinator. Save compare-and-swaps the last
// receipt, and Commit finalizes that same receipt after publication.
type KeptPackageTransaction struct {
	Program, PackageSHA256, ConnectorSHA256 string
	KeptPackageSHA256                       string
	ConnectorBytes                          int64
	Timeout                                 time.Duration
	ExistingReplacement                     *PackageReplacement
	Image                                   func() (string, int64, error)
	Keep                                    func(context.Context) (string, string, error)
	Ready                                   func() error
	Install                                 func(context.Context, func() error) (func(context.Context) error, error)
	Restore                                 func(context.Context, string) error
	Hold                                    func(context.Context) (HeldProgram, error)
	Observe                                 func(context.Context) (string, func(context.Context, string) (fwire.ApplicationChange, string, error), error)
	Expect                                  func(context.Context)
	Save                                    func(context.Context, *PackageReplacement) error
	MarkChanged                             func(context.Context)
	Publish                                 func(context.Context) error
	Commit                                  func(context.Context, PackageReplacement) error
}

// ForgetUntouchedReplacement releases a superseded receipt only while
// both predecessor images are held and the native revision agrees. It
// clears only the exact pending update observed by the caller.
func (s PackageMaintenanceStore) ForgetUntouchedReplacement(ctx context.Context, pending *PackageUpdateExpectation, program string, observeRevision func(context.Context) (string, error)) error {
	if pending == nil || pending.Replacement == nil || !pending.MatchesProgram(program) {
		return errors.New("the pending replacement has no matching target")
	}
	step := pending.Replacement
	prior := step.Previous
	if !step.BeforeDispatch || step.InstallComplete || step.After != "" || step.Candidate == "" ||
		step.Before == "" || prior.Program != pending.Program || !assetSHA.MatchString(prior.SHA256) || prior.Bytes <= 0 {
		return errors.New("the pending replacement has no complete pre-dispatch proof")
	}
	release, err := HoldSavedPackage(ctx, prior.Package, prior.PackageSHA256)
	if err != nil {
		return err
	}
	defer release()
	held, err := holdPinnedProgram(ctx, sevenZipPin{Path: program, Bytes: prior.Bytes, SHA256: prior.SHA256})
	if err != nil {
		return err
	}
	defer held.Close()
	revision, err := observeRevision(ctx)
	if err != nil {
		return err
	}
	if revision != step.Before {
		return errors.New("the native registration revision changed")
	}
	return s.Edit(ctx, func(r *PackageMaintenanceRecord) error {
		if err := r.CheckUpdate(pending); err != nil {
			return err
		}
		r.PendingUpdate = nil
		return nil
	})
}

// runKeptPackageTransaction completes or resumes a replacement using the
// exact acknowledged installer and conditional Registry revisions.
func runKeptPackageTransaction(parent context.Context, tx KeptPackageTransaction) error {
	ctx, cancel := context.WithTimeout(parent, tx.Timeout)
	defer cancel()
	image, size, err := tx.Image()
	if err != nil {
		return err
	}
	revision, replace, err := tx.Observe(ctx)
	if err != nil {
		return err
	}
	step := tx.ExistingReplacement
	if step == nil {
		path, digest, err := tx.Keep(ctx)
		if err != nil {
			return fmt.Errorf("the earlier integration cannot be kept to restore it: %w", err)
		}
		step = &PackageReplacement{Previous: PreviousIntegration{Program: tx.Program, SHA256: image,
			Bytes: size, Package: filepath.Clean(path), PackageSHA256: digest},
			Candidate: tx.PackageSHA256, Before: revision, BeforeDispatch: true}
	}
	if step.Candidate == "" || step.Candidate != tx.PackageSHA256 || step.Previous.Program != tx.Program ||
		revision != step.Before && revision != step.After {
		return errors.New("the pending package or registration changed")
	}
	prior := step.Previous
	release, err := HoldSavedPackage(ctx, prior.Package, prior.PackageSHA256)
	if err != nil {
		return fmt.Errorf("the earlier integration cannot be kept to restore it: %w", err)
	}
	defer func() {
		if err := release(); err != nil {
			log.Printf("setup: integration update: release the earlier package: %v", err)
		}
	}()
	// Legacy receipts lack a current MSI digest. A recorded digest also
	// binds the held predecessor to the package this integration kept.
	if tx.KeptPackageSHA256 != "" && tx.KeptPackageSHA256 != prior.PackageSHA256 {
		return errors.New("the saved predecessor differs from the kept package digest")
	}
	if tx.ExistingReplacement == nil {
		if err := tx.Save(ctx, step); err != nil {
			return err
		}
	}
	saveProof := func(step *PackageReplacement) error {
		// Saving an already acknowledged effect is local reconciliation.
		// Restore, replacement and publication still use the caller's lifetime.
		proof, cancel := context.WithTimeout(context.WithoutCancel(parent), tx.Timeout)
		defer cancel()
		return tx.Save(proof, step)
	}
	checkRevision := func(ctx context.Context, expected string) error {
		revision, _, err := tx.Observe(ctx)
		if err != nil {
			return err
		}
		if revision != expected {
			return errors.New("the registration revision changed during the integration update")
		}
		return nil
	}
	publish := tx.Publish
	if !step.InstallComplete {
		if !strings.EqualFold(image, prior.SHA256) || size != prior.Bytes || !step.BeforeDispatch || step.After != "" {
			return errors.New("the installer did not acknowledge this replacement")
		}
		if err := tx.Ready(); err != nil {
			return err
		}
		dispatched := *step
		dispatched.BeforeDispatch = false
		mayHaveChanged := false
		publish, err = tx.Install(ctx, func() error {
			if err := checkRevision(ctx, step.Before); err != nil {
				return err
			}
			if err := tx.Save(ctx, &dispatched); err != nil {
				return err
			}
			step, mayHaveChanged = &dispatched, true
			return nil
		})
		if err != nil {
			if mayHaveChanged {
				restoreCtx, cancel := context.WithTimeout(parent, tx.Timeout)
				defer cancel()
				if cancelErr := restoreCtx.Err(); cancelErr != nil {
					return errors.Join(err, cancelErr)
				}
				if check := checkRevision(restoreCtx, step.Before); check != nil {
					return errors.Join(err, fmt.Errorf("restore waits: %w", check))
				}
				if restoreErr := tx.Restore(restoreCtx, prior.Package); restoreErr != nil {
					return errors.Join(err, restoreErr)
				}
				if sum, _, readErr := tx.Image(); readErr != nil || !strings.EqualFold(sum, prior.SHA256) {
					return errors.Join(err, fmt.Errorf("the restored connector differs from the earlier one (%s): %v", sum, readErr))
				}
				if checkRevision(restoreCtx, step.Before) == nil {
					restored := *step
					restored.BeforeDispatch = true
					_ = tx.Save(restoreCtx, &restored)
				}
			}
			return err
		}
		if !mayHaveChanged {
			return errors.New("the installer returned success without acknowledging dispatch")
		}
		completed := *step
		completed.InstallComplete = true
		if err := saveProof(&completed); err != nil {
			return err
		}
		step = &completed
	}
	held, err := tx.Hold(ctx)
	if err != nil {
		digest, size, readErr := tx.Image()
		if readErr == nil && (!strings.EqualFold(digest, tx.ConnectorSHA256) || size != tx.ConnectorBytes) {
			tx.MarkChanged(ctx)
		}
		return fmt.Errorf("the connector differs from this build's: %w", err)
	}
	defer func() {
		if err := held.Close(); err != nil {
			log.Printf("setup: integration update: release the connector: %v", err)
		}
	}()
	if step.After == "" {
		tx.Expect(ctx)
		change, applied, err := replace(ctx, held.Path)
		if change.Outcome != fwire.ApplicationOutcomeApplied || applied == "" || applied == step.Before {
			return fmt.Errorf("the re-pin has no applied revision (%s %s): %v", change.Outcome.String(), change.Reason, err)
		}
		confirmed := *step
		confirmed.After = applied
		if errSave := saveProof(&confirmed); errSave != nil {
			return errSave
		}
		step = &confirmed
		if err != nil {
			return fmt.Errorf("the applied revision could not be read back: %w", err)
		}
	} else if revision != step.After {
		return errors.New("the acknowledged registration revision changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := publish(ctx); err != nil {
		return fmt.Errorf("the confirmed package could not be saved: %w", err)
	}
	return tx.Commit(ctx, *step)
}

// PackageUndoTransaction supplies the host operations and durable receipt
// owner. Save compares the supplied snapshot before recording any new proof.
type PackageUndoTransaction struct {
	Timeout        time.Duration
	HoldPrevious   func(context.Context, string, string) (func() error, func(context.Context) error, error)
	Observe        func(context.Context, string) (string, func(context.Context, string) (fwire.ApplicationChange, string, error), error)
	Image          func() (string, int64, error)
	LegacyDigest   func(string, int64) (string, error)
	KeepCurrent    func(context.Context) (string, string, error)
	ReadyToRestore func() error
	Install        func(context.Context, string, func() error) error
	HoldConnector  func(context.Context, string, int64) (HeldProgram, error)
	Save           func(context.Context, PackageMaintenanceRecord, *PackageUndoProof, bool) error
	ReleaseError   func(string, error)
}

// RestoreKeptPackage replays a kept workspace's exact Undo or the interrupted
// restoration of its original package. Installer and Registry effects are
// acknowledged independently before publication.
func RestoreKeptPackage(ctx context.Context, record PackageMaintenanceRecord, at int64, tx PackageUndoTransaction) error {
	if record.Previous == nil {
		return errors.New("there is no earlier integration to restore")
	}
	previous := *record.Previous
	record.Previous = &previous
	var publish func(context.Context) error
	if record.Undo == nil || !record.Undo.RestoreRequired {
		release, publishSaved, err := tx.HoldPrevious(ctx, previous.Package, previous.PackageSHA256)
		if err != nil {
			return err
		}
		defer func() {
			if err := release(); err != nil {
				tx.ReleaseError("the saved package", err)
			}
		}()
		publish = publishSaved
	}
	revision, replace, err := tx.Observe(ctx, previous.Program)
	if err != nil {
		return err
	}

	proof := record.Undo
	if proof == nil {
		image, size, err := tx.Image()
		if err != nil {
			return err
		}
		if record.Kept != image {
			return errors.New("the current connector differs from its saved registration")
		}
		expected := record.KeptPackageSHA256
		if expected == "" {
			// A legacy receipt can use the current release pin only if it
			// also names the exact connector currently kept.
			expected, err = tx.LegacyDigest(image, size)
			if err != nil {
				return err
			}
		}
		pkg, digest, err := tx.KeepCurrent(ctx)
		if err != nil {
			return err
		}
		if digest != expected {
			return errors.New("the saved-current package differs from its verified digest")
		}
		proof = &PackageUndoProof{At: at, Replacement: PackageReplacement{
			Previous:  PreviousIntegration{Program: previous.Program, SHA256: image, Bytes: size, Package: pkg, PackageSHA256: digest},
			Candidate: previous.PackageSHA256, Before: revision, BeforeDispatch: true,
		}}
	}
	step := proof.Replacement
	if step.Candidate != previous.PackageSHA256 || step.Previous.Program != previous.Program ||
		(revision != step.Before && revision != step.After) {
		return errors.New("the pending Undo package or registration changed")
	}
	keepCurrent, err := HoldSavedPackage(ctx, step.Previous.Package, step.Previous.PackageSHA256)
	if err != nil {
		return err
	}
	defer func() {
		if err := keepCurrent(); err != nil {
			tx.ReleaseError("the current package", err)
		}
	}()

	save := func(ctx context.Context, next *PackageUndoProof, complete bool) error {
		if err := tx.Save(ctx, record, next, complete); err != nil {
			return err
		}
		record.Undo = next
		return nil
	}
	checkRevision := func(ctx context.Context, expected string) error {
		revision, _, err := tx.Observe(ctx, previous.Program)
		if err != nil {
			return err
		}
		if revision != expected {
			return errors.New("the registration revision changed during the integration update")
		}
		return nil
	}
	restoreCurrent := func(ctx context.Context) error {
		current := step.Previous
		restore := *record.Undo
		if !restore.RestoreComplete {
			if restore.RestoreDispatched {
				return errors.New("the rollback installer did not acknowledge restoration")
			}
			if err := tx.ReadyToRestore(); err != nil {
				return err
			}
			if err := tx.Install(ctx, current.Package, func() error {
				if err := checkRevision(ctx, step.Before); err != nil {
					return err
				}
				restore.RestoreDispatched = true
				return save(ctx, &restore, false)
			}); err != nil {
				return err
			}
			if !restore.RestoreDispatched {
				return errors.New("the installer did not record rollback dispatch")
			}
			acknowledged := restore
			acknowledged.RestoreComplete = true
			proofCtx, cancelProof := context.WithTimeout(context.WithoutCancel(ctx), tx.Timeout)
			err := save(proofCtx, &acknowledged, false)
			cancelProof()
			if err != nil {
				return err
			}
		}
		held, err := tx.HoldConnector(ctx, current.SHA256, current.Bytes)
		if err != nil {
			return err
		}
		defer held.Close()
		if err := checkRevision(ctx, step.Before); err != nil {
			return err
		}
		return save(ctx, nil, false)
	}
	if proof.RestoreRequired {
		if step.BeforeDispatch || step.InstallComplete || step.After != "" || (proof.RestoreComplete && !proof.RestoreDispatched) {
			return errors.New("the saved-current restoration has inconsistent phase evidence")
		}
		return errors.Join(errors.New("the previous Undo failed; the current integration must be recovered first"), restoreCurrent(ctx))
	}
	if !step.InstallComplete {
		if !step.BeforeDispatch || revision != step.Before || step.After != "" {
			return errors.New("the installer did not acknowledge the pending Undo")
		}
		if record.Undo == nil {
			if err := save(ctx, proof, false); err != nil {
				return err
			}
		}
		dispatched := *proof
		dispatched.Replacement.BeforeDispatch = false
		dispatchedNow := false
		err := tx.Install(ctx, previous.Package, func() error {
			if err := checkRevision(ctx, step.Before); err != nil {
				return err
			}
			if err := save(ctx, &dispatched, false); err != nil {
				return err
			}
			dispatchedNow = true
			step = dispatched.Replacement
			return nil
		})
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), tx.Timeout)
		defer cancel()
		if err != nil {
			if !dispatchedNow {
				return errors.Join(err, save(recovery, nil, false))
			}
			failed := dispatched
			failed.RestoreRequired = true
			if saveErr := save(recovery, &failed, false); saveErr != nil {
				return errors.Join(err, saveErr)
			}
			return errors.Join(err, restoreCurrent(recovery))
		}
		if !dispatchedNow {
			return errors.New("the installer did not record Undo dispatch")
		}
		step.InstallComplete = true
		if err := save(recovery, &PackageUndoProof{At: at, Replacement: step}, false); err != nil {
			return err
		}
		// Native mutation may have taken a long time. The registration
		// request still uses the exact revision from before the MSI.
		revision, replace, err = tx.Observe(ctx, previous.Program)
		if err != nil {
			return err
		}
	}
	held, err := tx.HoldConnector(ctx, previous.SHA256, previous.Bytes)
	if err != nil {
		return err
	}
	defer func() {
		if err := held.Close(); err != nil {
			tx.ReleaseError("the connector", err)
		}
	}()
	if step.After == "" {
		if revision != step.Before {
			return errors.New("the kept registration changed before Undo")
		}
		change, applied, err := replace(ctx, held.Path)
		if change.Outcome != fwire.ApplicationOutcomeApplied || applied == "" || applied == step.Before {
			return errors.Join(errors.New("Undo has no acknowledged replacement revision"), err)
		}
		step.After = applied
		proofCtx, cancelProof := context.WithTimeout(context.WithoutCancel(ctx), tx.Timeout)
		saveErr := save(proofCtx, &PackageUndoProof{At: at, Replacement: step}, false)
		cancelProof()
		if saveErr != nil || err != nil {
			return errors.Join(saveErr, err)
		}
	} else if revision != step.After {
		return errors.New("the acknowledged Undo revision changed")
	}
	if err := publish(ctx); err != nil {
		return err
	}
	return save(ctx, nil, true)
}

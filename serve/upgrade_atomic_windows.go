package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

type exclusionAtomicSnapshot struct {
	id     fileid.FileID
	data   []byte
	record *upgradeExclusion
}

// One scope lock covers the load, sidecar and v1 record, and their release.
// Per-file writer locks remain separate so they can retain native collision
// handling without recursively acquiring this operation lock.
func lockUpgradeScope(ctx context.Context, path, scope string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if scope == "machine" {
		if err := secureMachineDirectories(dir); err != nil {
			return nil, err
		}
	} else if scope == "user" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("invalid upgrade lock scope")
	}
	return cas.LockContext(ctx, filepath.Join(dir, "admission"), false)
}

func decodeUpgradeStorage(path string, data []byte) (*upgradeExclusion, error) {
	if filepath.Base(path) != "chain.json" {
		return decodeExclusion(data)
	}
	chain, err := decodeUpgradeChain(data)
	if err != nil {
		return nil, err
	}
	// Only scope and holder are needed by the shared atomic writer. Chain
	// bytes confer no activation exclusion; exclusion.json remains the v1 owner.
	return &upgradeExclusion{Scope: chain.Scope, Installer: chain.Holder}, nil
}

// Private observation seam for controlled native collision tests. The command
// owner creates its own Background/deadline context and supplies no observer.
type exclusionAtomicRetryObserver struct{}

func exclusionAtomicTarget(path, scope string) (*exclusionAtomicSnapshot, error) {
	held, err := openExclusionRead(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer held.Close()
	info, err := held.File().Stat()
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0200 == 0 {
		return nil, fmt.Errorf("target is read-only: %w", windows.ERROR_ACCESS_DENIED)
	}
	sd, err := windows.GetSecurityInfo(windows.Handle(held.File().Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	if err := trustedExclusionOwner(owner, scope); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(held.File(), exclusionMaxBytes+1))
	if err != nil {
		return nil, err
	}
	record, err := decodeUpgradeStorage(path, data)
	if err != nil {
		return nil, err
	}
	if record.Scope != scope {
		return nil, errors.New("target record scope changed")
	}
	return &exclusionAtomicSnapshot{id: held.FileID(), data: data, record: record}, nil
}

func sameExclusionSnapshot(a, b *exclusionAtomicSnapshot) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.id.Equal(b.id) && bytes.Equal(a.data, b.data)
}

func replaceExclusionContext(ctx context.Context, root *os.Root, rootID fileid.FileID, path, stage, scope string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Callers without an operation deadline get one attempt, never an implicit
	// wait budget. The real Begin owner supplies its one acquire+stop deadline.
	if _, bounded := ctx.Deadline(); !bounded {
		return root.Rename(filepath.Base(stage), filepath.Base(path))
	}
	unlock, err := cas.LockContext(ctx, path, false)
	if err != nil {
		return fmt.Errorf("stage=atomic-lock: %w", err)
	}
	defer unlock()
	incoming, err := decodeUpgradeStorage(path, data)
	if err != nil {
		return fmt.Errorf("stage=atomic-input: %w", err)
	}
	if incoming.Scope != scope {
		return errors.New("stage=atomic-input: scope mismatch")
	}
	baseline, err := exclusionAtomicTarget(path, scope)
	if err != nil {
		return fmt.Errorf("stage=atomic-target: %w", err)
	}
	stageID, err := fileid.Resolve(ctx, fileid.Self, stage, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return fmt.Errorf("stage=atomic-staged-identity: %w", err)
	}
	var collision error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(collision, err)
		}
		currentRoot, err := fileid.Resolve(ctx, fileid.Self, filepath.Dir(path), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return errors.Join(collision, fmt.Errorf("stage=atomic-root: %w", err))
		}
		if !currentRoot.Equal(rootID) {
			return errors.Join(collision, errors.New("stage=atomic-root: replacement root changed"))
		}
		current, err := exclusionAtomicTarget(path, scope)
		if err != nil {
			return errors.Join(collision, fmt.Errorf("stage=atomic-target: %w", err))
		}
		if !sameExclusionSnapshot(current, baseline) {
			return errors.Join(collision, errors.New("stage=atomic-target: record identity or bytes changed while acquiring exclusion"))
		}
		if filepath.Base(path) != "chain.json" && !processAlive(incoming.Installer, incoming.Begun, time.Now()) {
			return errors.Join(collision, errors.New("stage=atomic-holder: incoming installer occurrence ended"))
		}
		if filepath.Base(path) != "chain.json" && current != nil && current.record.Installer != incoming.Installer && processAlive(current.record.Installer, current.record.Begun, time.Now()) {
			return errors.Join(collision, fmt.Errorf("stage=atomic-holder: installer pid %d already holds this exclusion", current.record.Installer.PID))
		}
		held, err := openExclusionRead(stage)
		if err != nil {
			return errors.Join(collision, fmt.Errorf("stage=atomic-staged-read: %w", err))
		}
		actual, readErr := io.ReadAll(io.LimitReader(held.File(), exclusionMaxBytes+1))
		sameStage := held.FileID().Equal(stageID)
		closeErr := held.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(collision, readErr, closeErr)
		}
		if !sameStage || !bytes.Equal(actual, data) {
			return errors.Join(collision, errors.New("stage=atomic-staged-read: staged identity or bytes changed"))
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(collision, err)
		}
		err = root.Rename(filepath.Base(stage), filepath.Base(path))
		if err == nil {
			return nil
		}
		if !errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return err
		}
		// Recheck the exact target before another attempt. Access denied is
		// ambiguous, not a claim that sharing caused it. Windows enforces the
		// unchanged security on every attempt; no attribute/ACL is overridden.
		current, targetErr := exclusionAtomicTarget(path, scope)
		if targetErr != nil {
			return errors.Join(err, fmt.Errorf("stage=collision-target: %w", targetErr))
		}
		if current == nil || !sameExclusionSnapshot(current, baseline) {
			return errors.Join(err, errors.New("stage=collision-target: target changed or disappeared"))
		}
		outcome := "ambiguous-access-denied"
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			outcome = "sharing-violation"
		}
		collision = fmt.Errorf("stage=atomic-replace-pending outcome=%s path=%q: %w", outcome, path, err)
		if observe, ok := ctx.Value(exclusionAtomicRetryObserver{}).(func()); ok {
			observe()
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(collision, ctx.Err())
		case <-timer.C:
		}
	}
}

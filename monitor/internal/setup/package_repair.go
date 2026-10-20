package setup

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/openabstractions/abstraction-storage/go/digest"
)

// samePackageImageDigest compares the bare SHA-256 fields in a repair receipt.
func samePackageImageDigest(a, b string) bool {
	if len(a) != sha256.Size*2 || len(b) != sha256.Size*2 {
		return false
	}
	normalized := digest.Normalize(a)
	return normalized != "" && normalized == digest.Normalize(b)
}

// ExplicitPackageUpdate supplies the compiled pin and native observations for
// an explicitly requested Connect, Update or Repair. Setup decides whether
// the kept replacement applies. The caller owns first-time Connect.
type ExplicitPackageUpdate struct {
	Application, Program, Pin string
	Connected                 bool
	Available                 func() bool
	Outdated                  func() (bool, error)
	Image                     func() (string, int64, error)
	EntryKept                 func(context.Context) (kept, known bool)
	RegistrationAbsent        func(context.Context) bool
	CheckMissingCompanion     func(context.Context, string, int64) error
	Stamp                     func() (string, int64)
}

// PlanExplicitUpdate admits a new or resumable kept-package operation.
// A nil expectation without an error leaves the request to ordinary Connect.
func (r PackageMaintenanceRecord) PlanExplicitUpdate(ctx context.Context, request ExplicitPackageUpdate) (pending *PackageUpdateExpectation, resume bool, err error) {
	if r.Connection != nil {
		return nil, false, nil
	}
	if pending := r.PendingUpdate; pending != nil {
		if pending.Application != "" && pending.Application != request.Application {
			return nil, false, errors.New("a different integration owns the pending replacement")
		}
		if pending.Replacement != nil {
			if r.Changed || pending.SHA256 != request.Pin || !pending.MatchesProgram(request.Program) {
				return nil, false, errors.New("a different or changed integration replacement is pending")
			}
			return pending, true, nil
		}
	}
	if request.Pin == "" || !request.Available() {
		return nil, false, nil
	}
	if request.Connected {
		outdated, err := request.Outdated()
		if err != nil || !outdated {
			return nil, false, err
		}
	} else {
		digest, size, err := request.Image()
		if errors.Is(err, os.ErrNotExist) {
			kept, known := request.EntryKept(ctx)
			if kept || !known && r.Kept != "" {
				return nil, false, errors.New("the kept connector is missing; the integration cannot be repaired safely")
			}
			return nil, false, nil
		}
		if err != nil {
			return nil, false, err
		}
		if r.Kept == "" && request.RegistrationAbsent(ctx) {
			return nil, false, nil
		}
		if samePackageImageDigest(digest, request.Pin) {
			return nil, false, errors.New("the kept registration's exact connector image cannot be verified for repair")
		}
		if !samePackageImageDigest(r.Kept, digest) || r.KeptPackageSHA256 == "" {
			return nil, false, errors.New("the earlier connector and package were not recorded for repair")
		}
		if err := request.CheckMissingCompanion(ctx, r.Kept, size); err != nil {
			return nil, false, err
		}
	}
	kept, known := request.EntryKept(ctx)
	if !known {
		return nil, false, errors.New("the current integration registration could not be read")
	}
	if !kept {
		if !request.Connected {
			return nil, false, errors.New("the earlier connector is not the kept registration for this installation")
		}
		return nil, false, nil
	}
	version, at := request.Stamp()
	return &PackageUpdateExpectation{Application: request.Application, Program: request.Program, SHA256: request.Pin,
		Version: version, At: at}, false, nil
}

// AdmitExplicitUpdate saves the first replacement proof against the receipt
// observed at planning. A repair binds the same retained connector digest.
func (r *PackageMaintenanceRecord) AdmitExplicitUpdate(before PackageMaintenanceRecord, pending *PackageUpdateExpectation, repair bool) error {
	if !reflect.DeepEqual(*r, before) || r.Undo != nil || r.IsDowngrade(pending.SHA256) {
		return errors.New("another integration change owns the replacement")
	}
	if repair && (pending.Replacement == nil || !samePackageImageDigest(pending.Replacement.Previous.SHA256, before.Kept)) {
		return errors.New("the earlier connector changed before repair")
	}
	r.PendingUpdate, r.Changed = pending, false
	return nil
}

// CheckMissingPackageCompanion requires an absent companion and an intact,
// verified connector at an exact installation locator.
func CheckMissingPackageCompanion(ctx context.Context, base, companion string, hold func(context.Context) (HeldProgram, error)) error {
	if !filepath.IsAbs(base) {
		return errors.New("the repair installation has no exact locator")
	}
	if _, err := os.Lstat(companion); !errors.Is(err, os.ErrNotExist) {
		return errors.New("the integration companion is not confirmed missing")
	}
	held, err := hold(ctx)
	if err != nil {
		return err
	}
	return held.Close()
}

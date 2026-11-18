package main

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/placetest"
	"golang.org/x/sys/windows"
)

// Use only an owned temporary namespace. The existing exclusion fixture owns
// source/installer trust; storage, CAS locks, record read/removal and finalizer
// are the production implementations. No installed folder or machine ACL is used.
func removalScopeFixture(t *testing.T) (*testExclusion, string, string, processIdentity) {
	t.Helper()
	x := newTestExclusion(t)
	path := filepath.Join(x.dir, "abstraction", "upgrade-v1", "exclusion.json")
	x.env.path = func(string) (string, error) { return path, nil }
	x.env.lock = func(ctx context.Context, path, _ string) (func(), error) {
		return cas.LockContext(ctx, filepath.Join(filepath.Dir(path), "admission"), false)
	}
	x.env.read = readExclusionFile
	x.env.remove = removeExclusionFile
	x.env.retire = func(ctx context.Context, path, scope string) error {
		return retireUpgradeAdmissionWithOwner(ctx, path, scope, func(owner *windows.SID, _ string) error {
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				return err
			}
			if owner == nil || !owner.Equals(user.User.Sid) {
				return errors.New("temporary scope owner differs")
			}
			return nil
		})
	}
	installed := filepath.Join(x.dir, "installation")
	if err := os.MkdirAll(installed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installed, "keep"), []byte("caller data"), 0600); err != nil {
		t.Fatal(err)
	}
	holder := holderFor(7)
	x.alive[holder.PID] = true
	return x, path, installed, holder
}

func TestPresentRemovalFinalizerRetiresScopeScratch(t *testing.T) {
	x, path, installed, holder := removalScopeFixture(t)
	if err := x.env.beginContext(t.Context(), "machine", []string{installed}, holder); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(filepath.Dir(path), "admission.lock")
	// The removal script deletes the sidecar before COMMIT; its live exclusion
	// record still belongs to the installer and must survive until finalization.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if !fileExists(path) {
		t.Fatal("MSI simulation removed the live exclusion")
	}
	notes, _, err := settleUpgradeAsideOperation(x.env, "machine", false, holder, true)
	if err != nil {
		t.Fatal(err)
	}
	// serviceEndUpgrade reports these exact notes after settling. Success must
	// stay silent or the existing reporter recreates installer-actions.txt.
	// Assert no notes before calling that owner: this control never requests a
	// write to the real machine action-log path.
	if len(notes) != 0 {
		t.Fatalf("successful retirement emitted storage-writing notes: %v", notes)
	}
	reportUpgradeAside(io.Discard, "machine", "service end-upgrade --machine", notes)
	if fileExists(path) {
		t.Fatal("finalizer kept the completed exclusion")
	}
	if _, err := os.Stat(filepath.Join(x.dir, "abstraction")); !os.IsNotExist(err) {
		t.Fatalf("present-record finalizer left scope scratch after MSI removal: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(installed, "keep"))
	if err != nil || string(data) != "caller data" {
		t.Fatal("caller folder changed", string(data), err)
	}
}

func TestAbsentRemovalFinalizerDoesNotCreateScopeScratch(t *testing.T) {
	x, path, _, holder := removalScopeFixture(t)
	locks := 0
	x.env.lock = func(context.Context, string, string) (func(), error) {
		locks++
		return func() {}, nil
	}
	notes, _, err := settleUpgradeAsideOperation(x.env, "machine", false, holder, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("absent-record success emitted storage-writing notes: %v", notes)
	}
	reportUpgradeAside(io.Discard, "machine", "service end-upgrade --machine", notes)
	if locks != 0 || fileExists(filepath.Dir(path)) {
		t.Fatal("absent record created storage or took a lock", locks)
	}
}

func TestRemovalRetirementPreservesLiveCASHolderAndNativeWaiter(t *testing.T) {
	for _, waiter := range []bool{false, true} {
		t.Run(map[bool]string{false: "holder", true: "native-waiter"}[waiter], func(t *testing.T) {
			x, _, installed, holder := removalScopeFixture(t)
			if err := x.env.beginContext(t.Context(), "machine", []string{installed}, holder); err != nil {
				t.Fatal(err)
			}
			retire := x.env.retire
			x.env.retire = func(ctx context.Context, path, scope string) error {
				lock := filepath.Join(filepath.Dir(path), "admission.lock")
				key, err := fileid.FileKey(lock)
				if err != nil {
					t.Fatal(err)
				}
				unlock, err := cas.LockContext(ctx, filepath.Join(filepath.Dir(path), "admission"), false)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if unlock != nil {
						unlock()
					}
				}()
				var waiting *os.File
				if waiter {
					// The actual CAS holder hands its byte-range lock to an open
					// native waiter using precisely CAS's Windows lock protocol.
					// This descriptor is held before the old holder releases.
					waiting, err = os.OpenFile(lock, os.O_RDWR, 0600)
					if err != nil {
						t.Fatal(err)
					}
					defer waiting.Close()
					acquired := make(chan error, 1)
					go func() {
						var ov windows.Overlapped
						acquired <- windows.LockFileEx(windows.Handle(waiting.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ov)
					}()
					unlock()
					unlock = nil
					select {
					case err := <-acquired:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				err = retire(ctx, path, scope)
				if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
					t.Fatalf("live lock descriptor not refused: %v", err)
				}
				actual, keyErr := fileid.FileKey(lock)
				if keyErr != nil || actual != key {
					t.Fatal("claimant's lock identity changed", actual, keyErr)
				}
				return err
			}
			if _, _, err := settleUpgradeAsideOperation(x.env, "machine", false, holder, true); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				t.Fatal(err)
			}
		})
	}
}

func TestRemovalRetirementRefusesSuccessorAndFailedSettlement(t *testing.T) {
	for _, mode := range []string{"record", "chain", "joined", "wrong-installer", "rollback-error"} {
		t.Run(mode, func(t *testing.T) {
			x, path, installed, holder := removalScopeFixture(t)
			if err := x.env.beginContext(t.Context(), "machine", []string{installed}, holder); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(filepath.Dir(path), "admission.lock")
			key, err := fileid.FileKey(lock)
			if err != nil {
				t.Fatal(err)
			}
			retire := x.env.retire
			retired := false
			var successor string
			x.env.retire = func(ctx context.Context, path, scope string) error {
				retired = true
				switch mode {
				case "record":
					if err := x.env.beginContext(ctx, "machine", []string{installed}, holderFor(8)); err != nil {
						t.Fatal(err)
					}
					successor = path
				case "chain":
					successor = chainPath(path)
				case "joined":
					successor = joinedUpgradePath(path)
				}
				if mode != "record" {
					if err := os.WriteFile(successor, []byte("successor"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return retire(ctx, path, scope)
			}
			caller := holder
			if mode == "wrong-installer" {
				caller = holderFor(9)
			}
			if mode == "rollback-error" {
				x.env.remove = func(string) error { return errors.New("controlled removal failure") }
			}
			_, _, err = settleUpgradeAsideOperation(x.env, "machine", mode == "rollback-error", caller, true)
			if err == nil {
				t.Fatal("unsafe settlement accepted")
			}
			if mode == "wrong-installer" || mode == "rollback-error" {
				if retired || !fileExists(path) {
					t.Fatal("failed settlement retired scope or removed record")
				}
			} else if !retired || !fileExists(successor) {
				t.Fatal("successor scope was lost")
			}
			actual, keyErr := fileid.FileKey(lock)
			if keyErr != nil || actual != key {
				t.Fatal("scope lock identity changed", actual, keyErr)
			}
		})
	}
}

func TestRemovalRetirementRefusesUnsafeSidecarsAndPreservesOtherData(t *testing.T) {
	for _, mode := range []string{"content", "readonly", "hardlink", "reparse", "untrusted", "other-data", "cancelled", "wrong-path"} {
		t.Run(mode, func(t *testing.T) {
			x, path, _, _ := removalScopeFixture(t)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(filepath.Dir(path), "admission.lock")
			if err := os.WriteFile(lock, nil, 0600); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(x.dir, "outside")
			if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch mode {
			case "content":
				if err := os.WriteFile(lock, []byte("not a lock"), 0600); err != nil {
					t.Fatal(err)
				}
			case "readonly":
				name, err := windows.UTF16PtrFromString(lock)
				if err != nil {
					t.Fatal(err)
				}
				attributes, err := windows.GetFileAttributes(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetFileAttributes(name, attributes|windows.FILE_ATTRIBUTE_READONLY); err != nil {
					t.Fatal(err)
				}
				defer windows.SetFileAttributes(name, attributes) // Restore this authored temporary fixture only.
			case "hardlink":
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(outside, lock); err != nil {
					t.Fatal(err)
				}
			case "reparse":
				if err := os.Remove(lock); err != nil {
					t.Fatal(err)
				}
				if err := placetest.Junction(lock, filepath.Dir(outside)); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(lock)
			case "other-data":
				if err := os.WriteFile(filepath.Join(filepath.Dir(path), "keep"), []byte("data"), 0600); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			retire := x.env.retire
			if mode == "untrusted" {
				retire = retireUpgradeAdmission
			}
			if mode == "wrong-path" {
				path = filepath.Join(filepath.Dir(path), "different.json")
			}
			err := retire(ctx, path, "machine")
			if mode == "other-data" {
				if err != nil || fileExists(lock) || !fileExists(filepath.Join(filepath.Dir(path), "keep")) {
					t.Fatal(err, "other scratch data changed")
				}
			} else if err == nil {
				t.Fatal("unsafe sidecar accepted")
			}
			if mode == "readonly" {
				name, _ := windows.UTF16PtrFromString(lock)
				attributes, err := windows.GetFileAttributes(name)
				if err != nil || attributes&windows.FILE_ATTRIBUTE_READONLY == 0 {
					t.Fatal("readonly refusal changed attributes", err)
				}
			}
			data, readErr := os.ReadFile(outside)
			if readErr != nil || string(data) != "outside" {
				t.Fatal("outside original changed", readErr)
			}
		})
	}
}

func TestAdmissionRetirementFencesNewOpenAndAllowsNextClaim(t *testing.T) {
	x, _, installed, holder := removalScopeFixture(t)
	if err := x.env.beginContext(t.Context(), "machine", []string{installed}, holder); err != nil {
		t.Fatal(err)
	}
	x.env.retire = func(ctx context.Context, path, scope string) error {
		return retireUpgradeAdmissionWithOwner(ctx, path, scope, func(owner *windows.SID, _ string) error {
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				return err
			}
			if owner == nil || !owner.Equals(user.User.Sid) {
				return errors.New("temporary owner differs")
			}
			unlock, err := cas.LockContext(ctx, filepath.Join(filepath.Dir(path), "admission"), false)
			if unlock != nil {
				unlock()
			}
			if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
				t.Fatalf("new claimant crossed held exclusive deletion: %v", err)
			}
			return nil
		})
	}
	if _, _, err := settleUpgradeAsideOperation(x.env, "machine", false, holder, true); err != nil {
		t.Fatal(err)
	}
	next := holderFor(8)
	if err := x.env.beginContext(t.Context(), "machine", []string{installed}, next); err != nil {
		t.Fatal(err)
	}
	record, _, err := x.env.load("machine")
	if err != nil || record == nil || record.Installer != next {
		t.Fatal("next valid claimant lost its scope", record, err)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func upgradeAtomicFixture(t *testing.T) (string, []byte, []byte) {
	t.Helper()
	created, err := handleCreated(windows.CurrentProcess())
	if err != nil {
		t.Fatal(err)
	}
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record := upgradeExclusion{Version: 1, Scope: "user", Folders: []string{testFolder}, Installer: processIdentity{PID: uint32(os.Getpid()), Created: created, Image: image}, Begun: time.Now().Add(-time.Second).UTC()}
	old, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	record.Begun = record.Begun.Add(time.Millisecond)
	new, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "upgrade-v1", "exclusion.json")
	if err := writeExclusionFile(path, "user", old); err != nil {
		t.Fatal(err)
	}
	return path, old, new
}

func TestUpgradeChainUsesTrustedAtomicWriterBeforeV1Exclusion(t *testing.T) {
	path, old, _ := upgradeAtomicFixture(t)
	var record upgradeExclusion
	if err := json.Unmarshal(old, &record); err != nil {
		t.Fatal(err)
	}
	chain := upgradeChain{Version: 1, Scope: "user", Token: strings.Repeat("a", 32), Holder: record.Installer}
	data, _ := json.Marshal(chain)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := writeExclusionFileContext(ctx, chainPath(path), "user", data); err != nil {
		t.Fatal(err)
	}
	if fileExists(path) {
		t.Fatal("chain write created an exclusion")
	}
	if err := writeExclusionFileContext(ctx, path, "user", old); err != nil {
		t.Fatal(err)
	}
	env := systemExclusionEnv()
	env.path = func(string) (string, error) { return path, nil }
	if !env.matchingChain(path, "user", chain.Token, record.Installer) {
		t.Fatal("trusted chain not readable")
	}
	if _, err := decodeExclusion(old); err != nil {
		t.Fatal("v1 format changed", err)
	}
	if _, err := decodeExclusion(data); err == nil {
		t.Fatal("sidecar accepted as v1 exclusion")
	}
	if notes := env.release("user"); len(notes) != 0 {
		t.Fatal(notes)
	}
	if fileExists(path) || fileExists(chainPath(path)) {
		t.Fatal("rollback retained chain or exclusion")
	}
}

func waitUpgradeAtomicStage(t *testing.T, path string) (string, string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		stages, err := filepath.Glob(filepath.Join(filepath.Dir(path), "exclusion-*.tmp"))
		if err != nil {
			t.Fatal(err)
		}
		if len(stages) == 1 {
			data, err := os.ReadFile(stages[0])
			if err == nil && len(data) > 0 {
				key, err := fileid.FileKey(stages[0])
				if err == nil {
					return stages[0], key
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("bounded atomic writer did not expose its one staged file")
	return "", ""
}

func TestUpgradeAtomicLegacySharingCommitsExactStagedFileAfterReaderCloses(t *testing.T) {
	path, old, new := upgradeAtomicFixture(t)
	legacy, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	defer legacy.Close()
	collided := make(chan struct{}, 1)
	ctx = context.WithValue(ctx, exclusionAtomicRetryObserver{}, func() {
		select {
		case collided <- struct{}{}:
		default:
		}
	})
	done := make(chan error, 1)
	go func() { done <- writeExclusionFileContext(ctx, path, "user", new) }()
	stage, key := waitUpgradeAtomicStage(t, path)
	select {
	case <-collided:
	case <-ctx.Done():
		t.Fatal("native collision not observed", ctx.Err())
	}
	before, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, old) {
		t.Fatal(string(before), err)
	}
	legacy.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	after, err := readExclusionFile(path)
	if err != nil || !bytes.Equal(after, new) {
		t.Fatal(string(after), err)
	}
	committed, err := fileid.FileKey(path)
	if err != nil || committed != key {
		t.Fatal("retry recreated rather than committed the same stage", committed, key, err)
	}
	if _, err := os.Stat(stage); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stage still present", err)
	}
}

func TestUpgradeAtomicLegacySharingDeadlineCancelAndChangedRecord(t *testing.T) {
	for _, mode := range []string{"deadline", "cancel", "changed"} {
		t.Run(mode, func(t *testing.T) {
			path, old, new := upgradeAtomicFixture(t)
			legacy, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer legacy.Close()
			budget := 3 * time.Second
			if mode == "deadline" {
				budget = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			collided := make(chan struct{}, 1)
			ctx = context.WithValue(ctx, exclusionAtomicRetryObserver{}, func() {
				select {
				case collided <- struct{}{}:
				default:
				}
			})
			start := time.Now()
			done := make(chan error, 1)
			go func() { done <- writeExclusionFileContext(ctx, path, "user", new) }()
			if mode != "deadline" {
				waitUpgradeAtomicStage(t, path)
				select {
				case <-collided:
				case <-ctx.Done():
					t.Fatal("native collision not observed", ctx.Err())
				}
				if mode == "cancel" {
					cancel()
				} else {
					var record upgradeExclusion
					if err := json.Unmarshal(old, &record); err != nil {
						t.Fatal(err)
					}
					record.Begun = record.Begun.Add(time.Hour)
					old, _ = json.Marshal(record)
					if err := os.WriteFile(path, old, 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			err = <-done
			if err == nil {
				t.Fatal("collision accepted", mode)
			}
			if mode == "deadline" && (!errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, windows.ERROR_ACCESS_DENIED) || time.Since(start) > 2*time.Second) {
				t.Fatal(err, time.Since(start))
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if mode == "changed" && !strings.Contains(err.Error(), "stage=atomic-target") && !strings.Contains(err.Error(), "stage=collision-target") {
				t.Fatal(err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, old) {
				t.Fatal("refused write changed target", string(after), readErr)
			}
			if stages, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "exclusion-*.tmp")); len(stages) != 0 {
				t.Fatal("refused stage leaked", stages)
			}
		})
	}
}

func TestUpgradeAtomicDeniedACLIsBoundedAndOtherRefusalsImmediate(t *testing.T) {
	security := func(path string) string {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		return sd.String()
	}
	for _, mode := range []string{"readonly", "acl", "holder"} {
		t.Run(mode, func(t *testing.T) {
			path, old, new := upgradeAtomicFixture(t)
			if mode == "holder" {
				var record upgradeExclusion
				json.Unmarshal(old, &record)
				record.Installer.Image += "-other-claim"
				old, _ = json.Marshal(record)
				if err := os.WriteFile(path, old, 0600); err != nil {
					t.Fatal(err)
				}
			}
			legacy, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer legacy.Close()
			if mode == "readonly" {
				if err := os.Chmod(path, 0400); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0600)
			}
			if mode == "acl" {
				original, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
				if err != nil {
					t.Fatal(err)
				}
				originalACL, _, err := original.DACL()
				if err != nil {
					t.Fatal(err)
				}
				defer windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, originalACL, nil)
				user, err := windows.GetCurrentProcessToken().GetTokenUser()
				if err != nil {
					t.Fatal(err)
				}
				sid := user.User.Sid.String()
				sd, err := windows.SecurityDescriptorFromString("D:P(D;;SD;;;" + sid + ")(A;;FA;;;" + sid + ")")
				if err != nil {
					t.Fatal(err)
				}
				acl, _, err := sd.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil); err != nil {
					t.Fatal(err)
				}
				parent := filepath.Dir(path)
				originalParent, err := windows.GetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
				if err != nil {
					t.Fatal(err)
				}
				parentACL, _, err := originalParent.DACL()
				if err != nil {
					t.Fatal(err)
				}
				defer windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, parentACL, nil)
				parentSD, err := windows.SecurityDescriptorFromString("D:P(D;;0x40;;;" + sid + ")(A;OICI;FA;;;" + sid + ")")
				if err != nil {
					t.Fatal(err)
				}
				deniedParent, _, err := parentSD.DACL()
				if err != nil {
					t.Fatal(err)
				}
				if err := windows.SetNamedSecurityInfo(parent, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, deniedParent, nil); err != nil {
					t.Fatal(err)
				}

			}
			beforeSecurity, beforeParent := security(path), security(filepath.Dir(path))
			budget := 2 * time.Second
			if mode == "acl" {
				budget = 500 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			start := time.Now()
			err = writeExclusionFileContext(ctx, path, "user", new)
			if mode == "acl" {
				if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, windows.ERROR_ACCESS_DENIED) || time.Since(start) > 2*time.Second {
					t.Fatal("denied ACL was bypassed or unbounded", err, time.Since(start))
				}
			} else if err == nil || errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatal("explicit refusal did not stop immediately", mode, err, time.Since(start))
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, old) {
				t.Fatal(string(after), readErr)
			}
			if security(path) != beforeSecurity || security(filepath.Dir(path)) != beforeParent {
				t.Fatal("failed operation changed security")
			}
			if mode == "readonly" {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm()&0200 != 0 {
					t.Fatal("read-only flag was cleared", err)
				}
			}
		})
	}
}

func TestUpgradeAdmissionPrecedesExclusionWriteAndSharesStopDeadline(t *testing.T) {
	x := newTestExclusion(t)
	var events []string
	var deadline time.Time
	x.env.writeContext = func(ctx context.Context, _ string, _ string, _ []byte) error {
		events = append(events, "write")
		got, _ := ctx.Deadline()
		if !got.Equal(deadline) {
			t.Fatal(got, deadline)
		}
		return nil
	}
	admit := func(ctx context.Context, _ []string) error {
		events = append(events, "admit")
		deadline, _ = ctx.Deadline()
		return nil
	}
	stop := func(ctx context.Context, _ []string) error {
		events = append(events, "stop")
		got, _ := ctx.Deadline()
		if !got.Equal(deadline) {
			t.Fatal(got, deadline)
		}
		return nil
	}
	if err := holdAndStopUpgradeAdmission("user", []string{testFolder}, holderFor(77), x.env, admit, stop); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events, []string{"admit", "write", "stop"}) {
		t.Fatal(events)
	}
	events = nil
	if err := holdAndStopUpgradeAdmission("user", []string{testFolder}, holderFor(77), x.env, func(context.Context, []string) error { return windows.ERROR_ACCESS_DENIED }, stop); !errors.Is(err, windows.ERROR_ACCESS_DENIED) || len(events) != 0 {
		t.Fatal(err, events)
	}
}

func TestUpgradeReleaseSharingAndReadonlyRefusalLeaveGuard(t *testing.T) {
	path, old, _ := upgradeAtomicFixture(t)
	legacy, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = removeExclusionFile(path)
	legacy.Close()
	if err == nil || !strings.Contains(err.Error(), "stage=remove-record") {
		t.Fatal("guard falsely released with legacy reader", err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if err := removeExclusionFile(path); !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatal("read-only flag overridden", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, old) {
		t.Fatal("failed release changed guard", string(after), err)
	}
}

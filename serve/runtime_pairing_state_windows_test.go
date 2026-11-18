//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"golang.org/x/sys/windows"
)

func TestPairingStateWindowsFilePrivacy(t *testing.T) {
	s := pairingStateFixture(t)
	if err := privatedir.CheckFile(s.path); err != nil {
		t.Fatalf("actual inherited CAS file: %v", err)
	}
	before, err := os.Stat(s.path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;;FA;;;" + user.User.Sid.String() + ")(A;;FA;;;SY)(A;;FRFW;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(s.path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(s.path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("DACL-only fixture replaced object: %v", err)
	}
	if err = privatedir.CheckDir(s.root); err != nil {
		t.Fatalf("private parent changed: %v", err)
	}
	if _, _, err = s.identity(context.Background()); err == nil {
		t.Fatal("live DACL-only mutation accepted")
	}
	if _, err = loadRuntimePairingState(context.Background(), filepath.Dir(s.root)); err == nil {
		t.Fatal("existing permissive key file accepted")
	}
	if err = privatedir.CheckFile(s.path); err == nil {
		t.Fatal("unsafe file silently repaired")
	}
}

func TestPairingStateWindowsRootPrivacyAfterClosure(t *testing.T) {
	s := pairingStateFixture(t)
	ctx := context.Background()
	cert, tr := pairingStatePeer(t)
	rev, _ := pairingStateRead(t, s)
	rev, err := s.stage(ctx, rev, cert, tr, "peer", "")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	rootChanged := false
	_, err = s.activate(ctx, rev, pairingKey(cert), func(context.Context, pairingPeerRecord) (string, error) {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			return "", err
		}
		sd, err := windows.SecurityDescriptorFromString("O:" + user.User.Sid.String() + "D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")(A;OICI;FA;;;SY)(A;;FR;;;WD)")
		if err != nil {
			return "", err
		}
		name, err := windows.UTF16PtrFromString(s.root)
		if err != nil {
			return "", err
		}
		h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_DATA, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return "", err
		}
		defer windows.CloseHandle(h)
		// Same-handle descriptor write, without descendant propagation. This is
		// an owned temporary root; no actual account or installed ACL changes.
		if err = windows.SetKernelObjectSecurity(h, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sd); err != nil {
			return "", err
		}
		rootChanged = true
		return "observed rights", nil
	})
	if err == nil || !rootChanged {
		t.Fatal("post-callback unsafe root committed")
	}
	after, readErr := os.ReadFile(s.path)
	if readErr != nil || string(before) != string(after) {
		t.Fatalf("unsafe root changed CAS value: %v", readErr)
	}
	if s.admitContext(t.Context(), pairingStateAdmission(cert)) == nil {
		t.Fatal("unsafe root admitted")
	}
}

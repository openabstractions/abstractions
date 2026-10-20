//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

func TestApplicationManifestSignedSystemFileLacksOwnDeclaration(t *testing.T) {
	// This is the existing identity owner's inbox catalog-signature control.
	// It is read only; no program is instantiated or executed.
	program := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	code, proof, err := identity.VerifyFile(program, nil)
	if err != nil || !code.Trusted || proof != identity.ProofBound {
		t.Fatalf("actual existing signature control unavailable: %+v %v %v", code, proof, err)
	}
	h, err := openSignedApplicationManifest(context.Background(), fileid.Self, program)
	if h != nil {
		h.Close()
		t.Fatal("inbox file supplied application manifest")
	}
	if !errors.Is(err, errApplicationManifestMissing) {
		t.Fatalf("signed file without named resource: %v", err)
	}
}

func TestApplicationManifestHoldBlocksWriteDeleteAndAncestorRename(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "ordinary")
	dir := filepath.Join(parent, "child")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(program, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := windows.UTF16PtrFromString(program)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	h := &heldApplicationManifest{file: os.NewFile(uintptr(fd), program), program: program}
	defer h.Close()
	if err := windows.GetFileInformationByHandle(fd, &h.stamp); err != nil {
		t.Fatal(err)
	}
	h.key, err = fileid.FileKeyOf(h.file)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f, err := os.OpenFile(program, os.O_WRONLY, 0); err == nil {
		f.Close()
		t.Fatal("writer admitted during hold")
	}
	if err := os.Remove(program); err == nil {
		t.Fatal("delete admitted during hold")
	}
	for _, path := range []string{dir, parent} {
		if err := os.Rename(path, path+"-moved"); err == nil {
			t.Fatal("ancestor rename admitted during hold", path)
		}
	}
	if err := h.Recheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.Recheck(context.Background()); err == nil {
		t.Fatal("closed hold accepted")
	}
	// A new writer proves our deny-write hold is gone. Directory renaming can
	// independently meet a transient scanner handle after our file is closed.
	f, err := os.OpenFile(program, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal("hold remained after Close", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationManifestReaderRefusesViewLinksAndCancellation(t *testing.T) {
	program := filepath.Join(t.TempDir(), "data.bin")
	if err := os.WriteFile(program, []byte("DATA"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h, err := openSignedApplicationManifest(ctx, fileid.Self, program); err == nil || h != nil {
		t.Fatal("cancelled reader admitted")
	}
	if h, err := openSignedApplicationManifest(context.Background(), nil, program); err == nil || h != nil {
		t.Fatal("missing peer admitted")
	}
	if h, err := openSignedApplicationManifest(context.Background(), fileid.Self, program+":alternate"); err == nil || h != nil {
		t.Fatal("alternate data stream admitted")
	}
	if h, err := openSignedApplicationManifest(context.Background(), fileid.Self, filepath.Dir(program)); err == nil || h != nil {
		t.Fatal("directory admitted")
	}
}

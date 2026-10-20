//go:build darwin && cgo

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestDarwinApplicationManifestUnsignedBundleRefused(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Fixture.app", "Contents")
	if err := os.MkdirAll(filepath.Join(root, "MacOS"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "Resources"), 0700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "MacOS", "Fixture")
	for name, data := range map[string][]byte{
		program: []byte("unsigned executable"),
		filepath.Join(root, "Resources", "OA_APPLICATION_MANIFEST.json"): []byte("{}"),
		filepath.Join(root, "Info.plist"):                                []byte(`<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleExecutable</key><string>Fixture</string></dict></plist>`),
	} {
		if err := os.WriteFile(name, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := openSignedApplicationManifest(context.Background(), fileid.Self, program)
	if h != nil {
		_ = h.Close()
		t.Fatal("unsigned bundle supplied a hold")
	}
	if err == nil {
		t.Fatal("unsigned bundle supplied authority")
	}
	if err := os.Rename(program, program+".moved"); err != nil {
		t.Fatal(err)
	}
}

func TestDarwinApplicationManifestRequiresMainBundleLayout(t *testing.T) {
	program := filepath.Join(t.TempDir(), "child")
	if err := os.WriteFile(program, []byte("child"), 0600); err != nil {
		t.Fatal(err)
	}
	h, err := openSignedApplicationManifest(context.Background(), fileid.Self, program)
	if h != nil || err == nil {
		t.Fatal("arbitrary child inherited bundle authority")
	}
	var closed *heldApplicationManifest
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := closed.Recheck(context.Background()); err == nil {
		t.Fatal("nil hold rechecked")
	}
}

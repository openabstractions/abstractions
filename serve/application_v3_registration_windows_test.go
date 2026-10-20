//go:build windows

package main

import (
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplicationV3RegistrationNativeOrphanCleanupFailure(t *testing.T) {
	ctx := context.Background()
	// Read-only metadata supplies actual native replacement/removal failures.
	state := t.TempDir()
	d := registration1459Directory(t, state)
	r := registration1459Publish(t, d, "native", registration1459Program("native.exe"), "First", "operator", "")
	index := filepath.Join(d.v3.dir, "index.json")
	defer os.Chmod(index, 0600)
	d.v3.decode = func(raw []byte, program string, held *heldApplicationManifest) (wire.ApplicationDescriptor, string, error) {
		v, digest, err := registration1459Decoder(raw, program, held)
		if v.Title == "First" {
			if err := os.Chmod(index, 0400); err != nil {
				t.Fatal(err)
			}
		}
		return v, digest, err
	}
	v, next := registration1459File(t, "native", registration1459Program("native.exe"), "Second")
	if got := d.publishApplicationV3(ctx, v, next, "operator", r); got.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal("native index failure missing", got)
	}
	d.v3.decode = registration1459Decoder
	if err := os.Chmod(index, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(d.v3.dir)
	if err != nil {
		t.Fatal(err)
	}
	orphan := ""
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") && e.Name() != "index.json" && e.Name() != r+".json" {
			orphan = filepath.Join(d.v3.dir, e.Name())
		}
	}
	if orphan == "" {
		t.Fatal("actual failed publication did not retain candidate")
	}
	name, err := windows.UTF16PtrFromString(orphan)
	if err != nil {
		t.Fatal(err)
	}
	fd, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	hold := os.NewFile(uintptr(fd), orphan)
	defer hold.Close()
	before, _ := os.ReadDir(d.v3.dir)
	if got := d.publishApplicationV3(ctx, v, next, "operator", r); got.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal("failed cleanup admitted another candidate", got)
	}
	after, _ := os.ReadDir(d.v3.dir)
	if len(before) != len(after) {
		t.Fatal("cleanup failure grew inventory")
	}
	if err := hold.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := registration1459Directory(t, state)
	if reopened.v3.entries["native"].Revision != r {
		t.Fatal("recovery promoted failed candidate")
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("recovery orphan remains", err)
	}
	if got := reopened.publishApplicationV3(ctx, v, next, "operator", r); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("recovered publication unavailable", got)
	}
}

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func storageTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name, _ := filepath.Rel(root, path)
		if d.IsDir() {
			result[name+"/"] = ""
			return nil
		}
		raw, err := os.ReadFile(path)
		if err == nil {
			result[name] = string(raw)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func TestStorageHelpAndArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"check", "--help"}} {
		var out, diagnostics bytes.Buffer
		err := storageCommand(args, &out, &diagnostics)
		if err != nil && !errors.Is(err, flag.ErrHelp) {
			t.Fatal(err)
		}
		if !strings.Contains(out.String()+diagnostics.String(), "storage check") {
			t.Fatal("missing help")
		}
	}
	for _, args := range [][]string{{"unknown"}, {"check", "extra"}, {"check", "--state-dir", "relative"}, {"check", "--state-dir="}, {"check", "--unknown"}} {
		if err := storageCommand(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
func TestStorageMissingRootDoesNotCreate(t *testing.T) {
	state := filepath.Join(t.TempDir(), "missing")
	var out bytes.Buffer
	if err := storageCommand([]string{"check", "--state-dir", state}, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("check created missing state", err)
	}
	if !strings.Contains(out.String(), "Advisory snapshot") {
		t.Fatal(out.String())
	}
}
func TestStorageDefaultRootUsesRuntimeState(t *testing.T) {
	base := t.TempDir()
	t.Setenv("LOCALAPPDATA", base)
	t.Setenv("XDG_DATA_HOME", base)
	t.Setenv("HOME", base)
	t.Setenv("USERPROFILE", base)
	before := storageTree(t, base)
	if err := storageCommand([]string{"check"}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, storageTree(t, base)) {
		t.Fatal("default check created state")
	}
}
func TestStorageIncompatibleDoesNotMutate(t *testing.T) {
	for _, kind := range []string{"profile", "version"} {
		t.Run(kind, func(t *testing.T) {
			state := t.TempDir()
			root := filepath.Join(state, "jobs")
			var err error
			if kind == "profile" {
				_, err = acceptanceprovider.OpenManaged(root, nil)
			} else {
				_, err = acceptanceprovider.OpenManaged(root, downloadserve.HTTPExecution{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "version" {
				owner := filepath.Join(root, "acceptance", "owner.json")
				raw, err := os.ReadFile(owner)
				if err != nil {
					t.Fatal(err)
				}
				var fields map[string]any
				if err = json.Unmarshal(raw, &fields); err != nil {
					t.Fatal(err)
				}
				fields["version"] = 999
				raw, err = json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				if err = os.WriteFile(owner, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := storageTree(t, state)
			if err := storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
				t.Fatal("incompatible store accepted")
			}
			if !reflect.DeepEqual(before, storageTree(t, state)) {
				t.Fatal("refusal mutated store")
			}
		})
	}
}
func TestStorageGuardedWriterAndRelease(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "jobs")
	if _, err := acceptanceprovider.OpenManaged(root, downloadserve.HTTPExecution{}); err != nil {
		t.Fatal(err)
	}
	guard, err := acceptanceprovider.AcquireHost(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if guard != nil {
			guard.Close()
		}
	})
	err = storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, acceptanceprovider.ErrHostActive) {
		t.Fatalf("live store: %v", err)
	}
	if err = guard.Close(); err != nil {
		t.Fatal(err)
	}
	guard = nil
	before := storageTree(t, state)
	if err = storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, storageTree(t, state)) {
		t.Fatal("compatible check changed existing data")
	}
	next, err := acceptanceprovider.AcquireHost(root)
	if err != nil {
		t.Fatalf("preflight retained guard: %v", err)
	}
	next.Close()
}

func TestStorageEmptyExistingRootOnlyAddsGuard(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "jobs")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "acceptance", "owner.json")); !os.IsNotExist(err) {
		t.Fatal("preflight allocated owner", err)
	}
	if err := storageCommand([]string{"check", "--state-dir", state}, &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

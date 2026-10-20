package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Public charter exports maintained inputs through split.manifest; private
// development reads the same source files directly. Neither layout may skip.
func productionFile(t *testing.T, relative string) string {
	t.Helper()
	path := filepath.Join("../test/production", filepath.FromSlash(relative))
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return path
	}
	var root string
	t.Run("private-production-source", func(t *testing.T) {
		root = privateInput(t, "..", "..", "openabstractions-flat")
	})
	if root == "" {
		t.Fatalf("missing maintained production fixture %s (public idl/test/production or private flat source)", relative)
	}
	path = filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("missing maintained production fixture %s (public idl/test/production or private flat source): %v", relative, err)
	}
	return path
}

func TestProductionFileLookupChild(t *testing.T) {
	if os.Getenv("OA_PRODUCTION_LOOKUP_CHILD") != "1" {
		return
	}
	path := productionFile(t, "fixture.thrift")
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "selected fixture" {
		t.Fatalf("selected fixture %s: %q %v", path, raw, err)
	}
}

func TestProductionFileLookupPreservesFailure(t *testing.T) {
	for _, test := range []struct {
		name, layout string
		wantFailure  bool
	}{
		{"published-first", "published", false},
		{"private", "private", false},
		{"both-missing", "missing", true},
		{"private-child-missing", "private-empty", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			cwd := filepath.Join(root, "idl", "gen")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			var fixture string
			if test.layout == "published" {
				fixture = filepath.Join(root, "idl", "test", "production", "fixture.thrift")
			} else if test.layout == "private" || test.layout == "private-empty" {
				fixture = filepath.Join(root, "openabstractions-flat", "fixture.thrift")
			}
			if fixture != "" {
				if err := os.MkdirAll(filepath.Dir(fixture), 0o700); err != nil {
					t.Fatal(err)
				}
				if test.layout != "private-empty" {
					if err := os.WriteFile(fixture, []byte("selected fixture"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			command := exec.Command(os.Args[0], "-test.v", "-test.run=^TestProductionFileLookupChild$")
			command.Dir = cwd
			command.Env = append(os.Environ(), "OA_PRODUCTION_LOOKUP_CHILD=1", "IDL_GEN_TESTS_WITHOUT_PYTHON=1")
			out, err := command.CombinedOutput()
			if test.wantFailure {
				if err == nil || !strings.Contains(string(out), "--- FAIL: TestProductionFileLookupChild") || !strings.Contains(string(out), "missing maintained production fixture") || strings.Contains(string(out), "--- SKIP: TestProductionFileLookupChild (") {
					t.Fatalf("missing fixture must fail the parent: %v\n%s", err, out)
				}
			} else if err != nil || !strings.Contains(string(out), "--- PASS: TestProductionFileLookupChild") {
				t.Fatalf("maintained fixture must pass: %v\n%s", err, out)
			}
		})
	}
}

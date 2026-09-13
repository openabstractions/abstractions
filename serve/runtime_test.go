package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeArguments(t *testing.T) {
	for _, args := range [][]string{{"--without-models", "--model-endpoint", "ep"}, {"--jobs-root", "store"}, {"--jobs-owner", "owner"}, {"--without-jobs", "--jobs-endpoint", "ep"}, {"--without-jobs", "--jobs-run-downloads"}, {"--state-dir", "relative"}} {
		if _, err := parseRuntime(args, io.Discard); err == nil {
			t.Fatalf("accepted incomplete admission setup: %v", args)
		}
	}
	jobs, err := parseRuntime([]string{"--jobs-root", "store", "--jobs-owner", "owner", "--jobs-endpoint", "ep"}, io.Discard)
	if err != nil || jobs.jobRoot != "store" || jobs.jobOwner != "owner" || jobs.jobEndpoint != "ep" {
		t.Fatalf("admission flags: %+v %v", jobs, err)
	}
	if _, err := parseRuntime([]string{"--help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
	if _, err := parseRuntime([]string{"extra"}, io.Discard); err == nil {
		t.Fatal("accepted extra argument")
	}
	options, err := parseRuntime([]string{"--endpoint", "resolver", "--log-endpoint", "logs", "--config-endpoint", "config", "--out", "file"}, io.Discard)
	if err != nil || options.endpoint != "resolver" || options.logEndpoint != "logs" || options.configEndpoint != "config" || options.out != "file" {
		t.Fatalf("%+v: %v", options, err)
	}
}

func TestCanceledRuntimeCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "records.jsonl")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runRuntime(ctx, runtimeFlags{out: path}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("canceled startup changed filesystem: %v", err)
	}
}

func TestRuntimeStateLocation(t *testing.T) {
	base := t.TempDir()
	for _, platform := range []string{"windows", "linux", "darwin"} {
		got, err := runtimeStateDir(platform, func(string) string { return base }, func() (string, error) { return base, nil })
		want := filepath.Join(base, "openabstractions", "runtime-v1")
		if platform == "darwin" {
			want = filepath.Join(base, "Library", "Application Support", "openabstractions", "runtime-v1")
		}
		if err != nil || got != want {
			t.Fatalf("%s: %q %v", platform, got, err)
		}
	}
	for _, platform := range []string{"windows", "linux", "darwin", "unknown"} {
		if _, err := runtimeStateDir(platform, func(string) string { return "relative" }, func() (string, error) { return "relative", nil }); err == nil {
			t.Fatalf("accepted relative state for %s", platform)
		}
	}
	got, err := runtimeStateDir("linux", func(string) string { return "" }, func() (string, error) { return base, nil })
	if err != nil || got != filepath.Join(base, ".local", "share", "openabstractions", "runtime-v1") {
		t.Fatalf("Linux default: %q %v", got, err)
	}
	for _, args := range [][]string{{}, {"--jobs-endpoint", "isolated"}, {"--jobs-run-downloads"}, {"--without-jobs"}} {
		if _, err := parseRuntime(args, io.Discard); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

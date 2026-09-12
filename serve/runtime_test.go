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
	for _, args := range [][]string{{"--jobs-root", "store"}, {"--jobs-owner", "owner"}, {"--jobs-endpoint", "ep"}} {
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

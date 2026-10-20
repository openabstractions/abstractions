package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// The copied test image stands in for the operator CLI and refuses missing
// independent server proof, reproducing the native XPC setup integration gap.
func TestMain(m *testing.M) {
	if os.Getenv("OA_PLAYGROUND_OPERATOR_PROOF_TEST") == "1" {
		program, err := os.Executable()
		if err != nil {
			os.Exit(64)
		}
		want := []string{"credentials", "list", "--endpoint", "xpc:owned-fixture", "--runtime-program", program, "--json", "--timeout", "30s"}
		if !reflect.DeepEqual(os.Args[1:], want) {
			os.Exit(64)
		}
		fmt.Print(`{"records":[]}`)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOperatorCallSendsIndependentRuntimeProof(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	name := "openabstractions"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	operator := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(operator, bytes, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OA_PLAYGROUND_OPERATOR_PROOF_TEST", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := operatorCall(ctx, connection{RuntimeProgram: operator, Endpoint: "xpc:owned-fixture"}, nil, "credentials", "list")
	if err != nil || string(output) != `{"records":[]}` {
		t.Fatalf("operator metadata call: %q %v", output, err)
	}
}

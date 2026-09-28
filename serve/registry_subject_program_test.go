package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestRegistryRejectsSelfDeclarationThroughShortProgramAlias(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("DOS short aliases are Windows-specific")
	}
	dir := filepath.Join(t.TempDir(), "program with spaces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Clean(copyTestBinaryTo(t, dir, "oa-self-declaration"))
	short, hasAlias := shortSubjectProgramAlias(t, program)
	if !hasAlias {
		t.Skip("the filesystem did not provide a distinct DOS short alias")
	}

	state := testRights(t, t.TempDir())
	if err := state.install([]string{ActionProviderManage}, nil); err != nil {
		t.Fatal(err)
	}
	caller := rwire.Subject{Account: state.owner, Program: program}
	if err := state.policy.Set(caller, ActionProviderManage, "account", true); err != nil {
		t.Fatal(err)
	}
	registry := &runtimeRegistry{operator: &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: state}}}
	declaration := wire.Declaration{Name: "self-alias", Program: short, Arguments: []string{}, Endpoint: "self-alias",
		Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Activation: wire.ActivationAttach}
	result := registry.Declare(context.Background(), inference.Subject{Account: caller.Account, Program: caller.Program}, "", declaration)
	if result.Outcome != wire.DeclarationEditOutcomeInvalid || result.Reason != "program:self" {
		t.Fatalf("self declaration through short alias: %+v", result)
	}
}

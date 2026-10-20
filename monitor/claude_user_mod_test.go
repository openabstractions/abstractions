package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"reflect"
	"testing"
)

func TestClaudeUserModPartialFailurePreservesSavedMCP(t *testing.T) {
	root := t.TempDir()
	commands := 0
	image := &claudePanelFixtureImage{program: "kept-helper", key: "held-key"}
	c := &claudePanelConnections{userMod: true, state: func() (string, error) { return root, nil }, prepare: func(context.Context) (claudePanelImage, error) { return image, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, enroll: func(context.Context, claudePanelImage) (string, error) { return "enrolled", nil }, admit: func(_ context.Context, image claudePanelImage) (claudePanelImage, error) {
		return &claudePanelFixtureImage{program: image.Program(), key: image.FileKey()}, nil
	}}
	c.command = func(_ context.Context, program string, args ...string) ([]byte, error) {
		commands++
		if program != "kept-helper" {
			t.Fatal(program)
		}
		if commands == 2 {
			return []byte(`{"outcome":"page","state":"unreviewed"}`), nil
		}
		if commands == 3 {
			return nil, errors.New("native plugin refused")
		}
		return nil, nil
	}
	defer c.close()
	result, err := c.saveProject(context.Background(), fileid.FileID{})
	if err != nil || result.Outcome != "saved" || result.Companion != "unavailable" || commands != 3 {
		t.Fatal(result, err, commands)
	}
}

type userModCloseFailureCLI struct{ claudePanelFixtureCLI }

func (*userModCloseFailureCLI) Close() error { return errors.New("private cleanup failure") }

func TestClaudeUserModStateUsesGuardedReadOnlyOwner(t *testing.T) {
	root := t.TempDir()
	manager := &claudePanelFixtureImage{program: "installed-manager"}
	calls := 0
	c := &claudePanelConnections{cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, command: func(_ context.Context, program string, args ...string) ([]byte, error) {
		calls++
		want := []string{"plugin", "claude", "status", "--scope", "user", "--state-dir", root, "--claude", "fixture-gateway", "--registered-program", "owned-kept-helper"}
		if program != manager.Program() || !reflect.DeepEqual(args, want) {
			t.Fatal(program, args)
		}
		return []byte(`{"outcome":"page","state":"current"}`), nil
	}}
	if state := c.readUserModState(context.Background(), manager, root, "owned-kept-helper"); state != "current" || calls != 1 {
		t.Fatal(state, calls)
	}
	c.cli = func(context.Context) (claudePanelCLI, error) { return &userModCloseFailureCLI{}, nil }
	if state := c.readUserModState(context.Background(), manager, root, "owned-kept-helper"); state != "unknown" {
		t.Fatal("cleanup uncertainty claimed ready", state)
	}
}

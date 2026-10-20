package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"reflect"
	"testing"
)

func TestCompanionDeclarationPrecedesNativeKeepInBothSetupFlows(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(map[bool]string{false: "declared", true: "producer-refused"}[refuse], func(t *testing.T) {
			declared, enrolled := false, false
			c := &claudePanelConnections{prepare: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }}
			c.declare = func(context.Context, claudePanelImage, string) error {
				declared = true
				if refuse {
					return errors.New("inert declaration refusal")
				}
				return nil
			}
			c.enroll = func(context.Context, claudePanelImage) (string, error) {
				if !declared {
					t.Fatal("Keep preceded declaration")
				}
				enrolled = true
				return "pending", nil
			}
			_, err := c.saveProject(t.Context(), fileid.FileID{})
			if !declared || enrolled == refuse || (refuse && err == nil) {
				t.Fatal(declared, enrolled, err)
			}
			d := codexPanelFixture(t)
			declared, enrolled = false, false
			d.declare = func(context.Context, claudePanelImage) error {
				declared = true
				if refuse {
					return errors.New("inert declaration refusal")
				}
				return nil
			}
			d.enroll = func(context.Context, claudePanelImage) (string, error) {
				if !declared {
					t.Fatal("Keep preceded declaration")
				}
				enrolled = true
				return "pending", nil
			}
			_, err = d.change(t.Context(), false, "1")
			if !declared || enrolled == refuse || (refuse && err == nil) {
				t.Fatal(declared, enrolled, err)
			}
			d.close()
		})
	}
}

func TestCompanionDeclarationUsesInstalledProducerAndHeldTarget(t *testing.T) {
	manager, target, cli := &claudePanelFixtureImage{program: "installed-manager", key: "manager-key"}, &claudePanelFixtureImage{program: "staged-target", key: "target-key"}, &claudePanelFixtureCLI{claudePanelFixtureImage{program: "vendor-cli", key: "cli-key"}}
	called := false
	err := declarePanelCompanion(t.Context(), manager, target, cli, "state", "claude", "required", "OpenAbstractions", "", func(_ context.Context, program string, args ...string) ([]byte, error) {
		called = true
		if program != manager.Program() {
			t.Fatal("staged helper executed", program)
		}
		want := []string{"declare-companions", "--host", "claude", "--member", "required", "--state-dir", "state", "--cli", cli.Program(), "--helper-program", target.Program(), "--helper-key", target.FileKey(), "--name", "OpenAbstractions"}
		if !reflect.DeepEqual(args, want) {
			t.Fatal(args)
		}
		return nil, nil
	})
	if err != nil || !called {
		t.Fatal(called, err)
	}
}

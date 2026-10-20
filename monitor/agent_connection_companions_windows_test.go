//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestAgentConnectionCodexCompanionUsesKeptOwnerWithoutNewKeep(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "revoked"}[revoked], func(t *testing.T) {
			c := codexPanelFixture(t)
			c.enroll = func(context.Context, claudePanelImage) (string, error) {
				t.Fatal("maintenance requested new Keep")
				return "", nil
			}
			c.declare = func(context.Context, claudePanelImage) error {
				t.Fatal("maintenance replaced desired membership")
				return nil
			}
			writes := 0
			c.command = func(_ context.Context, program string, args ...string) ([]byte, error) {
				if args[0] == "connect" {
					writes++
					if program != "codex-agent" {
						t.Fatal(program)
					}
				}
				return json.Marshal(codexConnectionView{Outcome: "page", Scope: "user", Owned: true, Registration: "saved", Program: "codex-agent", SessionStart: "installed"})
			}
			verify := func() error {
				if revoked {
					return errors.New("revoked")
				}
				return nil
			}
			err := c.refreshKeptCompanion(t.Context(), &claudePanelFixtureImage{program: "codex-agent", key: "new-key"}, verify)
			if revoked {
				if err == nil || writes != 0 {
					t.Fatal("revocation reached producer", err, writes)
				}
			} else if err != nil || writes != 1 {
				t.Fatal(err, writes)
			}
		})
	}
}

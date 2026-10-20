package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

func TestAgentTransitionRecoveryCaptureExactFields(t *testing.T) {
	prior := agentPrevious{Application: "oa-claude-integration", Program: "/owned/helper", FileKey: "native-key", Revision: "revision"}
	marker := codexSetupMarker{Scope: "scope", Self: "self", CLI: "cli", State: "state", Name: "OpenAbstractions", Active: true, Previous: &prior}
	raw, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeCodexSetupMarker(raw); err != nil {
		t.Fatal(err)
	}
	for _, changed := range [][]byte{
		bytes.Replace(raw, []byte(`"Previous"`), []byte(`"previous"`), 1),
		bytes.Replace(raw, []byte(`"Scope"`), []byte(`"scope"`), 1),
		bytes.Replace(raw, []byte(`"revision":"revision"`), []byte(`"Revision":"revision"`), 1),
		bytes.Replace(raw, []byte(`"revision":"revision"`), []byte(`"revision":null`), 1),
		bytes.Replace(raw, []byte(`"revision":"revision"`), []byte(`"revision":"revision","revision":"other"`), 1),
	} {
		if bytes.Equal(raw, changed) {
			t.Fatal("fixture did not change")
		}
		if _, err := decodeCodexSetupMarker(changed); err == nil {
			t.Fatalf("accepted changed capture: %s", changed)
		}
	}
}

func TestOwnedConnectionTransitionBoundaries(t *testing.T) {
	for _, mode := range []string{"success", "add-failed", "remove-lost", "add-lost", "foreign-after-remove", "readback-unknown", "marker-race", "identity-race"} {
		t.Run(mode, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "private")
			if err := privatedir.Make(root); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "marker")
			base, next := []byte("old-owned-marker"), []byte("new-owned-marker")
			if err := os.WriteFile(path, base, 0600); err != nil {
				t.Fatal(err)
			}
			var current any = "old"
			var writes []any
			unknown, changed := false, false
			read := func() (any, error) {
				if unknown {
					return nil, errors.New("unknown readback")
				}
				return current, nil
			}
			write := func(value any) error {
				writes = append(writes, value)
				if value == nil {
					current = nil
					if mode == "foreign-after-remove" {
						current = "foreign"
					}
					if mode == "remove-lost" {
						return errors.New("lost remove")
					}
					return nil
				}
				if value == "new" {
					if mode == "add-failed" {
						return errors.New("failed add")
					}
					current = value
					if mode == "readback-unknown" {
						unknown = true
					}
					if mode == "marker-race" {
						if err := os.WriteFile(path, []byte("external-marker"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "identity-race" {
						changed = true
					}
					if mode == "add-lost" {
						return errors.New("lost add")
					}
					return nil
				}
				current = value
				return nil
			}
			err := transitionOwnedConfiguration(context.Background(), path, base, next, "old", "new", read, write, func() error {
				if changed {
					return errors.New("identity changed")
				}
				return nil
			})
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			switch mode {
			case "success", "add-lost":
				if err != nil || current != "new" || !bytes.Equal(raw, next) {
					t.Fatal(err, current, string(raw), writes)
				}
			case "add-failed", "remove-lost":
				if err == nil || current != "old" || !bytes.Equal(raw, base) || !strings.Contains(err.Error(), "restored") {
					t.Fatal(err, current, string(raw), writes)
				}
			case "foreign-after-remove":
				if err == nil || current != "foreign" || len(writes) != 1 || !bytes.Equal(raw, base) {
					t.Fatal(err, current, writes)
				}
			case "readback-unknown":
				if err == nil || len(writes) != 2 || !bytes.Equal(raw, base) {
					t.Fatal(err, writes, string(raw))
				}
			case "marker-race":
				if err == nil || len(writes) != 2 || string(raw) != "external-marker" {
					t.Fatal(err, writes, string(raw))
				}
			case "identity-race":
				if err == nil || len(writes) != 2 || !bytes.Equal(raw, base) {
					t.Fatal(err, writes, string(raw))
				}
			}
		})
	}
}

func TestAgentTransitionClaudeOwnedUserCommandAndMarker(t *testing.T) {
	o, self, v := claudeSetupFixture(t)
	o.project, o.userRoot, o.name = "", v.project, "OpenAbstractions"
	v.scope = "user"
	old := filepath.Join(filepath.Dir(self), "oa-claude-integration")
	if err := os.Rename(self, old); err != nil {
		t.Fatal(err)
	}
	if err := applyClaudeSetup(context.Background(), o, old, v.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(context.Background(), fileid.Self, old, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	newImage := filepath.Join(filepath.Dir(old), "oa-claude-integration-"+strings.Repeat("a", 64))
	if err := os.WriteFile(newImage, []byte("new kept native fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := agentPrevious{Application: "oa-claude-integration", Program: old, FileKey: id.Key(), Revision: "native-revision"}
	if err := applyAgentTransition(context.Background(), agentTransitionOptions{product: "claude", state: o.state, cli: o.cli, scope: o.userRoot, previous: previous}, newImage, v.run, nil); err != nil {
		t.Fatal(err)
	}
	page, err := readClaudeConnectionsFor(context.Background(), o.state, func() (string, error) { return o.userRoot, nil })
	if err != nil || len(page.Connections) != 1 || page.Connections[0].Program != newImage || page.Connections[0].Registration != "saved" || !reflect.DeepEqual(page.Connections[0].Previous, &previous) {
		t.Fatal(page, err)
	}
	if raw, err := os.ReadFile(old); err != nil || string(raw) != "identity fixture; never executed" {
		t.Fatal("old image changed", err)
	}
}

func TestAgentTransitionCodexOwnedUserCommandAndMarker(t *testing.T) {
	o, self, home, v := codexFixture(t)
	o.name = "OpenAbstractions"
	old := filepath.Join(filepath.Dir(self), "oa-codex-integration")
	if err := os.Rename(self, old); err != nil {
		t.Fatal(err)
	}
	if _, err := applyCodexSetup(context.Background(), o, old, home, v.run); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(context.Background(), fileid.Self, old, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	newImage := filepath.Join(filepath.Dir(old), "oa-codex-integration-"+strings.Repeat("b", 64))
	if err := os.WriteFile(newImage, []byte("new kept native fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	previous := agentPrevious{Application: "oa-codex-integration", Program: old, FileKey: id.Key(), Revision: "native-revision"}
	if err := applyAgentTransition(context.Background(), agentTransitionOptions{product: "codex", state: o.state, cli: o.cli, scope: home, previous: previous}, newImage, nil, v.run); err != nil {
		t.Fatal(err)
	}
	o.action = "connections"
	page, err := applyCodexSetup(context.Background(), o, newImage, home, v.run)
	if err != nil || page.Program != newImage || page.Registration != "saved" || !reflect.DeepEqual(page.Previous, &previous) {
		t.Fatal(page, err)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatal("old image removed", err)
	}
}

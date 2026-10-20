//go:build windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

type connectionLockedImage struct {
	file         *os.File
	program, key string
}

func (i *connectionLockedImage) Program() string             { return i.program }
func (i *connectionLockedImage) FileKey() string             { return i.key }
func (i *connectionLockedImage) Check(context.Context) error { _, err := i.file.Stat(); return err }
func (i *connectionLockedImage) Close() error                { return i.file.Close() }

// Exercise the same non-delete-shared native hold used in real setup. A second
// window's replacement must succeed once setup joins, even if its command failed.
func TestConnectionSetupReleasesImageForAnotherWindow(t *testing.T) {
	for _, party := range []string{"claude", "codex"} {
		for _, mode := range []string{"saved", "command-failed", "pending"} {
			t.Run(party+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				program, backup := filepath.Join(root, "helper.exe"), filepath.Join(root, "previous.exe")
				if err := os.WriteFile(program, []byte("inert helper"), 0600); err != nil {
					t.Fatal(err)
				}
				hold := func(context.Context) (claudePanelImage, error) {
					file, err := nativecli.OpenSource(context.Background(), program)
					if err != nil {
						return nil, err
					}
					key, err := fileid.FileKeyOf(file)
					if err != nil {
						file.Close()
						return nil, err
					}
					return &connectionLockedImage{file: file, program: program, key: key}, nil
				}
				enroll := func(context.Context, claudePanelImage) (string, error) {
					if mode == "pending" {
						return "pending", nil
					}
					return "enrolled", nil
				}
				command := func(context.Context, string, ...string) ([]byte, error) {
					if err := os.Rename(program, backup); err == nil {
						t.Fatal("helper was not held during the configuration command")
					}
					if mode == "command-failed" {
						return nil, errors.New("inert vendor failure")
					}
					return []byte(`{"outcome":"page","scope":"user","registration":"saved"}`), nil
				}
				var err error
				var closeWindow func()
				if party == "claude" {
					c := &claudePanelConnections{prepare: hold, enroll: enroll, admit: func(ctx context.Context, _ claudePanelImage) (claudePanelImage, error) { return hold(ctx) }, cli: func(context.Context) (claudePanelCLI, error) { return &claudePanelFixtureCLI{}, nil }, state: func() (string, error) { return root, nil }, command: command}
					closeWindow = c.close
					defer c.close()
					_, err = c.saveProject(context.Background(), fileid.FileID{})
				} else {
					c := codexPanelFixture(t)
					c.prepare, c.enroll, c.command = hold, enroll, command
					c.admit = func(ctx context.Context, _ claudePanelImage) (claudePanelImage, error) { return hold(ctx) }
					closeWindow = c.close
					defer c.close()
					_, err = c.change(context.Background(), false, "1")
				}
				if (err != nil) != (mode == "command-failed") {
					t.Fatalf("unexpected setup result: %v", err)
				}
				if mode == "pending" {
					if err := os.Rename(program, backup); err == nil {
						t.Fatal("unfinished Keep lost its candidate hold")
					}
					closeWindow()
				}
				if err := os.Rename(program, backup); err != nil {
					t.Fatal("completed setup still blocks another window's update", err)
				}
			})
		}
	}
}

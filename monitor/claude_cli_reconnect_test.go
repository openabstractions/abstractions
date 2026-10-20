package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A saved Claude Code command that no longer verifies (owner report
// 2026-11-10) offers Connect when the running Claude Code verifies; Connect
// then adopts the running command. Without one the page keeps Connect off.
func TestClaudeUnverifiableSavedCLIOffersReconnect(t *testing.T) {
	for _, mode := range []string{"running", "closed", "other-failure"} {
		t.Run(mode, func(t *testing.T) {
			live := 0
			c := &claudePanelConnections{
				cli: func(context.Context) (claudePanelCLI, error) {
					if mode == "other-failure" {
						return nil, errors.New("Code is unavailable; open it and retry")
					}
					return nil, &claudeSavedCLIError{errors.New("Code publisher or description differs from its registered product")}
				},
				liveCLI: func(context.Context) (claudePanelCLI, error) {
					live++
					if mode == "closed" {
						return nil, errors.New("Code is unavailable; open it and retry")
					}
					return &claudePanelFixtureCLI{claudePanelFixtureImage{program: "running-claude.exe"}}, nil
				},
				state: func() (string, error) { return t.TempDir(), nil },
				image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil },
				command: func(context.Context, string, ...string) ([]byte, error) {
					return []byte(`{"outcome":"page","connections":[]}`), nil
				},
			}
			view, err := c.read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if view.CLIAvailable {
				t.Fatal("unverified saved command enabled management", view)
			}
			if view.ReconnectAvailable != (mode == "running") {
				t.Fatal("reconnect offer", view.ReconnectAvailable, view)
			}
			if mode == "other-failure" && live != 0 {
				t.Fatal("a closed Code session probed the running image", live)
			}
			if mode != "other-failure" && view.CLIReasonCode != "cli_saved_verification" {
				t.Fatal(view.CLIReasonCode)
			}
			cli, err := c.connectionCLI(context.Background())
			switch mode {
			case "running":
				if err != nil || cli == nil || cli.Program() != "running-claude.exe" {
					t.Fatal("Connect did not adopt the running Claude Code", cli, err)
				}
				_ = cli.Close()
			default:
				if err == nil || cli != nil {
					t.Fatal("Connect admitted an unverified command", cli, err)
				}
			}
		})
	}
}

func TestClaudeConnectionCLIKeepsVerifiedSavedCommand(t *testing.T) {
	c := &claudePanelConnections{
		cli: func(context.Context) (claudePanelCLI, error) {
			return &claudePanelFixtureCLI{claudePanelFixtureImage{program: "saved-claude.exe"}}, nil
		},
		liveCLI: func(context.Context) (claudePanelCLI, error) {
			t.Fatal("a verified saved command consulted the running image")
			return nil, nil
		},
	}
	cli, err := c.connectionCLI(context.Background())
	if err != nil || cli.Program() != "saved-claude.exe" {
		t.Fatal(cli, err)
	}
	if c.reconnectAvailable(context.Background(), nil) {
		t.Fatal("reconnect offered without a saved failure")
	}
}

// OA's own helper or state-folder failure keeps its own reason: the Claude
// Code change sentence and Connect's adoption apply only to Claude Code.
func TestClaudeManagerFailureIsNotAClaudeCodeChange(t *testing.T) {
	code, text := claudeCLISelectionFailure(&claudeManagerError{errors.New("helper failed")})
	if code != "cli_manager" || strings.Contains(text, "Claude Code changed") {
		t.Fatal("manager failure reported as a Claude Code change", code, text)
	}
	code, text = claudeCLISelectionFailure(&claudeSavedCLIError{errors.New("image changed")})
	if code != "cli_saved_verification" || text != "Claude Code changed since you connected it. Open Claude Code, then return here." {
		t.Fatal("saved change sentence", code, text)
	}
	c := &claudePanelConnections{liveCLI: func(context.Context) (claudePanelCLI, error) {
		t.Fatal("manager failure consulted the running Claude Code")
		return nil, nil
	}}
	if c.reconnectAvailable(context.Background(), &claudeManagerError{errors.New("x")}) {
		t.Fatal("manager failure offered Connect")
	}
}

// The saved command names another Claude Code installation than the running
// one. Reading the page saves nothing and keeps the saved command; only the
// person's Connect adopts the running installation.
func TestClaudeOtherSavedInstallationAdoptedOnlyByConnect(t *testing.T) {
	savedValid := true
	var commands [][]string
	c := &claudePanelConnections{
		cli: func(context.Context) (claudePanelCLI, error) {
			if savedValid {
				return &claudePanelFixtureCLI{claudePanelFixtureImage{program: `D:\other\claude.exe`}}, nil
			}
			return nil, &claudeSavedCLIError{errors.New("saved installation changed")}
		},
		liveCLI: func(context.Context) (claudePanelCLI, error) {
			return &claudePanelFixtureCLI{claudePanelFixtureImage{program: "running-claude.exe"}}, nil
		},
		state: func() (string, error) { return t.TempDir(), nil },
		image: func(context.Context) (claudePanelImage, error) { return &claudePanelFixtureImage{}, nil },
		command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			commands = append(commands, args)
			return []byte(`{"outcome":"page","connections":[]}`), nil
		},
	}
	view, err := c.read(context.Background())
	if err != nil || !view.CLIAvailable || view.ReconnectAvailable {
		t.Fatal("a valid other installation was replaced on read", view, err)
	}
	cli, err := c.connectionCLI(context.Background())
	if err != nil || cli.Program() != `D:\other\claude.exe` {
		t.Fatal("Connect replaced a valid saved installation", cli, err)
	}
	_ = cli.Close()
	savedValid = false
	before := len(commands)
	view, err = c.read(context.Background())
	if err != nil || view.CLIAvailable || !view.ReconnectAvailable {
		t.Fatal("changed saved installation not offered for Connect", view, err)
	}
	for _, args := range commands[before:] {
		for _, arg := range args {
			if arg == "running-claude.exe" {
				t.Fatal("reading the page saved the running installation", args)
			}
		}
	}
	cli, err = c.connectionCLI(context.Background())
	if err != nil || cli.Program() != "running-claude.exe" {
		t.Fatal("Connect did not adopt the running installation", cli, err)
	}
	_ = cli.Close()
}

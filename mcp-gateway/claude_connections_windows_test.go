package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestClaudeConnectionsFirstRunRefusesReparseParentAndState(t *testing.T) {
	for _, suffix := range []string{"", "claude-connections", "setup/claude-connections", "OpenAbstractions/setup/claude-connections"} {
		t.Run(suffix, func(t *testing.T) {
			root, other := t.TempDir(), t.TempDir()
			link := filepath.Join(root, "junction")
			// Junctions require no symlink privilege; both endpoints are inert
			// fixture folders. No owner configuration is opened or changed.
			if out, err := exec.Command("cmd.exe", "/c", "mklink", "/J", link, other).CombinedOutput(); err != nil {
				t.Fatal(err, string(out))
			}
			state := filepath.Join(link, filepath.FromSlash(suffix))
			if page, err := readClaudeConnections(context.Background(), state); err == nil {
				t.Fatal("reparse state looked empty", page)
			}
			var output, diagnostic bytes.Buffer
			if code := claudeConnectionsCommand([]string{"connections", "claude", "--state-dir", state}, &output, &diagnostic); code != 1 {
				t.Fatal(code, output.String())
			}
			var page claudeConnectionPage
			if err := json.Unmarshal(output.Bytes(), &page); err != nil || page.Outcome != "unavailable" {
				t.Fatal(page, err)
			}
		})
	}
}

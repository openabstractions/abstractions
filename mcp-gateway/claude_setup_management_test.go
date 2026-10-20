package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestClaudeSetupRemovalUsesManagementImageWithoutTarget(t *testing.T) {
	options, target, vendor := claudeSetupFixture(t)
	if err := applyClaudeSetup(context.Background(), options, target, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	markerPath := claudeMarkers(t, options.state)[0]
	before, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var original claudeSetupMarker
	if err := json.Unmarshal(before, &original); err != nil {
		t.Fatal(err)
	}
	management := filepath.Join(filepath.Dir(target), "management.exe")
	if err := os.WriteFile(management, []byte("trusted management fixture; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	options.action = "disconnect"
	options.registeredProgram = original.Entry.Command
	vendor.calls = nil
	if err := applyClaudeSetup(context.Background(), options, management, vendor.run, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if len(vendor.calls) != 2 || !reflect.DeepEqual(vendor.calls[1], []string{"mcp", "remove", "--scope", "project", options.name}) {
		t.Fatal(vendor.calls)
	}
	after, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved claudeSetupMarker
	if err := json.Unmarshal(after, &saved); err != nil || saved.Self != original.Self || saved.Active {
		t.Fatal(err, saved)
	}
	vendor.calls = nil
	if err := applyClaudeSetup(context.Background(), options, management, vendor.run, &bytes.Buffer{}); err != nil || len(vendor.calls) != 0 {
		t.Fatal(err, vendor.calls)
	}
}

func TestClaudeSetupManagementPreservesForeignOrUnownedEntry(t *testing.T) {
	for _, mode := range []string{"no-marker", "changed-entry", "wrong-command", "connect"} {
		t.Run(mode, func(t *testing.T) {
			options, target, vendor := claudeSetupFixture(t)
			if err := applyClaudeSetup(context.Background(), options, target, vendor.run, &bytes.Buffer{}); err != nil {
				t.Fatal(err)
			}
			management := filepath.Join(filepath.Dir(target), "management.exe")
			if err := os.WriteFile(management, []byte("management fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			options.action = "disconnect"
			options.registeredProgram = target
			switch mode {
			case "no-marker":
				if err := os.Remove(claudeMarkers(t, options.state)[0]); err != nil {
					t.Fatal(err)
				}
			case "changed-entry":
				document := vendor.document()
				document["mcpServers"].(map[string]any)[options.name] = claudeEntry{Type: "stdio", Command: "foreign", Args: []string{}}
				vendor.save(document)
			case "wrong-command":
				options.registeredProgram = management
			case "connect":
				options.action = "connect"
			}
			before, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
			if err != nil {
				t.Fatal(err)
			}
			vendor.calls = nil
			if err := applyClaudeSetup(context.Background(), options, management, vendor.run, &bytes.Buffer{}); err == nil {
				t.Fatal("unowned management admitted")
			}
			after, err := os.ReadFile(filepath.Join(options.project, ".mcp.json"))
			if err != nil || !bytes.Equal(before, after) || len(vendor.calls) != 0 {
				t.Fatal(err, vendor.calls)
			}
		})
	}
}

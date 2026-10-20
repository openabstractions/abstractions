package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClaudeColdMainFlagsRefuseBeforeMCPSession(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("dedicated Windows helper images")
	}
	folder := t.TempDir()
	image := filepath.Join(folder, "oa-claude-integration.exe")
	build := exec.Command("go", "build", "-o", image, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build actual helper: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(image)
	if err != nil {
		t.Fatal(err)
	}
	images := map[string]string{"claude": image}
	for key, name := range map[string]string{"codex": "oa-codex-integration.exe", "generic": "openabstractions-mcp.exe"} {
		path := filepath.Join(folder, name)
		if err := os.WriteFile(path, raw, 0700); err != nil {
			t.Fatal(err)
		}
		images[key] = path
	}
	binding := string(wire.EncodeChatBinding(&wire.ChatBinding{ChatID: codexAttachThread, Launch: &wire.ChatLaunch{Session: codexAttachThread, Cwd: folder, Origin: wire.ChatOriginDesktop, Mode: wire.ChatModePlan}}))
	cold := []string{"--messages-reply-only", "--inbox-context", "chat-retained", "--claude-resume-binding", binding, "--claude-resume-cwd-key", "recorded-key"}
	cases := []struct {
		name, image string
		args        []string
	}{
		{"unknown flag", "claude", []string{"--not-an-option"}},
		{"positional", "claude", []string{"unexpected"}},
		{"invalid context", "claude", []string{"--inbox-context", "../foreign"}},
		{"combined views", "claude", []string{"--messages-reply-only", "--messages-only", "--inbox-context", "fixed"}},
		{"check view", "claude", []string{"--messages-reply-only", "--check", "--inbox-context", "fixed"}},
		{"missing context", "claude", []string{"--messages-reply-only"}},
		{"missing key", "claude", []string{"--messages-reply-only", "--inbox-context", "fixed", "--claude-resume-binding", binding}},
		{"missing binding", "claude", []string{"--messages-reply-only", "--inbox-context", "fixed", "--claude-resume-cwd-key", "recorded-key"}},
		{"ordinary view", "claude", []string{"--inbox-context", "fixed", "--claude-resume-binding", binding, "--claude-resume-cwd-key", "recorded-key"}},
		{"malformed binding", "claude", []string{"--messages-reply-only", "--inbox-context", "fixed", "--claude-resume-binding", "{}", "--claude-resume-cwd-key", "recorded-key"}},
		{"wrong integration", "codex", cold},
		{"generic image", "generic", cold},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			args := append([]string{"--state-dir", filepath.Join(folder, fmt.Sprint(index))}, tc.args...)
			command := exec.CommandContext(ctx, images[tc.image], args...)
			command.Env = append(os.Environ(), fmt.Sprintf(`ABSTRACTION_RUNTIME_ENDPOINT=\\.\pipe\oa-cold-flags-%d-%d-absent`, os.Getpid(), index))
			command.Stdin = strings.NewReader("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2025-11-25\",\"capabilities\":{},\"clientInfo\":{\"name\":\"flag-refusal-fixture\",\"version\":\"1\"}}}\n")
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) || exit.ExitCode() != 2 || stdout.Len() != 0 {
				t.Fatalf("flag refusal: err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
		})
	}
}

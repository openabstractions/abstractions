//go:build darwin && cgo

package main

import (
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Uses the existing identity XPC native fixture's temporary launchd namespace,
// bootstrap/terminal-state/bootout owner. No installed LaunchAgent is touched.
func TestDarwinAgentXPCFixture(t *testing.T) {
	service := os.Getenv("OA_DARWIN_AGENT_XPC_SERVICE")
	if service == "" {
		return
	}
	if !strings.HasPrefix(service, "org.openabstractions.test.darwinagent1771.") {
		t.Fatal("invalid owned fixture service")
	}
	exerciseApplicationAgentNativePeer(t, "xpc:"+service, darwinAgentImage(t, "other-agent"))
}

func TestDarwinAgentXPCNativeProgramEnrollment(t *testing.T) {
	if os.Getenv("OA_DARWIN_AGENT_XPC_TEST") != "1" {
		t.Skip("requires isolated task-owned launchd fixture admission")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("org.openabstractions.test.darwinagent1771.%d", os.Getpid())
	domain := "gui/" + strconv.Itoa(os.Getuid())
	target := domain + "/" + label
	command := func(args ...string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return exec.CommandContext(ctx, "/bin/launchctl", args...).CombinedOutput()
	}
	if out, err := command("print", target); err == nil || !strings.Contains(string(out), "Could not find service") {
		t.Fatalf("fixture already present/unavailable: %v %s", err, out)
	}
	escape := html.EscapeString
	var environment strings.Builder
	for key, value := range map[string]string{"OA_DARWIN_AGENT_XPC_SERVICE": label, "TMPDIR": os.Getenv("TMPDIR"), "HOME": os.Getenv("HOME"), "GOMEMLIMIT": "2GiB", "GORACE": "atexit_sleep_ms=0"} {
		fmt.Fprintf(&environment, "<key>%s</key><string>%s</string>", escape(key), escape(value))
	}
	log := filepath.Join(dir, "server.log")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>%s</string><key>ProgramArguments</key><array><string>%s</string><string>-test.run=^TestDarwinAgentXPCFixture$</string><string>-test.timeout=25s</string></array><key>EnvironmentVariables</key><dict>%s</dict><key>MachServices</key><dict><key>%s</key><true/></dict><key>StandardOutPath</key><string>%s</string><key>StandardErrorPath</key><string>%s</string></dict></plist>`, escape(label), escape(exe), environment.String(), escape(label), escape(log), escape(log))
	path := filepath.Join(dir, "server.plist")
	if err := os.WriteFile(path, []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		out, err := command("bootout", target)
		if err != nil {
			t.Errorf("owned service bootout: %v %s", err, out)
		}
		out, err = command("print", target)
		if err == nil || !strings.Contains(string(out), "Could not find service") {
			t.Errorf("owned service cleanup unproven: %v %s", err, out)
		}
	})
	if out, err := command("bootstrap", domain, path); err != nil {
		t.Fatalf("bootstrap: %v %s", err, out)
	}
	if out, err := command("kickstart", target); err != nil {
		t.Fatalf("start: %v %s", err, out)
	}
	until := time.Now().Add(30 * time.Second)
	var state []byte
	for time.Now().Before(until) {
		state, err = command("print", target)
		if err == nil && strings.Contains(string(state), "state = not running") && strings.Contains(string(state), "runs = 1") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, readErr := os.ReadFile(log)
	t.Logf("owned XPC server output:\n%s", raw)
	if err != nil || readErr != nil || !strings.Contains(string(state), "last exit code = 0") || !strings.Contains(string(raw), "PASS") || strings.Contains(string(raw), "FAIL") || strings.Contains(string(raw), "DATA RACE") {
		t.Fatalf("owned server successful terminal unproven: %v %v %s", err, readErr, state)
	}
}

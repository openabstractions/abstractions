package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// helperProcess is the real helper main in its own process, under the
// dedicated name, with an isolated runtime endpoint nothing listens on. The
// facade never activates a runtime for an endpoint named in the environment,
// so nothing here reaches the installed runtime.
type helperProcess struct {
	cmd     *exec.Cmd
	session *mcp.ClientSession
	state   string
	exited  chan error
}

func helperImage(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("the dedicated helper names are Windows images here")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(t.TempDir(), name+".exe")
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(image)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return image
}

func startHelperProcess(t *testing.T, image string, index int) *helperProcess {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(state); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(image, "--state-dir", state)
	cmd.Env = append(os.Environ(), "OA_AGENT_HELPER_PROCESS_FIXTURE=1",
		fmt.Sprintf(`ABSTRACTION_RUNTIME_ENDPOINT=\\.\pipe\oa-helper-chaos-%d-%d-absent`, os.Getpid(), index))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &helperProcess{cmd: cmd, state: state, exited: make(chan error, 1)}
	go func() { p.exited <- cmd.Wait() }()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "process-chaos", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, nil)
	if err != nil {
		t.Fatal("initialize without a runtime", err)
	}
	p.session = session
	return p
}

func (p *helperProcess) connecting(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := p.session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0}})
	if err != nil {
		t.Fatal("the helper closed its MCP connection", err)
	}
	if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, agentToolConnecting) {
		t.Fatal("unexpected answer without a runtime", out.Content[0].(*mcp.TextContent).Text)
	}
	select {
	case err := <-p.exited:
		t.Fatal("the helper process exited while stdio was open", err)
	default:
	}
}

func (p *helperProcess) closeStdio(t *testing.T) {
	t.Helper()
	_ = p.session.Close()
	select {
	case err := <-p.exited:
		if err != nil {
			t.Fatal("the helper exited with an error after stdio EOF", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the helper did not exit after stdio EOF")
	}
	rows := readAgentDiagnostics(context.Background(), p.state)
	if len(rows) != 1 || rows[0].Phase != "stdio_end" || rows[0].Cause != "eof" || rows[0].Losses == 0 || rows[0].LastLoss == "" {
		t.Fatalf("lifecycle: %+v", rows)
	}
}

// With no runtime the real helper process initializes, answers every tool
// call with a typed connecting refusal for as long as stdio stays open, and
// exits cleanly, recording its end, only when the host closes stdio.
func TestAgentHelperProcessOutlivesAbsentRuntime(t *testing.T) {
	for _, name := range []string{"oa-claude-integration", "oa-codex-integration"} {
		t.Run(name, func(t *testing.T) {
			p := startHelperProcess(t, helperImage(t, name), 0)
			for round := 0; round < chaosRounds; round++ {
				p.connecting(t)
				time.Sleep(150 * time.Millisecond)
			}
			p.closeStdio(t)
		})
	}
}

// Seven helper processes started together, as Codex starts them for one
// desktop session, all stay up.
func TestAgentHelperProcessesSevenConcurrent(t *testing.T) {
	image := helperImage(t, "oa-codex-integration")
	helpers := make([]*helperProcess, 7)
	var wg sync.WaitGroup
	for i := range helpers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			helpers[i] = startHelperProcess(t, image, i+1)
		}(i)
	}
	wg.Wait()
	for round := 0; round < chaosRounds; round++ {
		for _, p := range helpers {
			p.connecting(t)
		}
	}
	for _, p := range helpers {
		p.closeStdio(t)
	}
}

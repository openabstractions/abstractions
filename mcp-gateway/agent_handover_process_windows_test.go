package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// copyHelperImage writes this test binary, plus extra bytes, to to.
func copyHelperImage(t *testing.T, to string, extra ...byte) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err == nil && len(extra) != 0 {
		_, err = out.Write(extra)
	}
	if err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
}

// publishHelperImage is Setup's update over a running helper: the running
// image is renamed aside, and the new connector, written beside it, is
// renamed into its path.
func publishHelperImage(t *testing.T, image string, extra ...byte) {
	t.Helper()
	if err := os.Rename(image, image+".0123456789abcdef.tmp.previous"); err != nil {
		t.Fatal(err)
	}
	staged := image + ".fedcba9876543210.tmp"
	copyHelperImage(t, staged, extra...)
	if err := os.Rename(staged, image); err != nil {
		t.Fatal(err)
	}
}

type processHost struct {
	t       *testing.T
	session *mcp.ClientSession
	exited  chan error
	state   string
	ctx     context.Context
}

// startProcessHelper runs image as a helper with env added, in its own
// folder, and connects one host session to it.
func startProcessHelper(t *testing.T, image string, env ...string) *processHost {
	t.Helper()
	state := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(state); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(image, "--state-dir", state)
	cmd.Dir = t.TempDir()
	cmd.Env = append(append(os.Environ(), env...), "OA_HANDOVER_STATE="+state)
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
	p := &processHost{t: t, exited: make(chan error, 1), state: state}
	go func() { p.exited <- cmd.Wait() }()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	p.ctx = ctx
	session, err := mcp.NewClient(&mcp.Implementation{Name: "agent-app", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: stdout, Writer: stdin}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatal("initialize", err)
	}
	p.session = session
	t.Cleanup(func() {
		_ = session.Close()
		select {
		case <-p.exited:
		case <-time.After(20 * time.Second):
			_ = cmd.Process.Kill()
			t.Error("the helper did not exit after stdio EOF")
		}
	})
	return p
}

func (p *processHost) call(name string, arguments map[string]any) string {
	p.t.Helper()
	out, err := p.session.CallTool(p.ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		p.t.Fatal("the host session failed", err)
	}
	return out.Content[0].(*mcp.TextContent).Text
}

func (p *processHost) wait() string {
	return p.call(gateway.ToolMessagesWait, map[string]any{"wait_ms": 0})
}

// rows waits until the shared lifecycle record holds want helpers.
func (p *processHost) rows(want int, within time.Duration) {
	p.t.Helper()
	deadline := time.Now().Add(within)
	for {
		rows := readAgentDiagnostics(context.Background(), p.state)
		if len(rows) == want {
			return
		}
		if time.Now().After(deadline) {
			p.t.Fatalf("lifecycle rows: want %d, got %+v", want, rows)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func integrationsImage(t *testing.T, base string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "agent-integrations")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	image := filepath.Join(dir, base+".exe")
	copyHelperImage(t, image)
	return image
}

func absentRuntime(n int) string {
	return fmt.Sprintf(`ABSTRACTION_RUNTIME_ENDPOINT=\\.\pipe\oa-handover-%d-%d-absent`, os.Getpid(), n)
}

// The real helper process, as Setup's connector update finds it: running
// from agent-integrations while its image is renamed aside and the new
// connector is renamed into its path. The same host session then reaches the
// published connector, which answers as a fresh helper does; the lifecycle
// record proves a second process answered. No runtime listens on the isolated
// endpoint, so nothing reaches an installed runtime. Test images are
// unsigned, as the owner's previews are: the fixture's runtime reads the
// connector enrolled at the published path (CONNECTOR-HANDOVER-AUTH ruling c).
func TestAgentHandoverRealProcessAfterImageMovedAside(t *testing.T) {
	image := integrationsImage(t, "oa-claude-integration")
	p := startProcessHelper(t, image, "OA_AGENT_HELPER_PROCESS_FIXTURE=1", "OA_HANDOVER_TEST_ENROLLED=published", "CLAUDE_CODE_SESSION_ID=local_process_handover", absentRuntime(1))
	if text := p.wait(); !strings.Contains(text, agentToolConnecting) {
		t.Fatal("unexpected first answer", text)
	}
	publishHelperImage(t, image)
	// The moved helper answers with the handover sentence until the
	// published connector answers.
	deadline := time.Now().Add(30 * time.Second)
	for {
		text := p.wait()
		if strings.Contains(text, agentToolConnecting) {
			break
		}
		if !strings.Contains(text, agentHandoverSentence) || time.Now().After(deadline) {
			t.Fatal("the published connector never answered the same session", text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	p.rows(2, 10*time.Second)
}

// A published file the runtime does not keep for this connector is never
// started: an enrollment read the runtime refuses (here no runtime answers),
// or the connector kept at another path. The moved helper names the restart.
func TestAgentHandoverRealProcessRefusesUnkeptConnector(t *testing.T) {
	for _, tc := range []struct {
		name string
		kept string
	}{
		{"refused read", ""},
		{"kept elsewhere", `OA_HANDOVER_TEST_ENROLLED=C:\elsewhere\agent-integrations\oa-claude-integration.exe`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := integrationsImage(t, "oa-claude-integration")
			env := []string{"OA_AGENT_HELPER_PROCESS_FIXTURE=1", "CLAUDE_CODE_SESSION_ID=local_unkept", absentRuntime(2)}
			if tc.kept != "" {
				env = append(env, tc.kept)
			}
			p := startProcessHelper(t, image, env...)
			p.wait()
			publishHelperImage(t, image)
			restart := nativecli.MovedAgentImageSentence("Claude")
			for range 5 {
				if text := p.wait(); text != restart {
					t.Fatal("an unverified connector took the connection", text)
				}
				time.Sleep(500 * time.Millisecond)
			}
			p.rows(1, time.Second)
		})
	}
}

// The original OpenCode stdio keeps its canonical inbox and adapter key
// across a real image replacement. The host only watches after publication;
// the relay must supply the successor's binding from the original start.
func TestAgentHandoverOpenCodeSuccessorKeepsAdapterKey(t *testing.T) {
	image := integrationsImage(t, "oa-opencode-integration")
	const chat = "opencode_handover_chat"
	const key = "0123456789abcdef0123456789abcdef"
	p := startProcessHelper(t, image, "OA_HANDOVER_OPENCODE_FIXTURE=1", "OA_HANDOVER_TEST_ENROLLED=published", "OA_OPENCODE_ADAPTER_KEY="+key, "OA_OPENCODE_HOST_CHAT_ID="+chat)
	var binding struct{ Outcome, Context, Adapter string }
	deadline := time.Now().Add(20 * time.Second)
	for {
		text := p.call(openCodeChatStartTool, map[string]any{})
		if json.Unmarshal([]byte(text), &binding) == nil && binding.Outcome == "observed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the OpenCode chat was not observed", text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if binding.Context != fixtureNativeChatContext(chat) || binding.Adapter != "accepted" {
		t.Fatalf("original OpenCode binding: %+v", binding)
	}
	watch := func() (string, string, string, string) {
		text := p.call("oa_messages_watch", map[string]any{"turn": "ended", "adapter_key": key, "wait_ms": 0})
		var page struct{ Outcome, Context, Reason string }
		_ = json.Unmarshal([]byte(text), &page)
		return page.Outcome, page.Context, page.Reason, text
	}
	if outcome, context, _, text := watch(); outcome != "page" || context != binding.Context {
		t.Fatal("the original Watch did not reach its inbox", text)
	}
	publishHelperImage(t, image)
	p.rows(2, 35*time.Second)
	deadline = time.Now().Add(15 * time.Second)
	for {
		outcome, context, reason, text := watch()
		if outcome == "page" && context == binding.Context {
			break
		}
		if outcome == "page" || outcome == "invalid" || reason == "adapter_key" || time.Now().After(deadline) {
			t.Fatal("the successor Watch did not reach the original inbox", text)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// echoCandidate places a copy of this binary at agent-integrations, the
// published path, and returns a launcher that reads the runtime's answer
// from read and runs the candidate as an echo.
func echoCandidate(t *testing.T, read func(string) agentEnrollmentRead) (string, func() (*agentSuccessor, error)) {
	t.Helper()
	image := integrationsImage(t, "oa-claude-integration")
	return image, agentSuccessorLauncher(image, t.TempDir(), nil, append(os.Environ(), "OA_HANDOVER_ECHO_FIXTURE=1"), read(image))
}

func restoreSuccessorSeams(t *testing.T) {
	signed, authentic, before, key := agentImageSigned, agentSuccessorAuthentic, agentSuccessorBeforeStart, agentChildImageKey
	t.Cleanup(func() {
		agentImageSigned, agentSuccessorAuthentic, agentSuccessorBeforeStart, agentChildImageKey = signed, authentic, before, key
	})
}

// An unsigned successor the runtime keeps at the held path starts, and the
// held candidate cannot be swapped between the check and the start.
func TestAgentSuccessorLauncherStartsTheKeptUnsignedFile(t *testing.T) {
	restoreSuccessorSeams(t)
	if agentImageSigned(ownImagePath()) {
		t.Skip("this test binary is signed")
	}
	image, launch := echoCandidate(t, enrolledAt)
	var swaps []error
	agentSuccessorBeforeStart = func(candidate string) {
		swaps = append(swaps, os.Rename(candidate, candidate+".swapped"), os.WriteFile(candidate, []byte("swapped"), 0o600))
	}
	successor, err := launch()
	if err != nil {
		t.Fatal(err)
	}
	defer successor.stop()
	if len(swaps) != 2 || swaps[0] == nil || swaps[1] == nil {
		t.Fatal("the held candidate could be replaced before its start", swaps)
	}
	ping := `{"ping":1}` + "\n"
	if _, err := successor.in.Write([]byte(ping)); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(successor.out).ReadString('\n')
	if err != nil || line != ping {
		t.Fatal("the started successor did not run", line, err)
	}
	if _, err := os.Stat(image); err != nil {
		t.Fatal("the candidate moved", err)
	}
}

// CONNECTOR-HANDOVER-AUTH refusals: the runtime keeping the connector at
// another path, a state other than enrolled, a refused or failed read, and on
// a signed build another signer even when enrolled, start nothing. A started
// process whose image is not the held file is stopped before anything
// reaches it.
func TestAgentSuccessorLauncherRefusesUnkeptCandidates(t *testing.T) {
	restoreSuccessorSeams(t)
	started := 0
	agentSuccessorBeforeStart = func(string) { started++ }
	state := func(state applications.AgentEnrollmentState) func(string) agentEnrollmentRead {
		return func(image string) agentEnrollmentRead {
			return enrollmentAnswer(applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{Application: "oa-claude-integration", Program: image, State: state}}, nil)
		}
	}
	for name, read := range map[string]func(string) agentEnrollmentRead{
		"kept elsewhere": func(image string) agentEnrollmentRead { return enrolledAt(image + ".other.exe") },
		"none":           state(applications.AgentEnrollmentStateNone),
		"pending":        state(applications.AgentEnrollmentStatePending),
		"refused read": func(string) agentEnrollmentRead {
			return enrollmentAnswer(applications.AgentEnrollmentResult{Outcome: applications.OutcomeForbidden}, nil)
		},
		"failed read": func(string) agentEnrollmentRead {
			return enrollmentAnswer(applications.AgentEnrollmentResult{}, errors.New("runtime unavailable"))
		},
	} {
		_, launch := echoCandidate(t, read)
		if successor, err := launch(); err == nil || successor != nil || started != 0 {
			t.Fatal(name, "started an unkept connector", err, started)
		}
	}
	// A signed build also requires the moved image's signer.
	agentImageSigned = func(string) bool { return true }
	agentSuccessorAuthentic = func(string, string) bool { return false }
	_, launch := echoCandidate(t, enrolledAt)
	if successor, err := launch(); err == nil || successor != nil || started != 0 {
		t.Fatal("a differently signed connector started", err, started)
	}
	agentSuccessorAuthentic = func(string, string) bool { return true }
	agentChildImageKey = func(int) (string, error) { return "another-file", nil }
	_, launch = echoCandidate(t, enrolledAt)
	if successor, err := launch(); err == nil || successor != nil || started != 1 {
		t.Fatal("a successor running another image was kept", err, started)
	}
}

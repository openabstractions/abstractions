package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

type movedReadAnswer struct {
	Outcome, State, Program, Error string
}

// movedEnrollmentReadChild waits for one line on stdin, then reads the
// batch-claude enrollment from the runtime at endpoint over a fresh
// connection, as a moved helper's handover check does, and prints the answer.
func movedEnrollmentReadChild(endpoint string) int {
	fmt.Print("READY 1\n")
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		return 3
	}
	client := applications.NewApplicationsClient(listen.FrameClient{Endpoint: endpoint, Timeout: 10 * time.Second, MaxFrame: 1 << 20})
	result, err := client.ReadAgentEnrollment("batch-claude")
	answer := movedReadAnswer{Outcome: result.Outcome.String()}
	if err != nil {
		answer.Error = err.Error()
	}
	if result.Enrollment != nil {
		answer.State, answer.Program = result.Enrollment.State.String(), result.Enrollment.Program
	}
	raw, _ := json.Marshal(answer)
	fmt.Println(string(raw))
	return 0
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	in, err := os.Open(from)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(to)
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
}

// The ruling's acceptance at the runtime boundary: a real helper process
// keeps running its connector image while a committed update publishes over
// it and Setup moves the image aside. Its next fresh connection reaches the
// real Applications host under the moved path, and its own enrollment read
// answers enrolled at the published path. A process the update never kept
// is refused the same read.
func TestMovedConnectorProcessReadsItsEnrollmentOverTheRuntime(t *testing.T) {
	run := agent1478Fixture(t)
	run.d.agentCompanionProducer = &agentCompanionFixture{}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "installed.exe")
	if err := os.WriteFile(source, []byte("new installed gateway"), 0o600); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "batch-claude.exe")
	copyFile(t, self, program)
	stage := program + ".staged"
	if err := os.WriteFile(stage, []byte("new installed gateway"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run.d.register(run.ctx, run.f.panel, wire.ApplicationDescriptor{Name: "batch-claude", Program: program, Title: "Claude Code integration"}); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	pending := agent1478Set(t, run, "batch-claude", "", true)
	answer(t, run.f, agent1478Question(t, run, "batch-claude").ID, "keep")
	agent1478Wait(t, run, "batch-claude", "enrolled")
	run.d.agentExcluded = func(_ context.Context, candidate, _ string) (bool, error) {
		return identity.SameFile(candidate, source)
	}

	endpoint := shortMediationEndpoint(t)
	h, err := listenApplications(endpoint, run.d, run.d.owner, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	// Applications@3, which carries ReadAgentEnrollment, as the runtime serves it.
	h.v3 = &applicationV3Gateway{directory: run.d}
	ctx, cancel := context.WithCancel(run.ctx)
	served := make(chan error, 1)
	go func() { served <- h.Serve(ctx) }()
	var once sync.Once
	t.Cleanup(func() {
		once.Do(func() {
			cancel()
			h.Close()
			<-served
		})
	})

	// The helper starts from the enrolled image, as the agent app ran it.
	start := func(image string) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
		t.Helper()
		cmd := exec.Command(image, "serve", "runtime", "--supervised", "moved-read-child", endpoint)
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
		lines := bufio.NewReader(out)
		if ready, err := lines.ReadString('\n'); err != nil || ready != "READY 1\n" {
			t.Fatal("child", ready, err)
		}
		return cmd, in, lines
	}
	read := func(in io.WriteCloser, lines *bufio.Reader) movedReadAnswer {
		t.Helper()
		if _, err := in.Write([]byte("read\n")); err != nil {
			t.Fatal(err)
		}
		line, err := lines.ReadString('\n')
		var answer movedReadAnswer
		if err != nil || json.Unmarshal([]byte(line), &answer) != nil {
			t.Fatal("child answer", line, err)
		}
		return answer
	}
	_, helperIn, helperOut := start(program)

	// The committed update: published over the running helper.
	request := applications.AgentUpdateRequest{SourceProgram: source, Members: []applications.AgentUpdateMember{{Application: "batch-claude", ExpectedRevision: pending.Revision, StagedProgram: stage}}}
	prepared := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if prepared.Outcome != applications.OutcomeApplied || prepared.Question == "" {
		t.Fatal("prepare", prepared)
	}
	answer(t, run.f, prepared.Question, "update")
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent); got.Reason != "update_ready" {
		t.Fatal("publication authority", got)
	}
	if err := os.Rename(program, stage+".previous"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(stage, program); err != nil {
		t.Fatal(err)
	}
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, prepared.Intent); got.Reason != "updated" {
		t.Fatal("commit", got)
	}
	// Setup's moved-copy name in its agent-integrations folder.
	integrations := filepath.Join(root, "agent-integrations")
	if err := os.Mkdir(integrations, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(integrations, "oa-claude-integration.exe."+strings.Repeat("c3", 16)+".tmp.previous")
	if err := os.Rename(stage+".previous", moved); err != nil {
		t.Fatal(err)
	}

	got := read(helperIn, helperOut)
	if got.Error != "" || got.Outcome != applications.OutcomePage.String() || got.State != applications.AgentEnrollmentStateEnrolled.String() || !identity.SameSubjectProgram(got.Program, program) {
		t.Fatalf("the kept image's helper did not read its enrollment: %+v", got)
	}

	// Another process under a moved-copy name, which no update kept.
	stranger := filepath.Join(integrations, "oa-claude-integration.exe."+strings.Repeat("d4", 16)+".tmp.previous")
	copyFile(t, self, stranger)
	_, strangerIn, strangerOut := start(stranger)
	if got := read(strangerIn, strangerOut); got.Outcome != applications.OutcomeForbidden.String() || got.Program != "" {
		t.Fatalf("an unkept moved image read the enrollment: %+v", got)
	}
}

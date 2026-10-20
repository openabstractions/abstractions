//go:build windows || darwin

package main

import (
	"context"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"os"
	"testing"
	"time"
)

func exerciseApplicationAgentNativePeer(t *testing.T, endpoint, otherProgram string) {
	run := agent1478Fixture(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program = identity.NormalizeSubjectProgram(program)
	agent1478Register(t, run, "native-agent", program)
	// Actual native image reading, person-answer state and classifier are used
	// at identity's existing Program proof ceiling. This does not prove a held
	// FileID for the connected process's mapped image after path replacement.
	// Only installed shared-component selection is an exact inert fixture.
	run.d.agentImage = nil
	pending := agent1478Set(t, run, "native-agent", "", true)
	q := agent1478Question(t, run, "native-agent")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "native-agent", "enrolled")
	if endpoint == "" {
		id, err := applicationToken()
		if err != nil {
			t.Fatal(err)
		}
		endpoint = mustProviderEndpoint(t, "oa-test-agent1478-"+id[:16])
	}
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	accepted := make(chan *listen.FramedCall, 1)
	serverDone := make(chan error, 1)
	release := make(chan struct{})
	go func() {
		conn, err := l.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 4096)
		if err != nil {
			serverDone <- err
			return
		}
		defer call.Close()
		accepted <- call
		<-release
		serverDone <- call.Reply([]byte("{}"))
	}()
	principal, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	expected := listen.ServerExpectation{Principal: principal, Program: program}
	clientDone := make(chan error, 1)
	go func() {
		_, err := (listen.FrameClient{Endpoint: endpoint, Server: &expected, Timeout: 10 * time.Second, MaxFrame: 4096}).ExchangeFrameContext(ctx, []byte("{}"))
		clientDone <- err
	}()
	var call *listen.FramedCall
	select {
	case call = <-accepted:
	case err := <-serverDone:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		close(release)
		if err := <-serverDone; err != nil {
			t.Error(err)
		}
		if err := <-clientDone; err != nil {
			t.Error(err)
		}
	}()
	peer, err := call.Peer()
	if err != nil {
		t.Fatal(err)
	}
	if yes, err := run.d.applicationV3AgentSubject(ctx, peer); !yes || err != nil {
		t.Fatal("actual native peer failed kept classification", yes, err)
	}
	run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return true, nil }
	// An installation change reaches admission at the next verification.
	run.d.agentImages.forget()
	if yes, err := run.d.applicationV3AgentSubject(ctx, peer); yes || err != nil {
		t.Fatal("known shared gateway received class", yes, err)
	}
	run.d.agentExcluded = func(context.Context, string, string) (bool, error) { return false, nil }
	run.d.agentImages.forget()
	agent1478Register(t, run, "native-agent-alias", program)
	if got := run.d.setAgentEnrollment(ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: "native-agent-alias", Enabled: true}); got.Outcome != applications.OutcomeConflict || got.Reason != "enrollment:native-agent" {
		t.Fatal("same subject received second class owner", got)
	}
	if otherProgram == "" {
		otherProgram = run.f.answerer.Program
	}
	agent1478Register(t, run, "other-agent", otherProgram)
	other := agent1478Set(t, run, "other-agent", "", true)
	otherQ := agent1478Question(t, run, "other-agent")
	answer(t, run.f, otherQ.ID, "keep")
	agent1478Wait(t, run, "other-agent", "enrolled")
	if other.Revision == pending.Revision {
		t.Fatal("two program generations shared")
	}
	if yes, err := run.d.applicationV3AgentSubject(ctx, peer); !yes || err != nil {
		t.Fatal("unrelated agent Keep ended the unchanged native enrollment", yes, err)
	}
	agent1478Set(t, run, "native-agent", pending.Revision, false)
	if yes, err := run.d.applicationV3AgentSubject(ctx, peer); yes || err != nil {
		t.Fatal("other program supplied revoked class", yes, err)
	}
	if call.Recheck() != nil {
		t.Fatal("native peer was lost during inert class controls")
	}
}

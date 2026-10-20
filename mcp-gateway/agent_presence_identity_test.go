//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// Windows only: the pinned expectation names a pipe server's image. The
// identity owner's own tests cover the POSIX socket check.
//
// Re-admission resolves the runtime again on every attempt, and each resolve
// verifies the serving runtime against the pinned expectation. A runtime
// answering at the same endpoint with a different identity is refused before
// any Announce or InboxOpen, so the connection is never re-admitted there.
func TestAgentPresenceReadmitRefusesRuntimeWithDifferentIdentity(t *testing.T) {
	endpoint := fmt.Sprintf(`\\.\pipe\oa-readmit-identity-%d-%d`, os.Getpid(), readmitIdentityPipes.Add(1))
	listener, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			_ = conn.Close()
		}
	}()
	principal, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	// The pin names another runtime image than the process serving here.
	other := listen.ServerExpectation{Principal: identity.User{Kind: "windows", SID: principal.Uid}, Program: filepath.Join(t.TempDir(), "openabstractions.exe")}
	machine := client.NewVerified(endpoint, other)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	attempts := 0
	var refusals []error
	superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
		attempts++
		if attempts == 1 {
			// The original runtime admitted this connection, then was lost.
			ready("original")
			return &errAgentPresenceLost{cause: errors.New("presence lost")}
		}
		err := runNativeAgentPresenceAttempt(ctx, machine, "fixture-agent", "native-kept", ready, status, readmit, stop)
		refusals = append(refusals, err)
		if attempts == 4 {
			cancel()
		}
		return err
	}, status, readmitImmediately, time.Now)
	if attempts != 4 {
		t.Fatal(attempts)
	}
	if status.currentInstance() != "original" {
		t.Fatal("re-admitted under", status.currentInstance())
	}
	if _, _, admitted := status.admission.begin(context.Background()); admitted {
		t.Fatal("tools were admitted against a runtime with another identity")
	}
	for _, refusal := range refusals {
		if !errors.Is(refusal, listen.ErrServerUntrusted) {
			t.Fatal("resolve did not verify the server:", refusal)
		}
	}
	if accepted.Load() < 3 {
		t.Fatal("each attempt must resolve again; connections:", accepted.Load())
	}
}

// readmitIdentityPipes numbers this process's fixture pipes; the process id
// and the counter keep two fixtures apart where the wall clock repeats.
var readmitIdentityPipes atomic.Uint64

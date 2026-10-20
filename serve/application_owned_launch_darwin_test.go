//go:build darwin && cgo

package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestDarwinOwnedOccurrenceMissingFactsRefused(t *testing.T) {
	for _, b := range []ownedApplicationBinding{
		{}, {pid: 1}, {pid: 1, generation: 1, started: time.Now()},
	} {
		if s, err := newDarwinOwnedApplicationSession(b, nil, nil, nil, nil); s != nil || err == nil {
			t.Fatal("incomplete occurrence admitted")
		}
	}
	if _, ok := applicationSession(nil); ok {
		t.Fatal("nil peer supplied audit session")
	}
	if _, ok := applicationSession(&identity.Peer{}); ok {
		t.Fatal("unknown peer supplied audit session")
	}
	p := &ownedApplicationProcess{}
	if err := p.check(context.Background(), ownedApplicationBinding{}); err == nil {
		t.Fatal("closed native occurrence admitted")
	}
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
}

// Inert image callbacks let this control reach the actual retained kernel
// occurrence comparisons. They supply no sealed-resource trust positive.
func TestDarwinOwnedOccurrenceRejectsContradictoryNativeFacts(t *testing.T) {
	self, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	account, err := self.Account()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sleep", "10")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pin, err := identity.BindStartedProcess(cmd.Process)
	if err != nil {
		t.Fatal(err)
	}
	defer pin.Close()
	peer, err := pin.Peer()
	if err != nil {
		t.Fatal(err)
	}
	process, err := peer.Process.AtLeast(identity.ProofBound)
	if err != nil {
		t.Fatal(err)
	}
	base := ownedApplicationBinding{pid: process.PID, generation: process.Generation, account: account, desktop: "darwin:" + strconv.FormatUint(uint64(self.AuditSessionID), 10)}
	image := ownedApplicationImage{program: "/bin/sleep", code: identity.Code{Trusted: true}, proof: identity.ProofBound, recheck: func(context.Context) error { return nil }, close: func() error { return nil }}
	for _, field := range []string{"account", "session", "generation", "pid"} {
		t.Run(field, func(t *testing.T) {
			bad := base
			switch field {
			case "account":
				bad.account += "-wrong"
			case "session":
				bad.desktop += "-wrong"
			case "generation":
				bad.generation++
			case "pid":
				bad.pid++
			}
			p := &ownedApplicationProcess{pin: pin, image: image, binding: bad}
			if err := p.check(context.Background(), bad); !errors.Is(err, identity.ErrPeerMoved) {
				t.Fatalf("contradictory %s: %v", field, err)
			}
		})
	}
}

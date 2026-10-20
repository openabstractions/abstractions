package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// A real native connection supplies the peer. No request or test constructor
// manufactures an identity Attr or substitutes a program string for a process.
func applicationV3NativePeer(t *testing.T) (*identity.Peer, func() error) {
	t.Helper()
	// ReceiveFramed retains its context for subsequent native Recheck calls.
	// Keep that call alive for the fixture, with a separate bound for capture.
	lifetime, cancel := context.WithTimeout(t.Context(), time.Minute)
	t.Cleanup(cancel)
	capture, stopCapture := context.WithTimeout(lifetime, 5*time.Second)
	defer stopCapture()
	endpoint := shortMediationEndpoint(t)
	l, err := listen.ListenFramed(endpoint, listen.Program)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	ready := make(chan *listen.FramedCall)
	failed := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			failed <- err
			return
		}
		call, err := listen.ReceiveFramed(lifetime, conn, listen.Program, ownedApplicationFrameLimit)
		if err != nil {
			conn.Close()
			failed <- err
			return
		}
		select {
		case ready <- call:
		case <-capture.Done():
			call.Close()
		}
	}()
	conn, err := listen.Dial(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err = listen.WriteFrameTo(conn, []byte("{}"), ownedApplicationFrameLimit); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-failed:
		t.Fatal(err)
	case call := <-ready:
		t.Cleanup(func() { call.Close() })
		peer, err := call.Peer()
		if err != nil {
			t.Fatal(err)
		}
		return peer, call.Recheck
	case <-capture.Done():
		cancel()
		t.Fatal(capture.Err())
	}
	return nil, nil
}

func applicationV3PendingSession(t *testing.T, peer *identity.Peer) (*ownedApplicationSession, net.Conn) {
	t.Helper()
	process, err := peer.Process.AtLeast(identity.ProofBound)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		t.Fatal(err)
	}
	desktop, ok := applicationSession(peer)
	if !ok {
		t.Fatal("native peer lacks desktop")
	}
	b := ownedApplicationBinding{application: "fixture", account: subject.Account, desktop: desktop, runtimeEpoch: "runtime1", manifestRevision: "manifest1", imageCode: "held-image", pid: process.PID, started: process.StartTime}
	client, server := net.Pipe()
	s, err := newOwnedApplicationSession(b, client, client, func(context.Context, ownedApplicationBinding) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(); server.Close() })
	return s, server
}

func TestApplicationV3OwnedInstanceBindsActualNativeProcessOnce(t *testing.T) {
	peer, recheck := applicationV3NativePeer(t)
	s, server := applicationV3PendingSession(t, peer)
	ownedFixtureInitialize(t, s, server, "2025-11-25")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.call(ctx, ownedFixtureCall(), ownedFixtureResult, ownedFixtureAuthorize); err == nil {
		t.Fatal("pending instance dispatched")
	}
	if err := s.bindAnnouncedInstance(ctx, peer, "fixture", "i1", recheck); err != nil {
		t.Fatal(err)
	}
	if s.bindingValue().instance != "i1" {
		t.Fatal("actual instance not frozen")
	}
	if err := s.bindAnnouncedInstance(ctx, peer, "fixture", "i1", recheck); err != nil {
		t.Fatal("equal native announcement changed binding", err)
	}
	if err := s.bindAnnouncedInstance(ctx, peer, "fixture", "other", recheck); err == nil {
		t.Fatal("different instance rebound")
	}
}

func TestApplicationV3OwnedInstanceRejectsOtherProcessAndMovedAuthority(t *testing.T) {
	peer, recheck := applicationV3NativePeer(t)
	for _, variant := range []string{"pid", "start", "account", "desktop", "application", "closed"} {
		t.Run(variant, func(t *testing.T) {
			s, _ := applicationV3PendingSession(t, peer)
			switch variant {
			case "pid":
				s.binding.pid++
			case "start":
				s.binding.started = s.binding.started.Add(time.Second)
			case "account":
				s.binding.account = "foreign"
			case "desktop":
				s.binding.desktop = "windows:other"
			case "closed":
				s.Close()
			}
			application := "fixture"
			if variant == "application" {
				application = "other"
			}
			if err := s.bindAnnouncedInstance(context.Background(), peer, application, "i1", recheck); err == nil {
				t.Fatal("moved binding accepted")
			}
			if s.bindingValue().instance != "" {
				t.Fatal("failed bind changed instance")
			}
		})
	}
}

func TestApplicationV3OwnedInstanceConcurrentAnnouncementsFreezeOne(t *testing.T) {
	peer, recheck := applicationV3NativePeer(t)
	s, _ := applicationV3PendingSession(t, peer)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, instance := range []string{"first", "second"} {
		wg.Add(1)
		go func(instance string) {
			defer wg.Done()
			results <- s.bindAnnouncedInstance(context.Background(), peer, "fixture", instance, recheck)
		}(instance)
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d different instances", accepted)
	}
}

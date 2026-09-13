package main

import (
	"context"
	"fmt"
	config "github.com/openabstractions/abstraction-config/go"
	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-identity/listen"
	"os"
	"strings"
	"testing"
	"time"
)

func awaitConfiguration(t *testing.T, s *configurationObservation, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	for {
		if ready() {
			return
		}
		select {
		case <-s.Changes():
		case <-deadline.C:
			t.Fatalf("configuration wait: %s %+v", s.How(), s.Current())
		}
	}
}
func TestPanelConfigurationObservationUsesService(t *testing.T) {
	own(t)
	if err := config.Save(config.UserPath(), config.Config{Store: "existing-record", Off: map[string]string{"other": "keep"}}); err != nil {
		t.Fatal(err)
	}
	stopRuntime := panelConfigRuntime(t)
	s := observeConfiguration(context.Background(), 10*time.Millisecond)
	defer s.Close()
	awaitConfiguration(t, s, func() bool { return s.Error() == nil && s.Current().Store == "existing-record" })
	if s.Current().Origin("store").Rung != config.User {
		t.Fatal("provenance lost")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	editor, err := panelMachine().ResolveConfigEditor(ctx, facade.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	old, err := editor.ReadUserContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values := old.Values
	values.Store = "changed-service-record"
	applied, err := editor.ReplaceUserContext(ctx, old.Revision, values)
	if err != nil || applied.Outcome != wire.UserReplaceOutcomeApplied {
		t.Fatalf("edit: %+v %v", applied, err)
	}
	stale, err := editor.ReplaceUserContext(ctx, old.Revision, old.Values)
	if err != nil || stale.Outcome != wire.UserReplaceOutcomeConflict {
		t.Fatalf("conflict: %+v %v", stale, err)
	}
	awaitConfiguration(t, s, func() bool { return s.Current().Store == "changed-service-record" })
	if s.Current().Off["other"] != "keep" {
		t.Fatal("existing record lost")
	}
	stopRuntime()
	awaitConfiguration(t, s, func() bool { return s.Error() != nil })
	if s.Current().Store != "changed-service-record" || !strings.Contains(s.How(), "unavailable") {
		t.Fatal("failed refresh lost snapshot or hid error")
	}
}
func TestPanelConfigurationObservationAbsenceAndClose(t *testing.T) {
	own(t)
	if err := config.Save(config.UserPath(), config.Config{Store: "must-not-read-locally"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", listen.Endpoint(fmt.Sprintf("absent-initial-%d", os.Getpid())))
	s := observeConfiguration(context.Background(), 10*time.Millisecond)
	defer s.Close()
	awaitConfiguration(t, s, func() bool {
		return strings.Contains(s.How(), "unavailable") && !strings.Contains(s.How(), "has not answered")
	})
	if s.Current().Store != "" {
		t.Fatal("client read local provider files")
	}
	start := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("Close did not cancel observation")
	}
}

func TestPanelConfigurationCloseCancelsStalledRead(t *testing.T) {
	own(t)
	endpoint := listen.Endpoint(fmt.Sprintf("stalled-config-%d", os.Getpid()))
	listener, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan listen.Conn, 1)
	go func() {
		c, err := listener.Accept()
		if err == nil {
			accepted <- c
		}
	}()
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", endpoint)
	trustPanelRuntime(t, endpoint)
	s := observeConfiguration(context.Background(), time.Second)
	defer s.Close()
	select {
	case c := <-accepted:
		defer c.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("probe never connected")
	}
	done := make(chan struct{})
	go func() { s.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close left the read blocked")
	}
}

func TestPanelConfigurationSlowConsumerReceivesLatest(t *testing.T) {
	own(t)
	if err := config.Save(config.UserPath(), config.Config{Store: "queued-old"}); err != nil {
		t.Fatal(err)
	}
	panelConfigRuntime(t)
	s := observeConfiguration(context.Background(), 10*time.Millisecond)
	defer s.Close()
	// Deliberately leave Changes unread while two service snapshots arrive.
	waitCurrent := func(want string) {
		t.Helper()
		deadline := time.NewTimer(4 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for s.Current().Store != want {
			select {
			case <-deadline.C:
				t.Fatalf("snapshot %q: %s", want, s.How())
			case <-tick.C:
			}
		}
	}
	waitCurrent("queued-old")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	editor, err := panelMachine().ResolveConfigEditor(ctx, facade.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	old, err := editor.ReadUserContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	values := old.Values
	values.Store = "latest"
	result, err := editor.ReplaceUserContext(ctx, old.Revision, values)
	if err != nil || result.Outcome != wire.UserReplaceOutcomeApplied {
		t.Fatalf("edit: %+v %v", result, err)
	}
	waitCurrent("latest")
	select {
	case got := <-s.Changes():
		if got.Store != "latest" {
			t.Fatalf("slow consumer received stale %q", got.Store)
		}
	case <-time.After(time.Second):
		t.Fatal("latest snapshot lost")
	}
}

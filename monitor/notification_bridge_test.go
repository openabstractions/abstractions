package main

import (
	"strings"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// ASK-N7 on macOS: the launcher receives one notification frame with the
// sender as its own field, each action's prepared click arguments and OA's
// Turn off last; removal names the record; replies carry a click's
// arguments or a closed record back, and malformed ones are refused.
func TestBridgeNotificationFramesAndReplies(t *testing.T) {
	bridge, frames := nativeBridgeFixture(t)
	surface := bridgeNotificationSurface{bridge: bridge}
	n := shownNotification{ID: "a1", Token: "t1", Title: "Done", Body: "ready", Sender: "Pretend Lemonade",
		Actions: []askclient.NotificationAction{{ID: "copy", Title: "Copy now", Activation: askclient.NotificationActivationApp}}}
	if err := surface.Show(n); err != nil {
		t.Fatal(err)
	}
	frame := nextNativeFrame(t, frames, "notification")
	got := frame.Notification
	if frame.ID != "a1" || got == nil || got.Sender != "Pretend Lemonade" || len(got.Actions) != 2 || !strings.Contains(got.Actions[1].Title, "Turn off") {
		t.Fatalf("%+v", frame)
	}
	if click, ok := parseNotificationClick(got.Actions[0].Args); !ok || click.Action != "copy" || click.Token != "t1" {
		t.Fatal(click, ok)
	}
	if click, ok := parseNotificationClick(got.Actions[1].Args); !ok || !click.TurnOff {
		t.Fatal(click, ok)
	}
	if click, ok := parseNotificationClick(got.Open); !ok || click.Action != "" {
		t.Fatal(click, ok)
	}
	if err := surface.Remove("a1", ""); err != nil {
		t.Fatal(err)
	}
	if removed := nextNativeFrame(t, frames, "notification-remove"); removed.ID != "a1" {
		t.Fatal(removed)
	}
	clicked, closed := make(chan string, 1), make(chan string, 1)
	bridge.onNotification = func(args string) { clicked <- args }
	bridge.onNotificationClosed = func(id string) { closed <- id }
	if err := bridge.accept(nativeAsksReply{Kind: "notification-click", Args: got.Actions[0].Args}, nil); err != nil {
		t.Fatal(err)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "notification-closed", ID: "a1"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, ch := range []chan string{clicked, closed} {
		select {
		case <-ch:
		case <-time.After(time.Second):
			t.Fatal("reply not delivered")
		}
	}
	for _, bad := range []nativeAsksReply{{Kind: "notification-click"}, {Kind: "notification-click", Args: "x", ID: "a1"}, {Kind: "notification-closed"}, {Kind: "answer", ID: "1", Option: "allow", Args: "x"}} {
		if err := bridge.accept(bad, nil); err == nil {
			t.Fatalf("malformed reply accepted: %+v", bad)
		}
	}
}

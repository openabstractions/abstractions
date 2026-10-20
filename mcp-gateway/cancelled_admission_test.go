package main

import (
	"context"
	"testing"
	"time"
)

func cancelledConfirmedAdmission() *agentToolAdmission {
	return &agentToolAdmission{supervised: true, ready: true, admitted: true, deadline: time.Now().Add(time.Minute)}
}

func TestAwaitAdmissionCancelledContextReturnsFalse(t *testing.T) {
	a := cancelledConfirmedAdmission()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan bool, 1)
	go func() { done <- a.awaitAdmission(ctx, time.Now().Add(time.Minute)) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("awaitAdmission returned true for a cancelled context")
		}
	case <-time.After(time.Second):
		t.Fatal("awaitAdmission did not return within 1s")
	}
}

func TestChatBodyBackendCancelledContextReturnsFalse(t *testing.T) {
	receiver := &messagesBackend{status: &messagesInboxStatus{admission: cancelledConfirmedAdmission()}}
	b := &messagesBackend{requireChat: true, chatReceiver: receiver}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), agentAdmissionUntilKey{}, time.Now().Add(time.Minute)))
	cancel()
	done := make(chan bool, 1)
	go func() { _, ok := b.chatBodyBackend(ctx); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("chatBodyBackend returned true for a cancelled context")
		}
	case <-time.After(time.Second):
		t.Fatal("chatBodyBackend did not return within 1s")
	}
}

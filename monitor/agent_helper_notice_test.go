package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAgentHelperUpdateLoginDelegatesOneNativeBatch(t *testing.T) {
	calls := 0
	n := agentHelperNotices{batch: func(context.Context) error { calls++; return nil }}
	if err := raiseAgentHelperUpdatesAtLogin(context.Background(), n, time.Millisecond); err != nil || calls != 1 {
		t.Fatal(calls, err)
	}
}

func TestAgentHelperUpdateLoginRetriesRuntimeStartup(t *testing.T) {
	calls := 0
	n := agentHelperNotices{batch: func(context.Context) error {
		calls++
		if calls < 2 {
			return errors.New("runtime starting")
		}
		return nil
	}}
	if err := raiseAgentHelperUpdatesAtLogin(context.Background(), n, time.Millisecond); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
}

func TestAgentHelperUpdateLoginWaitsForRestartWithSameIntent(t *testing.T) {
	calls := 0
	n := agentHelperNotices{batch: func(context.Context) error {
		calls++
		if calls <= 5 {
			return &agentConnectionUpdatePending{Intent: "retained-intent", RestartRequired: true, Err: errors.New("active image")}
		}
		return nil
	}}
	if err := raiseAgentHelperUpdatesAtLogin(context.Background(), n, time.Millisecond); err != nil || calls != 6 {
		t.Fatal(calls, err)
	}
}

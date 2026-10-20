package main

import (
	"context"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
)

func TestAgentEnrollmentPendingReachesNativeBridgeWithoutAnswer(t *testing.T) {
	program := platformProgram(`C:\OA\setup\oa-codex-integration.exe`)
	agentConsentPendingNativeBridge(t, asks.AgentEnrollmentKey, map[string]string{"program": program, "title": "Codex integration"}, "keep")
}

func TestAgentUpdatePendingReachesNativeBridgeWithoutAnswer(t *testing.T) {
	for _, title := range []string{"Claude integration and Codex integration", "Codex integration", "Claude integration"} {
		t.Run(title, func(t *testing.T) {
			agentConsentPendingNativeBridge(t, asks.AgentUpdateKey, map[string]string{"members": title, "membersmore": "", "memberslast": "", "code": strings.Repeat("c", 48)}, "update")
		})
	}
}

func agentConsentPendingNativeBridge(t *testing.T, key string, slots map[string]string, choice string) {
	t.Helper()
	book, _ := trayTestBook(t)
	scope := "agent-enrollment:" + strings.Repeat("a", 64)
	record, outcome, err := book.AskApplicationFrom(scope, "fixture-runtime", "agent-enrollment:"+strings.Repeat("b", 48), key, slots, listen.Seen{Why: "explicit operator request for person-kept native agent enrollment"}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	fresh, operator, unavailable := pollTray(context.Background(), panelMachine(), newTrayAnnouncer())
	if unavailable || operator == nil || len(fresh) != 1 || fresh[0].ID != record.ID {
		t.Fatalf("pending enrollment not presented: unavailable=%t records=%d", unavailable, len(fresh))
	}
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	presenter := &questionPresenter{session: bridge, surface: bridge, operator: func(context.Context) (presenterOperator, error) { return operator, nil }, account: probe.Self().Account, budget: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan presentResult, 1)
	go func() { done <- presenter.present(ctx, fresh[0]) }()
	shown := nextNativeFrame(t, frames, "question")
	if shown.Prompt == nil || !shown.Prompt.PermissionPrompt || !strings.Contains(shown.Prompt.Details, record.ID) {
		t.Fatal("native frame lost exact enrollment occurrence")
	}
	keep := false
	for _, item := range shown.Prompt.Choices {
		if item.Option == choice {
			keep = true
		}
	}
	if !keep {
		t.Fatal("native frame lost Keep choice")
	}
	if key == asks.AgentUpdateKey {
		choices := shown.Prompt.Choices
		if len(choices) != 2 || choices[0].Option != "update" || choices[0].Label != "Update" || choices[1].Option != "not_now" || choices[1].Label != "Not now" || shown.Prompt.Default != "not_now" {
			t.Fatal("native batch frame lost exact service choices or safe default", shown.Prompt)
		}
		if shown.Prompt.Instruction != "Update OpenAbstractions in "+strings.ReplaceAll(slots["members"], " integration", "")+"?" || strings.Contains(shown.Prompt.Instruction+shown.Prompt.Content, "Request ") {
			t.Fatal("native frame lost named connections", shown.Prompt.Instruction)
		}
		for _, name := range []string{"Claude", "Codex"} {
			if !strings.Contains(slots["members"], name) && strings.Contains(shown.Prompt.Instruction, name) {
				t.Fatal("single member prompt named absent peer", shown.Prompt.Instruction)
			}
		}
	}
	if err := bridge.accept(nativeAsksReply{Kind: "close", ID: shown.ID}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.Outcome != presentClosed {
			t.Fatal(result)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, found, err := book.ApplicationRecord(record.ID)
	if err != nil || !found || !current.Pending() || current.Option != "" || current.AnsweredBy != "" || !current.Answered.IsZero() {
		t.Fatal("closing bridge recorded an answer", found, err)
	}
}

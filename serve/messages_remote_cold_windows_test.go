//go:build windows

package main

import (
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"sync/atomic"
	"testing"
)

func TestMessagesPairedCanonicalColdStartUsesPinnedDirectionAndReceiverOwner(t *testing.T) {
	f := pairedMessagesFixture(t)
	f.permit(t, 0, "app:other-agent")
	h := pairedAttachReceiver(t, f)
	run := f.nodes[1].run
	descriptor, _, err := h.directory.agentSnapshot(t.Context(), "other-agent")
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	profile := startprofile.Profile{Name: "claude-v1", Context: "ctx", Folder: folder, StateDir: folder, CLIFileID: "inert", Model: "inert", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 1}
	// The real admission rechecks native enrollment, pairing and CAS state
	// before the inert launcher observes its request on Windows under -race.
	activation := &wire.ApplicationActivationRecipe{Arguments: profile.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 5000}
	if out := h.directory.setActivation(t.Context(), run.f.panel, applications.ActivationConfiguration{Expected: descriptor, Activation: activation}, nil); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Cwd: folder, Origin: msg.ChatOriginDesktop, Mode: msg.ChatModeAcceptEdits}
	canonical, err := h.messages.contextForChat(t.Context(), h.directory.owner, descriptor.Program, launch.Session, &msg.ChatBinding{ChatID: launch.Session, Launch: launch})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var observed atomic.Pointer[applicationMessageStartRequest]
	h.directory.launchMessageStart = func(ctx context.Context, r applicationMessageStartRequest, authority func(context.Context) error) error {
		if err := authority(ctx); err != nil {
			return err
		}
		observed.Store(&r)
		calls.Add(1)
		return errors.New("inert fixture does not launch a vendor")
	}
	instance, source := f.sender(t, 0)
	request := pairedAttachQuestion(f, "canonical-cold")
	request.Context = canonical
	sent, err := f.nodes[0].reader.Send(instance, source, request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	record, key, err := h.messages.readByID(t.Context(), sent.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	if err := h.progressMessageStartSettled(t.Context(), key, record); err != nil {
		t.Fatal(err)
	}
	question := pairedStartQuestion(t, f)
	if question.ID == "" || calls.Load() != 0 {
		t.Fatal("paired cold start preceded person direction permit", question, calls.Load())
	}
	resource := question.Permission.Targets[0].Resource
	if err := h.messageRights.policy.Set(rights.Subject{Account: h.owner, Program: f.nodes[0].self.Program}, grants.ApplicationMessageStartAction, resource, true); err != nil {
		t.Fatal(err)
	}
	if err := h.progressMessageStartSettled(t.Context(), key, record); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("claimed native program permitted paired start")
	}
	pinned := rights.Subject{Account: h.owner, Program: "remote:" + f.keys[0]}
	if err := h.messageRights.policy.Set(pinned, grants.ApplicationMessageStartAction, resource, true); err != nil {
		t.Fatal(err)
	}
	if err := h.progressMessageStartSettled(t.Context(), key, record); err != nil {
		t.Fatal(err)
	}
	selected := observed.Load()
	if calls.Load() != 1 || selected == nil || selected.Cold == nil || selected.Cold.Context != canonical || selected.Account != h.directory.owner {
		t.Fatal("paired canonical selection", calls.Load(), selected)
	}
	if err := h.messages.checkColdResume(t.Context(), h.directory.owner, descriptor.Program, selected.Cold); err != nil {
		t.Fatal(err)
	}
	rows, err := h.messages.retainedChats(t.Context(), h.directory.owner, descriptor.Program)
	if err != nil || len(rows) != 1 || rows[0].Launch == nil || *rows[0].Launch != *launch {
		t.Fatal("paired start changed retained facts", rows, err)
	}
}

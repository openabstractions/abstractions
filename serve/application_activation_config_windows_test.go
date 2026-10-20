package main

import (
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"reflect"
	"testing"
)

func TestActivationConfigurationPreservesActualLiveInbox(t *testing.T) {
	run, _ := messages1608Fixture(t)
	call, _ := messages1608Call(t)
	instance := messages1608Presence(t, run, call, "", "ctx")
	opened := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready")
	if opened.outcome != "opened" {
		t.Fatal(opened)
	}
	row := messages1608Row(t, run.d)
	before := run.d.currentAgentEnrollment(run.ctx, "native-agent")
	descriptor, _, err := run.d.agentSnapshot(run.ctx, "native-agent")
	if err != nil {
		t.Fatal(err)
	}
	request := applications.ActivationConfiguration{Expected: descriptor, Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"fixed"}, Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 1000}}
	for _, recipe := range []*wire.ApplicationActivationRecipe{request.Activation, nil} {
		request.Activation = recipe
		if out := run.d.setActivation(run.ctx, run.f.panel, request, nil); out.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(out)
		}
		if after := run.d.currentAgentEnrollment(run.ctx, "native-agent"); !reflect.DeepEqual(before, after) {
			t.Fatal(before, after)
		}
		currentRow := messages1608Row(t, run.d)
		owned, ownerErr := run.d.withNativeInboxOwner(run.ctx, call, instance, "ctx", nil)
		if !owned || ownerErr != nil || currentRow == nil || currentRow.pin != row.pin || currentRow.revision != row.revision || currentRow.instance != row.instance {
			t.Fatalf("activation edit retired live inbox: owned=%v err=%v row=%v", owned, ownerErr, currentRow != nil)
		}
		if again := run.d.openNativeInbox(run.ctx, call, instance, "ctx", "ready"); again.outcome != "opened" || again.revision != opened.revision {
			t.Fatal("activation edit changed live occurrence", again)
		}
		// Ordinary renewal replaces its independent native pin. The next
		// configuration must preserve that freshly renewed occurrence.
		row = messages1608Row(t, run.d)
		request.Expected, _, err = run.d.agentSnapshot(run.ctx, "native-agent")
		if err != nil {
			t.Fatal(err)
		}
	}
}

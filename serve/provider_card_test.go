package main

import (
	"context"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// A GPU provider declared with "provider add --card N" names card:N, and the
// registry writes its program the hold rules a card holder needs: the card
// and the wake lease it takes while GPU work runs (resource RES-A1, RES-G5).
// On 2026-10-02 a video provider declared through "provider add" could name
// no card, and its wake lease was refused at every video start
// (research/live-runs/2026-10-02.md, phase 2 finding 3).
func TestProviderAddCardWritesTheCardAndWakeHoldRules(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}

	program := filepath.Join(t.TempDir(), "video-provider")
	if out, err := runProvider(t, append([]string{"add", "video-provider", "--program", program, "--provider-endpoint", "video-provider",
		"--contract", inference.VideoContract, "--profiles", "video", "--model", "video:clip-model", "--activate", "attach", "--card", "0"}, endpoint...)...); err != nil {
		t.Fatalf("provider add --card 0: %v\n%s", err, out)
	}
	if !strings.Contains(providerListRow(t, endpoint, "video-provider"), "card:0") {
		t.Fatal("the declaration does not name card:0")
	}

	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	held := rights.Subject{Account: account.Uid, Program: filepath.Clean(program)}
	for _, resource := range []string{instrument.Card0, resourceservice.ResourceAwake} {
		read, err := operator.ReadRuleContext(call, held, resourceservice.ActionHold, resource)
		if err != nil || read.Outcome.String() != "found" || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
			t.Fatalf("abstraction.resource/hold on %s for the provider declared with --card: %+v %v", resource, read, err)
		}
	}

	for _, bad := range []string{"-1", "x", "01"} {
		if out, err := runProvider(t, append([]string{"add", "bad-card", "--program", program, "--provider-endpoint", "bad-card",
			"--contract", inference.VideoContract, "--activate", "attach", "--card", bad}, endpoint...)...); err == nil {
			t.Fatalf("provider add --card %s was accepted:\n%s", bad, out)
		}
	}
}

func providerListRow(t *testing.T, endpoint []string, name string) string {
	t.Helper()
	out, err := runProvider(t, append([]string{"list"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider list: %v\n%s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, name+" ") {
			return line
		}
	}
	t.Fatalf("provider list has no row %s:\n%s", name, out)
	return ""
}

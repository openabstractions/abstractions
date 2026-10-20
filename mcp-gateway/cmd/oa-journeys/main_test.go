//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-identity/placetest"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/personfolders"
)

func TestMain(m *testing.M) { os.Exit(personfolders.Main(m)) }

// A check awaiting a fix branch fails its journey once the checkout has the
// branch, and reads as an expected failure while it lacks it.
func TestAwaitingCheckFailsOnlyOnceItsBranchIsMerged(t *testing.T) {
	t.Cleanup(func() { unmerged = map[string]bool{} })
	run := func() result {
		r := result{Journey: "J-X"}
		r.check("ran", true, "")
		r.awaiting("claude/fix", "fixed", false, "")
		r.finish()
		return r
	}
	unmerged = map[string]bool{"claude/fix": true}
	if r := run(); !r.Pass || !strings.HasPrefix(summaryLine(r), "J-X       XFAIL (until claude/fix merges)") {
		t.Fatalf("unmerged: pass=%v %q", r.Pass, summaryLine(r))
	}
	unmerged = map[string]bool{}
	if r := run(); r.Pass || !strings.Contains(summaryLine(r), "FAIL") {
		t.Fatalf("merged: pass=%v %q", r.Pass, summaryLine(r))
	}
}

// Browsers and the compositor never block a cold journey; a model loader
// blocks above 2 GiB and any other process above 4 GiB.
func TestGPUHoldersSkipBrowsersAndCountModelLoaders(t *testing.T) {
	used := parseGPUCounters("pid_10_luid_0x1_phys_0 2362232012\npid_11_luid_0x1_phys_0 3000000000\npid_12_luid_0x1_phys_0 3000000000\npid_13_luid_0x1_phys_0 5000000000\npid_14_luid_0x1_phys_0 9000000000\n")
	names := map[int]string{10: "msedge.exe", 11: "python.exe", 12: "Game.exe", 13: "Game.exe", 14: "dwm.exe"}
	var logged string
	busy := gpuBusy(used, names, nil, func(f string, a ...any) { logged = fmt.Sprintf(f, a...) })
	if busy != "python.exe (pid 11) holds 2.8 GiB; Game.exe (pid 13) holds 4.7 GiB" {
		t.Fatalf("busy %q", busy)
	}
	if !strings.Contains(logged, "msedge.exe (pid 10) holds 2.2 GiB, skipped") || !strings.Contains(logged, "Game.exe (pid 12) holds 2.8 GiB, under 4 GiB") {
		t.Fatalf("log %q", logged)
	}
}

// A check awaiting an architect ruling reads as XFAIL with the ruling.
func TestRulingCheckIsExpected(t *testing.T) {
	r := result{Journey: "J-X"}
	r.check("ran", true, "")
	r.awaitingRuling("the architect rules", "listed", false, "")
	r.finish()
	if !r.Pass || !strings.HasPrefix(summaryLine(r), "J-X       XFAIL (until the architect rules)") {
		t.Fatalf("pass=%v %q", r.Pass, summaryLine(r))
	}
}

func TestJourneyUsesClosedAsksOutcomes(t *testing.T) {
	reply := map[string]any{}
	if err := json.Unmarshal(rightswire.EncodeDecision(&rightswire.Decision{Outcome: rightswire.DecisionOutcomePermitted}), &reply); err != nil {
		t.Fatal(err)
	}
	if !enrollmentPermitted(reply) {
		t.Fatal("a permitted native decision was not read")
	}
	if enrollmentPermitted(map[string]any{"outcome": "PERMITTED"}) ||
		enrollmentPermitted(map[string]any{"notice": `{"outcome":"permitted"}`}) {
		t.Fatal("a noncanonical outcome or quoted notice confirmed enrollment")
	}
	reply = nil
	denied := askswire.ResponderDecision{Outcome: askswire.OperatorDecisionOutcomeForbidden, Effect: askswire.ResponderEffectOutcomeNone}
	if err := json.Unmarshal(askswire.EncodeResponderDecision(&denied), &reply); err != nil {
		t.Fatal(err)
	}
	if asksAnswerOutcome(reply) != denied.Outcome || asksAnswerOutcome(map[string]any{"outcome": "FORBIDDEN"}).Known() {
		t.Fatal("a responder outcome did not use the native enum")
	}
}

func TestJourneyCardLeaseNamesTheSelectedProgramOnTheSelectedCard(t *testing.T) {
	root := t.TempDir()
	selected, foreign := filepath.Join(root, "modelhostd.exe"), filepath.Join(root, "other-modelhostd.exe")
	for _, file := range []string{selected, foreign} {
		if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	alias := filepath.Join(root, "alias.exe")
	if err := os.Link(selected, alias); err != nil {
		t.Fatal(err)
	}
	report := fmt.Sprintf(`{"resources":[
		{"resource":"card:1","holders":[{"program":%q,"lease":"other-card"}]},
		{"resource":"card:0","holders":[{"program":%q,"lease":"foreign"},{"program":%q,"lease":"selected"}]}
	]}`, selected, foreign, alias)
	if got := reportedCardLease(report, selected); got != "selected" {
		t.Fatalf("lease = %q, want selected", got)
	}
	raw := fmt.Sprintf(`{"resources":[{"resource":"card:0","holders":[{"program":%q,"lease":"foreign"}]}]}`, foreign)
	if got := reportedCardLease(raw, selected); got != "" {
		t.Fatalf("a similarly named foreign program matched: %q", got)
	}
}

func TestJourneyModelCacheRequiresSelectedFileIdentity(t *testing.T) {
	root := t.TempDir()
	source, foreign := filepath.Join(root, "source.gguf"), filepath.Join(root, "foreign.gguf")
	link := filepath.Join(root, "cached.gguf")
	for _, file := range []string{source, foreign} {
		if err := os.WriteFile(file, []byte("same model bytes"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := linkJourneyChatModel(source, link); err != nil {
		t.Fatal(err)
	}
	if err := linkJourneyChatModel(source, link); err != nil {
		t.Fatalf("same model could not reuse its own hard link: %v", err)
	}
	if err := linkJourneyChatModel(foreign, link); err == nil {
		t.Fatal("aliased cache name supplied identity to a foreign model")
	}
}

func TestComfyProcessesUseTheFileIdentityOfTheExactImage(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(comfyProcesses(program), os.Getpid()) {
		t.Fatal("the current process image was not found")
	}
	foreign := filepath.Join(t.TempDir(), filepath.Base(program))
	if err := os.WriteFile(foreign, []byte("different file with the same image name"), 0o600); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(comfyProcesses(foreign), os.Getpid()) {
		t.Fatal("the same name in another directory matched the current image")
	}
}

func TestConnectFixtureFolderIdentityAndContainment(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	person := filepath.Join(parent, "home")
	local := filepath.Join(person, "Local")
	roaming := filepath.Join(person, "Roaming")
	foreign := filepath.Join(parent, "home-other", "Local")
	for _, dir := range []string{local, roaming, foreign} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if !connectFixtureFolders(ctx, person, local, roaming) || connectFixtureFolders(ctx, person, foreign, roaming) {
		t.Fatal("fixture containment accepted the wrong folder")
	}
	alias := filepath.Join(person, "Link")
	if err := placetest.Junction(alias, foreign); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(alias) })
	if connectFixtureFolders(ctx, person, alias, roaming) {
		t.Fatal("fixture containment followed a link out of the fixture home")
	}
	if !sameConnectFolder(ctx, alias, foreign) || sameConnectFolder(ctx, alias, local) {
		t.Fatal("Catalog folder comparison lost the identity of the link target")
	}
}

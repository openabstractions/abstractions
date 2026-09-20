package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	content "github.com/openabstractions/abstraction-storage/go/abstraction/storage/content"
)

// providerFixtureArg makes this test binary a fixture provider:
// <binary> -oa-provider-fixture <chat|chat-delayed|inventory> <endpoint name> <pid directory>.
const providerFixtureArg = "-oa-provider-fixture"
const mediatedAppFixtureArg = "-oa-mediated-app-fixture"
const mediatedOperationFixtureArg = "-oa-mediated-operation-fixture"

// fixtureGuarantee is a guarantee only the fixture provider advertises, so a
// resolution requiring it passes over the runtime's own chat@1.
const fixtureGuarantee = "openabstractions-test/fixture-provider@1"

func init() {
	if (len(os.Args) == 5 || len(os.Args) == 6) && os.Args[1] == providerFixtureArg {
		broker := ""
		if len(os.Args) == 6 {
			broker = os.Args[5]
		}
		os.Exit(runProviderFixture(os.Args[2], os.Args[3], os.Args[4], broker))
	}
	if (len(os.Args) == 5 || len(os.Args) == 6) && os.Args[1] == mediatedAppFixtureArg {
		claim := ""
		if len(os.Args) == 6 {
			claim = os.Args[5]
		}
		os.Exit(runMediatedAppFixture(os.Args[2], os.Args[3], os.Args[4], claim))
	}
	if len(os.Args) == 5 && os.Args[1] == mediatedOperationFixtureArg {
		os.Exit(runMediatedOperationFixture(os.Args[2], os.Args[3], os.Args[4]))
	}
}

func runMediatedOperationFixture(endpoint, operation, resultPath string) int {
	client := iwire.NewChatClient(listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second, MaxFrame: 1 << 20})
	page, observeErr := client.Observe(operation, 0, 256, 65536, 0)
	cancelled, cancelErr := client.Cancel(operation)
	result := struct {
		Observe iwire.PageOutcome   `json:"observe"`
		Cancel  iwire.CancelOutcome `json:"cancel"`
		Error   bool                `json:"error"`
	}{Observe: page.Outcome, Cancel: cancelled.Outcome, Error: observeErr != nil || cancelErr != nil}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(resultPath, raw, 0o600); err != nil {
		return 13
	}
	return 0
}

// runProviderFixture serves one contract on the endpoint until killed, for at
// most two minutes: chat serves chat@1 through its generated dispatcher, and
// old answers every call unknown_service, as a provider built before
// endpoint@1. It records its pid in dir; a relaunch waits a second before
// listening, which keeps a restart observable.
func runProviderFixture(kind, endpoint, dir, brokerProgram string) int {
	entries, _ := os.ReadDir(dir)
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(os.Getpid())), nil, 0o600); err != nil {
		return 3
	}
	if len(entries) > 0 {
		time.Sleep(time.Second)
	}
	l, err := listen.Listen(listen.Endpoint(endpoint))
	if err != nil {
		return 4
	}
	time.AfterFunc(2*time.Minute, func() { os.Exit(5) })
	for {
		conn, err := l.Accept()
		if err != nil {
			return 6
		}
		go func() {
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err != nil {
				return
			}
			peer, err := call.Peer()
			if err != nil {
				return
			}
			caller, err := identity.SubjectProgram(peer, listen.Program.Path)
			if err != nil || brokerProgram != "" && !samePrograms(caller, brokerProgram) {
				return
			}
			if kind == "old" {
				service, _ := iwire.ServiceName(call.Frame)
				call.Reply([]byte(`{"version":1,"service":` + strconv.Quote(service) + `,"method":"Describe","ok":false,"payload":{"code":"unknown_service","message":""}}`))
				return
			}
			var reply []byte
			switch kind {
			case "chat", "chat-delayed":
				reply, err = (&iwire.ChatDispatcher{Handler: fixtureChat{calls: filepath.Join(dir, "calls"), delayed: kind == "chat-delayed"}}).ExchangeFrame(call.Frame)
			case "inventory":
				reply, err = (&content.InventorySourceDispatcher{Handler: fixtureInventory{}}).ExchangeFrame(call.Frame)
			default:
				return
			}
			if err == nil {
				call.Reply(reply)
			}
		}()
	}
}

// runMediatedAppFixture proves that a separately named application can call
// the OA endpoint while the declared provider refuses that application path.
func runMediatedAppFixture(oaEndpoint, providerEndpoint, resultPath, claim string) int {
	req := iwire.Request{Model: "fixture-model", Extensions: map[string]string{inference.ClaimExtension: claim},
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "hello"}}}}}
	_, directErr := iwire.NewChatClient(listen.FrameClient{Endpoint: providerEndpoint, Timeout: 2 * time.Second, MaxFrame: 1 << 20}).Start(req)
	client := iwire.NewChatClient(listen.FrameClient{Endpoint: oaEndpoint, Timeout: 5 * time.Second, MaxFrame: 1 << 20})
	admission, err := client.Start(req)
	if err != nil {
		return 10
	}
	result := struct {
		DirectRefused bool                `json:"direct_refused"`
		StartOutcome  iwire.StartOutcome  `json:"start_outcome"`
		Outcome       *iwire.ReplyOutcome `json:"outcome,omitempty"`
		Operation     string              `json:"operation"`
	}{DirectRefused: directErr != nil, StartOutcome: admission.Outcome, Operation: admission.Operation}
	if admission.Outcome == iwire.StartOutcomeAccepted {
		cursor := int64(0)
		deadline := time.Now().Add(10 * time.Second)
		var end *iwire.Reply
		terminal := false
		for time.Now().Before(deadline) {
			page, err := client.Observe(admission.Operation, cursor, 256, 65536, 1000)
			if err != nil || page.Outcome != iwire.PageOutcomePage {
				return 11
			}
			for _, delta := range page.Deltas {
				if delta.End != nil {
					end = delta.End
				}
			}
			cursor = page.Next
			if page.AtEnd {
				terminal = true
				break
			}
		}
		if end == nil || !terminal {
			return 11
		}
		outcome := end.Outcome
		result.Outcome = &outcome
	}
	raw, _ := json.Marshal(result)
	if err := os.WriteFile(resultPath, raw, 0o600); err != nil {
		return 12
	}
	return 0
}

type fixtureChat struct {
	calls   string
	delayed bool
}

type fixtureInventory struct{}

func (fixtureInventory) Describe() (content.SourceDescription, error) {
	stores := []content.Store{
		{Name: "ollama", Program: "ollama", Rule: "fixture", Errors: []content.StoreError{}, Placement: content.PlacementLocal},
		{Name: "huggingface", Program: "huggingface", Rule: "fixture", Errors: []content.StoreError{}, Placement: content.PlacementLocal},
	}
	return content.SourceDescription{Outcome: content.SourceDescriptionOutcomeDescribed, Name: "fixture", Stores: stores,
		Schemes: []string{}, Capabilities: []string{}}, nil
}

func unavailableSourcePage() content.SourcePage {
	return content.SourcePage{Outcome: content.SourcePageOutcomeUnavailable, Stores: []content.Store{}, Manifests: []content.ManifestHolders{},
		Objects: []content.ObjectHolders{}, Dangling: []content.Dangling{}, Changes: []content.SourceChange{}}
}

func (fixtureInventory) Snapshot(string, int64) (content.SourcePage, error) {
	return unavailableSourcePage(), nil
}
func (fixtureInventory) Observe(string, int64, int64) (content.SourcePage, error) {
	return unavailableSourcePage(), nil
}
func (fixtureInventory) Verify(string) (content.SourcePage, error) {
	return unavailableSourcePage(), nil
}
func (fixtureInventory) Remove(string) (content.RemoveResult, error) {
	return content.RemoveResult{Outcome: content.RemoveOutcomeUnsupported, Holders: []content.Holder{}}, nil
}

func (f fixtureChat) Start(iwire.Request) (iwire.Admission, error) {
	file, _ := os.OpenFile(f.calls, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if file != nil {
		_, _ = file.WriteString("start\n")
		_ = file.Close()
	}
	return iwire.Admission{Outcome: iwire.StartOutcomeAccepted, Operation: "downstream-fixture", Host: "leaf", Model: "fixture-model", RetentionMs: 60000, IdleMs: 10000, RetainedDeltas: 16}, nil
}
func (f fixtureChat) Observe(operation string, cursor, _, _, _ int64) (iwire.DeltaPage, error) {
	if operation != "downstream-fixture" {
		return iwire.DeltaPage{Outcome: iwire.PageOutcomeUnknown, Deltas: []iwire.Delta{}, Next: cursor}, nil
	}
	if f.delayed {
		time.Sleep(2500 * time.Millisecond)
	}
	part := iwire.Part{Kind: iwire.PartKindText, Text: "mediated"}
	end := iwire.Reply{Outcome: iwire.ReplyOutcomeCompleted, StopReason: iwire.StopReasonEnd, Message: iwire.Message{Role: iwire.RoleAssistant, Parts: []iwire.Part{part}}, Host: "leaf", Model: "fixture-model"}
	return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{{Sequence: 0, Kind: iwire.DeltaKindPart, Index: 0, Part: &part}, {Sequence: 1, Kind: iwire.DeltaKindEnd, End: &end}}, Next: 2, AtEnd: true}, nil
}
func (fixtureChat) Cancel(string) (iwire.Cancellation, error) {
	return iwire.Cancellation{Outcome: iwire.CancelOutcomeInvalid}, nil
}

// copyTestBinary copies this test binary to a program path of its own, so the
// command line (this process) never declares its own program.
func copyTestBinary(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), name)
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

func runProvider(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, diagnostics strings.Builder
	err := providerCommand(args, &out, &diagnostics)
	return out.String() + diagnostics.String(), err
}

func providerStates(t *testing.T, endpoint []string) map[string]fwire.DeclarationState {
	t.Helper()
	out, err := runProvider(t, append([]string{"list", "--json"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider list: %v\n%s", err, out)
	}
	var list fwire.DeclarationList
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("provider list json: %v\n%s", err, out)
	}
	states := map[string]fwire.DeclarationState{}
	for _, p := range list.Declarations {
		states[p.Declaration.Name] = p
	}
	return states
}

func eventually(t *testing.T, within time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, within)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func pids(t *testing.T, dir string) []int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []int
	for _, e := range entries {
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

func answers(endpoint string) bool {
	c, err := listen.Dial(listen.Endpoint(endpoint))
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func answersAt(endpoint string) bool {
	c, err := listen.Dial(endpoint)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// An on-demand provider starts on its first resolution and becomes a ready
// candidate; a crashed provider is not ready until it restarts; removing it
// ends the child; a program cannot declare itself.
func TestOnDemandProviderStartsRestartsAndStops(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	program := copyTestBinary(t, "oa-fixture-provider")
	pidDir := t.TempDir()
	name := fmt.Sprintf("fixture-%d", time.Now().UnixNano()%1_000_000_000)
	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(`{"retention_ms":10000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	out, err := runProvider(t, append([]string{"add", "self", "--program", self, "--provider-endpoint", name + "-self", "--contract", inference.Contract}, endpoint...)...)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "program:self") {
		t.Fatalf("a program declaring itself: %v\n%s", err, out)
	}

	addArgs := []string{"add", name, "--program", program, "--provider-endpoint", name, "--contract", inference.Contract,
		"--guarantee", fixtureGuarantee, "--model", "fixture-model", "--arg", providerFixtureArg, "--arg", "chat-delayed", "--arg", "{endpoint}", "--arg", pidDir, "--arg", self}
	out, err = runProvider(t, append(addArgs, endpoint...)...)
	if err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	if state := providerStates(t, endpoint)[name]; state.Readiness != fwire.DeclarationReadinessIdle || state.DeclaredBy == "" || len(pids(t, pidDir)) != 0 {
		t.Fatalf("before any resolution: %+v, launched %v", state, pids(t, pidDir))
	}
	resolve := func() fwire.ResolveResult {
		call, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := resolution.NewUnverifiedClient(options.endpoint, 5*time.Second).Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference",
			Contracts: []string{inference.Contract}, Guarantees: []string{fixtureGuarantee}, Scope: fwire.ScopeLocal})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if first := resolve(); first.Status != fwire.ResolutionStatusNotReady {
		t.Fatalf("first resolution %+v", first)
	}
	var resolved fwire.ResolveResult
	eventually(t, 20*time.Second, "the on-demand provider becoming a ready candidate", func() bool {
		resolved = resolve()
		return resolved.Status == fwire.ResolutionStatusResolved
	})
	if ref := resolved.Reference; ref.Provider != name || ref.Endpoint == options.endpoint+"-inference" || ref.Endpoint == listen.Endpoint(name) || ref.Contract != inference.Contract {
		t.Fatalf("resolved %+v", ref)
	}
	mediatedEndpoint := resolved.Reference.Endpoint
	launched := pids(t, pidDir)
	if len(launched) != 1 || providerStates(t, endpoint)[name].Readiness != fwire.DeclarationReadinessReady {
		t.Fatalf("launched %v, state %+v", launched, providerStates(t, endpoint)[name])
	}
	appProgram := copyTestBinary(t, "oa-mediated-app")
	type mediatedResult struct {
		DirectRefused bool                `json:"direct_refused"`
		StartOutcome  iwire.StartOutcome  `json:"start_outcome"`
		Outcome       *iwire.ReplyOutcome `json:"outcome,omitempty"`
		Operation     string              `json:"operation"`
	}
	runApp := func(label, endpoint string) mediatedResult {
		resultPath := filepath.Join(t.TempDir(), label+".json")
		if output, err := exec.Command(appProgram, mediatedAppFixtureArg, endpoint, listen.Endpoint(name), resultPath, self).CombinedOutput(); err != nil {
			t.Fatalf("%s application: %v: %s", label, err, output)
		}
		var result mediatedResult
		raw, err := os.ReadFile(resultPath)
		if err != nil || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("%s result %q: %v", label, raw, err)
		}
		return result
	}
	type operationResult struct {
		Observe iwire.PageOutcome   `json:"observe"`
		Cancel  iwire.CancelOutcome `json:"cancel"`
		Error   bool                `json:"error"`
	}
	runOperation := func(program, label, operation string) operationResult {
		resultPath := filepath.Join(t.TempDir(), label+".json")
		if output, err := exec.Command(program, mediatedOperationFixtureArg, mediatedEndpoint, operation, resultPath).CombinedOutput(); err != nil {
			t.Fatalf("%s operation application: %v: %s", label, err, output)
		}
		var result operationResult
		raw, err := os.ReadFile(resultPath)
		if err != nil || json.Unmarshal(raw, &result) != nil {
			t.Fatalf("%s operation result %q: %v", label, raw, err)
		}
		return result
	}
	denied := runApp("denied", mediatedEndpoint)
	if !denied.DirectRefused || denied.StartOutcome != iwire.StartOutcomeNotPermitted {
		t.Fatalf("denied result %+v", denied)
	}
	if calls, _ := os.ReadFile(filepath.Join(pidDir, "calls")); len(calls) != 0 {
		t.Fatalf("denied caller reached provider: %q", calls)
	}
	setCompleteRule(t, options.endpoint, appProgram, name, true)
	mediated := runApp("mediated", mediatedEndpoint)
	if !mediated.DirectRefused || mediated.StartOutcome != iwire.StartOutcomeAccepted || mediated.Outcome == nil || *mediated.Outcome != iwire.ReplyOutcomeCompleted || mediated.Operation == "" || mediated.Operation == "downstream-fixture" {
		t.Fatalf("mediated result %+v", mediated)
	}
	if calls, _ := os.ReadFile(filepath.Join(pidDir, "calls")); strings.Count(string(calls), "start\n") != 1 {
		t.Fatalf("permitted caller reached provider %q", calls)
	}

	child, err := os.FindProcess(launched[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := child.Kill(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, "the crash being noticed", func() bool {
		return providerStates(t, endpoint)[name].Readiness != fwire.DeclarationReadinessReady
	})
	if result := resolve(); result.Status != fwire.ResolutionStatusNotReady {
		t.Fatalf("resolution while restarting %+v", result)
	}
	eventually(t, 20*time.Second, "the provider restarting", func() bool {
		s := providerStates(t, endpoint)[name]
		return s.Readiness == fwire.DeclarationReadinessReady && s.Restarts == 1 && len(pids(t, pidDir)) == 2
	})
	eventually(t, 10*time.Second, "the restarted provider returning to the catalogue", func() bool {
		resolved = resolve()
		return resolved.Status == fwire.ResolutionStatusResolved
	})
	if resolved.Reference.Endpoint != mediatedEndpoint {
		t.Fatalf("ordinary restart changed mediated endpoint from %q to %q", mediatedEndpoint, resolved.Reference.Endpoint)
	}

	out, err = runProvider(t, append([]string{"remove", name}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider remove: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "the removed provider's child ending", func() bool { return !answers(name) })
	if _, listed := providerStates(t, endpoint)[name]; listed {
		t.Fatal("a removed provider is still listed")
	}
	if !answersAt(mediatedEndpoint) {
		t.Fatal("withdrawal stranded the accepted OA operation")
	}
	ownerResult := runOperation(appProgram, "owner-retained", mediated.Operation)
	if ownerResult.Error || ownerResult.Observe != iwire.PageOutcomePage || ownerResult.Cancel != iwire.CancelOutcomeEnded {
		t.Fatalf("owner retained operation %+v", ownerResult)
	}
	strangerProgram := copyTestBinary(t, "oa-mediated-stranger")
	strangerResult := runOperation(strangerProgram, "stranger", mediated.Operation)
	if strangerResult.Error || strangerResult.Observe != iwire.PageOutcomeUnknown || strangerResult.Cancel != iwire.CancelOutcomeUnknown {
		t.Fatalf("stranger operation access %+v", strangerResult)
	}

	// A redeclaration has a fresh binding generation and endpoint. Work sent to
	// the stale endpoint is refused before the replacement provider is started.
	out, err = runProvider(t, append(addArgs, endpoint...)...)
	if err != nil {
		t.Fatalf("provider re-add: %v\n%s", err, out)
	}
	stale := runApp("stale", mediatedEndpoint)
	if stale.StartOutcome != iwire.StartOutcomeNoHost || stale.Operation != "" {
		t.Fatalf("stale mediated endpoint reached replacement binding: %+v", stale)
	}
	if calls, _ := os.ReadFile(filepath.Join(pidDir, "calls")); strings.Count(string(calls), "start\n") != 1 {
		t.Fatalf("stale endpoint reached replacement provider: %q", calls)
	}
	if result := resolve(); result.Status != fwire.ResolutionStatusNotReady {
		t.Fatalf("first replacement resolution %+v", result)
	}
	eventually(t, 20*time.Second, "the replacement provider becoming ready", func() bool {
		resolved = resolve()
		return resolved.Status == fwire.ResolutionStatusResolved
	})
	if resolved.Reference.Endpoint == mediatedEndpoint {
		t.Fatalf("replacement reused stale endpoint %q", mediatedEndpoint)
	}
	replaced := runApp("replacement", resolved.Reference.Endpoint)
	if replaced.StartOutcome != iwire.StartOutcomeAccepted || replaced.Outcome == nil || *replaced.Outcome != iwire.ReplyOutcomeCompleted {
		t.Fatalf("replacement result %+v", replaced)
	}
	if calls, _ := os.ReadFile(filepath.Join(pidDir, "calls")); strings.Count(string(calls), "start\n") != 2 {
		t.Fatalf("replacement calls %q", calls)
	}
	out, err = runProvider(t, append([]string{"remove", name}, endpoint...)...)
	if err != nil {
		t.Fatalf("replacement remove: %v\n%s", err, out)
	}
	if result := resolve(); result.Status != fwire.ResolutionStatusUnmetRequirements {
		t.Fatalf("resolution after replacement removal %+v", result)
	}
	eventually(t, 12*time.Second, "the retired generation endpoint closing after operation retention", func() bool {
		return !answersAt(mediatedEndpoint)
	})
}

// A running provider whose process is another program than the declaration's
// is refused and never offered.
func TestAProviderRunningAnotherProgramIsRefused(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	declared := copyTestBinary(t, "oa-declared-provider")
	pidDir := t.TempDir()
	name := fmt.Sprintf("impostor-%d", time.Now().UnixNano()%1_000_000_000)
	impostor := exec.Command(self, providerFixtureArg, "chat", name, pidDir, self)
	if err := impostor.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { impostor.Process.Kill(); impostor.Wait() })
	eventually(t, 10*time.Second, "the impostor listening", func() bool { return answers(name) })

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, err := runProvider(t, append([]string{"add", name, "--program", declared, "--provider-endpoint", name, "--contract", inference.Contract,
		"--guarantee", fixtureGuarantee, "--model", "fixture-model", "--activate", "attach"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	var state fwire.DeclarationState
	eventually(t, 10*time.Second, "the attached provider being probed", func() bool {
		state = providerStates(t, endpoint)[name]
		return state.Readiness == fwire.DeclarationReadinessRefused
	})
	if !strings.HasPrefix(state.Why, "program:") {
		t.Fatalf("refusal %+v", state)
	}
	call, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := resolution.NewUnverifiedClient(options.endpoint, 5*time.Second).Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference",
		Contracts: []string{inference.Contract}, Guarantees: []string{fixtureGuarantee}, Scope: fwire.ScopeLocal})
	if err != nil || result.Status != fwire.ResolutionStatusNotReady || result.Reference != nil {
		t.Fatalf("resolution of a refused provider %+v %v", result, err)
	}
}

// Declarations are validated field by field.
func TestProviderDeclarationValidation(t *testing.T) {
	for _, name := range []string{"model", "owner/model", "registry.example/owner/model:tag", "模型/聊天"} {
		if !validProviderModel(name) {
			t.Fatalf("common model name %q refused", name)
		}
	}
	program := filepath.Join(t.TempDir(), "provider")
	good := providerDeclaration{Name: "local-llm", Program: program, Arguments: []string{"serve", "{endpoint}"}, Endpoint: "local-llm.v1",
		Transport: transportNative, Contracts: []string{inference.Contract}, Activation: "on_demand"}
	for _, c := range []struct {
		edit  func(*providerDeclaration)
		field string
	}{
		{func(*providerDeclaration) {}, ""},
		{func(d *providerDeclaration) { d.Name = "Local" }, "name"},
		{func(d *providerDeclaration) { d.Program = "provider" }, "program"},
		{func(d *providerDeclaration) { d.Arguments = []string{""} }, "arguments"},
		{func(d *providerDeclaration) { d.Endpoint = `\\.\pipe\x` }, "endpoint"},
		{func(d *providerDeclaration) { d.Transport = "http" }, "transport"},
		{func(d *providerDeclaration) { d.Contracts = []string{"abstraction.job/acceptance"} }, "contracts"},
		{func(d *providerDeclaration) { d.Contracts = nil }, "contracts"},
		{func(d *providerDeclaration) { d.Guarantees = []string{"g", "g"} }, "guarantees"},
		{func(d *providerDeclaration) { d.Models = []string{"owner/model", "owner/model"} }, "models"},
		{func(d *providerDeclaration) { d.Models = []string{"owner/\nmodel"} }, "models"},
		{func(d *providerDeclaration) { d.Models = []string{strings.Repeat("m", 257)} }, "models"},
		{func(d *providerDeclaration) { d.Resources = []string{"profile:vision"} }, "resources"},
		{func(d *providerDeclaration) { d.Resources = []string{"account:x"} }, "resources"},
		{func(d *providerDeclaration) { d.Remote = &inferenceRemoteTrust{ServerName: "lab"} }, "remote"},
		{func(d *providerDeclaration) {
			d.Transport, d.Program, d.Arguments, d.Endpoint, d.Activation = transportRemote, "", nil, "tls://lab.test:9443", "remote"
		}, "remote"},
		{func(d *providerDeclaration) {
			d.Transport, d.Program, d.Arguments, d.Endpoint, d.Activation = transportRemote, "", nil, "lab", "remote"
		}, "endpoint"},
		{func(d *providerDeclaration) { d.Activation = "always" }, "activation"},
	} {
		d := good
		c.edit(&d)
		if got := validProviderDeclaration(d); got != c.field {
			t.Fatalf("%+v: invalid %q, want %q", d, got, c.field)
		}
	}
}

func TestProviderDeclarationTransportBoundary(t *testing.T) {
	for _, tc := range []struct {
		word string
		want fwire.DeclarationTransport
	}{
		{transportNative, fwire.DeclarationTransportNative},
		{transportRemote, fwire.DeclarationTransportRemote},
	} {
		got := (providerDeclaration{Transport: tc.word}).wire().Transport
		if got != tc.want {
			t.Fatalf("wire transport for %q = %v, want %v", tc.word, got, tc.want)
		}
		if roundtrip := declarationOf(fwire.Declaration{Transport: got}).Transport; roundtrip != tc.word {
			t.Fatalf("stored transport for %q = %q", tc.word, roundtrip)
		}
	}
}

// A provider built before endpoint@1 answers Describe unknown_service: it reads
// unreachable with why describe:unknown_service and is never offered.
func TestAnOldProviderWithoutDescribeReadsUnreachable(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	program := copyTestBinary(t, "oa-old-provider")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("old-%d", time.Now().UnixNano()%1_000_000_000)
	old := exec.Command(program, providerFixtureArg, "old", name, t.TempDir(), self)
	if err := old.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { old.Process.Kill(); old.Wait() })
	eventually(t, 10*time.Second, "the old provider listening", func() bool { return answers(name) })

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, err := runProvider(t, append([]string{"add", name, "--program", program, "--provider-endpoint", name, "--contract", inference.Contract,
		"--guarantee", fixtureGuarantee, "--model", "fixture-model", "--activate", "attach"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	var state fwire.DeclarationState
	eventually(t, 10*time.Second, "the old provider being probed", func() bool {
		state = providerStates(t, endpoint)[name]
		return state.Why != "not probed yet"
	})
	if state.Readiness != fwire.DeclarationReadinessUnreachable || state.Why != "describe:unknown_service" || len(state.Described) != 0 {
		t.Fatalf("old provider %+v", state)
	}
	call, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := resolution.NewUnverifiedClient(options.endpoint, 5*time.Second).Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference",
		Contracts: []string{inference.Contract}, Guarantees: []string{fixtureGuarantee}, Scope: fwire.ScopeLocal})
	if err != nil || result.Status != fwire.ResolutionStatusNotReady {
		t.Fatalf("resolution of an old provider %+v %v", result, err)
	}
}

// A caller without abstraction.facade/provider.manage reads forbidden from
// every registry call, and the declarations stay as they were.
func TestACallerWithoutProviderManageIsForbidden(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	if _, err := runProvider(t, append([]string{"list"}, endpoint...)...); err != nil {
		t.Fatalf("provider list by installation: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rightsOperator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := rightsOperator.ListPolicyContext(call, "", 1)
	if err != nil || page.Outcome != rights.PolicyPageOutcomePage {
		t.Fatalf("list policy: %+v %v", page, err)
	}
	deny := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}, Action: ActionProviderManage, Resource: "account", Permit: false}
	if edit, err := rightsOperator.SetRuleContext(call, page.Revision, deny); err != nil || edit.Outcome != rights.PolicyEditOutcomeApplied {
		t.Fatalf("deny provider.manage: %+v %v", edit, err)
	}
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	if list, err := registry.Declarations(call); err != nil || list.Outcome != fwire.DeclarationListOutcomeForbidden || list.Revision != "" || len(list.Declarations) != 0 {
		t.Fatalf("Declarations without provider.manage: %+v %v", list, err)
	}
	declaration := fwire.Declaration{Name: "denied", Program: filepath.Join(t.TempDir(), "provider"), Arguments: []string{}, Endpoint: "denied",
		Transport: fwire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Activation: fwire.ActivationAttach}
	if change, err := registry.Declare(call, "", declaration); err != nil || change.Outcome != fwire.DeclarationEditOutcomeForbidden {
		t.Fatalf("Declare without provider.manage: %+v %v", change, err)
	}
	if change, err := registry.Withdraw(call, "", "denied"); err != nil || change.Outcome != fwire.DeclarationEditOutcomeForbidden {
		t.Fatalf("Withdraw without provider.manage: %+v %v", change, err)
	}
	if observed, err := registry.Observe(call, "", 0); err != nil || observed.Outcome != fwire.DeclarationListOutcomeForbidden {
		t.Fatalf("Observe without provider.manage: %+v %v", observed, err)
	}
	_, err = runProvider(t, append([]string{"list"}, endpoint...)...)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("provider list without provider.manage: %v", err)
	}
	if entries, err := os.ReadDir(filepath.Join(options.stateDir, providersDir)); err != nil || len(entries) != 0 {
		t.Fatalf("declarations after refused calls: %v %v", entries, err)
	}
}

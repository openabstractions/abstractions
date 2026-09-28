package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

func measuredState() rwire.ResourceState {
	return rwire.ResourceState{Resource: "card:0", Capacity: 0, Held: 39808067379,
		Observed: "2026-09-22T10:17:57.000000Z", Instrument: "windows-gpu-counters",
		Holders: []rwire.Holder{
			{Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 21247127552, Evidence: rwire.EvidenceVerified},
			{Program: `C:\lms\llama-server.exe`, Account: "S-1-5-21-7-1001", Amount: 18560939827, Evidence: rwire.EvidenceVerified},
			{Program: "host:lmstudio", Evidence: rwire.EvidenceClaimed, Detail: "gemma-4-26b", Since: "2026-09-22T10:15:41.000000Z"},
		}}
}

// The printed table names each holder, the amount the instrument measured, and
// which rows are claims. A claim prints no amount, because it has none.
func TestResourcesPrintsHoldersAndEvidence(t *testing.T) {
	var out bytes.Buffer
	if err := printResources(&out, resourcesOutput{Resources: []resourceOutput{projectResource(measuredState())}}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"card:0  held 37.07 GiB of unknown  instrument windows-gpu-counters  observed 2026-09-22T10:17:57.000000Z",
		`C:\lms\llama-server.exe`, "S-1-5-21-7-1001", "19.79 GiB", "17.29 GiB", "verified",
		"host:lmstudio", "claimed", "gemma-4-26b",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("the printed table is missing %q:\n%s", want, text)
		}
	}
	claimed := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "host:lmstudio") {
			claimed = line
		}
	}
	if !strings.Contains(claimed, " - ") || strings.Contains(claimed, "GiB") {
		t.Fatalf("a claimed row printed an amount: %q", claimed)
	}
}

// A resource nothing holds says so.
func TestResourcesPrintsAnEmptyResource(t *testing.T) {
	var out bytes.Buffer
	empty := rwire.ResourceState{Resource: "card:0", Observed: "2026-09-22T10:19:21.000000Z", Instrument: "windows-gpu-counters"}
	if err := printResources(&out, resourcesOutput{Resources: []resourceOutput{projectResource(empty)}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no holder") {
		t.Fatalf("an empty card printed:\n%s", out.String())
	}
}

// --json prints the rows as bytes and words, which is what a window reads.
func TestResourcesJSONCarriesBytesAndEvidence(t *testing.T) {
	var out bytes.Buffer
	if err := writeJSON(&out, resourcesOutput{Resources: []resourceOutput{projectResource(measuredState())}}); err != nil {
		t.Fatal(err)
	}
	var decoded resourcesOutput
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(decoded.Resources) != 1 || len(decoded.Resources[0].Holders) != 3 {
		t.Fatalf("decoded %+v", decoded)
	}
	first := decoded.Resources[0].Holders[0]
	if first.Amount != 21247127552 || first.Evidence != "verified" {
		t.Fatalf("first row %+v", first)
	}
	if decoded.Resources[0].Holders[2].Evidence != "claimed" || decoded.Resources[0].Holders[2].Amount != 0 {
		t.Fatalf("claimed row %+v", decoded.Resources[0].Holders[2])
	}
	if decoded.Resources[0].Held != 39808067379 {
		t.Fatalf("held %d", decoded.Resources[0].Held)
	}
}

// --help prints on stdout and says what the command does not do.
func TestResourcesHelp(t *testing.T) {
	var out, diagnostics bytes.Buffer
	if err := resourcesCommand([]string{"--help"}, &out, &diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"openabstractions resources", "--fresh", "--json",
		"abstraction.resource/table.read", "always sees its own rows", "changes nothing"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("the help is missing %q:\n%s", want, out.String())
		}
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("--help wrote to stderr: %s", diagnostics.String())
	}
}

// A negative budget is a usage error before anything is resolved.
func TestResourcesRefusesANegativeTimeout(t *testing.T) {
	var out, diagnostics bytes.Buffer
	err := resourcesCommand([]string{"--timeout", "-1s"}, &out, &diagnostics)
	if exitStatus(err) != exitUsage {
		t.Fatalf("a negative timeout gave %v", err)
	}
}

// fakeTable answers abstraction.resource/table@1 without holding any real
// resource state: TestResourcesEndpointNamesTheServiceItServes never reaches
// its methods, because the resolve request the command sends first is not
// that contract.
type fakeTable struct{}

func (fakeTable) Resources() (rwire.ResourcesResult, error) {
	return rwire.ResourcesResult{Outcome: rwire.ResourceCallOutcomeUnavailable}, nil
}
func (fakeTable) Holders(string, bool) (rwire.HoldersResult, error) {
	return rwire.HoldersResult{Outcome: rwire.ResourceCallOutcomeUnavailable}, nil
}

// serveOneService listens on a fresh pipe, dispatches every call it receives
// through dispatch (a generated *XDispatcher's ExchangeFrame), and stops when
// the test ends. The Program check refuses this Unix fixture on macOS before
// dispatch; the installed macOS runtime uses XPC for its trusted calls.
func serveOneService(t *testing.T, name string, dispatch func([]byte) ([]byte, error)) (string, <-chan error) {
	t.Helper()
	pipe := testPipe("oa-resources-wrong-service", name)
	if runtime.GOOS == "darwin" {
		pipe = filepath.Join(shortSocketDir(t), "service.sock")
	}
	l, err := listen.Listen(pipe)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	proof := make(chan error, 1)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
				select {
				case proof <- err:
				default:
				}
				if call != nil {
					defer call.Close()
				}
				if err != nil {
					return
				}
				if reply, err := dispatch(call.Frame); err == nil {
					call.Reply(reply)
				}
			}()
		}
	}()
	return pipe, proof
}

// `resources --endpoint <a service pipe>` used to answer the opaque
// "unknown_service", the resolver's own refusal word, against an endpoint the
// caller named themselves. It now asks that endpoint what it serves, through
// abstraction.facade/endpoint@1, and names it, and says what --endpoint
// actually wants.
func TestResourcesEndpointNamesTheServiceItServes(t *testing.T) {
	var dispatched atomic.Bool
	pipe, proof := serveOneService(t, "table", func(frame []byte) ([]byte, error) {
		dispatched.Store(true)
		return (&rwire.TableDispatcher{Handler: fakeTable{}}).ExchangeFrame(frame)
	})

	var out, diagnostics bytes.Buffer
	err := resourcesCommand([]string{"--endpoint", pipe, "--timeout", "5s"}, &out, &diagnostics)
	exit := assertExit(t, err, exitNotResolved, "resources --endpoint <a service pipe>")
	if runtime.GOOS == "darwin" {
		select {
		case proofErr := <-proof:
			if !errors.Is(proofErr, identity.ErrNotProven) || dispatched.Load() || out.Len() != 0 {
				t.Fatalf("unproven Unix service: proof %v, dispatched %v, output %q", proofErr, dispatched.Load(), out.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing Unix Program proof refusal")
		}
		t.Skip("service-name response requires Program proof; Unix refusal and no dispatch were checked")
	}
	got := exit.Error()
	for _, want := range []string{"abstraction.resource/table@1", "resolver"} {
		if !strings.Contains(got, want) {
			t.Fatalf("refusal %q is missing %q", got, want)
		}
	}
	if strings.Contains(got, "unknown_service") {
		t.Fatalf("refusal %q still reads the opaque unknown_service", got)
	}
}

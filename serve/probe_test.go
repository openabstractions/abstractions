package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	oaprobe "github.com/openabstractions/abstraction-facade/go/probe"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
)

type probeResult = oaprobe.Result

// runProbe runs the probe command and decodes each JSON result it printed.
func runProbeCommand(t *testing.T, args ...string) ([]probeResult, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	err := probeCommand(append(args, "--json"), &out, &diagnostics)
	var results []probeResult
	decoder := json.NewDecoder(&out)
	for decoder.More() {
		var r probeResult
		if decodeErr := decoder.Decode(&r); decodeErr != nil {
			t.Fatalf("probe %v printed %q: %v", args, out.String(), decodeErr)
		}
		results = append(results, r)
	}
	return results, err
}

func runRightsCommand(t *testing.T, args ...string) (rightsReply, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	err := rightsCommand(append(args, "--json"), &out, &diagnostics)
	var reply rightsReply
	if out.Len() > 0 {
		if decodeErr := json.Unmarshal(out.Bytes(), &reply); decodeErr != nil {
			t.Fatalf("rights %v printed %q: %v", args, out.String(), decodeErr)
		}
	}
	return reply, err
}

func unreachableEndpoint(t *testing.T) string {
	if runtime.GOOS == "windows" {
		return testPipe("oa-probe-absent", "r")
	}
	return filepath.Join(t.TempDir(), "absent.sock")
}

// With no runtime every probe answers runtime_unavailable, as a result, and
// usage mistakes stop before anything is sent.
func TestProbeWithNoRuntimeIsRuntimeUnavailable(t *testing.T) {
	endpoint := unreachableEndpoint(t)
	for _, spec := range oaprobe.List() {
		args := []string{spec.Capability, spec.Operation}
		switch {
		case spec.Argument == "":
		case spec.Capability == "storage":
			args = append(args, "sha256:"+strings.Repeat("0", 64))
		case spec.Capability == "rights":
			args = append(args, host.ConfigEditAction, host.ConfigEditResource)
		case spec.Capability == "jobs":
			args = append(args, "http://127.0.0.1:9/probe")
		default:
			args = append(args, "hf:org/model")
		}
		results, err := runProbeCommand(t, append(args, "--endpoint", endpoint, "--timeout", "2s")...)
		assertExit(t, err, exitNotResolved, strings.Join(args, " "))
		if len(results) != 1 || results[0].Outcome != "runtime_unavailable" || results[0].Resolution != "runtime_unavailable" || results[0].Contract != spec.Contract {
			t.Fatalf("%v with no runtime: %+v", args, results)
		}
		if results[0].Subject.Program == "" || results[0].Subject.Account == "" {
			t.Fatalf("%v names no subject: %+v", args, results[0])
		}
	}
	results, err := runProbeCommand(t, "all", "--endpoint", endpoint, "--timeout", "2s")
	if err != nil || len(results) < 8 {
		t.Fatalf("probe all with no runtime: %v %+v", err, results)
	}
	for _, r := range results {
		if r.Outcome != "runtime_unavailable" {
			t.Fatalf("probe all result %+v", r)
		}
	}
	for _, bad := range [][]string{
		{"nothing"}, {"config", "delete"}, {"storage", "read"}, {"jobs", "inventory", "extra"},
		{"rights", "decide", "only-action"}, {"config", "--runtime-program", `C:\x.exe`},
		{"config", "--endpoint", endpoint, "--runtime-program", "relative"}, {"config", "--timeout", "0s", "--endpoint", endpoint},
	} {
		_, err := runProbeCommand(t, bad...)
		assertExit(t, err, exitUsage, strings.Join(bad, " "))
	}
	// Help lists every probe of the shared list, and says which one writes.
	var help, diagnostics bytes.Buffer
	if err := probeCommand(nil, &help, &diagnostics); err != nil {
		t.Fatal(err)
	}
	for _, p := range oaprobe.List() {
		line := ""
		for _, l := range strings.Split(help.String(), "\n") {
			if f := strings.Fields(l); len(f) >= 2 && f[0] == p.Capability && strings.TrimSuffix(f[1], "*") == p.Operation {
				line = l
				break
			}
		}
		if line == "" || strings.Contains(line, "WRITES") != p.Writes || strings.Contains(line, p.Operation+"*") != p.Default {
			t.Fatalf("help line for %s %s: %q", p.Capability, p.Operation, line)
		}
	}
	defaulted, err := runProbeCommand(t, "config", "--endpoint", endpoint, "--timeout", "2s")
	assertExit(t, err, exitNotResolved, "config default")
	if len(defaulted) != 1 || defaulted[0].Operation != "read" {
		t.Fatalf("probe config ran %+v", defaulted)
	}
	dir := t.TempDir()
	if !probeClient(filepath.Join(dir, "OpenAbstractions-Probe.EXE")) || !probeClient(filepath.Join(dir, "openabstractions-probe")) || probeClient(filepath.Join(dir, "openabstractions")) {
		t.Fatal("probe client name detection")
	}
}

package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// provider list prints each declaration's readiness and its described
// contracts, and status describe prints the provider's own Description, by
// endpoint name and as JSON.
func TestProviderListAndStatusDescribePrintTheRegistryReading(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	program := copyTestBinary(t, "oa-listed-provider")
	name := fmt.Sprintf("listed-%d", time.Now().UnixNano()%1_000_000_000)
	chat := exec.Command(program, providerFixtureArg, "chat", name, t.TempDir())
	if err := chat.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { chat.Process.Kill(); chat.Wait() })
	eventually(t, 10*time.Second, "the provider listening", func() bool { return answers(name) })

	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	if out, err := runProvider(t, append([]string{"add", name, "--program", program, "--provider-endpoint", name, "--contract", inference.Contract,
		"--activate", "attach", "--profiles", "chat"}, endpoint...)...); err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "the provider reading ready", func() bool {
		return providerStates(t, endpoint)[name].Readiness == fwire.DeclarationReadinessReady
	})
	out, err := runProvider(t, append([]string{"list"}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider list: %v\n%s", err, out)
	}
	var row string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, name+" ") {
			row = line
		}
	}
	for _, want := range []string{"attach", " ready ", inference.Contract + "=ready", "profile:chat", program} {
		if !strings.Contains(row, want) {
			t.Fatalf("provider list row lacks %q:\n%s", want, out)
		}
	}

	var text, diagnostics strings.Builder
	if err := statusDescribe([]string{name}, &text, &diagnostics); err != nil {
		t.Fatalf("status describe: %v\n%s%s", err, text.String(), diagnostics.String())
	}
	if !strings.Contains(text.String(), "outcome: described") || !strings.Contains(text.String(), inference.Contract) || !strings.Contains(text.String(), "ready") {
		t.Fatalf("status describe:\n%s", text.String())
	}
	var asJSON strings.Builder
	if err := statusDescribe([]string{name, "--json"}, &asJSON, &diagnostics); err != nil {
		t.Fatalf("status describe --json: %v", err)
	}
	var description fwire.Description
	if err := json.Unmarshal([]byte(asJSON.String()), &description); err != nil || description.Outcome != fwire.DescriptionOutcomeDescribed ||
		len(description.Services) != 1 || description.Services[0].Contract != inference.Contract {
		t.Fatalf("status describe --json: %v\n%s", err, asJSON.String())
	}
	var missing strings.Builder
	if err := statusDescribe([]string{name + "-absent", "--timeout", "1s"}, &missing, &diagnostics); err == nil {
		t.Fatalf("status describe of an absent endpoint answered:\n%s", missing.String())
	}
}

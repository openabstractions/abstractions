package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestEnvironmentEndpointHonoredPerCommand is item 1's own per-command proof:
// a naive user's `probe all` with ABSTRACTION_RUNTIME_ENDPOINT set reached the
// installed runtime instead, because most commands ignored the variable. With
// only the variable set, naming an endpoint nothing answers, every command
// that connects to a runtime must dial that named endpoint, never the
// installed runtime's own default: the fake endpoint's own name, and the
// variable's, surface in the failure. No runtime is needed for this; the
// endpoint is never reached, only dialed and refused.
func TestEnvironmentEndpointHonoredPerCommand(t *testing.T) {
	fake := unreachableEndpoint(t)
	t.Setenv(runtimeEndpointVar, fake)

	commands := []struct {
		name string
		run  func() (diagnostics string, err error)
	}{
		{"probe", func() (string, error) {
			var out, diag bytes.Buffer
			err := probeCommand([]string{"config", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"download", func() (string, error) {
			var out, diag bytes.Buffer
			err := downloadCommand([]string{"http://example.invalid/x", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"jobs", func() (string, error) {
			var out, diag bytes.Buffer
			err := jobsCommand([]string{"list", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"credentials", func() (string, error) {
			var out, diag bytes.Buffer
			err := credentialsCommand([]string{"list", "--timeout", "2s"}, strings.NewReader(""), &out, &diag)
			return diag.String(), err
		}},
		{"applications", func() (string, error) {
			var out, diag bytes.Buffer
			err := applicationsCommand([]string{"list", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"inference", func() (string, error) {
			var out, diag bytes.Buffer
			err := inferenceCommand([]string{"audit", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"resources", func() (string, error) {
			var out, diag bytes.Buffer
			err := resourcesCommand([]string{"--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"models", func() (string, error) {
			var out, diag bytes.Buffer
			err := modelsCommand([]string{"lend", "store/id", "--to", "engine", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"provider", func() (string, error) {
			var out, diag bytes.Buffer
			err := providerCommand([]string{"list", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
		{"opencode", func() (string, error) {
			var out, diag bytes.Buffer
			err := opencodeCommand([]string{"configure", "--dry-run", "--timeout", "2s"}, &out, &diag)
			return diag.String(), err
		}},
	}
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			diagnostics, err := c.run()
			if err == nil {
				t.Fatalf("%s: err = nil, want a failure dialing the fake endpoint", c.name)
			}
			combined := diagnostics + " " + err.Error()
			for _, want := range []string{runtimeEndpointVar, fake} {
				if !strings.Contains(combined, want) {
					t.Fatalf("%s: output %q does not mention %q", c.name, combined, want)
				}
			}
		})
	}
}

// TestEnvironmentEndpointYieldsToExplicitEndpoint checks the other half of
// resolveEndpoint's rule for a representative command: an explicit --endpoint
// wins over the variable, and the failure names that endpoint, not the
// variable's.
func TestEnvironmentEndpointYieldsToExplicitEndpoint(t *testing.T) {
	t.Setenv(runtimeEndpointVar, unreachableEndpoint(t))
	explicit := unreachableEndpoint(t)
	var out, diag bytes.Buffer
	err := jobsCommand([]string{"list", "--endpoint", explicit, "--timeout", "2s"}, &out, &diag)
	if err == nil {
		t.Fatal("jobs list --endpoint: err = nil, want a failure dialing the explicit endpoint")
	}
	if !strings.Contains(err.Error(), explicit) {
		t.Fatalf("err %q does not mention the explicit endpoint %q", err.Error(), explicit)
	}
	if strings.Contains(diag.String(), runtimeEndpointVar) {
		t.Fatalf("diagnostics %q names %s although --endpoint was given explicitly", diag.String(), runtimeEndpointVar)
	}
}

// TestDeadEndpointGetsFriendlyMessage is item 2, round 5: a naive user's dead
// endpoint (nothing listens at it, unlike the refused-connection cases
// above) no longer leaks the raw dial error ("open \\.\pipe\...: The system
// cannot find the file specified.") download, jobs list and rights list each
// used to print. All three, and every other command sharing the shared
// resolver path (notResolved), now print status describe's own sentence,
// naming the endpoint and which of resolveEndpoint's three rules chose it.
// probe is checked separately below: its result carries the same sentence in
// its own text rather than in a returned error, and `probe all` exits 0 once
// every call answered or failed with its own result (documented, unchanged).
func TestDeadEndpointGetsFriendlyMessage(t *testing.T) {
	endpoint := unreachableEndpoint(t)
	commands := []struct {
		name string
		run  func() (text string, err error)
	}{
		{"download", func() (string, error) {
			var out, diag bytes.Buffer
			err := downloadCommand([]string{"http://example.invalid/x", "--endpoint", endpoint, "--timeout", "2s"}, &out, &diag)
			return out.String() + diag.String(), err
		}},
		{"jobs list", func() (string, error) {
			var out, diag bytes.Buffer
			err := jobsCommand([]string{"list", "--endpoint", endpoint, "--timeout", "2s"}, &out, &diag)
			return out.String() + diag.String(), err
		}},
		{"rights list", func() (string, error) {
			var out, diag bytes.Buffer
			err := rightsCommand([]string{"list", "--endpoint", endpoint, "--timeout", "2s"}, &out, &diag)
			return out.String() + diag.String(), err
		}},
	}
	for _, c := range commands {
		t.Run(c.name, func(t *testing.T) {
			text, err := c.run()
			if err == nil {
				t.Fatalf("%s: err = nil, want a failure dialing the dead endpoint", c.name)
			}
			combined := text + " " + err.Error()
			if !strings.Contains(combined, endpoint) {
				t.Fatalf("%s: %q does not name the endpoint", c.name, combined)
			}
			if !strings.Contains(combined, "no runtime listens at") ||
				!strings.Contains(combined, `run "openabstractions status" to list the endpoints that do`) {
				t.Fatalf("%s: %q does not carry the friendly sentence", c.name, combined)
			}
			if !strings.Contains(combined, "explicit endpoint") {
				t.Fatalf("%s: %q does not name the endpoint source", c.name, combined)
			}
			if strings.Contains(combined, "cannot find the file specified") || strings.Contains(combined, "no such file or directory") || strings.Contains(combined, "connection refused") {
				t.Fatalf("%s: %q leaks the raw dial error", c.name, combined)
			}
		})
	}

	t.Run("probe all", func(t *testing.T) {
		var out, diag bytes.Buffer
		err := probeCommand([]string{"all", "--endpoint", endpoint, "--timeout", "2s"}, &out, &diag)
		if err != nil {
			t.Fatalf("probe all: %v", err)
		}
		text := out.String()
		if !strings.Contains(text, endpoint) || !strings.Contains(text, "no runtime listens at") {
			t.Fatalf("probe all: %q does not carry the friendly sentence", text)
		}
		if strings.Contains(text, "cannot find the file specified") || strings.Contains(text, "no such file or directory") {
			t.Fatalf("probe all: %q leaks the raw dial error", text)
		}
	})
}

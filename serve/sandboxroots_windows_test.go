package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func fakeSandboxRoots(state map[string]bool, failing string, recorded *[]string) sandboxRootOps {
	fail := errors.New("access is denied")
	return sandboxRootOps{
		roots: func() ([]string, error) { return []string{`C:\`, `C:\Users`}, nil },
		grant: func(p string) (bool, error) {
			if p == failing {
				return false, fail
			}
			changed := !state[p]
			state[p] = true
			return changed, nil
		},
		revoke: func(p string) (bool, error) {
			if p == failing {
				return false, fail
			}
			changed := state[p]
			state[p] = false
			return changed, nil
		},
		present:       func(p string) (bool, error) { return state[p], nil },
		recordFailure: func(command string, err error) { *recorded = append(*recorded, command+": "+err.Error()) },
	}
}

// TestSandboxRootsCommand: grant, revoke and show report each root once, are
// idempotent, and a failure is returned and recorded for the installer log.
func TestSandboxRootsCommand(t *testing.T) {
	state := map[string]bool{}
	var recorded []string
	ops := fakeSandboxRoots(state, "", &recorded)
	for _, c := range []struct{ args, want string }{
		{"show", "C:\\: no entry\nC:\\Users: no entry\n"},
		{"grant", "C:\\: entry added\nC:\\Users: entry added\n"},
		{"grant", "C:\\: entry already present\nC:\\Users: entry already present\n"},
		{"show", "C:\\: entry present\nC:\\Users: entry present\n"},
		{"revoke", "C:\\: entry removed\nC:\\Users: entry removed\n"},
		{"revoke", "C:\\: no entry to remove\nC:\\Users: no entry to remove\n"},
	} {
		var out, diagnostics bytes.Buffer
		if err := sandboxRootsCommand([]string{c.args}, &out, &diagnostics, ops); err != nil || out.String() != c.want {
			t.Fatalf("%s: %v %q", c.args, err, out.String())
		}
	}
	if len(recorded) != 0 {
		t.Fatalf("recorded %v", recorded)
	}
	ops = fakeSandboxRoots(state, `C:\`, &recorded)
	var out, diagnostics bytes.Buffer
	if err := sandboxRootsCommand([]string{"grant"}, &out, &diagnostics, ops); err == nil || out.String() != "C:\\Users: entry added\n" {
		t.Fatalf("a failed grant: %v %q", err, out.String())
	}
	if len(recorded) != 1 || !strings.HasPrefix(recorded[0], "host sandbox-roots grant: C:\\: ") {
		t.Fatalf("recorded %v", recorded)
	}
	for _, args := range [][]string{nil, {"add"}, {"grant", "now"}} {
		var exit *exitError
		if err := sandboxRootsCommand(args, &out, &diagnostics, ops); !errors.As(err, &exit) || exit.code != 2 {
			t.Fatalf("%v accepted: %v", args, err)
		}
	}
	out.Reset()
	if err := hostCommand([]string{"sandbox-roots", "--help"}, &out, &diagnostics); err != nil || !strings.Contains(out.String(), "host sandbox-roots grant|revoke|show") {
		t.Fatalf("help: %v %q", err, out.String())
	}
}

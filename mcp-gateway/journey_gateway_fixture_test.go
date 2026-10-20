package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// journeyGatewayFixtureMain runs the shipped gateway main against an isolated
// runtime for the journey suite (release/testing/journeys.py). The suite
// copies this package's test binary to an agent helper name such as
// oa-opencode-integration.exe and sets OA_JOURNEY_GATEWAY to
// {"endpoint", "runtime_program", "args"}. Only the runtime selection
// differs from the shipped binary: the same flags, agent composition, chat
// binding and presence run. Nothing here reaches the installed runtime.
func journeyGatewayFixtureMain() bool {
	raw := os.Getenv("OA_JOURNEY_GATEWAY")
	if raw == "" {
		return false
	}
	var cfg struct {
		Endpoint       string   `json:"endpoint"`
		RuntimeProgram string   `json:"runtime_program"`
		Args           []string `json:"args"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil || cfg.Endpoint == "" || !filepath.IsAbs(cfg.RuntimeProgram) {
		fmt.Fprintln(os.Stderr, "journey gateway: OA_JOURNEY_GATEWAY needs endpoint and an absolute runtime_program")
		os.Exit(2)
	}
	// Journeys start agent helpers only with a fixture home
	// (backlog/JOURNEY-SCOPE.md): the helper never reads or wakes the
	// person's own Claude, Codex or OpenCode setup.
	person := os.Getenv("OA_TEST_PERSON")
	if person == "" || !samePath(firstSet(os.Getenv("USERPROFILE"), os.Getenv("HOME")), person) ||
		!within(os.Getenv("APPDATA"), person) || !within(os.Getenv("LOCALAPPDATA"), person) {
		fmt.Fprintln(os.Stderr, "journey gateway: refused; OA_TEST_PERSON must name a fixture home that USERPROFILE or HOME, APPDATA and LOCALAPPDATA point into")
		os.Exit(2)
	}
	who, err := identity.Self()
	if err != nil {
		fmt.Fprintln(os.Stderr, "journey gateway:", err)
		os.Exit(1)
	}
	expected := listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(cfg.RuntimeProgram))}
	gatewayMachine = func() *facade.Machine { return facade.NewVerified(cfg.Endpoint, expected) }
	_ = os.Unsetenv("OA_JOURNEY_GATEWAY")
	os.Args = append([]string{os.Args[0]}, cfg.Args...)
	main()
	return true
}

func firstSet(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func samePath(a, b string) bool {
	return a != "" && strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// within reports whether path lies inside root; an unset variable counts, as
// on a platform without it.
func within(path, root string) bool {
	if path == "" {
		return true
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

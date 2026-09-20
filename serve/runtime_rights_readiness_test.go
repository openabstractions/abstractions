package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
)

// While the policy file is unreadable, the resolver reports the rights
// decision and operator contracts not_ready; once the file is back they
// resolve again. Configuration, which no rule gates, resolves throughout.
func TestRightsReadinessFollowsThePolicyFile(t *testing.T) {
	options := gatedRuntime(t)
	resolver := resolution.NewClient(options.endpoint, 2*time.Second)
	status := func(contract string) string {
		t.Helper()
		capability := contract[:len("abstraction.rights")]
		if contract == "abstraction.config/reader@1" {
			capability = "abstraction.config"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		result, err := resolver.Resolve(ctx, wire.ResolveRequest{Capability: capability, Contracts: []string{contract}, Guarantees: []string{}, Scope: wire.ScopeLocal})
		if err != nil {
			t.Fatalf("resolve %s: %v", contract, err)
		}
		return result.Status.String()
	}
	await := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			decision, operator := status("abstraction.rights/authorization@1"), status("abstraction.rights/operator@1")
			if decision == want && operator == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("rights readiness %s and %s, want %s", decision, operator, want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	await("resolved")
	policy := filepath.Join(options.stateDir, "rights", "decisions.json")
	if err := os.Rename(policy, policy+".moved"); err != nil {
		t.Fatal(err)
	}
	await("not_ready")
	if got := status("abstraction.config/reader@1"); got != "resolved" {
		t.Fatalf("configuration during a policy outage: %s", got)
	}
	if err := os.Rename(policy+".moved", policy); err != nil {
		t.Fatal(err)
	}
	await("resolved")
}

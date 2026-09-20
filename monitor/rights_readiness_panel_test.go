package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Identity section shows the rights decision and administration contracts
// not ready while the runtime's policy file is missing, and ready once it is
// back.
func TestIdentityShowsRightsNotReadyDuringAPolicyOutage(t *testing.T) {
	own(t)
	path := filepath.Join(t.TempDir(), "decisions.json")
	seed, err := rights.LoadDecisionPolicy(path, []string{panelRightsAction})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Set(rightsclient.Subject{Account: "panel-account", Program: filepath.Clean(os.Args[0])}, panelRightsAction, "sha256:x", true); err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(path, []string{panelRightsAction})
	if err != nil {
		t.Fatal(err)
	}
	policy.StateRequired = true
	if err := os.Rename(path, path+".moved"); err != nil {
		t.Fatal(err)
	}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		o.RightsPolicy, o.RightsEndpoint = policy, o.Endpoint+"-rights"
		o.RightsOperator = func(ctx context.Context, _ *identity.Peer) error { return ctx.Err() }
	})
	h := (&servicePanel{}).handler("test-key")
	rows := func() map[string]capabilityView {
		r := panelRequest(t, h, "/identity", nil)
		view := decodePanel[identityView](t, r.Code, r.Body.Bytes())
		out := map[string]capabilityView{}
		for _, row := range view.Capabilities {
			out[row.Contract] = row
		}
		return out
	}
	await := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			got := rows()
			decision, operator := got["abstraction.rights/authorization@1"], got["abstraction.rights/operator@1"]
			if decision.Status == want && operator.Status == want {
				if decision.Name != "Rights decisions" || operator.Name != "Rights administration" {
					t.Fatalf("rights rows are unnamed: %+v %+v", decision, operator)
				}
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("Identity shows rights %+v and %+v, want %s", decision, operator, want)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	await("not_ready")
	if err := os.Rename(path+".moved", path); err != nil {
		t.Fatal(err)
	}
	await("resolved")
}

package main

import (
	credentials "github.com/openabstractions/abstraction-credentials/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"path/filepath"
	"testing"
)

func TestInferenceRequestsUsesAuditReadAccountAuthority(t *testing.T) {
	f := newAnswererFixture(t)
	if err := f.a.rights.policy.RegisterAction(inference.ActionAuditRead); err != nil {
		t.Fatal(err)
	}
	j, err := inference.OpenJournal(filepath.Join(t.TempDir(), "audit.jsonl"), 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Append(inference.Record{Outcome: "completed", Host: "local", Model: "local-model", Preferred: "cloud-model", PreferredHost: "cloud", Selection: "preferred_route_unavailable", Placement: wire.ServedPlaceLocal}); err != nil {
		t.Fatal(err)
	}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: f.a.rights}, journal: j}
	caller := inference.Subject{Account: f.panel.Account, Program: f.panel.Program}
	refused := func(s inference.Subject) {
		t.Helper()
		p := o.Requests(t.Context(), s, 0, 64)
		if p.Outcome != wire.AuditOutcomeForbidden || len(p.Entries) != 0 || p.Next != 0 || p.AtEnd {
			t.Fatal(p)
		}
	}
	refused(caller)
	if err := f.a.rights.policy.Set(f.panel, inference.ActionAuditRead, credentials.ResourceAccount, true); err != nil {
		t.Fatal(err)
	}
	p := o.Requests(t.Context(), caller, 0, 64)
	if p.Outcome != wire.AuditOutcomePage || len(p.Entries) != 1 || p.Entries[0].PreferredModel != "cloud-model" || p.Entries[0].Placement == nil || *p.Entries[0].Placement != wire.ServedPlaceLocal {
		t.Fatal(p)
	}
	refused(inference.Subject{Account: "foreign", Program: caller.Program})
	refused(inference.Subject{Account: caller.Account, Program: ""})
	if err := f.a.rights.policy.Set(f.panel, inference.ActionAuditRead, credentials.ResourceAccount, false); err != nil {
		t.Fatal(err)
	}
	refused(caller)
}

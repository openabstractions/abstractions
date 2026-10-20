package main

import (
	"context"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"strings"
	"testing"
)

func TestRevisionFinalRuntimeConfiguration(t *testing.T) {
	r := testRights(t, t.TempDir())
	o := host.Options{OnError: func(e error) { t.Error(e) }}
	r.configure(&o, nil)
	if o.RightsPolicy != r.policy || o.RightsEnforcer != nil {
		t.Fatal("runtime admission changed")
	}
	d := r.policy.DecideForAtLeastRevisionContext(context.Background(), wire.Subject{Account: r.owner, Program: "remote:" + strings.Repeat("a", 64)}, "abstraction.storage/content.read", "item", "")
	if d.ObservedRevision == "" {
		t.Fatal(d)
	}
}

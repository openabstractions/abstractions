package main

import (
	"context"
	askapi "github.com/openabstractions/abstraction-asks/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	"testing"
)

func TestPreloadWithoutGrantAsksNothingAndLoadsNothing(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{inference.ActionComplete}, nil); err != nil {
		t.Fatal(err)
	}
	asks, effects := 0, 0
	a, _ := firstUseBook(t)
	a.rights = r
	r.firstUse = func(s rwire.Subject, p func() askapi.Publisher, action, resource string) string {
		asks++
		return a.admitFirstUse(s, p, action, resource)
	}
	p, err := inference.New(inference.Config{Router: router.New(router.OpenAICompatible("engine", "http://engine0.modelhost.invalid", "/v1", []string{"tiny"})), Decide: r.decideInference,
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			effects++
			return nil, "not_permitted"
		},
		Admit: func(context.Context, *router.Host, string) (string, bool) { effects++; return "", true },
		EvaluatePrefix: func(context.Context, *router.Host, string, []byte) (wire.PreloadOutcome, string, int64) {
			effects++
			return wire.PreloadOutcomeLoaded, "", 0
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	reply := p.Preload(context.Background(), inference.Subject{Account: r.owner, Program: appProgram("ungranted-preloader")}, wire.Request{Model: "tiny", Messages: []wire.Message{{Role: wire.RoleSystem, Parts: []wire.Part{{Kind: wire.PartKindText}}}}})
	if reply.Outcome != wire.PreloadOutcomeNotPermitted || asks != 0 || effects != 0 {
		t.Fatalf("reply %+v asks %d effects %d", reply, asks, effects)
	}
}

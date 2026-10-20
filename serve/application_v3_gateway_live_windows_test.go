package main

import (
	"context"
	"testing"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
)

// J-COMFY-LIVE (owner, 2026-11-10): OpenCode opened ComfyUI through OA, its
// owned host announced and renewed its instance, and still
// oa_applications_describe comfyui listed no instance while Commands and Call
// answered stale. The gateway's production single-application loader
// (loadDeclaration) built its view without the directory's live leases; the
// gateway fixture's own loader read them, so no test reached the gap. This
// test runs Describe, Commands and Call through the production loader over a
// kept @3 declaration.
func TestApplicationV3GatewayProductionLoadSeesTheAnnouncedInstance(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	ctx := t.Context()
	d := f.g.directory
	// The fixture's session announced under a legacy registration. Keep that
	// lease, replace the registration with a kept @3 declaration of the same
	// manifest, and hand the lease back: the instance an owned host announced
	// for a self-registered application.
	d.mu.Lock()
	lease, found := d.instances[f.instance]
	d.mu.Unlock()
	if !found {
		t.Fatal("the fixture's announced lease is missing")
	}
	if out := d.remove(ctx, f.subject, "fixture"); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if err := d.attachApplicationV3(ctx, f.g.decoder); err != nil {
		t.Fatal(err)
	}
	display, file := registration1459File(t, "fixture", f.subject.Program, f.manifest.Title)
	file.Raw = registrationwire.EncodeManifest(f.manifest)
	file.Digest = applicationV3Hash(file.Raw)
	file.Accepted, file.Verified = registrationTime(d.now()), registrationTime(d.now())
	if out := d.publishApplicationV3(ctx, display, file, "operator", ""); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	revision := d.v3.entries["fixture"].Revision
	f.s.bindMu.Lock()
	f.s.binding.manifestRevision = revision
	f.s.bindMu.Unlock()
	d.mu.Lock()
	d.instances[f.instance] = lease
	d.mu.Unlock()
	// The production loader builds the view and its live set. The fixture
	// holds no signed image, so only the held-image recheck is replaced.
	f.g.load = func(ctx context.Context, peer *identity.Peer, name string) (*applicationV3Kept, error) {
		k, err := f.g.loadDeclaration(ctx, peer, name)
		if err == nil {
			k.recheck = func(context.Context) error { return nil }
		}
		return k, err
	}

	described, err := f.c.Describe(appmodel.ApplicationsQuery{Application: "fixture"})
	if err != nil || described.Answer == nil || described.Answer.Outcome != appmodel.ReadingOutcomePage || len(described.Answer.Applications) != 1 {
		t.Fatalf("describe of the named application: %+v %v", described, err)
	}
	instances := described.Answer.Applications[0].Instances
	if len(instances) != 1 || instances[0].Instance != f.instance || instances[0].Reason != "" || len(instances[0].Contexts) != 1 {
		t.Fatalf("describe shows no live instance: %+v", instances)
	}
	listed, err := f.c.Commands(appmodel.CommandsQuery{Application: "fixture", Instance: f.instance})
	if err != nil || listed.Answer == nil || listed.Answer.Outcome != appmodel.ReadingOutcomePage || len(listed.Answer.Commands) != 1 || listed.Answer.Commands[0].Name != "note.read" {
		t.Fatalf("commands of the live instance: %+v %v", listed, err)
	}
	result, _ := f.call(t, 1110)
	if result.Answer == nil || result.Answer.Result == nil || result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied || f.effects.Load() != 1 {
		t.Fatalf("call on the live instance: %+v", result)
	}

	// A withdrawn instance is stale again through the same loader.
	d.mu.Lock()
	delete(d.instances, f.instance)
	d.mu.Unlock()
	if listed, err := f.c.Commands(appmodel.CommandsQuery{Application: "fixture", Instance: f.instance}); err != nil || listed.Answer == nil || listed.Answer.Outcome.String() != "stale" {
		t.Fatalf("commands of a withdrawn instance: %+v %v", listed, err)
	}
}

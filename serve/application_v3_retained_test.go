package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/manifest"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestRegistryV3DraftRenameAllowanceAtSchemaMaxima(t *testing.T) {
	// Exercise every renamed occurrence at its schema cardinality. Content
	// bounds remain16 models,16 chooser entries,64 applications and4096 text.
	spec := registration.RegistrationSpec{Applications: make([]registration.ApplicationFacet, 64)}
	for i := range spec.Applications {
		m := registration.Manifest{Version: "applications@3", Name: fmt.Sprintf("app%d", i), Title: "App", Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}, Host: registration.HostRecipe{ReadinessDeadlineMs: 100}, Choosers: &registration.Choosers{}}
		for j := 0; j < 16; j++ {
			m.Needs.Models = append(m.Needs.Models, registration.ModelNeed{Modality: "image", Placement: registration.PlacementAny})
			m.Choosers.Entries = append(m.Choosers.Entries, registration.Chooser{Name: fmt.Sprintf("image%d", j), Modality: "image"})
		}
		spec.Applications[i] = registration.ApplicationFacet{Name: m.Name, Manifest: m}
	}
	compact := func(raw []byte) []byte {
		var b bytes.Buffer
		if err := json.Compact(&b, raw); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	priorOf := func(raw []byte) []byte {
		out, _, err := retainedDraftMembers(raw, append([]retainedDraftMember{{"", "", "hosts", "servers"}}, retainedManifestMembers("/applications/[]/manifest")...), false, nil)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	current := compact(registration.EncodeRegistrationSpec(&spec))
	prior := priorOf(current)
	// The single-document budget has the same exact33-byte increase at its
	// own previous boundary, independently of the outer spec's64 occurrences.
	m := spec.Applications[0].Manifest
	m.Host.Arguments = make([]string, 64)
	for i := range m.Host.Arguments {
		m.Host.Arguments[i] = "a"
	}
	manifestRaw := compact(registration.EncodeManifest(&m))
	manifestPrior := applicationV3PriorCanonical(manifestRaw)
	left := 262144 - len(manifestPrior)
	for i := range m.Host.Arguments {
		n := min(left, 4095)
		m.Host.Arguments[i] += strings.Repeat("a", n)
		left -= n
	}
	if left != 0 {
		t.Fatal("manifest fixture capacity", left)
	}
	manifestRaw = compact(registration.EncodeManifest(&m))
	manifestPrior = applicationV3PriorCanonical(manifestRaw)
	if len(manifestPrior) != 262144 || len(manifestRaw) != manifest.MaxApplicationManifestBytes {
		t.Fatal("manifest caps", len(manifestPrior), len(manifestRaw))
	}
	if decoded, err := registration.DecodeManifest(manifestRaw); err != nil || !bytes.Equal(applicationV3PriorCanonical(compact(registration.EncodeManifest(decoded))), manifestPrior) {
		t.Fatal("prior maximum manifest roundtrip", err)
	}
	// Earlier accepted resources also had a physical canonical-document cap.
	// Reach that exact cap with the actual generated formatting, then retain
	// its original digest through injected held-image signature evidence.
	for i := range m.Host.Arguments {
		m.Host.Arguments[i] = "a"
	}
	left = 262144 - len(applicationV3PriorCanonical(registration.EncodeManifest(&m)))
	for i := range m.Host.Arguments {
		n := min(left, 4095)
		m.Host.Arguments[i] += strings.Repeat("a", n)
		left -= n
	}
	manifestRaw = registration.EncodeManifest(&m)
	manifestPrior = applicationV3PriorCanonical(manifestRaw)
	if left != 0 || len(manifestPrior) != 262144 || len(manifestRaw) != manifest.MaxApplicationManifestBytes {
		t.Fatal("physical canonical caps", left, len(manifestPrior), len(manifestRaw))
	}
	g := &applicationV3Gateway{}
	held := &heldApplicationManifest{Bytes: bytes.Clone(manifestPrior), Code: identity.Code{Trusted: true}, Proof: identity.ProofBound}
	if _, err := g.parseRetained(manifestPrior, "fixture-program", held); err != nil {
		t.Fatal("prior physical maximum held manifest", err)
	}
	if _, digest, err := g.decoder(manifestPrior, "fixture-program", nil); err != nil || digest != applicationV3Hash(manifestPrior) {
		t.Fatal("prior physical maximum digest", digest, err)
	}
	// Small whole values isolate the original content bounds from whole caps.
	small := spec
	small.Applications = append([]registration.ApplicationFacet(nil), spec.Applications[:1]...)
	for _, change := range []func(map[string]any){
		func(m map[string]any) { m["description"] = strings.Repeat("x", 4097) },
		func(m map[string]any) {
			n := m["needs"].(map[string]any)
			a := n["models"].([]any)
			n["models"] = append(a, a[0])
		},
		func(m map[string]any) {
			n := m["choosers"].(map[string]any)
			a := n["entries"].([]any)
			n["entries"] = append(a, a[0])
		},
	} {
		var value map[string]any
		if err := json.Unmarshal(registration.EncodeRegistrationSpec(&small), &value); err != nil {
			t.Fatal(err)
		}
		change(value["applications"].([]any)[0].(map[string]any)["manifest"].(map[string]any))
		bad, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if len(bad) >= maxRegistrationSpecBytes {
			t.Fatal("content-bound fixture oversized")
		}
		if _, err := registration.DecodeRegistrationSpec(bad); err == nil {
			t.Fatal("content field bound+1 accepted")
		}
	}
	if len(current)-len(prior) != 64*(16+16+1)+2 || maxRegistrationSpecBytes != 262144+len(current)-len(prior) || manifest.MaxApplicationManifestBytes != 262144+16+16+1 {
		t.Fatal("schema rename multiplicity changed", len(current)-len(prior))
	}
	remaining := 262144 - len(prior)
	for i := range spec.Applications {
		n := min(remaining, 4096)
		spec.Applications[i].Manifest.Description = strings.Repeat("x", n)
		remaining -= n
	}
	if remaining != 0 {
		t.Fatal("fixture cannot reach old cap", remaining)
	}
	current = compact(registration.EncodeRegistrationSpec(&spec))
	prior = priorOf(current)
	if len(prior) != 262144 || len(current) != maxRegistrationSpecBytes {
		t.Fatal("wrong exact caps", len(prior), len(current))
	}
	original := bytes.Clone(prior)
	projected, _, err := retainedDraftMembers(prior, append([]retainedDraftMember{{"", "", "hosts", "servers"}}, retainedManifestMembers("/applications/[]/manifest")...), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := registration.DecodeRegistrationSpec(projected)
	if err != nil || !bytes.Equal(priorOf(compact(registration.EncodeRegistrationSpec(decoded))), original) || !bytes.Equal(prior, original) {
		t.Fatal("maximum prior payload lost", err)
	}
	if _, err := decodeProviderRegistration(append(current, ' ')); err == nil {
		t.Fatal("physical spec cap+1 accepted")
	}
	// A content bound+1 remains a generated refusal despite rename allowance.
	for _, bad := range [][]byte{
		bytes.Replace(current, []byte(`"description":"`+strings.Repeat("x", 4096)+`"`), []byte(`"description":"`+strings.Repeat("x", 4097)+`"`), 1),
		bytes.Replace(current, []byte(`"readiness_deadline_ms":100`), []byte(`"readiness_deadline_ms":30001`), 1),
	} {
		if bytes.Equal(bad, current) {
			t.Fatal("negative fixture unchanged")
		}
		if _, err := registration.DecodeRegistrationSpec(bad); err == nil {
			t.Fatal("content bound+1 accepted")
		}
	}
}

func TestApplicationV3RetainedRenamePreservesValuesAndRefusesCollisions(t *testing.T) {
	raw := []byte(`{"profiles":{"entries":[{"name":"profile","profile":"image"}]},"needs":{"models":[{"profile":"image"}]},"offers":{"input":{"modality":"profile"}}}`)
	got, prior := applicationV3RetainedManifest(raw)
	if !prior || !bytes.Contains(got, []byte(`"name":"profile"`)) || !bytes.Contains(got, []byte(`"input":{"modality":"profile"}`)) {
		t.Fatalf("retained values changed: %s", got)
	}
	if reverse := applicationV3PriorCanonical(got); !bytes.Equal(reverse, raw) {
		t.Fatalf("canonical custody changed: %s", reverse)
	}
	opaque := []byte(` { "profiles" : { "entries" : [{"name":"profile","profile" : "image"}]}, "offers":{"schema": { "modality" : "<>&", "name" : "profile" }} } `)
	forward, changed := applicationV3RetainedManifest(opaque)
	if !changed || !bytes.Equal(applicationV3PriorCanonical(forward), opaque) {
		t.Fatal("opaque JSON or whitespace changed")
	}
	for _, raw := range []string{
		`{"profiles":{"entries":[]},"choosers":{"entries":[]}}`,
		`{"profiles":{"entries":[{"name":"profile","profile":"image","modality":"image"}]}}`,
		`{"needs":{"models":[{"profile":"image","modality":"image"}]}}`,
		`{"profiles":{"entries":[]},"name":"first","name":"second"}`,
		`{"needs":{"models":[{"profile":"image","profile":"chat"}]}}`,
	} {
		got, prior := applicationV3RetainedManifest([]byte(raw))
		if prior || string(got) != raw {
			t.Fatalf("collision transformed: %s", got)
		}
	}
}

// Captured signature evidence is injected; this tests retained decoding and
// binding, and makes no signed executable or installed old-child claim.
func TestApplicationV3RetainedManifestPreservesBothPriorCanonicalSpellings(t *testing.T) {
	m := &registration.Manifest{Version: "applications@3", Name: "retained", Title: "Retained", Host: registration.HostRecipe{ReadinessDeadlineMs: 1000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}, Choosers: &registration.Choosers{Entries: []registration.Chooser{{Name: "image", Modality: "image"}}}}
	canonical := registration.EncodeManifest(m)
	immediate := bytes.Replace(canonical, []byte(`"readiness_deadline_ms"`), []byte(`"readiness_timeout_ms"`), 1)
	earliest := applicationV3PriorCanonical(canonical)
	for _, raw := range [][]byte{immediate, earliest} {
		original := bytes.Clone(raw)
		g := &applicationV3Gateway{}
		_, digest, err := g.decoder(raw, "fixture-program", nil)
		if err != nil || digest != applicationV3Hash(original) {
			t.Fatal("historical canonical digest changed", digest, applicationV3Hash(original), err)
		}
		if _, err := registration.DecodeManifest(raw); err == nil {
			t.Fatal("public codec accepted prior vocabulary")
		}
		held := &heldApplicationManifest{Bytes: bytes.Clone(raw), Code: identity.Code{Trusted: true}, Proof: identity.ProofBound}
		if _, err := g.parse(raw, "fixture-program", held); err == nil {
			t.Fatal("new admission accepted retained-only spelling")
		}
		parsed, err := g.parseRetained(raw, "fixture-program", held)
		if err != nil || parsed.Host.ReadinessDeadlineMs != 1000 || parsed.Choosers == nil || parsed.Choosers.Entries[0].Modality != "image" {
			t.Fatal(parsed, err)
		}
		if got := applicationV3PriorCanonical(registration.EncodeManifest(parsed), original); !bytes.Equal(got, original) {
			t.Fatal("retained roundtrip changed original canonical bytes")
		}
		held.Bytes = append(held.Bytes, ' ')
		if _, err := g.parseRetained(raw, "fixture-program", held); err == nil {
			t.Fatal("changed held payload accepted")
		}
		if !bytes.Equal(raw, original) {
			t.Fatal("retained source was rewritten")
		}
	}
	for _, raw := range []string{
		strings.Replace(string(immediate), `"readiness_timeout_ms": 1000`, `"readiness_timeout_ms": 1000, "readiness_deadline_ms": 1000`, 1),
		strings.Replace(string(immediate), `"readiness_timeout_ms": 1000`, `"readiness_timeout_ms": 99`, 1),
		strings.Replace(string(immediate), `"readiness_timeout_ms": 1000`, `"readiness_timeout_ms": 1000, "unknown": true`, 1),
	} {
		if _, _, err := (&applicationV3Gateway{}).decoder([]byte(raw), "fixture-program", nil); err == nil {
			t.Fatal("mixed, out-of-bound or unknown retained member accepted")
		}
	}
}

// Owner, 2026-11-09: an agent's open of ComfyUI, kept on 2026-10-31 under the
// earlier spelling (readiness_timeout_ms, profiles), answered
// identity_refused/owned_host because the owned start read the held image
// with the current closed codec. The start, the recheck and the kept
// registration read one declaration with one digest.
func TestApplicationV3OwnedStartReadsRetainedDeclaration(t *testing.T) {
	m := &registration.Manifest{Version: "applications@3", Name: "retained", Title: "Retained", Host: registration.HostRecipe{Arguments: []string{"owned-host"}, ReadinessDeadlineMs: 30000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1"}, Choosers: &registration.Choosers{Entries: []registration.Chooser{{Name: "image", Modality: "image"}}}}
	canonical := registration.EncodeManifest(m)
	immediate := bytes.Replace(canonical, []byte(`"readiness_deadline_ms"`), []byte(`"readiness_timeout_ms"`), 1)
	for name, raw := range map[string][]byte{"current": canonical, "immediate": immediate, "earliest": applicationV3PriorCanonical(canonical)} {
		g := &applicationV3Gateway{}
		_, recorded, err := g.decoder(raw, "fixture-program", nil)
		if err != nil {
			t.Fatal(name, err)
		}
		held := &heldApplicationManifest{Bytes: bytes.Clone(raw), Code: identity.Code{Trusted: true}, Proof: identity.ProofBound}
		fresh, digest, err := g.heldDeclaration(held, "fixture-program")
		if err != nil || digest != recorded || fresh.Host.ReadinessDeadlineMs != 30000 || len(fresh.Host.Arguments) != 1 {
			t.Fatalf("%s: the owned start does not read the kept declaration: %v %s %s", name, err, digest, recorded)
		}
		if name != "current" {
			if _, err := g.parse(raw, "fixture-program", held); err == nil {
				t.Fatal(name, "new admission accepted retained-only spelling")
			}
		}
	}
	unsigned := &heldApplicationManifest{Bytes: bytes.Clone(immediate)}
	if _, _, err := (&applicationV3Gateway{}).heldDeclaration(unsigned, "fixture-program"); err == nil {
		t.Fatal("a hold with neither signature nor kept image pin started")
	}
}

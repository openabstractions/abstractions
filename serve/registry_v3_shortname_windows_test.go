package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// An application registered under the 8.3 alias of its program keeps its
// image provenance in the record's own subject spelling, so the program index
// it writes reads back: the next publication and a restarted runtime both
// load it, and neither reports the store unavailable.
func TestApplicationV3ShortAliasProgramReloadsItsProvenance(t *testing.T) {
	folder := filepath.Join(t.TempDir(), "short alias program")
	if err := os.Mkdir(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(folder, "registered program.exe")
	other := filepath.Join(folder, "other.exe")
	for _, path := range []string{program, other} {
		if err := os.WriteFile(path, []byte("inert program"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	short, ok := shortPathName(t, program)
	if !ok {
		t.Skip("filesystem has no distinct DOS alias for this file")
	}
	key := identity.NormalizeSubjectProgram(short)
	if same, err := identity.SameFile(key, program); err != nil || !same || key == short {
		t.Fatalf("subject spelling of %q is %q (same file %t, %v)", short, key, same, err)
	}

	state := t.TempDir()
	decoder := (&applicationV3Gateway{}).decoder
	open := func() *applicationDirectory {
		t.Helper()
		d, err := openApplications(state, "account", func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
		if err != nil {
			t.Fatal(err)
		}
		if err := d.attachApplicationV3(t.Context(), decoder); err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := open()
	publish := func(name, program string) {
		t.Helper()
		manifest := registrationwire.Manifest{Version: "applications@3", Name: name, Title: "Fixture " + name, Host: registrationwire.HostRecipe{ReadinessDeadlineMs: 1000}, Offers: appmodel.CommandDeclaration{Version: "appmodel@1", Commands: []appmodel.DeclaredCommand{{Name: "note.read", Class: appmodel.CommandClassRead}}}}
		raw := registrationwire.EncodeManifest(&manifest)
		v, digest, err := decoder(raw, program, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, f := registration1459File(t, name, program, v.Title)
		f.Raw, f.Digest = raw, digest
		f.Accepted, f.Verified = registrationTime(d.now()), registrationTime(d.now())
		if out := d.publishApplicationV3(t.Context(), v, f, "operator", ""); out.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatalf("publish %s: %+v", name, out)
		}
	}
	publish("aliased", short)
	// The second publication reads the program index the first one wrote.
	publish("other", other)
	if source, ok := d.v3.programSources[key]; !ok || source.Program != key {
		t.Fatalf("retained provenance for %q: %+v %t", key, source, ok)
	}

	restarted := open()
	if source, ok := restarted.v3.programSources[key]; !ok || source.Program != key {
		t.Fatalf("provenance after restart for %q: %+v %t", key, source, ok)
	}
	if _, ok := restarted.v3.entries["aliased"]; !ok {
		t.Fatal("aliased registration lost on restart")
	}
}

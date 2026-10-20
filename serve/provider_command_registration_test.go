package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

func TestProviderTypedInputRefusesBeforeResolution(t *testing.T) {
	for _, raw := range []string{`{}`, `{"unknown":true}`, strings.Repeat(" ", 262145)} {
		var out, diagnostics bytes.Buffer
		if err := providerCommandInput([]string{"add", "-"}, strings.NewReader(raw), &out, &diagnostics); err == nil || out.Len() != 0 {
			t.Fatalf("invalid typed input reached registration: %v", err)
		}
	}
}

type providerRegistrationFixture struct {
	page                 registration.Page
	registered, replaced int
	expected             string
	spec                 registration.RegistrationSpec
}

func (f *providerRegistrationFixture) Registrations(context.Context, string, int64) (registration.Page, error) {
	return f.page, nil
}
func (f *providerRegistrationFixture) Register(context.Context, registration.RegistrationSpec) (registration.Change, error) {
	f.registered++
	return registration.Change{Outcome: registration.OutcomeApplied}, nil
}
func (f *providerRegistrationFixture) Replace(_ context.Context, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	f.replaced++
	f.expected = expected
	f.spec = spec
	return registration.Change{Outcome: registration.OutcomeConflict}, nil
}

func TestProviderTypedRefreshUsesCurrentRevisionAndPreservesConflict(t *testing.T) {
	program := filepath.Join(t.TempDir(), "producer.exe")
	spec := registration.RegistrationSpec{Program: program, Providers: []registration.ProviderFacet{{Name: "submitted"}}}
	f := &providerRegistrationFixture{page: registration.Page{Outcome: registration.OutcomePage,
		Registrations: []registration.Registration{{Spec: registration.RegistrationSpec{Program: program}, Revision: "observed-revision"}}}}
	change, err := writeProviderRegistration(context.Background(), f, spec)
	if err != nil || change.Outcome != registration.OutcomeConflict || f.replaced != 1 || f.registered != 0 || f.expected != "observed-revision" {
		t.Fatalf("conditional refresh: %+v %+v %v", f, change, err)
	}
	f = &providerRegistrationFixture{page: registration.Page{Outcome: registration.OutcomePage}}
	if _, err := writeProviderRegistration(context.Background(), f, spec); err != nil || f.registered != 1 || f.replaced != 0 {
		t.Fatalf("create: %+v %v", f, err)
	}
}

func TestProviderTypedUpsertPreservesUnrelatedFacets(t *testing.T) {
	program := filepath.Join(t.TempDir(), "producer.exe")
	f := &providerRegistrationFixture{page: registration.Page{Outcome: registration.OutcomePage,
		Registrations: []registration.Registration{{Revision: "one", Spec: registration.RegistrationSpec{Program: program,
			Providers:    []registration.ProviderFacet{{Name: "other"}, {Name: "selected", Endpoint: "old"}},
			Applications: []registration.ApplicationFacet{{Name: "kept-app"}}, Servers: []registration.ServerFacet{{Name: "kept-server"}}}}}}}
	_, err := writeProviderRegistration(context.Background(), f, registration.RegistrationSpec{Program: program,
		Providers: []registration.ProviderFacet{{Name: "selected", Endpoint: "new"}}})
	if err != nil || len(f.spec.Providers) != 2 || f.spec.Providers[0].Name != "other" || f.spec.Providers[1].Endpoint != "new" ||
		len(f.spec.Applications) != 1 || f.spec.Applications[0].Name != "kept-app" || len(f.spec.Servers) != 1 {
		t.Fatalf("upsert erased unrelated facets: %+v %v", f.spec, err)
	}
}

func TestProviderTypedUpsertRefusesAmbiguousProgramAndEmptyInput(t *testing.T) {
	program := filepath.Join(t.TempDir(), "producer.exe")
	f := &providerRegistrationFixture{page: registration.Page{Outcome: registration.OutcomePage,
		Registrations: []registration.Registration{{Spec: registration.RegistrationSpec{Program: program}}, {Spec: registration.RegistrationSpec{Program: program}}}}}
	for _, spec := range []registration.RegistrationSpec{{Program: program}, {Program: program, Providers: []registration.ProviderFacet{{Name: "submitted"}}}} {
		change, err := writeProviderRegistration(context.Background(), f, spec)
		if err != nil || change.Outcome != registration.OutcomeInvalid || f.registered+f.replaced != 0 {
			t.Fatalf("invalid upsert wrote: %+v %v", change, err)
		}
	}
}

package main

import (
	"context"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// hiddenEntryApps is a directory whose one entry the Panel can see only once
// its read rule on the name exists, as the applications service filters
// Observe by application.read.
type hiddenEntryApps struct {
	setupApplicationsFake
	entry    *fwire.ApplicationDescriptor
	readable func() bool
	log      *[]string
}

func (a *hiddenEntryApps) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	page := fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}
	if a.entry != nil && a.readable() {
		page.Applications = []fwire.ApplicationEntry{{Descriptor: *a.entry, Scope: fwire.ScopeLocal}}
	}
	return page, nil
}

func (a *hiddenEntryApps) Register(_ context.Context, d fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	*a.log = append(*a.log, "register")
	if a.entry != nil {
		return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeConflict, Reason: "name"}, nil
	}
	a.entry = &d
	return fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}, nil
}

// orderedReads records when the Panel's rules were written.
type orderedReads struct {
	log     *[]string
	written bool
}

func (r *orderedReads) allowPanelRead(context.Context, string) error {
	*r.log = append(*r.log, "rules")
	r.written = true
	return nil
}
func (r *orderedReads) clearPanelRead(context.Context, string) error { return nil }

// default-policy review item 3: Setup's confirmed registration writes the
// Panel's read and activate rules before Register; writing them again is
// harmless; and an entry the Panel could not read before, matching the
// manifest, reads as registered after the rules land instead of failing as
// a conflict.
func TestComfySetupWritesThePanelRulesBeforeRegister(t *testing.T) {
	descriptor := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(t.TempDir(), "python.exe"), Title: "ComfyUI", StartGuidance: "Open"}
	var log []string
	reads := &orderedReads{log: &log}
	apps := &hiddenEntryApps{readable: func() bool { return reads.written }, log: &log}
	created, err := registerSetupEntry(context.Background(), apps, reads, descriptor, nil)
	if err != nil || !created || len(log) != 2 || log[0] != "rules" || log[1] != "register" {
		t.Fatalf("fresh registration: created=%t %v order %v", created, err, log)
	}

	// The same name is registered already, and the Panel had no rule to see it.
	log = nil
	reads = &orderedReads{log: &log}
	apps = &hiddenEntryApps{entry: &descriptor, readable: func() bool { return reads.written }, log: &log}
	created, err = registerSetupEntry(context.Background(), apps, reads, descriptor, nil)
	if err != nil || created {
		t.Fatalf("hidden matching entry: created=%t %v order %v", created, err, log)
	}
	if len(log) == 0 || log[0] != "rules" {
		t.Fatalf("rules were not written first: %v", log)
	}
}

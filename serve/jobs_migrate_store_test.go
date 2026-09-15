package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	download "github.com/openabstractions/abstraction-download/go"
	job "github.com/openabstractions/abstraction-job/go"
)

func TestInspectNamesSeparateJobdStore(t *testing.T) {
	jobd := filepath.Join(t.TempDir(), "jobd-store")
	store, err := job.NewFileStore(jobd)
	if err != nil {
		t.Fatal(err)
	}
	spec := download.Spec{Sources: []download.Source{{Scheme: "https", Locator: "https://legacy.invalid/x"}}, Sink: download.Sink{Final: "models/x.bin"}}
	if _, err = download.Submit(store, spec); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(t.TempDir(), "state", "jobs")
	t.Setenv("MODELGET_STORE", jobd)

	var out bytes.Buffer
	if err := inspectLegacy(managed, false, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"jobd legacy store: " + jobd + " (from MODELGET_STORE) holds 1 job record(s).", "Migration cannot see these records", "jobd, dl or jobctl"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("inspect lacks %q:\n%s", want, out.String())
		}
	}
	var template, diagnostics bytes.Buffer
	if err := inspectLegacy(managed, true, &template, &diagnostics); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(template.String(), "jobd legacy store") || !strings.Contains(diagnostics.String(), "Migration cannot see these records") {
		t.Fatalf("template output must stay JSON: %s / %s", template.String(), diagnostics.String())
	}

	// The same directory as the managed root is not a separate store.
	t.Setenv("MODELGET_STORE", jobd)
	out.Reset()
	if err := inspectLegacy(jobd, false, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "jobd legacy store") {
		t.Fatalf("managed root reported as a separate store:\n%s", out.String())
	}
}

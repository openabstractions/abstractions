package main

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

func labelled(operation, label string, derived bool) api.OperationSnapshot {
	return api.OperationSnapshot{Receipt: api.Receipt{OperationID: operation, Identity: api.RequestIdentity{Key: operation + "-key", HistoryEpoch: "epoch"}},
		State: api.WorkStateRunning, Label: label, LabelDerived: derived}
}

// jobs list and jobs show say what each operation is fetching (JOB-A12), in
// text and JSON, with derived labels marked in the human form.
func TestJobsListAndShowPrintTheLabel(t *testing.T) {
	snapshots := []api.OperationSnapshot{
		labelled("derived", "huggingface.co · model.safetensors", true),
		labelled("caller", "org/tiny@abc · unet/model.safetensors", false),
		labelled("absent", "", false),
		labelled("hostile", "two\nlines", false),
	}
	var table bytes.Buffer
	if err := printSnapshots(&table, snapshots); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(table.String()), "\n")
	if len(lines) != 5 || !strings.Contains(lines[0], "LABEL") {
		t.Fatalf("table:\n%s", table.String())
	}
	for i, want := range []string{"huggingface.co · model.safetensors (derived)", "org/tiny@abc · unet/model.safetensors", "  -  ", "  -  "} {
		if !strings.Contains(lines[i+1], want) {
			t.Errorf("row %d lacks %q:\n%s", i+1, want, table.String())
		}
	}
	if strings.Contains(lines[2], "(derived)") {
		t.Errorf("a caller label is marked derived: %s", lines[2])
	}

	var shown bytes.Buffer
	if err := printSnapshot(&shown, &snapshots[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(shown.String(), "  label      huggingface.co · model.safetensors (derived)\n") {
		t.Fatalf("show: %q", shown.String())
	}

	var document bytes.Buffer
	if err := writeJSON(&document, projectSnapshot(&snapshots[0])); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(document.Bytes(), &fields); err != nil || fields["label"] != "huggingface.co · model.safetensors" || fields["label_derived"] != true {
		t.Fatalf("json: %s %v", document.Bytes(), err)
	}
	document.Reset()
	if err := writeJSON(&document, projectSnapshot(&snapshots[2])); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(document.Bytes(), []byte("label")) {
		t.Fatalf("an absent label was written: %s", document.Bytes())
	}
}

// An invalid --label is a usage refusal before anything is sent, and the help
// says the label is the caller's own text.
func TestDownloadLabelUsage(t *testing.T) {
	for _, label := range []string{"two\nlines", "tab\tinside", strings.Repeat("x", api.MaxLabelBytes+1)} {
		var out bytes.Buffer
		assertExit(t, downloadCommand([]string{"--label", label, "http://example.invalid/a"}, &out, io.Discard), exitUsage, "--label "+label)
		if out.Len() != 0 {
			t.Fatalf("--label %q wrote %q", label, out.String())
		}
	}
	var help bytes.Buffer
	if err := downloadCommand([]string{"--help"}, &help, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "--label TEXT") || !strings.Contains(help.String(), "your own text") {
		t.Fatalf("download help: %s", help.String())
	}
	// The label stays outside the request key (JOB-A2).
	plain := submissionFor(t, "https://example.invalid/model.bin")
	relabelled := plain
	relabelled.Label = "my model"
	if submissionKey(plain) != submissionKey(relabelled) {
		t.Fatal("the label changed the request key")
	}
}

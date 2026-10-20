package startprofile

import (
	"path/filepath"
	"testing"
)

func TestRegisteredProfileCanonical(t *testing.T) {
	folder := t.TempDir()
	p := Profile{Name: "claude-v1", Context: "inbox", Folder: folder, StateDir: filepath.Join(folder, "state"), CLIFileID: "held-native-file-id", Model: "approved-model", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	got, err := Parse(p.Arguments())
	if err != nil || got != p {
		t.Fatal(got, err)
	}
	if p.Fingerprint("helper") == p.Fingerprint("other") {
		t.Fatal("image absent from binding")
	}
	for _, mutate := range []func([]string) []string{
		func(a []string) []string { return a[:len(a)-2] },
		func(a []string) []string { a[2] = "opencode-v1"; return a },
		func(a []string) []string { a[4] = "peer text"; return a },
		func(a []string) []string { a[12] = "01000"; return a },
		func(a []string) []string { a[18] = "0"; return a },
		func(a []string) []string { return append(a, "--arbitrary") },
	} {
		if _, err := Parse(mutate(p.Arguments())); err == nil {
			t.Fatal("malformed profile accepted")
		}
	}
}

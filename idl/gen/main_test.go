package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHelpDoesNotNeedDefinition(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}} {
		var out bytes.Buffer
		if err := run(args, &out); err != nil || !strings.Contains(out.String(), "Languages:") {
			t.Fatalf("help %v: %v, %q", args, err, out.String())
		}
	}
}

func TestInvalidRequestDoesNotChangeOutputs(t *testing.T) {
	for _, extra := range [][]string{{"go", "cp"}, {"go", "go"}, {"-only=Doc", "-only=Doc"}, {"go", "--wrong"}} {
		t.Run(strings.Join(extra, "_"), func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "example.thrift")
			if err := os.WriteFile(src, []byte(head+doc), 0600); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, "output")
			old := filepath.Join(out, "go", "rec", "rec.go")
			if err := os.MkdirAll(filepath.Dir(old), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(old, []byte("previous output"), 0600); err != nil {
				t.Fatal(err)
			}
			var report bytes.Buffer
			if err := run(append([]string{src, out}, extra...), &report); err == nil {
				t.Fatal("invalid request succeeded")
			}
			got, err := os.ReadFile(old)
			if err != nil || string(got) != "previous output" || report.Len() != 0 {
				t.Fatalf("invalid request changed output: %q, %v, report %q", got, err, report.String())
			}
		})
	}
}

func TestLateBackendValidationDoesNotWriteEarlierBackend(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "example.thrift")
	valid, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, valid, 0600); err != nil {
		t.Fatal(err)
	}
	before := backends
	reached := false
	backends = append(append([]backend(nil), backends...), backend{
		lang: "broken", path: "broken.txt", emit: func(*Definition) string { reached = true; return "" }, emitsCode: true,
	})
	t.Cleanup(func() { backends = before })
	out := filepath.Join(dir, "output")
	var report bytes.Buffer
	if err := run([]string{src, out, "go", "broken"}, &report); err == nil {
		t.Fatal("broken emitter passed verification")
	}
	if !reached {
		t.Fatal("test did not reach late backend validation")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("partial output directory exists: %v", err)
	}
}

func TestServiceBackendSupportIsCheckedBeforeAnyOutput(t *testing.T) {
	profile, err := os.ReadFile("../test/preserve/preserve.thrift")
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "service.thrift")
	if err := os.WriteFile(source, []byte(strings.Split(string(profile), "struct Closed")[0]+serviceFixture), 0600); err != nil {
		t.Fatal(err)
	}
	profileDoc, err := os.ReadFile("../LANGUAGE.md")
	if err != nil {
		t.Fatal(err)
	}
	writeNamespaceFile(t, filepath.Dir(source), "LANGUAGE.md", string(profileDoc))
	before := backends
	backends = append(append([]backend(nil), backends...), backend{lang: "unsupported", path: "unsupported.txt", emit: func(*Definition) string { t.Fatal("unsupported emitter reached"); return "" }})
	t.Cleanup(func() { backends = before })
	for _, langs := range [][]string{nil, {"go", "python", "unsupported"}, {"cpp", "unsupported"}, {"go", "unsupported"}} {
		out := filepath.Join(t.TempDir(), "out")
		var report bytes.Buffer
		err := run(append([]string{source, out}, langs...), &report)
		if err == nil || !strings.Contains(err.Error(), "service generation is not implemented") {
			t.Fatalf("%v: %v", langs, err)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("partial service output exists: %v", err)
		}
		if report.Len() != 0 {
			t.Fatal("partial success reported")
		}
	}
	backends = before
	if err := run([]string{source, t.TempDir(), "go", "cpp", "python", "javascript", "rust", "docs"}, &bytes.Buffer{}); err != nil {
		t.Fatalf("supported service generation: %v", err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(data))
	if err != nil {
		t.Fatal(err)
	}
	var only []string
	for _, st := range def.Structs {
		only = append(only, st.Name)
	}
	var report bytes.Buffer
	out := t.TempDir()
	if err := run([]string{source, out, "-only=" + strings.Join(only, ","), "python"}, &report); err != nil {
		t.Fatalf("explicit data-only selection: %v", err)
	}
}

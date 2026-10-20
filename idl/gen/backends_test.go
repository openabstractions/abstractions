package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBackendsEmbedded is the freshness check for the gen --backends table:
// each document that embeds it must carry the current output byte for byte.
func TestBackendsEmbedded(t *testing.T) {
	want := backendsTable()
	for _, doc := range []string{"LANGUAGE.md", "README.md"} {
		path := filepath.Join("..", doc)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		got, ok := embeddedBackends(text)
		if !ok {
			t.Errorf("idl/%s lacks the gen --backends block %q ... %q", doc, backendsBegin, backendsEnd)
			continue
		}
		if got != want {
			t.Errorf("idl/%s embeds a stale gen --backends table; replace the block with the output of GOWORK=off go -C idl/gen run . --backends\nembedded:\n%s\ncurrent:\n%s", doc, got, want)
		}
	}
}

func embeddedBackends(text string) (string, bool) {
	start := strings.Index(text, backendsBegin)
	if start < 0 {
		return "", false
	}
	rest := text[start+len(backendsBegin):]
	end := strings.Index(rest, backendsEnd)
	if end < 0 {
		return "", false
	}
	return rest[:end], true
}

// TestBackendsTableFollowsBackends proves the table is derived: flipping one
// backend's dispatcher flag changes its row, so the embedded copies go stale.
func TestBackendsTableFollowsBackends(t *testing.T) {
	before := backendsTable()
	saved := backends
	t.Cleanup(func() { backends = saved })
	backends = append([]backend(nil), saved...)
	for i := range backends {
		if backends[i].lang == "python" {
			backends[i].dispatchers = true
		}
	}
	after := backendsTable()
	if after == before || !strings.Contains(after, "| python | yes | yes | yes | yes |") {
		t.Fatalf("dispatcher flag did not reach the table:\n%s", after)
	}
}

func TestBackendsFlag(t *testing.T) {
	var out bytes.Buffer
	if err := run([]string{"--backends"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != backendsTable() {
		t.Fatalf("gen --backends printed %q", out.String())
	}
	out.Reset()
	if err := run([]string{"--help"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Rust, JavaScript)") || !strings.Contains(out.String(), "--backends") {
		t.Fatalf("help omits JavaScript --no-ipc or --backends:\n%s", out.String())
	}
}

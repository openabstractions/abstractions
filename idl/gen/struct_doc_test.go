package main

import (
	"strings"
	"testing"
)

func TestStructDocRendersNothingWithoutADoc(t *testing.T) {
	for _, ann := range []map[string]string{nil, {}, {"doc": "   \n\t "}} {
		if got := structDoc(Struct{Name: "S", Ann: ann}, "// "); got != "" {
			t.Fatalf("%v rendered %q", ann, got)
		}
	}
}

func TestStructDocWrapsAndPrefixesEveryLine(t *testing.T) {
	doc := strings.Repeat("word ", 40) + "end\n  with\tspaces"
	for _, prefix := range []string{"// ", "# "} {
		got := structDoc(Struct{Name: "S", Ann: map[string]string{"doc": doc}}, prefix)
		if !strings.HasSuffix(got, "\n") {
			t.Fatalf("no trailing newline: %q", got)
		}
		lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
		if len(lines) < 2 {
			t.Fatalf("not wrapped: %q", got)
		}
		var words []string
		for _, line := range lines {
			if !strings.HasPrefix(line, prefix) || len(line) > structDocWidth || strings.HasSuffix(line, " ") {
				t.Fatalf("line %q", line)
			}
			words = append(words, strings.Fields(strings.TrimPrefix(line, prefix))...)
		}
		if strings.Join(words, " ") != strings.Join(strings.Fields(doc), " ") {
			t.Fatalf("words changed: %q", got)
		}
	}
}

func TestStructDocKeepsAWordLongerThanTheLine(t *testing.T) {
	long := strings.Repeat("x", 120)
	got := structDoc(Struct{Ann: map[string]string{"doc": "a " + long + " b"}}, "// ")
	if got != "// a\n// "+long+"\n// b\n" {
		t.Fatalf("%q", got)
	}
}

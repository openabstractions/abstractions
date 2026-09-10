package main

import (
	"os"
	"strings"
	"testing"
)

const repeated = "../test/repeated/repeated.thrift"

func repeatedDefinition(t *testing.T) *Definition {
	t.Helper()
	src, err := os.ReadFile(repeated)
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func TestARepeatedRecordGeneratesForEveryBackend(t *testing.T) {
	def := repeatedDefinition(t)
	for _, b := range backends {
		body := b.emit(def)
		if len(body) == 0 {
			t.Fatalf("%s emitted nothing", b.lang)
		}
		if body != b.emit(def) {
			t.Fatalf("two runs of the %s backend disagree", b.lang)
		}
		if err := b.verify(emitted{def: def, lang: b.lang, imports: b.imports, body: body, source: repeated, whole: body}); err != nil {
			t.Fatalf("%s: %v", b.lang, err)
		}
	}
}

// The element struct is reached through the list and nowhere else, so a
// selection that names the document alone does not close and the refusal is the
// line that would have worked.
func TestAStructReachedOnlyThroughAListIsCarriedWithIt(t *testing.T) {
	def := repeatedDefinition(t)
	_, err := selected(def, []string{def.Document})
	if err == nil {
		t.Fatal("a document was emitted without the struct its list repeats")
	}
	if !strings.Contains(err.Error(), "Source") {
		t.Fatalf("the refusal does not name the element struct: %v", err)
	}
	if _, err := selected(def, []string{def.Document, "Source"}); err != nil {
		t.Fatalf("a closed selection was refused: %v", err)
	}
}

func TestTheNewShapesReachEveryBackend(t *testing.T) {
	src, err := os.ReadFile(repeated)
	if err != nil {
		t.Fatal(err)
	}
	base, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	moved := map[string]string{
		"the depth limit":          strings.Replace(string(src), `depth_limit    = "5"`, `depth_limit    = "9"`, 1),
		"the duplicate-key policy": strings.Replace(string(src), `duplicate_keys = "refuse"`, `duplicate_keys = "last"`, 1),
		"the escape policy":        strings.Replace(string(src), `"minimal"`, `"ascii"`, 1),
	}
	for what, text := range moved {
		altered, err := parse(text)
		if err != nil {
			t.Fatalf("%s: %v", what, err)
		}
		for _, b := range backends {
			if b.emit(base) == b.emit(altered) {
				t.Errorf("%s ignores %s", b.lang, what)
			}
		}
	}
}

func TestShapesTheProfileStillRefuses(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"a list of a type nothing declares", head + `struct Doc {
  1: required list<Origin> sources
} (document = "true", unknown_fields = "refuse")
`, "does not encode list<Origin>"},
		{"a collection omitted when absent", head + `struct Doc {
  1: required string id
  2: optional list<string> tags (omit = "absent")
} (document = "true", unknown_fields = "refuse")
`, `a collection says omit = "zero"`},
		{"a map omitted when absent", head + `struct Doc {
  1: required string id
  2: optional map<string,string> labels (omit = "absent")
} (document = "true", unknown_fields = "refuse")
`, `a collection says omit = "zero"`},
		{"a repeated record in an envelope", head + `struct Inner {
  1: required string a
} (unknown_fields = "refuse")
struct Doc {
  1: required string id
} (document = "true", unknown_fields = "refuse")
struct Request {
  1: required string op
  2: optional list<Inner> inners (omit = "zero")
} (unknown_fields = "grant")
struct Response {
  1: optional string kind (omit = "zero")
} (unknown_fields = "grant")
enum Verdict {
  1: unknown_op
} (unknown = "grant")
protocol Store {
  1: write
} (request = "Request", response = "Response", operation = "op",
   verdict = "kind", verdicts = "Verdict", unknown_operation = "unknown_op")
`, "an envelope is a parameter list rather than a tree"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parse(c.src)
			if err == nil {
				t.Fatalf("accepted; wanted a refusal naming %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("refused with %q; wanted a message naming %q", err, c.want)
			}
		})
	}
}

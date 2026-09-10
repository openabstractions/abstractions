package main

import (
	"os"
	"strings"
	"testing"
)

const head = `encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "verbatim"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "64"
}
` + refusals

const refusals = `refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: bad_timestamp    (stage = "grammar")
   6: depth_exceeded   (stage = "grammar")
   7: duplicate_key    (stage = "grammar")
   8: duplicate_field  (stage = "structure")
   9: unknown_field    (stage = "structure")
  10: missing_field    (stage = "structure")
  11: trailing_bytes   (stage = "document")
  12: unknown_critical (stage = "derivation")
  13: not_a_subset     (stage = "derivation")
  14: content_mismatch (stage = "derivation")
}
`

const doc = `struct Doc {
  1: required string id
} (document = "true", unknown_fields = "refuse")
`

const vocabDoc = `struct Doc {
  1: required list<string> content
  2: optional list<string> critical (omit = "zero")
  3: optional string note (omit = "zero")
} (document = "true", unknown_fields = "refuse")
`

const pathDoc = head + `typedef string timestamp (write = "rfc3339-micros", read = "rfc3339-wide")
struct Recall {
  1: required string reason
} (unknown_fields = "refuse")
struct Lease {
  1: required string owner
  2: required i64 epoch
  3: required timestamp expires_at
  4: optional Recall recall (omit = "absent")
} (unknown_fields = "refuse")
struct Delegation {
  1: required string system
  2: optional bool delivered (omit = "zero")
} (unknown_fields = "refuse")
struct Doc {
  1: required list<string> content
  2: optional list<string> critical (omit = "zero")
  3: required string state
  4: optional json checkpoint (omit = "absent")
  5: required Lease lease
  6: optional Delegation delegation (omit = "absent")
} (document = "true", unknown_fields = "refuse")
`

func TestPredicatesResolve(t *testing.T) {
	def, err := parse(pathDoc + `vocabulary V {
  1: "a/base@1"     (when = "always")
  2: "a/recall@1"   (when = "lease.recall")
  3: "a/ranges@1"   (when = "checkpoint.verified", strip_critical = "true")
  4: "a/terminal@1" (when = "state", is = "complete, failed,cancelled")
} (of = "Doc", names = "content", critical = "critical")
`)
	if err != nil {
		t.Fatal(err)
	}
	terms := def.Vocab.Terms
	if len(terms[1].Path) != 2 || terms[1].Last().Name != "recall" || terms[1].Member != "" {
		t.Errorf("lease.recall resolved to %+v", terms[1])
	}
	if len(terms[2].Path) != 1 || terms[2].Member != "verified" {
		t.Errorf("checkpoint.verified resolved to %+v", terms[2])
	}
	if strings.Join(terms[3].Is, "|") != "complete|failed|cancelled" || terms[3].Last().Name != "state" {
		t.Errorf("state membership resolved to %+v", terms[3])
	}
	for _, b := range backends {
		if !b.emitsCode {
			continue
		}
		out := b.emit(def)
		for _, want := range []string{"complete", "cancelled", "verified", "recall"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s emits no test naming %q", b.lang, want)
			}
		}
	}
}

func TestRefusals(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"no encoding", doc, "no legal spelling"},
		{"encoding half stated", `encoding json {
  escape = "minimal"
  indent = "2"
}
` + doc, "does not say map_keys"},
		{"escape not in the profile", strings.Replace(head, `"minimal"`, `"whatever-the-library-does"`, 1) + doc,
			"this profile defines minimal, ascii"},
		{"optional without omit", head + `struct Doc {
  1: optional string id
} (document = "true", unknown_fields = "refuse")
`, "omitted when zero or when absent"},
		{"required with omit", head + `struct Doc {
  1: required string id (omit = "zero")
} (document = "true", unknown_fields = "refuse")
`, "cannot say when it is omitted"},
		{"a default value", head + `struct Doc {
  1: optional i32 n = 3 (omit = "zero")
} (document = "true", unknown_fields = "refuse")
`, "a default written back is not an absence"},
		{"service", head + doc + "service S { void ping() }\n", "behaviour is not in the schema"},
		{"a type the profile does not encode", head + `struct Doc {
  1: required double x
} (document = "true", unknown_fields = "refuse")
`, "does not encode double"},
		{"no document", head + `struct Doc {
  1: required string id
} (unknown_fields = "refuse")
`, "exactly one struct carries"},
		{"two documents", head + doc + `struct Other {
  1: required string id
} (document = "true", unknown_fields = "refuse")
`, "exactly one struct carries"},
		{"repeated field id", head + `struct Doc {
  1: required string id
  1: required string kind
} (document = "true", unknown_fields = "refuse")
`, "field id 1 appears twice"},
		{"enum with no unknown policy", head + doc + `enum E {
  1: a
}
`, "does not say what an unknown member does"},
		{"a struct omitted on zero", head + `struct Inner {
  1: required string a
} (unknown_fields = "refuse")
struct Doc {
  1: required string id
  2: optional Inner inner (omit = "zero")
} (document = "true", unknown_fields = "refuse")
`, "a struct has no zero to omit on"},

		{"struct with no unknown-field policy", head + `struct Doc {
  1: required string id
} (document = "true")
`, "never heard of"},
		{"encoding with no depth limit",
			strings.Replace(head, "  depth_limit    = \"64\"\n", "", 1) + doc, "does not say depth_limit"},
		{"a depth limit of zero",
			strings.Replace(head, `"64"`, `"0"`, 1) + doc, "depth_limit is a nesting depth"},
		{"one grammar where two are needed",
			head + "typedef string stamp (read = \"rfc3339-wide\")\n" + doc, "names one grammar"},
		{"a read grammar narrower than the write grammar",
			head + "typedef string stamp (read = \"rfc3339-micros\", write = \"rfc3339-wide\")\n" + doc,
			"narrower on read than on write"},
		{"a grammar the profile does not define",
			head + "typedef string stamp (read = \"iso8601\", write = \"rfc3339-micros\")\n" + doc,
			"not a grammar this profile defines"},
		{"a vocabulary term with no derivation", head + vocabDoc + `vocabulary V {
  1: "a/base@1"
} (of = "Doc", names = "content", critical = "critical")
`, "does not say when it is derived"},
		{"a vocabulary with no carriers", head + vocabDoc + `vocabulary V {
  1: "a/base@1" (when = "always")
} (of = "Doc")
`, "of, names and critical are all required"},
		{"a vocabulary derived from a struct that is not the document",
			head + vocabDoc + `struct Side {
  1: required list<string> content
  2: optional list<string> critical (omit = "zero")
} (unknown_fields = "refuse")
vocabulary V {
  1: "a/base@1" (when = "always")
} (of = "Side", names = "content", critical = "critical")
`, "not the document"},
		{"a vocabulary carried by a field that is not there", head + vocabDoc + `vocabulary V {
  1: "a/base@1" (when = "always")
} (of = "Doc", names = "content", critical = "absent")
`, "has no such field"},
		{"a critical list that is required", head + `struct Doc {
  1: required list<string> content
  2: required list<string> critical
  3: optional string note (omit = "zero")
} (document = "true", unknown_fields = "refuse")
vocabulary V {
  1: "a/base@1" (when = "always")
} (of = "Doc", names = "content", critical = "critical")
`, "requires nothing of its reader"},
		{"a term derived from a field that is always present", head + vocabDoc + `vocabulary V {
  1: "a/base@1" (when = "content")
} (of = "Doc", names = "content", critical = "critical")
`, `says when = "always"`},
		{"two vocabularies over one document", head + vocabDoc + `vocabulary V {
  1: "a/base@1" (when = "always")
} (of = "Doc", names = "content", critical = "critical")
vocabulary W {
  1: "a/base@1" (when = "always")
} (of = "Doc", names = "content", critical = "critical")
`, "two derivations are two truths"},
		{"a path into a field the struct lacks", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "lease.nobody")
} (of = "Doc", names = "content", critical = "critical")
`, `has no field "nobody"`},
		{"a path through an optional struct", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "delegation.delivered")
} (of = "Doc", names = "content", critical = "critical")
`, "walks required structs"},
		{"a path through a scalar", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "state.word")
} (of = "Doc", names = "content", critical = "critical")
`, "enters a struct or an opaque value"},
		{"a path two keys into an opaque value", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "checkpoint.verified.first")
} (of = "Doc", names = "content", critical = "critical")
`, "exactly one key"},
		{"a nested path ending at a required field", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "lease.owner")
} (of = "Doc", names = "content", critical = "critical")
`, `says when = "always"`},
		{"membership on a field that is not a string", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "lease.epoch", is = "1,2")
} (of = "Doc", names = "content", critical = "critical")
`, "words a string field may hold"},
		{"membership on a timestamp", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "lease.expires_at", is = "never")
} (of = "Doc", names = "content", critical = "critical")
`, "words a string field may hold"},
		{"membership inside an opaque value", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "checkpoint.mode", is = "fast")
} (of = "Doc", names = "content", critical = "critical")
`, "never a value inside an opaque one"},
		{"membership with an empty word", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "state", is = "complete,,failed")
} (of = "Doc", names = "content", critical = "critical")
`, "every word in it is non-empty"},
		{"membership naming a word twice", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "state", is = "complete,complete")
} (of = "Doc", names = "content", critical = "critical")
`, "twice in is"},
		{"words on a term that is always present", pathDoc + `vocabulary V {
  1: "a/x@1" (when = "always", is = "complete")
} (of = "Doc", names = "content", critical = "critical")
`, "names no words"},
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

func TestExampleGeneratesForEveryBackend(t *testing.T) {
	src, err := os.ReadFile("../../job/job.thrift")
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		if out := b.emit(def); len(out) == 0 {
			t.Fatalf("%s emitted nothing", b.lang)
		}
	}
}

func TestSeparatorFlagAppearsOnlyWhenItCanVary(t *testing.T) {
	requiredFirst, err := parse(head + `struct Doc {
  1: required string id
  2: optional string kind (omit = "zero")
} (document = "true", unknown_fields = "refuse")
`)
	if err != nil {
		t.Fatal(err)
	}
	optionalFirst, err := parse(head + `struct Doc {
  1: optional string id (omit = "zero")
  2: optional string kind (omit = "zero")
} (document = "true", unknown_fields = "refuse")
`)
	if err != nil {
		t.Fatal(err)
	}
	branches := func(out string) bool {
		return strings.Contains(out, "!first") || strings.Contains(out, "not first")
	}
	for _, b := range backends {
		if !b.emitsCode {
			continue
		}
		if branches(b.emit(requiredFirst)) {
			t.Errorf("%s tracks a separator flag that cannot vary", b.lang)
		}
		if !branches(b.emit(optionalFirst)) {
			t.Errorf("%s drops the separator flag where every field may be omitted", b.lang)
		}
	}
}

func TestEncodingDeclarationReachesEveryBackend(t *testing.T) {
	src, err := os.ReadFile("../../job/job.thrift")
	if err != nil {
		t.Fatal(err)
	}
	minimal, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	ascii, err := parse(strings.Replace(string(src), `"minimal"`, `"ascii"`, 1))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range backends {
		if b.emit(minimal) == b.emit(ascii) {
			t.Fatalf("%s ignores the declared escape policy", b.lang)
		}
	}
}

func TestReaderObligationsReachEveryBackend(t *testing.T) {
	src, err := os.ReadFile("../../job/job.thrift")
	if err != nil {
		t.Fatal(err)
	}
	base, err := parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	moved := map[string]string{
		"the unknown-field policy": strings.ReplaceAll(string(src), `unknown_fields = "refuse"`, `unknown_fields = "grant"`),
		"the duplicate-key policy": strings.Replace(string(src), `duplicate_keys = "refuse"`, `duplicate_keys = "last"`, 1),
		"the depth limit":          strings.Replace(string(src), `depth_limit    = "64"`, `depth_limit    = "8"`, 1),
		"a term's criticality":     strings.Replace(string(src), `(when = "intent")`, `(when = "intent", strip_critical = "true")`, 1),
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

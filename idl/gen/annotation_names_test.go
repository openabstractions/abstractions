package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Annotations the contract rules read (reader, catalogue, closed_by) are refused
// when misspelled or given a value the profile does not define, and so is any
// annotation name an enum, const or field does not take.
func TestAnnotationNamesAndValuesAreRefused(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"enum name", `enum E { 1: a } (unknown = "refuse", readr = "act")`, `enum E carries "readr"`},
		{"enum reader value", `enum E { 1: a } (unknown = "refuse", reader = "acts")`, `reader = "acts"`},
		{"field name", "struct S {\n  1: required string key (catalog = \"closed\")\n} (unknown_fields = \"refuse\")", `key carries "catalog"`},
		{"field catalogue value", "struct S {\n  1: required string key (catalogue = \"shut\")\n} (unknown_fields = \"refuse\")", `catalogue = "shut"`},
		{"field closed without rule", "struct S {\n  1: required string key (catalogue = \"closed\")\n} (unknown_fields = \"refuse\")", `names no rule`},
		{"field closed_by alone", "struct S {\n  1: required string key (closed_by = \"CFG-R2\")\n} (unknown_fields = \"refuse\")", `closed_by without catalogue`},
		{"field open with rule", "struct S {\n  1: required string key (catalogue = \"open\", closed_by = \"CFG-R2\")\n} (unknown_fields = \"refuse\")", `open catalogue and carries closed_by`},
		{"const name", `const list<string> keys = ["a"] (catalog = "open")`, `const keys carries "catalog"`},
		{"const catalogue value", `const list<string> keys = ["a"] (catalogue = "ajar")`, `catalogue = "ajar"`},
		{"struct catalogue value", "struct S {\n  1: required string key\n} (unknown_fields = \"refuse\", catalogue = \"sealed\")", `catalogue = "sealed"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parse(head + c.src + "\n")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("want an error containing %q, got %v", c.want, err)
			}
		})
	}
}

func TestAnnotationNamesTheProfileReadsParse(t *testing.T) {
	src := head + doc + `enum Shown { 1: a } (unknown = "grant", reader = "display")
enum Acted { 1: a (transcript = "A") } (unknown = "refuse", reader = "act")
enum Unmarked { 1: a } (unknown = "refuse")
const list<string> open_keys = ["a"] (catalogue = "open")
const list<string> closed_keys = ["a"] (catalogue = "closed", closed_by = "CFG-R2")
const list<string> plain = ["a"]
struct S {
  1: required string key (catalogue = "closed", closed_by = "ASK-K1")
  2: optional string note (omit = "zero", go.name = "Remark")
  3: required i64 kind (equals = "1", equals_refusal = "missing_field")
} (unknown_fields = "refuse", catalogue = "open")
`
	def, err := parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if got := def.Consts[1].Ann["closed_by"]; got != "CFG-R2" {
		t.Fatalf("const annotations were not kept: %q", got)
	}
	if def.Consts[2].Ann == nil || len(def.Consts[2].Ann) != 0 {
		t.Fatalf("a const without annotations carries %v", def.Consts[2].Ann)
	}
}

// Every definition the generator reads today still parses under the stricter names.
func TestEveryCurrentDefinitionParses(t *testing.T) {
	root := filepath.Join("..", "..")
	targets, err := os.ReadFile(filepath.Join(root, "scripts", "generate.targets"))
	if err != nil {
		t.Skip("scripts/generate.targets is not beside this generator:", err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(targets), "\n") {
		words := strings.Fields(strings.SplitN(line, "#", 2)[0])
		if len(words) == 0 || seen[words[0]] {
			continue
		}
		seen[words[0]] = true
		path := filepath.Join(root, filepath.FromSlash(words[0]))
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		dir := filepath.Dir(path)
		load := func(name string) (*Definition, error) {
			included, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			return parse(string(included))
		}
		if _, err := parseWithIncludes(string(src), load); err != nil {
			t.Errorf("%s: %v", words[0], err)
		}
	}
	if len(seen) == 0 {
		t.Fatal("no declared definitions found")
	}
}

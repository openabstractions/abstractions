package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests for [DEF-V1] and [DEF-V2], which hold [CONV-V1] and [CONV-V2].

const compatHead = `encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "verbatim"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "64"
}
refusal {
  1: malformed       (stage = "grammar")
  2: bad_string      (stage = "grammar")
  3: number_spelling (stage = "grammar")
  4: wrong_type      (stage = "grammar")
  5: depth_exceeded  (stage = "grammar")
  6: duplicate_key   (stage = "grammar")
  7: duplicate_field (stage = "structure")
  8: unknown_field   (stage = "structure")
  9: missing_field   (stage = "structure")
 10: bad_enum        (stage = "structure")
 11: trailing_bytes  (stage = "document")
}
`

// compatFixture is a published service whose result reaches a closed struct,
// an open struct, a nested closed struct, a closed enum and an open enum.
const compatFixture = compatHead + `
enum Outcome { 1: found 2: unknown 3: invalid 4: forbidden 5: unavailable }(unknown = "refuse", reader = "act")
enum Label { 1: red 2: green }(unknown = "grant", reader = "display")
struct Origin {
  1: required string rung
  2: required string path
}(unknown_fields = "refuse")
struct Note {
  1: required string text
}(unknown_fields = "grant")
struct Answer {
  1: required Outcome outcome
  2: optional list<Origin> origins(omit = "zero")
  3: optional Note note(omit = "absent")
  4: optional Label label(omit = "absent")
}(document = "true", unknown_fields = "refuse")
service Lookup {
  Answer Read(1: string id) (doc = "Read one record.")
}(wire_name = "example.test/reader@1", doc = "Reads records.")
`

func mustParse(t *testing.T, src string) *Definition {
	t.Helper()
	def, err := parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return def
}

func mustParseConfig(t *testing.T, src string) *Definition {
	t.Helper()
	def, err := parseWithIncludes(src, func(name string) (*Definition, error) {
		return loadDefinition(filepath.Join(filepath.Dir(productionFile(t, "abstraction-config/config.thrift")), name))
	})
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func TestCompatIgnoresEnumDocumentation(t *testing.T) {
	candidate := strings.Replace(compatFixture, `reader = "act")`, `reader = "act", doc = "[Outcome](https://openabstractions.github.io/reference.html#concept-job.wire-verdict) description.")`, 1)
	base := definitionHashes(mustParse(t, compatFixture))
	got := definitionHashes(mustParse(t, candidate))
	if len(got) != len(base) || got[0] != base[0] {
		t.Fatalf("enum documentation changed wire identity: %v -> %v", base, got)
	}
	if problems := compatProblems(t, compatFixture, candidate); len(problems) != 0 {
		t.Fatalf("enum documentation changed compatibility: %v", problems)
	}
}

func compatProblems(t *testing.T, published, candidate string) []string {
	t.Helper()
	return compatibility(mustParse(t, published), mustParse(t, candidate))
}

func wantProblem(t *testing.T, problems []string, pattern string) {
	t.Helper()
	re := regexp.MustCompile(pattern)
	for _, p := range problems {
		if re.MatchString(p) {
			return
		}
	}
	t.Fatalf("no problem matches %q in %q", pattern, problems)
}

func withCompatRefusals(source string, rows []Refusal) string {
	start := strings.Index(source, "refusal {")
	end := start + strings.Index(source[start:], "}") + 1
	var block strings.Builder
	block.WriteString("refusal {\n")
	for index, row := range rows {
		fmt.Fprintf(&block, " %d: %s (stage = %q)\n", index+1, row.Word, row.Stage)
	}
	block.WriteByte('}')
	return source[:start] + block.String() + source[end:]
}

func TestCompatRefusalWordsStagesAndRelativePrecedence(t *testing.T) {
	published := mustParse(t, compatFixture)
	for _, change := range []struct {
		name    string
		edit    func([]Refusal) []Refusal
		problem string
	}{
		{"removed", func(rows []Refusal) []Refusal { return append(rows[:1], rows[2:]...) }, `refusal bad_string was removed`},
		{"renamed", func(rows []Refusal) []Refusal { rows[1].Word = "renamed_string"; return rows }, `refusal bad_string was removed`},
		{"precedence", func(rows []Refusal) []Refusal { rows[1], rows[2] = rows[2], rows[1]; return rows }, `refusals changed relative precedence`},
		{"stage", func(rows []Refusal) []Refusal { rows[len(rows)-1].Stage = "derivation"; return rows }, `refusal trailing_bytes changed stage`},
	} {
		t.Run(change.name, func(t *testing.T) {
			rows := change.edit(append([]Refusal(nil), published.Refusals...))
			wantProblem(t, compatProblems(t, compatFixture, withCompatRefusals(compatFixture, rows)), change.problem)
			dir := t.TempDir()
			publishedPath, candidatePath := filepath.Join(dir, "published.thrift"), filepath.Join(dir, "candidate.thrift")
			for path, source := range map[string]string{publishedPath: compatFixture, candidatePath: withCompatRefusals(compatFixture, rows)} {
				if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var output bytes.Buffer
			if err := run([]string{"--compat", publishedPath, candidatePath}, &output); err == nil || !regexp.MustCompile(change.problem).MatchString(err.Error()) {
				t.Fatalf("--compat did not refuse the known change: %v", err)
			}
		})
	}
}

func TestCompatRefusalAllowsStagedInsertionAndNewVersion(t *testing.T) {
	before := mustParse(t, compatFixture)
	rows := append([]Refusal(nil), before.Refusals[:len(before.Refusals)-1]...)
	rows = append(rows, Refusal{Word: "bad_value", Stage: "structure"}, before.Refusals[len(before.Refusals)-1])
	candidate := withCompatRefusals(compatFixture, rows)
	if problems := compatProblems(t, compatFixture, candidate); len(problems) != 0 {
		t.Fatalf("staged insertion changed known precedence: %v", problems)
	}
	if got, want := definitionHashes(mustParse(t, candidate)), definitionHashes(before); len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("refusal compatibility checking changed definition hashes: %v -> %v", want, got)
	}
	// Contiguous IDs remain derived ranks; inserting before trailing_bytes
	// must not be confused with a serialized numeric error identity.
	after := mustParse(t, candidate)
	if after.Refusals[len(after.Refusals)-1].ID == before.Refusals[len(before.Refusals)-1].ID {
		t.Fatal("fixture did not move derived rank")
	}
	rows[0].Word = "replacement"
	changed := strings.Replace(withCompatRefusals(compatFixture, rows), "reader@1", "reader@2", 1)
	if problems := compatProblems(t, compatFixture, changed); len(problems) != 0 {
		t.Fatalf("retired service version was compared: %v", problems)
	}
}

func TestCompatRefusalChecksReachedIncludedDefinition(t *testing.T) {
	child := compatHead + "struct Note { 1: required string text }(document=\"true\", unknown_fields=\"refuse\")\n"
	rows := append([]Refusal(nil), mustParse(t, child).Refusals...)
	rows[len(rows)-1].Stage = "derivation"
	changed := withCompatRefusals(child, rows)
	root := "include \"child.thrift\"\n" + compatHead + "struct Answer { 1: required child.Note note }(document=\"true\", unknown_fields=\"refuse\")\nservice Lookup { Answer Read() }(wire_name=\"example.test/included@1\")\n"
	load := func(source string) *Definition {
		def, err := parseWithIncludes(root, func(string) (*Definition, error) { return parse(source) })
		if err != nil {
			t.Fatal(err)
		}
		return def
	}
	wantProblem(t, compatibility(load(child), load(changed)), `refusal child\.trailing_bytes changed stage`)
}

func TestCompatRefusesClosedMemberAddedToPublishedStruct(t *testing.T) {
	candidate := strings.Replace(compatFixture, "  2: required string path\n}(unknown_fields = \"refuse\")",
		"  2: required string path\n  3: optional string location(omit = \"absent\")\n}(unknown_fields = \"refuse\")", 1)
	if candidate == compatFixture {
		t.Fatal("fixture edit did not apply")
	}
	problems := compatProblems(t, compatFixture, candidate)
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %q", problems)
	}
	wantProblem(t, problems, `^example\.test/reader@1: closed struct Origin gained member location; .*new service version$`)
}

func TestCompatAcceptsClosedMemberUnderNewServiceVersion(t *testing.T) {
	candidate := strings.Replace(compatFixture, "  2: required string path\n}(unknown_fields = \"refuse\")",
		"  2: required string path\n  3: optional string location(omit = \"absent\")\n}(unknown_fields = \"refuse\")", 1)
	candidate = strings.Replace(candidate, "reader@1", "reader@2", 1)
	if problems := compatProblems(t, compatFixture, candidate); len(problems) != 0 {
		t.Fatalf("a new service version may carry the member: %q", problems)
	}
}

func TestCompatKeepsTheOldVersionWhileItIsServed(t *testing.T) {
	// The same struct reached from @1 and @2: @1 is still published, so the
	// member is refused until @2 takes a struct of its own or @1 is retired.
	candidate := strings.Replace(compatFixture, "  2: required string path\n}(unknown_fields = \"refuse\")",
		"  2: required string path\n  3: optional string location(omit = \"absent\")\n}(unknown_fields = \"refuse\")", 1)
	candidate += `service LookupNext {
  Answer Read(1: string id)
}(wire_name = "example.test/reader@2")
`
	wantProblem(t, compatProblems(t, compatFixture, candidate), `^example\.test/reader@1: closed struct Origin gained member location`)
}

func TestCompatOpenStructTakesOptionalMembersOnly(t *testing.T) {
	optional := strings.Replace(compatFixture, "  1: required string text\n}", "  1: required string text\n  2: optional string author(omit = \"absent\")\n}", 1)
	if problems := compatProblems(t, compatFixture, optional); len(problems) != 0 {
		t.Fatalf("an open struct may gain an optional member: %q", problems)
	}
	required := strings.Replace(compatFixture, "  1: required string text\n}", "  1: required string text\n  2: required string author\n}", 1)
	wantProblem(t, compatProblems(t, compatFixture, required), `struct Note gained required member author`)
}

func TestCompatEnums(t *testing.T) {
	closed := strings.Replace(compatFixture, "5: unavailable }", "5: unavailable 6: not_found }", 1)
	wantProblem(t, compatProblems(t, compatFixture, closed), `closed enum Outcome gained member not_found`)
	open := strings.Replace(compatFixture, "2: green }", "2: green 3: blue }", 1)
	if problems := compatProblems(t, compatFixture, open); len(problems) != 0 {
		t.Fatalf("an open enum may gain a member: %q", problems)
	}
	removed := strings.Replace(compatFixture, "2: green }", "}", 1)
	wantProblem(t, compatProblems(t, compatFixture, removed), `enum Label lost member green`)
	policy := strings.Replace(compatFixture, `2: green }(unknown = "grant", reader = "display")`, `2: green }(unknown = "refuse", reader = "act")`, 1)
	wantProblem(t, compatProblems(t, compatFixture, policy), `enum Label changed what its reader does`)
}

func TestCompatMethods(t *testing.T) {
	added := strings.Replace(compatFixture, "  Answer Read(1: string id) (doc = \"Read one record.\")\n",
		"  Answer Read(1: string id) (doc = \"Read one record.\")\n  Answer ReadAgain(1: string id)\n", 1)
	if problems := compatProblems(t, compatFixture, added); len(problems) != 0 {
		t.Fatalf("a service version may gain a method: %q", problems)
	}
	changed := strings.Replace(compatFixture, "Answer Read(1: string id)", "Answer Read(1: string id, 2: string why)", 1)
	wantProblem(t, compatProblems(t, compatFixture, changed), `method Read changed its arguments or result`)
	removed := strings.Replace(compatFixture, "  Answer Read(1: string id) (doc = \"Read one record.\")\n", "  Answer Find(1: string id)\n", 1)
	wantProblem(t, compatProblems(t, compatFixture, removed), `method Read was removed`)
	member := strings.Replace(compatFixture, "  1: required string rung\n", "  1: required i64 rung\n", 1)
	wantProblem(t, compatProblems(t, compatFixture, member), `struct Origin changed member rung`)
}

func TestHashNamesTheWireMeaning(t *testing.T) {
	base := definitionHashes(mustParse(t, compatFixture))
	if len(base) != 1 || base[0][0] != "example.test/reader@1" || !regexp.MustCompile(`^sha256:[0-9a-f]{64}$`).MatchString(base[0][1]) {
		t.Fatalf("hash row %q", base)
	}
	cosmetic := strings.Replace(compatFixture, `(doc = "Read one record.")`, `(doc = "Reads exactly one record.")`, 1)
	cosmetic = strings.Replace(cosmetic, "struct Note {\n  1: required string text\n}(unknown_fields = \"grant\")\n", "", 1)
	cosmetic = strings.Replace(cosmetic, "enum Label", "// a comment\nstruct Note {\n  1: required string text\n}(unknown_fields = \"grant\")\nenum Label", 1)
	if got := definitionHashes(mustParse(t, cosmetic)); got[0] != base[0] {
		t.Fatalf("docs, comments and declaration order moved the hash: %q against %q", got, base)
	}
	added := strings.Replace(compatFixture, "  Answer Read(1: string id) (doc = \"Read one record.\")\n",
		"  Answer Read(1: string id) (doc = \"Read one record.\")\n  Answer ReadAgain(1: string id)\n", 1)
	if got := definitionHashes(mustParse(t, added)); got[0][1] == base[0][1] {
		t.Fatal("a new method left the hash unchanged")
	}
	open := strings.Replace(compatFixture, "2: green }", "2: green 3: blue }", 1)
	if got := definitionHashes(mustParse(t, open)); got[0][1] == base[0][1] {
		t.Fatal("a new enum member left the hash unchanged")
	}
}

// A writer emits a struct's members and a call's arguments in declaration
// order, so a reorder changes every published byte: it is refused under the
// published version and moves the hash. An enum is written as its word, so
// reordering its members changes neither.
func TestCompatOrder(t *testing.T) {
	base := definitionHashes(mustParse(t, compatFixture))
	swapped := strings.Replace(compatFixture, "  1: required string rung\n  2: required string path\n",
		"  1: required string path\n  2: required string rung\n", 1)
	if swapped == compatFixture {
		t.Fatal("fixture edit did not apply")
	}
	problems := compatProblems(t, compatFixture, swapped)
	if len(problems) != 1 {
		t.Fatalf("want one problem, got %q", problems)
	}
	wantProblem(t, problems, `^example\.test/reader@1: struct Origin reordered its members`)
	if got := definitionHashes(mustParse(t, swapped)); got[0][1] == base[0][1] {
		t.Fatal("a member reorder left the hash unchanged")
	}
	if problems := compatProblems(t, compatFixture, strings.Replace(swapped, "reader@1", "reader@2", 1)); len(problems) != 0 {
		t.Fatalf("a new service version may reorder: %q", problems)
	}
	// An optional member inserted between two kept ones keeps their order.
	inserted := strings.Replace(compatFixture, "struct Note {\n  1: required string text\n}",
		"struct Note {\n  2: optional string author(omit = \"absent\")\n  1: required string text\n}", 1)
	if problems := compatProblems(t, compatFixture, inserted); len(problems) != 0 {
		t.Fatalf("an inserted optional member is not a reorder: %q", problems)
	}

	twoArgs := strings.Replace(compatFixture, "Answer Read(1: string id)", "Answer Read(1: string id, 2: string why)", 1)
	argsSwapped := strings.Replace(compatFixture, "Answer Read(1: string id)", "Answer Read(2: string why, 1: string id)", 1)
	wantProblem(t, compatProblems(t, twoArgs, argsSwapped), `method Read changed its arguments or result`)
	if definitionHashes(mustParse(t, twoArgs))[0][1] == definitionHashes(mustParse(t, argsSwapped))[0][1] {
		t.Fatal("an argument reorder left the hash unchanged")
	}

	enumSwapped := strings.Replace(compatFixture, "{ 1: found 2: unknown 3: invalid", "{ 2: unknown 1: found 3: invalid", 1)
	if enumSwapped == compatFixture {
		t.Fatal("enum fixture edit did not apply")
	}
	if problems := compatProblems(t, compatFixture, enumSwapped); len(problems) != 0 {
		t.Fatalf("an enum reorder changes no byte: %q", problems)
	}
	if got := definitionHashes(mustParse(t, enumSwapped)); got[0] != base[0] {
		t.Fatalf("an enum reorder moved the hash: %q against %q", got, base)
	}
}

// ServiceErrorCode is open: a code may be added within a version, and a
// published code stays declared. Both changes move the hash.
func TestCompatErrorCodes(t *testing.T) {
	published := strings.Replace(compatFixture, "service Lookup {", "const list<string> lookup_error_codes = [\"gone\", \"busy\"]\nservice Lookup {", 1)
	published = strings.Replace(published, `wire_name = "example.test/reader@1"`, `wire_name = "example.test/reader@1", error_codes = "lookup_error_codes"`, 1)
	base := definitionHashes(mustParse(t, published))
	added := strings.Replace(published, `["gone", "busy"]`, `["gone", "busy", "stale"]`, 1)
	if problems := compatProblems(t, published, added); len(problems) != 0 {
		t.Fatalf("an open error vocabulary may gain a code: %q", problems)
	}
	if definitionHashes(mustParse(t, added))[0][1] == base[0][1] {
		t.Fatal("an added error code left the hash unchanged")
	}
	removed := strings.Replace(published, `["gone", "busy"]`, `["busy"]`, 1)
	wantProblem(t, compatProblems(t, published, removed), `^example\.test/reader@1: error code gone was removed$`)
	if definitionHashes(mustParse(t, removed))[0][1] == base[0][1] {
		t.Fatal("a removed error code left the hash unchanged")
	}
}

// The production configuration definition carries the rename C8 names
// (Origin.rung to Origin.location). Adding the member under the published
// wire names is refused for every service that reaches Origin.
func TestCompatRefusesTheProductionRenameWithoutAVersion(t *testing.T) {
	path := productionFile(t, "abstraction-config/config.thrift")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	published := mustParseConfig(t, string(data))
	if problems := compatibility(published, published); len(problems) != 0 {
		t.Fatalf("a definition is compatible with itself: %q", problems)
	}
	edited := strings.Replace(string(data), "  2: required string path\n} (unknown_fields = \"refuse\", doc=\"Per-key provenance",
		"  2: required string path\n  3: optional string location(omit = \"absent\")\n} (unknown_fields = \"refuse\", doc=\"Per-key provenance", 1)
	if edited == string(data) {
		t.Fatal("config.thrift no longer has the Origin struct this test edits")
	}
	problems := compatibility(published, mustParseConfig(t, edited))
	wantProblem(t, problems, `^abstraction\.config/reader@1: closed struct Origin gained member location`)
}

func TestCompatFollowsIncludedRecords(t *testing.T) {
	dir := t.TempDir()
	model := compatHead + `struct Ref {
  1: required string name
}(document = "true", unknown_fields = "refuse")
`
	consumer := compatHead + `include "model.thrift"
enum Outcome { 1: found 2: unknown 3: invalid 4: forbidden 5: unavailable }(unknown = "refuse", reader = "act")
struct Answer {
  1: required Outcome outcome
  2: optional model.Ref ref(omit = "absent")
}(document = "true", unknown_fields = "refuse")
service Lookup {
  Answer Read(1: string id)
}(wire_name = "example.test/reader@1")
`
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("model.thrift", model)
	published, err := loadDefinition(write("consumer.thrift", consumer))
	if err != nil {
		t.Fatal(err)
	}
	write("model.thrift", strings.Replace(model, "  1: required string name\n", "  1: required string name\n  2: optional string digest(omit = \"absent\")\n", 1))
	candidate, err := loadDefinition(filepath.Join(dir, "consumer.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, compatibility(published, candidate), `closed struct model\.Ref gained member digest`)
}

func TestCompatCommandLine(t *testing.T) {
	dir := t.TempDir()
	published := filepath.Join(dir, "published.thrift")
	candidate := filepath.Join(dir, "candidate.thrift")
	if err := os.WriteFile(published, []byte(compatFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(compatFixture, "5: unavailable }", "5: unavailable 6: not_found }", 1)
	if err := os.WriteFile(candidate, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := run([]string{"--compat", published, candidate}, &out)
	if err == nil || !strings.Contains(err.Error(), "closed enum Outcome gained member not_found") {
		t.Fatalf("--compat accepted a closed member: %v", err)
	}
	out.Reset()
	if err := run([]string{"--compat", published, published}, &out); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^example\.test/reader@1 sha256:[0-9a-f]{64}\n$`).MatchString(out.String()) {
		t.Fatalf("--compat output %q", out.String())
	}
	var hash bytes.Buffer
	if err := run([]string{"--hash", published}, &hash); err != nil || hash.String() != out.String() {
		t.Fatalf("--hash %q (%v) against --compat %q", hash.String(), err, out.String())
	}
	if err := run([]string{"--hash"}, &hash); err == nil {
		t.Fatal("--hash without a definition was accepted")
	}
}

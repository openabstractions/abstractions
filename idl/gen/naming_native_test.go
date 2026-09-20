package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Record-only definitions with flat protocol encoders compile as library
// crates with warnings denied: no flag assigned and never read, no suppression.
func TestRustRecordDefinitionsCompileWithoutWarnings(t *testing.T) {
	rust := rustServiceCompiler(t)
	for _, def := range []string{"../test/repeated/repeated.thrift", "../test/preserve/preserve.thrift", "../../openabstractions-flat/abstraction-job/job.thrift"} {
		if _, err := os.Stat(def); err != nil {
			t.Logf("skip %s: %v", def, err)
			continue
		}
		t.Run(filepath.Base(def), func(t *testing.T) {
			dir := t.TempDir()
			var report bytes.Buffer
			if err := run([]string{def, dir, "rust"}, &report); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(rust, "--edition=2021", "--crate-type=rlib", "--crate-name=record", "-D", "warnings", "-F", "nonstandard_style", filepath.Join(dir, "rs", "rec.rs"), "-o", filepath.Join(dir, "librecord.rlib"))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

func TestNativeRustCppGoNames(t *testing.T) {
	for _, c := range []struct{ in, snake, screaming, upper, goName, goLocal string }{
		{"Decide", "decide", "DECIDE", "Decide", "Decide", "decide"},
		{"DecideFor", "decide_for", "DECIDE_FOR", "DecideFor", "DecideFor", "decideFor"},
		{"policy_revision", "policy_revision", "POLICY_REVISION", "PolicyRevision", "PolicyRevision", "policyRevision"},
		{"operation_id", "operation_id", "OPERATION_ID", "OperationId", "OperationID", "operationID"},
		{"ttl_ms", "ttl_ms", "TTL_MS", "TtlMs", "TTLMs", "ttlMs"},
		{"sha256_digest", "sha256_digest", "SHA256_DIGEST", "Sha256Digest", "SHA256Digest", "sha256Digest"},
		{"OAServiceFrame", "oa_service_frame", "OA_SERVICE_FRAME", "OaServiceFrame", "OAServiceFrame", "oaServiceFrame"},
		{"request_ids", "request_ids", "REQUEST_IDS", "RequestIds", "RequestIDs", "requestIDs"},
		{"url", "url", "URL", "Url", "URL", "url"},
	} {
		if got := snakeName(c.in); got != c.snake {
			t.Errorf("snakeName(%q)=%q, want %q", c.in, got, c.snake)
		}
		if got := screamingName(c.in); got != c.screaming {
			t.Errorf("screamingName(%q)=%q, want %q", c.in, got, c.screaming)
		}
		if got := upperCamelName(c.in); got != c.upper {
			t.Errorf("upperCamelName(%q)=%q, want %q", c.in, got, c.upper)
		}
		if got := goName(c.in); got != c.goName {
			t.Errorf("goName(%q)=%q, want %q", c.in, got, c.goName)
		}
		if got := goLocalName(c.in); got != c.goLocal {
			t.Errorf("goLocalName(%q)=%q, want %q", c.in, got, c.goLocal)
		}
	}
	for in, want := range map[string]string{"bool": "bool_", "requires": "requires_", "value": "value"} {
		if got := cppIdent(in); got != want {
			t.Errorf("cppIdent(%q)=%q, want %q", in, got, want)
		}
	}
	if got := rustIdent("Match"); got != "match_" {
		t.Errorf("rustIdent(Match)=%q", got)
	}
	if got := goParam(Field{Name: "type"}, nil); got != "type_" {
		t.Errorf("goParam(type)=%q", got)
	}
	if got := goParam(Field{Name: "result"}, goReservedLocals); got != "result_" {
		t.Errorf("goParam(result)=%q", got)
	}
	f := Field{Name: "ref", Ann: map[string]string{"rust.name": "reference", "cpp.name": "ref_value"}}
	if f.Ident("rust") != "reference" || f.Ident("cpp") != "ref_value" || f.Ident("go") != "Ref" {
		t.Fatal("per-language override lost", f.Ident("rust"), f.Ident("cpp"), f.Ident("go"))
	}
}

// A generated crate carries only the helpers it reaches: pruning removes an
// unreferenced private function and keeps a public one and everything it calls.
func TestRustPruneKeepsReachableItems(t *testing.T) {
	body := "use std::collections::BTreeMap;\n\nfn helper() -> i32 {\n    1\n}\n\nfn orphan() -> i32 {\n    helper()\n}\n\n// a sentence naming unused_by_code keeps nothing alive\nfn unused_by_code() {}\n\npub fn api() -> i32 {\n    let _ = \"orphan\";\n    helper()\n}\n"
	got := rsPrune(body)
	for _, gone := range []string{"fn orphan", "fn unused_by_code", "BTreeMap"} {
		if strings.Contains(got, gone) {
			t.Fatalf("pruned body keeps %q:\n%s", gone, got)
		}
	}
	for _, kept := range []string{"fn helper", "pub fn api"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("pruned body lost %q:\n%s", kept, got)
		}
	}
}

// Closed vocabularies are native enumerations in Rust, C++ and Go, with no
// member at the zero value in C++ and no Default in Rust, so an unset outcome
// is refused rather than read as the first member.
func TestClosedVocabulariesAreNativeEnumerations(t *testing.T) {
	s, e := parse(enumHead() + enumFixture)
	if e != nil {
		t.Fatal(e)
	}
	rust, cpp, goSrc := genRust(s), genCpp(s), strings.Join(strings.Fields(genGo(s)), " ")
	for _, want := range []string{"pub enum Scope {", "pub scope: Scope,", "pub open: String,", "pub mod open {"} {
		if !strings.Contains(rust, want) {
			t.Fatalf("Rust lacks %q", want)
		}
	}
	if strings.Contains(rust, "#[derive(Clone, Debug, Default, PartialEq, Eq)]\npub struct Record") {
		t.Fatal("Rust record with a required closed vocabulary derives Default")
	}
	for _, want := range []string{"enum class Scope : std::int32_t {", "    Local = 1,", "Scope scope{};", "std::string open;", "inline constexpr std::string_view kOpenKnown"} {
		if !strings.Contains(cpp, want) {
			t.Fatalf("C++ lacks %q", want)
		}
	}
	for _, want := range []string{"type Scope uint32", "ScopeLocal Scope = 1", "func ParseScope(word string) (Scope, bool)", "Scope Scope", "type Open string", "OpenKnown Open = \"known/value\"", "Open Open"} {
		if !strings.Contains(goSrc, want) {
			t.Fatalf("Go lacks %q", want)
		}
	}
}

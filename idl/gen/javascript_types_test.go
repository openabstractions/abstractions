package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

const typesFixture = `enum Outcome {
  1: accepted
  2: not_granted
} (unknown = "refuse")
enum Cause {
  1: other
} (unknown = "grant")
struct Entry {
  1: required string entry_name
  2: optional map<string,string> labels (omit = "zero")
}(unknown_fields="refuse")
struct Result {
  1: required Outcome outcome
  2: optional Entry entry (omit = "absent")
  3: required list<Entry> entries
  4: required i64 total_bytes
  5: optional binary data (omit = "absent")
  6: optional Cause cause (omit = "zero")
}(document="true",unknown_fields="refuse")
service Catalog {
  Result Lookup(1: string entry_name, 2: i64 max_bytes)
  oneway void Touch(1: Entry entry)
}(wire_name="example.catalog/catalog@1")
`

func TestJavaScriptDeclarationsMatchTheModule(t *testing.T) {
	s, err := parse(head + typesFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateServiceBackend(s, "javascript"); err != nil {
		t.Fatal(err)
	}
	body := genJS(s)
	decl, err := genJSTypes(s, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`export declare const Outcome: Readonly<{ Accepted: "accepted"; NotGranted: "not_granted" }>;`,
		"export type Outcome = (typeof Outcome)[keyof typeof Outcome];",
		"  entryName: string;\n  labels: { [key: string]: string };",
		"  outcome: Outcome;\n  entry: Entry | null;\n  entries: Entry[];\n  totalBytes: bigint;\n  data: Uint8Array | null;\n  cause: Cause | (string & {});",
		"  lookup(entryName: string, maxBytes: bigint): Promise<Result>;",
		"  touch(entry: Entry): Promise<void>;",
		`export declare const CatalogService: Readonly<{ wireName: "example.catalog/catalog@1"; Client: typeof CatalogClient }>;`,
		"export declare function decode(data: Uint8Array): Result;",
		"\nexport {};\n",
	} {
		if !strings.Contains(decl, want) {
			t.Errorf("declarations lack %q\n%s", want, decl)
		}
	}
	for _, internal := range []string{"OACatalogLookupArguments", "OAServiceFrame", "newOAServiceReply"} {
		if strings.Contains(decl, internal) {
			t.Errorf("declarations expose the internal record %s", internal)
		}
	}
	// Every public name of index.mjs is declared, and nothing else is a value.
	for _, name := range jsPublicNames(body) {
		if !strings.Contains(decl, " "+name+"(") && !strings.Contains(decl, " "+name+":") && !strings.Contains(decl, "class "+name+" ") {
			t.Errorf("index.mjs exports %s and the declarations do not declare it", name)
		}
	}
	if _, err := genJSTypes(s, body+"\nexport function undeclaredHelper() {}\n"); err == nil || !strings.Contains(err.Error(), "undeclaredHelper with no declaration") {
		t.Errorf("an exported name without a declaration was accepted: %v", err)
	}
	if _, err := genJSTypes(s, strings.Replace(body, "export class Refusal ", "class Refusal ", 1)); err == nil || !strings.Contains(err.Error(), "Refusal declared and not exported") {
		t.Errorf("a declaration for a name the module does not export was accepted: %v", err)
	}
	shadow, err := parse(head + `struct Promise {1: required string value}(document="true",unknown_fields="refuse")` + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := genJSTypes(shadow, genJS(shadow)); err == nil || !strings.Contains(err.Error(), "shadows the TypeScript built-in") {
		t.Errorf("a record named Promise was accepted: %v", err)
	}
}

// TestJavaScriptDeclarationsCompile runs the TypeScript compiler named by TSC
// (a typescript package's bin/tsc) over the fixture's declarations and a
// consumer that must and must not compile. Without TSC it is skipped.
func TestJavaScriptDeclarationsCompile(t *testing.T) {
	tsc := os.Getenv("TSC")
	if tsc == "" {
		t.Skip("TSC unset: no TypeScript compiler to judge the declarations")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	s, err := parse(head + typesFixture)
	if err != nil {
		t.Fatal(err)
	}
	body := genJS(s)
	decl, err := genJSTypes(s, body)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "internal.mjs", body)
	writeNamespaceFile(t, dir, "index.mjs", genJSPublic(s, body))
	writeNamespaceFile(t, dir, "index.d.mts", decl)
	writeNamespaceFile(t, dir, "consumer.mts", `import * as c from './index.mjs';
declare const transport: { exchangeFrame(f: Uint8Array): Promise<Uint8Array>; writeFrame(f: Uint8Array): Promise<void> };
const client = new c.CatalogClient(transport);
const result: c.Result = await client.lookup('name', 10n);
const outcome: c.Outcome = result.outcome === c.Outcome.NotGranted ? c.Outcome.Accepted : result.outcome;
const cause: string = result.cause;
const bytes: Uint8Array = c.encode({ ...c.newResult(), entries: [{ ...c.newEntry(), entryName: 'x', labels: { k: 'v' } }] });
// @ts-expect-error i64 is bigint
await client.lookup('name', 10);
// @ts-expect-error a closed vocabulary has no other word
const wrong: c.Outcome = 'maybe';
// @ts-expect-error the codec helpers are private
c.Out;
export { outcome, cause, bytes, wrong };
`)
	cmd := exec.Command(node, tsc, "--noEmit", "--strict", "--skipLibCheck", "false", "--target", "es2022", "--lib", "es2022", "--module", "nodenext", "--moduleResolution", "nodenext", "consumer.mts")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tsc: %v\n%s\n%s", err, out, decl)
	}
	// A wrong declaration must fail the same compile.
	writeNamespaceFile(t, dir, "index.d.mts", strings.Replace(decl, "totalBytes: bigint;", "totalBytes: number;", 1))
	writeNamespaceFile(t, dir, "mutation.mts", "import * as c from './index.mjs';\nexport const n: bigint = c.newResult().totalBytes;\n")
	cmd = exec.Command(node, tsc, "--noEmit", "--strict", "--target", "es2022", "--lib", "es2022", "--module", "nodenext", "--moduleResolution", "nodenext", "mutation.mts")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("tsc accepted a mutated declaration\n%s", out)
	}

}

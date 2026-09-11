package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreserveIsExplicitAndReservesItsStorage(t *testing.T) {
	input, err := os.ReadFile("../test/preserve/preserve.thrift")
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(input))
	if err != nil {
		t.Fatal(err)
	}
	if !def.PreservesUnknown() {
		t.Fatal("preserve not recorded")
	}
	for _, b := range backends {
		body := b.emit(def)
		if err := b.verify(emitted{def: def, lang: b.lang, imports: b.imports, body: body, source: "preserve.thrift", whole: body}); err != nil {
			t.Fatalf("%s: %v", b.lang, err)
		}
	}
	for _, field := range []string{"extras", "Extras", `id (go.name = "extras")`, `id (cpp.name = "extras")`, `id (python.name = "extras")`, `id (rust.name = "extras")`, `id (javascript.name = "extras")`} {
		_, err := parse(strings.Replace(string(input), "required string id", "required string "+field, 1))
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Fatalf("storage collision %s: %v", field, err)
		}
	}
	// The reserved synthetic member exists only in a preserving scope.
	plain := head + `struct Plain { 1: required string extras } (document = "true", unknown_fields = "grant")`
	if _, err := parse(plain); err != nil {
		t.Fatal(err)
	}
}

func TestProtocolEnvelopesRequireGrant(t *testing.T) {
	const schema = `struct Doc {
  1: required string id
} (document = "true", unknown_fields = "refuse")
struct Request {
  1: required string op
} (unknown_fields = "REQUEST_MODE")
struct Response {
  1: optional string kind (omit = "zero")
} (unknown_fields = "RESPONSE_MODE")
enum Verdict { 1: unknown_op } (unknown = "grant")
protocol Store { 1: write }
(request = "Request", response = "Response", operation = "op",
 verdict = "kind", verdicts = "Verdict", unknown_operation = "unknown_op")`
	for _, request := range []string{"grant", "preserve", "refuse"} {
		for _, response := range []string{"grant", "preserve", "refuse"} {
			input := strings.NewReplacer("REQUEST_MODE", request, "RESPONSE_MODE", response).Replace(head + schema)
			_, err := parse(input)
			if request == "grant" && response == "grant" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), `requires unknown_fields = "grant"`) {
				t.Fatalf("request=%s response=%s: %v", request, response, err)
			}
		}
	}
}

func TestGeneratedJSRetainsStringBOM(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: generated JavaScript BOM regression unproven")
	}
	input, err := os.ReadFile("../test/preserve/preserve.thrift")
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(input))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string]string{"rec.mjs": genJS(def), "check.mjs": preserveJSBOMTest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(node, "check.mjs")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated JavaScript BOM: %v\n%s", err, output)
	}
}

const preserveJSBOMTest = `import assert from 'node:assert/strict';
import {decode,encode} from './rec.mjs';
const enc = new TextEncoder();
for (const bom of ['\\uFEFF', '\uFEFF']) {
 const r=decode(enc.encode('{"id":"'+bom+'value","'+bom+'x":1,"x":2}'));
 assert.equal(r.id,'\uFEFFvalue');
 assert.deepEqual(Object.keys(r.extras),['\uFEFFx','x']);
 assert.equal(decode(encode(r)).id,'\uFEFFvalue');
}
assert.throws(()=>decode(enc.encode('{"id":"a","\uFEFFx":1,"\\uFEFFx":2}')),e=>e.word==='duplicate_key');
`

// Exercise the same direct-write counterexamples in the ordinary generator
// suite. The fixture runner adds all five backends and both duplicate policies.
func TestGeneratedPreserveWrites(t *testing.T) {
	input, err := os.ReadFile("../test/preserve/preserve.thrift")
	if err != nil {
		t.Fatal(err)
	}
	def, err := parse(string(input))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("rec.go", []byte(genGo(def)))
	write("go.mod", []byte("module preserve.test\n\ngo 1.22\n"))
	write("rec_test.go", []byte(preserveGoTest))
	fixtures, err := filepath.Abs("../test/preserve")
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off", "PRESERVE_FIXTURES="+fixtures)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated preservation checks: %v\n%s", err, output)
	}
}

const preserveGoTest = `package rec
import("bytes";"os";"path/filepath";"strings";"testing")
func TestDirectExtras(t *testing.T) {
 root:=os.Getenv("PRESERVE_FIXTURES")
 read:=func(name string) []byte { b,e:=os.ReadFile(filepath.Join(root,name));if e!=nil{t.Fatal(e)};return b }
 base:=read("base.json")
 for _,line:=range strings.Split(strings.TrimSpace(string(read("writes.tsv"))),"\n") {
  p:=strings.Split(strings.TrimSpace(line),"\t")
  t.Run(p[0],func(t *testing.T){
   value,err:=Decode(base);if err!=nil{t.Fatal(err)}
   target:=&value.Extras
   if p[1]=="child"{target=&value.Child.Extras}
   if p[1]=="repeated"{target=&value.Children[0].Extras}
   key:=p[2];if p[1]=="bad-key"{key=string([]byte{255})}
   *target=map[string]Raw{key:Raw(read(filepath.Join("raw",p[3])))}
   if p[4]!="ok" {
    defer func(){v:=recover();r,ok:=v.(*Refusal);if !ok||r.Word!=p[4]{t.Fatalf("want %s, got %v",p[4],v)}}()
    Encode(value);return
   }
   output:=Encode(value);again,err:=Decode(output);if err!=nil{t.Fatal(err)}
   if !bytes.Equal(output,Encode(again)){t.Fatal("not idempotent")}
   target=&again.Extras
   if p[1]=="child"{target=&again.Child.Extras}
   if p[1]=="repeated"{target=&again.Children[0].Extras}
   if _,ok:=(*target)[key];!ok{t.Fatal("extra dropped")}
  })
 }
}
`

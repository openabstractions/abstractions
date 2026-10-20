package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportedEnumsActualPipeline(t *testing.T) {
	dir := t.TempDir()
	profile, err := os.ReadFile("../LANGUAGE.md")
	if err != nil {
		t.Fatal(err)
	}
	writeNamespaceFile(t, dir, "LANGUAGE.md", string(profile))
	writeNamespaceFile(t, dir, "owner.thrift", enumHead()+`namespace * pipeline.owner enum Scope{1:local 2:remote}(unknown="refuse",reader="act") struct Child{1:required Scope scope}(document="true",unknown_fields="refuse")`)
	writeNamespaceFile(t, dir, "consumer.thrift", enumHead()+`namespace * pipeline.consumer include "owner.thrift" struct Record{1:required owner.Scope scope 2:required owner.Child child 3:required list<owner.Scope> scopes}(document="true",unknown_fields="refuse") service Echo {owner.Scope Pick(1:owner.Scope scope) list<owner.Scope> Many(1:list<owner.Scope> scopes)}(wire_name="pipeline/echo@1")`)
	for _, name := range []string{"owner", "consumer"} {
		args := []string{filepath.Join(dir, name+".thrift"), filepath.Join(dir, "out"), "--named-codecs"}
		if name == "consumer" {
			args = append(args, "--go-import=owner=pipeline.test/owner", "--js-import=owner=../owner/index.mjs", "--rust-import=owner=owner")
		}
		var report bytes.Buffer
		if err := run(append(args, "go", "cpp", "python", "rust", "javascript", "jsonschema", "docs"), &report); err != nil {
			t.Fatal(name, err)
		}
		if name == "consumer" {
			body, err := os.ReadFile(filepath.Join(dir, "out/go/pipeline/consumer/rec.go"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "func OAImported") || !strings.Contains(string(body), "func oaImported0Values()") {
				t.Fatal("imported enum helpers must stay private")
			}
			for _, selection := range []string{"Record", "Echo"} {
				selectedArgs := append(append([]string(nil), args...), "-only="+selection, "go", "cpp", "python", "rust", "javascript", "jsonschema", "docs")
				if err := run(selectedArgs, &report); err != nil {
					t.Fatal("selected", selection, err)
				}
			}
		}
	}
	owner, err := os.ReadFile(filepath.Join(dir, "out/go/pipeline/owner/rec.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(owner), "func ScopeValues() []Scope") {
		t.Fatal("public owner enum Values helper lost")
	}
}

func TestImportedEnumsSelection(t *testing.T) {
	s := importedEnumDefinition(t)
	if _, err := selected(s, []string{"Record"}); err == nil || !strings.Contains(err.Error(), "Scope, which Record needs") {
		t.Fatal("missing local enum accepted", err)
	}
	if _, err := selected(s, []string{"Record", "Scope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected(s, []string{"Echo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := selected(s, []string{"owner.Scope"}); err == nil {
		t.Fatal("imported enum became local surface")
	}
}

func TestImportedEnumJavaScriptNeedsNoPrivateOwnerModule(t *testing.T) {
	owner := mustParse(t, enumHead()+`namespace * standalone.owner enum Scope{1:local 2:remote}(unknown="refuse",reader="act") enum Open{1:known}(unknown="grant",reader="display") struct Document{1:required Scope scope}(document="true",unknown_fields="refuse")`)
	s, err := parseWithIncludes(enumHead()+`namespace * standalone.consumer include "owner.thrift"
enum Local{1:own}(unknown="refuse",reader="act")
enum Open{1:known}(unknown="grant",reader="display")
struct Record{1:required owner.Scope scope 2:required list<owner.Scope> scopes 3:required list<Local> locals 4:required list<Open> open 5:required list<owner.Open> imported_open}(document="true",unknown_fields="refuse")
service Echo{
 list<owner.Scope> Pick(1:list<owner.Scope> scopes,2:list<Local> locals,3:list<owner.Open> remote_open,4:list<Open> local_open)
 list<Local> Own(1:list<Local> locals)
 list<owner.Open> Opened(1:list<owner.Open> opened)
 list<Open> LocalOpened(1:list<Open> opened)
}(wire_name="standalone/echo@1")`, func(string) (*Definition, error) { return owner, nil })
	if err != nil {
		t.Fatal(err)
	}
	s.NamedCodecs = true
	s.JSImports = map[string]string{"owner": "@fixture/public-owner"}
	var body string
	for _, b := range backends {
		if b.lang == "javascript" {
			body = mustEmitIncluded(t, b, s)
		}
	}
	if strings.Contains(body, "@fixture/public-owner") {
		t.Fatal("enum-only owner acquired a private runtime dependency")
	}
	decl, err := genJSTypes(s, body)
	if err != nil || !strings.Contains(decl, `from "@fixture/public-owner"`) {
		t.Fatalf("public defining type import lost: %v\n%s", err, decl)
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.mjs", body)
	writeNamespaceFile(t, dir, "test.mjs", `import assert from 'node:assert/strict';import * as r from './rec.mjs';
const value={scope:'local',scopes:['remote'],locals:['own'],open:['future'],importedOpen:['remote-future']};
assert.deepEqual(r.decodeRecord(r.encodeRecord(value)),value);
const refused=e=>e.word==='bad_enum';
for(const invalid of [{scope:'future'},{scopes:['future']},{locals:['future']}]){
 const bad={...value,...invalid};const raw=JSON.stringify({scope:bad.scope,scopes:bad.scopes,locals:bad.locals,open:bad.open,imported_open:bad.importedOpen});
 assert.throws(()=>r.decodeRecord(Buffer.from(raw)),refused,'direct reader: '+raw);
 assert.throws(()=>r.encodeRecord(bad),refused,'direct writer: '+raw);
}
let calls=0;let answer=['remote'];let received;
const client=new r.EchoClient({exchangeFrame:async bytes=>{
 calls++;const frame=JSON.parse(new TextDecoder().decode(bytes));received=frame;
 // Construct reply bytes independently of every generated enum writer.
 return new TextEncoder().encode('{"version":1,"service":"standalone/echo@1","method":'+JSON.stringify(frame.method)+',"ok":true,"payload":{"value":'+JSON.stringify(answer)+'}}');
}});
for(const args of [[['future'],['own'],[],[]],[['local'],['future'],[],[]]]){
 await assert.rejects(client.pick(...args),refused);assert.equal(calls,0,'invalid argument reached transport');
}
assert.deepEqual(await client.pick(['local'],['own'],['remote-future'],['local-future']),['remote']);
assert.equal(calls,1);assert.deepEqual(received.arguments.remote_open,['remote-future']);assert.deepEqual(received.arguments.local_open,['local-future']);
answer=['future'];await assert.rejects(client.pick(['local'],['own'],[],[]),refused);assert.equal(calls,2);
await assert.rejects(client.own(['future']),refused);assert.equal(calls,2,'local invalid argument reached transport');
await assert.rejects(client.own(['own']),refused);assert.equal(calls,3,'local invalid reply was not read');
answer=['own'];assert.deepEqual(await client.own(['own']),['own']);
answer=['remote-future'];assert.deepEqual(await client.opened(['remote-future']),['remote-future']);
answer=['local-future'];assert.deepEqual(await client.localOpened(['local-future']),['local-future']);`)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(node, "test.mjs")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("enum-only owner %v\n%s", err, out)
	}
}
func TestImportedEnumsLocalProtocol(t *testing.T) {
	dependency, err := parse(enumHead() + `enum Verdict{1:unknown_op}(unknown="grant") struct Doc{1:required string id}(document="true",unknown_fields="refuse")`)
	if err != nil {
		t.Fatal(err)
	}
	for _, qualified := range []bool{false, true} {
		declaration, name := `enum Verdict{1:unknown_op}(unknown="grant")`, "Verdict"
		if qualified {
			declaration = ""
			name = "owner.Verdict"
		}
		source := enumHead() + `include "owner.thrift" ` + declaration + ` struct Doc{1:required string id}(document="true",unknown_fields="refuse") struct Request{1:required string op}(unknown_fields="grant") struct Response{1:optional string kind(omit="zero")}(unknown_fields="grant") protocol Store{1:write}(request="Request",response="Response",operation="op",verdict="kind",verdicts="` + name + `",unknown_operation="unknown_op")`
		_, err := parseWithIncludes(source, func(string) (*Definition, error) { return dependency, nil })
		if qualified {
			if err == nil || !strings.Contains(err.Error(), "does not declare as an enum") {
				t.Fatal("qualified protocol admitted", err)
			}
		} else if err != nil {
			t.Fatal("local protocol refused", err)
		}
	}
}

func TestImportedEnumsFiveBackendConsumers(t *testing.T) {
	s := importedEnumDefinition(t)
	s.NamedCodecs = true
	s.Imports[0].Def.NamedCodecs = true
	s.GoImports["owner"] = "imported.test/imported/owner"
	s.JSImports["owner"] = "../owner/index.mjs"
	dir := t.TempDir()
	for _, def := range []*Definition{s.Imports[0].Def, s} {
		for _, b := range backends {
			if b.lang != "go" && b.lang != "cpp" && b.lang != "python" && b.lang != "rust" && b.lang != "javascript" {
				continue
			}
			if err := validateIncludes(def, b.lang); err != nil {
				t.Fatal(err)
			}
			body := mustEmitIncluded(t, b, def)
			rel, body := namespacedOutput(b, def.Namespaces, body)
			writeNamespaceFile(t, dir, rel, body)
			if public, ok := publicPath(b.lang, rel); ok {
				writeNamespaceFile(t, dir, public, publicModules[b.lang].emit(def, body))
			}
			if b.lang == "javascript" {
				decl, err := genJSTypes(def, body)
				if err != nil {
					t.Fatal(err)
				}
				writeNamespaceFile(t, dir, jsTypesPath(rel), decl)
			}
		}
	}
	base := `{"scope":"local","child":{"scope":"remote"},"scopes":["remote"],"local":"own","open":"future"}`
	writeNamespaceFile(t, dir, "cases.txt", base+"\n"+strings.Replace(base, `"scope":"local"`, `"scope":"unknown"`, 1)+"\n"+strings.Replace(base, `"scope":"remote"`, `"scope":"unknown"`, 1)+"\n"+strings.Replace(base, `["remote"]`, `["unknown"]`, 1)+"\n")
	want := "accept\nbad_enum\nbad_enum\nbad_enum\n"
	invoke := func(t *testing.T, command []string, where string, check bool) {
		t.Helper()
		c := exec.Command(command[0], command[1:]...)
		c.Dir = filepath.Join(dir, where)
		c.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "PYTHONPATH="+filepath.Join(dir, "py"))
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", command, err, out)
		}
		if check && strings.ReplaceAll(string(out), "\r\n", "\n") != want {
			t.Fatalf("%v verdicts:\n%s", command, out)
		}
	}
	t.Run("go", func(t *testing.T) {
		writeNamespaceFile(t, dir, "go/go.mod", "module imported.test\n\ngo 1.26\n")
		writeNamespaceFile(t, dir, "go/main.go", `package main
import("fmt";"os";"strings";r "imported.test/imported/consumer";o "imported.test/imported/owner")
func main(){raw,_:=os.ReadFile("../cases.txt");for _,line:=range strings.Split(strings.TrimSpace(string(raw)),"\n"){v,e:=r.DecodeRecord([]byte(line));if e!=nil{fmt.Println(e.(*r.Refusal).Word);continue};var original o.Scope=v.Scope;_=original;if _,e=r.DecodeRecord(r.EncodeRecord(v));e!=nil{panic(e)};fmt.Println("accept")}}`)
		invoke(t, []string{"go", "run", "."}, "go", true)
	})
	t.Run("python", func(t *testing.T) {
		writeNamespaceFile(t, dir, "main.py", `from imported.consumer import _codec as r
from imported.owner import _codec as o
for line in open('cases.txt',encoding='utf-8'):
 try:
  v=r.decode_record(line.encode());assert type(v.scope) is o.Scope;r.decode_record(r.encode_record(v));print('accept')
 except r.Refusal as e:print(e.word)
`)
		invoke(t, []string{servicePython(t), "-B", "main.py"}, "", true)
	})
	t.Run("javascript", func(t *testing.T) {
		node, err := exec.LookPath("node")
		if err != nil {
			t.Fatal(err)
		}
		writeNamespaceFile(t, dir, "main.mjs", `import * as r from './js/imported/consumer/internal.mjs';import fs from 'node:fs';for(const line of fs.readFileSync('cases.txt','utf8').trim().split('\n')){try{const v=r.decodeRecord(Buffer.from(line));r.decodeRecord(r.encodeRecord(v));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}`)
		invoke(t, []string{node, "main.mjs"}, "", true)
	})
	t.Run("rust", func(t *testing.T) {
		rust, err := exec.LookPath("rustc")
		if err != nil {
			t.Fatal(err)
		}
		writeNamespaceFile(t, dir, "main.rs", `#[path="rs/imported/consumer/rec.rs"] mod consumer;
fn main(){for line in std::fs::read_to_string("cases.txt").unwrap().lines(){match consumer::decode_record_document(line.as_bytes()){Ok(v)=>{let _: &owner::Scope=&v.scope;consumer::decode_record_document(&consumer::encode_record_document(&v).unwrap()).unwrap();println!("accept")},Err(e)=>println!("{}",e.word)}}}`)
		invoke(t, []string{rust, "--edition=2021", "--crate-type=rlib", "--crate-name=owner", "rs/imported/owner/rec.rs", "-o", "libowner.rlib"}, "", false)
		invoke(t, []string{rust, "--edition=2021", "main.rs", "--extern", "owner=libowner.rlib", "-o", "rust.exe"}, "", false)
		invoke(t, []string{filepath.Join(dir, "rust.exe")}, "", true)
	})
	t.Run("cpp", func(t *testing.T) {
		cxx := os.Getenv("CXX")
		if cxx == "" {
			cxx, _ = exec.LookPath("g++")
		}
		if cxx == "" {
			t.Fatal("CXX missing")
		}
		writeNamespaceFile(t, dir, "main.cpp", `#include <imported/consumer/rec.h>
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line;while(std::getline(f,line)){try{auto v=imported::consumer::decode_record(line);imported::owner::Scope original=v.scope;(void)original;imported::consumer::decode_record(imported::consumer::encode_record(v));std::cout<<"accept\n";}catch(const imported::consumer::Refusal&e){std::cout<<e.word<<'\n';}}}`)
		exe := filepath.Join(dir, "cpp.exe")
		invoke(t, []string{cxx, "-std=c++17", "-Icpp", "main.cpp", "-o", exe}, "", false)
		invoke(t, []string{exe}, "", true)
	})
}

func importedEnumDefinition(t *testing.T) *Definition {
	t.Helper()
	dependency, err := parse(enumHead() + `namespace * imported.owner
 enum Scope {1: local 2: remote}(unknown="refuse",reader="act")
 enum Open {1: known}(unknown="grant",reader="display")
 struct Child {1:required Scope scope}(document="true",unknown_fields="refuse")`)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseWithIncludes(enumHead()+`namespace * imported.consumer
 include "owner.thrift"
 enum Scope {1: own}(unknown="refuse",reader="act")
 struct Record {1:required owner.Scope scope 2:required owner.Child child 3:required list<owner.Scope> scopes 4:required Scope local 5:optional owner.Scope maybe(omit="absent") 6:required owner.Open open}(document="true",unknown_fields="refuse")
 service Echo {owner.Scope Pick(1:owner.Scope scope)}(wire_name="test.imported/echo@1")`, func(string) (*Definition, error) { return dependency, nil })
	if err != nil {
		t.Fatal(err)
	}
	root.GoImports = map[string]string{"owner": "imported.test/owner"}
	root.JSImports = map[string]string{"owner": "./owner.mjs"}
	root.RustImports = map[string]string{"owner": "owner"}
	return root
}

func TestImportedEnumsResolutionAndCanonicalShape(t *testing.T) {
	s := importedEnumDefinition(t)
	if s.Enum("owner.Scope") == nil || s.Enum("Scope").Members[0].Name != "own" || s.Enum("missing.Scope") != nil || s.Enum("owner.Missing") != nil {
		t.Fatal("qualified/local resolution")
	}
	lowered := enumCarriers(importCarriers(s))
	if lowered.Structs[0].Fields[0].EnumType == nil || lowered.Structs[0].Fields[2].EnumType == nil || !lowered.Structs[0].Fields[2].EnumList {
		t.Fatal("imported enum carrier")
	}
	if len(lowered.Foreign) != 1 || len(lowered.ForeignEnums) != 2 {
		t.Fatal("record/enum identities conflated")
	}
	before := shapeOf(s.Services[0], s)
	dependency := s.Imports[0].Def
	dependency.Enums[0].Members[0].Name = "changed"
	after := shapeOf(s.Services[0], s)
	if before.decls["owner.Scope"].lines["local"] == after.decls["owner.Scope"].lines["local"] {
		t.Fatal("imported enum omitted from canonical service shape")
	}
}

func TestImportedEnumsAllEmitters(t *testing.T) {
	s := importedEnumDefinition(t)
	for _, lang := range []string{"go", "cpp", "python", "javascript", "rust", "jsonschema", "docs"} {
		t.Run(lang, func(t *testing.T) {
			if err := validateIncludes(s, lang); err != nil {
				t.Fatal(err)
			}
			var output string
			for _, b := range backends {
				if b.lang == lang {
					output = mustEmitIncluded(t, b, s)
				}
			}
			if output == "" {
				t.Fatal("no output")
			}
			if lang == "go" && strings.Contains(output, "func ParseOAImported") {
				t.Fatal("private imported parser exported")
			}
			if lang == "python" {
				for _, name := range pyPublicNames(output) {
					if strings.HasPrefix(name, "OAImported") || strings.HasPrefix(name, "ImportedEnum") {
						t.Fatal("temporary imported enum alias became public:", name)
					}
				}
			}
		})
	}
	for _, typ := range []string{"missing.Scope", "owner.Missing", "owner.other.Scope"} {
		_, err := parseWithIncludes(enumHead()+`include "owner.thrift" struct Record{1:required `+typ+` scope}(document="true",unknown_fields="refuse")`, func(string) (*Definition, error) { return s.Imports[0].Def, nil })
		if err == nil || !strings.Contains(err.Error(), "does not encode") {
			t.Fatalf("unresolved %s: %v", typ, err)
		}
	}
	for _, declaration := range []string{`typedef owner.Scope Alias struct Record{1:required Alias scope}`, `struct Record{1:required map<string,owner.Scope> scopes}`} {
		_, err := parseWithIncludes(enumHead()+`include "owner.thrift" `+declaration+`(document="true",unknown_fields="refuse")`, func(string) (*Definition, error) { return s.Imports[0].Def, nil })
		if err == nil {
			t.Fatal("unsupported profile form accepted", declaration)
		}
	}
	for _, declaration := range []string{`enum owner{1:local}(unknown="refuse") struct Record{1:required owner.Scope scope}`, `enum OAImported0{1:local}(unknown="refuse") struct Record{1:required owner.Scope scope}`} {
		d, err := parseWithIncludes(enumHead()+`namespace * collision.consumer include "owner.thrift" `+declaration+`(document="true",unknown_fields="refuse")`, func(string) (*Definition, error) { return s.Imports[0].Def, nil })
		if err == nil {
			d.GoImports = s.GoImports
			err = validateIncludes(d, "go")
		}
		if err == nil {
			t.Fatal("alias/carrier collision accepted", declaration)
		}
	}
}

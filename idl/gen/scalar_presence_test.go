package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const scalarPresenceFixture = `struct Doc {
 1:optional i32 small(omit="absent",range="-5..5")
 2:optional i64 count(omit="absent",range="-5..5")
 3:optional bool flag(omit="absent")
 4:optional i64 positive(omit="absent",range="1..5")
}(document="true",unknown_fields="refuse",max_encoded="80")`

func TestScalarPresenceSurfaces(t *testing.T) {
	s := mustParse(t, maxEncodedHead()+scalarPresenceFixture)
	ts, err := genJSTypes(s, genJS(s))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ body, want string }{
		{genPy(s), "small: int | None"}, {genPy(s), "flag: bool | None"},
		{genRust(s), "pub small: ::std::option::Option<i32>"}, {genRust(s), "pub flag: ::std::option::Option<bool>"},
		{genCpp(s), "std::optional<std::int64_t> count"}, {genCpp(s), "std::optional<bool> flag"},
		{ts, "small: number | undefined"}, {ts, "count: bigint | undefined"}, {ts, "flag: boolean | undefined"},
	} {
		if !strings.Contains(c.body, c.want) {
			t.Errorf("missing surface %q", c.want)
		}
	}
	for i, want := range []string{"*int32", "*int64", "*bool"} {
		if got := goType(s, s.Struct("Doc").Fields[i]); got != want {
			t.Fatalf("Go type %s, want %s", got, want)
		}
	}
	if err := checkNames(emitted{def: s, lang: "docs", body: genDocs(s)}); err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(jsonSchemaRecords(s)["Doc.schema.json"]), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema["required"]; ok {
		t.Fatal("presence scalars made required")
	}
	props := schema["properties"].(map[string]any)
	if props["positive"].(map[string]any)["minimum"] != float64(1) {
		t.Fatal("present-only range omitted from schema")
	}
	if props["count"].(map[string]any)["type"] != "integer" || props["flag"].(map[string]any)["type"] != "boolean" {
		t.Fatal(props)
	}
}

func TestScalarPresenceJavaScriptService(t *testing.T) {
	s := mustParse(t, maxEncodedHead()+scalarPresenceFixture+`service Presence {Doc Echo(1:Doc value)}(wire_name="test.presence/presence@1")`)
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.mjs", genJS(s))
	writeNamespaceFile(t, dir, "main.mjs", `import * as r from './rec.mjs';const enc=new TextEncoder(),dec=new TextDecoder();let calls=0;
const c=new r.PresenceClient({async exchangeFrame(b){calls++;const q=JSON.parse(dec.decode(b));return enc.encode(JSON.stringify({version:1,service:q.service,method:q.method,ok:true,payload:{value:q.arguments.value}}))}});
for(const present of [false,true]){const v=r.newDoc();if(present){v.small=0;v.count=0n;v.flag=false}const back=await c.echo(v);if(dec.decode(r.encode(back))!==dec.decode(r.encode(v)))throw Error('service presence lost')}
const before=calls;const v=r.newDoc();v.flag=null;try{await c.echo(v);throw Error('null accepted')}catch(e){if(!(e instanceof r.Refusal)||e.word!=='wrong_type'||calls!==before)throw e}
`)
	c := exec.Command("node", "main.mjs")
	c.Dir = dir
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("JS service: %v\n%s", err, out)
	}
	// Named record checks use the same presence rule as service checks.
	if !strings.Contains(jsNamedChecks(s), `item === undefined`) {
		t.Fatal("named check lost scalar absence")
	}
}

func TestScalarPresenceAliases(t *testing.T) {
	aliases := "typedef i32 Small\ntypedef i64 Count\ntypedef bool Flag\n"
	local := mustParse(t, maxEncodedHead()+aliases+strings.NewReplacer("i32 small", "Small small", "i64 count", "Count count", "bool flag", "Flag flag").Replace(scalarPresenceFixture))
	for i := 0; i < 3; i++ {
		if !scalarAbsent(local.Struct("Doc").Fields[i]) {
			t.Fatal("typedef lost scalar presence")
		}
	}
	imported, err := importedAliasFixture(t, `struct Doc {1:optional owner.Count count(omit="absent",range="0..9223372036854775807")}(document="true",unknown_fields="refuse")`)
	if err != nil {
		t.Fatal(err)
	}
	plain := mustParse(t, constraintHead+"namespace * alias.use\n"+`struct Doc {1:optional i64 count(omit="absent",range="0..9223372036854775807")}(document="true",unknown_fields="refuse")`)
	plain.Imports = imported.Imports
	for _, emit := range []func(*Definition) string{genGo, genPy, genJS, genRust, genCpp} {
		if emit(imported) != emit(plain) {
			t.Fatal("imported scalar typedef changed presence codec")
		}
	}
}

func TestScalarPresenceCompatibility(t *testing.T) {
	before := constraintHead + `struct Doc {1:optional i64 count(omit="zero")}(document="true",unknown_fields="refuse")
service Presence {Doc Read()}(wire_name="test.presence/presence@1")`
	after := strings.Replace(before, `omit="zero"`, `omit="absent"`, 1)
	if len(compatProblems(t, before, after)) == 0 {
		t.Fatal("published omission change accepted in same service version")
	}
	if definitionHashes(mustParse(t, before))[0][1] == definitionHashes(mustParse(t, after))[0][1] {
		t.Fatal("omission meaning absent from hash")
	}
}

func TestScalarPresenceAllFive(t *testing.T) {
	// This regression requires every runtime, including the C++ compile owner.
	for _, name := range []string{"go", "node", "rustc"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Fatal(err)
		}
	}
	s := mustParse(t, maxEncodedHead()+scalarPresenceFixture+`service Presence {Doc Echo(1:Doc value)}(wire_name="test.presence/presence@1")`)
	cases := []struct{ input, want string }{
		{`{}`, "accept"},
		{`{"small":0,"count":0,"flag":false}`, "accept"},
		{`{"small":5,"count":5,"flag":true,"positive":1}`, "accept"},
		{`{"small":-5,"count":-5,"flag":false,"positive":5}`, "accept"},
		{`{"positive":0}`, "bad_value"}, {`{"count":6}`, "bad_value"}, {`{"small":-6}`, "bad_value"},
		{`{"small":2147483648}`, "number_spelling"}, {`{"count":9223372036854775808}`, "number_spelling"},
		{`{"small":null}`, "wrong_type"}, {`{"count":null}`, "wrong_type"}, {`{"flag":null}`, "wrong_type"},
		{`{"small":false}`, "wrong_type"}, {`{"count":"0"}`, "wrong_type"}, {`{"flag":0}`, "wrong_type"},
		{`{"small":0,"small":1}`, "duplicate_field"}, {`{"flag":false,"flag":true}`, "duplicate_field"},
	}
	runScalarPresencePrograms(t, s, cases)
	t.Run("typedefs", func(t *testing.T) {
		source := "typedef i32 Small\ntypedef i64 Count\ntypedef bool Flag\n" + strings.NewReplacer("i32 small", "Small small", "i64 count", "Count count", "bool flag", "Flag flag").Replace(scalarPresenceFixture)
		runScalarPresencePrograms(t, mustParse(t, maxEncodedHead()+source), cases[:4])
	})
	t.Run("extrema", func(t *testing.T) {
		source := strings.Replace(scalarPresenceFixture, `range="-5..5"`, `range="-2147483648..2147483647"`, 1)
		source = strings.Replace(source, `range="-5..5"`, `range="-9223372036854775808..9223372036854775807"`, 1)
		runScalarPresencePrograms(t, mustParse(t, maxEncodedHead()+source), []struct{ input, want string }{
			{`{"small":-2147483648,"count":-9223372036854775808}`, "accept"},
			{`{"small":2147483647,"count":9223372036854775807}`, "accept"},
		})
	})
	t.Run("max_encoded", func(t *testing.T) {
		source := strings.Replace(scalarPresenceFixture, `max_encoded="80"`, `max_encoded="20"`, 1)
		runScalarPresencePrograms(t, mustParse(t, maxEncodedHead()+source), []struct{ input, want string }{
			{`{}`, "accept"}, {`{"small":0}`, "accept"},
			{`{"small":0,"count":0,"flag":false}`, "encoded_size"},
		})
	})
}

func runScalarPresencePrograms(t *testing.T, s *Definition, cases []struct{ input, want string }) {
	t.Helper()
	dir := t.TempDir()
	var inputs, wants []string
	for _, c := range cases {
		inputs = append(inputs, c.input)
		wants = append(wants, c.want)
	}
	for name, body := range map[string]string{
		"cases.txt": strings.Join(inputs, "\n") + "\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.mjs": genJS(s), "rec.h": genCpp(s), "rec.rs": genRust(s),
		"go.mod":  "module presence.test\n\ngo 1.22\n",
		"main.go": scalarPresenceGo, "main.py": scalarPresencePy, "main.mjs": scalarPresenceJS, "main.cpp": scalarPresenceCpp, "main.rs": scalarPresenceRust,
	} {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, strings.Join(wants, "\n")+"\nbad_value\n")
}

func TestScalarPresenceCrossLanguage(t *testing.T) {
	s := mustParse(t, maxEncodedHead()+scalarPresenceFixture)
	dir := t.TempDir()
	for name, body := range map[string]string{"rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.mjs": genJS(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "go.mod": "module presence.test\n\ngo 1.22\n"} {
		writeNamespaceFile(t, dir, name, body)
	}
	// Each implementation reads the previous implementation's actual output.
	writeNamespaceFile(t, dir, "value.json", `{"small":0,"count":0,"flag":false}`)
	writeNamespaceFile(t, dir, "main.go", `package main
import("os";r "presence.test/rec")
func main(){b,_:=os.ReadFile("value.json");v,e:=r.Decode(b);if e!=nil||v.Small==nil||*v.Small!=0||v.Count==nil||*v.Count!=0||v.Flag==nil||*v.Flag{panic("presence")};os.WriteFile("value.json",r.Encode(v),0600)}`)
	writeNamespaceFile(t, dir, "main.py", `import rec as r
v=r.decode(open('value.json','rb').read());assert v.small==0 and v.count==0 and v.flag is False and v.positive is None
open('value.json','wb').write(r.encode(v))`)
	writeNamespaceFile(t, dir, "main.mjs", `import * as r from './rec.mjs';import fs from 'node:fs';const v=r.decode(fs.readFileSync('value.json'));if(v.small!==0||v.count!==0n||v.flag!==false||v.positive!==undefined)throw Error('presence');fs.writeFileSync('value.json',r.encode(v));`)
	writeNamespaceFile(t, dir, "main.rs", `mod rec;fn main(){let v=rec::decode(&std::fs::read("value.json").unwrap()).unwrap();assert_eq!(v.small,Some(0));assert_eq!(v.count,Some(0));assert_eq!(v.flag,Some(false));assert_eq!(v.positive,None);std::fs::write("value.json",rec::encode(&v)).unwrap();}`)
	writeNamespaceFile(t, dir, "main.cpp", `#include "rec.h"
#include <fstream>
#include <iterator>
#include <cassert>
int main(){std::ifstream f("value.json");std::string b((std::istreambuf_iterator<char>(f)),{});auto v=rec::decode(b);assert(v.small==0&&v.count==0&&v.flag==false&&!v.positive);std::ofstream("value.json")<<rec::encode(v);}`)
	run := func(argv ...string) {
		t.Helper()
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", argv, err, out)
		}
	}
	run("go", "run", "main.go")
	run(servicePython(t), "-B", "main.py")
	run("node", "main.mjs")
	run("rustc", "--edition=2021", "main.rs", "-o", filepath.Join(dir, "rust.exe"))
	run(filepath.Join(dir, "rust.exe"))
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx = "g++"
	}
	args := []string{cxx, "-std=c++17", "main.cpp", "-o", filepath.Join(dir, "cpp.exe")}
	if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
		args = []string{cxx, "/nologo", "/EHsc", "/std:c++17", "main.cpp", "/Fe:" + filepath.Join(dir, "cpp.exe")}
	}
	run(args...)
	run(filepath.Join(dir, "cpp.exe"))
	run("go", "run", "main.go")
}

const scalarPresenceGo = `package main
import("bufio";"bytes";"fmt";"os";r "presence.test/rec")
func compact(b []byte)string{return string(bytes.Map(func(c rune)rune{if c==' '||c=='\n'||c=='\r'||c=='\t'{return -1};return c},b))}
func main(){f,_:=os.Open("cases.txt");defer f.Close();sc:=bufio.NewScanner(f);for sc.Scan(){v,e:=r.Decode(sc.Bytes());if e!=nil{fmt.Println(e.(*r.Refusal).Word);continue};b:=r.Encode(v);if compact(b)!=sc.Text(){panic("presence lost")};v,e=r.Decode(b);if e!=nil||!bytes.Equal(b,r.Encode(v)){panic("roundtrip")};fmt.Println("accept")};func(){defer func(){fmt.Println(recover().(*r.Refusal).Word)}();z:=int64(0);r.Encode(&r.Doc{Positive:&z})}()}
`
const scalarPresencePy = `import rec as r
for raw in open('cases.txt'):
 try:
  v=r.decode(raw.encode()); b=r.encode(v)
  assert b''.join(b.split())==raw.strip().encode(), 'presence lost'
  assert r.encode(r.decode(b))==b
  print('accept')
 except r.Refusal as e: print(e.word)
try:r.encode(r.Doc(positive=0))
except r.Refusal as e:print(e.word)
for field,bad in [('small',False),('count',True),('flag',0),('small','0'),('count',1.5),('flag','false')]:
 v=r.Doc();setattr(v,field,bad)
 try:r.encode(v)
 except r.Refusal as e:assert e.word=='wrong_type'
 else:raise AssertionError((field,bad))
`
const scalarPresenceJS = `import * as r from './rec.mjs';import fs from 'node:fs';const enc=new TextEncoder(),dec=new TextDecoder();
for(const raw of fs.readFileSync('cases.txt','utf8').trim().split('\n')){try{let v=r.decode(enc.encode(raw));const b=r.encode(v);if(dec.decode(b).replace(/\s/g,'')!==raw)throw Error('presence lost');if(dec.decode(r.encode(r.decode(b)))!==dec.decode(b))throw Error('roundtrip');console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
try{const v=r.newDoc();v.positive=0n;r.encode(v)}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}
for(const [field,bad] of [['small',false],['count',0],['flag',0],['small','0'],['small',1.5],['flag','false'],['small',null],['count',null],['flag',null]]){const v=r.newDoc();v[field]=bad;try{r.encode(v);throw Error('accepted invalid type')}catch(e){if(!(e instanceof r.Refusal)||e.word!=='wrong_type')throw e}}
`
const scalarPresenceCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
#include <cctype>
int main(){std::ifstream f("cases.txt");std::string raw;while(std::getline(f,raw)){try{auto v=rec::decode(raw);auto b=rec::encode(v);std::string compact;for(auto c:b)if(!std::isspace(static_cast<unsigned char>(c)))compact+=c;if(compact!=raw)throw std::runtime_error("presence lost");if(rec::encode(rec::decode(b))!=b)throw std::runtime_error("roundtrip");std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<"\n";}}try{rec::Doc v;v.positive=0;rec::encode(v);}catch(const rec::Refusal&e){std::cout<<e.word<<"\n";}}
`
const scalarPresenceRust = `mod rec;
fn main(){std::panic::set_hook(Box::new(|_|{}));for raw in std::fs::read_to_string("cases.txt").unwrap().lines(){match rec::decode(raw.as_bytes()){Ok(v)=>{let b=rec::encode(&v);let compact:String=String::from_utf8(b.clone()).unwrap().chars().filter(|c|!c.is_whitespace()).collect();assert_eq!(compact,raw,"presence lost");assert_eq!(rec::encode(&rec::decode(&b).unwrap()),b);println!("accept");},Err(e)=>println!("{}",e.word)}}let v=rec::Doc{positive:Some(0),..Default::default()};let e=std::panic::catch_unwind(||rec::encode(&v)).unwrap_err();println!("{}",e.downcast_ref::<rec::Refusal>().unwrap().word);}
`

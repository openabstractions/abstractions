package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const enumFixture = `enum Scope {1:local(wire="oa/local@1",label="Local") 2:remote 3:unknown}(unknown="refuse")
enum Open {1:known(wire="known/value")}(unknown="grant")
struct Child {1:required Scope scope}(unknown_fields="refuse")
struct Record {1:required Scope scope 2:required Open open 3:required Child child 4:optional Open maybe(omit="absent") 5:optional Scope choice(omit="absent") 6:optional Scope hint(omit="zero") 7:required list<Scope> scopes 8:required list<Open> opens}(document="true",unknown_fields="refuse")`

func enumHead() string {
	return strings.Split(head, "refusal")[0] + `refusal {
1:malformed(stage="grammar") 2:bad_string(stage="grammar") 3:number_spelling(stage="grammar") 4:wrong_type(stage="grammar") 5:depth_exceeded(stage="grammar") 6:duplicate_key(stage="grammar")
7:duplicate_field(stage="structure") 8:unknown_field(stage="structure") 9:missing_field(stage="structure") 10:bad_enum(stage="structure") 11:trailing_bytes(stage="document")
}`
}
func enumDefinition(t *testing.T) *Definition {
	t.Helper()
	s, e := parse(enumHead() + enumFixture)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestEnumSchema(t *testing.T) {
	s := enumDefinition(t)
	for _, source := range []string{strings.Replace(enumHead()+enumFixture, `document="true"`, `document="false"`, 1)} {
		if _, e := parse(source); e == nil {
			t.Fatal("unsupported enum collection or missing document accepted")
		}
	}
	if s.Struct("Record").Fields[0].Type != "Scope" {
		t.Fatal("lost schema type")
	}
	for _, source := range []string{
		strings.Replace(enumHead()+enumFixture, "2:remote", `2:remote(wire="oa/local@1")`, 1),
		strings.Replace(enumHead()+enumFixture, "2:remote", `2:remote(wire="")`, 1),
	} {
		if _, err := parse(source); err == nil {
			t.Fatal("duplicate or empty enum wire spelling accepted")
		}
	}
	if _, e := selected(s, []string{"Record", "Child", "Open"}); e == nil {
		t.Fatal("selection forgot enum")
	}
	for _, b := range backends {
		if b.emitsCode {
			body := b.emit(s)
			if e := b.verify(emitted{def: s, lang: b.lang, imports: b.imports, body: body, whole: body}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if s.Struct("Record").Fields[0].Type != "Scope" {
		t.Fatal("backend mutated schema")
	}
	if e := checkNames(emitted{def: s, lang: "docs", body: genDocs(s)}); e != nil {
		t.Fatal(e)
	}
}

func TestEnumCollectionNativeSurfaces(t *testing.T) {
	s := enumDefinition(t)
	ts, err := genJSTypes(s, genJS(s))
	if err != nil {
		t.Fatal(err)
	}
	wants := []struct{ body, text string }{
		{genGo(s), "Scopes []Scope"},
		{genGo(s), `return "oa/local@1", true`},
		{genCpp(s), "std::vector<Scope> scopes"},
		{genCpp(s), `case Scope::Local: return "oa/local@1"`},
		{genRust(s), "pub scopes: Vec<Scope>"},
		{genRust(s), `Self::Local => "oa/local@1"`},
		{genPy(s), "scopes: list[Scope]"},
		{genPy(s), `LOCAL = "oa/local@1"`},
		{genJS(s), `Local: "oa/local@1"`},
		{ts, "scopes: Scope[]"},
		{ts, "opens: Array<Open | (string & {})>"},
	}
	for _, want := range wants {
		if !strings.Contains(want.body, want.text) {
			t.Errorf("generated surface lacks %q", want.text)
		}
	}
	if !strings.Contains(genGo(s), `ScopeLocal`) || !strings.Contains(genGo(s), `return "oa/local@1", true`) {
		t.Fatal("Go member naming or wire alias lost")
	}
	if !strings.Contains(ts, `Local: "oa/local@1"`) || !strings.Contains(ts, `Known: "known/value"`) {
		t.Fatal("TypeScript member naming or wire alias lost")
	}
	// Closed collections cannot be confused with strings or another enum list.
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "go.mod", "module enum.collections\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "rec/rec.go", genGo(s))
	writeNamespaceFile(t, dir, "bad.go", `package bad
import r "enum.collections/rec"
var _ []r.Scope = []string{"local"}
var _ []r.Scope = []r.Open{r.OpenKnown}
`)
	c := exec.Command("go", "test", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, err := c.CombinedOutput(); err == nil || !strings.Contains(string(out), "cannot use") {
		t.Fatalf("plain strings or enum mixup compiled: %v\n%s", err, out)
	}
	if err := os.Remove(filepath.Join(dir, "bad.go")); err != nil {
		t.Fatal(err)
	}
	writeNamespaceFile(t, dir, "rec/json_test.go", `package rec
import("encoding/json";"testing")
func TestStandardJSONEnumWire(t *testing.T){
 b,e:=json.Marshal(ScopeLocal);if e!=nil||string(b)!="\"oa/local@1\""{t.Fatalf("marshal %s %v",b,e)}
 var v Scope;if e=json.Unmarshal(b,&v);e!=nil||v!=ScopeLocal{t.Fatalf("unmarshal %v %v",v,e)}
 for _,bad:=range []string{"1","null","\"local\"","\"future\""}{if json.Unmarshal([]byte(bad),&v)==nil{t.Fatalf("accepted %s",bad)}}
 if _,e=json.Marshal(Scope(99));e==nil{t.Fatal("invalid enum marshaled")}
 m:=map[Scope]string{ScopeLocal:"yes"};b,e=json.Marshal(m);if e!=nil||string(b)!="{\"oa/local@1\":\"yes\"}"{t.Fatalf("map marshal %s %v",b,e)}
 var back map[Scope]string;if e=json.Unmarshal(b,&back);e!=nil||back[ScopeLocal]!="yes"{t.Fatalf("map unmarshal %#v %v",back,e)}
 z:=struct{Scope Scope `+"`json:\"scope,omitempty\"`"+`}{ };b,e=json.Marshal(z);if e!=nil||string(b)!="{}"{t.Fatalf("zero omission %s %v",b,e)}
}
`)
	c = exec.Command("go", "test", "./rec")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("standard JSON enum wire: %v\n%s", err, out)
	}
}

func TestEnumListHelpersAreConditional(t *testing.T) {
	s, err := parse(enumHead() + `enum Scope {1:local}(unknown="refuse") struct Record {1:required Scope scope}(document="true",unknown_fields="refuse")`)
	if err != nil {
		t.Fatal(err)
	}
	for lang, body := range map[string]string{"go": genGo(s), "cpp": genCpp(s), "rust": genRust(s)} {
		if strings.Contains(body, "enumStrs") || strings.Contains(body, "enum_strs") {
			t.Errorf("%s emitted unused enum-list helper", lang)
		}
	}
}
func TestEnumFiveBackends(t *testing.T) {
	s := enumDefinition(t)
	dir := t.TempDir()
	base := `{"scope":"oa/local@1","open":"future","child":{"scope":"remote"},"scopes":["oa/local@1","remote"],"opens":["known/value","future"]}`
	cases := []string{base, `{"scope":"unknown","open":"","child":{"scope":"oa/local@1"},"maybe":"","choice":"unknown","hint":"oa/local@1","scopes":[],"opens":[]}`, strings.Replace(base, `"scope":"oa/local@1"`, `"scope":"bad"`, 1), strings.Replace(base, `"scope":"oa/local@1",`, "", 1), strings.Replace(base, `"scope":"oa/local@1"`, `"scope":1`, 1), strings.Replace(base, `"remote"`, `"bad"`, 1), strings.Replace(base, `"open":"future"`, `"open":null`, 1), strings.Replace(base, `"open":"future"`, `"open":"future","maybe":""`, 1), strings.Replace(base, `"open":"future"`, `"open":"future","choice":""`, 1), strings.Replace(base, `"open":"future"`, `"open":"future","hint":""`, 1), strings.Replace(base, `"scopes":["oa/local@1","remote"]`, `"scopes":["oa/local@1","future"]`, 1), strings.Replace(base, `"scopes":["oa/local@1","remote"]`, `"scopes":["oa/local@1",1]`, 1)}
	for n, b := range map[string]string{"cases.txt": strings.Join(cases, "\n") + "\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module equal.test\n\ngo 1.22\n", "main.go": enumGo, "main.py": enumPy, "main.cpp": enumCpp, "main.rs": enumRust, "main.mjs": enumJS} {
		writeNamespaceFile(t, dir, n, b)
	}
	want := "accept\naccept\nbad_enum\nmissing_field\nwrong_type\nbad_enum\nwrong_type\naccept\nbad_enum\nbad_enum\nbad_enum\nwrong_type\nbad_enum\n"
	run := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		out, e := cmd.CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
		if strings.ReplaceAll(string(out), "\r\n", "\n") != want {
			t.Fatalf("%s wrong verdicts:\n%s", name, out)
		}
	}
	compile := func(t *testing.T, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
	}
	t.Run("go", func(t *testing.T) { run(t, "go", "run", "main.go") })
	t.Run("python", func(t *testing.T) { run(t, servicePython(t), "-B", "main.py") })
	t.Run("javascript", func(t *testing.T) {
		p, e := exec.LookPath("node")
		if e != nil {
			t.Skip("Node unavailable")
		}
		run(t, p, "main.mjs")
	})
	t.Run("rust", func(t *testing.T) {
		p, e := exec.LookPath("rustc")
		if e != nil {
			t.Skip("Rust unavailable")
		}
		exe := filepath.Join(dir, "rust.exe")
		compile(t, p, "--edition=2021", "main.rs", "-o", exe)
		run(t, exe)
	})
	t.Run("cpp", func(t *testing.T) {
		p := os.Getenv("CXX")
		if p == "" {
			p, _ = exec.LookPath("g++")
		}
		if p == "" {
			t.Skip("C++ requires CXX or g++")
		}
		exe := filepath.Join(dir, "cpp.exe")
		args := []string{"-std=c++17", "main.cpp", "-o", exe}
		if n := strings.ToLower(filepath.Base(p)); n == "cl" || n == "cl.exe" {
			args = []string{"/nologo", "/EHsc", "/std:c++17", "main.cpp", "/Fe:" + exe}
		}
		compile(t, p, args...)
		run(t, exe)
	})
}

const enumGo = `package main
import("bytes";"fmt";"os";"strings";r "equal.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");lines:=strings.Split(strings.TrimSpace(string(b)),"\n");for _,line:=range lines{v,e:=r.Decode([]byte(line));if e!=nil{fmt.Println(e.(*r.Refusal).Word)}else{encoded:=r.Encode(v);if _,e=r.Decode(encoded);e!=nil{panic(e)};if v.Hint==0&&bytes.Contains(encoded,[]byte(` + "`" + `"hint"` + "`" + `)){panic("zero optional enum encoded")};fmt.Println("accept")}};if v,ok:=r.ParseScope("remote");!ok||v!=r.ScopeRemote||v.String()!="remote"{panic("closed mapping")};if _,ok:=r.ParseScope("bad");ok{panic("unknown closed word parsed")};v,_:=r.Decode([]byte(lines[0]));v.Scopes[0]=r.Scope(99);defer func(){p:=recover();if p==nil{panic("encoder accepted")};fmt.Println(p.(*r.Refusal).Word)}();r.Encode(v)}
`
const enumPy = `import rec
lines=open('cases.txt',encoding='utf-8').read().splitlines()
for line in lines:
 try:
  v=rec.decode(line.encode());rec.decode(rec.encode(v));print('accept')
 except rec.Refusal as e:print(e.word)
v=rec.decode(lines[0].encode());v.scopes[0]='bad'
try:rec.encode(v);raise AssertionError('encoder accepted')
except rec.Refusal as e:print(e.word)
v.scope=True
try:rec.encode(v);raise AssertionError('bool accepted as integer')
except rec.Refusal as e:assert e.word=='wrong_type'
`
const enumJS = `import * as r from './rec.mjs';import fs from 'node:fs';
const lines=fs.readFileSync('cases.txt','utf8').trim().split('\n');
for(const line of lines){try{const v=r.decode(Buffer.from(line));r.decode(r.encode(v));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
const v=r.decode(Buffer.from(lines[0]));v.scopes[0]='bad';try{r.encode(v);throw Error('encoder accepted')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}
`
const enumCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line,first;while(std::getline(f,line)){if(first.empty())first=line;try{auto v=rec::decode(line);rec::decode(rec::encode(v));std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}auto v=rec::decode(first);v.scopes[0]=static_cast<rec::Scope>(99);try{rec::encode(v);return 2;}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}
`
const enumRust = `mod rec;
fn main(){std::panic::set_hook(Box::new(|_|{}));let text=std::fs::read_to_string("cases.txt").unwrap();for line in text.lines(){match rec::decode(line.as_bytes()){Ok(v)=>{rec::decode(&rec::encode(&v)).unwrap();println!("accept")},Err(e)=>println!("{}",e.word)}}// A Scope that names no member cannot be constructed, so the encoder has nothing to refuse; the reader still refuses the word.
let bad=text.lines().next().unwrap().replacen("\"scope\":\"oa/local@1\"","\"scope\":\"bad\"",1);println!("{}",rec::decode(bad.as_bytes()).err().unwrap().word);}
`

func TestEnumServiceExchange(t *testing.T) {
	s, e := parse(enumHead() + enumFixture + `service Picker{ Scope Pick(1:Scope scope) list<Scope> PickMany(1:list<Scope> scopes, 2:list<Open> opens) Record Echo(1:Record value) }(wire_name="example/picker@1")`)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = selected(s, []string{"Picker", "Record", "Child", "Open"}); e == nil {
		t.Fatal("service selection forgot enum")
	}
	dir := t.TempDir()
	files := map[string]string{"go.mod": "module enum.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "main.go": `package main
import("io";"os";r "enum.test/rec")
type handler struct{}
func(handler)Pick(scope r.Scope)(r.Scope,error){return scope,nil}
func(handler)PickMany(scopes []r.Scope, opens []r.Open)([]r.Scope,error){return scopes,nil}
func(handler)Echo(v r.Record)(r.Record,error){return v,nil}
func main(){d:=r.PickerDispatcher{Handler:handler{}};v,_:=io.ReadAll(os.Stdin);out,e:=d.ExchangeFrame(v);if e!=nil{panic(e)};os.Stdout.Write(out)}
`, "main.py": `import rec as r,subprocess,sys,json
class Transport:
 calls=0
 def exchange_frame(self,b):
  self.calls+=1
  return subprocess.run([sys.argv[1]],input=b,capture_output=True,check=True).stdout
t=Transport();c=r.PickerClient(t)
assert c.pick('unknown')=='unknown'
assert c.pick_many([r.Scope.LOCAL,r.Scope.REMOTE],[r.Open.KNOWN,'future'])==[r.Scope.LOCAL,r.Scope.REMOTE]
v=r.Record(scope=r.Scope.LOCAL,open='future',child=r.Child(scope='remote'),maybe='',scopes=[r.Scope.LOCAL],opens=['future'])
b=c.echo(v);assert b.maybe=='' and b.open=='future' and b.child.scope=='remote'
for bad in ['future','',True,None]:
 calls=t.calls
 try:c.pick(bad);raise AssertionError('accepted bad argument')
 except r.Refusal:pass
 assert calls==t.calls
frame={'version':1,'service':'example/picker@1','method':'Pick','arguments':{'scope':'future'}}
reply=json.loads(t.exchange_frame(json.dumps(frame).encode()));assert reply['ok']==False and reply['payload']['code']=='bad_enum'
class Bad:
 def exchange_frame(self,b):return b'{"version":1,"service":"example/picker@1","method":"Pick","ok":true,"payload":{"value":"future"}}'
try:r.PickerClient(Bad()).pick('local');raise AssertionError('accepted bad result')
except r.Refusal as e:assert e.word=='bad_enum'
`, "main.cpp": `#include "rec.h"
struct H:rec::Picker{rec::Scope pick(const rec::Scope& scope)override{return scope;}std::vector<rec::Scope> pick_many(const std::vector<rec::Scope>& scopes,const std::vector<std::string>&)override{return scopes;}rec::Record echo(const rec::Record& value)override{return value;}};
int main(){H h;rec::PickerDispatcher d(h);rec::PickerClient c(d);if(c.pick(rec::Scope::Unknown)!=rec::Scope::Unknown)return 1;auto xs=c.pick_many({rec::Scope::Local,rec::Scope::Remote},{"known","future"});if(xs.size()!=2||xs[1]!=rec::Scope::Remote)return 4;try{c.pick(rec::Scope{});return 2;}catch(const rec::Refusal&e){if(std::string(e.word)!="bad_enum")return 3;}}
`}
	for n, b := range files {
		writeNamespaceFile(t, dir, n, b)
	}
	run := func(name string, args ...string) {
		t.Helper()
		c := exec.Command(name, args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%s: %v\n%s", name, e, out)
		}
	}
	host := filepath.Join(dir, "host.exe")
	run("go", "build", "-o", host, "main.go")
	run(servicePython(t), "main.py", host)
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Log("C++ service compiler unavailable")
		return
	}
	exe := filepath.Join(dir, "cpp.exe")
	args := []string{"-std=c++17", "main.cpp", "-o", exe}
	if strings.EqualFold(filepath.Base(cxx), "cl") || strings.EqualFold(filepath.Base(cxx), "cl.exe") {
		args = []string{"/nologo", "/std:c++17", "/EHsc", "main.cpp", "/Fe:" + exe}
	}
	run(cxx, args...)
	run(exe)
}

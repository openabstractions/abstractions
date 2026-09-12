package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const enumFixture = `enum Scope {1:local 2:remote 3:unknown}(unknown="refuse")
enum Open {1:known}(unknown="grant")
struct Child {1:required Scope scope}(unknown_fields="refuse")
struct Record {1:required Scope scope 2:required Open open 3:required Child child 4:optional Open maybe(omit="absent") 5:optional Scope choice(omit="absent") 6:optional Scope hint(omit="zero")}(document="true",unknown_fields="refuse")`

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
	for _, source := range []string{strings.Replace(enumHead()+enumFixture, "required Scope scope", "required list<Scope> scope", 1), strings.Replace(enumHead()+enumFixture, `document="true"`, `document="false"`, 1)} {
		if _, e := parse(source); e == nil {
			t.Fatal("unsupported enum collection or missing document accepted")
		}
	}
	if s.Struct("Record").Fields[0].Type != "Scope" {
		t.Fatal("lost schema type")
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
func TestEnumFiveBackends(t *testing.T) {
	s := enumDefinition(t)
	dir := t.TempDir()
	base := `{"scope":"local","open":"future","child":{"scope":"remote"}}`
	cases := []string{base, `{"scope":"unknown","open":"","child":{"scope":"local"},"maybe":"","choice":"unknown","hint":"local"}`, strings.Replace(base, `"scope":"local"`, `"scope":"bad"`, 1), strings.Replace(base, `"scope":"local",`, "", 1), strings.Replace(base, `"scope":"local"`, `"scope":1`, 1), strings.Replace(base, `"remote"`, `"bad"`, 1), strings.Replace(base, `"open":"future"`, `"open":null`, 1), strings.Replace(base, `"open":"future"`, `"open":"future","maybe":""`, 1), strings.Replace(base, `"open":"future"`, `"open":"future","choice":""`, 1), strings.Replace(base, `"open":"future"`, `"open":"future","hint":""`, 1)}
	for n, b := range map[string]string{"cases.txt": strings.Join(cases, "\n") + "\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module equal.test\n\ngo 1.22\n", "main.go": enumGo, "main.py": enumPy, "main.cpp": enumCpp, "main.rs": enumRust, "main.mjs": enumJS} {
		writeNamespaceFile(t, dir, n, b)
	}
	want := "accept\naccept\nbad_enum\nmissing_field\nwrong_type\nbad_enum\nwrong_type\naccept\nbad_enum\nbad_enum\nbad_enum\n"
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
	t.Run("python", func(t *testing.T) {
		p := os.Getenv("PYTHON")
		if p == "" {
			p, _ = exec.LookPath("python")
		}
		if p == "" {
			t.Skip("Python unavailable")
		}
		run(t, p, "-B", "main.py")
	})
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
import("fmt";"os";"strings";r "equal.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");lines:=strings.Split(strings.TrimSpace(string(b)),"\n");for _,line:=range lines{v,e:=r.Decode([]byte(line));if e!=nil{fmt.Println(e.(*r.Refusal).Word)}else{if _,e=r.Decode(r.Encode(v));e!=nil{panic(e)};fmt.Println("accept")}};v,_:=r.Decode([]byte(lines[0]));v.Scope="bad";defer func(){p:=recover();if p==nil{panic("encoder accepted")};fmt.Println(p.(*r.Refusal).Word)}();r.Encode(v)}
`
const enumPy = `import rec
lines=open('cases.txt',encoding='utf-8').read().splitlines()
for line in lines:
 try:
  v=rec.decode(line.encode());rec.decode(rec.encode(v));print('accept')
 except rec.Refusal as e:print(e.word)
v=rec.decode(lines[0].encode());v.scope='bad'
try:rec.encode(v);raise AssertionError('encoder accepted')
except rec.Refusal as e:print(e.word)
v.scope=True
try:rec.encode(v);raise AssertionError('bool accepted as integer')
except rec.Refusal as e:assert e.word=='wrong_type'
`
const enumJS = `import * as r from './rec.mjs';import fs from 'node:fs';
const lines=fs.readFileSync('cases.txt','utf8').trim().split('\n');
for(const line of lines){try{const v=r.decode(Buffer.from(line));r.decode(r.encode(v));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
const v=r.decode(Buffer.from(lines[0]));v.scope='bad';try{r.encode(v);throw Error('encoder accepted')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}
`
const enumCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line,first;while(std::getline(f,line)){if(first.empty())first=line;try{auto v=rec::decode(line);rec::decode(rec::encode(v));std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}auto v=rec::decode(first);v.scope="bad";try{rec::encode(v);return 2;}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}
`
const enumRust = `mod rec;
fn main(){std::panic::set_hook(Box::new(|_|{}));let text=std::fs::read_to_string("cases.txt").unwrap();for line in text.lines(){match rec::decode(line.as_bytes()){Ok(v)=>{rec::decode(&rec::encode(&v)).unwrap();println!("accept")},Err(e)=>println!("{}",e.word)}}let mut v=rec::decode(text.lines().next().unwrap().as_bytes()).unwrap();v.scope="bad".to_string();let err=std::panic::catch_unwind(||rec::encode(&v)).expect_err("encoder accepted");println!("{}",err.downcast_ref::<rec::Refusal>().unwrap().word);}
`

func TestEnumServiceExchange(t *testing.T) {
	s, e := parse(enumHead() + enumFixture + `service Picker{ Scope Pick(1:Scope scope) Record Echo(1:Record value) }(wire_name="example/picker@1")`)
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
func(handler)Pick(s string)(string,error){return s,nil}
func(handler)Echo(v r.Record)(r.Record,error){return v,nil}
func main(){d:=r.PickerDispatcher{Handler:handler{}};v,_:=io.ReadAll(os.Stdin);out,e:=d.ExchangeFrame(v);if e!=nil{panic(e)};os.Stdout.Write(out)}
`, "main.py": `import rec as r,subprocess,sys,json
class Transport:
 calls=0
 def exchange_frame(self,b):
  self.calls+=1
  return subprocess.run([sys.argv[1]],input=b,capture_output=True,check=True).stdout
t=Transport();c=r.PickerClient(t)
assert c.Pick('unknown')=='unknown'
v=r.Record(scope='local',open='future',child=r.Child(scope='remote'),maybe='')
b=c.Echo(v);assert b.maybe=='' and b.open=='future' and b.child.scope=='remote'
for bad in ['future','',True,None]:
 calls=t.calls
 try:c.Pick(bad);raise AssertionError('accepted bad argument')
 except r.Refusal:pass
 assert calls==t.calls
frame={'version':1,'service':'example/picker@1','method':'Pick','arguments':{'scope':'future'}}
reply=json.loads(t.exchange_frame(json.dumps(frame).encode()));assert reply['ok']==False and reply['payload']['code']=='bad_enum'
class Bad:
 def exchange_frame(self,b):return b'{"version":1,"service":"example/picker@1","method":"Pick","ok":true,"payload":{"value":"future"}}'
try:r.PickerClient(Bad()).Pick('local');raise AssertionError('accepted bad result')
except r.Refusal as e:assert e.word=='bad_enum'
`, "main.cpp": `#include "rec.h"
struct H:rec::Picker{std::string Pick(const std::string& s)override{return s;}rec::Record Echo(const rec::Record& v)override{return v;}};
int main(){H h;rec::PickerDispatcher d(h);rec::PickerClient c(d);if(c.Pick("unknown")!="unknown")return 1;try{c.Pick("future");return 2;}catch(const rec::Refusal&e){if(std::string(e.word)!="bad_enum")return 3;}}
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

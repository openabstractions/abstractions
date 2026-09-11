package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const equalityRecord = `struct Record{
1:required i64 schema(equals="1",equals_refusal="bad_schema")
2:required i32 minor(equals="-2",equals_refusal="bad_schema")
3:required i64 low(equals="-9223372036854775808",equals_refusal="bad_schema")
4:required i64 high(equals="9223372036854775807",equals_refusal="bad_schema")
}(document="true",unknown_fields="refuse")`

func equalitySource() string {
	return strings.Replace(strings.Replace(strings.Replace(strings.Replace(head, "11: trailing_bytes", "12: trailing_bytes", 1), "12: unknown_critical", "13: unknown_critical", 1), "13: not_a_subset", "14: not_a_subset", 1), "14: content_mismatch", "15: content_mismatch", 1) + equalityRecord
}
func equalityDefinition(t *testing.T) *Definition {
	t.Helper()
	source := strings.Replace(equalitySource(), "12: trailing_bytes", `11: bad_schema(stage="structure")
12: trailing_bytes`, 1)
	s, e := parse(source)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestEqualityValidation(t *testing.T) {
	source := strings.Replace(equalitySource(), "12: trailing_bytes", `11: bad_schema(stage="structure")
12: trailing_bytes`, 1)
	for _, tc := range []struct{ old, new, want string }{
		{`equals="1"`, `equals="01"`, "canonical"}, {`equals="1"`, `equals="+1"`, "canonical"}, {`equals="1"`, `equals="-0"`, "canonical"}, {`equals="1"`, `equals="9223372036854775808"`, "in-range"}, {`equals="-2"`, `equals="2147483648"`, "in-range"}, {`required i64 schema`, `required string schema`, "required i32 or i64"}, {`required i64 schema`, `optional i64 schema`, "does not say"}, {`equals_refusal="bad_schema"`, `equals_refusal="absent"`, "declared structure"}, {`equals_refusal="bad_schema"`, `equals_refusal="malformed"`, "declared structure"}, {`,equals_refusal="bad_schema"`, "", "both equals"}, {`equals="1",`, "", "both equals"}} {
		_, e := parse(strings.Replace(source, tc.old, tc.new, 1))
		if e == nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("%s: want %s got %v", tc.new, tc.want, e)
		}
	}
	s := equalityDefinition(t)
	if !strings.Contains(genDocs(s), "equals <code>1</code>; refusal <code>bad_schema</code>") {
		t.Fatal("docs lost constraint")
	}
}
func TestEqualityFiveBackends(t *testing.T) {
	s := equalityDefinition(t)
	dir := t.TempDir()
	base := `{"schema":1,"minor":-2,"low":-9223372036854775808,"high":9223372036854775807}`
	cases := []string{base, strings.Replace(base, `"schema":1`, `"schema":2`, 1), strings.Replace(base, `"schema":1,`, "", 1), strings.Replace(base, `"schema":1`, `"schema":9223372036854775808`, 1), strings.Replace(base, `"schema":1`, `"schema":true`, 1), strings.Replace(base, `"minor":-2`, `"minor":-3`, 1), strings.Replace(base, `"minor":-2`, `"minor":2147483648`, 1), strings.Replace(base, `"schema":1`, `"schema":1.0`, 1)}
	for n, b := range map[string]string{"cases.txt": strings.Join(cases, "\n") + "\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "rec.h": genCpp(s), "rec.rs": genRust(s), "rec.mjs": genJS(s), "go.mod": "module equal.test\n\ngo 1.22\n", "main.go": equalityGo, "main.py": equalityPy, "main.cpp": equalityCpp, "main.rs": equalityRust, "main.mjs": equalityJS} {
		writeNamespaceFile(t, dir, n, b)
	}
	want := "accept\nbad_schema\nmissing_field\nnumber_spelling\nwrong_type\nbad_schema\nnumber_spelling\nnumber_spelling\nbad_schema\n"
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

const equalityGo = `package main
import("fmt";"os";"strings";r "equal.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");lines:=strings.Split(strings.TrimSpace(string(b)),"\n");for _,line:=range lines{v,e:=r.Decode([]byte(line));if e!=nil{fmt.Println(e.(*r.Refusal).Word)}else{if _,e=r.Decode(r.Encode(v));e!=nil{panic(e)};fmt.Println("accept")}};v,_:=r.Decode([]byte(lines[0]));v.Schema=2;defer func(){p:=recover();if p==nil{panic("encoder accepted")};fmt.Println(p.(*r.Refusal).Word)}();r.Encode(v)}
`
const equalityPy = `import rec
lines=open('cases.txt',encoding='utf-8').read().splitlines()
for line in lines:
 try:
  v=rec.decode(line.encode());rec.decode(rec.encode(v));print('accept')
 except rec.Refusal as e:print(e.word)
v=rec.decode(lines[0].encode());v.schema=2
try:rec.encode(v);raise AssertionError('encoder accepted')
except rec.Refusal as e:print(e.word)
v.schema=True
try:rec.encode(v);raise AssertionError('bool accepted as integer')
except rec.Refusal as e:assert e.word=='wrong_type'
`
const equalityJS = `import * as r from './rec.mjs';import fs from 'node:fs';
const lines=fs.readFileSync('cases.txt','utf8').trim().split('\n');
for(const line of lines){try{const v=r.decode(Buffer.from(line));r.decode(r.encode(v));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}
const v=r.decode(Buffer.from(lines[0]));v.schema=2n;try{r.encode(v);throw Error('encoder accepted')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}
`
const equalityCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line,first;while(std::getline(f,line)){if(first.empty())first=line;try{auto v=rec::decode(line);rec::decode(rec::encode(v));std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}auto v=rec::decode(first);v.schema=2;try{rec::encode(v);return 2;}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}
`
const equalityRust = `mod rec;
fn main(){std::panic::set_hook(Box::new(|_|{}));let text=std::fs::read_to_string("cases.txt").unwrap();for line in text.lines(){match rec::decode(line.as_bytes()){Ok(v)=>{rec::decode(&rec::encode(&v)).unwrap();println!("accept")},Err(e)=>println!("{}",e.word)}}let mut v=rec::decode(text.lines().next().unwrap().as_bytes()).unwrap();v.schema=2;let err=std::panic::catch_unwind(||rec::encode(&v)).expect_err("encoder accepted");println!("{}",err.downcast_ref::<rec::Refusal>().unwrap().word);}
`

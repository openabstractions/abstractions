package main

import (
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const binaryFixture = `struct Value {1: optional binary data(omit="absent")}(document="true",unknown_fields="refuse")`

func binaryDefinition(t *testing.T) *Definition {
	t.Helper()
	s, e := parse(head + binaryFixture)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestBinaryGo(t *testing.T) {
	s := binaryDefinition(t)
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.go", genGo(s))
	writeNamespaceFile(t, dir, "go.mod", "module binary.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "binary_test.go", `package rec
import("testing";"bytes";"encoding/base64";"strings")
func TestBinary(t *testing.T){
 all:=make([]byte,256);for i:=range all{all[i]=byte(i)}
 for _,data:=range [][]byte{nil,{}, {0},{255,0},all}{
  r:=Value{Data:data};raw:=Encode(&r);back,e:=Decode(raw);if e!=nil{t.Fatal(e)}
  if (data==nil)!=(back.Data==nil)||!bytes.Equal(data,back.Data){t.Fatalf("lost presence/bytes %q",raw)}
  if data!=nil&&!strings.Contains(string(raw),"\""+base64.StdEncoding.EncodeToString(data)+"\""){t.Fatal(string(raw))}
 }
 for _,bad:=range []string{"A","AA","AAA","AB==","AAB=","AA=A","====","AA==AAAA","AA-_","AA==\n"}{
  // Escape actual newlines for valid JSON, so refusal is binary grammar.
  bad=strings.ReplaceAll(bad,"\n","\\n")
  _,e:=Decode([]byte("{\"data\":\""+bad+"\"}"));f,ok:=e.(*Refusal);if !ok||f.Word!="bad_binary"{t.Fatalf("%q: %v",bad,e)}
 }
}
`)
	c := exec.Command("go", "test", "./...")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}
func TestBinaryPython(t *testing.T) {
	s := binaryDefinition(t)
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.py", genPy(s))
	writeNamespaceFile(t, dir, "test.py", `import rec,base64
for data in [None,b'',b'\x00',b'\xff\x00',bytes(range(256))]:
 v=rec.Value();v.data=data
 raw=rec.encode(v);back=rec.decode(raw)
 assert back.data==data
 if data is not None: assert b'"'+base64.b64encode(data)+b'"' in raw
for value in ['A','AA','AAA','AB==','AAB=','AA=A','====','AA==AAAA','AA-_']:
 try: rec.decode(('{"data":"'+value+'"}').encode())
 except rec.Refusal as e: assert e.word=='bad_binary',e
 else: raise AssertionError(value)
v=rec.Value();v.data='text'
try:rec.encode(v)
except rec.Refusal as e:assert e.word=='wrong_type'
else:raise AssertionError('accepted UTF8 text as binary')
`)
	c := exec.Command(servicePython(t), "test.py")
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}
func TestBinaryCpp(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Skip("C++ unavailable")
	}
	s := binaryDefinition(t)
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.h", genCpp(s))
	writeNamespaceFile(t, dir, "main.cpp", `#include "rec.h"
#include <iostream>
int main(){rec::Value v;auto absent=rec::decode(rec::encode(v));if(absent.data)return 1;
v.data=std::vector<std::uint8_t>{};auto empty=rec::decode(rec::encode(v));if(!empty.data||!empty.data->empty())return 2;
for(int i=0;i<256;i++)v.data->push_back(static_cast<std::uint8_t>(i));auto back=rec::decode(rec::encode(v));if(back.data!=v.data)return 3;
if(rec::encode_binary({0,255})!="AP8=")return 4;
for(auto bad:{"A","AA","AAA","AB==","AAB=","AA=A","====","AA==AAAA","AA-_"}){try{rec::decode(std::string("{\"data\":\"")+bad+"\"}");std::cerr<<"accepted invalid Base64: "<<bad<<"\n";return 5;}catch(const rec::Refusal&e){if(std::string(e.word)!="bad_binary"){std::cerr<<"invalid Base64 "<<bad<<": expected bad_binary, got "<<e.word<<"\n";return 6;}}}
}`)
	exe := filepath.Join(dir, "binary.exe")
	args := []string{"-std=c++17", filepath.Join(dir, "main.cpp"), "-o", exe}
	if strings.EqualFold(filepath.Base(cxx), "cl.exe") || strings.EqualFold(filepath.Base(cxx), "cl") {
		args = []string{"/nologo", "/std:c++17", "/EHsc", filepath.Join(dir, "main.cpp"), "/Fe:" + exe, "/Fo:" + filepath.Join(dir, "main.obj")}
	}
	c := exec.Command(cxx, args...)
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	if out, e := exec.Command(exe).CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}
func TestBinaryUnsupportedBackends(t *testing.T) {
	s := binaryDefinition(t)
	for _, lang := range []string{"unsupported"} {
		if validateBinaryBackend(s, lang) == nil {
			t.Fatal(lang)
		}
	}
}

func TestBinaryHelpersAreFeatureScoped(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	for lang, body := range map[string]string{"go": genGo(s), "cpp": genCpp(s), "python": genPy(s), "rust": genRust(s), "javascript": genJS(s)} {
		for _, marker := range []string{"encodeBinary", "encode_binary", "bad_binary", "kind == \"binary\""} {
			if strings.Contains(body, marker) {
				t.Fatalf("%s added binary machinery to an existing schema", lang)
			}
		}
	}
}

func TestBinaryRust(t *testing.T) {
	s := binaryDefinition(t)
	if e := validateBinaryBackend(s, "rust"); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.rs", genRust(s))
	writeNamespaceFile(t, dir, "main.rs", `mod rec;
use std::io::Write;
fn main(){
 assert!(rec::decode(b"{}").unwrap().data.is_none());
 for data in [vec![],vec![0],vec![255,0],(0..=255u8).collect()] {
 let v=rec::Value{data:Some(data.clone())}; assert_eq!(rec::decode(&rec::encode(&v)).unwrap().data,Some(data)); }
 for bad in ["A","AA","AAA","AB==","AAB=","AA=A","====","AA==AAAA","AA-_","AA==\\n","éAAA","AAA=AAAA"] {
 let raw=format!("{{\"data\":\"{}\"}}",bad);
 assert_eq!(rec::decode(raw.as_bytes()).err().unwrap().word,"bad_binary","{}",bad); }
 for (raw,word) in [("{\"data\":false}","wrong_type"),("{\"data\":\"AB==\"}x","bad_binary"),("{\"data\":\"AA==\"}x","trailing_bytes")] {
 assert_eq!(rec::decode(raw.as_bytes()).err().unwrap().word,word,"{}",raw); }
 std::io::stdout().write_all(&rec::encode(&rec::Value{data:Some((0..=255u8).collect())})).unwrap();
}
`)
	exe := filepath.Join(dir, "probe.exe")
	c := exec.Command(rustServiceCompiler(t), "--edition=2021", filepath.Join(dir, "main.rs"), "-o", exe)
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	out, e := exec.Command(exe).CombinedOutput()
	if e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	all := make([]byte, 256)
	for i := range all {
		all[i] = byte(i)
	}
	if !strings.Contains(string(out), "\""+base64.StdEncoding.EncodeToString(all)+"\"") {
		t.Fatalf("noncanonical output: %s", out)
	}
	// Negative control: canonical tail-bit validation must be exercised by the executable.
	body := strings.Replace(genRust(s), `if encode_binary(&out) != text { return r.refuse("bad_binary"); }`, "", 1)
	writeNamespaceFile(t, dir, "rec.rs", body)
	c = exec.Command(rustServiceCompiler(t), "--edition=2021", filepath.Join(dir, "main.rs"), "-o", exe)
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("mutation compile: %v\n%s", e, out)
	}
	if out, e := exec.Command(exe).CombinedOutput(); e == nil {
		t.Fatalf("canonical-tail mutation passed: %s", out)
	}

}
func TestRustAcceptanceBinarySchemaCompiles(t *testing.T) {
	raw, e := os.ReadFile("../../openabstractions-flat/abstraction-job/acceptance.thrift")
	if e != nil {
		t.Fatal(e)
	}
	s, e := parse(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.rs", genRust(s))
	c := exec.Command(rustServiceCompiler(t), "--edition=2021", "--crate-type=lib", filepath.Join(dir, "rec.rs"), "-o", filepath.Join(dir, "lib.rlib"))
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

func TestRustBinaryDepth(t *testing.T) {
	s, e := parse(strings.Replace(head, `depth_limit    = "64"`, `depth_limit    = "1"`, 1) + `struct Child {1: required binary data}(unknown_fields="refuse") struct Value {1: required Child child}(document="true",unknown_fields="refuse")`)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.rs", genRust(s))
	writeNamespaceFile(t, dir, "main.rs", `mod rec;fn main(){assert_eq!(rec::decode(b"{\"child\":{\"data\":\"AA==\"}}").err().unwrap().word,"depth_exceeded");}`)
	exe := filepath.Join(dir, "probe.exe")
	c := exec.Command(rustServiceCompiler(t), "--edition=2021", filepath.Join(dir, "main.rs"), "-o", exe)
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
	if out, e := exec.Command(exe).CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

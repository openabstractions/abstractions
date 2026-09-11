package main

import (
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
	for _, lang := range []string{"rust", "javascript"} {
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
	for lang, body := range map[string]string{"go": genGo(s), "cpp": genCpp(s), "python": genPy(s)} {
		for _, marker := range []string{"encodeBinary", "encode_binary", "bad_binary", "kind == \"binary\""} {
			if strings.Contains(body, marker) {
				t.Fatalf("%s added binary machinery to an existing schema", lang)
			}
		}
	}
}

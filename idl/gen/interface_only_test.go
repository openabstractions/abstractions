package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNoIPCGeneration(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "example.thrift")
	profile, err := os.ReadFile("../LANGUAGE.md")
	if err != nil {
		t.Fatal(err)
	}
	writeNamespaceFile(t, dir, "LANGUAGE.md", string(profile))
	sourceText := head + replyFixture
	var lines []string
	refusalID := 0
	for _, line := range strings.Split(sourceText, "\n") {
		if strings.Contains(line, "bad_timestamp") || strings.Contains(line, "content_mismatch") || strings.Contains(line, "not_a_subset") || strings.Contains(line, "unknown_critical") {
			continue
		}
		if strings.Contains(line, "stage") {
			refusalID++
			line = strconv.Itoa(refusalID) + ":" + strings.SplitN(line, ":", 2)[1]
		}
		lines = append(lines, line)
	}
	writeNamespaceFile(t, dir, "example.thrift", strings.Join(lines, "\n"))
	// CLI preflight and docs verification must run, not only backend helpers.
	if err := run([]string{source, dir, "--no-ipc", "go", "cpp", "python", "docs"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"go/rec/rec.go", "cpp/rec.h", "py/rec.py"} {
		body, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		for _, absent := range []string{"OAServiceFrame", "OAServiceReply", "QueryClient", "QueryDispatcher", "FrameWriter", "FrameExchanger", "_service_request"} {
			if strings.Contains(string(body), absent) {
				t.Fatalf("%s retained %s", path, absent)
			}
		}
		if !strings.Contains(string(body), "Query") || !strings.Contains(string(body), "Record") {
			t.Fatalf("missing types/interface in %s", path)
		}
	}
	docs, err := os.ReadFile(filepath.Join(dir, "schema.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(docs), "Interface-only output") || strings.Contains(string(docs), "Frames carry version") {
		t.Fatal("docs claim emitted IPC")
	}
	writeNamespaceFile(t, dir, "go/go.mod", "module example.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "go/rec/api_test.go", `package rec
import "testing"
type provider struct{}
func(provider)Echo(r Record,s string,b bool,n int64)(Record,error){return r,nil}
func(provider)Opaque(v Raw)(Raw,error){return v,nil}
func(provider)Reset()error{return nil}
func(provider)Notify()error{return nil}
func(provider)Fail(string)(string,error){return "",nil}
var _ Query=provider{}
func TestAPI(t *testing.T){var p Query=provider{};r,e:=p.Echo(Record{Value:"x"},"",false,0);if e!=nil||r.Value!="x"{t.Fatal(r,e)}}`)
	c := exec.Command("go", "test", "./...")
	c.Dir = filepath.Join(dir, "go")
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestNoIPCDefaultAndUnsupported(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	baseline := genGo(s)
	copy := *s
	copy.NoIPC = true
	_ = genGo(&copy)
	if genGo(s) != baseline || !strings.Contains(baseline, "QueryClient") {
		t.Fatal("mode mutated default")
	}
	for _, lang := range []string{"rust", "javascript"} {
		if err := validateServiceBackend(&copy, lang); err == nil || !strings.Contains(err.Error(), "--no-ipc") {
			t.Fatalf("%s: %v", lang, err)
		}
	}
}

func TestNoIPCCpp(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Skip("C++ compiler unavailable")
	}
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	s.NoIPC = true
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.h", genCpp(s))
	writeNamespaceFile(t, dir, "main.cpp", `#include "rec.h"
struct Provider:rec::Query{
rec::Record Echo(const rec::Record&r,const std::string&,const bool&,const std::int64_t&)override{return r;}
std::string Opaque(const std::string&r)override{return r;}
void Reset()override{} void Notify()override{} std::string Fail(const std::string&)override{return "";}
};
int main(){Provider p;rec::Query& api=p;rec::Record r;r.value="x";return api.Echo(r,"",false,0).value=="x"?0:1;}`)
	exe := filepath.Join(dir, "api.exe")
	args := []string{"-std=c++17", filepath.Join(dir, "main.cpp"), "-o", exe}
	if name := strings.ToLower(filepath.Base(cxx)); name == "cl" || name == "cl.exe" {
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

func TestNoIPCPython(t *testing.T) {
	py := servicePython(t)
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	s.NoIPC = true
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.py", genPy(s))
	writeNamespaceFile(t, dir, "test.py", `import rec
class Provider(rec.Query):
 def Echo(self, record, text, enabled, count): return record
p=Provider()
r=rec.Record()
r.value='x'
assert p.Echo(r,'',False,0).value=='x'
assert not hasattr(rec,'QueryClient')
assert not hasattr(rec,'FrameWriter')
`)
	c := exec.Command(py, "test.py")
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

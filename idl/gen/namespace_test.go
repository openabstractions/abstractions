package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNamespaceFromSchemaControlsArtifactsAndSelection(t *testing.T) {
	dir := t.TempDir()
	base, err := os.ReadFile(example)
	if err != nil {
		t.Fatal(err)
	}
	source := string(base) + "\nnamespace * oa.logging\nnamespace go oa.loggingclient\n"
	profile, err := os.ReadFile("../LANGUAGE.md")
	if err != nil {
		t.Fatal(err)
	}
	writeNamespaceFile(t, dir, "LANGUAGE.md", string(profile))
	writeNamespaceFile(t, dir, "logging.thrift", source)
	var report bytes.Buffer
	if err := run([]string{filepath.Join(dir, "logging.thrift"), filepath.Join(dir, "out"), "-only=" + strings.Join(surfaces(exampleDefinition(t)), ","), "go", "cpp", "docs"}, &report); err != nil {
		t.Fatal(err)
	}
	for path, fragment := range map[string]string{
		"go/oa/loggingclient/rec.go": "package loggingclient\n",
		"cpp/oa/logging/rec.h":       "namespace oa::logging {",
		"oa.logging.schema.html":     "<h1>Schema</h1>",
	} {
		got, err := os.ReadFile(filepath.Join(dir, "out", path))
		if err != nil || !strings.Contains(string(got), fragment) {
			t.Fatalf("%s: %v, missing %q", path, err, fragment)
		}
	}
}

func TestInvalidNamespaceDoesNotWriteOutputs(t *testing.T) {
	for _, directive := range []string{"namespace * class", "namespace go oa..logging", "namespace ruby example", "namespace go first\nnamespace go second"} {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "bad.thrift", head+directive+"\n"+doc)
		out := filepath.Join(dir, "out")
		var report bytes.Buffer
		if err := run([]string{filepath.Join(dir, "bad.thrift"), out, "go"}, &report); err == nil {
			t.Fatalf("accepted %s", directive)
		}
		if _, err := os.Stat(out); !os.IsNotExist(err) {
			t.Fatalf("output created for %s", directive)
		}
	}
}

func TestNamespaceValidation(t *testing.T) {
	for _, n := range []string{"", "a..b", ".a", "a.", "../escape", "a/b", "2bad", "_", "__reserved", "CON", "a.NUL", "class", "self", "for"} {
		if err := validateNamespace("*", n); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
	for _, n := range []string{"abstraction.logging", "job_v1", "org.Example"} {
		if err := validateNamespace("*", n); err != nil {
			t.Errorf("rejected %q: %v", n, err)
		}
	}
	if err := validateNamespace("typo", "valid"); err == nil {
		t.Fatal("unknown language accepted")
	}
}

func TestOutputPathsUseEveryNamespaceWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "logging.thrift", head+"namespace * oa.logging\n"+doc)
	var out bytes.Buffer
	if err := run([]string{filepath.Join(dir, "logging.thrift"), "--paths", "go", "cpp", "docs"}, &out); err != nil {
		t.Fatal(err)
	}
	want := "go/oa/logging/rec.go\ncpp/oa/logging/rec.h\noa.logging.schema.html\n"
	if out.String() != want {
		t.Fatalf("paths %q, want %q", out.String(), want)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("path query wrote files: %v %v", entries, err)
	}
}

func TestNamespacePreservesLegacyOutputsAndSeparatesPaths(t *testing.T) {
	def := exampleDefinition(t)
	for _, b := range backends {
		body := b.emit(def)
		path, got := namespacedOutput(b, nil, body)
		if path != b.path || got != body {
			t.Fatalf("legacy %s changed", b.lang)
		}
		p1, _ := namespacedOutput(b, map[string]string{"*": "oa.first"}, body)
		p2, _ := namespacedOutput(b, map[string]string{"*": "oa.second"}, body)
		if p1 == p2 {
			t.Fatalf("%s output paths collide", b.lang)
		}
	}
	if got := namespaceFor(map[string]string{"*": "oa.first", "cpp": "oa.second"}, "cpp"); got != "oa.second" {
		t.Fatal(got)
	}
}

func TestGeneratedGoSchemasCoexist(t *testing.T) {
	dir := t.TempDir()
	def := exampleDefinition(t)
	var b backend
	for _, candidate := range backends {
		if candidate.lang == "go" {
			b = candidate
		}
	}
	for _, n := range []string{"oa.first", "oa.second"} {
		path, body := namespacedOutput(b, map[string]string{"*": n}, genGo(def))
		writeNamespaceFile(t, dir, path, body)
	}
	writeNamespaceFile(t, dir, "go.mod", "module composition.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "main.go", `package main
import (first "composition.test/go/oa/first"; second "composition.test/go/oa/second")
func main() { _ = first.Raw("{}"); _ = second.Raw("{}") }
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("two Go schemas: %v\n%s", err, out)
	}
}

func TestGeneratedCppSchemasCoexist(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		cxx, _ = exec.LookPath("clang++")
	}
	if cxx == "" {
		t.Skip("C++ composition requires CXX, g++ or clang++")
	}
	dir := t.TempDir()
	def := exampleDefinition(t)
	var b backend
	for _, candidate := range backends {
		if candidate.lang == "cpp" {
			b = candidate
		}
	}
	for _, n := range []string{"oa.first", "oa.second"} {
		path, body := namespacedOutput(b, map[string]string{"*": n}, genCpp(def))
		writeNamespaceFile(t, dir, path, body)
	}
	writeNamespaceFile(t, dir, "main.cpp", `#include "cpp/oa/first/rec.h"
#include "cpp/oa/second/rec.h"
int main() { oa::first::Raw a = "{}"; oa::second::Raw b = "{}"; return a != b; }
`)
	exe := filepath.Join(dir, "consumer.exe")
	args := []string{"-std=c++17", filepath.Join(dir, "main.cpp"), "-o", exe}
	if name := strings.ToLower(filepath.Base(cxx)); name == "cl" || name == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/EHsc", filepath.Join(dir, "main.cpp"), "/Fe:" + exe, "/Fo:" + filepath.Join(dir, "main.obj")}
	}
	cmd := exec.Command(cxx, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("two C++ schemas: %v\n%s", err, out)
	}
	if out, err := exec.Command(exe).CombinedOutput(); err != nil {
		t.Fatalf("C++ consumer: %v\n%s", err, out)
	}
}

func TestGeneratedCppServicesShareOneTransport(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		cxx, _ = exec.LookPath("clang++")
	}
	if cxx == "" {
		t.Skip("C++ service composition requires CXX, g++ or clang++")
	}
	dir := t.TempDir()
	var b backend
	for _, candidate := range backends {
		if candidate.lang == "cpp" {
			b = candidate
		}
	}
	for _, n := range []string{"oa.first", "oa.second"} {
		def, err := parse(head + strings.Replace(serviceFixture, "example.events/events@1", n+"/events@1", 1))
		if err != nil {
			t.Fatal(err)
		}
		path, body := namespacedOutput(b, map[string]string{"*": n}, genCpp(def))
		writeNamespaceFile(t, dir, path, body)
	}
	writeNamespaceFile(t, dir, "main.cpp", `#include "cpp/oa/first/rec.h"
#include "cpp/oa/second/rec.h"
struct SharedTransport {
  std::vector<std::string> frames;
  void WriteFrame(std::string_view frame) { frames.emplace_back(frame); }
};
int main() {
  SharedTransport transport;
  oa::first::EventsClient first(transport);
  oa::second::EventsClient second(transport);
  first.Ping(); second.Ping();
  return transport.frames.size() != 2 ||
    transport.frames[0].find("oa.first/events@1") == std::string::npos ||
    transport.frames[1].find("oa.second/events@1") == std::string::npos;
}
`)
	exe := filepath.Join(dir, "consumer.exe")
	args := []string{"-std=c++17", filepath.Join(dir, "main.cpp"), "-o", exe}
	if name := strings.ToLower(filepath.Base(cxx)); name == "cl" || name == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/EHsc", filepath.Join(dir, "main.cpp"), "/Fe:" + exe, "/Fo:" + filepath.Join(dir, "main.obj")}
	}
	cmd := exec.Command(cxx, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shared transport compilation: %v\n%s", err, out)
	}
	if out, err := exec.Command(exe).CombinedOutput(); err != nil {
		t.Fatalf("shared transport consumer: %v\n%s", err, out)
	}
}

func writeNamespaceFile(t *testing.T, root, path, body string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

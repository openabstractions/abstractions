package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const selectorFixture = `struct Record {1:required string value}(document="true",unknown_fields="refuse")
service First {oneway void Ping()}(wire_name="test/first@1")
service Second {oneway void Ping()}(wire_name="test/second@1")
`

func TestServiceSelector(t *testing.T) {
	s, err := parse(head + selectorFixture)
	if err != nil {
		t.Fatal(err)
	}
	valid := `{"version":1,"service":"test/first@1","method":"Ping","arguments":{}}`
	bad := []string{"", "{}", valid + "x", strings.Replace(valid, `"version":1`, `"version":2`, 1), strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1), strings.Replace(valid, `"service":"test/first@1"`, `"service":4`, 1), strings.Replace(valid, `"arguments":{}`, `"arguments":{},"extra":true`, 1)}
	goChecks, pyChecks, cppChecks := "", "", ""
	for _, frame := range bad {
		q := strconv.Quote(frame)
		goChecks += fmt.Sprintf("if _,err:=rec.ServiceName([]byte(%s));err==nil{t.Fatal(\"accepted malformed envelope\")}\n", q)
		pyChecks += fmt.Sprintf("try:\n rec.service_name(%s.encode())\nexcept Exception:\n pass\nelse:\n raise AssertionError('accepted malformed envelope')\n", q)
		cppChecks += fmt.Sprintf("try{rec::service_name(%s);return 2;}catch(const std::exception&){}\n", q)
	}
	for _, name := range []string{"test/first@1", "test/second@1", "future/unknown@1"} {
		q := strconv.Quote(strings.Replace(valid, "test/first@1", name, 1))
		goChecks += fmt.Sprintf("if got,err:=rec.ServiceName([]byte(%s));err!=nil||got!=%q{t.Fatal(got,err)}\n", q, name)
		pyChecks += fmt.Sprintf("assert rec.service_name(%s.encode()) == %q\n", q, name)
		cppChecks += fmt.Sprintf("if(rec::service_name(%s)!=%q)return 3;\n", q, name)
	}
	t.Run("Go", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "go.mod", "module selector.test\n\ngo 1.22\n")
		writeNamespaceFile(t, dir, "rec.go", genGo(s))
		writeNamespaceFile(t, dir, "rec_test.go", "package rec_test\nimport(\"testing\";rec \"selector.test\")\nfunc TestSelector(t *testing.T){"+goChecks+`}
type handler struct{ calls int }; func(h *handler)Ping()error{h.calls++;return nil}
func TestDispatch(t *testing.T){
 a,b:=&handler{},&handler{}; first,second:=&rec.FirstDispatcher{Handler:a},&rec.SecondDispatcher{Handler:b}
 route:=func(frame []byte)error{name,err:=rec.ServiceName(frame);if err!=nil{return err};switch name{case "test/first@1":return first.WriteFrame(frame);case "test/second@1":return second.WriteFrame(frame)};return rec.DispatchError("unknown_service")}
 for _,name:=range []string{"test/first@1","test/second@1"}{if err:=route([]byte("{\"version\":1,\"service\":\""+name+"\",\"method\":\"Ping\",\"arguments\":{}}"));err!=nil{t.Fatal(err)}}
 if a.calls!=1||b.calls!=1{t.Fatal(a.calls,b.calls)}
 for _,frame:=range []string{
 `+strconv.Quote(strings.Replace(valid, `"Ping"`, `"Unknown"`, 1))+`,
 `+strconv.Quote(strings.Replace(valid, `"arguments":{}`, `"arguments":{"extra":true}`, 1))+`,
 }{if err:=route([]byte(frame));err==nil{t.Fatal("accepted bad dispatch")}}
 if a.calls!=1||b.calls!=1{t.Fatal("invalid request reached handler")}
}
`)
		cmd := exec.Command("go", "test", ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("Python", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.py", genPy(s))
		writeNamespaceFile(t, dir, "test.py", "import rec\n"+pyChecks)
		cmd := exec.Command(servicePython(t), "test.py")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
	t.Run("Cpp", func(t *testing.T) {
		cxx := os.Getenv("CXX")
		if cxx == "" {
			cxx, _ = exec.LookPath("g++")
		}
		if cxx == "" {
			t.Skip("C++ selector requires CXX or g++")
		}
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.h", genCpp(s))
		writeNamespaceFile(t, dir, "main.cpp", "#include \"rec.h\"\nint main(){"+cppChecks+"}\n")
		exe := filepath.Join(dir, "selector.exe")
		args := []string{"-std=c++17", "main.cpp", "-o", exe}
		if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
			args = []string{"/nologo", "/std:c++17", "/EHsc", "main.cpp", "/Fe:" + exe, "/Fo:" + filepath.Join(dir, "main.obj")}
		}
		cmd := exec.Command(cxx, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if out, err := exec.Command(exe).CombinedOutput(); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
	})
}

func TestServiceSelectorNames(t *testing.T) {
	for _, name := range []string{"ServiceName", "service_name"} {
		if _, err := parse(head + strings.Replace(selectorFixture, "Record", name, 1)); err == nil {
			t.Fatal("accepted selector collision", name)
		}
	}
	s, err := parse(head + serviceFixture)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(genGo(s), "func ServiceName(") || strings.Contains(genCpp(s), "service_name(std::string_view") || strings.Contains(genPy(s), "def service_name(") {
		t.Fatal("single-service API changed")
	}
}

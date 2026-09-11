package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const serviceFixture = `struct Record {1: required string value} (document="true", unknown_fields="grant")
service Events { oneway void Write(1: Record record,2: string text,3: bool enabled,4: i64 count) oneway void Ping() oneway void Opaque(1: json payload) } (wire_name="example.events/events@1")
`

func TestServiceParse(t *testing.T) {
	s, e := parse(head + serviceFixture)
	if e != nil {
		t.Fatal(e)
	}
	prefix := head + `struct Record {1: required string value}(document="true",unknown_fields="grant") service S {`
	suffix := `} (wire_name="example.test/s@1")`
	if _, e := parse(prefix + "oneway void Ping()" + suffix); e != nil {
		t.Fatal("control", e)
	}
	for _, tc := range []struct{ input, want string }{
		{"oneway void delete()", "colliding method"},
		{"oneway void Ping(1:string value(cpp.name=\"delete\"))", "invalid C++ service argument"},
		{"oneway i64 Ping()", "oneway methods require void"},
		{"oneway void Ping(1: optional string text)", "optional service"},
		{"oneway void Ping(1: string x,1: string y)", "duplicate argument"},
		{"oneway void Ping() oneway void Ping()", "colliding method"},
		{"oneway void Ping() (docs=\"typo\")", "unknown method annotation"},
		{"oneway void putA() oneway void puta()", "name collision"},
		{"oneway void Ping(1: string x (cpp.name=\"a\"),2: string y (cpp.name=\"a\"))", "duplicate argument"},
	} {
		_, e := parse(prefix + tc.input + suffix)
		if e == nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("%s: want %s, got %v", tc.input, tc.want, e)
		}
	}
	for _, lang := range []string{"rust", "javascript"} {
		if validateServiceBackend(s, lang) == nil {
			t.Fatal("silently omitted service", lang)
		}
	}
	if _, e = selected(s, []string{"Events"}); e == nil {
		t.Fatal("service selected without argument record")
	}
	if _, e = selected(s, []string{"Events", "Record"}); e != nil {
		t.Fatal(e)
	}
}
func TestGeneratedServiceGo(t *testing.T) {
	s, e := parse(head + serviceFixture)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module service.test\n\ngo 1.22\n", "rec.go": genGo(s), "rec_test.go": serviceGoTest} {
		if e = os.WriteFile(filepath.Join(dir, n), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := exec.Command("go", "test", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}
func TestServiceRejectsNestedOpaque(t *testing.T) {
	source := head + `struct Record{1:required json payload}(document="true",unknown_fields="grant") service S{oneway void Write(1:Record r)}(wire_name="test/s@1")`
	if _, e := parse(source); e == nil || !strings.Contains(e.Error(), "nested opaque") {
		t.Fatal(e)
	}
}
func TestServiceDocs(t *testing.T) {
	s, e := parse(head + strings.Replace(serviceFixture, `wire_name="example.events/events@1"`, `wire_name="example.events/events@1",doc="<intent>&"`, 1))
	if e != nil {
		t.Fatal(e)
	}
	out := genDocs(s)
	for _, v := range []string{"Events", "Write", "Ping", "oneway", "arguments", "example.events/events@1", "&lt;intent&gt;&amp;"} {
		if !strings.Contains(out, v) {
			t.Fatal("missing service docs", v)
		}
	}
}

const serviceGoTest = `package rec
import("errors";"testing";"strings")
type handler struct{count int}
func(h *handler)Write(r Record,s string,b bool,n int64)error{if r.Value!="x"||s!=""||b||n!=0{panic("changed values")};h.count++;return nil}
func(h *handler)Ping()error{h.count++;return nil}
func(h *handler)Opaque(p Raw)error{if string(p)!=` + "`" + `{ "a" : 1 }` + "`" + `{panic("raw bytes changed")};return nil}
type capture struct{frame []byte; err error}
func(c *capture)WriteFrame(b []byte)error{c.frame=append([]byte(nil),b...);return c.err}
func TestCalls(t *testing.T){h:=&handler{};d:=&EventsDispatcher{Handler:h};c:=NewEventsClient(d);if e:=c.Write(Record{Value:"x"},"",false,0);e!=nil{t.Fatal(e)};if e:=c.Ping();e!=nil{t.Fatal(e)};if h.count!=2{t.Fatal(h.count)};if e:=c.Opaque(Raw(` + "`" + `{ "a" : 1 }` + "`" + `));e!=nil{t.Fatal(e)}
 sink:=&capture{err:errors.New("transport down")};c=NewEventsClient(sink);if e:=c.Ping();e!=sink.err{t.Fatal(e)}
 valid:=string(sink.frame);
 for _,bad:=range []string{strings.Replace(valid,"\"version\": 1","\"version\": 2",1),strings.Replace(valid,"example.events/events@1","Else",1),strings.Replace(valid,"Ping","Gone",1),valid+"{}",valid[:len(valid)-1],strings.Replace(valid,"{}","{\"unexpected\":1}",1),"{\"version\":1,\"service\":\"example.events/events@1\",\"method\":\"Write\",\"arguments\":{}}"}{if e:=d.WriteFrame([]byte(bad));e==nil{t.Fatal("accepted",bad)};if h.count!=2{t.Fatal("partial dispatch")}}
}
`

const replyFixture = `struct Record {1: required string value}(document="true",unknown_fields="grant")
service Query {
 Record Echo(1: Record record,2: string text,3: bool enabled,4: i64 count)
 json Opaque(1: json payload)
 void Reset()
 string Fail(1: string kind)
 oneway void Notify()
}(wire_name="example.query/query@1")
`

func TestReplyDocsAndSelection(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	out := genDocs(s)
	for _, word := range []string{"request-response", "ExchangeFrame", "ServiceError", "handler_error", "invalid_result", "payload", "Record"} {
		if !strings.Contains(out, word) {
			t.Fatal("missing reply documentation", word)
		}
	}
	source := head + `struct Record{1:required string value}(document="true",unknown_fields="grant") service S{Record Get()}(wire_name="test/s@1")`
	s, e = parse(source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = selected(s, []string{"S"}); e == nil {
		t.Fatal("return-only dependency omitted")
	}
	if _, e = selected(s, []string{"S", "Record"}); e != nil {
		t.Fatal(e)
	}
}
func TestGeneratedReplyGo(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = selected(s, []string{"Query"}); e == nil {
		t.Fatal("missing result dependency")
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module reply.test\n\ngo 1.22\n", "rec.go": genGo(s), "rec_test.go": replyGoTest} {
		if e = os.WriteFile(filepath.Join(dir, n), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := exec.Command("go", "test", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

const replyGoTest = `package rec
import("errors";"testing";"strings")
type handler struct{calls int}
func(h *handler)Echo(r Record,text string,enabled bool,count int64)(Record,error){h.calls++;if text!=""||enabled||count!=0{panic("changed zero values")};return r,nil}
func(h *handler)Opaque(p Raw)(Raw,error){h.calls++;return p,nil}
func(h *handler)Reset()error{h.calls++;return nil}
func(h *handler)Notify()error{h.calls++;return nil}
func(h *handler)Fail(kind string)(string,error){h.calls++;switch kind{case "panic":panic("secret panic");case "invalid":return string([]byte{255}),nil;case "diagnostic":return "",&ServiceError{Code:"denied",Message:string([]byte{255})};case "ordinary":return "",errors.New("secret error");default:return "",&ServiceError{Code:kind}}}
type fake struct{frame []byte;response []byte;err error}
func(f *fake)WriteFrame(b []byte)error{f.frame=b;return f.err}
func(f *fake)ExchangeFrame(b []byte)([]byte,error){f.frame=b;return f.response,f.err}
func TestRoundtrip(t *testing.T){h:=&handler{};d:=&QueryDispatcher{Handler:h};c:=NewQueryClient(d)
 r,e:=c.Echo(Record{Value:"\u96ea<&"},"",false,0);if e!=nil||r.Value!="\u96ea<&"{t.Fatal(r,e)}
 raw:=Raw("{ \"a\" : [1,\n false] }");v,e:=c.Opaque(raw);if e!=nil||v!=raw{t.Fatal(v,e)}
 if e=c.Reset();e!=nil{t.Fatal(e)};if e=c.Notify();e!=nil{t.Fatal(e)}
 for _,tc:=range []struct{kind,code,message string}{{"future_code","future_code",""},{"panic","handler_error","handler failed"},{"ordinary","handler_error","handler failed"},{"invalid","invalid_result",""}}{_,e=c.Fail(tc.kind);var se *ServiceError;if !errors.As(e,&se)||se.Code!=tc.code||se.Message!=tc.message{t.Fatalf("%s: %#v",tc.kind,e)}}
 if _,e=c.Fail("diagnostic");e==nil{t.Fatal("invalid diagnostic")}
}
func TestRefusals(t *testing.T){h:=&handler{};d:=&QueryDispatcher{Handler:h};f:=&fake{err:errors.New("offline")};c:=NewQueryClient(f)
 if _,e:=c.Echo(Record{Value:"x"},"",false,0);e!=f.err{t.Fatal(e)};frame:=string(f.frame)
 if e:=d.WriteFrame([]byte(frame));e==nil{t.Fatal("wrong mode")}
 for _,bad:=range []string{strings.Replace(frame,"\"version\": 1","\"version\": 2",1),frame+"{}",frame[:len(frame)-1],strings.Replace(frame,"\"count\": 0","\"count\": false",1),strings.Replace(frame,"\"count\": 0","\"missing\": 0",1),strings.Replace(frame,"example.query/query@1","unknown",1),strings.Replace(frame,"Echo","unknown",1)}{reply,e:=d.ExchangeFrame([]byte(bad));if e==nil{_,e=serviceResponse(reply,"example.query/query@1","Echo")};if e==nil{t.Fatal("accepted",bad)}}
 if h.calls!=0{t.Fatal("partial dispatch",h.calls)}
 good,e:=d.ExchangeFrame([]byte(frame));if e!=nil{t.Fatal(e)};f.err=nil
 for _,bad:=range []string{string(good)+"{}",strings.Replace(string(good),"\"version\": 1","\"version\": 2",1),strings.Replace(string(good),"Echo","Else",1),strings.Replace(string(good),"example.query/query@1","Else",1),strings.Replace(string(good),"\"value\": \"x\"","\"value\": false",1)}{f.response=[]byte(bad);if _,e=c.Echo(Record{Value:"x"},"",false,0);e==nil{t.Fatal("accepted response",bad)}}
 if h.calls!=1{t.Fatal(h.calls)}
}
`

func TestReplyCppGoExchange(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		cxx, _ = exec.LookPath("clang++")
	}
	if cxx == "" {
		t.Skip("C++ exchange requires CXX, g++ or clang++")
	}
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module exchange.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.h": genCpp(s), "main.go": replyExchangeGo, "main.cpp": replyExchangeCpp} {
		writeNamespaceFile(t, dir, n, b)
	}
	cpp := filepath.Join(dir, "client.exe")
	goexe := filepath.Join(dir, "host.exe")
	args := []string{"-std=c++17", filepath.Join(dir, "main.cpp"), "-o", cpp}
	if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/EHsc", filepath.Join(dir, "main.cpp"), "/Fe:" + cpp, "/Fo:" + filepath.Join(dir, "main.obj")}
	}
	c := exec.Command(cxx, args...)
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("C++ compile: %v\n%s", e, out)
	}
	c = exec.Command("go", "build", "-o", goexe, "main.go")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("Go compile: %v\n%s", e, out)
	}
	run := func(exe, mode, method string, input []byte) []byte {
		t.Helper()
		c := exec.Command(exe, mode, method)
		c.Stdin = strings.NewReader(string(input))
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %s %s: %v\n%s", exe, mode, method, e, out)
		}
		return out
	}
	for _, method := range []string{"Echo", "Opaque", "Reset", "Fail"} {
		request := run(cpp, "emit", method, nil)
		goRequest := run(goexe, "emit", method, nil)
		if string(request) != string(goRequest) {
			t.Fatalf("%s request byte mismatch\nC++ %s\nGo %s", method, request, goRequest)
		}
		response := run(goexe, "serve", method, request)
		run(cpp, "check", method, response)
		// Same generated C++ client must reject a response belonging to another method.
		bad := strings.Replace(string(response), "\"method\": \""+method+"\"", "\"method\": \"Other\"", 1)
		run(cpp, "reject", method, []byte(bad))
	}
}

const replyExchangeGo = `package main
import("os";"io";"fmt";r "exchange.test/rec")
type handler struct{}
func(handler)Echo(v r.Record,s string,b bool,n int64)(r.Record,error){if s!=""||b||n!=0{panic("bad args")};return v,nil}
func(handler)Opaque(v r.Raw)(r.Raw,error){return v,nil}
func(handler)Reset()error{return nil}
func(handler)Notify()error{return nil}
func(handler)Fail(string)(string,error){return "",&r.ServiceError{Code:"future_code"}}
type capture struct{}
func(capture)WriteFrame(b []byte)error{_,e:=os.Stdout.Write(b);return e}
func(capture)ExchangeFrame(b []byte)([]byte,error){os.Stdout.Write(b);return nil,fmt.Errorf("captured")}
func main(){if os.Args[1]=="emit"{c:=r.NewQueryClient(capture{});switch os.Args[2]{case "Echo":c.Echo(r.Record{Value:"\u96ea<&"},"",false,0);case "Opaque":c.Opaque(r.Raw("{ \"a\" : [1,\n false] }"));case "Reset":c.Reset();case "Fail":c.Fail("future_code")};return};b,e:=io.ReadAll(os.Stdin);if e!=nil{panic(e)};d:=r.QueryDispatcher{Handler:handler{}};v,e:=d.ExchangeFrame(b);if e!=nil{panic(e)};os.Stdout.Write(v)}
`
const replyExchangeCpp = `#include "rec.h"
#include <iostream>
#include <iterator>
#ifdef _WIN32
#include <fcntl.h>
#include <io.h>
#endif
struct Captured{};
struct Transport{
 std::string mode;
 void WriteFrame(std::string_view){}
 std::string ExchangeFrame(std::string_view frame){if(mode=="emit"){std::cout<<frame;throw Captured{};}return std::string(std::istreambuf_iterator<char>(std::cin),{});}
};
int main(int argc,char**argv){
#ifdef _WIN32
_setmode(_fileno(stdin),_O_BINARY);_setmode(_fileno(stdout),_O_BINARY);
#endif
Transport t{argv[1]};rec::QueryClient c(t);std::string method=argv[2];bool reject=t.mode=="reject";
 try{if(method=="Echo"){rec::Record v;v.value="\xE9\x9B\xAA<&";auto result=c.Echo(v,"",false,0);if(result.value!=v.value)return 2;}
 else if(method=="Opaque"){std::string raw="{ \"a\" : [1,\n false] }";if(c.Opaque(raw)!=raw)return 3;}
 else if(method=="Reset"){c.Reset();}
 else if(method=="Fail"){c.Fail("future_code");return 4;}
 return reject?5:0;
 }catch(const Captured&){return t.mode=="emit"?0:6;}
 catch(const rec::DispatchError& e){return reject&&std::string(e.what())=="mismatched_response"?0:7;}
 catch(const rec::ServiceError&e){return !reject&&method=="Fail"&&e.code=="future_code"&&e.message.empty()?0:8;}
 catch(const std::exception&e){std::cerr<<e.what();return 9;}
}
`

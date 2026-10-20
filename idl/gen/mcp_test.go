package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPCLIPathsAndAdmission(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api.thrift")
	source := head + `struct Record {1:required string value}(document="true",unknown_fields="refuse") service Query {Record Read()(readonly="true")}(wire_name="test/query@1")`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{path, "--paths", "go", "--mcp"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "go/rec/mcp.go\ngo/rec/mcp.json\n") {
		t.Fatal(out.String())
	}
	for _, flags := range [][]string{{"go", "--no-ipc", "--mcp"}, {"python", "--mcp"}, {"go", "--mcp", "--mcp"}} {
		args := append([]string{path, filepath.Join(dir, "out")}, flags...)
		if err := run(args, &bytes.Buffer{}); err == nil {
			t.Fatal(flags)
		}
	}
	if err := os.WriteFile(path, []byte(source+` struct MCPMethod {1:required bool value}(unknown_fields="refuse")`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{path, filepath.Join(dir, "out"), "go", "--mcp"}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatal(err)
	}
}

func TestMCPGeneratedCodecJoin(t *testing.T) {
	d, err := parse(head + `struct Record {1:required string value}(document="true",unknown_fields="refuse")
service Query {Record Read(1:string key)(readonly="true") Record Call(1:string key)(idempotency_key="key") Record Person()(remote="person")}(wire_name="test/query@1")`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module mcp.test\n\ngo 1.22\n", "rec.go": genGo(d), "mcp.go": genMCP(d), "rec_test.go": mcpCodecTest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

const mcpCodecTest = `package rec
import("bytes";"testing";"errors")
type lost struct{ calls int; err error }
func(t *lost)ExchangeFrame([]byte)([]byte,error){t.calls++;return nil,t.err}
func(t *lost)WriteFrame([]byte)error{t.calls++;return t.err}
type handler struct{ calls int }
func(h *handler)Read(key string)(Record,error){h.calls++;return Record{Value:key},nil}
func(h *handler)Call(key string)(Record,error){h.calls++;return Record{Value:key},nil}
func(h *handler)Person()(Record,error){return Record{},&ServiceError{Code:"forbidden"}}
func TestJoin(t *testing.T){
 h:=&handler{};d:=&QueryDispatcher{Handler:h}
 out,e:=MCPInvoke(d,"test/query@1","Read",[]byte("{\"key\":\"native\"}"));if e!=nil||!bytes.Contains(out,[]byte("native")){t.Fatal(string(out),e)}
 for _,input:=range []string{"{\"key\":\"a\",\"key\":\"b\"}","{\"key\":1}","{} trailing"}{if _,e:=MCPInvoke(d,"test/query@1","Read",[]byte(input));e==nil{t.Fatal(input)}}
 if h.calls!=1{t.Fatal("invalid arguments dispatched",h.calls)}
 if _,e:=MCPInvoke(d,"test/query@1","Person",[]byte("{}"));e==nil||e.(*ServiceError).Code!="forbidden"{t.Fatal(e)}else if raw,ok:=MCPServiceError(e);!ok||!bytes.Contains(raw,[]byte("forbidden")){t.Fatal(string(raw),ok)}
 uncertain:=errors.New("uncertain native result");transport:=&lost{err:uncertain};if _,e:=MCPInvoke(transport,"test/query@1","Call",[]byte("{\"key\":\"same\"}"));e!=uncertain||transport.calls!=1{t.Fatal(e,transport.calls)};if _,ok:=MCPServiceError(uncertain);ok{t.Fatal("uncertain error reclassified")}
 methods:=MCPMethods();if methods[0].Class!="read"||methods[1].Class!="submit"||methods[1].IdempotencyKey!="key"||methods[2].Class!="effect"||methods[0].ArgumentsSchema==""{t.Fatal(methods)}
}
`

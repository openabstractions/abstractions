package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func servicePython(t *testing.T) string {
	t.Helper()
	p := os.Getenv("PYTHON")
	if p == "" {
		p, _ = exec.LookPath("python")
	}
	if p == "" {
		t.Skip("Python unavailable")
	}
	return p
}
func TestPythonServiceValidation(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateServiceBackend(s, "python"); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ old, new string }{{"Query {", "bytes {"}, {"Echo(", "async("}, {"Echo(", "__init__("}, {"Echo(", "_transport("}, {"string text", "string text(python.name=\"class\")"}, {"string text", "string text(python.name=\"record\")"}, {"string text", "string text(python.name=\"self\")"}, {"string value", "string value(python.name=\"a.b\")"}} {
		d, e := parse(head + strings.Replace(replyFixture, tc.old, tc.new, 1))
		if e != nil {
			t.Fatal("must reach Python preflight", tc, e)
		}
		if e = validateServiceBackend(d, "python"); e == nil {
			t.Fatal("accepted", tc)
		}
	}
	alias := strings.Replace(replyFixture, "string text", "string text(python.name=\"label\")", 1)
	s, e = parse(head + alias)
	if e != nil || validateServiceBackend(s, "python") != nil {
		t.Fatal("legal alias", e)
	}
	selectedDef, e := selected(s, []string{"Record"})
	if e != nil {
		t.Fatal(e)
	}
	plain, e := parse(head + strings.Split(alias, "service Query")[0])
	if e != nil {
		t.Fatal(e)
	}
	if genPy(selectedDef) != genPy(plain) {
		t.Fatal("record-only selection changed bytes")
	}
}
func TestPythonGoServiceExchange(t *testing.T) {
	p := servicePython(t)
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	host := strings.Replace(replyExchangeGo, "v,e:=d.ExchangeFrame(b)", `if os.Args[1]=="oneway"{if e=d.WriteFrame(b);e!=nil{panic(e)};return};v,e:=d.ExchangeFrame(b)`, 1)
	for n, b := range map[string]string{"go.mod": "module exchange.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "main.go": host, "main.py": pythonExchangeTest} {
		writeNamespaceFile(t, dir, n, b)
	}
	exe := filepath.Join(dir, "host.exe")
	c := exec.Command("go", "build", "-o", exe, "main.go")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("Go host %v\n%s", e, out)
	}
	c = exec.Command(p, "-B", "main.py", exe)
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("Python exchange %v\n%s", e, out)
	} else {
		t.Log(string(out))
	}
}

const pythonExchangeTest = `import sys, subprocess, json
import rec as r
host=sys.argv[1]
class Transport:
 def __init__(self):self.calls=0;self.frame=None;self.reply=None
 def write_frame(self,frame):
  self.calls+=1;self.frame=frame
  subprocess.run([host,'oneway','Notify'],input=frame,check=True,capture_output=True)
 def exchange_frame(self,frame):
  self.calls+=1;self.frame=frame
  if self.reply is not None:return self.reply
  return subprocess.run([host,'serve',''],input=frame,check=True,capture_output=True).stdout
t=Transport();c=r.QueryClient(t)
def equal_request(method):assert t.frame==subprocess.run([host,'emit',method],capture_output=True,check=True).stdout
v=c.Echo(r.Record(value='\u96ea<&'),'',False,0);assert v.value=='\u96ea<&';equal_request('Echo')
raw=b'{ "a" : [1,\n false] }';assert c.Opaque(raw)==raw;equal_request('Opaque')
assert c.Reset() is None;equal_request('Reset')
try:c.Fail('future_code');raise AssertionError('accepted failure')
except r.ServiceError as e:assert e.code=='future_code' and e.message==''
equal_request('Fail');assert c.Notify() is None
count=t.calls
for args in [(r.Record(value='x'),'',False,True),(r.Record(value='x'),'',False,1.5),(r.Record(value='x'),'',False,2**63),(r.Record(value='x'),'',0,0),(r.Record(value=1),'',False,0)]:
 try:c.Echo(*args);raise AssertionError('coerced wrong type')
 except r.Refusal:pass
for raw in [b'{',b'{}{}',b'{"a":1,"a":2}',b'"\xff"']:
 try:c.Opaque(raw);raise AssertionError('accepted bad raw')
 except r.Refusal:pass
assert t.calls==count
reply={'version':1,'service':'example.query/query@1','method':'Echo','ok':True,'payload':{'value':{'value':'x'}}}
for bad in [b'{',json.dumps(reply).encode()+b'{}',json.dumps(dict(reply,version=2)).encode(),json.dumps(dict(reply,service='wrong')).encode(),json.dumps(dict(reply,method='wrong')).encode(),json.dumps(dict(reply,payload={})).encode(),json.dumps(dict(reply,payload={'value':{'value':False}})).encode(),json.dumps(dict(reply,ok=False,payload={'code':'','message':''})).encode(),json.dumps(dict(reply,ok=False,payload={'code':'future_code'})).encode()]:
 t.reply=bad
 try:c.Echo(r.Record(value='x'),'',False,0);raise AssertionError('accepted bad response')
 except (r.Refusal,r.DispatchError):pass
t.reply=json.dumps(dict(reply,ok=False,payload={'code':'unknown','message':''})).encode()
try:c.Echo(r.Record(value='x'),'',False,0);raise AssertionError('accepted failure')
except r.ServiceError as e:assert e.code=='unknown' and e.message==''
class Offline:
 def exchange_frame(self,frame):raise OSError('offline')
try:r.QueryClient(Offline()).Reset();raise AssertionError('swallowed transport error')
except OSError:pass
print('Python '+sys.version.split()[0]+': typed replies, oneway, raw bytes, zero/false/empty, malformed frames and unknown errors passed')
`

func TestPythonLoggingClient(t *testing.T) {
	p := servicePython(t)
	path := "../../openabstractions-flat/abstraction-logging/logging.thrift"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		path = "../testdata/logging.thrift"
	}
	source, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	s, e := parse(string(source))
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module log.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.py": genPy(s), "main.go": pythonLoggingHost, "main.py": pythonLoggingTest} {
		writeNamespaceFile(t, dir, n, b)
	}
	exe := filepath.Join(dir, "host.exe")
	c := exec.Command("go", "build", "-o", exe, "main.go")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("logging host %v\n%s", e, out)
	}
	c = exec.Command(p, "-B", "main.py", exe)
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("logging client %v\n%s", e, out)
	}
	// Exercise real CLI preflight, not just internal emitters.
	input := filepath.Join(dir, "logging.thrift")
	os.WriteFile(input, source, 0600)
	var report bytes.Buffer
	if e = run([]string{input, filepath.Join(dir, "output"), "python"}, &report); e != nil {
		t.Fatal(e)
	}
}

const pythonLoggingHost = `package main
import("io";"os";r "log.test/rec")
type handler struct{}
func(handler)Write(v r.Record)error{if v.Schema!=1||v.Msg!=""||v.Level!=0{panic("changed log record")};return nil}
func main(){b,e:=io.ReadAll(os.Stdin);if e!=nil{panic(e)};d:=r.SinkDispatcher{Handler:handler{}};if e=d.WriteFrame(b);e!=nil{panic(e)}}
`
const pythonLoggingTest = `import sys,subprocess
import rec as r
class Transport:
 calls=0
 def write_frame(self,frame):self.calls+=1;subprocess.run([sys.argv[1]],input=frame,capture_output=True,check=True)
t=Transport();c=r.SinkClient(t);v=r.Record(schema=1,time='2026-09-11T00:00:00Z',level=0,msg='');assert c.Write(v) is None and t.calls==1
v.schema=2
try:c.Write(v);raise AssertionError('bad schema emitted')
except r.Refusal as e:assert e.word=='bad_schema'
assert t.calls==1
`

func TestPythonServiceAliasesAndVocabulary(t *testing.T) {
	source := head + `struct Record{1:required list<string> content 2:optional list<string> critical(omit="zero")}(document="true",unknown_fields="refuse")
 vocabulary Content {1:"base"(when="always")}(of="Record",names="content",critical="critical")
 service S{oneway void Write(1:string args(python.name="_oa_args"),2:string other(python.name="label")) Record Echo(1:Record record)}(wire_name="example/s@1")`
	s, e := parse(source)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateServiceBackend(s, "python"); e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.py", genPy(s))
	writeNamespaceFile(t, dir, "test.py", pythonAliasVocabularyTest)
	c := exec.Command(servicePython(t), "-B", "test.py")
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("aliases/vocabulary %v\n%s", e, out)
	}
}

const pythonAliasVocabularyTest = `import json
import rec as r
class T:
 calls=0
 def write_frame(self,b):self.calls+=1;assert json.loads(b)['arguments']=={'args':'one','other':'two'}
 def exchange_frame(self,b):self.calls+=1;return b'{"version":1,"service":"example/s@1","method":"Echo","ok":true,"payload":{"value":{"content":[]}}}'
t=T();c=r.SClient(t);c.Write(_oa_args='one',label='two');assert t.calls==1
try:c.Echo(r.Record(content=[]));raise AssertionError('vocabulary bypass')
except r.Refusal as e:assert e.word=='content_mismatch'
assert t.calls==1
try:c.Echo(r.Record(content=['base']));raise AssertionError('invalid result vocabulary')
except r.Refusal as e:assert e.word=='content_mismatch'
assert t.calls==2
`

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestJavaScriptServiceNoIPCAndFullMask(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	var fields strings.Builder
	for i := 1; i <= 32; i++ {
		fmt.Fprintf(&fields, "%d:required string f%d\n", i, i)
	}
	s, e := parse(head + "struct Record {" + fields.String() + "}(document=\"true\",unknown_fields=\"refuse\")\nservice Store {Record Load(1:string key)}(wire_name=\"example/store@1\")")
	if e != nil {
		t.Fatal(e)
	}
	s.NoIPC = true
	if e = validateServiceBackend(s, "javascript"); e != nil {
		t.Fatal(e)
	}
	body := genJS(s)
	if strings.Contains(body, "OAServiceFrame") || strings.Contains(body, "StoreClient") {
		t.Fatal("no-IPC output gained transport")
	}
	dir := t.TempDir()
	writeNamespaceFile(t, dir, "rec.mjs", body)
	writeNamespaceFile(t, dir, "test.mjs", `import assert from 'node:assert/strict';import * as r from './rec.mjs';
const value=r.newRecord();assert.deepEqual(r.decode(r.encode(value)),value);
await assert.rejects(new r.Store().Load('key'),/not implemented/);
class Memory extends r.Store {async Load(key){return value;}}
assert.equal(await new Memory().Load('key'),value);`)
	c := exec.Command(node, "test.mjs")
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("interface/mask %v\n%s", e, out)
	}
}

func TestJavaScriptServiceExchange(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	s, err := parse(head + replyFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err = validateServiceBackend(s, "javascript"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	host := strings.Replace(replyExchangeGo, "v,e:=d.ExchangeFrame(b)", `if os.Args[1]=="oneway"{if e=d.WriteFrame(b);e!=nil{panic(e)};return};v,e:=d.ExchangeFrame(b)`, 1)
	for name, body := range map[string]string{"go.mod": "module exchange.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.mjs": genJS(s), "main.go": host, "main.mjs": jsServiceExchangeTest} {
		writeNamespaceFile(t, dir, name, body)
	}
	exe := filepath.Join(dir, "host.exe")
	c := exec.Command("go", "build", "-o", exe, "main.go")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("host %v\n%s", e, out)
	}
	c = exec.Command(node, "main.mjs", exe)
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("JavaScript exchange %v\n%s", e, out)
	} else {
		t.Log(string(out))
	}
}

func TestJavaScriptServicePreflight(t *testing.T) {
	for _, change := range [][2]string{{"Query {", "Reader {"}, {"Echo(", "constructor("}, {"Echo(", "then("}, {"string value", "string value(javascript.name=\"bad.name\")"}} {
		s, e := parse(head + strings.Replace(replyFixture, change[0], change[1], 1))
		if e != nil {
			continue
		}
		if validateServiceBackend(s, "javascript") == nil {
			t.Fatalf("accepted collision %v", change)
		}
	}
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	selectedDef, e := selected(s, []string{"Record"})
	if e != nil {
		t.Fatal(e)
	}
	plain, e := parse(head + strings.Split(replyFixture, "service Query")[0])
	if e != nil {
		t.Fatal(e)
	}
	if genJS(selectedDef) != genJS(plain) {
		t.Fatal("record selection changed")
	}
}

const jsServiceExchangeTest = `
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import * as r from './rec.mjs';
const host=process.argv[2];
let calls=0;
function invoke(mode,frame) {calls++;const p=spawnSync(host,[mode],{input:frame});assert.equal(p.status,0,p.stderr.toString());return new Uint8Array(p.stdout);}
const transport={exchangeFrame:async frame=>invoke('exchange',frame),writeFrame:async frame=>{invoke('oneway',frame);}};
const c=new r.QueryClient(transport);
const record=r.newRecord();record.value='雪<&';
assert.equal((await c.Echo(record,'',false,0n)).value,record.value);
const raw='{ "a" : [1,\n false] }';
assert.equal(new TextDecoder().decode(await c.Opaque(raw)),raw);
assert.equal(await c.Reset(),undefined);
assert.equal(await c.Notify(),undefined);
await assert.rejects(c.Fail('future_code'),e=>e instanceof r.ServiceError&&e.code==='future_code');
const count=calls;
for(const args of [[record,'',false,0],[record,'',0,0n],[{...record,value:'\ud800'},'',false,0n],[record,'',false,9223372036854775808n]]) await assert.rejects(c.Echo(...args),r.Refusal);
for(const value of ['{','{}{}','{"same":1,"same":2}']) await assert.rejects(c.Opaque(value),r.Refusal);
assert.equal(calls,count);
const good={version:1,service:'example.query/query@1',method:'Echo',ok:true,payload:{value:{value:'x'}}};
for(const value of [{...good,version:2},{...good,method:'wrong'},{...good,service:'wrong'},{...good,payload:{}},{...good,payload:{value:{value:false}}},{...good,ok:false,payload:{code:'',message:''}}]) {
 const bad=new r.QueryClient({exchangeFrame:async()=>new TextEncoder().encode(JSON.stringify(value))});
 await assert.rejects(bad.Echo(record,'',false,0n),e=>e instanceof r.Refusal||e instanceof r.DispatchError);
}
const offline=new Error('offline');
await assert.rejects(new r.QueryClient({exchangeFrame:async()=>{throw offline;}}).Reset(),e=>e===offline);
console.log('PASS: generated JavaScript/Go replies, oneway, exact raw tokens, typed input and hostile reply controls');
`

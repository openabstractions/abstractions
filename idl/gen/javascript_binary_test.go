package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestJavaScriptBinary(t *testing.T) {
	node, e := exec.LookPath("node")
	if e != nil {
		t.Fatal(e)
	}
	s, e := parse(head + `struct Value {1: required binary required_data 2: optional binary data(omit="absent") 3: optional binary sparse(omit="zero")}(document="true",unknown_fields="refuse")
 service Blob { binary Echo(1:binary data) oneway void Put(1:binary data) }(wire_name="example/blob@1")`)
	if e != nil {
		t.Fatal(e)
	}
	if e = validateBinaryBackend(s, "javascript"); e != nil {
		t.Fatal(e)
	}
	if e = validateServiceBackend(s, "javascript"); e != nil {
		t.Fatal(e)
	}
	body := genJS(s)
	for _, mutation := range []bool{false, true} {
		t.Run(map[bool]string{false: "canonical", true: "canonical-control"}[mutation], func(t *testing.T) {
			generated := body
			if mutation {
				generated = strings.Replace(generated, `if (encodeBinary(result) !== text) bad();`, "", 1)
				if generated == body {
					t.Fatal("mutation did not apply")
				}
			}
			dir := t.TempDir()
			writeNamespaceFile(t, dir, "rec.mjs", generated)
			writeNamespaceFile(t, dir, "test.mjs", jsBinaryTest)
			c := exec.Command(node, "test.mjs")
			c.Dir = dir
			out, err := c.CombinedOutput()
			if mutation {
				if err == nil || !strings.Contains(string(out), "Missing expected exception") {
					t.Fatalf("control: %v %s", err, out)
				}
			} else if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
		})
	}
}

const jsBinaryTest = `import assert from 'node:assert/strict';import * as r from './rec.mjs';
const te=new TextEncoder(),td=new TextDecoder();
const all=Uint8Array.from({length:131079},(_,i)=>i&255);
for(const data of [null,new Uint8Array(),Uint8Array.of(0),Uint8Array.of(255,0),all,all.subarray(3,260),Buffer.from([1,2,3])]) {
 const v=r.newValue();v.required_data=data??new Uint8Array();v.data=data;
 const raw=r.encode(v),back=r.decode(raw);
 assert.deepEqual(back.required_data,new Uint8Array(v.required_data));
 if(data===null)assert.equal(back.data,null);else {assert.deepEqual(back.data,new Uint8Array(data));assert.equal(JSON.parse(td.decode(raw)).data,Buffer.from(data).toString('base64'));}
}
for(const bad of ['A','AA','AAA','AB==','AAB=','AA=A','====','AA==AAAA','AA-_','AA==\n']) {
 assert.throws(()=>r.decode(te.encode(JSON.stringify({required_data:'',data:bad}))),e=>e instanceof r.Refusal&&e.word==='bad_binary');
}
for(const raw of ['{}','{"required_data":null}']) assert.throws(()=>r.decode(te.encode(raw)),r.Refusal);
for(const bad of ['',[],new Uint16Array(),new ArrayBuffer(0),1,undefined]) {
 for(const field of ['required_data','data','sparse']) {if(field==='data'&&bad===undefined)continue;const v=r.newValue();v[field]=bad;assert.throws(()=>r.encode(v),e=>e instanceof r.Refusal&&e.word==='wrong_type');}
}
let calls=0;const failure=new Error('uncertain write');
const transport={exchangeFrame:async frame=>{calls++;const q=JSON.parse(td.decode(frame));assert.equal(q.arguments.data,Buffer.from(all).toString('base64'));return te.encode(JSON.stringify({version:1,service:q.service,method:q.method,ok:true,payload:{value:q.arguments.data}}));},writeFrame:async frame=>{calls++;assert.equal(JSON.parse(td.decode(frame)).arguments.data,'AP8=');throw failure;}};
const client=new r.BlobClient(transport);assert.deepEqual(await client.Echo(all),all);
await assert.rejects(client.Put(Uint8Array.of(0,255)),e=>e===failure);assert.equal(calls,2);
await assert.rejects(client.Echo('text'),r.Refusal);assert.equal(calls,2);
const forged=new r.BlobClient({exchangeFrame:async()=>te.encode(JSON.stringify({version:1,service:'example/blob@1',method:'Echo',ok:true,payload:{value:'AB=='}}))});
await assert.rejects(forged.Echo(new Uint8Array()),e=>e instanceof r.Refusal&&e.word==='bad_binary');
`

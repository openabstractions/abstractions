package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const cleanupFixture = compatHead + `enum Outcome {1:ok 2:no}(unknown="refuse",reader="act")
struct Leaf {1:required binary value 2:optional binary spare(omit="absent")}(unknown_fields="refuse")
struct Branch {1:required list<Leaf> items 2:optional Leaf extra(omit="absent") 3:required map<string,json> raw 4:required map<string,string> labels}(unknown_fields="refuse")
struct Data {1:required Leaf primary 2:required list<Branch> branches}(unknown_fields="refuse")
struct Reply {1:required Outcome outcome 2:optional Data data(omit="absent") 3:optional binary side(omit="absent")}(document="true",unknown_fields="refuse")
service Query {Reply Read(1:binary input)(result_present="data",result_when="outcome",result_is="ok")}(wire_name="test/cleanup@1")`

func TestDecodedCleanupOwnership(t *testing.T) {
	d := mustParse(t, cleanupFixture)
	payload := `{"primary":{"value":"AP8K","spare":"AP8K"},"branches":[{"items":[{"value":"AP8K","spare":"AP8K"}],"extra":{"value":"AP8K","spare":"AP8K"},"raw":{"opaque":"keep"},"labels":{"opaque":"keep"}}]}`
	records := []string{`{"outcome":"ok","data":` + payload + `,"side":"AP8K"}`, `{"outcome":"no","data":` + payload + `,"side":"AP8K"}`, `{"outcome":"ok","side":"AP8K"}`}
	var replies []string
	for _, record := range records {
		replies = append(replies, `{"version":1,"service":"test/cleanup@1","method":"Read","ok":true,"payload":{"value":`+record+`}}`)
	}
	bad := strings.Replace(replies[0], `"value":"AP8K"`, `"value":"!"`, 1)
	request := `{"version":1,"service":"test/cleanup@1","method":"Read","arguments":{"input":"AP8K"}}`
	render := func(code string) string {
		return strings.NewReplacer("REPLIES", quotedList(replies), "BAD", fmt.Sprintf("%q", bad), "REQUEST", fmt.Sprintf("%q", request), "RECORD", fmt.Sprintf("%q", records[1])).Replace(code)
	}
	run := func(t *testing.T, dir string, args ...string) string {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		code := genGo(d)
		start := strings.Index(code, "func (c *QueryClient) Read(")
		if start < 0 {
			t.Fatal("client method missing")
		}
		end := start + strings.Index(code[start:], "\n}") + 2
		body := code[start:end]
		marker := `err = &ServiceError{Code: ServiceErrorCodeInvalidResult}`
		if strings.Count(body, marker) != 1 {
			t.Fatal("client rejection seam changed")
		}
		code = code[:start] + strings.Replace(body, marker, "cleanupObserve(&decoded.Value)\n"+marker, 1) + code[end:]
		writeNamespaceFile(t, dir, "go.mod", "module cleanup.test\n\ngo 1.26\n")
		writeNamespaceFile(t, dir, "rec.go", code)
		writeNamespaceFile(t, dir, "rec_test.go", render(`package rec
import("bytes";"errors";"testing")
var observations int
func checkBytes(value []byte,zero bool){want:=[]byte{0,255,10};if zero{want=make([]byte,len(value))};if !bytes.Equal(value,want){panic("decoded byte ownership")}}
func checkValue(v *Reply,zero bool){checkBytes(v.Side,zero);if v.Data==nil{return};checkBytes(v.Data.Primary.Value,zero);checkBytes(v.Data.Primary.Spare,zero);for _,b:=range v.Data.Branches{for _,v:=range b.Items{checkBytes(v.Value,zero);checkBytes(v.Spare,zero)};if b.Extra!=nil{checkBytes(b.Extra.Value,zero);checkBytes(b.Extra.Spare,zero)};if b.Raw["opaque"]!="\"keep\""||b.Labels["opaque"]!="keep"{panic("map ownership changed")}}}
func cleanupObserve(v *Reply){observations++;checkValue(v,true)}
type transport struct{raw []byte};func(f transport)ExchangeFrame([]byte)([]byte,error){return f.raw,nil}
type handler struct{v Reply};func(h handler)Read([]byte)(Reply,error){return h.v,nil}
func TestOwnership(t *testing.T){input:=[]byte{0,255,10};for i,raw:=range []string{REPLIES}{frame:=[]byte(raw);v,e:=NewQueryClient(transport{frame}).Read(input);if i==0{if e!=nil{t.Fatal(e)};checkValue(&v,false)}else{var s *ServiceError;if !errors.As(e,&s)||s.Code!=ServiceErrorCodeInvalidResult||v.Data!=nil{t.Fatal("wrong postcondition refusal",e)}};if string(frame)!=raw||!bytes.Equal(input,[]byte{0,255,10}){t.Fatal("external buffer changed")}};if observations!=2{t.Fatal(observations)};_,e:=NewQueryClient(transport{[]byte(BAD)}).Read(input);var refusal *Refusal;if !errors.As(e,&refusal)||refusal.Word!="bad_binary"||observations!=2{t.Fatal("codec refusal changed",e)};hvalue,e:=Decode([]byte(RECORD));if e!=nil{t.Fatal(e)};_,e=(&QueryDispatcher{Handler:handler{*hvalue}}).ExchangeFrame([]byte(REQUEST));if e!=nil{t.Fatal(e)};checkValue(hvalue,false)}
`))
		run(t, dir, "go", "test", ".")
		// Receive the selected optimizing compiler with no test observation hook.
		var primitive strings.Builder
		emitDecodedCleanupPrimitive(&primitive, d, "go")
		writeNamespaceFile(t, dir, "primitive.go", "package primitive\n"+primitive.String()+"\nfunc Probe(value []byte){serviceClearDecodedBinary(value)}\n")
		assembly := run(t, dir, "go", "tool", "compile", "-S", "-o", filepath.Join(dir, "primitive.o"), "primitive.go")
		if !strings.Contains(assembly, "runtime.memclrNoHeapPointers") || !strings.Contains(assembly, "CALL\t<unlinkable>.serviceClearDecodedBinary") {
			t.Fatal("optimized clear/call missing")
		}
	})
	t.Run("cpp", func(t *testing.T) {
		dir := t.TempDir()
		code := genCpp(d)
		marker := `throw ServiceError("invalid_result","");`
		if strings.Count(code, marker) < 2 {
			t.Fatal("client/dispatcher rejection seams changed")
		}
		code = strings.Replace(code, marker, `cleanup_observe(result.value);`+marker, 1)
		writeNamespaceFile(t, dir, "rec.h", code)
		writeNamespaceFile(t, dir, "probe.cpp", render(`namespace rec{struct Reply;void cleanup_observe(const Reply&);}
#include "rec.h"
#include <cassert>
namespace r=rec;int observations=0;
void check_bytes(const std::vector<std::uint8_t>& v,bool zero){assert(v.size()==3);for(std::size_t i=0;i<v.size();++i)assert(v[i]==(zero?0:std::vector<std::uint8_t>{0,255,10}[i]));}
void check_value(const r::Reply& v,bool zero){assert(v.side);check_bytes(*v.side,zero);if(!v.data)return;check_bytes(v.data->primary.value,zero);check_bytes(*v.data->primary.spare,zero);for(const auto& b:v.data->branches){for(const auto& leaf:b.items){check_bytes(leaf.value,zero);check_bytes(*leaf.spare,zero);}if(b.extra){check_bytes(b.extra->value,zero);check_bytes(*b.extra->spare,zero);}assert(b.raw.at("opaque")=="\"keep\""&&b.labels.at("opaque")=="keep");}}
void rec::cleanup_observe(const r::Reply& value){++observations;check_value(value,true);}
struct Transport{std::string raw;std::string exchange_frame(std::string_view){return raw;}};struct Handler:r::Query{r::Reply value;r::Reply read(const std::vector<std::uint8_t>&)override{return value;}};
int main(){std::vector<std::uint8_t> input{0,255,10};int i=0;for(auto raw:{REPLIES}){Transport f{raw};r::QueryClient<Transport> c(f);try{auto value=c.read(input);assert(i==0);check_value(value,false);}catch(const r::ServiceError& e){assert(i!=0&&e.code=="invalid_result");}assert(f.raw==raw&&input==std::vector<std::uint8_t>({0,255,10}));++i;}assert(observations==2);Transport f{BAD};r::QueryClient<Transport> c(f);try{c.read(input);assert(false);}catch(const r::Refusal&e){assert(std::string(e.word)=="bad_binary");}assert(observations==2);Handler h;h.value=r::decode(RECORD);r::QueryDispatcher dispatcher(h);dispatcher.exchange_frame(REQUEST);check_value(h.value,false);}
`))
		run(t, dir, "g++", "-std=c++17", "-O2", "probe.cpp", "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
	t.Run("rust", func(t *testing.T) {
		dir := t.TempDir()
		code := genRust(d)
		marker := `return Err(CallError::Service(ServiceError { code: "invalid_result".into()`
		if strings.Count(code, marker) != 1 {
			t.Fatal("client rejection seam changed")
		}
		code = strings.Replace(code, marker, `cleanup_observe(&result.value);`+marker, 1)
		writeNamespaceFile(t, dir, "rec.rs", code+`
fn check_bytes(v:&[u8],zero:bool){assert_eq!(v,if zero{&[0,0,0]}else{&[0,255,10]});}
pub fn check_value(v:&Reply,zero:bool){check_bytes(v.side.as_ref().unwrap(),zero);if let Some(data)=&v.data{check_bytes(&data.primary.value,zero);check_bytes(data.primary.spare.as_ref().unwrap(),zero);for b in &data.branches{for leaf in &b.items{check_bytes(&leaf.value,zero);check_bytes(leaf.spare.as_ref().unwrap(),zero);}if let Some(leaf)=&b.extra{check_bytes(&leaf.value,zero);check_bytes(leaf.spare.as_ref().unwrap(),zero);}assert_eq!(b.raw.get("opaque").unwrap(),b"\"keep\"");assert_eq!(b.labels.get("opaque").unwrap(),"keep");}}}
pub static OBSERVED:std::sync::atomic::AtomicUsize=std::sync::atomic::AtomicUsize::new(0);
fn cleanup_observe(v:&Reply){OBSERVED.fetch_add(1,std::sync::atomic::Ordering::SeqCst);check_value(v,true);}
`)
		writeNamespaceFile(t, dir, "main.rs", render(`mod rec;use rec::*;struct Transport{raw:&'static str}impl FrameTransport for Transport{type Error=();fn write_frame(&self,_:&[u8])->Result<(),()>{Ok(())}fn exchange_frame(&self,_:&[u8])->Result<Vec<u8>,()>{Ok(self.raw.as_bytes().to_vec())}}
fn main(){let input=vec![0,255,10];for(i,raw)in[REPLIES].iter().enumerate(){match QueryClient::new(Transport{raw}).read(input.clone()){Ok(v)=>{assert_eq!(i,0);check_value(&v,false);},Err(CallError::Service(e))=>{assert!(i>0);assert_eq!(e.code,"invalid_result");},_=>panic!("wrong refusal")};assert_eq!(input,vec![0,255,10]);}assert_eq!(OBSERVED.load(std::sync::atomic::Ordering::SeqCst),2);match QueryClient::new(Transport{raw:BAD}).read(input){Err(CallError::Refusal(e))=>assert_eq!(e.word,"bad_binary"),_=>panic!("codec refusal changed")};assert_eq!(OBSERVED.load(std::sync::atomic::Ordering::SeqCst),2);}
`))
		run(t, dir, rustServiceCompiler(t), "--edition=2021", "-O", "main.rs", "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
	t.Run("javascript", func(t *testing.T) {
		dir := t.TempDir()
		code := genJS(d)
		marker := `throw new ServiceError("invalid_result", "");`
		if strings.Count(code, marker) != 1 {
			t.Fatal("client rejection seam changed")
		}
		code = strings.Replace(code, marker, `globalThis.cleanupObserve(_result.value);`+marker, 1)
		writeNamespaceFile(t, dir, "rec.mjs", code)
		writeNamespaceFile(t, dir, "probe.mjs", render(`import assert from 'node:assert/strict';import * as r from './rec.mjs';let observed=0;
function bytes(v,zero){assert.deepEqual([...v],zero?[0,0,0]:[0,255,10]);}function value(v,zero){bytes(v.side,zero);if(!v.data)return;bytes(v.data.primary.value,zero);bytes(v.data.primary.spare,zero);for(const b of v.data.branches){for(const leaf of b.items){bytes(leaf.value,zero);bytes(leaf.spare,zero);}if(b.extra){bytes(b.extra.value,zero);bytes(b.extra.spare,zero);}assert.equal(new TextDecoder().decode(b.raw.opaque),'"keep"');assert.equal(b.labels.opaque,'keep');}}
globalThis.cleanupObserve=v=>{++observed;value(v,true);};const input=new Uint8Array([0,255,10]);for(const [i,raw]of[REPLIES].entries()){const frame=new TextEncoder().encode(raw);const c=new r.QueryClient({exchangeFrame:async()=>frame});if(i===0)value(await c.read(input),false);else await assert.rejects(c.read(input),e=>e instanceof r.ServiceError&&e.code==='invalid_result');assert.equal(new TextDecoder().decode(frame),raw);assert.deepEqual([...input],[0,255,10]);}assert.equal(observed,2);const c=new r.QueryClient({exchangeFrame:async()=>new TextEncoder().encode(BAD)});await assert.rejects(c.read(input),e=>e instanceof r.Refusal&&e.word==='bad_binary');assert.equal(observed,2);
`))
		run(t, dir, "node", "probe.mjs")
	})
	t.Run("python", func(t *testing.T) {
		dir := t.TempDir()
		code := genPy(d)
		if strings.Contains(code, "clear_decoded_binary") {
			t.Fatal("immutable Python byte cleanup introduced")
		}
		writeNamespaceFile(t, dir, "rec.py", code)
		writeNamespaceFile(t, dir, "probe.py", render(`import rec
class Transport:
    def __init__(self,raw): self.raw=raw.encode()
    def exchange_frame(self,_): return self.raw
input=bytes([0,255,10])
for i,raw in enumerate([REPLIES]):
    transport=Transport(raw)
    try:
        v=rec.QueryClient(transport).read(input)
        assert i==0 and v.data.primary.value==input and v.side==input
    except rec.ServiceError as e: assert i>0 and e.code=='invalid_result'
    assert transport.raw==raw.encode() and input==bytes([0,255,10])
try: rec.QueryClient(Transport(BAD)).read(input); assert False
except rec.Refusal as e: assert e.word=='bad_binary'
`))
		run(t, dir, os.Getenv("PYTHON"), "probe.py")
	})
}

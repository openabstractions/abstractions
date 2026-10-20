package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveMetadataValidatesExistingFields(t *testing.T) {
	base := head + `struct Page {1:required string next}(document="true",unknown_fields="refuse")
service Watch {Page Observe(1:string cursor,2:i64 wait_ms)(observe="cursor")}(wire_name="test/watch@1")`
	for name, src := range map[string]string{
		"missing input":                strings.Replace(base, `observe="cursor"`, `observe="missing"`, 1),
		"input type":                   strings.Replace(base, "1:string cursor", "1:i64 cursor", 1),
		"output type":                  strings.Replace(base, "required string next", "required i64 next", 1),
		"optional output":              strings.Replace(base, "required string next", "optional string next (omit=\"absent\")", 1),
		"wait type":                    strings.Replace(base, "2:i64 wait_ms", "2:string wait_ms", 1),
		"orphan mapping":               strings.Replace(base, `observe="cursor"`, `observe_next="next"`, 1),
		"factory collision":            strings.Replace(base, "}(wire_name", ` Page ObservePages() }(wire_name`, 1),
		"initialism factory collision": strings.Replace(strings.Replace(base, "Page Observe(", "Page URL(", 1), "}(wire_name", ` Page UrlPages() }(wire_name`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(src); err == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	if _, err := parse(base); err != nil {
		t.Fatal(err)
	}
	if _, err := parse(strings.ReplaceAll(strings.Replace(base, "string next", "string cursor", 1), `observe="cursor"`, `observe="cursor",observe_next="cursor"`)); err != nil {
		t.Fatal(err)
	}
}

func observedConfig(t *testing.T) *Definition {
	t.Helper()
	raw, err := os.ReadFile(productionFile(t, "abstraction-config/config.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	if !strings.Contains(src, `observe="cursor"`) {
		src = strings.Replace(src, `(doc="Empty cursor`, `(observe="cursor", observe_next="cursor", doc="Empty cursor`, 1)
	}
	d, err := parseWithIncludes(src, func(name string) (*Definition, error) {
		return loadDefinition(filepath.Join(filepath.Dir(productionFile(t, "abstraction-config/config.thrift")), name))
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Services[len(d.Services)-1].Methods[0].ObserveNext != "cursor" {
		t.Fatal("actual ConfigObserver mapping missing")
	}
	return d
}

func TestObserveMetadataPreservesPublishedWireShape(t *testing.T) {
	annotated := observedConfig(t)
	plain := *annotated
	plain.Services = append([]Service(nil), annotated.Services...)
	for i := range plain.Services {
		plain.Services[i].Methods = append([]Method(nil), plain.Services[i].Methods...)
		for j := range plain.Services[i].Methods {
			plain.Services[i].Methods[j].Observe = ""
			plain.Services[i].Methods[j].ObserveNext = ""
		}
		if shapeOf(plain.Services[i], &plain).hash() != shapeOf(annotated.Services[i], annotated).hash() {
			t.Fatal("compiler metadata changed published service hash")
		}
	}
}

func TestObserveHelpersUseDeclaredWaitRange(t *testing.T) {
	d := observedConfig(t)
	svc := d.Services[len(d.Services)-1]
	emit := func(lang string, service Service) string {
		var b strings.Builder
		m := service.Methods[0]
		switch lang {
		case "go":
			goObserve(&b, d, service, m)
		case "python":
			pyObserve(&b, d, service, m)
		case "javascript":
			jsObserve(&b, d, service, m)
		case "rust":
			rsObserve(&b, d, service)
		case "cpp":
			cppObserve(&b, d, service)
		}
		return b.String()
	}
	withRange := func(min, max string) Service {
		parsed, err := parse(constraintHead + `struct Page {1:required string next}(document="true",unknown_fields="refuse")
service Watch {Page Observe(1:string cursor,2:i64 wait_ms(range="` + min + `..` + max + `"))(observe="cursor")}(wire_name="test/watch@1")`)
		if err != nil {
			t.Fatal(err)
		}
		_, _, rangedWait := observeFields(parsed, parsed.Services[0].Methods[0])
		copy := svc
		copy.Methods = append([]Method(nil), svc.Methods...)
		copy.Methods[0].Args = append([]Field(nil), svc.Methods[0].Args...)
		for i, f := range copy.Methods[0].Args {
			if f.Name == "wait_ms" {
				copy.Methods[0].Args[i].Checks = rangedWait.Checks
			}
		}
		return copy
	}
	for _, lang := range []string{"go", "python", "javascript", "rust", "cpp"} {
		t.Run(lang, func(t *testing.T) {
			legacy := emit(lang, svc)
			if got := emit(lang, withRange("0", "30000")); got != legacy {
				t.Fatal("current declared range changed legacy helper bytes")
			}
			want := strings.NewReplacer("0..30000", "7..41", "0..=30000", "7..=41", "0 <=", "7 <=", "<= 30000", "<= 41", "< 0n", "< 7n", "> 30000n", "> 41n", "<0||", "<7||", ">30000", ">41").Replace(legacy)
			if got := emit(lang, withRange("7", "41")); got == legacy || got != want {
				t.Fatalf("planted range did not change exactly the helper guard\ngot: %s\nwant: %s", got, want)
			}
			if got := emit(lang, withRange("", "41")); strings.Contains(got, "@min@") || strings.Contains(got, "@max@") || strings.Contains(got, "30000") {
				t.Fatal("open declared range retained legacy bound")
			}
		})
	}
}

func TestObserveIteratorDeclarationNameReservedOnlyWhenUsed(t *testing.T) {
	plain := constraintHead + `struct AsyncIterableIterator {1:required string next}(document="true",unknown_fields="refuse")
service Watch {AsyncIterableIterator Observe(1:string cursor,2:i64 wait_ms(range="0..30000"))}(wire_name="test/watch@1")`
	d, err := parse(plain)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = genJSTypes(d, genJS(d)); err != nil {
		t.Fatal("unannotated declarations changed", err)
	}
	d, err = parse(strings.Replace(plain, ")}(wire_name", `)(observe="cursor")}(wire_name`, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = genJSTypes(d, genJS(d)); err == nil || !strings.Contains(err.Error(), "AsyncIterableIterator") {
		t.Fatal("iterator built-in shadow accepted", err)
	}
}

func TestObserveRustIteratorLocalHygiene(t *testing.T) {
	for _, cursorName := range []string{"page", "result", "_observe_page", "_observe_result", "_observe_invalid", "_observe_closed"} {
		t.Run(cursorName, func(t *testing.T) {
			var arguments []string
			wireName, annotation := cursorName, ""
			if strings.HasPrefix(cursorName, "_") {
				wireName = "cursor"
				annotation = fmt.Sprintf("(rust.name=%q)", cursorName)
			}
			arguments = append(arguments, fmt.Sprintf("1:string %s%s", wireName, annotation), `2:i64 wait_ms(range="0..30000")`)
			values := []string{"String::new()", "42"}
			for _, name := range []string{"_observe_page", "_observe_page_", "_observe_result", "_observe_result_", "_observe_invalid", "_observe_closed"} {
				if name == cursorName {
					continue
				}
				id := len(arguments) + 1
				arguments = append(arguments, fmt.Sprintf("%d:string held%d(rust.name=%q)", id, id, name))
				values = append(values, `"retained".into()`)
			}
			src := constraintHead + fmt.Sprintf(`struct PageReply {1:required string next}(document="true",unknown_fields="refuse")
service Watch {PageReply Observe(%s)(observe=%q)}(wire_name="test/watch@1")`, strings.Join(arguments, ","), wireName)
			d, err := parse(src)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			writeNamespaceFile(t, dir, "rec.rs", genRust(d))
			reply := `{"version":1,"service":"test/watch@1","method":"Observe","ok":true,"payload":{"value":{"next":"one"}}}`
			body := fmt.Sprintf(`mod rec;use rec::*;use std::cell::Cell;
struct Transport{calls:Cell<usize>}
impl FrameTransport for Transport{type Error=&'static str;fn write_frame(&self,_:&[u8])->Result<(),Self::Error>{Ok(())}fn exchange_frame(&self,raw:&[u8])->Result<Vec<u8>,Self::Error>{let n=self.calls.get()+1;self.calls.set(n);let q:String=String::from_utf8_lossy(raw).chars().filter(|c|!c.is_whitespace()).collect();let want=format!("\"%s\":\"{}\"",if n==1{""}else{"one"});assert!(q.contains(&want));Ok(%q.as_bytes().to_vec())}}
fn main(){let c=WatchClient::new(Transport{calls:Cell::new(0)});let mut pages=c.observe_pages(%s);assert_eq!(pages.next().unwrap().unwrap().next,"one");assert_eq!(pages.next().unwrap().unwrap().next,"one");assert_eq!(c.transport().calls.get(),2);}
`, wireName, reply, strings.Join(values, ","))
			writeNamespaceFile(t, dir, "main.rs", body)
			for _, argv := range [][]string{{rustServiceCompiler(t), "--edition=2021", "main.rs", "-o", "probe.exe"}, {filepath.Join(dir, "probe.exe")}} {
				c := exec.Command(argv[0], argv[1:]...)
				c.Dir = dir
				if out, err := c.CombinedOutput(); err != nil {
					t.Fatalf("%v: %v\n%s", argv, err, out)
				}
			}
		})
	}
}

func configReply(outcome string) string {
	page := map[string]any{"outcome": outcome, "cursor": "one"}
	if outcome == "snapshot" {
		origins := map[string]any{}
		for _, key := range []string{"nas_store", "store", "log_sink", "log_service", "off"} {
			origins[key] = map[string]string{"rung": "run", "path": ""}
		}
		page["snapshot"] = map[string]any{"nas_store": "", "store": "retained", "log_sink": "", "log_service": "", "off": map[string]string{}, "origins": origins, "stamp": "one"}
	}
	raw, _ := json.Marshal(map[string]any{"version": 1, "service": "abstraction.config/observer@1", "method": "Observe", "ok": true, "payload": map[string]any{"value": page}})
	return string(raw) + "\n"
}

// Each real generated client explicitly asks again after a disconnect. Its
// continuation remains at the last successfully decoded page; no retry is hidden.
func TestGeneratedConfigObserveIteratorsResumeAfterDisconnect(t *testing.T) {
	d := observedConfig(t)
	// This fixture exercises the published observer alone. The whole current
	// definition also carries imported @2 settings types emitted by its normal
	// multi-schema generation owner.
	var err error
	d, err = selected(d, []string{"ConfigObserver", "RunOverrides", "ConfigObservation", "ConfigObservationOutcome", "Snapshot", "Origins", "Origin"})
	if err != nil {
		t.Fatal(err)
	}
	// Match the normal Go backend's named document codec composition.
	d.NamedCodecs = true
	snapshot, unchanged, gap := configReply("snapshot"), configReply("unchanged"), configReply("gap")
	run := func(t *testing.T, dir string, argv ...string) {
		t.Helper()
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", argv, err, out)
		}
	}
	t.Run("go", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "go.mod", "module observe.test\n\ngo 1.26\n")
		writeNamespaceFile(t, dir, "rec.go", mustEmitIncluded(t, backends[0], d))
		body := fmt.Sprintf(`package rec
import("encoding/json";"errors";"testing")
type observationTransport struct{calls int}
func(f *observationTransport)ExchangeFrame(raw []byte)([]byte,error){f.calls++;var q struct{Arguments struct{Cursor string;WaitMs int64 %s;Overrides RunOverrides}};if err:=json.Unmarshal(raw,&q);err!=nil{panic(err)};want:="one";if f.calls==1{want=""};if q.Arguments.Cursor!=want||q.Arguments.WaitMs!=42||q.Arguments.Overrides.Store!="retained"{panic(string(raw))};if f.calls==2{return nil,errors.New("disconnect")};if f.calls==1{return []byte(%q),nil};if f.calls==4{return []byte(%q),nil};return []byte(%q),nil}
func TestReceive(t *testing.T){f:=&observationTransport{};c:=NewConfigObserverClient(f);pages:=c.ObservePages(RunOverrides{Store:"retained"},"",42);for i:=1;i<=4;i++{got:=0;for page,err:=range pages{got++;if i==2{if err==nil{t.Fatal("disconnect lost")}}else if err!=nil||page.Cursor!="one"{t.Fatal(page,err)};break};if got!=1||f.calls!=i{t.Fatal("hidden retry",got,f.calls)}};for _,err:=range c.ObservePages(RunOverrides{},"",30001){if err==nil{t.Fatal("invalid wait")}};if f.calls!=4{t.Fatal("invalid wait dispatched")}}
`, "`json:\"wait_ms\"`", snapshot, gap, unchanged)
		writeNamespaceFile(t, dir, "rec_test.go", body)
		run(t, dir, "go", "test", ".")
	})
	t.Run("python", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.py", genPy(d))
		body := fmt.Sprintf(`import json
import rec
class Transport:
    calls=0
    def exchange_frame(self, raw):
        self.calls+=1
        q=json.loads(raw)["arguments"]
        assert q["cursor"] == ("" if self.calls==1 else "one") and q["wait_ms"]==42 and q["overrides"]["store"]=="retained"
        if self.calls==2: raise OSError("disconnect")
        return (%q if self.calls==1 else %q if self.calls==4 else %q).encode()
f=Transport(); c=rec.ConfigObserverClient(f); pages=c.observe_pages(rec.RunOverrides(store="retained"),"",42)
assert iter(pages) is pages and next(pages).cursor=="one"
try: next(pages); assert False
except OSError: pass
assert f.calls==2 and next(pages).cursor=="one" and next(pages).outcome==rec.ConfigObservationOutcome.GAP and f.calls==4
try: c.observe_pages(rec.RunOverrides(),"",30001); assert False
except ValueError: pass
assert f.calls==4
`, snapshot, gap, unchanged)
		writeNamespaceFile(t, dir, "test.py", body)
		python := os.Getenv("PYTHON")
		if python == "" {
			t.Fatal("PYTHON missing")
		}
		run(t, dir, python, "test.py")
	})
	t.Run("javascript", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.mjs", genJS(d))
		body := fmt.Sprintf(`import assert from 'node:assert/strict';import * as r from './rec.mjs';
let calls=0;const c=new r.ConfigObserverClient({exchangeFrame:async raw=>{calls++;const q=JSON.parse(new TextDecoder().decode(raw)).arguments;assert.equal(q.cursor,calls===1?'':'one');assert.equal(q.wait_ms,42);assert.equal(q.overrides.store,'retained');if(calls===2)throw Error('disconnect');return new TextEncoder().encode(calls===1?%q:calls===4?%q:%q);}});
const pages=c.observePages({...r.newRunOverrides(),store:'retained'},'',42n);assert.equal(pages[Symbol.asyncIterator](),pages);assert.equal((await pages.next()).value.cursor,'one');await assert.rejects(pages.next(),/disconnect/);assert.equal(calls,2);const [a,b]=await Promise.all([pages.next(),pages.next()]);assert.equal(a.value.cursor,'one');assert.equal(b.value.outcome,'gap');assert.equal(calls,4);await pages.return();assert.equal((await pages.next()).done,true);assert.throws(()=>c.observePages(r.newRunOverrides(),'',30001n),RangeError);assert.equal(calls,4);
`, snapshot, gap, unchanged)
		writeNamespaceFile(t, dir, "test.mjs", body)
		run(t, dir, "node", "test.mjs")
	})
	t.Run("cpp", func(t *testing.T) {
		dir := t.TempDir()
		writeNamespaceFile(t, dir, "rec.h", genCpp(d))
		body := fmt.Sprintf(`#include "rec.h"
#include <cassert>
namespace r=rec;
struct Transport{int calls=0;std::string exchange_frame(std::string_view raw){++calls;std::string q;for(char c:raw)if(c!=' '&&c!='\n'&&c!='\r'&&c!='\t')q+=c;assert(q.find(calls==1?"\"cursor\":\"\"":"\"cursor\":\"one\"")!=std::string::npos);assert(q.find("\"wait_ms\":42")!=std::string::npos);assert(q.find("\"store\":\"retained\"")!=std::string::npos);if(calls==2)throw std::runtime_error("disconnect");if(calls==1)return R"oa(%s)oa";if(calls==4)return R"oa(%s)oa";return R"oa(%s)oa";}};
int main(){Transport f;r::ConfigObserverClient<Transport> c(f);r::RunOverrides o;o.store="retained";auto pages=c.observe_pages(o,"",42);auto it=pages.begin();assert((*it).cursor=="one");try{++it;assert(false);}catch(const std::runtime_error&){}assert(f.calls==2);it=pages.begin();assert((*it).cursor=="one");++it;assert(f.calls==4);it.close();assert(!(it!=pages.end()));try{c.observe_pages(o,"",30001);assert(false);}catch(const std::invalid_argument&){}assert(f.calls==4);}
`, snapshot, gap, unchanged)
		writeNamespaceFile(t, dir, "test.cpp", body)
		args := []string{"g++", "-std=c++17", "test.cpp", "-o", "probe.exe"}
		run(t, dir, args...)
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
	t.Run("rust", func(t *testing.T) {
		dir := t.TempDir()
		copy := *d
		copy.SharedRustTransport = false
		writeNamespaceFile(t, dir, "rec.rs", genRust(&copy))
		body := fmt.Sprintf(`mod rec;use rec::*;use std::cell::Cell;
struct Transport{calls:Cell<usize>}
impl FrameTransport for Transport{type Error=&'static str;fn write_frame(&self,_:&[u8])->Result<(),Self::Error>{Ok(())}fn exchange_frame(&self,raw:&[u8])->Result<Vec<u8>,Self::Error>{let n=self.calls.get()+1;self.calls.set(n);let q:String=String::from_utf8_lossy(raw).chars().filter(|c|!c.is_whitespace()).collect();assert!(q.contains(if n==1{"\"cursor\":\"\""}else{"\"cursor\":\"one\""}));assert!(q.contains("\"wait_ms\":42"));assert!(q.contains("\"store\":\"retained\""));if n==2{return Err("disconnect")};Ok(if n==1{%q}else if n==4{%q}else{%q}.as_bytes().to_vec())}}
fn main(){let c=ConfigObserverClient::new(Transport{calls:Cell::new(0)});let o=RunOverrides{nas_store:String::new(),store:"retained".into(),log_sink:String::new(),log_service:String::new()};let mut pages=c.observe_pages(o.clone(),String::new(),42);assert_eq!(pages.next().unwrap().unwrap().cursor,"one");assert!(pages.next().unwrap().is_err());assert_eq!(c.transport().calls.get(),2);assert_eq!(pages.next().unwrap().unwrap().cursor,"one");assert_eq!(pages.next().unwrap().unwrap().cursor,"one");assert_eq!(c.transport().calls.get(),4);assert!(c.observe_pages(o,String::new(),30001).next().unwrap().is_err());assert_eq!(c.transport().calls.get(),4);}
`, snapshot, gap, unchanged)
		writeNamespaceFile(t, dir, "main.rs", body)
		run(t, dir, rustServiceCompiler(t), "--edition=2021", "main.rs", "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
}

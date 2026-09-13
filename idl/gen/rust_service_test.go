package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func rustServiceCompiler(t *testing.T) string {
	t.Helper()
	p, e := exec.LookPath("rustc")
	if e != nil {
		t.Skip("rustc unavailable")
	}
	return p
}
func TestRustGoServiceExchange(t *testing.T) {
	rust := rustServiceCompiler(t)
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	host := strings.Replace(replyExchangeGo, "v,e:=d.ExchangeFrame(b)", `if os.Args[1]=="oneway"{if e=d.WriteFrame(b);e!=nil{panic(e)};return};v,e:=d.ExchangeFrame(b)`, 1)
	for n, b := range map[string]string{"go.mod": "module exchange.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.rs": genRust(s), "main.go": host, "main.rs": rustExchangeTest} {
		writeNamespaceFile(t, dir, n, b)
	}
	for _, command := range [][]string{{"go", "build", "-o", "host.exe", "main.go"}, {rust, "--edition=2021", "main.rs", "-o", "probe.exe"}, {filepath.Join(dir, "probe.exe"), filepath.Join(dir, "host.exe")}} {
		c := exec.Command(command[0], command[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", command, e, out)
		}
	}
	// Control: accepting a reply for another method must make the executable fail.
	body := genRust(s)
	body = strings.ReplaceAll(body, `if reply.service != "example.query/query@1" || reply.method != "Reset"`, "if false")
	writeNamespaceFile(t, dir, "rec.rs", body)
	c := exec.Command(rust, "--edition=2021", "main.rs", "-o", "mutated.exe")
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("mutation compile: %v\n%s", e, out)
	}
	c = exec.Command(filepath.Join(dir, "mutated.exe"), filepath.Join(dir, "host.exe"))
	c.Dir = dir
	if out, e := c.CombinedOutput(); e == nil {
		t.Fatalf("missing reply identity guard passed: %s", out)
	}
}

const rustExchangeTest = `mod rec;
use rec::{Query,FrameTransport,CallError};
use std::{cell::{Cell,RefCell},io::Write,process::{Command,Stdio}};
struct Transport { host:String, calls:Cell<usize>, reply:RefCell<Option<Vec<u8>>>, fail:Cell<bool> }
impl Transport {
 fn run(&self,mode:&str,frame:&[u8])->Vec<u8>{
  let mut p=Command::new(&self.host).arg(mode).stdin(Stdio::piped()).stdout(Stdio::piped()).spawn().unwrap();
  p.stdin.take().unwrap().write_all(frame).unwrap(); let out=p.wait_with_output().unwrap();assert!(out.status.success());out.stdout
 }
}
impl FrameTransport for Transport {
 type Error=u32;
 fn write_frame(&self,frame:&[u8])->Result<(),u32>{self.calls.set(self.calls.get()+1);if self.fail.get(){return Err(7)}self.run("oneway",frame);Ok(())}
 fn exchange_frame(&self,frame:&[u8])->Result<Vec<u8>,u32>{self.calls.set(self.calls.get()+1);if self.fail.get(){return Err(7)}if let Some(v)=self.reply.borrow().as_ref(){return Ok(v.clone())}Ok(self.run("serve",frame))}
}
fn main(){
 let c=rec::QueryClient::new(Transport{host:std::env::args().nth(1).unwrap(),calls:Cell::new(0),reply:RefCell::new(None),fail:Cell::new(false)});
 let v=c.Echo(rec::Record{value:"雪<&".into()},"".into(),false,0).unwrap();assert_eq!(v.value,"雪<&");
 let raw=b"{ \"a\" : [1,\n false] }".to_vec();assert_eq!(c.Opaque(raw.clone()).unwrap(),raw);
 c.Reset().unwrap();c.Notify().unwrap();
 match c.Fail("future_code".into()){Err(CallError::Service(e))=>assert_eq!(e.code,"future_code"),_=>panic!("service error lost")}
 let before=c.transport().calls.get();assert!(matches!(c.Opaque(b"[".to_vec()),Err(CallError::Refusal(_))));assert_eq!(before,c.transport().calls.get());
 for reply in [
 r#"{"version":2,"service":"example.query/query@1","method":"Reset","ok":true,"payload":{}}"#,
 r#"{"version":1,"service":"wrong","method":"Reset","ok":true,"payload":{}}"#,
 r#"{"version":1,"service":"example.query/query@1","method":"wrong","ok":true,"payload":{}}"#,
 r#"{"version":1,"service":"example.query/query@1","method":"Reset","ok":true,"payload":{"extra":0}}"#,
 r#"{"version":1,"service":"example.query/query@1","method":"Reset","ok":false,"payload":{}}"#,
 r#"{"version":1,"service":"example.query/query@1","method":"Reset","ok":false,"payload":{"code":"","message":""}}"#,
 r#"{"version":1,"version":1,"service":"example.query/query@1","method":"Reset","ok":true,"payload":{}}"#,
 r#"{"version":1,"service":"example.query/query@1","method":"Reset","ok":true,"payload":{}} null"#
 ]{*c.transport().reply.borrow_mut()=Some(reply.as_bytes().to_vec());assert!(c.Reset().is_err(),"accepted {}",reply);}
 *c.transport().reply.borrow_mut()=None;c.transport().fail.set(true);let before=c.transport().calls.get();
 assert!(matches!(c.Notify(),Err(CallError::Transport(7))));assert!(matches!(c.Reset(),Err(CallError::Transport(7))));assert_eq!(c.transport().calls.get(),before+2);
 c.transport().fail.set(false);c.Reset().unwrap();
}
`

func TestRustServiceProductionAndNoIPC(t *testing.T) {
	rust := rustServiceCompiler(t)
	for _, name := range []string{"logging", "facade"} {
		s, e := loadDefinition(productionFile(t, "abstraction-"+name+"/"+name+".thrift"))
		if e != nil {
			t.Fatal(e)
		}
		if e = validateServiceBackend(s, "rust"); e != nil {
			t.Fatal(e)
		}
		for _, noIPC := range []bool{false, true} {
			s.NoIPC = noIPC
			body := genRust(s)
			if noIPC && strings.Contains(body, "OAServiceFrame") {
				t.Fatal("interface retained framing")
			}
			dir := t.TempDir()
			writeNamespaceFile(t, dir, "rec.rs", body)
			c := exec.Command(rust, "--edition=2021", "--crate-type", "lib", "rec.rs", "-o", "rec.rlib")
			c.Dir = dir
			if out, e := c.CombinedOutput(); e != nil {
				t.Fatalf("%s noIPC=%v: %v\n%s", name, noIPC, e, out)
			}
		}
	}
}

func TestRustServiceValidation(t *testing.T) {
	for _, name := range []string{"FrameTransport", "CallError", "ServiceError"} {
		s, e := parse(head + strings.Replace(replyFixture, "Query {", name+" {", 1))
		if e != nil {
			if strings.Contains(e.Error(), "collision") {
				continue
			}
			t.Fatal(e)
		}
		if validateServiceBackend(s, "rust") == nil {
			t.Fatal("accepted collision", name)
		}
	}
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	record, e := selected(s, []string{"Record"})
	if e != nil {
		t.Fatal(e)
	}
	plain, e := parse(head + strings.Split(replyFixture, "service Query")[0])
	if e != nil {
		t.Fatal(e)
	}
	if genRust(record) != genRust(plain) {
		t.Fatal("record-only bytes changed")
	}
}

func TestRustProductionServiceExchange(t *testing.T) {
	rust := rustServiceCompiler(t)
	dir := t.TempDir()
	for _, name := range []string{"logging", "facade"} {
		s, e := loadDefinition(productionFile(t, "abstraction-"+name+"/"+name+".thrift"))
		if e != nil {
			t.Fatal(e)
		}
		writeNamespaceFile(t, dir, name+".rs", genRust(s))
		writeNamespaceFile(t, dir, name+"/rec.go", genGo(s))
	}
	writeNamespaceFile(t, dir, "go.mod", "module production.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "main.go", rustProductionHost)
	writeNamespaceFile(t, dir, "main.rs", rustProductionProbe)
	for _, command := range [][]string{{"go", "build", "-o", "host.exe", "main.go"}, {rust, "--edition=2021", "main.rs", "-o", "probe.exe"}, {filepath.Join(dir, "probe.exe"), filepath.Join(dir, "host.exe")}} {
		c := exec.Command(command[0], command[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", command, e, out)
		}
	}
}

const rustProductionHost = `package main
import("os";"io";l "production.test/logging";f "production.test/facade")
type sink struct{}
func(sink)Write(v l.Record)error{if v.Msg!="rust-log"||v.Schema!=1{panic("invalid record")};return nil}
type history struct{}
func(history)Read(cursor string,n,b int64)(l.Page,error){if cursor!="cursor"||n!=1||b!=1000{panic("invalid history args")};return l.Page{Outcome:"page",Records:[]l.Record{{Schema:1,Time:"2026-09-13T00:00:00.000000Z",Msg:"retained"}},Next:"next",AtEnd:true},nil}
type resolver struct{}
func(resolver)Resolve(v f.ResolveRequest)(f.ResolveResult,error){if v.Capability!="abstraction.logging"||v.Scope!="local"||len(v.Contracts)!=1||v.Contracts[0]!="abstraction.logging/sink@1"{panic("invalid request")};return f.ResolveResult{Status:"unavailable"},nil}
func main(){b,e:=io.ReadAll(os.Stdin);if e!=nil{panic(e)};var out []byte;switch os.Args[1]{case "sink":d:=l.SinkDispatcher{Handler:sink{}};e=d.WriteFrame(b);case "history":d:=l.HistoryReaderDispatcher{Handler:history{}};out,e=d.ExchangeFrame(b);case "resolver":d:=f.ResolverDispatcher{Handler:resolver{}};out,e=d.ExchangeFrame(b)};if e!=nil{panic(e)};os.Stdout.Write(out)}
`
const rustProductionProbe = `mod logging;mod facade;
use logging::{Sink,HistoryReader};use facade::Resolver;
use std::{cell::Cell,io::Write,process::{Command,Stdio}};
struct Transport{host:String,mode:&'static str,calls:Cell<usize>}
impl Transport{fn run(&self,b:&[u8])->Vec<u8>{self.calls.set(self.calls.get()+1);let mut p=Command::new(&self.host).arg(self.mode).stdin(Stdio::piped()).stdout(Stdio::piped()).spawn().unwrap();p.stdin.take().unwrap().write_all(b).unwrap();let o=p.wait_with_output().unwrap();assert!(o.status.success());o.stdout}}
impl logging::FrameTransport for Transport{type Error=();fn write_frame(&self,b:&[u8])->Result<(),()>{self.run(b);Ok(())}fn exchange_frame(&self,b:&[u8])->Result<Vec<u8>,()>{Ok(self.run(b))}}
impl facade::FrameTransport for Transport{type Error=();fn write_frame(&self,b:&[u8])->Result<(),()>{self.run(b);Ok(())}fn exchange_frame(&self,b:&[u8])->Result<Vec<u8>,()>{Ok(self.run(b))}}
fn transport(mode:&'static str)->Transport{Transport{host:std::env::args().nth(1).unwrap(),mode,calls:Cell::new(0)}}
fn main(){
 let sink=logging::SinkClient::new(transport("sink"));let mut r=logging::Record::default();r.schema=1;r.time="2026-09-13T00:00:00.000000Z".into();r.msg="rust-log".into();sink.Write(r).unwrap();
 let page=logging::HistoryReaderClient::new(transport("history")).Read("cursor".into(),1,1000).unwrap();assert_eq!(page.outcome,"page");assert_eq!(page.records[0].msg,"retained");assert!(page.at_end);
 let resolver=facade::ResolverClient::new(transport("resolver"));let mut r=facade::ResolveRequest::default();r.capability="abstraction.logging".into();r.scope="local".into();r.contracts=vec!["abstraction.logging/sink@1".into()];assert_eq!(resolver.Resolve(r).unwrap().status,"unavailable");
 let n=resolver.transport().calls.get();let mut bad=facade::ResolveRequest::default();bad.scope="invalid".into();assert!(matches!(resolver.Resolve(bad),Err(facade::CallError::Refusal(_))));assert_eq!(n,resolver.transport().calls.get());
}
`

func TestSharedRustTransportMode(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	ordinary := genRust(s)
	s.SharedRustTransport = true
	if !strings.Contains(genRust(s), "pub use abstraction_frame::FrameTransport;") {
		t.Fatal("shared mode did not reuse contract")
	}
	s.NoIPC = true
	shared := genRust(s)
	s.SharedRustTransport = false
	if shared != genRust(s) || strings.Contains(shared, "abstraction_frame") {
		t.Fatal("no-ipc acquired runtime dependency")
	}
	s.NoIPC = false
	if ordinary != genRust(s) {
		t.Fatal("standalone output changed")
	}
	s.SharedRustTransport = true
	record, e := selected(s, []string{"Record"})
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(genRust(record), "abstraction_frame") {
		t.Fatal("record selection acquired transport")
	}
	dir := t.TempDir()
	src, e := filepath.Abs(productionFile(t, "abstraction-logging/logging.thrift"))
	if e != nil {
		t.Fatal(e)
	}
	var report bytes.Buffer
	if e = run([]string{src, filepath.Join(dir, "out"), "rust", "--shared-rust-transport"}, &report); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join(dir, "out", "rs", "abstraction", "logging", "rec.rs"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(raw), "pub use abstraction_frame::FrameTransport;") {
		t.Fatal("CLI mode missing")
	}
	if e = run([]string{src, filepath.Join(dir, "out"), "rust", "--shared-rust-transport", "--shared-rust-transport"}, &report); e == nil {
		t.Fatal("duplicate mode accepted")
	}
}

func TestSharedRustTransportProductionExchange(t *testing.T) {
	rust := rustServiceCompiler(t)
	dir := t.TempDir()
	for _, name := range []string{"logging", "facade"} {
		s, e := loadDefinition(productionFile(t, "abstraction-"+name+"/"+name+".thrift"))
		if e != nil {
			t.Fatal(e)
		}
		s.SharedRustTransport = true
		writeNamespaceFile(t, dir, name+".rs", genRust(s))
		writeNamespaceFile(t, dir, name+"/rec.go", genGo(s))
	}
	core, e := filepath.Abs(productionFile(t, "abstraction-identity/rust-frame/src/lib.rs"))
	if e != nil {
		t.Fatal(e)
	}
	writeNamespaceFile(t, dir, "go.mod", "module production.test\n\ngo 1.22\n")
	writeNamespaceFile(t, dir, "main.go", rustProductionHost)
	probe := strings.Replace(rustProductionProbe, `impl logging::FrameTransport for Transport`, `impl abstraction_frame::FrameTransport for Transport`, 1)
	line := `impl facade::FrameTransport for Transport{type Error=();fn write_frame(&self,b:&[u8])->Result<(),()>{self.run(b);Ok(())}fn exchange_frame(&self,b:&[u8])->Result<Vec<u8>,()>{Ok(self.run(b))}}`
	if !strings.Contains(probe, line) {
		t.Fatal("duplicate adapter control missing")
	}
	probe = strings.Replace(probe, line, "", 1)
	writeNamespaceFile(t, dir, "main.rs", probe)
	commands := [][]string{{rust, "--edition=2021", "--crate-name", "abstraction_frame", "--crate-type=lib", core, "-o", "libabstraction_frame.rlib"}, {"go", "build", "-o", "host.exe", "main.go"}, {rust, "--edition=2021", "main.rs", "--extern", "abstraction_frame=libabstraction_frame.rlib", "-o", "probe.exe"}, {filepath.Join(dir, "probe.exe"), filepath.Join(dir, "host.exe")}}
	for _, args := range commands {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off", "OA_IPC_PREFIX=")
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", args, e, out)
		}
	}
}

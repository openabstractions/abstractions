package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type resourceResultCase struct{ service, method, reply, want string }

// This corpus comes from the actual Resource methods/enum vocabularies. Every
// public generated language client receives precisely the same result bytes.
func resourceResultCases(t *testing.T, d *Definition) []resourceResultCase {
	t.Helper()
	at := `"2026-10-27T00:00:00.000000Z"`
	payload := map[string]string{
		"state":  `{"resource":"memory","capacity":0,"held":0,"holders":[],"observed":` + at + `,"instrument":"none"}`,
		"list":   `{"resources":[],"observed":` + at + `}`,
		"lease":  `{"id":"one","resource":"memory","amount":0,"since":` + at + `,"renew_by":` + at + `}`,
		"change": `{"applied":false}`, "page": `{"requests":[],"next":""}`, "answer": `{"recorded":false}`,
	}
	var out []resourceResultCase
	for _, svc := range d.Services {
		for _, m := range svc.Methods {
			p := m.ResultPostcondition
			if p == nil {
				continue
			}
			appendCase := func(outcome string, present bool, data, want string) {
				record := fmt.Sprintf(`{"outcome":%q`, outcome)
				if present {
					record += fmt.Sprintf(`,%q:%s`, p.Payload.Name, data)
				}
				if m.Name == "AcquireFor" || m.Name == "Acquire" || m.Name == "AcquireFree" {
					record += `,"asked":[]`
				}
				record += `}`
				reply := fmt.Sprintf(`{"version":1,"service":%q,"method":%q,"ok":true,"payload":{"value":%s}}`, svc.WireName, m.Name, record)
				out = append(out, resourceResultCase{svc.Name, m.Name, reply, want})
			}
			for _, member := range d.Enum(p.Discriminator.Type).Members {
				word := member.WireName()
				success := word == p.Members[0]
				if success {
					appendCase(word, true, payload[p.Payload.Name], "")
					appendCase(word, false, "", "invalid_result")
				} else {
					appendCase(word, false, "", "")
					appendCase(word, true, payload[p.Payload.Name], "invalid_result")
				}
			}
			appendCase(p.Members[0], true, "null", "wrong_type")
			appendCase("not-a-declared-outcome", false, "", "bad_enum")
		}
	}
	if len(out) != 132 {
		t.Fatalf("Resource corpus changed: %d cases, review full enum/method coverage", len(out))
	}
	return out
}

func TestResourceResultPostconditionPublicClients(t *testing.T) {
	path := productionFile(t, "abstraction-resource/resource.thrift")
	d, err := loadDefinition(path)
	if err != nil {
		t.Fatal(err)
	}
	cases := resourceResultCases(t, d)
	out := t.TempDir()
	for _, source := range []string{"abstraction-rights/rights.thrift", "abstraction-resource/resource.thrift"} {
		args := []string{productionFile(t, source), out, "--named-codecs", "go", "cpp", "python", "rust", "javascript"}
		if strings.Contains(source, "abstraction-resource/") {
			args = append(args, "--go-import=rights=resource.post.test/abstraction/rights/api", "--rust-import=rights=rights_api", "--js-import=rights=../rights/api/index.mjs")
		}
		var report bytes.Buffer
		if err := run(args, &report); err != nil {
			t.Fatal(source, err, report.String())
		}
	}
	run := func(t *testing.T, dir string, args ...string) {
		t.Helper()
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "PYTHONPATH="+filepath.Join(out, "py"))
		if out, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", args, e, out)
		}
	}
	calls := map[string]string{"Resources": "", "Holders": `"memory",false`, "Acquire": `"memory",0,0`, "AcquireFree": `"memory",0`, "Renew": `"one"`, "Release": `"one"`, "Label": `"one",""`, "Observe": `"",0`, "Answer": `"one",YieldAnswerRefused,""`}
	t.Run("go", func(t *testing.T) {
		dir := filepath.Join(out, "go")
		writeNamespaceFile(t, dir, "go.mod", "module resource.post.test\n\ngo 1.26\n")
		var b strings.Builder
		b.WriteString(`package resource
import("testing";"errors";rights "resource.post.test/abstraction/rights/api")
type replyTransport string;func(f replyTransport)ExchangeFrame([]byte)([]byte,error){return []byte(f),nil}
func TestActualResourceResults(t *testing.T){
`)
		for i, c := range cases {
			args := calls[c.method]
			if c.method == "AcquireFor" {
				args = `rights.Subject{Account:"fixture-account",Program:"/fixture-program"},"memory",0,0`
			}
			fmt.Fprintf(&b, "{f:=replyTransport(%q);_,err:=New%sClient(f).%s(%s);got:=\"\";if err!=nil{var s *ServiceError;var r *Refusal;if errors.As(err,&s){got=string(s.Code)}else if errors.As(err,&r){got=r.Word}else{t.Fatal(err)}};if got!=%q{t.Fatalf(\"case %d %s: %%s\",got)}}\n", c.reply, c.service, c.method, args, c.want, i, c.method)
		}
		b.WriteString("}\n")
		writeNamespaceFile(t, dir, "abstraction/resource/rec_test.go", b.String())
		run(t, dir, "go", "test", "./abstraction/resource")
	})
	t.Run("python", func(t *testing.T) {
		dir := out
		var b strings.Builder
		b.WriteString("import abstraction.resource as rec\nfrom abstraction.rights import api as rights\nclass Transport:\n    def __init__(self,raw): self.raw=raw\n    def exchange_frame(self,_): return self.raw.encode()\n")
		for i, c := range cases {
			args := strings.ReplaceAll(calls[c.method], "false", "False")
			args = strings.ReplaceAll(args, "YieldAnswerRefused", "rec.YieldAnswer.REFUSED")
			if c.method == "AcquireFor" {
				args = `rights.Subject(account="fixture-account",program="/fixture-program"),"memory",0,0`
			}
			fmt.Fprintf(&b, "got=''\ntry: rec.%sClient(Transport(%q)).%s(%s)\nexcept rec.ServiceError as e: got=e.code\nexcept rec.Refusal as e: got=e.word\nassert got==%q, (%d,got)\n", c.service, c.reply, pyKeywordSafe(snakeCase(c.method)), args, c.want, i)
		}
		writeNamespaceFile(t, dir, "test.py", b.String())
		python := os.Getenv("PYTHON")
		if python == "" {
			t.Fatal("PYTHON missing")
		}
		run(t, dir, python, "test.py")
	})
	t.Run("javascript", func(t *testing.T) {
		dir := out
		var b strings.Builder
		b.WriteString("import assert from 'node:assert/strict';import * as r from './js/abstraction/resource/index.mjs';import * as rights from './js/abstraction/rights/api/index.mjs';\n")
		for i, c := range cases {
			args := strings.ReplaceAll(calls[c.method], "YieldAnswerRefused", "r.YieldAnswer.Refused") // integer arguments are JS bigint
			if c.method == "Acquire" {
				args = `"memory",0n,0n`
			}
			if c.method == "AcquireFor" {
				args = `Object.assign(rights.newSubject(),{account:"fixture-account",program:"/fixture-program"}),"memory",0n,0n`
			}
			if c.method == "AcquireFree" {
				args = `"memory",0n`
			}
			if c.method == "Observe" {
				args = `"",0n`
			}
			fmt.Fprintf(&b, "{let got='';try{await new r.%sClient({exchangeFrame:async()=>new TextEncoder().encode(%q)}).%s(%s);}catch(e){if(e instanceof r.ServiceError)got=e.code;else if(e instanceof r.Refusal)got=e.word;else throw e;}assert.equal(got,%q,'case %d');}\n", c.service, c.reply, camelCase(c.method), args, c.want, i)
		}
		writeNamespaceFile(t, dir, "test.mjs", b.String())
		run(t, dir, "node", "test.mjs")
	})
	t.Run("cpp", func(t *testing.T) {
		dir := out
		var b strings.Builder
		b.WriteString("#include <abstraction/resource/rec.h>\n#include <cassert>\nnamespace r=abstraction::resource;namespace rights=abstraction::rights::api;struct Transport{std::string raw;std::string exchange_frame(std::string_view){return raw;}};int main(){\n")
		for _, c := range cases {
			args := strings.ReplaceAll(calls[c.method], "YieldAnswerRefused", "r::YieldAnswer::Refused")
			if c.method == "AcquireFor" {
				args = `rights::Subject{"fixture-account","/fixture-program"},"memory",0,0`
			}
			fmt.Fprintf(&b, "{Transport f{%q};std::string got;try{r::%sClient<Transport>(f).%s(%s);}catch(const r::ServiceError&e){got=e.code;}catch(const r::Refusal&e){got=e.word;}assert(got==%q);}\n", c.reply, c.service, cppMethod(Method{Name: c.method}), args, c.want)
		}
		b.WriteString("}\n")
		writeNamespaceFile(t, dir, "test.cpp", b.String())
		run(t, dir, "g++", "-std=c++17", "-I", filepath.Join(out, "cpp"), "test.cpp", "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
	t.Run("rust", func(t *testing.T) {
		dir := out
		var b strings.Builder
		b.WriteString(`use resource_api::*;struct Transport{raw:&'static str}impl FrameTransport for Transport{type Error=&'static str;fn write_frame(&self,_:&[u8])->Result<(),Self::Error>{Ok(())}fn exchange_frame(&self,_:&[u8])->Result<Vec<u8>,Self::Error>{Ok(self.raw.as_bytes().to_vec())}}fn main(){
`)
		for i, c := range cases {
			args := strings.ReplaceAll(calls[c.method], "YieldAnswerRefused", "YieldAnswer::Refused")
			if c.method == "AcquireFor" {
				args = `rights_api::Subject{account:"fixture-account".into(),program:"/fixture-program".into()},"memory",0,0`
			}
			for _, text := range []string{`"memory"`, `"one"`, `""`} {
				args = strings.ReplaceAll(args, text, text+".into()")
			}
			fmt.Fprintf(&b, "{let got=match %sClient::new(Transport{raw:%q}).%s(%s){Ok(_)=>String::new(),Err(CallError::Service(e))=>e.code,Err(CallError::Refusal(e))=>e.word.into(),_=>panic!(\"unexpected result\")};assert_eq!(got,%q,\"case %d\");}\n", c.service, c.reply, rustIdent(c.method), args, c.want, i)
		}
		b.WriteString("}\n")
		writeNamespaceFile(t, dir, "main.rs", b.String())
		compiler := rustServiceCompiler(t)
		run(t, dir, compiler, "--edition=2021", "--crate-type=rlib", "--crate-name", "rights_api", filepath.Join(out, "rs/abstraction/rights/api/rec.rs"), "-o", "librights_api.rlib")
		run(t, dir, compiler, "--edition=2021", "--crate-type=rlib", "--crate-name", "resource_api", filepath.Join(out, "rs/abstraction/resource/rec.rs"), "--extern", "rights_api=librights_api.rlib", "-o", "libresource_api.rlib")
		run(t, dir, compiler, "--edition=2021", "main.rs", "--extern", "resource_api=libresource_api.rlib", "--extern", "rights_api=librights_api.rlib", "-L", out, "-o", "probe.exe")
		run(t, dir, filepath.Join(dir, "probe.exe"))
	})
}

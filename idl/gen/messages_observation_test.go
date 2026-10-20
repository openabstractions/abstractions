package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func messagesOutputObservation(name string) bool {
	switch name {
	case "Page", "InboxPage", "Inbox", "Header", "Message", "RemoteOrigin", "InboxChange", "ChatInboxChange", "SendResult", "StopResult", "ObserveDeliveryResult", "TurnWakeResult", "AgentApplication", "ProgramChats", "RefusalDetail":
		return true
	}
	return false
}

func TestMessagesChatLaunchLocalPathMetadata(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-facade", "messages.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	marker := `cwd(length="1..4096",pattern="no_control",local="path")`
	if strings.Count(source, marker) != 1 {
		t.Fatal("host cwd local-path marker missing")
	}
	after := mustParse(t, messagesObservationDefinition(t, source))
	for _, name := range []string{"ChatOrigin", "ChatMode"} {
		if e := after.Enum(name); e.Ann["unknown"] != "grant" || e.Ann["reader"] != "validate" {
			t.Fatal(name, e.Ann)
		}
	}
	before := mustParse(t, messagesObservationDefinition(t, strings.Replace(source, marker, `cwd(length="1..4096",pattern="no_control")`, 1)))
	if !reflect.DeepEqual(definitionHashes(before), definitionHashes(after)) || len(compatibility(before, after)) != 0 {
		t.Fatal("local marker changed wire shape")
	}
	before.NoIPC, after.NoIPC = true, true
	before.NamedCodecs, after.NamedCodecs = true, true
	for _, backend := range backends {
		if backend.emitsCode && mustEmitIncluded(t, backend, before) != mustEmitIncluded(t, backend, after) {
			t.Fatal(backend.lang, "local marker changed codec")
		}
	}
}

// Every output record has its own additive boundary. Nested claims and locators
// retain their closed input language even when their enclosing output is open.
func TestMessagesOutputOnlyReadersFiveLanguages(t *testing.T) {
	body, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-facade", "messages.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	d := mustParse(t, messagesObservationDefinition(t, string(body)))
	// The same temporary five-language corpus carries the exact Machines
	// record. Its C++/Python fixtures create no new shipped language artifact.
	machines, err := loadDefinition(productionFile(t, "abstraction-facade/machines.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	d.Structs = append(d.Structs, *machines.Struct("MachineRecord"))
	d.Namespaces, d.Services = map[string]string{"*": "rec"}, nil
	d.NoIPC, d.NamedCodecs = true, true
	header := `{"message_id":"m","from":"app:s","from_context":"s","to":"app:r","context":"r","kind":"note","subject":"","context_id":"c","expires_unix_ms":1,"refused":false}`
	claim := `{"namespace":"` + strings.Repeat("a", 64) + `","application":"sender","program_label":"Sender"}`
	ref := `{"owner_peer_key":"` + strings.Repeat("a", 64) + `","message_id":"m"}`
	remote := `{"peer_key":"` + strings.Repeat("a", 64) + `","claim":` + claim + `}`
	bases := []struct{ name, raw string }{
		{"Header", header}, {"Message", `{"header":` + header + `,"idempotency_key":"k","body":"body"}`},
		{"RemoteOrigin", remote}, {"InboxChange", `{"outcome":"opened","revision":"r","holder":"app:r","opened_unix_ms":1}`},
		{"ChatInboxChange", `{"outcome":"opened","context":"c","revision":"r","holder":"app:r","opened_unix_ms":1}`},
		{"SendResult", `{"outcome":"sent","message_id":"m","context_id":"c","receiver_watching":false,"awaiting_permission":false,"retry_after_ms":0}`},
		{"StopResult", `{"outcome":"page"}`},
		{"ObserveDeliveryResult", `{"outcome":"observed","delivery":"delivered"}`},
	}
	var lines, wants []string
	add := func(name, raw, want string) { lines = append(lines, name+"\t"+raw); wants = append(wants, want) }
	for _, b := range bases {
		add(b.name, strings.TrimSuffix(b.raw, "}")+`,"future":{"nested":[true,null,1]}}`, "accept")
		add(b.name, strings.TrimSuffix(b.raw, "}")+`,"future":1,"\u0066uture":2}`, "duplicate_key")
		add(b.name, strings.TrimSuffix(b.raw, "}")+`,"future":{"x":1,"x":2}}`, "duplicate_key")
		add(b.name, strings.TrimSuffix(b.raw, "}")+`,"future":`+strings.Repeat("[", 70)+strings.Repeat("]", 70)+`}`, "depth_exceeded")
	}
	add("Header", strings.Replace(header, `"kind":"note"`, `"kind":"future"`, 1), "bad_enum")
	add("Header", strings.Replace(header, `"refused":false`, `"refused":"false"`, 1), "wrong_type")
	add("Header", strings.Replace(header, `"message_id":"m",`, "", 1), "missing_field")
	add("Header", strings.Replace(header, `"message_id":"m"`, `"message_id":"`+strings.Repeat("x", 129)+`"`, 1), "bad_value")
	add("Header", strings.TrimSuffix(header, "}")+`,"reference":`+strings.TrimSuffix(ref, "}")+`,"future":true}}`, "unknown_field")
	add("Header", strings.TrimSuffix(header, "}")+`,"remote_origin":`+strings.TrimSuffix(remote, "}")+`,"future":true}}`, "accept")
	add("Message", `{"header":`+strings.TrimSuffix(header, "}")+`,"future":true},"idempotency_key":"k","body":"body","future":true}`, "accept")
	add("RemoteOrigin", strings.Replace(remote, claim, strings.TrimSuffix(claim, "}")+`,"future":true}`, 1), "unknown_field")
	for _, b := range bases[3:] {
		add(b.name, strings.Replace(b.raw, `"outcome":"`+map[string]string{"InboxChange": "opened", "ChatInboxChange": "opened", "SendResult": "sent", "StopResult": "page", "ObserveDeliveryResult": "observed"}[b.name]+`"`, `"outcome":"future"`, 1), "bad_enum")
	}
	add("PrepareResult", `{"outcome":"page","future":true}`, "unknown_field")
	launch := `{"session":"host","origin":"desktop","cwd":"C:\\host-directory","mode":"plan"}`
	add("ChatLaunch", launch, "accept")
	add("ChatLaunch", strings.Replace(launch, `"desktop"`, `"future-host"`, 1), "accept")
	add("ChatLaunch", strings.Replace(launch, `"plan"`, `"future-mode"`, 1), "accept")
	add("ChatLaunch", strings.Replace(launch, `"origin":"desktop"`, `"origin":1`, 1), "wrong_type")
	add("ChatLaunch", strings.TrimSuffix(launch, "}")+`,"future":true}`, "unknown_field")
	machine := `{"peer_key":"` + strings.Repeat("a", 64) + `","state":"active","service_address":""}`
	add("MachineRecord", machine, "accept")
	add("MachineRecord", strings.TrimSuffix(machine, "}")+`,"last_call_unix_ms":1767268800123}`, "accept")
	add("MachineRecord", strings.TrimSuffix(machine, "}")+`,"last_call_unix_ms":"1767268800123"}`, "wrong_type")
	add("MachineRecord", strings.TrimSuffix(machine, "}")+`,"last_call_ms":1767268800123}`, "unknown_field")
	add("SendResult", `{"outcome":"forbidden","message_id":"","context_id":"","receiver_watching":false,"awaiting_permission":false,"retry_after_ms":0,"refusal":{"source":"rights","decision":"not_granted","ask":"available","action":"abstraction.facade/application.message"}}`, "accept")
	add("SendResult", `{"outcome":"forbidden","message_id":"","context_id":"","receiver_watching":false,"awaiting_permission":false,"retry_after_ms":0,"refusal":{"source":"rights","decision":"future","ask":"none"}}`, "bad_enum")
	add("OriginClaim", strings.TrimSuffix(claim, "}")+`,"future":true}`, "unknown_field")
	add("MessageRef", strings.TrimSuffix(ref, "}")+`,"future":true}`, "unknown_field")
	add("SendRequest", `{"to":"app:r","context":"c","kind":"note","subject":"","body":"body","idempotency_key":"k","expires_unix_ms":1,"future":true}`, "unknown_field")
	add("Page", `{"outcome":"page","cursor":"","revision":"r","messages":[],"headers":[],"unread":50,"pending_questions":0,"oldest_unread_unix_ms":1000}`, "accept")
	add("Page", `{"outcome":"page","cursor":"","revision":"r","messages":[],"headers":[],"unread":-1}`, "bad_value")
	add("Inbox", `{"program":"app:r","context":"r","status":"","revision":"r","watching":false,"last_seen_unix_ms":1,"mine":true}`, "accept")
	add("Inbox", `{"program":"app:r","context":"r","status":"","revision":"r","watching":false,"last_seen_unix_ms":1,"mine":"true"}`, "wrong_type")
	add("Header", strings.TrimSuffix(header, "}")+`,"delivery":"undelivered","undelivered":"future_reason","count":1}`, "accept")
	add("Header", strings.TrimSuffix(header, "}")+`,"delivery":"processed"}`, "bad_enum")
	add("Header", strings.TrimSuffix(header, "}")+`,"delivery":"undelivered","count":0}`, "bad_value")
	add("Header", strings.TrimSuffix(header, "}")+`,"delivery":"undelivered","undelivered":"unconfirmed","count":1}`, "accept")
	add("ObserveDeliveryResult", `{"outcome":"observed","delivery":"undelivered","undelivered":"future_reason"}`, "accept")
	add("ObserveDeliveryResult", `{"outcome":"observed","delivery":"processed"}`, "bad_enum")
	add("SendRequest", `{"to":"app:r","context":"c","kind":"note","subject":"","body":"body","idempotency_key":"k","expires_unix_ms":1,"notify_delivered":true}`, "accept")
	add("SendRequest", `{"to":"app:r","context":"c","kind":"note","subject":"","body":"body","idempotency_key":"k","expires_unix_ms":1,"notify_delivered":"true"}`, "wrong_type")
	names := []string{"Page", "Inbox", "Header", "Message", "RemoteOrigin", "InboxChange", "ChatInboxChange", "SendResult", "StopResult", "ObserveDeliveryResult", "PrepareResult", "OriginClaim", "MessageRef", "SendRequest", "ChatLaunch", "MachineRecord"}
	goCases, pyCases, jsCases, rustCases, cppCases := "", "", "", "", ""
	for _, name := range names {
		snake := snakeName(name)
		goCases += fmt.Sprintf("case %q:_,e=r.Decode%s([]byte(raw));", name, name)
		pyCases += fmt.Sprintf("%q:rec.decode_%s,", name, snake)
		jsCases += fmt.Sprintf("%q:r.decode%s,", name, name)
		rustCases += fmt.Sprintf("%q=>rec::decode_%s_document(raw.as_bytes()).map(|_|()),", name, snake)
		cppCases += fmt.Sprintf("if(kind==%q)rec::decode_%s(raw);else ", name, snake)
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module bound.test\n\ngo 1.22\n", "cases.txt": strings.Join(lines, "\n") + "\n",
		"main.go":  strings.Replace(messagesObservationGo, `case "page":_,e=r.DecodePage([]byte(raw));case "inboxes":_,e=r.DecodeInboxPage([]byte(raw));case "send":_,e=r.DecodeSendRequest([]byte(raw))`, goCases, 1),
		"main.py":  strings.Replace(messagesObservationPython, `'page':rec.decode_page,'inboxes':rec.decode_inbox_page,'send':rec.decode_send_request`, pyCases, 1),
		"main.mjs": strings.Replace(messagesObservationJS, `page:r.decodePage,inboxes:r.decodeInboxPage,send:r.decodeSendRequest`, jsCases, 1),
		"main.rs":  strings.Replace(messagesObservationRust, `"page"=>rec::decode_page_document(raw.as_bytes()).map(|_|()),"inboxes"=>rec::decode_inbox_page_document(raw.as_bytes()).map(|_|()),_=>rec::decode_send_request_document(raw.as_bytes()).map(|_|())`, rustCases+`_=>panic!("unknown case")`, 1),
		"main.cpp": strings.Replace(messagesObservationCpp, `if(kind=="page")rec::decode_page(raw);else if(kind=="inboxes")rec::decode_inbox_page(raw);else rec::decode_send_request(raw);`, cppCases+`throw std::runtime_error("unknown case");`, 1),
	}
	for _, backend := range backends {
		path := map[string]string{"go": "rec/rec.go", "python": "rec.py", "javascript": "rec.mjs", "rust": "rec.rs", "cpp": "rec.h"}[backend.lang]
		if path != "" {
			files[path] = mustEmitIncluded(t, backend, d)
		}
	}
	for name, body := range files {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, strings.Join(wants, "\n")+"\n")
}

// The codec corpus is a single standalone namespace per language. Copy the
// included owner's exact refusal definitions into that fixture, while the
// production surface checks compile the real imported bindings.
func messagesObservationDefinition(t *testing.T, body string) string {
	t.Helper()
	raw, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-rights", "rights.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	rights := string(raw)
	start, end := strings.Index(rights, "enum RefusalSource {"), strings.Index(rights, "service Authorization {")
	if start < 0 || end < start {
		t.Fatal("Rights refusal owner missing")
	}
	body = strings.Replace(body, `include "../abstraction-rights/rights.thrift"`, "", 1)
	return strings.ReplaceAll(body, "rights.RefusalDetail", "RefusalDetail") + "\n" + rights[start:end]
}

const messagesClosedRevision = "185222d2c3df63d24626b7cd46330237725a145f"

func historicalMessages(t *testing.T, path string) string {
	t.Helper()
	privateInput(t, "..", "..", "openabstractions-flat", "abstraction-facade", "messages.thrift")
	cmd := exec.Command("git", "show", messagesClosedRevision+":"+path)
	cmd.Dir = filepath.Join("..", "..")
	body, err := cmd.Output()
	if err != nil {
		t.Fatal("retained historical Messages source required", err)
	}
	return string(body)
}

// These readers deliberately retain the historical shape, which has neither
// wake nor chat_title. Opening observation records is the only semantic change.
func TestMessagesOldShapeOpenObservationReadersFiveLanguages(t *testing.T) {
	old := historicalMessages(t, "openabstractions-flat/abstraction-facade/messages.thrift")
	d := mustParse(t, old)
	for _, name := range []string{"Page", "InboxPage", "Inbox", "Header", "Message", "RemoteOrigin", "InboxChange", "SendResult", "StopResult"} {
		d.Struct(name).UnknownFields = "grant"
		d.Struct(name).Ann["unknown_fields"] = "grant"
	}
	d.Namespaces = map[string]string{"*": "rec"}
	d.Services = nil
	d.NoIPC, d.NamedCodecs = true, true
	code := func(lang string) string {
		for _, backend := range backends {
			if backend.lang == lang {
				return mustEmitIncluded(t, backend, d)
			}
		}
		t.Fatal("missing backend", lang)
		return ""
	}
	page := `{"outcome":"page","cursor":"","revision":"r","messages":[],"headers":[]}`
	inbox := `{"program":"app:receiver","context":"chat","status":"","revision":"r","watching":false,"last_seen":1}`
	listing := `{"outcome":"page","cursor":"","inboxes":[` + inbox + `]}`
	header := `{"message_id":"m","from":"app:s","from_context":"s","to":"app:r","context":"r","kind":"note","subject":"","context_id":"c","expires_at":1,"refused":false}`
	message := `{"header":` + header + `,"request_key":"k","body":"body","future":true}`
	cases := []struct{ kind, input, want string }{
		{"page", page, "accept"},
		{"page", strings.TrimSuffix(page, "}") + `,"unread":50,"pending_questions":7,"oldest_unread_at":1700000000000}`, "accept"},
		{"page", strings.Replace(page, `"headers":[]`, `"headers":[`+strings.TrimSuffix(header, "}")+`,"delivery":"undelivered","undelivered":"expired","count":1}]`, 1), "accept"},
		{"inboxes", strings.Replace(listing, inbox, strings.TrimSuffix(inbox, "}")+`,"mine":true}`, 1), "accept"},
		{"page", strings.Replace(page, `"headers":[]`, `"headers":[],"wake":"0001","future":{"values":[1,true,null]}`, 1), "accept"},
		{"inboxes", strings.Replace(listing, inbox, strings.TrimSuffix(inbox, "}")+`,"chat_title":"Named chat","future_metadata":{"available":true}}`, 1), "accept"},
		{"inboxes", strings.TrimSuffix(listing, "}") + `,"future_page":true}`, "accept"},
		{"page", strings.Replace(page, `"outcome":"page",`, "", 1), "missing_field"},
		{"page", strings.Replace(page, `"cursor":""`, `"cursor":5`, 1), "wrong_type"},
		{"page", strings.Replace(page, `"outcome":"page"`, `"outcome":"future"`, 1), "bad_enum"},
		{"page", strings.Replace(page, `"cursor":""`, `"cursor":"`+strings.Repeat("x", 257)+`"`, 1), "bad_value"},
		{"page", strings.TrimSuffix(page, "}") + `,"future":1,"\u0066uture":2}`, "duplicate_key"},
		{"page", strings.TrimSuffix(page, "}") + `,"future":{"x":1,"x":2}}`, "duplicate_key"},
		{"page", strings.TrimSuffix(page, "}") + `,"future":` + strings.Repeat("[", 70) + strings.Repeat("]", 70) + `}`, "depth_exceeded"},
		{"inboxes", strings.Replace(listing, `"watching":false`, `"watching":"false"`, 1), "wrong_type"},
		{"inboxes", strings.Replace(listing, `"program":"app:receiver",`, "", 1), "missing_field"},
		{"page", strings.Replace(page, `"headers":[]`, `"headers":[`+strings.TrimSuffix(header, "}")+`,"future":true}]`, 1), "accept"},
		{"page", strings.Replace(page, `"messages":[]`, `"messages":[`+message+`]`, 1), "accept"},
		{"send", `{"to":"app:r","context":"c","kind":"note","subject":"","body":"body","request_key":"k","expires_at":1,"future":true}`, "unknown_field"},
	}
	var inputs, wants []string
	for _, c := range cases {
		inputs = append(inputs, c.kind+"\t"+c.input)
		wants = append(wants, c.want)
	}
	dir := t.TempDir()
	for name, body := range map[string]string{
		"cases.txt": strings.Join(inputs, "\n") + "\n", "go.mod": "module bound.test\n\ngo 1.22\n",
		"rec/rec.go": code("go"), "rec.py": code("python"), "rec.mjs": code("javascript"), "rec.rs": code("rust"), "rec.h": code("cpp"),
		"main.go": messagesObservationGo, "main.py": messagesObservationPython, "main.mjs": messagesObservationJS, "main.rs": messagesObservationRust, "main.cpp": messagesObservationCpp,
	} {
		writeNamespaceFile(t, dir, name, body)
	}
	runFiveConstraintPrograms(t, dir, strings.Join(wants, "\n")+"\n")
}

// Compile the actual historical generated Go reader bytes, rather than a
// reconstructed closed schema. A newer server cannot repair this old binary.
func TestMessagesHistoricalClosedReaderRequiresHelperUpdate(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"go.mod":     "module bound.test\n\ngo 1.22\n",
		"rec/rec.go": historicalMessages(t, "openabstractions-flat/abstraction-facade/go/abstraction/facade/messages/rec.go"),
		"main.go":    messagesObservationGo,
		"cases.txt":  "page\t{\"outcome\":\"page\",\"cursor\":\"\",\"revision\":\"r\",\"messages\":[],\"headers\":[],\"wake\":\"0001\"}\ninboxes\t{\"outcome\":\"page\",\"cursor\":\"\",\"inboxes\":[{\"program\":\"app:r\",\"context\":\"c\",\"status\":\"\",\"revision\":\"r\",\"watching\":false,\"last_seen\":1,\"chat_title\":\"New title\"}]}\n",
	} {
		writeNamespaceFile(t, dir, name, body)
	}
	cmd := exec.Command("go", "run", "main.go")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil || strings.ReplaceAll(string(out), "\r\n", "\n") != "unknown_field\nunknown_field\n" {
		t.Fatal("historical closed reader no longer demonstrates update boundary", err, string(out))
	}
}

func TestMessagesObservationUnknownPolicyBoundary(t *testing.T) {
	body, err := os.ReadFile(privateInput(t, "..", "..", "openabstractions-flat", "abstraction-facade", "messages.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	d := mustParse(t, messagesObservationDefinition(t, string(body)))
	for _, record := range d.Structs {
		want := "refuse"
		if messagesOutputObservation(record.Name) {
			want = "grant"
		}
		if record.UnknownFields != want {
			t.Fatal("observation policy reached a canonical/input record", record.Name, record.UnknownFields, want)
		}
	}
}

const messagesObservationGo = `package main
import("fmt";"os";"strings";r "bound.test/rec")
func main(){b,_:=os.ReadFile("cases.txt");for _,line:=range strings.Split(strings.TrimSpace(string(b)),"\n"){kind,raw,_:=strings.Cut(line,"\t");var e error;switch kind{case "page":_,e=r.DecodePage([]byte(raw));case "inboxes":_,e=r.DecodeInboxPage([]byte(raw));case "send":_,e=r.DecodeSendRequest([]byte(raw))};if e!=nil{fmt.Println(e.(*r.Refusal).Word)}else{fmt.Println("accept")}}}`
const messagesObservationPython = `import rec
for line in open('cases.txt',encoding='utf-8').read().splitlines():
 kind,raw=line.split('\t',1)
 try:
  {'page':rec.decode_page,'inboxes':rec.decode_inbox_page,'send':rec.decode_send_request}[kind](raw.encode());print('accept')
 except rec.Refusal as e:print(e.word)
`
const messagesObservationJS = `import * as r from './rec.mjs';import fs from 'node:fs';for(const line of fs.readFileSync('cases.txt','utf8').trim().split('\n')){const n=line.indexOf('\t'),kind=line.slice(0,n),raw=line.slice(n+1);try{({page:r.decodePage,inboxes:r.decodeInboxPage,send:r.decodeSendRequest}[kind])(Buffer.from(raw));console.log('accept')}catch(e){if(!(e instanceof r.Refusal))throw e;console.log(e.word)}}`
const messagesObservationRust = `mod rec;fn main(){for line in std::fs::read_to_string("cases.txt").unwrap().lines(){let(kind,raw)=line.split_once('\t').unwrap();let result=match kind{"page"=>rec::decode_page_document(raw.as_bytes()).map(|_|()),"inboxes"=>rec::decode_inbox_page_document(raw.as_bytes()).map(|_|()),_=>rec::decode_send_request_document(raw.as_bytes()).map(|_|())};match result{Ok(_)=>println!("accept"),Err(e)=>println!("{}",e.word)}}}`
const messagesObservationCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
int main(){std::ifstream f("cases.txt");std::string line;while(std::getline(f,line)){auto n=line.find('\t');auto kind=line.substr(0,n),raw=line.substr(n+1);try{if(kind=="page")rec::decode_page(raw);else if(kind=="inboxes")rec::decode_inbox_page(raw);else rec::decode_send_request(raw);std::cout<<"accept\n";}catch(const rec::Refusal&e){std::cout<<e.word<<'\n';}}}`

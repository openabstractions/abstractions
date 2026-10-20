package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nestedFixture carries an opaque value two records deep through a service
// argument and its result (generation P1-3).
const nestedFixture = `namespace go nest
namespace python nest
namespace cpp nest
namespace javascript nest
namespace rust nest
encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "verbatim"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "1000"
}
refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: depth_exceeded   (stage = "grammar")
   6: duplicate_key    (stage = "grammar")
   7: duplicate_field  (stage = "structure")
   8: unknown_field    (stage = "structure")
   9: missing_field    (stage = "structure")
  10: trailing_bytes   (stage = "document")
}
struct Inner {1: required json payload} (unknown_fields="refuse")
struct Outer {1: required Inner inner} (document="true", unknown_fields="refuse")
service Nest { Outer Echo(1: Outer value) } (wire_name="example.nest/nest@1")
`

// nestedPayload has interior whitespace, escapes, number spellings and a
// member order no encoder would choose.
const nestedPayload = "{ \"z\" : [ 1 ,2.50, -0.0,1e3, \"\\u0041\\/\" ],\n\t\"a\" : { } , \"m\":{\"y\":null,\"x\":true}\n}"

func nestedReply(payload string) string {
	return `{"version":1,"service":"example.nest/nest@1","method":"Echo","ok":true,"payload":{"value":{"inner":{"payload":` + payload + `}}}}`
}

func nestedDeep(n int) string { return strings.Repeat("[", n) + strings.Repeat("]", n) }

// nestedDoc is an Outer as to_json writes it: the record layout at the
// document indentation, and the opaque value with its own interior
// whitespace, escapes and member order, which to_json keeps ([DEF-A3]).
const nestedDoc = "{\n  \"inner\": {\n    \"payload\": " + nestedPayload + "\n  }\n}"

const nestedDuplicate = `{"a":{"b":{"c":1,"c":2}}}`

// nestedWant is what every language prints: the value survives a client, the
// Go dispatcher and the client again byte for byte; a duplicate member three
// objects into the value and nesting past 1000 are refused in a reply and
// before a request leaves; 900 levels are accepted.
const nestedWant = "roundtrip ok\nreply 0 ok\nreply 1 duplicate_key\nreply 2 depth_exceeded\nreply 3 ok\nrequest dup duplicate_key\nrequest deep depth_exceeded\n"

func TestServiceNestedOpaqueValidation(t *testing.T) {
	s, err := parse(nestedFixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, lang := range []string{"go", "python", "cpp", "javascript", "rust"} {
		if err := validateServiceBackend(s, lang); err != nil {
			t.Fatalf("%s: %v", lang, err)
		}
	}
	if _, err := parse(strings.Replace(nestedFixture, `depth_limit    = "1000"`, `depth_limit    = "1001"`, 1)); err == nil || !strings.Contains(err.Error(), "1 to 1000") {
		t.Fatalf("depth_limit past 1000 accepted: %v", err)
	}
}

func TestServiceNestedOpaqueFiveLanguages(t *testing.T) {
	p := servicePython(t)
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable: five-language nested opaque round trip unproven")
	}
	rust := rustServiceCompiler(t)
	cxx := os.Getenv("CXX")
	for _, candidate := range []string{"g++", "clang++"} {
		if cxx == "" {
			cxx, _ = exec.LookPath(candidate)
		}
	}
	if cxx == "" {
		t.Skip("C++ compiler (CXX, g++ or clang++) unavailable: five-language nested opaque round trip unproven")
	}
	s, err := parse(nestedFixture)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	files := map[string]string{
		"go.mod": "module nest.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "main.go": nestedGo,
		"rec.py": genPy(s), "main.py": nestedPy,
		"rec.mjs": genJS(s), "main.mjs": nestedJS,
		"rec.rs": genRust(s), "main.rs": nestedRust,
		"rec.h": genCpp(s), "main.cpp": nestedCpp,
		"payload.json":      nestedPayload,
		"reply-0.json":      nestedReply(nestedPayload),
		"reply-1.json":      nestedReply(nestedDuplicate),
		"reply-2.json":      nestedReply(nestedDeep(1001)),
		"reply-3.json":      nestedReply(nestedDeep(900)),
		"request-dup.json":  nestedDuplicate,
		"request-deep.json": nestedDeep(1001),
		"doc.json":          nestedDoc,
		"doc-trailing.json": nestedDoc + " 1",
	}
	for n, b := range files {
		writeNamespaceFile(t, dir, n, b)
	}
	exe := func(name string) string { return filepath.Join(dir, name) }
	build := func(command ...string) {
		t.Helper()
		c := exec.Command(command[0], command[1:]...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GOWORK=off")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", command, err, out)
		}
	}
	build("go", "build", "-o", exe("host.exe"), "main.go")
	build(rust, "--edition=2021", "main.rs", "-o", exe("rust.exe"))
	cppArgs := []string{"-std=c++17", "main.cpp", "-o", exe("cpp.exe")}
	if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
		cppArgs = []string{"/nologo", "/std:c++17", "/EHsc", "main.cpp", "/Fe:" + exe("cpp.exe"), "/Fo:" + exe("main.obj")}
	}
	build(append([]string{cxx}, cppArgs...)...)
	clients := map[string][]string{
		"go":         {exe("host.exe")},
		"python":     {p, "-B", "main.py"},
		"javascript": {node, "main.mjs"},
		"rust":       {exe("rust.exe")},
		"cpp":        {exe("cpp.exe")},
	}
	run := func(t *testing.T, lang string, stdin []byte, args ...string) []byte {
		t.Helper()
		command := append(append([]string(nil), clients[lang]...), args...)
		c := exec.Command(command[0], command[1:]...)
		c.Dir = dir
		if stdin != nil {
			c.Stdin = strings.NewReader(string(stdin))
		}
		out, err := c.Output()
		if err != nil {
			if x, ok := err.(*exec.ExitError); ok {
				t.Fatalf("%s %v: %v\n%s%s", lang, args, err, out, x.Stderr)
			}
			t.Fatalf("%s %v: %v\n%s", lang, args, err, out)
		}
		return out
	}
	for _, lang := range []string{"go", "python", "javascript", "rust", "cpp"} {
		t.Run(lang, func(t *testing.T) {
			run(t, lang, nil, "emit", lang)
			request, err := os.ReadFile(exe("request-" + lang + ".json"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(request), nestedPayload) {
				t.Fatalf("%s request frame lost the value's bytes:\n%s", lang, request)
			}
			reply := run(t, "go", request, "serve")
			if !strings.Contains(string(reply), nestedPayload) {
				t.Fatalf("Go dispatcher reply for %s lost the value's bytes:\n%s", lang, reply)
			}
			writeNamespaceFile(t, dir, "roundtrip-"+lang+".json", string(reply))
			if got := string(run(t, lang, nil, "check", lang)); strings.ReplaceAll(got, "\r\n", "\n") != nestedWant {
				t.Fatalf("%s printed\n%s\nwant\n%s", lang, got, nestedWant)
			}
			// CC-8: the public to_json and from_json, no private codec.
			if got := string(run(t, lang, nil, "json", lang)); strings.ReplaceAll(got, "\r\n", "\n") != "json same trailing_bytes\n" {
				t.Fatalf("%s to_json/from_json printed %q", lang, got)
			}
		})
	}
}

// nestedGo is the Go host: "serve" dispatches one frame from stdin and the
// handler refuses with "altered" unless it received payload.json's bytes;
// "emit" and "check" run the same client sequence as every other language.
const nestedGo = `package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	r "nest.test/rec"
)

type handler struct{ want string }

func (h handler) Echo(v r.Outer) (r.Outer, error) {
	if string(v.Inner.Payload) != h.want {
		return r.Outer{}, &r.ServiceError{Code: "altered"}
	}
	return v, nil
}

type transport struct {
	mode, lang string
	reply      []byte
}

func (t *transport) ExchangeFrame(b []byte) ([]byte, error) {
	if t.mode == "emit" {
		if err := os.WriteFile("request-"+t.lang+".json", b, 0o600); err != nil {
			panic(err)
		}
		return nil, errors.New("captured")
	}
	return t.reply, nil
}

func word(err error) string {
	var refusal *r.Refusal
	var service *r.ServiceError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &refusal):
		return refusal.Word
	case errors.As(err, &service):
		return "service:" + string(service.Code)
	}
	return "error:" + err.Error()
}

func read(name string) []byte {
	b, err := os.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return b
}

func main() {
	want := read("payload.json")
	if os.Args[1] == "serve" {
		frame, err := io.ReadAll(os.Stdin)
		if err != nil {
			panic(err)
		}
		d := r.NestDispatcher{Handler: handler{string(want)}}
		out, err := d.ExchangeFrame(frame)
		if err != nil {
			panic(err)
		}
		os.Stdout.Write(out)
		return
	}
	if os.Args[1] == "json" {
		doc := read("doc.json")
		var v r.Outer
		if err := r.FromJSON(doc, &v); err != nil {
			panic(err)
		}
		out, err := r.ToJSON(v)
		same := "differs"
		if err == nil && string(out) == string(doc) {
			same = "same"
		}
		// Go misuse: a nil record pointer and a foreign value are refusals.
		for _, bad := range []any{(*r.Outer)(nil), 42} {
			if _, e := r.ToJSON(bad); word(e) != "wrong_type" {
				panic(fmt.Sprintf("ToJSON(%#v): %v", bad, e))
			}
		}
		if e := r.FromJSON(doc, (*r.Outer)(nil)); word(e) != "wrong_type" {
			panic(fmt.Sprintf("FromJSON into nil: %v", e))
		}
		err = r.FromJSON(read("doc-trailing.json"), &v)
		fmt.Println("json", same, word(err))
		return
	}
	t := &transport{mode: os.Args[1], lang: os.Args[2]}
	c := r.NewNestClient(t)
	call := func(payload []byte) (r.Outer, error) {
		return c.Echo(r.Outer{Inner: r.Inner{Payload: r.Raw(payload)}})
	}
	if t.mode == "emit" {
		call(want)
		return
	}
	t.reply = read("roundtrip-" + t.lang + ".json")
	v, err := call(want)
	if err == nil && string(v.Inner.Payload) != string(want) {
		fmt.Println("roundtrip altered")
	} else {
		fmt.Println("roundtrip", word(err))
	}
	for k := 0; k < 4; k++ {
		t.reply = read(fmt.Sprintf("reply-%d.json", k))
		_, err := call(want)
		fmt.Println("reply", k, word(err))
	}
	t.reply = read("reply-0.json")
	for _, name := range []string{"dup", "deep"} {
		_, err := call(read("request-" + name + ".json"))
		fmt.Println("request", name, word(err))
	}
}
`

const nestedPy = `import sys
import rec as r

mode, lang = sys.argv[1], sys.argv[2]


class Captured(Exception):
    pass


class Transport:
    reply = None

    def exchange_frame(self, frame):
        if mode == "emit":
            with open("request-%s.json" % lang, "wb") as f:
                f.write(frame)
            raise Captured()
        return self.reply


def read(name):
    with open(name, "rb") as f:
        return f.read()


def raw(x):
    return x.encode("utf-8") if isinstance(x, str) else bytes(x)


t = Transport()
c = r.NestClient(t)
want = read("payload.json")


def call(payload):
    try:
        v = c.echo(r.Outer(inner=r.Inner(payload=payload)))
        return "ok" if raw(v.inner.payload) == raw(payload) or t.reply != roundtrip else "altered"
    except r.Refusal as e:
        return e.word
    except r.ServiceError as e:
        return "service:" + e.code


if mode == "json":
    doc = read("doc.json")
    v = r.from_json(r.Outer, doc)
    same = "same" if r.to_json(v) == doc else "differs"
    try:
        r.from_json(r.Outer, read("doc-trailing.json"))
        word = "ok"
    except r.Refusal as e:
        word = e.word
    print("json", same, word)
    sys.exit(0)
if mode == "emit":
    try:
        c.echo(r.Outer(inner=r.Inner(payload=want)))
    except Captured:
        pass
    sys.exit(0)
roundtrip = t.reply = read("roundtrip-%s.json" % lang)
print("roundtrip", call(want))
for k in range(4):
    t.reply = read("reply-%d.json" % k)
    print("reply", k, call(want))
t.reply = read("reply-0.json")
for name in ("dup", "deep"):
    print("request", name, call(read("request-%s.json" % name)))
`

const nestedJS = `import * as r from "./rec.mjs";
import { readFileSync, writeFileSync } from "node:fs";

const [mode, lang] = process.argv.slice(2);
class Captured extends Error {}
const t = {
  reply: null,
  async exchangeFrame(frame) {
    if (mode === "emit") { writeFileSync("request-" + lang + ".json", frame); throw new Captured(); }
    return this.reply;
  },
};
const c = new r.NestClient(t);
const read = (name) => new Uint8Array(readFileSync(name));
const bytes = (x) => (typeof x === "string" ? new TextEncoder().encode(x) : x);
const same_ = (a, b) => a.length === b.length && a.every((v, i) => v === b[i]);
const same = (a, b) => a.length === b.length && a.every((v, i) => v === b[i]);
const want = read("payload.json");
let roundtrip = null;
async function call(payload) {
  const value = r.newOuter();
  value.inner.payload = new TextDecoder().decode(payload);
  try {
    const v = await c.echo(value);
    return t.reply !== roundtrip || same(bytes(v.inner.payload), payload) ? "ok" : "altered";
  } catch (e) {
    if (e instanceof r.Refusal) return e.word;
    if (e instanceof r.ServiceError) return "service:" + e.code;
    throw e;
  }
}
if (mode === "json") {
  const doc = read("doc.json");
  const same = same_(r.toJson("Outer", r.fromJson("Outer", doc)), doc) ? "same" : "differs";
  let word = "ok";
  try { r.fromJson("Outer", read("doc-trailing.json")); } catch (e) { if (!(e instanceof r.Refusal)) throw e; word = e.word; }
  process.stdout.write("json " + same + " " + word + "\n");
} else if (mode === "emit") {
  try { await call(want); } catch (e) { if (!(e instanceof Captured)) throw e; }
} else {
  roundtrip = t.reply = read("roundtrip-" + lang + ".json");
  const out = ["roundtrip " + (await call(want))];
  for (let k = 0; k < 4; k++) { t.reply = read("reply-" + k + ".json"); out.push("reply " + k + " " + (await call(want))); }
  t.reply = read("reply-0.json");
  for (const name of ["dup", "deep"]) out.push("request " + name + " " + (await call(read("request-" + name + ".json"))));
  process.stdout.write(out.join("\n") + "\n");
}
`

const nestedRust = `mod rec;
use rec::{CallError, FrameTransport, JsonRecord, Nest};
use std::cell::RefCell;

struct Transport { mode: String, lang: String, reply: RefCell<Vec<u8>> }
impl FrameTransport for Transport {
    type Error = ();
    fn write_frame(&self, _frame: &[u8]) -> Result<(), ()> { Err(()) }
    fn exchange_frame(&self, frame: &[u8]) -> Result<Vec<u8>, ()> {
        if self.mode == "emit" {
            std::fs::write(format!("request-{}.json", self.lang), frame).unwrap();
            return Err(());
        }
        Ok(self.reply.borrow().clone())
    }
}

fn read(name: &str) -> Vec<u8> { std::fs::read(name).unwrap() }

fn main() {
    let args: Vec<String> = std::env::args().collect();
    if args[1] == "json" {
        let doc = read("doc.json");
        let v = rec::Outer::from_json(&doc).unwrap();
        let same = if v.to_json() == doc { "same" } else { "differs" };
        let word = match rec::Outer::from_json(&read("doc-trailing.json")) { Ok(_) => "ok", Err(e) => e.word };
        println!("json {} {}", same, word);
        return;
    }
    let t = Transport { mode: args[1].clone(), lang: args[2].clone(), reply: RefCell::new(Vec::new()) };
    let lang = t.lang.clone();
    let emit = t.mode == "emit";
    let c = rec::NestClient::new(t);
    let want = read("payload.json");
    let call = |payload: Vec<u8>, compare: bool| -> String {
        match c.echo(rec::Outer { inner: rec::Inner { payload: payload.clone() } }) {
            Ok(v) => if !compare || v.inner.payload == payload { "ok".into() } else { "altered".into() },
            Err(CallError::Refusal(e)) => e.word.into(),
            Err(CallError::Service(e)) => format!("service:{}", e.code),
            Err(CallError::Dispatch(w)) => format!("dispatch:{}", w),
            Err(CallError::Transport(())) => "transport".into(),
        }
    };
    if emit {
        call(want, false);
        return;
    }
    let set = |name: &str| { *c.transport().reply.borrow_mut() = read(name); };
    set(&format!("roundtrip-{}.json", lang));
    println!("roundtrip {}", call(want.clone(), true));
    for k in 0..4 {
        set(&format!("reply-{}.json", k));
        println!("reply {} {}", k, call(want.clone(), false));
    }
    set("reply-0.json");
    for name in ["dup", "deep"] {
        println!("request {} {}", name, call(read(&format!("request-{}.json", name)), false));
    }
}
`

const nestedCpp = `#include "rec.h"
#include <fstream>
#include <iostream>
#include <iterator>
#include <string>

struct Captured {};
std::string read(const std::string& name) {
    std::ifstream in(name, std::ios::binary);
    if (!in) throw std::runtime_error("cannot read " + name);
    return std::string(std::istreambuf_iterator<char>(in), {});
}
struct Transport {
    std::string mode, lang, reply;
    void write_frame(std::string_view) {}
    std::string exchange_frame(std::string_view frame) {
        if (mode == "emit") {
            std::ofstream("request-" + lang + ".json", std::ios::binary).write(frame.data(), static_cast<std::streamsize>(frame.size()));
            throw Captured{};
        }
        return reply;
    }
};
int main(int argc, char** argv) {
    if (argc < 3) return 2;
    if (std::string(argv[1]) == "json") {
        const std::string doc = read("doc.json");
        rec::Outer v = rec::from_json<rec::Outer>(doc);
        std::string word = "ok";
        try { rec::from_json<rec::Outer>(read("doc-trailing.json")); } catch (const rec::Refusal& e) { word = e.word; }
        std::cout << "json " << (rec::to_json(v) == doc ? "same" : "differs") << " " << word << "\n";
        return 0;
    }
    Transport t{argv[1], argv[2], ""};
    rec::NestClient<Transport> c(t);
    const std::string want = read("payload.json");
    auto call = [&](const std::string& payload, bool compare) -> std::string {
        rec::Outer value;
        value.inner.payload = payload;
        try {
            rec::Outer v = c.echo(value);
            return !compare || v.inner.payload == payload ? "ok" : "altered";
        } catch (const rec::Refusal& e) {
            return e.word;
        } catch (const rec::ServiceError& e) {
            return "service:" + e.code;
        }
    };
    if (t.mode == "emit") {
        try { call(want, false); } catch (const Captured&) {}
        return 0;
    }
    t.reply = read("roundtrip-" + t.lang + ".json");
    std::string out = "roundtrip " + call(want, true) + "\n";
    for (int k = 0; k < 4; ++k) {
        t.reply = read("reply-" + std::to_string(k) + ".json");
        out += "reply " + std::to_string(k) + " " + call(want, false) + "\n";
    }
    t.reply = read("reply-0.json");
    for (const char* name : {"dup", "deep"}) out += std::string("request ") + name + " " + call(read(std::string("request-") + name + ".json"), false) + "\n";
    std::cout << out;
    return 0;
}
`

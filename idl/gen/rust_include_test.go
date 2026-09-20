package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var rsIncludeMappings = []string{"--rust-import=model=fixture_model", "--rust-import=request=fixture_request"}

// rustIncludeFixture renames the Rust spelling of the keyword field ref. The
// wire key stays "ref", so Go and Rust exchange identical bytes.
func rustIncludeFixture(t *testing.T) (string, string) {
	t.Helper()
	dir, out := includeFixture(t)
	p := filepath.Join(dir, "resolver.thrift")
	raw, _ := os.ReadFile(p)
	src := strings.Replace(string(raw), "model.Ref ref 2:", `model.Ref ref (rust.name = "reference") 2:`, 1)
	src = strings.Replace(src, "Resolve(1: model.Ref ref)", `Resolve(1: model.Ref ref (rust.name = "reference"))`, 1)
	if src == string(raw) {
		t.Fatal("fixture rename did not apply")
	}
	writeNamespaceFile(t, dir, "resolver.thrift", src)
	return dir, out
}

func generateRustIncludes(t *testing.T, dir, out string, extra ...string) {
	t.Helper()
	for _, name := range []string{"model", "request"} {
		var report bytes.Buffer
		if e := run([]string{filepath.Join(dir, name+".thrift"), out, "--named-codecs", "rust"}, &report); e != nil {
			t.Fatal(name, e)
		}
	}
	var report bytes.Buffer
	args := append(append([]string{filepath.Join(dir, "resolver.thrift"), out}, rsIncludeMappings...), extra...)
	if e := run(append(args, "rust"), &report); e != nil {
		t.Fatal(e)
	}
}

// buildRustIncludeProgram compiles both dependencies as separate crates and
// the consumer against them, the way Cargo links generated API crates.
func buildRustIncludeProgram(t *testing.T, rust, out, source, name string) string {
	t.Helper()
	writeNamespaceFile(t, out, name+".rs", source)
	for _, command := range [][]string{
		{rust, "--edition=2021", "--crate-type=rlib", "--crate-name=fixture_request", "rs/abstraction/download/request/rec.rs", "-o", "libfixture_request.rlib"},
		{rust, "--edition=2021", "--crate-type=rlib", "--crate-name=fixture_model", "rs/abstraction/model/api/rec.rs", "-o", "libfixture_model.rlib"},
		{rust, "--edition=2021", name + ".rs", "--extern", "fixture_model=libfixture_model.rlib", "--extern", "fixture_request=libfixture_request.rlib", "-L", ".", "-o", name + ".exe"},
	} {
		c := exec.Command(command[0], command[1:]...)
		c.Dir = out
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("%v: %v\n%s", command, e, b)
		}
	}
	return filepath.Join(out, name+".exe")
}

func TestRustTypedIncludes(t *testing.T) {
	rust := rustServiceCompiler(t)
	dir, out := rustIncludeFixture(t)
	rs := filepath.Join(out, "rust")
	generateRustIncludes(t, dir, rs)
	again := filepath.Join(dir, "again")
	generateRustIncludes(t, dir, again)
	for _, rel := range []string{"rs/cross/resolver/rec.rs", "rs/abstraction/model/api/rec.rs", "rs/abstraction/download/request/rec.rs"} {
		x, _ := os.ReadFile(filepath.Join(rs, rel))
		y, e := os.ReadFile(filepath.Join(again, rel))
		if e != nil || len(x) == 0 || !bytes.Equal(x, y) {
			t.Fatal("nondeterministic", rel)
		}
	}
	codecPath := filepath.Join(rs, "rs/cross/resolver/rec.rs")
	codec, _ := os.ReadFile(codecPath)
	for _, line := range []string{"type OAImported0 = fixture_model::Ref;\n", "fixture_request::internal::decode_request_at(&r.buf[r.pos..], r.depth, r.limit)"} {
		if !strings.Contains(string(codec), line) {
			t.Fatalf("generated crate lacks %q", line)
		}
	}
	if strings.Contains(string(codec), "pub struct Ref ") || strings.Contains(string(codec), "fn decode_ref(") {
		t.Fatal("imported codec body copied into importing crate")
	}

	goOut := filepath.Join(out, "gohost")
	for _, name := range []string{"model", "request", "resolver"} {
		args := []string{filepath.Join(dir, name+".thrift"), goOut, "--named-codecs", "go"}
		if name == "resolver" {
			args = []string{filepath.Join(dir, name+".thrift"), goOut, "--go-import=model=example.test/abstraction/model/api", "--go-import=request=example.test/abstraction/download/request", "go"}
		}
		var report bytes.Buffer
		if e := run(args, &report); e != nil {
			t.Fatal(name, e)
		}
	}
	writeNamespaceFile(t, goOut, "go/go.mod", "module example.test\n\ngo 1.23\n")
	writeNamespaceFile(t, goOut, "go/main.go", jsIncludeGoHost)
	host := filepath.Join(goOut, "host.exe")
	build := exec.Command("go", "build", "-o", host, ".")
	build.Dir = filepath.Join(goOut, "go")
	build.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("Go host: %v\n%s", e, b)
	}
	consumer := buildRustIncludeProgram(t, rust, rs, rsIncludeConsumer, "consumer")
	if b, e := exec.Command(consumer, host).CombinedOutput(); e != nil {
		t.Fatalf("Rust consumer: %v\n%s", e, b)
	} else {
		t.Log(strings.TrimSpace(string(b)))
	}

	// Mutation control: a bridge that resets the enclosing depth must fail the
	// consumer's exhaustion assertion while ordinary round trips still pass.
	mutated := strings.ReplaceAll(string(codec), "&r.buf[r.pos..], r.depth, r.limit", "&r.buf[r.pos..], 0, r.limit")
	if mutated == string(codec) {
		t.Fatal("depth mutation did not match bridge")
	}
	os.WriteFile(codecPath, []byte(mutated), 0600)
	mutant := buildRustIncludeProgram(t, rust, rs, rsIncludeConsumer, "mutant")
	if b, e := exec.Command(mutant, host).CombinedOutput(); e == nil || !strings.Contains(string(b), "depth reset") {
		t.Fatalf("depth mutation escaped regression: %v %s", e, b)
	}
	os.WriteFile(codecPath, codec, 0600)
}

func TestRustIncludeMixedDepth(t *testing.T) {
	rust := rustServiceCompiler(t)
	dir, out := rustIncludeFixture(t)
	p := filepath.Join(dir, "resolver.thrift")
	raw, _ := os.ReadFile(p)
	src := string(raw[:bytes.Index(raw, []byte("service Resolver"))])
	src = strings.Replace(src, `depth_limit="64"`, `depth_limit="3"`, 1)
	writeNamespaceFile(t, dir, "resolver.thrift", src)
	generateRustIncludes(t, dir, out)
	mixed := buildRustIncludeProgram(t, rust, out, rsIncludeMixed, "mixed")
	if b, e := exec.Command(mixed).CombinedOutput(); e != nil {
		t.Fatalf("mixed Rust: %v %s", e, b)
	}
}

func TestRustIncludeRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing", []string{"--rust-import=model=fixture_model"}, "--rust-import=request"},
		{"unused", append([]string{"--rust-import=other=fixture_other"}, rsIncludeMappings...), "does not include"},
		{"ambiguous", []string{"--rust-import=model=fixture_same", "--rust-import=request=fixture_same"}, "ambiguous Rust crate"},
		{"malformed", []string{"--rust-import=model=fixture-model", "--rust-import=request=fixture_request"}, "requires alias=crate-path"},
		{"duplicate", append([]string{"--rust-import=model=fixture_other"}, rsIncludeMappings...), "duplicate Rust import"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, out := rustIncludeFixture(t)
			var b bytes.Buffer
			e := run(append(append([]string{filepath.Join(dir, "resolver.thrift"), out}, tc.args...), "rust"), &b)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("got %v, want %s", e, tc.want)
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("wrote partial output")
			}
		})
	}
	dir, out := rustIncludeFixture(t)
	p := filepath.Join(dir, "resolver.thrift")
	data, _ := os.ReadFile(p)
	writeNamespaceFile(t, dir, "resolver.thrift", string(data)+"\nstruct QueryAt {1: required string text}(unknown_fields=\"refuse\")\n")
	var b bytes.Buffer
	if e := run(append(append([]string{p, out}, rsIncludeMappings...), "rust"), &b); e == nil || !strings.Contains(e.Error(), "QueryAt") {
		t.Fatalf("accepted colliding named codec: %v", e)
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		t.Fatal("collision wrote partial output")
	}
	dir, out = rustIncludeFixture(t)
	selected := append(append([]string{filepath.Join(dir, "resolver.thrift"), out, "-only=Resolver", "--no-ipc"}, rsIncludeMappings...), "rust")
	if e := run(selected, &b); e != nil {
		t.Fatal(e)
	}
	generated, _ := os.ReadFile(filepath.Join(out, "rs/cross/resolver/rec.rs"))
	if strings.Contains(string(generated), "pub struct Query") || !strings.Contains(string(generated), "pub trait Resolver") || !strings.Contains(string(generated), "fixture_request::Request") {
		t.Fatal("selection lost imported closure or retained local record")
	}
}

const rsIncludeConsumer = `#[path = "rs/cross/resolver/rec.rs"]
mod r;
use r::Resolver;
use std::cell::Cell;
use std::io::Write;
use std::process::{Command, Stdio};

fn go(host: &str, mode: &str, input: &[u8]) -> Vec<u8> {
    let mut p = Command::new(host).arg(mode).stdin(Stdio::piped()).stdout(Stdio::piped()).spawn().unwrap();
    p.stdin.take().unwrap().write_all(input).unwrap();
    let out = p.wait_with_output().unwrap();
    assert!(out.status.success());
    out.stdout
}
struct Transport {
    host: String,
    calls: Cell<usize>,
}
impl r::FrameTransport for Transport {
    type Error = ();
    fn write_frame(&self, _: &[u8]) -> Result<(), ()> {
        Err(())
    }
    fn exchange_frame(&self, frame: &[u8]) -> Result<Vec<u8>, ()> {
        self.calls.set(self.calls.get() + 1);
        Ok(go(&self.host, "exchange", frame))
    }
}
fn reference(repo: &str, file: &str) -> fixture_model::Ref {
    let mut v = fixture_model::Ref::default();
    v.repo = repo.into();
    v.file = file.into();
    v
}
fn query() -> r::Query {
    let mut work = fixture_request::Request::default();
    work.artifact.digest = "sha256:00".into();
    let mut source = fixture_request::Source::default();
    source.scheme = "http".into();
    source.locator = "雪".into();
    work.sources = vec![source];
    r::Query { reference: reference("same", "weights.gguf"), work: Some(work), alternatives: vec![reference("second", "")] }
}
fn main() {
    let host = std::env::args().nth(1).unwrap();
    let v = query();
    let bytes = r::encode(&v);
    assert_eq!(r::encode_query_document(&v).unwrap(), bytes);
    assert_eq!(go(&host, "decode", &bytes), bytes, "Go re-encodes Rust bytes identically");
    assert_eq!(r::encode(&r::decode(&bytes).unwrap()), bytes);
    assert_eq!(r::encode(&r::decode_query_document(&bytes).unwrap()), bytes);
    let text = String::from_utf8(bytes.clone()).unwrap();
    let hostile = [
        text.replacen("\"repo\": \"same\"", "\"repo\": \"same\", \"future\": 1", 1),
        text.replacen("\"locator\": \"雪\"", "\"locator\": 7", 1),
        text.replacen("\"scheme\": \"http\",", "", 1),
        text.replacen("\"repo\": \"same\"", "\"repo\": \"same\", \"repo\": \"again\"", 1),
        text.replacen("\"alternatives\"", "\"future\": 1, \"alternatives\"", 1),
        text.replacen("\"digest\": \"sha256:00\"", "\"digest\": \"\\ud800\"", 1),
        format!("{text}x"),
    ];
    for input in &hostile {
        assert_ne!(input, &text);
        let want = String::from_utf8(go(&host, "decode", input.as_bytes())).unwrap();
        let seen = match r::decode(input.as_bytes()) {
            Ok(_) => "accepted".to_string(),
            Err(e) => format!("{} {}", e.word, e.offset),
        };
        assert_eq!(seen, want, "{input}");
    }
    let mut named = fixture_request::Source::default();
    named.scheme = "http".into();
    named.locator = "named".into();
    let round = fixture_request::decode_source_document(&fixture_request::encode_source_document(&named).unwrap()).unwrap();
    assert_eq!(round.locator, "named");
    assert!(fixture_model::internal::check_ref(&reference("x", "y"), 0, 64).is_ok());
    assert_eq!(fixture_model::internal::check_ref(&reference("x", "y"), 64, 64).unwrap_err().word, "depth_exceeded");
    let client = r::ResolverClient::new(Transport { host: host.clone(), calls: Cell::new(0) });
    let answer = client.resolve(reference("same", "weights.gguf")).unwrap();
    assert_eq!(answer.artifact.digest, "same");
    assert_eq!(answer.sources.len(), 1);
    assert_eq!(answer.sources[0].locator, "weights.gguf");
    assert_eq!(client.transport().calls.get(), 1);
    for (what, outcome) in [("decode", r::internal::decode_query_at(&bytes, 62, 64).map(|_| ())), ("check", r::internal::check_query(&v, 62, 64))] {
        match outcome {
            Err(e) if e.word == "depth_exceeded" => {}
            other => {
                eprintln!("depth reset in {what}: {other:?}");
                std::process::exit(1);
            }
        }
    }
    println!("PASS Rust typed includes: Go byte/refusal parity, generated Go dispatcher exchange, named dependency codecs, depth budget");
}
`

const rsIncludeMixed = `#[path = "rs/cross/resolver/rec.rs"]
mod r;
fn refused<T>(v: Result<T, r::Refusal>) -> bool {
    matches!(v, Err(e) if e.word == "depth_exceeded")
}
fn main() {
    let mut work = fixture_request::Request::default();
    let mut source = fixture_request::Source::default();
    source.scheme = "http".into();
    source.locator = "test".into();
    work.sources = vec![source];
    let mut v = r::Query { reference: Default::default(), work: Some(work), alternatives: vec![] };
    assert!(refused(r::encode_query_document(&v)), "named encode reset budget");
    assert!(std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| r::encode(&v))).is_err(), "default encode reset budget");
    let mut data = b"{\"ref\":".to_vec();
    data.extend(fixture_model::encode_ref_document(&v.reference).unwrap());
    data.extend_from_slice(b",\"work\":");
    data.extend(fixture_request::encode_request_document(v.work.as_ref().unwrap()).unwrap());
    data.extend_from_slice(b",\"alternatives\":[]}");
    assert!(fixture_request::decode_request_document(&fixture_request::encode_request_document(v.work.as_ref().unwrap()).unwrap()).is_ok());
    assert!(refused(r::decode(&data)), "decode reset budget");
    v.work.as_mut().unwrap().sources.clear();
    let bytes = r::encode_query_document(&v).unwrap();
    assert_eq!(r::encode(&r::decode(&bytes).unwrap()), bytes);
    println!("PASS mixed parent/child depth limits");
}
`

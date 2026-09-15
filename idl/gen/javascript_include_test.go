package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var jsIncludeMappings = []string{"--js-import=model=@fixture/model", "--js-import=request=@fixture/request"}

// Dependencies are separate packages resolved by specifier, as installed npm
// packages are; the importing module is generated beside their node_modules.
func generateJSIncludes(t *testing.T, dir, out string, extra ...string) {
	t.Helper()
	for name, ns := range map[string]string{"model": "abstraction/model/api", "request": "abstraction/download/request"} {
		root := filepath.Join(out, "node_modules", "@fixture", name)
		var report bytes.Buffer
		if e := run([]string{filepath.Join(dir, name+".thrift"), root, "--named-codecs", "javascript"}, &report); e != nil {
			t.Fatal(name, e)
		}
		writeNamespaceFile(t, root, "package.json", `{"name":"@fixture/`+name+`","type":"module","exports":"./js/`+ns+`/rec.mjs"}`)
	}
	var report bytes.Buffer
	args := append(append([]string{filepath.Join(dir, "resolver.thrift"), out}, jsIncludeMappings...), extra...)
	if e := run(append(args, "javascript"), &report); e != nil {
		t.Fatal(e)
	}
}

func sameTree(t *testing.T, a, b string) {
	t.Helper()
	count := 0
	filepath.Walk(a, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			t.Fatal(e)
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(a, p)
			x, _ := os.ReadFile(p)
			y, e := os.ReadFile(filepath.Join(b, rel))
			if e != nil || !bytes.Equal(x, y) {
				t.Fatal("nondeterministic", rel)
			}
			count++
		}
		return nil
	})
	if count != 5 {
		t.Fatalf("compared %d generated files", count)
	}
}

func runNode(t *testing.T, node, dir string, args ...string) ([]byte, error) {
	t.Helper()
	c := exec.Command(node, args...)
	c.Dir = dir
	return c.CombinedOutput()
}

func TestJavaScriptTypedIncludes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	dir, out := includeFixture(t)
	js := filepath.Join(out, "node")
	generateJSIncludes(t, dir, js)
	generateJSIncludes(t, dir, filepath.Join(dir, "again"))
	sameTree(t, js, filepath.Join(dir, "again"))
	codecPath := filepath.Join(js, "js/cross/resolver/rec.mjs")
	codec, _ := os.ReadFile(codecPath)
	for _, line := range []string{"import * as oa_dependency_model from \"@fixture/model\";\n", "import * as oa_dependency_request from \"@fixture/request\";\n"} {
		if !strings.Contains(string(codec), line) {
			t.Fatalf("generated module lacks %q", line)
		}
	}
	if strings.Contains(string(codec), "struct Ref") || strings.Contains(string(codec), "function decode_ref(") {
		t.Fatal("imported codec body copied into importing module")
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
	writeNamespaceFile(t, js, "consumer.mjs", jsIncludeConsumer)
	if b, e := runNode(t, node, js, "consumer.mjs", host); e != nil {
		t.Fatalf("JavaScript consumer: %v\n%s", e, b)
	} else {
		t.Log(strings.TrimSpace(string(b)))
	}

	// Mutation control: a bridge that resets the enclosing depth must fail the
	// consumer's exhaustion assertion while ordinary round trips still pass.
	mutated := strings.ReplaceAll(string(codec), "r.buf.subarray(r.pos), r.depth, r.limit", "r.buf.subarray(r.pos), 0, r.limit")
	if mutated == string(codec) {
		t.Fatal("depth mutation did not match bridge")
	}
	os.WriteFile(codecPath, []byte(mutated), 0600)
	if b, e := runNode(t, node, js, "consumer.mjs", host); e == nil || !strings.Contains(string(b), "depth reset") {
		t.Fatalf("depth mutation escaped regression: %v %s", e, b)
	}
	os.WriteFile(codecPath, codec, 0600)
}

func TestJavaScriptIncludeMixedDepth(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	dir, out := includeFixture(t)
	p := filepath.Join(dir, "resolver.thrift")
	raw, _ := os.ReadFile(p)
	src := string(raw[:bytes.Index(raw, []byte("service Resolver"))])
	src = strings.Replace(src, `depth_limit="64"`, `depth_limit="3"`, 1)
	writeNamespaceFile(t, dir, "resolver.thrift", src)
	generateJSIncludes(t, dir, out)
	writeNamespaceFile(t, out, "mixed.mjs", `import assert from 'node:assert/strict';
import * as r from './js/cross/resolver/rec.mjs';
import * as m from '@fixture/model';
import * as d from '@fixture/request';
const enc=new TextEncoder();
const refused=e=>e instanceof r.Refusal&&e.word==='depth_exceeded';
const v={...r.newQuery(),work:{...d.newRequest(),sources:[{...d.newSource(),scheme:'http',locator:'test'}]}};
assert.throws(()=>r.encode(v),refused,'default encode reset budget');
const cat=(...parts)=>Uint8Array.from(parts.flatMap(p=>[...(typeof p==='string'?enc.encode(p):p)]));
const data=cat('{"ref":',m.encodeRef(v.ref),',"work":',d.encodeRequest(v.work),',"alternatives":[]}');
assert.deepEqual(d.decodeRequest(d.encodeRequest(v.work)),v.work);
assert.throws(()=>r.decode(data),refused,'decode reset budget');
v.work.sources=[];
assert.deepEqual(r.decode(r.encode(v)),v);
console.log('PASS mixed parent/child depth limits');
`)
	if b, e := runNode(t, node, out, "mixed.mjs"); e != nil {
		t.Fatalf("mixed JavaScript: %v %s", e, b)
	}
}

func TestJavaScriptIncludeRefusals(t *testing.T) {
	resolver := func(t *testing.T) (string, string) {
		dir, out := includeFixture(t)
		return filepath.Join(dir, "resolver.thrift"), out
	}
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing", []string{"--js-import=model=@fixture/model"}, "--js-import=request"},
		{"unused", append([]string{"--js-import=other=@fixture/other"}, jsIncludeMappings...), "does not include"},
		{"ambiguous", []string{"--js-import=model=@fixture/same", "--js-import=request=@fixture/same"}, "ambiguous JavaScript module"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, out := resolver(t)
			var b bytes.Buffer
			e := run(append(append([]string{p, out}, tc.args...), "javascript"), &b)
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("got %v, want %s", e, tc.want)
			}
			if _, e := os.Stat(out); !os.IsNotExist(e) {
				t.Fatal("wrote partial output")
			}
		})
	}
	p, out := resolver(t)
	var b bytes.Buffer
	if e := run([]string{p, out, "--js-import=model=bad\"quote", "javascript"}, &b); e == nil {
		t.Fatal("accepted quoted module specifier")
	}
	data, _ := os.ReadFile(p)
	writeNamespaceFile(t, filepath.Dir(p), "resolver.thrift", string(data)+"\nstruct QueryAt {1: required string text}(unknown_fields=\"refuse\")\n")
	if e := run(append(append([]string{p, out}, jsIncludeMappings...), "javascript"), &b); e == nil || !strings.Contains(e.Error(), "encodeQueryAt") {
		t.Fatalf("accepted colliding named codec: %v", e)
	}
	p, out = resolver(t)
	selected := append(append([]string{p, out, "-only=Resolver", "--no-ipc"}, jsIncludeMappings...), "javascript")
	if e := run(selected, &b); e != nil {
		t.Fatal(e)
	}
	generated, _ := os.ReadFile(filepath.Join(out, "js/cross/resolver/rec.mjs"))
	if strings.Contains(string(generated), "newQuery") || !strings.Contains(string(generated), "export class Resolver") || !strings.Contains(string(generated), "@fixture/request") {
		t.Fatal("selection lost imported closure or retained local record")
	}
}

const jsIncludeGoHost = `package main
import("fmt";"io";"os";r "example.test/cross/resolver";m "example.test/abstraction/model/api";d "example.test/abstraction/download/request")
type provider struct{}
func(provider)Resolve(ref m.Ref)(d.Request,error){return d.Request{Artifact:d.Artifact{Digest:ref.Repo},Sources:[]d.Source{{Scheme:"http",Locator:ref.File}}},nil}
func main(){
 in,e:=io.ReadAll(os.Stdin);if e!=nil{panic(e)}
 switch os.Args[1]{
 case "exchange":dispatcher:=r.ResolverDispatcher{Handler:provider{}};reply,e:=dispatcher.ExchangeFrame(in);if e!=nil{panic(e)};os.Stdout.Write(reply)
 case "decode":v,e:=r.DecodeQuery(in);if x,ok:=e.(*r.Refusal);ok{fmt.Printf("%s %d",x.Word,x.Offset);return};if e!=nil{panic(e)};os.Stdout.Write(r.EncodeQuery(v))
 }
}
`

const jsIncludeConsumer = `import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import * as r from './js/cross/resolver/rec.mjs';
import * as m from '@fixture/model';
import * as d from '@fixture/request';
const host=process.argv[2];
const enc=new TextEncoder(),dec=new TextDecoder();
function go(mode,input){const p=spawnSync(host,[mode],{input});assert.equal(p.status,0,p.stderr.toString());return new Uint8Array(p.stdout);}
const ref={...m.newRef(),repo:'same',file:'weights.gguf'};
const work=d.newRequest();work.artifact.digest='sha256:00';work.sources=[{...d.newSource(),scheme:'http',locator:'雪'}];
const v={...r.newQuery(),ref,work,alternatives:[{...m.newRef(),repo:'second'}]};
const bytes=r.encode(v);
assert.deepEqual(r.encodeQuery(v),bytes);
assert.deepEqual(go('decode',bytes),bytes,'Go re-encodes JavaScript bytes identically');
assert.deepEqual(r.decode(bytes),v);
assert.deepEqual(r.decodeQuery(bytes),v);
const text=dec.decode(bytes);
const hostile=[
 text.replace('"repo": "same"','"repo": "same", "future": 1'),
 text.replace('"locator": "雪"','"locator": 7'),
 text.replace('"scheme": "http",',''),
 text.replace('"repo": "same"','"repo": "same", "repo": "again"'),
 text.replace('"alternatives"','"future": 1, "alternatives"'),
 text.replace('"digest": "sha256:00"','"digest": "\\ud800"'),
 text+'x',
];
for(const input of hostile){
 assert.notEqual(input,text);
 const b=enc.encode(input),want=dec.decode(go('decode',b));
 let seen='accepted';
 try{r.decode(b);}catch(e){assert.ok(e instanceof r.Refusal,String(e));seen=e.word+' '+e.offset;}
 assert.equal(seen,want,input);
}
assert.throws(()=>r.encode({...v,ref:{...ref,repo:17}}),e=>e instanceof r.Refusal&&e.word==='wrong_type');
assert.equal(d.decodeSource(d.encodeSource({...d.newSource(),scheme:'http',locator:'named'})).locator,'named');
let calls=0;
const c=new r.ResolverClient({exchangeFrame:async frame=>{calls++;return go('exchange',frame);}});
assert.deepEqual(await c.Resolve(ref),{...d.newRequest(),artifact:{...d.newArtifact(),digest:'same'},sources:[{...d.newSource(),scheme:'http',locator:'weights.gguf'}]});
assert.equal(calls,1);
for(const bad of [{...ref,repo:17},{...ref,repo:'\ud800'},null,{registry:''}]) await assert.rejects(c.Resolve(bad),e=>e instanceof r.Refusal,JSON.stringify(bad));
assert.equal(calls,1);
try{r.decodeQueryAt(bytes,62,64);throw new Error('accepted');}
catch(e){if(!(e instanceof r.Refusal)||e.word!=='depth_exceeded')throw new Error('depth reset: '+e);}
console.log('PASS JavaScript typed includes: Go byte/refusal parity, generated Go dispatcher exchange, imported value checks, named dependency codecs, depth budget');
`

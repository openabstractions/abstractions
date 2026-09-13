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

const includeRoot = `namespace * cross.resolver
include "model.thrift"
include "request.thrift"
struct Query {1: required model.Ref ref 2: optional request.Request work (omit="absent") 3: required list<model.Ref> alternatives}(document="true",unknown_fields="refuse")
service Resolver {request.Request Resolve(1: model.Ref ref)}(wire_name="test.model/resolver@1")
`

func includeFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	for _, pair := range [][2]string{{"model", "model.thrift"}, {"request", "request.thrift"}} {
		data, e := os.ReadFile("../test/includes/" + pair[1])
		if e != nil {
			t.Fatal(e)
		}
		writeNamespaceFile(t, dir, pair[0]+".thrift", string(data))
	}
	profile, e := os.ReadFile("../LANGUAGE.md")
	if e != nil {
		t.Fatal(e)
	}
	writeNamespaceFile(t, dir, "LANGUAGE.md", string(profile))
	model, _ := os.ReadFile(filepath.Join(dir, "model.thrift"))
	prefix := string(model)
	prefix = prefix[strings.Index(prefix, "encoding json"):strings.Index(prefix, "struct Ref")]
	writeNamespaceFile(t, dir, "resolver.thrift", prefix+includeRoot)
	return dir, filepath.Join(dir, "out")
}
func generateIncludes(t *testing.T, dir, out string) {
	t.Helper()
	for _, name := range []string{"model", "request", "resolver"} {
		args := []string{filepath.Join(dir, name+".thrift"), out, "--named-codecs", "go", "cpp", "python", "docs"}
		if name == "resolver" {
			args = append(args, "--go-import=model=example.test/abstraction/model/api", "--go-import=request=example.test/abstraction/download/request")
		}
		var report bytes.Buffer
		if e := run(args, &report); e != nil {
			t.Fatal(name, e)
		}
	}
}
func TestTypedIncludesOutsideConsumers(t *testing.T) {
	dir, out := includeFixture(t)
	generateIncludes(t, dir, out)
	second := filepath.Join(dir, "again")
	generateIncludes(t, dir, second)
	filepath.Walk(out, func(p string, info os.FileInfo, e error) error {
		if e != nil {
			t.Fatal(e)
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(out, p)
			a, _ := os.ReadFile(p)
			b, _ := os.ReadFile(filepath.Join(second, rel))
			if !bytes.Equal(a, b) {
				t.Fatal("nondeterministic", rel)
			}
		}
		return nil
	})
	writeNamespaceFile(t, out, "go/go.mod", "module example.test\n\ngo 1.23\n")
	writeNamespaceFile(t, out, "go/main.go", `package main
import(r "example.test/cross/resolver";m "example.test/abstraction/model/api";d "example.test/abstraction/download/request")
type provider struct{}
func(provider)Resolve(ref m.Ref)(d.Request,error){return d.Request{Artifact:d.Artifact{Digest:ref.Repo},Sources:[]d.Source{{Scheme:"http",Locator:"test"}}},nil}
type transport struct{d r.ResolverDispatcher}
func(t transport)ExchangeFrame(b []byte)([]byte,error){return t.d.ExchangeFrame(b)}
func main(){var _ r.Resolver=provider{};c:=r.NewResolverClient(transport{r.ResolverDispatcher{Handler:provider{}}});result,e:=c.Resolve(m.Ref{Repo:"service"});if e!=nil||result.Artifact.Digest!="service"{panic("service roundtrip")};if _,e=c.Resolve(m.Ref{Repo:string([]byte{255})});e==nil{panic("invalid outbound import")};if x,e:=d.DecodeSource(d.EncodeSource(&d.Source{Scheme:"http",Locator:"named"}));e!=nil||x.Locator!="named"{panic("named non-document")}; v:=r.Query{Ref:m.Ref{Repo:"same"},Alternatives:[]m.Ref{{Repo:"second"}}}; b:=r.EncodeQuery(&v);got,e:=r.DecodeQuery(b);if e!=nil||got.Ref.Repo!="same"||got.Alternatives[0].Repo!="second"{panic("roundtrip")};var imported *m.Ref=&got.Ref;_ = imported;if _,e=m.DecodeRef([]byte("{}"));e==nil{panic("missing fields accepted")};if _,_,e=r.DecodeQueryAt(b,62,64);e==nil{panic("depth reset")}}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = filepath.Join(out, "go")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("Go consumer: %v\n%s", e, b)
	}
	// Mutation control: resetting depth in the dependency bridge must fail the
	// consumer's exhaustion assertion, even though ordinary round trips work.
	codecPath := filepath.Join(out, "go/cross/resolver/rec.go")
	original, _ := os.ReadFile(codecPath)
	mutated := strings.ReplaceAll(string(original), "r.buf[r.pos:],r.depth,r.depthLimit()", "r.buf[r.pos:],0,r.depthLimit()")
	if mutated == string(original) {
		t.Fatal("depth mutation did not match bridge")
	}
	os.WriteFile(codecPath, []byte(mutated), 0600)
	mutationCmd := exec.Command("go", "run", ".")
	mutationCmd.Dir = cmd.Dir
	mutationCmd.Env = cmd.Env
	if b, e := mutationCmd.CombinedOutput(); e == nil || !strings.Contains(string(b), "depth reset") {
		t.Fatalf("depth mutation escaped regression: %v %s", e, b)
	}
	os.WriteFile(codecPath, original, 0600)
	writeNamespaceFile(t, out, "consumer.py", `from cross.resolver import rec as r
from abstraction.model.api import rec as m
from abstraction.download.request import rec as d
v=r.Query(ref=m.Ref(registry='',repo='same',revision='',quant='',file=''),alternatives=[])
v.alternatives=[v.ref]
got=r.decode_query(r.encode_query(v))
assert type(got.ref) is m.Ref and got.ref.repo=='same'
assert d.decode_source(d.encode_source(d.Source(scheme='http',locator='named'))).locator=='named'
class Provider(r.Resolver):
    def Resolve(self,ref):return d.Request(artifact=d.Artifact(digest=ref.repo),sources=[])
class Transport:
    def exchange_frame(self,frame):
        req=r._service_decode(r._decode_oaserviceframe,frame)
        args=r._service_decode(r._decode_oaresolverresolvearguments,req.arguments,1)
        result=r.OAResolverResolveResult(value=Provider().Resolve(args.ref))
        payload=r._service_encode(r.enc_oaresolverresolveresult,result,1)
        reply=r.OAServiceReply(version=1,service=req.service,method=req.method,ok=True,payload=payload)
        return r._service_encode(r.enc_oaservicereply,reply,0)
c=r.ResolverClient(Transport())
assert c.Resolve(v.ref).artifact.digest=='same'
try:c.Resolve(m.Ref(registry='',repo=17,revision='',quant='',file=''))
except r.Refusal:pass
else:raise AssertionError('invalid imported Python member')
try:r.decode_query_at(r.encode_query(v),62,64)
except Exception as e: assert 'depth_exceeded' in str(e)
else:raise AssertionError('depth reset')
`)
	cmd = exec.Command(servicePython(t), filepath.Join(out, "consumer.py"))
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(out, "py"))
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("Python consumer: %v\n%s", e, b)
	}
}
func TestTypedIncludeRefusals(t *testing.T) {
	for _, tc := range []struct{ name, edit, want string }{{"missing", "include \"absent.thrift\"", ""}, {"unresolved", "model.Ref", "model.Absent"}, {"ambiguous", "include \"model.thrift\"", "include \"model.thrift\"\ninclude \"other/model.thrift\""}, {"cycle", "include \"model.thrift\"", "include \"resolver.thrift\""}} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := includeFixture(t)
			text := head + includeRoot
			if tc.name == "missing" {
				text = tc.edit + "\n" + text
			} else {
				text = strings.Replace(text, tc.edit, tc.want, 1)
			}
			writeNamespaceFile(t, dir, "resolver.thrift", text)
			if _, e := loadDefinition(filepath.Join(dir, "resolver.thrift")); e == nil {
				t.Fatal("accepted invalid graph")
			}
		})
	}
	for _, lang := range []string{"rust", "javascript", "go"} {
		dir, out := includeFixture(t)
		var b bytes.Buffer
		if e := run([]string{filepath.Join(dir, "resolver.thrift"), out, lang}, &b); e == nil {
			t.Fatal("accepted unsupported backend/missing mapping", lang)
		}
		if _, e := os.Stat(out); !os.IsNotExist(e) {
			t.Fatal("wrote partial output")
		}
	}
}
func TestTypedIncludesCppConsumer(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Skip("C++ compiler unavailable")
	}
	dir, out := includeFixture(t)
	generateIncludes(t, dir, out)
	writeNamespaceFile(t, out, "consumer.cpp", `#include <cross/resolver/rec.h>
#include <type_traits>
namespace r=cross::resolver;namespace m=abstraction::model::api;namespace d=abstraction::download::request;
struct Provider:r::Resolver{d::Request Resolve(const m::Ref& ref) override {d::Request v;v.artifact.digest=ref.repo;return v;}};
int main(){static_assert(std::is_same<decltype(r::Query{}.ref),m::Ref>::value);r::Query v;v.ref.repo="same";v.alternatives.push_back(v.ref);auto bytes=r::encode_query(v);auto got=r::decode_query(std::string_view(bytes));if(got.ref.repo!="same"||got.alternatives.size()!=1)return 1;d::Source source;source.scheme="http";source.locator="named";if(d::decode_source(std::string_view(d::encode_source(source))).locator!="named")return 5;Provider p;auto work=p.Resolve(got.ref);if(work.artifact.digest!="same")return 2;try{std::size_t n=0;r::decode_query_at(bytes,62,64,n);return 3;}catch(const r::Refusal& e){if(std::string(e.word)!="depth_exceeded")return 4;}return 0;}
`)
	exe := filepath.Join(out, "consumer.exe")
	args := []string{"-std=c++17", "-Wall", "-Wextra", "-Werror", "-I" + filepath.Join(out, "cpp"), filepath.Join(out, "consumer.cpp"), "-o", exe}
	if name := strings.ToLower(filepath.Base(cxx)); name == "cl" || name == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/EHsc", "/I" + filepath.Join(out, "cpp"), filepath.Join(out, "consumer.cpp"), "/Fe:" + exe, "/Fo:" + filepath.Join(out, "consumer.obj")}
	}
	cmd := exec.Command(cxx, args...)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("C++ compile: %v\n%s", e, b)
	}
	if b, e := exec.Command(exe).CombinedOutput(); e != nil {
		t.Fatalf("C++ consumer: %v\n%s", e, b)
	}
}

func TestTypedIncludeSelectionAndPolicies(t *testing.T) {
	dir, out := includeFixture(t)
	// Imported Source preserves extensions under last-wins, while the root and
	// imported model.Ref retain their own unknown-field refusal policy.
	path := filepath.Join(dir, "request.thrift")
	raw, _ := os.ReadFile(path)
	source := strings.Replace(string(raw), `duplicate_keys = "refuse"`, `duplicate_keys = "last"`, 1)
	source = strings.Replace(source, `  6: duplicate_key (stage = "grammar")`, "", 1)
	for i := 7; i <= 10; i++ {
		source = strings.Replace(source, fmt.Sprintf("%d:", i), fmt.Sprintf("%d:", i-1), 1)
	}
	source = strings.Replace(source, `unknown_fields = "refuse", doc="A location`, `unknown_fields = "preserve", doc="A location`, 1)
	writeNamespaceFile(t, dir, "request.thrift", source)
	var refusal bytes.Buffer
	if err := run([]string{filepath.Join(dir, "resolver.thrift"), out, "--go-import=model=example.test/abstraction/model/api", "--go-import=request=example.test/abstraction/download/request", "go"}, &refusal); err == nil || !strings.Contains(err.Error(), "IPC envelope") {
		t.Fatalf("mixed IPC policy accepted: %v", err)
	}
	if err := run([]string{filepath.Join(dir, "resolver.thrift"), filepath.Join(dir, "direct"), "--no-ipc", "--go-import=model=example.test/abstraction/model/api", "--go-import=request=example.test/abstraction/download/request", "go"}, &refusal); err != nil {
		t.Fatal("direct mixed policies", err)
	}
	root, _ := os.ReadFile(filepath.Join(dir, "resolver.thrift"))
	writeNamespaceFile(t, dir, "resolver.thrift", string(root[:bytes.Index(root, []byte("service Resolver"))]))
	generateIncludes(t, dir, out)
	writeNamespaceFile(t, out, "go/go.mod", "module example.test\n\ngo 1.23\n")
	writeNamespaceFile(t, out, "go/main.go", `package main
import(r "example.test/cross/resolver";"strings")
func main(){input:=[]byte("{\"ref\":{\"registry\":\"\",\"repo\":\"same\",\"revision\":\"\",\"quant\":\"\",\"file\":\"\"},\"work\":{\"artifact\":{},\"sources\":[{\"scheme\":\"http\",\"locator\":\"test\",\"future\":1,\"future\":2}]},\"alternatives\":[]}")
v,e:=r.DecodeQuery(input);if e!=nil{panic(e)};if v.Work.Sources[0].Extras["future"]!="2"{panic("extension lost")};if _,e=r.DecodeQuery([]byte(strings.Replace(string(input),"\"registry\":", "\"future\":1,\"registry\":",1)));e==nil{panic("imported unknown policy weakened")};if _,e=r.DecodeQuery([]byte(strings.Replace(string(input),"\"alternatives\":", "\"future\":1,\"alternatives\":",1)));e==nil{panic("root unknown policy weakened")}}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = filepath.Join(out, "go")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("policy/service: %v\n%s", e, b)
	}
	dir, _ = includeFixture(t)
	var report bytes.Buffer
	args := []string{filepath.Join(dir, "resolver.thrift"), filepath.Join(dir, "selected"), "-only=Resolver", "--no-ipc", "--go-import=model=example.test/abstraction/model/api", "--go-import=request=example.test/abstraction/download/request", "go", "cpp", "python", "docs"}
	if e := run(args, &report); e != nil {
		t.Fatal(e)
	}
	generated, _ := os.ReadFile(filepath.Join(dir, "selected/go/cross/resolver/rec.go"))
	if strings.Contains(string(generated), "type Query struct") || !strings.Contains(string(generated), "type Resolver interface") {
		t.Fatal("selection lost imported closure or retained local record")
	}
}
func TestTypedIncludeGraphAndCodecCollisions(t *testing.T) {
	for _, change := range []string{"cycle", "codec", "namespace"} {
		dir, out := includeFixture(t)
		p := filepath.Join(dir, "resolver.thrift")
		data, _ := os.ReadFile(p)
		s := string(data)
		switch change {
		case "cycle":
			s = strings.Replace(s, "model.Ref ref", "Query ref", 1)
		case "codec":
			s += "\nstruct EncodeQuery {1: required string text}(unknown_fields=\"refuse\")\n"
		case "namespace":
			data, _ := os.ReadFile(filepath.Join(dir, "model.thrift"))
			writeNamespaceFile(t, dir, "other.thrift", string(data))
			s = "include \"other.thrift\"\n" + s
		}
		writeNamespaceFile(t, dir, "resolver.thrift", s)
		var report bytes.Buffer
		if e := run([]string{p, out, "docs"}, &report); e == nil {
			t.Fatal("accepted", change)
		}
	}
}
func TestIncludeLoaderReusesDiamondAndBoundsDepth(t *testing.T) {
	dir, _ := includeFixture(t)
	for _, n := range []string{"a", "b"} {
		writeNamespaceFile(t, dir, n+".thrift", head+"include \"model.thrift\"\n"+doc)
	}
	writeNamespaceFile(t, dir, "root.thrift", head+"include \"a.thrift\"\ninclude \"b.thrift\"\n"+doc)
	d, e := loadDefinition(filepath.Join(dir, "root.thrift"))
	if e != nil {
		t.Fatal(e)
	}
	if d.Imports[0].Def.Imports[0].Def != d.Imports[1].Def.Imports[0].Def {
		t.Fatal("diamond reparsed common definition")
	}
	for i := 0; i < 66; i++ {
		next := ""
		if i < 65 {
			next = fmt.Sprintf("include \"d%d.thrift\"\n", i+1)
		}
		writeNamespaceFile(t, dir, fmt.Sprintf("d%d.thrift", i), head+next+doc)
	}
	if _, e := loadDefinition(filepath.Join(dir, "d0.thrift")); e == nil || !strings.Contains(e.Error(), "depth exceeds") {
		t.Fatalf("unbounded include chain: %v", e)
	}
}
func TestIncludeLoaderCanonicalSymlinkCycle(t *testing.T) {
	dir, _ := includeFixture(t)
	if e := os.Symlink(dir, filepath.Join(dir, "alias")); e != nil {
		t.Skipf("directory symlink unavailable: %v", e)
	}
	writeNamespaceFile(t, dir, "cycle.thrift", head+"include \"alias/cycle.thrift\"\n"+doc)
	if _, e := loadDefinition(filepath.Join(dir, "cycle.thrift")); e == nil || !strings.Contains(e.Error(), "cyclic include") {
		t.Fatalf("symlink cycle: %v", e)
	}
}
func TestIncludeMixedDepthDefaultEncoding(t *testing.T) {
	dir, out := includeFixture(t)
	p := filepath.Join(dir, "resolver.thrift")
	raw, _ := os.ReadFile(p)
	src := string(raw[:bytes.Index(raw, []byte("service Resolver"))])
	src = strings.Replace(src, `depth_limit="64"`, `depth_limit="3"`, 1)
	writeNamespaceFile(t, dir, "resolver.thrift", src)
	generateIncludes(t, dir, out)
	writeNamespaceFile(t, out, "go/go.mod", "module example.test\n\ngo 1.23\n")
	writeNamespaceFile(t, out, "go/main.go", `package main
import(r "example.test/cross/resolver";m "example.test/abstraction/model/api";d "example.test/abstraction/download/request")
func rejected(v *r.Query)(yes bool){defer func(){yes=recover()!=nil}();r.Encode(v);return}
func main(){v:=r.Query{Work:&d.Request{Sources:[]d.Source{{Scheme:"http",Locator:"test"}}}};if !rejected(&v){panic("default encode reset budget")};in:=append([]byte("{\"ref\":"),m.EncodeRef(&v.Ref)...);in=append(in,[]byte(",\"work\":")...);in=append(in,d.EncodeRequest(v.Work)...);in=append(in,[]byte(",\"alternatives\":[]}")...);if _,e:=r.Decode(in);e==nil{panic("decode reset budget")};v.Work.Sources=nil;if _,e:=r.Decode(r.Encode(&v));e!=nil{panic(e)}}
`)
	cmd := exec.Command("go", "run", ".")
	cmd.Dir = filepath.Join(out, "go")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("mixed Go: %v %s", e, b)
	}
	writeNamespaceFile(t, out, "mixed.py", `from cross.resolver import rec as r
from abstraction.model.api import rec as m
from abstraction.download.request import rec as d
v=r.Query(work=d.Request(sources=[d.Source(scheme='http',locator='test')]))
try:r.encode(v)
except r.Refusal as e:assert e.word=='depth_exceeded'
else:raise AssertionError('default encode reset budget')
data=b'{"ref":'+m.encode(v.ref)+b',"work":'+d.encode(v.work)+b',"alternatives":[]}'
try:r.decode(data)
except r.Refusal as e:assert e.word=='depth_exceeded'
else:raise AssertionError('decode reset budget')
v.work.sources=[]
r.decode(r.encode(v))
`)
	cmd = exec.Command(servicePython(t), filepath.Join(out, "mixed.py"))
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(out, "py"))
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("mixed Python: %v %s", e, b)
	}
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Log("C++ mixed-depth consumer not measured: compiler unavailable")
		return
	}
	writeNamespaceFile(t, out, "mixed.cpp", `#include <cross/resolver/rec.h>
namespace r=cross::resolver;namespace m=abstraction::model::api;namespace d=abstraction::download::request;
int main(){r::Query v;v.work=d::Request{};v.work->sources.push_back(d::Source{"http","test"});try{r::encode(v);return 1;}catch(const r::Refusal&e){if(std::string(e.word)!="depth_exceeded")return 2;}auto data=std::string("{\"ref\":")+m::encode(v.ref)+",\"work\":"+d::encode(*v.work)+",\"alternatives\":[]}";try{r::decode(data);return 3;}catch(const r::Refusal&e){if(std::string(e.word)!="depth_exceeded")return 4;}v.work->sources.clear();r::decode(r::encode(v));return 0;}
`)
	exe := filepath.Join(out, "mixed.exe")
	args := []string{"-std=c++17", "-Wall", "-Wextra", "-Werror", "-I" + filepath.Join(out, "cpp"), filepath.Join(out, "mixed.cpp"), "-o", exe}
	if name := strings.ToLower(filepath.Base(cxx)); name == "cl" || name == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/EHsc", "/I" + filepath.Join(out, "cpp"), filepath.Join(out, "mixed.cpp"), "/Fe:" + exe, "/Fo:" + filepath.Join(out, "mixed.obj")}
	}
	if b, e := exec.Command(cxx, args...).CombinedOutput(); e != nil {
		t.Fatalf("mixed C++ compile: %v %s", e, b)
	}
	if b, e := exec.Command(exe).CombinedOutput(); e != nil {
		t.Fatalf("mixed C++: %v %s", e, b)
	}
}

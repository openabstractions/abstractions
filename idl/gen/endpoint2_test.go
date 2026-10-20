package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEndpointSchemaCanonical(t *testing.T) {
	if pythonSkipped != "" {
		t.Skip(pythonSkipped)
	}
	registration := productionFile(t, "abstraction-facade/registration.thrift")
	cmd := exec.Command(testPython, "endpoint_schema_generate.py", "--check", "--production-root", filepath.Dir(filepath.Dir(registration)))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	s, err := parse(endpointSchema)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := loadDefinition(registration)
	if err != nil {
		t.Fatal(err)
	}
	// The prefix changes implementation identifiers only. The canonical service
	// definition hash continues to come from the original parsed definition.
	if len(s.Structs) != 4 || len(actual.Services) != 2 {
		t.Fatal("endpoint closure changed", len(s.Structs), len(actual.Services))
	}
	for i := range s.Structs {
		s.Structs[i].Document = false
		if s.Structs[i].Name == "EndpointRefusalDetail" {
			s.Structs[i].CodecDepthLimit = endpointRefusalDepthLimit
		}
	}
	s.byName = map[string]*Struct{}
	for i := range s.Structs {
		s.byName[s.Structs[i].Name] = &s.Structs[i]
	}
	service := actual.Services[0]
	if service.WireName != "abstraction.facade/endpoint@2" {
		t.Fatal(service.WireName)
	}
	service.Methods = append([]Method(nil), service.Methods...)
	service.Methods[0].Result.Type = "EndpointDescription"
	derived := shapeOf(service, s)
	names := strings.NewReplacer(
		"EndpointDescriptionOutcome", "DescriptionOutcome", "EndpointDescription", "Description",
		"EndpointProducerCheckState", "ProducerCheckState", "EndpointProducerCheck", "ProducerCheck",
		"EndpointServiceState", "ServiceState", "EndpointServiceReadiness", "facade.ServiceReadiness",
		"EndpointRefusalDetail", "rights.RefusalDetail", "EndpointRefusalSource", "rights.RefusalSource",
		"EndpointRefusalDecision", "rights.RefusalDecision", "EndpointRefusalAsk", "rights.RefusalAsk",
	)
	for name, line := range derived.methods {
		derived.methods[name] = names.Replace(line)
	}
	decls := map[string]*shape{}
	for name, declaration := range derived.decls {
		declaration.header = names.Replace(declaration.header)
		for member, line := range declaration.lines {
			declaration.lines[member] = names.Replace(line)
		}
		decls[names.Replace(name)] = declaration
	}
	derived.decls = decls
	canonical := derived.canonical()
	// Normalizing private carrier names restores the exact canonical closure,
	// including Rights' refusal record and all independent member/value bounds.
	if expected := shapeOf(actual.Services[0], actual).canonical(); canonical != expected {
		t.Fatalf("derived endpoint canonical closure differs\nDERIVED\n%s\nCANONICAL\n%s", canonical, expected)
	}
	digest := sha256.Sum256([]byte(canonical))
	if "sha256:"+hex.EncodeToString(digest[:]) != definitionHashes(actual)[0][1] {
		t.Fatal("derived endpoint hash differs from gen --hash")
	}
}

func TestEndpointSchemaGeneratorCLIAdmission(t *testing.T) {
	if pythonSkipped != "" {
		t.Skip(pythonSkipped)
	}
	root := t.TempDir()
	script := filepath.Join(root, "endpoint_schema_generate.py")
	body, err := os.ReadFile("endpoint_schema_generate.py")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(script, body, 0600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "endpoint_schema_data.go")
	const sentinel = "unchanged carried output\n"
	if err := os.WriteFile(output, []byte(sentinel), 0600); err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"-h", "--help", "--unknown"} {
		cmd := exec.Command(testPython, script, option)
		out, err := cmd.CombinedOutput()
		if option == "--unknown" {
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 {
				t.Fatalf("%s: %v\n%s", option, err, out)
			}
		} else if err != nil || !strings.Contains(string(out), "--check") {
			t.Fatalf("%s: %v\n%s", option, err, out)
		}
		if actual, err := os.ReadFile(output); err != nil || string(actual) != sentinel {
			t.Fatalf("%s changed output: %v %q", option, err, actual)
		}
	}
}

func TestEndpointIntrinsicRefusalOwnership(t *testing.T) {
	low := regexp.MustCompile(`(?m)^.*(?:bad_timestamp|unknown_critical|not_a_subset|content_mismatch).*\n`).ReplaceAllString(head, "")
	id := 0
	low = regexp.MustCompile(`(?m)\b[0-9]+:`).ReplaceAllStringFunc(low, func(string) string { id++; return fmt.Sprintf("%d:", id) })
	declaration := `struct R {1:required string value}(document="true",unknown_fields="refuse") service S {R Read()}(wire_name="test/low@1")`
	def, err := parse(low + declaration)
	if err != nil {
		t.Fatal(err)
	}
	for _, backend := range backends {
		if backend.lang != "go" && backend.lang != "cpp" {
			continue
		}
		body := mustEmit(t, backend, def)
		if err := checkWords(emitted{def: def, lang: backend.lang, body: body, whole: body}); err != nil {
			t.Fatal(backend.lang, err)
		}
		if err := checkWords(emitted{def: def, lang: backend.lang, body: body + ` refuse("invented_refusal")`, whole: body}); err == nil || !strings.Contains(err.Error(), "invented_refusal") {
			t.Fatal("noncanonical word admitted", err)
		}
		for _, word := range []string{"bad_timestamp", "encoded_size"} {
			stage := "grammar"
			if word == "encoded_size" {
				stage = "structure"
			}
			declared := *def
			declared.Refusals = append(append([]Refusal(nil), def.Refusals...), Refusal{ID: len(def.Refusals) + 1, Word: word, Stage: stage})
			output := mustEmit(t, backend, &declared)
			if err := checkWords(emitted{def: &declared, lang: backend.lang, body: output, whole: output}); err == nil || !strings.Contains(err.Error(), "can never say") {
				t.Fatal("intrinsic codec hid unreachable target word", word, err)
			}
		}
	}
	for _, mode := range []string{"noipc", "record", "python"} {
		copy := *def
		lang := "go"
		switch mode {
		case "noipc":
			copy.NoIPC = true
		case "record":
			copy.Services = nil
		case "python":
			lang = "python"
		}
		if err := checkWords(emitted{def: &copy, lang: lang, body: `refuse("bad_timestamp")`, whole: ""}); err == nil || !strings.Contains(err.Error(), "does not declare") {
			t.Fatal("intrinsic vocabulary escaped owning output", mode, err)
		}
	}
}

func TestGeneratedEndpointV2Go(t *testing.T) {
	s, err := parse(head + `struct R {1:required string value}(document="true",unknown_fields="refuse")
service S {R Read()(remote="see",readonly="true")}(wire_name="test/endpoint@1")`)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(endpointV2GoTest, "@HASH@", definitionHashes(s)[0][1])
	dir := t.TempDir()
	for name, value := range map[string]string{"go.mod": "module endpoint.test\n\ngo 1.22\n", "rec.go": genGo(s), "rec_test.go": body} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

func TestGeneratedEndpointV2Cpp(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Fatal("focused endpoint C++ proof requires CXX or g++")
	}
	s, err := parse(head + `struct R {1:required string value}(document="true",unknown_fields="refuse")
service S {R Read()(remote="see",readonly="true")}(wire_name="test/endpoint@1")`)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	body := strings.NewReplacer("@HASH@", definitionHashes(s)[0][1], "@MANY@", strings.TrimSuffix(strings.Repeat("d,", 65), ","), "@LARGE@", strings.TrimSuffix(strings.Repeat("large,", 64), ",")).Replace(endpointV2CppTest)
	for name, value := range map[string]string{"rec.h": genCpp(s), "main.cpp": body} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	exe := filepath.Join(dir, "endpoint.exe")
	cmd := exec.Command(cxx, "-std=c++17", filepath.Join(dir, "main.cpp"), "-o", exe)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("C++ compile %v\n%s", err, out)
	}
	if out, err := exec.Command(exe).CombinedOutput(); err != nil {
		t.Fatalf("C++ endpoint %v\n%s", err, out)
	}
}

func TestGeneratedEndpointV2ProductionCpp(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		t.Fatal("focused endpoint C++ proof requires CXX or g++")
	}
	definition, err := filepath.Abs(productionFile(t, "abstraction-inference/inference.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	includeDirs := generateProductionCpp(t, s, definition, dir)
	writeNamespaceFile(t, dir, "main.cpp", `#include <abstraction/inference/api/rec.h>
namespace rec=abstraction::inference::api;
std::string describe(const rec::ChatDispatcher& dispatcher) {return rec::describe_endpoint(R"({"version":1,"service":"abstraction.facade/endpoint@2","method":"Describe","arguments":{}})","p","v",dispatcher);}
int main(){return 0;}`)
	args := []string{"-std=c++17", "-fsyntax-only", filepath.Join(dir, "main.cpp")}
	for _, include := range includeDirs {
		args = append(args, "-I"+include)
	}
	if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/Zs", filepath.Join(dir, "main.cpp")}
		for _, include := range includeDirs {
			args = append(args, "/I"+include)
		}
	}
	cmd := exec.Command(cxx, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("canonical inference C++ compile %v\n%s", err, out)
	}
	generatedGo := genGo(s)
	for _, row := range definitionHashes(s) {
		if !strings.Contains(generatedGo, `return "`+row[1]+`"`) {
			t.Fatal("canonical inference hash missing", row[0])
		}
	}
}

const endpointV2CppTest = `#include "rec.h"
#include <cassert>
struct H:rec::S{rec::R read()override{return {};}
std::pair<bool,std::string> ready(){return {false,"engine:loading"};}};
struct Large {const rec::SDispatcher& d;
auto describe_service()const{return d.describe_service();}
auto describe_service_definition_hash()const{return d.describe_service_definition_hash();}
std::pair<std::vector<std::string>,std::map<std::string,std::string>> describe_service_metadata()const{std::vector<std::string> g(64,std::string(128,'g'));std::map<std::string,std::string> c;for(int i=0;i<32;i++)c[std::string(1,char('A'+i))]=std::string(256,'v');return {g,c};}};
rec::EndpointDescription read(std::string_view frame){auto payload=rec::detail::service_response(frame,rec::kEndpointV2Contract,"Describe");rec::detail::Reader reader{payload};return rec::detail::decode_endpoint_description_result(reader).value;}
int main(){H h;rec::SDispatcher d(h);std::string request="{\"version\":1,\"service\":\"abstraction.facade/endpoint@2\",\"method\":\"Describe\",\"arguments\":{}}";
rec::EndpointProducerCheck check;check.kind="abstraction.provider.modelhost/engine";check.copy="release";check.state=rec::EndpointProducerCheckState::Current;check.checked="2026-11-02T00:00:00.000000Z";
auto reply=rec::describe_endpoint_with_checks(request,"p","v",{check},d);auto value=read(reply);assert(value.outcome==rec::EndpointDescriptionOutcome::Described);assert(value.checks.size()==1);assert(value.services.size()==1);assert(value.services[0].definition_hash=="@HASH@");assert(value.services[0].readiness==rec::EndpointServiceReadiness::NotReady);assert(value.services[0].reason=="engine:loading");
assert(read(d.exchange_frame(request)).checks.empty());
auto old=request;old.replace(old.find("endpoint@2"),10,"endpoint@1");assert(rec::describe_endpoint(old,"p","v",d)==rec::describe_endpoint_with_checks(old,"p","v",{check},d));
auto published=rec::detail::service_reply(rec::detail::service_payload(old),R"({"value":{"outcome":"described","program":"p","version":"v","services":[{"contract":"test/endpoint@1","readiness":"not_ready","why":"engine:loading","guarantees":[],"capabilities":{}}]}})");assert(rec::describe_endpoint(old,"p","v",d)==published);
value=read(rec::describe_endpoint(request,"p","v",@MANY@));assert(value.outcome==rec::EndpointDescriptionOutcome::Unavailable);assert(value.reason=="bound:reply_bytes");assert(value.services.empty());assert(value.program.empty());assert(value.checks.empty());
Large large{d};value=read(rec::describe_endpoint(request,"p","v",@LARGE@));assert(value.outcome==rec::EndpointDescriptionOutcome::Unavailable);assert(value.reason=="bound:reply_bytes");
for(int count:{62,63}){std::string unknown(count,'[');unknown+="0";unknown+=std::string(count,']');std::string parent(100,'[');parent+="0";parent+=std::string(100,']');auto document=std::string(R"({"outcome":"forbidden","program":"","version":"","services":[],"refusal":{"source":"rights","decision":"denied","ask":"none","future":)")+unknown+"},\"reason\":\"\",\"parent\":"+parent+"}";rec::detail::Reader reader{document};bool refused=false;try{rec::detail::decode_endpoint_description(reader);assert(reader.limit==0&&reader.ceiling==0);}catch(const rec::Refusal&e){assert(e.word=="depth_exceeded");refused=true;}assert(refused==(count==63));}
}
`

const endpointV2GoTest = `package rec
import("bytes";"encoding/json";"strings";"testing")
type h struct{}
func(h)Read()(R,error){return R{},nil}
func(h)Ready()(bool,string){return false,"engine:loading"}
func req(contract string)[]byte{return encOAServiceFrame(nil,&oaServiceFrame{Version:1,Service:contract,Method:"Describe",Arguments:Raw("{}")},0)}
func read(t *testing.T,b []byte)EndpointDescription{t.Helper();p,e:=serviceResponse(b,EndpointV2Contract,"Describe");if e!=nil{t.Fatal(e)};r:=reader{buf:[]byte(p)};v,e:=r.decodeEndpointDescriptionResult();if e!=nil{t.Fatal(e,string(p))};return v.Value}
func TestDescription(t *testing.T){d:=&SDispatcher{Handler:h{}};checks:=[]EndpointProducerCheck{{Kind:"abstraction.provider.modelhost/engine",Copy:"release",State:EndpointProducerCheckStateCurrent,Checked:"2026-11-02T00:00:00.000000Z"}}
 b,e:=ServeEndpointWithChecks(req(EndpointV2Contract),"p","v",checks,d);if e!=nil{t.Fatal(e)};v:=read(t,b)
 if v.Outcome!=EndpointDescriptionOutcomeDescribed||len(v.Services)!=1||len(v.Checks)!=1||v.Checks[0].State!=EndpointProducerCheckStateCurrent{t.Fatal(v)}
 if v.Services[0].Service!="test/endpoint@1"||v.Services[0].Readiness!=EndpointServiceReadinessNotReady||v.Services[0].Reason!="engine:loading"||v.Services[0].DefinitionHash!="@HASH@"{t.Fatal(v.Services)}
 b,e=d.ExchangeFrame(req(EndpointV2Contract));if e!=nil{t.Fatal(e)};if v=read(t,b);len(v.Checks)!=0{t.Fatal(v)}
 old,e:=ServeEndpoint(req(EndpointContract),"p","v",d);if e!=nil{t.Fatal(e)};again,e:=ServeEndpointWithChecks(req(EndpointContract),"p","v",checks,d);if e!=nil||!bytes.Equal(old,again){t.Fatal("endpoint1 bytes changed",e)}
 p,e:=serviceResponse(old,EndpointContract,"Describe");if e!=nil{t.Fatal(e)};var compact bytes.Buffer;if e=json.Compact(&compact,[]byte(p));e!=nil{t.Fatal(e)}
 if compact.String()!=` + "`" + `{"value":{"outcome":"described","program":"p","version":"v","services":[{"contract":"test/endpoint@1","readiness":"not_ready","why":"engine:loading","guarantees":[],"capabilities":{}}]}}` + "`" + `{t.Fatal(compact.String())}
 want,e:=serviceReply(&oaServiceFrame{Version:1,Service:EndpointContract,Method:"Describe"},Raw(compact.String()),nil);if e!=nil||!bytes.Equal(old,want){t.Fatal("published endpoint1 bytes differ",e)}
}
type large struct{*SDispatcher;n int}
func(s large)DescribeServiceMetadata()([]string,map[string]string){g:=make([]string,64);for i:=range g{g[i]=strings.Repeat("g",128)};m:=map[string]string{};for i:=0;i<32;i++{m[string(rune('A'+i))]=strings.Repeat("v",s.n)};return g,m}
func TestBounds(t *testing.T){d:=&SDispatcher{Handler:h{}};services:=make([]ServedService,65);for i:=range services{services[i]=d};b,e:=ServeEndpoint(req(EndpointV2Contract),"p","v",services...);if e!=nil{t.Fatal(e)};v:=read(t,b);if v.Outcome!=EndpointDescriptionOutcomeUnavailable||v.Reason!="bound:reply_bytes"||len(v.Services)!=0||len(v.Checks)!=0||v.Program!=""{t.Fatal(v)}
 services=services[:64];for i:=range services{services[i]=large{d,256}};b,e=ServeEndpoint(req(EndpointV2Contract),"p","v",services...);if e!=nil{t.Fatal(e)};v=read(t,b);if v.Outcome!=EndpointDescriptionOutcomeUnavailable||v.Reason!="bound:reply_bytes"{t.Fatal(v)}
}
func TestIndependentWholeFrameBound(t *testing.T){
 d:=&SDispatcher{Handler:h{}}
 candidate:=func(n,last int)(services []ServedService,document []byte,frame []byte){
  defer func(){if p:=recover();p!=nil{if e,ok:=p.(*Refusal);ok&&e.Word=="encoded_size"{document=nil;frame=make([]byte,1048577);return};panic(p)}}()
  services=make([]ServedService,64);description:=EndpointDescription{Outcome:EndpointDescriptionOutcomeDescribed,Program:"p",Version:"v"}
  for i:=range services{size:=n;if i==63{size=last};s:=large{d,size};services[i]=s;g,c:=s.DescribeServiceMetadata();description.Services=append(description.Services,EndpointServiceState{Service:"test/endpoint@1",Readiness:EndpointServiceReadinessNotReady,Reason:"engine:loading",Guarantees:g,Capabilities:c,DefinitionHash:"@HASH@"})}
  document=encEndpointDescription(nil,&description,2);payload:=encEndpointDescriptionResult(nil,&EndpointDescriptionResult{Value:description},1);request,_:=servicePayload(req(EndpointV2Contract));var e error;frame,e=serviceReply(request,Raw(payload),nil);if e!=nil{t.Fatal(e)};return services,document,frame
 }
 low,high:=0,200;for low<high{middle:=(low+high)/2;_,_,frame:=candidate(middle,middle);if len(frame)>1048576{high=middle}else{low=middle+1}}
 for extra:=0;extra<=64;extra++{services,document,frame:=candidate(low-1,low-1+extra);if len(frame)<=1048576||document==nil{continue};var compact bytes.Buffer;if e:=json.Compact(&compact,document);e!=nil{t.Fatal(e)};if len(document)>1048576||compact.Len()>1048576{t.Fatal("fixture failed to isolate whole-frame bound",len(document),compact.Len(),len(frame))};reply,e:=ServeEndpoint(req(EndpointV2Contract),"p","v",services...);if e!=nil{t.Fatal(e)};v:=read(t,reply);if v.Outcome!=EndpointDescriptionOutcomeUnavailable||v.Reason!="bound:reply_bytes"{t.Fatal("physical reply escaped",v.Outcome,len(reply))};return};t.Fatal("no whole-frame fixture")
}
func TestRightsOwnerDepthInsideDescription(t *testing.T){
 for _,n:=range []int{62,63}{unknown:=strings.Repeat("[",n)+"0"+strings.Repeat("]",n);parent:=strings.Repeat("[",100)+"0"+strings.Repeat("]",100)
 document:=` + "`" + `{"outcome":"forbidden","program":"","version":"","services":[],"refusal":{"source":"rights","decision":"denied","ask":"none","future":` + "`" + `+unknown+` + "`" + `},"reason":"","parent":` + "`" + `+parent+"}"
 r:=reader{buf:[]byte(document)};_,e:=r.decodeEndpointDescription();if n==62{if e!=nil||r.limit!=0{t.Fatal("owner scope failed",e,r.limit)}}else{refusal,ok:=e.(*Refusal);if !ok||refusal.Word!="depth_exceeded"{t.Fatal("Rights depth64 escaped",e)}}
 }
}
`

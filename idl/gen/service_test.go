package main

import (
	"bytes"
	"go/ast"
	goparser "go/parser"
	gotoken "go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteCanonicalReceiptAndKeywordPrefix(t *testing.T) {
	prefix, err := parse(head + `struct onewayRecord {1:required string value}(document="true",unknown_fields="refuse")
service S {onewayRecord Read()(remote="call") oneway void Send(1:onewayRecord request)(remote="call")}(wire_name="test/prefix@1")`)
	if err != nil {
		t.Fatal(err)
	}
	if prefix.Services[0].Methods[0].Result.Type != "onewayRecord" || prefix.Services[0].Methods[1].Result.Type != "void" {
		t.Fatal(prefix.Services[0].Methods)
	}
	actual, err := os.ReadFile(productionFile(t, "abstraction-job/acceptance.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "acceptance.thrift"), actual, 0600); err != nil {
		t.Fatal(err)
	}
	// Reuse the existing imported-record fixture's plain-record grammar.
	grammar, err := os.ReadFile("../test/includes/model.thrift")
	if err != nil {
		t.Fatal(err)
	}
	plain := string(grammar)
	plain = plain[strings.Index(plain, "encoding json"):strings.Index(plain, "struct Ref")]
	source := "include \"acceptance.thrift\"\n" + plain + `struct Result {1:required acceptance.Receipt receipt}(document="true",unknown_fields="refuse")
service S {Result Submit(1:string key)(remote="delegate",idempotency_key="key")}(wire_name="test/receipt@1")`
	path := filepath.Join(dir, "main.thrift")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{path, filepath.Join(dir, "out"), "go", "--go-import=acceptance=github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
}

const serviceFixture = `struct Record {1: required string value} (document="true", unknown_fields="grant")
service Events { oneway void Write(1: Record record,2: string text,3: bool enabled,4: i64 count) oneway void Ping() oneway void Opaque(1: json payload) } (wire_name="example.events/events@1")
`

func TestGeneratedRemoteEndpointAdmission(t *testing.T) {
	source := head + `struct Record {1:required string value}(document="true",unknown_fields="refuse")
service Query {Record Read()(readonly="true",remote="see") Record Call()(remote="call") Record Local() Record Person()(remote="person") oneway void Send()(remote="call")}(wire_name="test/query@1")
service One {oneway void Ping()(remote="call")}(wire_name="test/one@1")`
	def, err := parse(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, declaration := range []string{`struct ServeEndpointRemote {1:required bool value}(unknown_fields="refuse")`, `service ServeEndpointRemote {Record Read()}(wire_name="test/collision@1")`} {
		if _, err := parse(source + declaration); err == nil {
			t.Fatal("remote helper name collision accepted", declaration)
		}
	}
	actual, err := loadDefinition(productionFile(t, "abstraction-facade/facade.thrift"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "facade"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"go.mod": "module remote.test\n\ngo 1.22\n", "rec.go": genGo(def), "facade/rec.go": genGo(actual), "rec_test.go": remoteEndpointGoTest} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	aliases, err := goExportAliases(genGo(actual), "remote.test/facade")
	if err != nil || !strings.Contains(aliases, "func ServeEndpointRemote(") {
		t.Fatal("missing alias", err)
	}
	command := exec.Command("go", "test", ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

const remoteEndpointGoTest = `package rec
import("errors";"testing";facade "remote.test/facade")
func serviceRequest(contract,method string,arguments Raw)[]byte{return encOAServiceFrame(nil,&oaServiceFrame{Version:1,Service:contract,Method:method,Arguments:arguments},0)}
type handler struct{calls int;admitted *bool}
func(h *handler)Read()(Record,error){if h.admitted!=nil&&!*h.admitted{panic("handler before admission")};h.calls++;return Record{Value:"ok"},nil}
func(h *handler)Call()(Record,error){return h.Read()}
func(h *handler)Local()(Record,error){return h.Read()}
func(h *handler)Person()(Record,error){return h.Read()}
func(h *handler)Send()error{h.calls++;return nil}
func(h *handler)Ping()error{h.calls++;return nil}
type transport func([]byte)([]byte,error)
func(f transport)ExchangeFrame(b []byte)([]byte,error){return f(b)}
func(f transport)WriteFrame([]byte)error{return DispatchError("wrong_mode")}
type legacy struct{contract string;calls *int}
func(l legacy)ServiceContract()string{return l.contract}
func(l legacy)DescribeService()(string,bool,string){return l.contract,true,""}
func(l legacy)ExchangeFrame([]byte)([]byte,error){*l.calls++;panic("legacy handler reached")}
type unknown struct{*QueryDispatcher}
func(unknown)RemoteMethodProfile(string,string)(string,bool){return "unknown",true}
func errorCode(t *testing.T,err error,code,message string){t.Helper();var se *ServiceError;if !errors.As(err,&se)||string(se.Code)!=code||se.Message!=message{t.Fatalf("want %s/%s, got %#v",code,message,err)}}
func TestRemoteAdmission(t *testing.T){
 admitted:=false;h:=&handler{admitted:&admitted};d:=&QueryDispatcher{Handler:h};calls:=0
 allow:=func(contract,method,profile string)error{if contract!="test/query@1"||(method!="Read"&&method!="Call")||(profile!="see"&&profile!="call"){t.Fatal(contract,method,profile)};calls++;admitted=true;return nil}
 c:=NewQueryClient(transport(func(b []byte)([]byte,error){return ServeEndpointRemote(b,"p","v",allow,d)}))
 if v,e:=c.Read();e!=nil||v.Value!="ok"{t.Fatal(v,e)};admitted=false;if _,e:=c.Call();e!=nil{t.Fatal(e)};if calls!=2||h.calls!=2{t.Fatal(calls,h.calls)}
 denied:=NewQueryClient(transport(func(b []byte)([]byte,error){return ServeEndpointRemote(b,"p","v",func(string,string,string)error{return &ServiceError{Code:"denied",Message:"no"}},d)}));_,deniedError:=denied.Read();errorCode(t,deniedError,"denied","no");if h.calls!=2{t.Fatal("denied client reached handler")}
 for _,tc:=range []struct{name,code,message string;admit func(string,string,string)error}{{"Read","forbidden","remote admission required",nil},{"Local","unsupported","remote:local_only",allow},{"Person","forbidden","remote:person_only",allow},{"Missing","unsupported","remote:local_only",allow},{"Read","denied","no",func(string,string,string)error{return &ServiceError{Code:"denied",Message:"no"}}},{"Read","handler_error","handler failed",func(string,string,string)error{panic("private")}}}{
  before:=h.calls;b,e:=ServeEndpointRemote(serviceRequest("test/query@1",tc.name,Raw("{}")),"p","v",tc.admit,d);if e!=nil{t.Fatal(e)};_,e=serviceResponse(b,"test/query@1",tc.name);errorCode(t,e,tc.code,tc.message);if h.calls!=before{t.Fatal("refused handler called")}
 }
 before:=h.calls
 for _,service:=range []ServedService{legacy{"test/query@1",&h.calls},unknown{d}}{b,e:=ServeEndpointRemote(serviceRequest("test/query@1","Read",Raw("{}")),"p","v",allow,service);if e!=nil{t.Fatal(e)};_,e=serviceResponse(b,"test/query@1","Read");errorCode(t,e,"unsupported","remote:local_only")};if h.calls!=before{t.Fatal("legacy/unknown called")}
 noAdmission:=func(string,string,string)error{t.Fatal("unexpected admission");return nil}
 for _,frame:=range [][]byte{[]byte("{}"),append(serviceRequest("test/query@1","Read",Raw("{}")),[]byte("{}")...)}{if _,e:=ServeEndpointRemote(frame,"p","v",noAdmission,d);e==nil{t.Fatal("malformed frame accepted")}}
 for _,tc:=range []struct{contract,method,code string;services []ServedService}{{"absent","Read","unknown_service",[]ServedService{d}},{"test/one@1","Ping","wrong_mode",[]ServedService{&OneDispatcher{Handler:h}}}}{b,e:=ServeEndpointRemote(serviceRequest(tc.contract,tc.method,Raw("{}")),"p","v",noAdmission,tc.services...);if e!=nil{t.Fatal(e)};_,e=serviceResponse(b,tc.contract,tc.method);errorCode(t,e,tc.code,"")}
 // The existing local helper still routes a local method without admission.
 c=NewQueryClient(transport(func(b []byte)([]byte,error){return ServeEndpoint(b,"p","v",d)}));admitted=true;if _,e:=c.Local();e!=nil{t.Fatal(e)}
}
func TestDescribeUsesActualEndpointMetadata(t *testing.T){
 h:=&handler{};d:=&QueryDispatcher{Handler:h};calls:=0
 allow:=func(contract,method,profile string)error{if contract!=EndpointContract||method!="Describe"||profile!="see"{t.Fatal(contract,method,profile)};calls++;return nil}
 // Core facade owns the actual Endpoint.Describe definition.
 c:=facade.NewEndpointClient(transport(func(b []byte)([]byte,error){return facade.ServeEndpointRemote(b,"actual","v1",allow,d)}))
 description,e:=c.Describe();if e!=nil||description.Program!="actual"||calls!=1{t.Fatal(description,e,calls)}
 // Another package requires an explicit metadata-bearing Endpoint service.
 c=facade.NewEndpointClient(transport(func(b []byte)([]byte,error){return ServeEndpointRemote(b,"explicit","v1",allow,d,&facade.EndpointDispatcher{})}))
 description,e=c.Describe();if e!=nil||description.Program!="explicit"||calls!=2{t.Fatal(description,e,calls)}
 c=facade.NewEndpointClient(transport(func(b []byte)([]byte,error){return ServeEndpointRemote(b,"missing","v1",allow,d)}));_,e=c.Describe();var se *facade.ServiceError;if !errors.As(e,&se)||se.Code!="unsupported"||se.Message!="remote:local_only"{t.Fatal(e)}
 // An explicit legacy Endpoint cannot inherit the core package's default.
 c=facade.NewEndpointClient(transport(func(b []byte)([]byte,error){return facade.ServeEndpointRemote(b,"legacy","v1",allow,legacy{EndpointContract,&h.calls})}));_,e=c.Describe();if !errors.As(e,&se)||se.Code!="unsupported"||se.Message!="remote:local_only"{t.Fatal(e)}
 if calls!=2||h.calls!=0{t.Fatal(calls,h.calls)}
}
`

func TestRemoteAnnotationValuesAndTraitAgreement(t *testing.T) {
	source := head + `struct Box {1: required string value}(document="true",unknown_fields="grant")
service S {Box Read()(readonly="true") Box Local()(remote="local") Box Call()(remote="call")}(wire_name="test/s@1",remote="see")`
	def, err := parse(source)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"see", "local", "call"} {
		if def.Services[0].Methods[i].Remote != want {
			t.Fatal(def.Services[0].Methods)
		}
	}
	for _, replacement := range []string{`remote=""`, `remote="unknown"`, `remote="see",remote="local"`} {
		if _, err := parse(strings.Replace(source, `remote="call"`, replacement, 1)); err == nil {
			t.Fatal("accepted", replacement)
		}
	}
	for _, replacement := range []string{`remote="see"`, `remote="delegate"`} {
		if _, err := parse(strings.Replace(source, `remote="call"`, replacement, 1)); err == nil {
			t.Fatal("trait disagreement", replacement)
		}
	}
	for _, marker := range []string{`secret="false"`, `secret=""`, `local=""`, `local="host"`, `local="pid",local="path"`} {
		if _, err := parse(strings.Replace(source, `string value`, `string value(`+marker+`)`, 1)); err == nil {
			t.Fatal("accepted field", marker)
		}
	}
	for _, marker := range []string{`secret="true"`, `local="path"`, `local="pid"`, `local="handle"`} {
		if _, err := parse(strings.Replace(source, `string value`, `string value(`+marker+`)`, 1)); err != nil {
			t.Fatal(marker, err)
		}
	}
	for _, name := range []string{"RemoteMethodProfile", "remote_method_profile"} {
		if _, err := parse(strings.Replace(source, `Box Call()`, `Box `+name+`()`, 1)); err == nil {
			t.Fatal("collision accepted", name)
		}
	}
}

func TestDispatcherTableCarriesRemoteProfile(t *testing.T) {
	source := head + `struct Box {1:required string value}(document="true",unknown_fields="grant")
service S { Box Read()(readonly="true",remote="see") Box Call()(remote="call") Box Local() }(wire_name="test/s@1")`
	def, err := parse(source)
	if err != nil {
		t.Fatal(err)
	}
	output := genGo(def)
	if !strings.Contains(output, "if contract != d.ServiceContract()") {
		t.Fatal("profile lookup must reuse its dispatcher contract owner")
	}
	if strings.Contains(output, "if contract != \"test/s@1\"") {
		t.Fatal("profile lookup repeats the service wire name")
	}
	file, err := goparser.ParseFile(gotoken.NewFileSet(), "generated.go", output, 0)
	if err != nil {
		t.Fatal(err)
	}
	var required []string
	for _, decl := range file.Decls {
		if group, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range group.Specs {
				if kind, ok := spec.(*ast.TypeSpec); ok && kind.Name.Name == "ServedService" {
					for _, field := range kind.Type.(*ast.InterfaceType).Methods.List {
						if len(field.Names) == 0 {
							required = append(required, field.Type.(*ast.Ident).Name)
						} else {
							required = append(required, field.Names[0].Name)
						}
					}
				}
			}
		}
	}
	if strings.Join(required, ",") != "DescribedService,ServiceContract" {
		t.Fatal("required interface changed", required)
	}
	aliases, err := goExportAliases(output, "example.test/canonical")
	if err != nil || !strings.Contains(aliases, "func RemoteMethodProfile(") {
		t.Fatal("alias lookup missing", err)
	}
	dir := t.TempDir()
	test := `package rec
import "testing"
type oldService struct{}
func(oldService)DescribeService()(string,bool,string){return "old",true,""}
func(oldService)ServiceContract()string{return "old"}
var _ ServedService=oldService{}
type unknown struct{}
func(unknown)RemoteMethodProfile(string,string)(string,bool){return "unrecognized",true}
func TestLookup(t *testing.T){d:=&SDispatcher{};for _,tc:=range []struct{contract,method,want string}{{"test/s@1","Read","see"},{"test/s@1","Call","call"},{"test/s@1","Local","local"},{EndpointContract,"Describe","see"},{"wrong","Read","local"},{"test/s@1","missing","local"}}{if got:=RemoteMethodProfile(d,tc.contract,tc.method);got!=tc.want{t.Fatal(tc,got)}};if RemoteMethodProfile(oldService{},"old","Read")!="local"||RemoteMethodProfile(unknown{},"x","y")!="local"{t.Fatal("missing/unknown metadata escaped local")}}
func TestNilDispatcherContract(t *testing.T){var d *SDispatcher; if profile,ok:=d.RemoteMethodProfile(d.ServiceContract(),"Read");!ok||profile!="see"{t.Fatal(profile,ok)};if profile,ok:=d.RemoteMethodProfile("foreign","Read");ok||profile!="local"{t.Fatal(profile,ok)}}
`
	for name, body := range map[string]string{"go.mod": "module profile.test\n\ngo 1.22\n", "rec.go": output, "rec_test.go": test} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command("go", "test", ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}

func TestRemoteMetadataKeepsWireHashes(t *testing.T) {
	source := head + `struct Box {1:required string value}(document="true",unknown_fields="refuse") service S {Box Read(1:Box box)}(wire_name="test/s@1")`
	before, err := parse(source)
	if err != nil {
		t.Fatal(err)
	}
	afterSource := strings.Replace(source, "string value", `string value(secret="true",local="path")`, 1)
	afterSource = strings.Replace(afterSource, "Box box)", `Box box)(remote="call")`, 1)
	after, err := parse(afterSource)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := definitionHashes(before), definitionHashes(after); len(a) != 1 || len(b) != 1 || a[0] != b[0] {
		t.Fatal("metadata changed wire hash", a, b)
	}
	if problems := compatibility(before, after); len(problems) != 0 {
		t.Fatal(problems)
	}
}

func TestServiceParse(t *testing.T) {
	s, e := parse(head + serviceFixture)
	if e != nil {
		t.Fatal(e)
	}
	prefix := head + `struct Record {1: required string value}(document="true",unknown_fields="grant") service S {`
	suffix := `} (wire_name="example.test/s@1")`
	if _, e := parse(prefix + "oneway void Ping()" + suffix); e != nil {
		t.Fatal("control", e)
	}
	for _, tc := range []struct{ input, want string }{
		{"oneway void delete()", "colliding method"},
		{"oneway void Ping(1:string value(cpp.name=\"delete\"))", "invalid C++ service argument"},
		{"oneway i64 Ping()", "oneway methods require void"},
		{"oneway void Ping(1: optional string text)", "optional service"},
		{"oneway void Ping(1: string x,1: string y)", "duplicate argument"},
		{"oneway void Ping() oneway void Ping()", "colliding method"},
		{"oneway void Ping() (docs=\"typo\")", "unknown method annotation"},
		{"oneway void putA() oneway void puta()", "name collision"},
		{"oneway void Ping(1: string x (cpp.name=\"a\"),2: string y (cpp.name=\"a\"))", "duplicate argument"},
	} {
		_, e := parse(prefix + tc.input + suffix)
		if e == nil || !strings.Contains(e.Error(), tc.want) {
			t.Fatalf("%s: want %s, got %v", tc.input, tc.want, e)
		}
	}
	for _, lang := range []string{"javascript"} {
		if err := validateServiceBackend(s, lang); err != nil {
			t.Fatal("supported service refused", lang, err)
		}
	}
	if !strings.Contains(genJS(s), "export class EventsClient") {
		t.Fatal("JavaScript service omitted")
	}
	if _, e = selected(s, []string{"Events"}); e == nil {
		t.Fatal("service selected without argument record")
	}
	if _, e = selected(s, []string{"Events", "Record"}); e != nil {
		t.Fatal(e)
	}
}
func TestGeneratedServiceGo(t *testing.T) {
	s, e := parse(head + serviceFixture)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module service.test\n\ngo 1.22\n", "rec.go": genGo(s), "rec_test.go": serviceGoTest} {
		if e = os.WriteFile(filepath.Join(dir, n), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := exec.Command("go", "test", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

// Every record encoder writes opaque values verbatim ([DEF-A3]), so a service
// may carry a record another definition owns, with or without opaque fields.
func TestServiceNestedOpaqueImportedRecord(t *testing.T) {
	source := head + `struct Record{1:required json payload}(document="true",unknown_fields="grant") service S{oneway void Write(1:Record r)}(wire_name="test/s@1")`
	if _, e := parse(source); e != nil {
		t.Fatal(e)
	}
	load := func(string) (*Definition, error) {
		return parse("namespace * dep.payload\n" + head + `struct Box{1:required json payload}(document="true",unknown_fields="grant")`)
	}
	root := "namespace * root.service\ninclude \"dep.thrift\"\n" + head + `struct Doc{1:required string id}(document="true",unknown_fields="grant") service S{oneway void Write(1:dep.Box r)}(wire_name="test/s@1")`
	if _, e := parseWithIncludes(root, load); e != nil {
		t.Fatal(e)
	}
}
func TestServiceDocs(t *testing.T) {
	s, e := parse(head + strings.Replace(serviceFixture, `wire_name="example.events/events@1"`, `wire_name="example.events/events@1",doc="<intent>&"`, 1))
	if e != nil {
		t.Fatal(e)
	}
	out := genDocs(s)
	for _, v := range []string{"Events", "Write", "Ping", "oneway", "arguments", "example.events/events@1", "&lt;intent&gt;&amp;"} {
		if !strings.Contains(out, v) {
			t.Fatal("missing service docs", v)
		}
	}
	if !strings.Contains(out, `href="#service-Events"`) || strings.Index(out, `id="service-Events"`) > strings.Index(out, `id="encoding"`) {
		t.Fatal("service methods must be discoverable before encoding details")
	}
	s.NoIPC = true
	out = genDocs(s)
	if strings.Contains(out, `href="#protocol"`) || !strings.Contains(out, "Interface <code>Events</code>") {
		t.Fatal("direct interfaces must not advertise an IPC protocol")
	}

}

const serviceGoTest = `package rec
import("errors";"testing";"strings")
type handler struct{count int}
func(h *handler)Write(r Record,s string,b bool,n int64)error{if r.Value!="x"||s!=""||b||n!=0{panic("changed values")};h.count++;return nil}
func(h *handler)Ping()error{h.count++;return nil}
func(h *handler)Opaque(p Raw)error{if string(p)!=` + "`" + `{ "a" : 1 }` + "`" + `{panic("raw bytes changed")};return nil}
type capture struct{frame []byte; err error}
func(c *capture)WriteFrame(b []byte)error{c.frame=append([]byte(nil),b...);return c.err}
func TestCalls(t *testing.T){h:=&handler{};d:=&EventsDispatcher{Handler:h};c:=NewEventsClient(d);if e:=c.Write(Record{Value:"x"},"",false,0);e!=nil{t.Fatal(e)};if e:=c.Ping();e!=nil{t.Fatal(e)};if h.count!=2{t.Fatal(h.count)};if e:=c.Opaque(Raw(` + "`" + `{ "a" : 1 }` + "`" + `));e!=nil{t.Fatal(e)}
 sink:=&capture{err:errors.New("transport down")};c=NewEventsClient(sink);if e:=c.Ping();e!=sink.err{t.Fatal(e)}
 valid:=string(sink.frame);
 for _,bad:=range []string{strings.Replace(valid,"\"version\": 1","\"version\": 2",1),strings.Replace(valid,"example.events/events@1","Else",1),strings.Replace(valid,"Ping","Gone",1),valid+"{}",valid[:len(valid)-1],strings.Replace(valid,"{}","{\"unexpected\":1}",1),"{\"version\":1,\"service\":\"example.events/events@1\",\"method\":\"Write\",\"arguments\":{}}"}{if e:=d.WriteFrame([]byte(bad));e==nil{t.Fatal("accepted",bad)};if h.count!=2{t.Fatal("partial dispatch")}}
}
`

const replyFixture = `struct Record {1: required string value}(document="true",unknown_fields="grant")
service Query {
 Record Echo(1: Record record,2: string text,3: bool enabled,4: i64 count)
 json Opaque(1: json payload)
 void Reset()
 string Fail(1: string kind)
 oneway void Notify()
}(wire_name="example.query/query@1")
`

func TestReplyDocsAndSelection(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	out := genDocs(s)
	for _, word := range []string{"request-response", "ExchangeFrame", "ServiceError", "handler_error", "invalid_result", "payload", "Record"} {
		if !strings.Contains(out, word) {
			t.Fatal("missing reply documentation", word)
		}
	}
	source := head + `struct Record{1:required string value}(document="true",unknown_fields="grant") service S{Record Get()}(wire_name="test/s@1")`
	s, e = parse(source)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = selected(s, []string{"S"}); e == nil {
		t.Fatal("return-only dependency omitted")
	}
	if _, e = selected(s, []string{"S", "Record"}); e != nil {
		t.Fatal(e)
	}
}
func TestGeneratedReplyGo(t *testing.T) {
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = selected(s, []string{"Query"}); e == nil {
		t.Fatal("missing result dependency")
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module reply.test\n\ngo 1.22\n", "rec.go": genGo(s), "rec_test.go": replyGoTest} {
		if e = os.WriteFile(filepath.Join(dir, n), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
	c := exec.Command("go", "test", ".")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("%v\n%s", e, out)
	}
}

const replyGoTest = `package rec
import("encoding/json";"errors";"testing";"strings")
type handler struct{calls int}
func(h *handler)Echo(r Record,text string,enabled bool,count int64)(Record,error){h.calls++;if text!=""||enabled||count!=0{panic("changed zero values")};return r,nil}
func(h *handler)Opaque(p Raw)(Raw,error){h.calls++;return p,nil}
func(h *handler)Reset()error{h.calls++;return nil}
func(h *handler)Notify()error{h.calls++;return nil}
func(h *handler)Fail(kind string)(string,error){h.calls++;switch kind{case "panic":panic("secret panic");case "invalid":return string([]byte{255}),nil;case "diagnostic":return "",&ServiceError{Code:"denied",Message:string([]byte{255})};case "ordinary":return "",errors.New("secret error");default:return "",&ServiceError{Code:ServiceErrorCode(kind)}}}
type fake struct{frame []byte;response []byte;err error}
func(f *fake)WriteFrame(b []byte)error{f.frame=b;return f.err}
func(f *fake)ExchangeFrame(b []byte)([]byte,error){f.frame=b;return f.response,f.err}
func TestRoundtrip(t *testing.T){h:=&handler{};d:=&QueryDispatcher{Handler:h};c:=NewQueryClient(d)
 r,e:=c.Echo(Record{Value:"\u96ea<&"},"",false,0);if e!=nil||r.Value!="\u96ea<&"{t.Fatal(r,e)}
 raw:=Raw("{ \"a\" : [1,\n false] }");v,e:=c.Opaque(raw);if e!=nil||v!=raw{t.Fatal(v,e)}
 if e=c.Reset();e!=nil{t.Fatal(e)};if e=c.Notify();e!=nil{t.Fatal(e)}
 for _,tc:=range []struct{kind,code,message string}{{"future_code","future_code",""},{"panic","handler_error","handler failed"},{"ordinary","handler_error","handler failed"},{"invalid","invalid_result",""}}{_,e=c.Fail(tc.kind);var se *ServiceError;if !errors.As(e,&se)||string(se.Code)!=tc.code||se.Message!=tc.message{t.Fatalf("%s: %#v",tc.kind,e)}}
 if _,e=c.Fail("diagnostic");e==nil{t.Fatal("invalid diagnostic")}
}
func TestRefusals(t *testing.T){h:=&handler{};d:=&QueryDispatcher{Handler:h};f:=&fake{err:errors.New("offline")};c:=NewQueryClient(f)
 if _,e:=c.Echo(Record{Value:"x"},"",false,0);e!=f.err{t.Fatal(e)};frame:=string(f.frame)
 if e:=d.WriteFrame([]byte(frame));e==nil{t.Fatal("wrong mode")}
 for _,bad:=range []string{strings.Replace(frame,"\"version\": 1","\"version\": 2",1),frame+"{}",frame[:len(frame)-1],strings.Replace(frame,"\"count\": 0","\"count\": false",1),strings.Replace(frame,"\"count\": 0","\"missing\": 0",1),strings.Replace(frame,"example.query/query@1","unknown",1),strings.Replace(frame,"Echo","unknown",1)}{reply,e:=d.ExchangeFrame([]byte(bad));if e==nil{_,e=serviceResponse(reply,"example.query/query@1","Echo")};if e==nil{t.Fatal("accepted",bad)}}
 if h.calls!=0{t.Fatal("partial dispatch",h.calls)}
 good,e:=d.ExchangeFrame([]byte(frame));if e!=nil{t.Fatal(e)};f.err=nil
 for _,bad:=range []string{string(good)+"{}",strings.Replace(string(good),"\"version\": 1","\"version\": 2",1),strings.Replace(string(good),"Echo","Else",1),strings.Replace(string(good),"example.query/query@1","Else",1),strings.Replace(string(good),"\"value\": \"x\"","\"value\": false",1)}{f.response=[]byte(bad);if _,e=c.Echo(Record{Value:"x"},"",false,0);e==nil{t.Fatal("accepted response",bad)}}
 if h.calls!=1{t.Fatal(h.calls)}
}
type metadataHandler struct{*handler}
func(metadataHandler)Ready()(bool,string){return false,"journal:\n\"unreadable\""}
func(metadataHandler)DescribeMetadata()([]string,map[string]string){return []string{"g\n\"one\"","second"},map[string]string{"z\"":"v\\end","a":"雪"}}
type readyWithReason struct{*handler}
func(readyWithReason)Ready()(bool,string){return true,"stale reason"}
func TestGeneratedEndpointMetadata(t *testing.T){
 frame:=[]byte("{\"version\":1,\"service\":\"abstraction.facade/endpoint@1\",\"method\":\"Describe\",\"arguments\":{}}")
 with:=&QueryDispatcher{Handler:metadataHandler{&handler{}}}
 without:=&QueryDispatcher{Handler:readyWithReason{&handler{}}}
 reply,err:=ServeEndpoint(frame,"program\n\"x\"","v1",with,without);if err!=nil{t.Fatal(err)}
 payload,err:=serviceResponse(reply,EndpointContract,"Describe");if err!=nil{t.Fatal(err)}
 var decoded struct{Value struct{Program string;Services []struct{Contract string;Readiness string;Why string;Guarantees []string;Capabilities map[string]string}}}
 if err=json.Unmarshal([]byte(payload),&decoded);err!=nil{t.Fatal(err,string(payload))}
 if decoded.Value.Program!="program\n\"x\""||len(decoded.Value.Services)!=2{t.Fatal(decoded)}
 first,second:=decoded.Value.Services[0],decoded.Value.Services[1]
 if first.Readiness!="not_ready"||first.Why!="journal:\n\"unreadable\""||len(first.Guarantees)!=2||first.Guarantees[0]!="g\n\"one\""||first.Capabilities["z\""]!="v\\end"||first.Capabilities["a"]!="雪"{t.Fatal(first)}
 if second.Readiness!="ready"||second.Why!=""||len(second.Guarantees)!=0||len(second.Capabilities)!=0{t.Fatal(second)}
 if strings.Index(string(payload),"\"a\":")>strings.Index(string(payload),"\"z\\\"\":"){t.Fatal("capability map order",payload)}
 again,err:=ServeEndpoint(frame,"program\n\"x\"","v1",with,without);if err!=nil||string(again)!=string(reply){t.Fatal("nondeterministic metadata",err)}
}
`

func TestReplyCppGoExchange(t *testing.T) {
	cxx := os.Getenv("CXX")
	if cxx == "" {
		cxx, _ = exec.LookPath("g++")
	}
	if cxx == "" {
		cxx, _ = exec.LookPath("clang++")
	}
	if cxx == "" {
		t.Skip("C++ exchange requires CXX, g++ or clang++")
	}
	if version, err := exec.Command(cxx, "--version").CombinedOutput(); err == nil {
		t.Logf("C++ compiler %s: %s", cxx, version)
	} else {
		t.Logf("C++ compiler %s (--version unavailable)", cxx)
	}
	s, e := parse(head + replyFixture)
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	for n, b := range map[string]string{"go.mod": "module exchange.test\n\ngo 1.22\n", "rec/rec.go": genGo(s), "rec.h": genCpp(s), "main.go": replyExchangeGo, "main.cpp": replyExchangeCpp} {
		writeNamespaceFile(t, dir, n, b)
	}
	cpp := filepath.Join(dir, "client.exe")
	goexe := filepath.Join(dir, "host.exe")
	args := []string{"-std=c++17", filepath.Join(dir, "main.cpp"), "-o", cpp}
	if n := strings.ToLower(filepath.Base(cxx)); n == "cl" || n == "cl.exe" {
		args = []string{"/nologo", "/std:c++17", "/EHsc", filepath.Join(dir, "main.cpp"), "/Fe:" + cpp, "/Fo:" + filepath.Join(dir, "main.obj")}
	}
	c := exec.Command(cxx, args...)
	c.Dir = dir
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("C++ compile: %v\n%s", e, out)
	}
	c = exec.Command("go", "build", "-o", goexe, "main.go")
	c.Dir = dir
	c.Env = append(os.Environ(), "GOWORK=off")
	if out, e := c.CombinedOutput(); e != nil {
		t.Fatalf("Go compile: %v\n%s", e, out)
	}
	run := func(exe, mode, method string, input []byte) []byte {
		t.Helper()
		c := exec.Command(exe, mode, method)
		c.Stdin = strings.NewReader(string(input))
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%s %s %s: %v\n%s", exe, mode, method, e, out)
		}
		return out
	}
	for _, method := range []string{"Echo", "Opaque", "Reset", "Fail"} {
		request := run(cpp, "emit", method, nil)
		goRequest := run(goexe, "emit", method, nil)
		if string(request) != string(goRequest) {
			t.Fatalf("%s request byte mismatch (%d C++ bytes, %d Go bytes)\nC++ %q\nGo %q", method, len(request), len(goRequest), request, goRequest)
		}
		response := run(goexe, "serve", method, request)
		run(cpp, "check", method, response)
		// Same generated C++ client must reject a response belonging to another method.
		bad := strings.Replace(string(response), "\"method\": \""+method+"\"", "\"method\": \"Other\"", 1)
		run(cpp, "reject", method, []byte(bad))
	}
}

const replyExchangeGo = `package main
import("os";"io";"fmt";r "exchange.test/rec")
type handler struct{}
func(handler)Echo(v r.Record,s string,b bool,n int64)(r.Record,error){if s!=""||b||n!=0{panic("bad args")};return v,nil}
func(handler)Opaque(v r.Raw)(r.Raw,error){return v,nil}
func(handler)Reset()error{return nil}
func(handler)Notify()error{return nil}
func(handler)Fail(string)(string,error){return "",&r.ServiceError{Code:"future_code"}}
type capture struct{}
func(capture)WriteFrame(b []byte)error{_,e:=os.Stdout.Write(b);return e}
func(capture)ExchangeFrame(b []byte)([]byte,error){os.Stdout.Write(b);return nil,fmt.Errorf("captured")}
func main(){if os.Args[1]=="emit"{c:=r.NewQueryClient(capture{});switch os.Args[2]{case "Echo":c.Echo(r.Record{Value:"\u96ea<&"},"",false,0);case "Opaque":c.Opaque(r.Raw("{ \"a\" : [1,\n false] }"));case "Reset":c.Reset();case "Fail":c.Fail("future_code")};return};b,e:=io.ReadAll(os.Stdin);if e!=nil{panic(e)};d:=r.QueryDispatcher{Handler:handler{}};v,e:=d.ExchangeFrame(b);if e!=nil{panic(e)};os.Stdout.Write(v)}
`
const replyExchangeCpp = `#include "rec.h"
#include <iostream>
#include <iterator>
#ifdef _WIN32
#define NOMINMAX
#include <windows.h>
#endif
// Test transport carries bytes. Some MinGW libstdc++ builds translate cout
// despite binary mode on the C standard stream; use native handles here.
void writeFrame(std::string_view frame){
#ifdef _WIN32
 HANDLE out=GetStdHandle(STD_OUTPUT_HANDLE);
 while(!frame.empty()){DWORD n=0;DWORD want=static_cast<DWORD>(frame.size());if(!WriteFile(out,frame.data(),want,&n,nullptr)||n==0)throw std::runtime_error("stdout write failed");frame.remove_prefix(n);}
#else
 std::cout.write(frame.data(),static_cast<std::streamsize>(frame.size()));if(!std::cout)throw std::runtime_error("stdout write failed");
#endif
}
std::string readFrame(){
#ifdef _WIN32
 std::string frame;char buffer[4096];DWORD n;
 for(;;){if(!ReadFile(GetStdHandle(STD_INPUT_HANDLE),buffer,sizeof buffer,&n,nullptr)){if(GetLastError()==ERROR_BROKEN_PIPE)break;throw std::runtime_error("stdin read failed");}if(n==0)break;frame.append(buffer,n);}return frame;
#else
 return std::string(std::istreambuf_iterator<char>(std::cin),{});
#endif
}
struct Captured{};
struct Transport{
 std::string mode;
 void write_frame(std::string_view){}
 std::string exchange_frame(std::string_view frame){if(mode=="emit"){writeFrame(frame);throw Captured{};}return readFrame();}
};
int main(int argc,char**argv){
Transport t{argv[1]};rec::QueryClient c(t);std::string method=argv[2];bool reject=t.mode=="reject";
 try{if(method=="Echo"){rec::Record v;v.value="\xE9\x9B\xAA<&";auto result=c.echo(v,"",false,0);if(result.value!=v.value)return 2;}
 else if(method=="Opaque"){std::string raw="{ \"a\" : [1,\n false] }";if(c.opaque(raw)!=raw)return 3;}
 else if(method=="Reset"){c.reset();}
 else if(method=="Fail"){c.fail("future_code");return 4;}
 return reject?5:0;
 }catch(const Captured&){return t.mode=="emit"?0:6;}
 catch(const rec::DispatchError& e){return reject&&std::string(e.what())=="mismatched_response"?0:7;}
 catch(const rec::ServiceError&e){return !reject&&method=="Fail"&&e.code=="future_code"&&e.message.empty()?0:8;}
 catch(const std::exception&e){std::cerr<<e.what();return 9;}
}
`

func TestCppServiceDescriptor(t *testing.T) {
	s, err := parse(head + serviceFixture)
	if err != nil {
		t.Fatal(err)
	}
	out := genCpp(s)
	for _, want := range []string{`struct EventsService{`, `kWireName="example.events/events@1"`, `kCapability="example.events"`, `using Client=EventsClient<Transport>`} {
		if !strings.Contains(out, want) {
			t.Fatal("missing descriptor", want)
		}
	}
	legacy, err := parse(head + strings.Replace(serviceFixture, "example.events/events@1", "legacy", 1))
	if err != nil {
		t.Fatal("legacy wire name", err)
	}
	if !strings.Contains(genCpp(legacy), `kCapability=""`) {
		t.Fatal("invented legacy capability")
	}
	_, err = parse(head + strings.Replace(serviceFixture, "struct Record", `struct EventsService{1:required string v}(unknown_fields="grant") struct Record`, 1))
	if err == nil {
		t.Fatal("descriptor name collision accepted")
	}
}

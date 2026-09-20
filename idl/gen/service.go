package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

func (p *parser) serviceDef() error {
	p.i++
	name, e := p.ident()
	if e != nil {
		return e
	}
	s := Service{Name: name}
	if e = p.want("{"); e != nil {
		return e
	}
	for !p.at("}") {
		oneway := p.at("oneway")
		if oneway {
			p.i++
		}
		alias := p.peek().text
		typ := "void"
		if p.at("void") {
			p.i++
		} else {
			typ, e = p.typeRef()
			if e != nil {
				return e
			}
		}
		if oneway && typ != "void" {
			return fmt.Errorf("oneway methods require void return")
		}
		mn, e := p.ident()
		if e != nil {
			return e
		}
		m := Method{Name: mn, Oneway: oneway, Result: Field{ID: 1, Name: "value", Type: typ, Omit: "never", Alias: alias, Grammar: p.grammars[alias]}}
		if e = p.want("("); e != nil {
			return e
		}
		for !p.at(")") {
			if p.peek().kind != "int" {
				return fmt.Errorf("service argument needs positive field id")
			}
			at := p.i + 2
			if at >= len(p.toks) {
				return fmt.Errorf("truncated argument")
			}
			if p.toks[at].text == "optional" {
				return fmt.Errorf("optional service arguments not supported")
			}
			if p.toks[at].text != "required" {
				p.toks = append(p.toks, token{})
				copy(p.toks[at+1:], p.toks[at:])
				p.toks[at] = token{kind: "ident", text: "required", line: p.peek().line}
			}
			f, e := p.field()
			if e != nil {
				return e
			}
			m.Args = append(m.Args, f)
		}
		p.i++
		if p.at("throws") {
			return fmt.Errorf("throws is not supported; handlers return ServiceError with explicit code and message")
		}
		ann, e := p.annotations()
		if e != nil {
			return e
		}
		for k := range ann {
			if k != "doc" {
				return fmt.Errorf("unknown method annotation %s", k)
			}
		}
		m.Doc = ann["doc"]
		if p.at(",") || p.at(";") {
			p.i++
		}
		s.Methods = append(s.Methods, m)
	}
	p.i++
	ann, e := p.annotations()
	if e != nil {
		return e
	}
	for k := range ann {
		if k != "wire_name" && k != "doc" && k != "error_codes" {
			return fmt.Errorf("unknown service annotation %s", k)
		}
	}
	s.Doc = ann["doc"]
	s.WireName = ann["wire_name"]
	s.ErrorCodesConst = ann["error_codes"]
	p.def.Services = append(p.def.Services, s)
	return nil
}
func argsName(s Service, m Method) string { return "OA" + s.Name + m.Name + "Arguments" }
func (p *parser) validateServices() error {
	if len(p.def.Services) == 0 {
		return nil
	}
	used := map[string]bool{}
	normalized := map[string]bool{}
	for _, n := range surfaces(p.def) {
		if used[n] {
			return fmt.Errorf("duplicate declaration %s", n)
		}
		used[n] = true
		normalized[lower(n)] = true
	}
	reserve := func(n string) error {
		if used[n] || normalized[lower(n)] {
			return fmt.Errorf("generated service name collision: %s", n)
		}
		used[n] = true
		normalized[lower(n)] = true
		return nil
	}
	for _, n := range []string{"Reader", "Raw", "Refusal", "OAServiceFrame", "FrameWriter", "DispatchError", "Transport", "FrameExchanger", "ServiceError", "OAServiceReply", "OAServiceError", "ServiceErrorCode", "EndpointContract", "DescribedService", "DescribeEndpoint", "ServedService", "ServeEndpoint"} {
		if e := reserve(n); e != nil {
			return e
		}
	}
	for i := range p.def.Services {
		if err := resolveErrorCodes(p.def, &p.def.Services[i]); err != nil {
			return err
		}
	}
	wireNames := map[string]bool{}
	if len(p.def.Services) > 1 {
		for _, name := range []string{"ServiceName", "service_name"} {
			if err := reserve(name); err != nil {
				return err
			}
		}
	}
	for _, s := range p.def.Services {
		if strings.TrimSpace(s.WireName) == "" || wireNames[s.WireName] {
			return fmt.Errorf("service %s needs a unique nonempty wire_name", s.Name)
		}
		wireNames[s.WireName] = true
		if !serviceIdentifier(s.Name) {
			return fmt.Errorf("invalid service name %s", s.Name)
		}
		if len(s.Methods) == 0 {
			return fmt.Errorf("service %s has no methods", s.Name)
		}
		for _, n := range []string{s.Name + "Client", s.Name + "Dispatcher", s.Name + "Transport", s.Name + "Service"} {
			if e := reserve(n); e != nil {
				return e
			}
		}
		methods := map[string]bool{}
		for _, m := range s.Methods {
			if !serviceIdentifier(m.Name) || methods[exported(m.Name)] || m.Name == "transport_" || m.Name == s.Name+"Client" || m.Name == s.Name {
				return fmt.Errorf("invalid or colliding method %s", m.Name)
			}
			methods[exported(m.Name)] = true
			if e := reserve(argsName(s, m)); e != nil {
				return e
			}
			if !m.Oneway {
				if e := reserve(resultName(s, m)); e != nil {
					return e
				}
				f := m.Result
				if f.Type != "void" && !scalarTypes[f.Type] && p.def.Enum(f.Type) == nil && p.def.Enum(listElement(f.Type)) == nil && !encodableCollections[f.Type] && !p.def.IsStruct(f.Type) && p.def.Repeated(f.Type) == "" {
					return fmt.Errorf("unsupported service return type %s", f.Type)
				}
				if f.Type != "json" && serviceNestedOpaque(p.def, f.Type, map[string]bool{}) {
					return fmt.Errorf("service return contains nested opaque JSON")
				}
			}
			if len(m.Args) > 32 {
				return fmt.Errorf("at most 32 arguments: generated struct readers track field presence in a 32-bit mask")
			}
			ids := map[int]bool{}
			names := map[string]bool{}
			cppNames := map[string]bool{}
			for _, f := range m.Args {
				if !serviceIdentifier(f.Ident("cpp")) {
					return fmt.Errorf("invalid C++ service argument identifier %q for %s", f.Ident("cpp"), f.Name)
				}
				if !serviceIdentifier(exported(f.Ident("go"))) {
					return fmt.Errorf("invalid Go service argument identifier %q for %s", exported(f.Ident("go")), f.Name)
				}
				if ids[f.ID] || names[exported(f.Ident("go"))] || cppNames[f.Ident("cpp")] {
					return fmt.Errorf("duplicate argument %s", f.Name)
				}
				ids[f.ID] = true
				names[exported(f.Ident("go"))] = true
				cppNames[f.Ident("cpp")] = true
				if f.Type != "json" && serviceNestedOpaque(p.def, f.Type, map[string]bool{}) {
					return fmt.Errorf("service argument %s contains nested opaque JSON; service-safe nested encoding is not implemented", f.Name)
				}
				if f.Omit != "never" {
					return fmt.Errorf("arguments must be required")
				}
				if !scalarTypes[f.Type] && p.def.Enum(f.Type) == nil && p.def.Enum(listElement(f.Type)) == nil && !encodableCollections[f.Type] && !p.def.IsStruct(f.Type) && p.def.Repeated(f.Type) == "" {
					return fmt.Errorf("unsupported argument type %s", f.Type)
				}
			}
		}
	}
	return nil
}
func validateServiceBackend(s *Definition, lang string) error {
	if lang == "javascript" {
		return validateJSServices(s)
	}
	if lang == "rust" {
		return validateRustServices(s)
	}
	if lang == "python" {
		return validatePythonServices(s)
	}
	if len(s.Services) > 0 && lang != "go" && lang != "cpp" && lang != "docs" {
		if s.NoIPC {
			return fmt.Errorf("%s --no-ipc service interface generation is not implemented", lang)
		}
		return fmt.Errorf("%s service generation is not implemented; refusing data-only output", lang)
	}
	return nil
}

// dispatcherErrorCodes are the codes a generated dispatcher itself sends on
// the reply error channel: a handler failure, a result its own codec refuses,
// and a request it cannot route.
var dispatcherErrorCodes = []string{"handler_error", "invalid_result", "unknown_version", "unknown_service", "unknown_method", "wrong_mode"}

var errorCodeWord = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

// resolveErrorCodes reads a service's error_codes annotation: the name of a
// const list<string> whose words are the codes its handlers send.
func resolveErrorCodes(def *Definition, svc *Service) error {
	svc.ErrorCodes = nil
	if svc.ErrorCodesConst == "" {
		return nil
	}
	for _, c := range def.Consts {
		if c.Name != svc.ErrorCodesConst {
			continue
		}
		if c.Type != "list<string>" {
			return fmt.Errorf("service %s: error_codes names %s, which is a %s and not a const list<string>", svc.Name, c.Name, c.Type)
		}
		seen := map[string]bool{}
		for _, w := range c.Strings {
			if !errorCodeWord.MatchString(w) || seen[w] {
				return fmt.Errorf("service %s: error code %q in %s is not a distinct snake_case word", svc.Name, w, c.Name)
			}
			seen[w] = true
		}
		svc.ErrorCodes = append([]string(nil), c.Strings...)
		return nil
	}
	return fmt.Errorf("service %s: error_codes names %s, which the definition does not declare as a const", svc.Name, svc.ErrorCodesConst)
}

// serviceErrorCodes is the reply error channel's vocabulary: the dispatcher's
// own codes, then each service's declared codes in declaration order.
func serviceErrorCodes(s *Definition) Enum {
	en := Enum{Name: "ServiceErrorCode", Ann: map[string]string{"unknown": "grant"}}
	seen := map[string]bool{}
	add := func(w string) {
		if !seen[w] {
			seen[w] = true
			en.Members = append(en.Members, Member{ID: len(en.Members) + 1, Name: w, Ann: map[string]string{}})
		}
	}
	for _, w := range dispatcherErrorCodes {
		add(w)
	}
	for _, svc := range s.Services {
		for _, w := range svc.ErrorCodes {
			add(w)
		}
	}
	return en
}

func serviceTypes(s *Definition) *Definition {
	if len(s.Services) == 0 {
		return s
	}
	o := *s
	if hasReplies(s) {
		o.Enums = append(append([]Enum(nil), s.Enums...), serviceErrorCodes(s))
	}
	o.Structs = append([]Struct(nil), s.Structs...)
	o.byName = map[string]*Struct{}
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			o.Structs = append(o.Structs, Struct{Name: argsName(svc, m), Fields: serviceFields(m.Args), UnknownFields: "refuse"})
		}
	}
	var fs []Field
	for i, x := range []struct{ n, t string }{{"version", "i32"}, {"service", "string"}, {"method", "string"}, {"arguments", "json"}} {
		fs = append(fs, Field{ID: i + 1, Name: x.n, Type: x.t, Omit: "never"})
	}
	o.Structs = append(o.Structs, Struct{Name: "OAServiceFrame", Fields: serviceFields(fs), UnknownFields: "refuse"})
	if hasReplies(s) {
		o.Structs = append(o.Structs, Struct{Name: "OAServiceReply", Fields: serviceFields([]Field{
			{ID: 1, Name: "version", Type: "i32", Omit: "never"}, {ID: 2, Name: "service", Type: "string", Omit: "never"}, {ID: 3, Name: "method", Type: "string", Omit: "never"}, {ID: 4, Name: "ok", Type: "bool", Omit: "never"}, {ID: 5, Name: "payload", Type: "json", Omit: "never"}}), UnknownFields: "refuse"},
			Struct{Name: "OAServiceError", Fields: []Field{{ID: 1, Name: "code", Type: "string", Omit: "never"}, {ID: 2, Name: "message", Type: "string", Omit: "never"}}, UnknownFields: "refuse"})
		for _, svc := range s.Services {
			for _, m := range svc.Methods {
				if !m.Oneway {
					var fields []Field
					if m.Result.Type != "void" {
						fields = []Field{m.Result}
					}
					o.Structs = append(o.Structs, Struct{Name: resultName(svc, m), Fields: serviceFields(fields), UnknownFields: "refuse"})
				}
			}
		}
	}
	for i := range o.Structs {
		o.byName[o.Structs[i].Name] = &o.Structs[i]
	}
	return &o
}

// goArgs is a method's parameter list, named from the contract. Consecutive
// parameters of one type share it, as gofmt users write them.
func goArgs(s *Definition, m Method, named bool) string {
	var a []string
	for i, f := range m.Args {
		t := goType(s, f)
		name := goParam(f, goReservedLocals)
		if i+1 < len(m.Args) && goType(s, m.Args[i+1]) == t {
			a = append(a, name)
			continue
		}
		a = append(a, name+" "+t)
	}
	return strings.Join(a, ", ")
}

// goReservedLocals are the names a generated client or dispatcher method body
// declares itself; a contract parameter spelled the same gains an underscore.
var goReservedLocals = map[string]bool{"c": true, "d": true, "args": true, "v": true, "frame": true, "response": true, "payload": true, "r": true, "decoded": true, "result": true, "err": true, "value": true}

func cppArgs(s *Definition, m Method) string {
	var a []string
	for _, f := range m.Args {
		a = append(a, fmt.Sprintf("const %s& %s", cppType(s, f), cppParam(f)))
	}
	return strings.Join(a, ",")
}

// Service carriers retain raw argument tokens verbatim. Ordinary document
// encoders keep their existing whitespace policy.
func serviceFields(fields []Field) []Field {
	out := append([]Field(nil), fields...)
	for i := range out {
		ann := map[string]string{}
		for k, v := range out[i].Ann {
			ann[k] = v
		}
		ann["service_raw"] = "true"
		out[i].Ann = ann
	}
	return out
}

func serviceIdentifier(name string) bool {
	if name == "" || strings.Contains(name, ".") {
		return false
	}
	// Generated C++ identifiers share the core language's reserved words, and
	// service type names must not shadow Go's predeclared scalar types.
	if namespaceKeyword("cpp", name) || namespaceKeyword("go", name) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if (i == 0 && !isIdentStart(name[i])) || (i > 0 && !isIdent(name[i])) {
			return false
		}
	}
	for _, word := range strings.Fields("string byte rune error int8 int16 int32 int64 uint uint8 uint16 uint32 uint64 uintptr complex64 complex128 float32 float64 any comparable") {
		if name == word {
			return false
		}
	}
	return true
}

func serviceNestedOpaque(s *Definition, t string, seen map[string]bool) bool {
	if imp, ok := s.importedRecord(t); ok {
		return serviceNestedOpaque(imp.Def, imp.Name, map[string]bool{})
	}
	if t == "json" || t == "map<string,json>" || t == "list<json>" {
		return true
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	if elem := s.Repeated(t); elem != "" {
		return serviceNestedOpaque(s, elem, seen)
	}
	if st := s.Struct(t); st != nil {
		for _, f := range st.Fields {
			if serviceNestedOpaque(s, f.Type, seen) {
				return true
			}
		}
	}
	return false
}

func resultName(s Service, m Method) string { return "OA" + s.Name + m.Name + "Result" }
func hasReplies(s *Definition) bool {
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			if !m.Oneway {
				return true
			}
		}
	}
	return false
}
func svcReplies(s Service) bool {
	for _, m := range s.Methods {
		if !m.Oneway {
			return true
		}
	}
	return false
}
func svcOneways(s Service) bool {
	for _, m := range s.Methods {
		if m.Oneway {
			return true
		}
	}
	return false
}
func goReturn(s *Definition, m Method, named bool) string {
	if m.Oneway || m.Result.Type == "void" {
		if named {
			return "(err error)"
		}
		return "error"
	}
	t := goType(s, m.Result)
	if named {
		return "(result " + t + ",err error)"
	}
	return "(" + t + ",error)"
}
func cppReturn(s *Definition, m Method) string {
	if m.Oneway || m.Result.Type == "void" {
		return "void"
	}
	return cppType(s, m.Result)
}
func callArgs(m Method, lang string) string {
	var args []string
	for _, f := range m.Args {
		n := f.Ident(lang)
		if lang == "go" {
			n = exported(n)
		}
		args = append(args, "args."+n)
	}
	return strings.Join(args, ",")
}

func goService(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	b.WriteString(`
// A transport consumes or copies frames before returning. WriteFrame is one-way.
type FrameWriter interface{WriteFrame(frame []byte)error}
type DispatchError string
func(e DispatchError)Error()string{return string(e)}
func servicePayload(frame []byte)(*OAServiceFrame,error){r:=&reader{buf:frame};r.ws();v,err:=r.decodeOAServiceFrame();if err!=nil{return nil,err};r.ws();if r.pos!=len(r.buf){return nil,r.refuse("trailing_bytes")};if v.Version!=1{return nil,DispatchError("unknown_version")};return v,nil}
`)
	if len(s.Services) > 1 {
		b.WriteString(`
// ServiceName validates the request envelope and version for routing. The chosen
// generated dispatcher validates service, method and typed arguments before use.
func ServiceName(frame []byte)(string,error){v,err:=servicePayload(frame);if err!=nil{return "",err};return v.Service,nil}
`)
	}
	if hasReplies(s) {
		b.WriteString(`
// ExchangeFrame returns the response associated with this call. Correlation,
// serialization and deadlines belong to the transport, not this codec.
type FrameExchanger interface{ExchangeFrame(frame []byte)([]byte,error)}
// ServiceError is a reply on the error channel. Code is a ServiceErrorCode
// constant or a word this package has never heard.
type ServiceError struct{Code ServiceErrorCode;Message string}
func(e *ServiceError)Error()string{if e.Message!=""{return e.Message};return string(e.Code)}
func serviceResponse(frame []byte,service,method string)(Raw,error){
 r:=&reader{buf:frame};r.ws();v,err:=r.decodeOAServiceReply();if err!=nil{return "",err};r.ws();if r.pos!=len(r.buf){return "",r.refuse("trailing_bytes")}
 if v.Version!=1{return "",DispatchError("unknown_version")};if v.Service!=service||v.Method!=method{return "",DispatchError("mismatched_response")}
 if !v.Ok{r=&reader{buf:[]byte(v.Payload),depth:1};r.ws();e,err:=r.decodeOAServiceError();if err!=nil{return "",err};r.ws();if r.pos!=len(r.buf){return "",r.refuse("trailing_bytes")};if e.Code==""{return "",DispatchError("invalid_error")};return "",&ServiceError{Code:ServiceErrorCode(e.Code),Message:e.Message}}
 return v.Payload,nil
}
func serviceReply(v *OAServiceFrame,payload Raw,err error)(frame []byte,outErr error){
 defer func(){if p:=recover();p!=nil{if e,ok:=p.(*Refusal);ok{frame=nil;outErr=e}else{panic(p)}}}()
 reply:=OAServiceReply{Version:1,Service:v.Service,Method:v.Method,Ok:err==nil,Payload:payload}
 if err!=nil{e:=OAServiceError{Code:string(ServiceErrorCodeHandlerError),Message:"handler failed"};switch x:=err.(type){case *ServiceError:if x.Code!=""{e.Code=string(x.Code)};e.Message=x.Message;case DispatchError:e.Code=string(x);e.Message="";case *Refusal:e.Code=x.Word;e.Message=""};reply.Payload=Raw(encOAServiceError(nil,&e,1))}
 frame=encOAServiceReply(nil,&reply,0)
 // Validate even error diagnostics before making them visible on the wire.
 r:=&reader{buf:frame};r.ws();if _,e:=r.decodeOAServiceReply();e!=nil{return nil,e};return frame,nil
}
`)
	}
	if hasReplies(s) {
		b.WriteString(goEndpoint)
	}
	for _, svc := range s.Services {
		fmt.Fprintf(b, "type %s interface{\n", svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "%s(%s)%s\n", goName(m.Name), goArgs(s, m, false), goReturn(s, m, false))
		}
		b.WriteString("}\n")
		fmt.Fprintf(b, "type %sTransport interface{\n", svc.Name)
		if svcOneways(svc) {
			b.WriteString("FrameWriter\n")
		}
		if svcReplies(svc) {
			b.WriteString("FrameExchanger\n")
		}
		b.WriteString("}\n")
		fmt.Fprintf(b, "type %sClient struct{transport %sTransport}\nfunc New%sClient(t %sTransport)*%sClient{return &%sClient{transport:t}}\ntype %sDispatcher struct{Handler %s}\n", svc.Name, svc.Name, svc.Name, svc.Name, svc.Name, svc.Name, svc.Name, svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "func(c *%sClient)%s(%s)%s{\n", svc.Name, goName(m.Name), goArgs(s, m, true), goReturn(s, m, true))
			b.WriteString("defer func(){if p:=recover();p!=nil{if e,ok:=p.(*Refusal);ok{err=e}else{panic(p)}}}()\n")
			fmt.Fprintf(b, "args:=%s{", argsName(svc, m))
			for _, f := range m.Args {
				fmt.Fprintf(b, "%s:%s,", exported(f.Ident("go")), goParam(f, goReservedLocals))
			}
			b.WriteString("}\n")
			fmt.Fprintf(b, "v:=OAServiceFrame{Version:1,Service:%q,Method:%q,Arguments:Raw(enc%s(nil,&args,1))};frame:=encOAServiceFrame(nil,&v,0);if _,err=servicePayload(frame);err!=nil{return}\n", svc.WireName, m.Name, argsName(svc, m))
			if m.Oneway {
				b.WriteString("err=c.transport.WriteFrame(frame);return\n}\n")
				continue
			}
			b.WriteString("var response []byte;response,err=c.transport.ExchangeFrame(frame);if err!=nil{return};var payload Raw;payload,err=serviceResponse(response,v.Service,v.Method);if err!=nil{return};r:=&reader{buf:[]byte(payload),depth:1};r.ws();\n")
			fmt.Fprintf(b, "var decoded *%s;decoded,err=r.decode%s();if err!=nil{return};_ = decoded;r.ws();if r.pos!=len(r.buf){err=r.refuse(\"trailing_bytes\");return}\n", resultName(svc, m), resultName(svc, m))
			if m.Result.Type != "void" {
				b.WriteString("result=decoded.Value\n")
			}
			b.WriteString("return\n}\n")
		}
		if hasReplies(s) {
			fmt.Fprintf(b, "// DescribeService is this dispatcher's service as %s Describe lists it:\n// ready unless its handler implements Ready() (bool, string) and reports otherwise.\nfunc(d *%sDispatcher)DescribeService()(contract string,ready bool,why string){if h,ok:=d.Handler.(interface{Ready()(bool,string)});ok{if ready,why=h.Ready();ready{why=\"\"};return %q,ready,why};return %q,true,\"\"}\n// ServiceContract is the wire name ServeEndpoint routes this dispatcher's frames by.\nfunc(d *%sDispatcher)ServiceContract()string{return %q}\n", endpointWireName, svc.Name, svc.WireName, svc.WireName, svc.Name, svc.WireName)
		}
		fmt.Fprintf(b, "func(d *%sDispatcher)WriteFrame(frame []byte)error{v,err:=servicePayload(frame);if err!=nil{return err};if v.Service!=%q{return DispatchError(\"unknown_service\")};switch v.Method{\n", svc.Name, svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "case %q:\n", m.Name)
			if !m.Oneway {
				b.WriteString("return DispatchError(\"wrong_mode\")\n")
				continue
			}
			fmt.Fprintf(b, "r:=&reader{buf:[]byte(v.Arguments),depth:1};r.ws();args,err:=r.decode%s();if err!=nil{return err};r.ws();if r.pos!=len(r.buf){return r.refuse(\"trailing_bytes\")};_ = args;return d.Handler.%s(%s)\n", argsName(svc, m), goName(m.Name), callArgs(m, "go"))
		}
		b.WriteString("default:return DispatchError(\"unknown_method\")}\n}\n")
		if !svcReplies(svc) {
			continue
		}
		describe := ""
		if svc.WireName != endpointWireName {
			describe = "if v.Service==EndpointContract{return DescribeEndpoint(frame,\"\",\"\",d)};"
		}
		fmt.Fprintf(b, "func(d *%sDispatcher)ExchangeFrame(frame []byte)([]byte,error){v,err:=servicePayload(frame);if err!=nil{return nil,err};%sif v.Service!=%q{return serviceReply(v,\"\",DispatchError(\"unknown_service\"))};switch v.Method{\n", svc.Name, describe, svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "case %q:\n", m.Name)
			if m.Oneway {
				b.WriteString("return serviceReply(v,\"\",DispatchError(\"wrong_mode\"))\n")
				continue
			}
			fmt.Fprintf(b, "r:=&reader{buf:[]byte(v.Arguments),depth:1};r.ws();args,err:=r.decode%s();if err!=nil{return serviceReply(v,\"\",err)};r.ws();if r.pos!=len(r.buf){return serviceReply(v,\"\",r.refuse(\"trailing_bytes\"))};payload,err:=d.invoke%s(args);return serviceReply(v,payload,err)\n", argsName(svc, m), goName(m.Name))
		}
		b.WriteString("default:return serviceReply(v,\"\",DispatchError(\"unknown_method\"))}\n}\n")
		for _, m := range svc.Methods {
			if m.Oneway {
				continue
			}
			fmt.Fprintf(b, "func(d *%sDispatcher)invoke%s(args *%s)(payload Raw,err error){\n", svc.Name, goName(m.Name), argsName(svc, m))
			b.WriteString("defer func(){if p:=recover();p!=nil{payload=\"\";if _,ok:=p.(*Refusal);ok{err=&ServiceError{Code:ServiceErrorCodeInvalidResult}}else{err=&ServiceError{Code:ServiceErrorCodeHandlerError,Message:\"handler failed\"}}}}()\n")
			if m.Result.Type != "void" {
				fmt.Fprintf(b, "var result %s;result,err=d.Handler.%s(%s);", goType(s, m.Result), goName(m.Name), callArgs(m, "go"))
			} else {
				fmt.Fprintf(b, "err=d.Handler.%s(%s);", goName(m.Name), callArgs(m, "go"))
			}
			b.WriteString("if err!=nil{return}\n")
			fmt.Fprintf(b, "value:=%s{", resultName(svc, m))
			if m.Result.Type != "void" {
				b.WriteString("Value:result,")
			}
			b.WriteString("}\n")
			fmt.Fprintf(b, "payload=Raw(enc%s(nil,&value,1));r:=&reader{buf:[]byte(payload),depth:1};r.ws();if _,e:=r.decode%s();e!=nil{payload=\"\";err=&ServiceError{Code:ServiceErrorCodeInvalidResult};return};r.ws();if r.pos!=len(r.buf){payload=\"\";err=&ServiceError{Code:ServiceErrorCodeInvalidResult}};return\n}\n", resultName(svc, m), resultName(svc, m))
		}
	}
}

// endpointWireName is the base-protocol service every generated dispatcher
// answers beside its own (idl/LANGUAGE.md DEF-S2).
const endpointWireName = "abstraction.facade/endpoint@1"

// goEndpoint answers abstraction.facade/endpoint@1 Describe for any generated
// dispatcher. Its reply is written here, in the record shape facade.thrift
// declares, so no generated package depends on the facade's.
const goEndpoint = `
// EndpointContract is abstraction.facade/endpoint@1, which every dispatcher
// answers beside its own service.
const EndpointContract = "abstraction.facade/endpoint@1"

// DescribedService is a dispatcher of any generated package, as
// abstraction.facade/endpoint@1 Describe lists it.
type DescribedService interface{DescribeService()(contract string,ready bool,why string)}

// DescribeEndpoint answers an abstraction.facade/endpoint@1 Describe frame for
// an endpoint hosting services, in that order. program and version are the
// provider's own display name and version, never authority. A frame for another
// service reads unknown_service.
func DescribeEndpoint(frame []byte,program,version string,services ...DescribedService)([]byte,error){
 v,err:=servicePayload(frame);if err!=nil{return nil,err}
 if v.Service!=EndpointContract{return serviceReply(v,"",DispatchError("unknown_service"))}
 if v.Method!="Describe"{return serviceReply(v,"",DispatchError("unknown_method"))}
 r:=&reader{buf:[]byte(v.Arguments)};r.ws();empty:=false;if r.pos<len(r.buf)&&r.buf[r.pos]=='{'{r.pos++;r.ws();if r.pos<len(r.buf)&&r.buf[r.pos]=='}'{r.pos++;r.ws();empty=r.pos==len(r.buf)}}
 if !empty{return serviceReply(v,"",&Refusal{Word:"unknown_field"})}
 out:=append([]byte(nil),"{\"value\":{\"outcome\":\"described\",\"program\":"...);out=esc(out,program);out=append(out,",\"version\":"...);out=esc(out,version);out=append(out,",\"services\":["...)
 for i,service:=range services{
  contract,ready,why:=service.DescribeService();readiness:="ready";if !ready{readiness="not_ready"}
  if i>0{out=append(out,',')};out=append(out,"{\"contract\":"...);out=esc(out,contract);out=append(out,",\"readiness\":\""+readiness+"\",\"why\":"...);out=esc(out,why);out=append(out,",\"guarantees\":[],\"capabilities\":{}}"...)
 }
 return serviceReply(v,Raw(append(out,"]}}"...)),nil)
}

// ServedService is a dispatcher of any generated package that ServeEndpoint
// routes frames to by its wire name.
type ServedService interface{DescribedService;ServiceContract()string}

// ServeEndpoint answers one request-response frame for an endpoint hosting
// services. A Describe frame lists all of them in the order given; any other
// frame goes to the service it names. A service that takes only one-way frames
// reads wrong_mode, and a frame naming none of them reads unknown_service.
func ServeEndpoint(frame []byte,program,version string,services ...ServedService)([]byte,error){
 v,err:=servicePayload(frame);if err!=nil{return nil,err}
 if v.Service==EndpointContract{described:=make([]DescribedService,len(services));for i,service:=range services{described[i]=service};return DescribeEndpoint(frame,program,version,described...)}
 for _,service:=range services{
  if service.ServiceContract()!=v.Service{continue}
  if exchanger,ok:=service.(interface{ExchangeFrame([]byte)([]byte,error)});ok{return exchanger.ExchangeFrame(frame)}
  return serviceReply(v,"",DispatchError("wrong_mode"))
 }
 return serviceReply(v,"",DispatchError("unknown_service"))
}
`

func cppService(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	b.WriteString(`
struct FrameWriter{virtual ~FrameWriter()=default;virtual void write_frame(std::string_view frame)=0;};
struct DispatchError:std::runtime_error{using std::runtime_error::runtime_error;};
namespace detail {
inline OAServiceFrame service_payload(std::string_view frame){Reader r{frame};r.skip_ws();auto v=decode_oa_service_frame(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse("trailing_bytes");if(v.version!=1)throw DispatchError("unknown_version");return v;}
}  // namespace detail
`)
	if len(s.Services) > 1 {
		b.WriteString("// Validates the request envelope and version; dispatchers validate typed arguments.\ninline std::string service_name(std::string_view frame){return detail::service_payload(frame).service;}\n")
	}
	if hasReplies(s) {
		b.WriteString(`
struct FrameExchanger{virtual ~FrameExchanger()=default;virtual std::string exchange_frame(std::string_view frame)=0;};
struct ServiceError:std::runtime_error{std::string code,message;ServiceError(std::string c,std::string m):std::runtime_error(m.empty()?c:m),code(c),message(m){}};
namespace detail {
inline Raw service_response(std::string_view frame,std::string_view service,std::string_view method){
 Reader r{frame};r.skip_ws();auto v=decode_oa_service_reply(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse("trailing_bytes");if(v.version!=1)throw DispatchError("unknown_version");if(v.service!=service||v.method!=method)throw DispatchError("mismatched_response");
 if(!v.ok){Reader e{v.payload};e.depth=1;e.skip_ws();auto error=decode_oa_service_error(e);e.skip_ws();if(e.pos!=e.buf.size())e.refuse("trailing_bytes");if(error.code.empty())throw DispatchError("invalid_error");throw ServiceError(error.code,error.message);}return v.payload;
}
inline std::string service_reply(const OAServiceFrame& request,const Raw& payload,const ServiceError* error=nullptr){
 OAServiceReply reply;reply.version=1;reply.service=request.service;reply.method=request.method;reply.ok=error==nullptr;reply.payload=payload;
 if(error){OAServiceError e;e.code=error->code.empty()?"handler_error":error->code;e.message=error->message;reply.payload.clear();enc_oa_service_error(reply.payload,e,1);}
 std::string frame;enc_oa_service_reply(frame,reply,0);Reader r{frame};r.skip_ws();decode_oa_service_reply(r);return frame;
}
}  // namespace detail
`)
	}
	if hasReplies(s) {
		b.WriteString(cppEndpoint)
	}
	for _, svc := range s.Services {
		fmt.Fprintf(b, "struct %s{virtual ~%s()=default;\n", svc.Name, svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "virtual %s %s(%s)=0;\n", cppReturn(s, m), cppMethod(m), cppArgs(s, m))
		}
		b.WriteString("};\n")
		fmt.Fprintf(b, "template<class Transport>struct %sClient:%s{Transport& transport_;explicit %sClient(Transport&t):transport_(t){}\n", svc.Name, svc.Name, svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "%s %s(%s)override{detail::%s args;\n", cppReturn(s, m), cppMethod(m), cppArgs(s, m), argsName(svc, m))
			for _, f := range m.Args {
				fmt.Fprintf(b, "args.%s=%s;\n", f.Ident("cpp"), cppParam(f))
			}
			fmt.Fprintf(b, "detail::OAServiceFrame v;v.version=1;v.service=%s;v.method=%s;detail::enc_%s(v.arguments,args,1);std::string frame;detail::enc_oa_service_frame(frame,v,0);detail::service_payload(frame);\n", strconv.Quote(svc.WireName), strconv.Quote(m.Name), cppFn(argsName(svc, m)))
			if m.Oneway {
				b.WriteString("transport_.write_frame(frame);}\n")
				continue
			}
			fmt.Fprintf(b, "auto response=transport_.exchange_frame(frame);auto payload=detail::service_response(response,v.service,v.method);detail::Reader r{payload};r.depth=1;r.skip_ws();auto result=detail::decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");\n", cppFn(resultName(svc, m)))
			if m.Result.Type != "void" {
				b.WriteString("return result.value;\n")
			}
			b.WriteString("}\n")
		}
		b.WriteString("};\n")
		capability := ""
		if prefix, profile, ok := strings.Cut(svc.WireName, "/"); ok && prefix != "" && profile != "" && !strings.ContainsAny(prefix, " \t\r\n\x00") {
			capability = prefix
		}
		fmt.Fprintf(b, "struct %sService{inline static constexpr std::string_view kWireName=%s;inline static constexpr std::string_view kCapability=%s;template<class Transport>using Client=%sClient<Transport>;};\n", svc.Name, strconv.Quote(svc.WireName), strconv.Quote(capability), svc.Name)
		base := "FrameWriter"
		if svcReplies(svc) {
			base += ",FrameExchanger"
		}
		fmt.Fprintf(b, "struct %sDispatcher:%s{%s&handler;explicit %sDispatcher(%s&h):handler(h){}\n", svc.Name, base, svc.Name, svc.Name, svc.Name)
		if hasReplies(s) {
			fmt.Fprintf(b, "// A handler whose own type has ready(), returning a pair of bool and std::string, reports its readiness through describe_service.\ntemplate<class H,class=decltype(static_cast<%s&>(*static_cast<H*>(nullptr)))>explicit %sDispatcher(H&h):handler(h),ready_self_(&h),ready_hook_(detail::ready_hook<H>(0)){}\n", svc.Name, svc.Name)
			fmt.Fprintf(b, "// This dispatcher's service as abstraction.facade/endpoint@1 Describe lists it.\nDescribedService describe_service()const{DescribedService s{%s,true,std::string()};if(ready_hook_){s.ready=ready_hook_(ready_self_,s.why);if(s.ready)s.why.clear();}return s;}\n", strconv.Quote(svc.WireName))
		}
		fmt.Fprintf(b, "void write_frame(std::string_view frame)override{auto v=detail::service_payload(frame);if(v.service!=%q)throw DispatchError(\"unknown_service\");\n", svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "if(v.method==%q){\n", m.Name)
			if !m.Oneway {
				b.WriteString("throw DispatchError(\"wrong_mode\");}\n")
				continue
			}
			fmt.Fprintf(b, "detail::Reader r{v.arguments};r.depth=1;r.skip_ws();auto args=detail::decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");handler.%s(%s);return;}\n", cppFn(argsName(svc, m)), cppMethod(m), callArgs(m, "cpp"))
		}
		b.WriteString("throw DispatchError(\"unknown_method\");}\n")
		if svcReplies(svc) {
			describe := ""
			if svc.WireName != endpointWireName {
				describe = "if(v.service==kEndpointContract)return describe_endpoint(frame,std::string(),std::string(),*this);"
			}
			fmt.Fprintf(b, "std::string exchange_frame(std::string_view frame)override{auto v=detail::service_payload(frame);%sif(v.service!=%q){ServiceError e(\"unknown_service\",\"\");return detail::service_reply(v,\"\",&e);}\ntry{\n", describe, svc.WireName)
			for _, m := range svc.Methods {
				fmt.Fprintf(b, "if(v.method==%q){\n", m.Name)
				if m.Oneway {
					b.WriteString("throw ServiceError(\"wrong_mode\",\"\");}\n")
					continue
				}
				fmt.Fprintf(b, "detail::Reader r{v.arguments};r.depth=1;r.skip_ws();auto args=detail::decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");auto payload=invoke_%s(args);return detail::service_reply(v,payload);}\n", cppFn(argsName(svc, m)), cppMethod(m))
			}
			b.WriteString("throw ServiceError(\"unknown_method\",\"\");}catch(const ServiceError&e){return detail::service_reply(v,\"\",&e);}catch(const Refusal&e){ServiceError error(e.word,\"\");return detail::service_reply(v,\"\",&error);}\n}\n")
			b.WriteString("private:\n")
			for _, m := range svc.Methods {
				if m.Oneway {
					continue
				}
				fmt.Fprintf(b, "Raw invoke_%s(const detail::%s&args){\n", cppMethod(m), argsName(svc, m))
				if m.Result.Type != "void" {
					fmt.Fprintf(b, "%s result{};\n", cppReturn(s, m))
				}
				b.WriteString("try{\n")
				if m.Result.Type != "void" {
					b.WriteString("result=")
				}
				fmt.Fprintf(b, "handler.%s(%s);\n", cppMethod(m), callArgs(m, "cpp"))
				b.WriteString("}catch(const ServiceError&){throw;}catch(...){throw ServiceError(\"handler_error\",\"handler failed\");}\ntry{\n")
				fmt.Fprintf(b, "detail::%s value;\n", resultName(svc, m))
				if m.Result.Type != "void" {
					b.WriteString("value.value=result;\n")
				}
				fmt.Fprintf(b, "Raw payload;detail::enc_%s(payload,value,1);detail::Reader r{payload};r.depth=1;r.skip_ws();detail::decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");return payload;\n", cppFn(resultName(svc, m)), cppFn(resultName(svc, m)))
				b.WriteString("}catch(...){throw ServiceError(\"invalid_result\",\"\");}\n}\n")
			}
		}
		if hasReplies(s) {
			b.WriteString("private:\nvoid* ready_self_=nullptr;\nbool(*ready_hook_)(void*,std::string&)=nullptr;\n")
		}
		b.WriteString("};\n")
	}
}

// cppEndpoint answers abstraction.facade/endpoint@1 Describe for any generated
// C++ dispatcher, in the record shape facade.thrift declares.
const cppEndpoint = `
// The base-protocol service every dispatcher answers beside its own.
inline constexpr std::string_view kEndpointContract="abstraction.facade/endpoint@1";
// One service an endpoint hosts, as a dispatcher of any generated namespace
// reports it to describe_endpoint.
struct DescribedService{std::string contract;bool ready;std::string why;};
namespace detail {
template<class H>auto ready_hook(int)->decltype((void)static_cast<H*>(nullptr)->ready(),static_cast<bool(*)(void*,std::string&)>(nullptr)){return [](void* h,std::string& why)->bool{auto r=static_cast<H*>(h)->ready();why=r.second;return r.first;};}
template<class H>bool(*ready_hook(long))(void*,std::string&){return nullptr;}
}  // namespace detail
// Answers an abstraction.facade/endpoint@1 Describe frame for an endpoint
// hosting services, in that order: each is a dispatcher of any generated
// namespace. program and version are the provider's own display name and
// version, never authority. A frame for another service reads unknown_service.
template<class... Services>std::string describe_endpoint(std::string_view frame,const std::string& program,const std::string& version,const Services&... services){
 auto v=detail::service_payload(frame);
 if(v.service!=kEndpointContract){ServiceError e("unknown_service","");return detail::service_reply(v,"",&e);}
 if(v.method!="Describe"){ServiceError e("unknown_method","");return detail::service_reply(v,"",&e);}
 detail::Reader r{v.arguments};r.skip_ws();bool empty=false;
 if(r.pos<r.buf.size()&&r.buf[r.pos]=='{'){r.pos++;r.skip_ws();if(r.pos<r.buf.size()&&r.buf[r.pos]=='}'){r.pos++;r.skip_ws();empty=r.pos==r.buf.size();}}
 if(!empty){ServiceError e("unknown_field","");return detail::service_reply(v,"",&e);}
 try{
  Raw out="{\"value\":{\"outcome\":\"described\",\"program\":";detail::esc(out,program);out+=",\"version\":";detail::esc(out,version);out+=",\"services\":[";
  bool first=true;
  auto add=[&](const auto& s){if(!first)out+=',';first=false;out+="{\"contract\":";detail::esc(out,s.contract);out+=",\"readiness\":\"";out+=s.ready?"ready":"not_ready";out+="\",\"why\":";detail::esc(out,s.why);out+=",\"guarantees\":[],\"capabilities\":{}}";};
  (void)add;
  (add(services.describe_service()),...);
  out+="]}}";
  return detail::service_reply(v,out);
 }catch(const Refusal&e){ServiceError error(e.word,"");return detail::service_reply(v,"",&error);}
}
`

// cppMethod is a service method's C++ name.
func cppMethod(m Method) string { return cppIdent(m.Name) }

// cppParam is a service argument's C++ parameter name. One the generated
// method body already uses gains a trailing underscore.
func cppParam(f Field) string {
	n := f.Ident("cpp")
	switch n {
	case "args", "v", "frame", "response", "payload", "r", "result", "transport_", "handler", "value", "e", "error":
		return n + "_"
	}
	return n
}

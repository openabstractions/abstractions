package main

import (
	"fmt"
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
		if k != "wire_name" && k != "doc" {
			return fmt.Errorf("unknown service annotation %s", k)
		}
	}
	s.Doc = ann["doc"]
	s.WireName = ann["wire_name"]
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
	for _, n := range []string{"Reader", "Raw", "Refusal", "OAServiceFrame", "FrameWriter", "DispatchError", "Transport", "FrameExchanger", "ServiceError", "OAServiceReply", "OAServiceError"} {
		if e := reserve(n); e != nil {
			return e
		}
	}
	wireNames := map[string]bool{}
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
		for _, n := range []string{s.Name + "Client", s.Name + "Dispatcher", s.Name + "Transport"} {
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
				if f.Type != "void" && !scalarTypes[f.Type] && !encodableCollections[f.Type] && !p.def.IsStruct(f.Type) && p.def.Repeated(f.Type) == "" {
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
				if !scalarTypes[f.Type] && !encodableCollections[f.Type] && !p.def.IsStruct(f.Type) && p.def.Repeated(f.Type) == "" {
					return fmt.Errorf("unsupported argument type %s", f.Type)
				}
			}
		}
	}
	return nil
}
func validateServiceBackend(s *Definition, lang string) error {
	if lang == "python" {
		return validatePythonServices(s)
	}
	if len(s.Services) > 0 && lang != "go" && lang != "cpp" && lang != "docs" {
		return fmt.Errorf("%s service generation is not implemented; refusing data-only output", lang)
	}
	return nil
}
func serviceTypes(s *Definition) *Definition {
	if len(s.Services) == 0 {
		return s
	}
	o := *s
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
func goArgs(s *Definition, m Method, named bool) string {
	var a []string
	for i, f := range m.Args {
		t := goType(s, f)
		if named {
			t = fmt.Sprintf("arg%d %s", i, t)
		}
		a = append(a, t)
	}
	return strings.Join(a, ",")
}
func cppArgs(s *Definition, m Method) string {
	var a []string
	for i, f := range m.Args {
		a = append(a, fmt.Sprintf("const %s& arg%d", cppType(s, f), i))
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
type FrameWriter interface{WriteFrame([]byte)error}
type DispatchError string
func(e DispatchError)Error()string{return string(e)}
func servicePayload(frame []byte)(*OAServiceFrame,error){r:=&reader{buf:frame};r.ws();v,err:=r.decodeOAServiceFrame();if err!=nil{return nil,err};r.ws();if r.pos!=len(r.buf){return nil,r.refuse("trailing_bytes")};if v.Version!=1{return nil,DispatchError("unknown_version")};return v,nil}
`)
	if hasReplies(s) {
		b.WriteString(`
// ExchangeFrame returns the response associated with this call. Correlation,
// serialization and deadlines belong to the transport, not this codec.
type FrameExchanger interface{ExchangeFrame([]byte)([]byte,error)}
type ServiceError struct{Code string;Message string}
func(e *ServiceError)Error()string{if e.Message!=""{return e.Message};return e.Code}
func serviceResponse(frame []byte,service,method string)(Raw,error){
 r:=&reader{buf:frame};r.ws();v,err:=r.decodeOAServiceReply();if err!=nil{return "",err};r.ws();if r.pos!=len(r.buf){return "",r.refuse("trailing_bytes")}
 if v.Version!=1{return "",DispatchError("unknown_version")};if v.Service!=service||v.Method!=method{return "",DispatchError("mismatched_response")}
 if !v.Ok{r=&reader{buf:[]byte(v.Payload),depth:1};r.ws();e,err:=r.decodeOAServiceError();if err!=nil{return "",err};r.ws();if r.pos!=len(r.buf){return "",r.refuse("trailing_bytes")};if e.Code==""{return "",DispatchError("invalid_error")};return "",&ServiceError{Code:e.Code,Message:e.Message}}
 return v.Payload,nil
}
func serviceReply(v *OAServiceFrame,payload Raw,err error)(frame []byte,outErr error){
 defer func(){if p:=recover();p!=nil{if e,ok:=p.(*Refusal);ok{frame=nil;outErr=e}else{panic(p)}}}()
 reply:=OAServiceReply{Version:1,Service:v.Service,Method:v.Method,Ok:err==nil,Payload:payload}
 if err!=nil{e:=OAServiceError{Code:"handler_error",Message:"handler failed"};switch x:=err.(type){case *ServiceError:if x.Code!=""{e.Code=x.Code};e.Message=x.Message;case DispatchError:e.Code=string(x);e.Message="";case *Refusal:e.Code=x.Word;e.Message=""};reply.Payload=Raw(encOAServiceError(nil,&e,1))}
 frame=encOAServiceReply(nil,&reply,0)
 // Validate even error diagnostics before making them visible on the wire.
 r:=&reader{buf:frame};r.ws();if _,e:=r.decodeOAServiceReply();e!=nil{return nil,e};return frame,nil
}
`)
	}
	for _, svc := range s.Services {
		fmt.Fprintf(b, "type %s interface{\n", svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "%s(%s)%s\n", exported(m.Name), goArgs(s, m, false), goReturn(s, m, false))
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
			fmt.Fprintf(b, "func(c *%sClient)%s(%s)%s{\n", svc.Name, exported(m.Name), goArgs(s, m, true), goReturn(s, m, true))
			b.WriteString("defer func(){if p:=recover();p!=nil{if e,ok:=p.(*Refusal);ok{err=e}else{panic(p)}}}()\n")
			fmt.Fprintf(b, "args:=%s{", argsName(svc, m))
			for i, f := range m.Args {
				fmt.Fprintf(b, "%s:arg%d,", exported(f.Ident("go")), i)
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
		fmt.Fprintf(b, "func(d *%sDispatcher)WriteFrame(frame []byte)error{v,err:=servicePayload(frame);if err!=nil{return err};if v.Service!=%q{return DispatchError(\"unknown_service\")};switch v.Method{\n", svc.Name, svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "case %q:\n", m.Name)
			if !m.Oneway {
				b.WriteString("return DispatchError(\"wrong_mode\")\n")
				continue
			}
			fmt.Fprintf(b, "r:=&reader{buf:[]byte(v.Arguments),depth:1};r.ws();args,err:=r.decode%s();if err!=nil{return err};r.ws();if r.pos!=len(r.buf){return r.refuse(\"trailing_bytes\")};_ = args;return d.Handler.%s(%s)\n", argsName(svc, m), exported(m.Name), callArgs(m, "go"))
		}
		b.WriteString("default:return DispatchError(\"unknown_method\")}\n}\n")
		if !svcReplies(svc) {
			continue
		}
		fmt.Fprintf(b, "func(d *%sDispatcher)ExchangeFrame(frame []byte)([]byte,error){v,err:=servicePayload(frame);if err!=nil{return nil,err};if v.Service!=%q{return serviceReply(v,\"\",DispatchError(\"unknown_service\"))};switch v.Method{\n", svc.Name, svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "case %q:\n", m.Name)
			if m.Oneway {
				b.WriteString("return serviceReply(v,\"\",DispatchError(\"wrong_mode\"))\n")
				continue
			}
			fmt.Fprintf(b, "r:=&reader{buf:[]byte(v.Arguments),depth:1};r.ws();args,err:=r.decode%s();if err!=nil{return serviceReply(v,\"\",err)};r.ws();if r.pos!=len(r.buf){return serviceReply(v,\"\",r.refuse(\"trailing_bytes\"))};payload,err:=d.invoke%s(args);return serviceReply(v,payload,err)\n", argsName(svc, m), exported(m.Name))
		}
		b.WriteString("default:return serviceReply(v,\"\",DispatchError(\"unknown_method\"))}\n}\n")
		for _, m := range svc.Methods {
			if m.Oneway {
				continue
			}
			fmt.Fprintf(b, "func(d *%sDispatcher)invoke%s(args *%s)(payload Raw,err error){\n", svc.Name, exported(m.Name), argsName(svc, m))
			b.WriteString("defer func(){if p:=recover();p!=nil{payload=\"\";if _,ok:=p.(*Refusal);ok{err=&ServiceError{Code:\"invalid_result\"}}else{err=&ServiceError{Code:\"handler_error\",Message:\"handler failed\"}}}}()\n")
			if m.Result.Type != "void" {
				fmt.Fprintf(b, "var result %s;result,err=d.Handler.%s(%s);", goType(s, m.Result), exported(m.Name), callArgs(m, "go"))
			} else {
				fmt.Fprintf(b, "err=d.Handler.%s(%s);", exported(m.Name), callArgs(m, "go"))
			}
			b.WriteString("if err!=nil{return}\n")
			fmt.Fprintf(b, "value:=%s{", resultName(svc, m))
			if m.Result.Type != "void" {
				b.WriteString("Value:result,")
			}
			b.WriteString("}\n")
			fmt.Fprintf(b, "payload=Raw(enc%s(nil,&value,1));r:=&reader{buf:[]byte(payload),depth:1};r.ws();if _,e:=r.decode%s();e!=nil{payload=\"\";err=&ServiceError{Code:\"invalid_result\"};return};r.ws();if r.pos!=len(r.buf){payload=\"\";err=&ServiceError{Code:\"invalid_result\"}};return\n}\n", resultName(svc, m), resultName(svc, m))
		}
	}
}

func cppService(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	b.WriteString(`
struct FrameWriter{virtual ~FrameWriter()=default;virtual void WriteFrame(std::string_view)=0;};
struct DispatchError:std::runtime_error{using std::runtime_error::runtime_error;};
inline OAServiceFrame service_payload(std::string_view frame){Reader r{frame};r.skip_ws();auto v=decode_oaserviceframe(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse("trailing_bytes");if(v.version!=1)throw DispatchError("unknown_version");return v;}
`)
	if hasReplies(s) {
		b.WriteString(`
struct FrameExchanger{virtual ~FrameExchanger()=default;virtual std::string ExchangeFrame(std::string_view)=0;};
struct ServiceError:std::runtime_error{std::string code,message;ServiceError(std::string c,std::string m):std::runtime_error(m.empty()?c:m),code(c),message(m){}};
inline Raw service_response(std::string_view frame,std::string_view service,std::string_view method){
 Reader r{frame};r.skip_ws();auto v=decode_oaservicereply(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse("trailing_bytes");if(v.version!=1)throw DispatchError("unknown_version");if(v.service!=service||v.method!=method)throw DispatchError("mismatched_response");
 if(!v.ok){Reader e{v.payload};e.depth=1;e.skip_ws();auto error=decode_oaserviceerror(e);e.skip_ws();if(e.pos!=e.buf.size())e.refuse("trailing_bytes");if(error.code.empty())throw DispatchError("invalid_error");throw ServiceError(error.code,error.message);}return v.payload;
}
inline std::string service_reply(const OAServiceFrame& request,const Raw& payload,const ServiceError* error=nullptr){
 OAServiceReply reply;reply.version=1;reply.service=request.service;reply.method=request.method;reply.ok=error==nullptr;reply.payload=payload;
 if(error){OAServiceError e;e.code=error->code.empty()?"handler_error":error->code;e.message=error->message;reply.payload.clear();enc_oaserviceerror(reply.payload,e,1);}
 std::string frame;enc_oaservicereply(frame,reply,0);Reader r{frame};r.skip_ws();decode_oaservicereply(r);return frame;
}
`)
	}
	for _, svc := range s.Services {
		fmt.Fprintf(b, "struct %s{virtual ~%s()=default;\n", svc.Name, svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "virtual %s %s(%s)=0;\n", cppReturn(s, m), m.Name, cppArgs(s, m))
		}
		b.WriteString("};\n")
		fmt.Fprintf(b, "template<class Transport>struct %sClient:%s{Transport& transport_;explicit %sClient(Transport&t):transport_(t){}\n", svc.Name, svc.Name, svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "%s %s(%s)override{%s args;\n", cppReturn(s, m), m.Name, cppArgs(s, m), argsName(svc, m))
			for i, f := range m.Args {
				fmt.Fprintf(b, "args.%s=arg%d;\n", f.Ident("cpp"), i)
			}
			fmt.Fprintf(b, "OAServiceFrame v;v.version=1;v.service=%s;v.method=%s;enc_%s(v.arguments,args,1);std::string frame;enc_oaserviceframe(frame,v,0);service_payload(frame);\n", strconv.Quote(svc.WireName), strconv.Quote(m.Name), lower(argsName(svc, m)))
			if m.Oneway {
				b.WriteString("transport_.WriteFrame(frame);}\n")
				continue
			}
			fmt.Fprintf(b, "auto response=transport_.ExchangeFrame(frame);auto payload=service_response(response,v.service,v.method);Reader r{payload};r.depth=1;r.skip_ws();auto result=decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");\n", lower(resultName(svc, m)))
			if m.Result.Type != "void" {
				b.WriteString("return result.value;\n")
			}
			b.WriteString("}\n")
		}
		b.WriteString("};\n")
		base := "FrameWriter"
		if svcReplies(svc) {
			base += ",FrameExchanger"
		}
		fmt.Fprintf(b, "struct %sDispatcher:%s{%s&handler;explicit %sDispatcher(%s&h):handler(h){}\nvoid WriteFrame(std::string_view frame)override{auto v=service_payload(frame);if(v.service!=%q)throw DispatchError(\"unknown_service\");\n", svc.Name, base, svc.Name, svc.Name, svc.Name, svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "if(v.method==%q){\n", m.Name)
			if !m.Oneway {
				b.WriteString("throw DispatchError(\"wrong_mode\");}\n")
				continue
			}
			fmt.Fprintf(b, "Reader r{v.arguments};r.depth=1;r.skip_ws();auto args=decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");handler.%s(%s);return;}\n", lower(argsName(svc, m)), m.Name, callArgs(m, "cpp"))
		}
		b.WriteString("throw DispatchError(\"unknown_method\");}\n")
		if svcReplies(svc) {
			fmt.Fprintf(b, "std::string ExchangeFrame(std::string_view frame)override{auto v=service_payload(frame);if(v.service!=%q){ServiceError e(\"unknown_service\",\"\");return service_reply(v,\"\",&e);}\ntry{\n", svc.WireName)
			for _, m := range svc.Methods {
				fmt.Fprintf(b, "if(v.method==%q){\n", m.Name)
				if m.Oneway {
					b.WriteString("throw ServiceError(\"wrong_mode\",\"\");}\n")
					continue
				}
				fmt.Fprintf(b, "Reader r{v.arguments};r.depth=1;r.skip_ws();auto args=decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");auto payload=invoke_%s(args);return service_reply(v,payload);}\n", lower(argsName(svc, m)), m.Name)
			}
			b.WriteString("throw ServiceError(\"unknown_method\",\"\");}catch(const ServiceError&e){return service_reply(v,\"\",&e);}catch(const Refusal&e){ServiceError error(e.word,\"\");return service_reply(v,\"\",&error);}\n}\n")
			for _, m := range svc.Methods {
				if m.Oneway {
					continue
				}
				fmt.Fprintf(b, "Raw invoke_%s(const %s&args){\n", m.Name, argsName(svc, m))
				if m.Result.Type != "void" {
					fmt.Fprintf(b, "%s result{};\n", cppReturn(s, m))
				}
				b.WriteString("try{\n")
				if m.Result.Type != "void" {
					b.WriteString("result=")
				}
				fmt.Fprintf(b, "handler.%s(%s);\n", m.Name, callArgs(m, "cpp"))
				b.WriteString("}catch(const ServiceError&){throw;}catch(...){throw ServiceError(\"handler_error\",\"handler failed\");}\ntry{\n")
				fmt.Fprintf(b, "%s value;\n", resultName(svc, m))
				if m.Result.Type != "void" {
					b.WriteString("value.value=result;\n")
				}
				fmt.Fprintf(b, "Raw payload;enc_%s(payload,value,1);Reader r{payload};r.depth=1;r.skip_ws();decode_%s(r);r.skip_ws();if(r.pos!=r.buf.size())r.refuse(\"trailing_bytes\");return payload;\n", lower(resultName(svc, m)), lower(resultName(svc, m)))
				b.WriteString("}catch(...){throw ServiceError(\"invalid_result\",\"\");}\n}\n")
			}
		}
		b.WriteString("};\n")
	}
}

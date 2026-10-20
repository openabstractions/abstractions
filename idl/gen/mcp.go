package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"strings"
)

func validateMCP(s *Definition) error {
	names := map[string]bool{"MCPMethod": true, "MCPMethods": true, "MCPInvoke": true, "MCPServiceError": true}
	check := func(name string) error {
		if names[name] {
			return fmt.Errorf("--mcp generated name collision: %s", name)
		}
		return nil
	}
	for _, st := range s.Structs {
		if err := check(st.Name); err != nil {
			return err
		}
	}
	for _, en := range s.Enums {
		if err := check(en.Name); err != nil {
			return err
		}
	}
	for _, td := range s.Typedefs {
		if err := check(td.Alias); err != nil {
			return err
		}
	}
	for _, svc := range s.Services {
		if err := check(svc.Name); err != nil {
			return err
		}
	}
	for _, c := range s.Consts {
		if err := check(c.Name); err != nil {
			return err
		}
	}
	return nil
}

func mcpClass(m Method) string {
	if m.Trait == "readonly" {
		return "read"
	}
	if m.Trait == "keyed" {
		return "submit"
	}
	return "effect"
}

func mcpManifest(s *Definition) string {
	methods := []map[string]any{}
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			methods = append(methods, map[string]any{"contract": svc.WireName, "method": m.Name, "class": mcpClass(m), "trait": m.Trait, "idempotency_key": m.IdempotencyKey})
		}
	}
	data, _ := json.MarshalIndent(map[string]any{"methods": methods}, "", "  ")
	return string(data) + "\n"
}

// genMCP joins the carrier to the existing typed client and record codecs.
// Inventory selection and transport exclusions belong to the gateway owner.
func genMCP(s *Definition) string {
	var b strings.Builder
	schemas := jsonSchemaRecords(serviceTypes(s))
	b.WriteString("package rec\n\ntype MCPMethod struct { Contract, Method, Class, Trait, IdempotencyKey, Documentation, ArgumentsSchema, ResultSchema string }\n")
	b.WriteString("func MCPMethods() []MCPMethod { return []MCPMethod{\n")
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			fmt.Fprintf(&b, "{%q,%q,%q,%q,%q,%q,%q,%q},\n", svc.WireName, m.Name, mcpClass(m), m.Trait, m.IdempotencyKey, m.Doc, schemas[argsName(svc, m)+".schema.json"], schemas[resultName(svc, m)+".schema.json"])
		}
	}
	b.WriteString("}}\n")
	b.WriteString("// MCPServiceError retains a native typed refusal; transport and uncertain-result errors remain errors.\nfunc MCPServiceError(err error)([]byte,bool){if e,ok:=err.(*ServiceError);ok{return encOAServiceError(nil,&OAServiceError{Code:string(e.Code),Message:e.Message},0),true};return nil,false}\n")
	b.WriteString("// MCPInvoke validates arguments and results with the native generated codecs. It retries no call.\nfunc MCPInvoke(t interface { ExchangeFrame([]byte)([]byte,error); WriteFrame([]byte)error }, contract, method string, input []byte)(out []byte,err error){\ndefer func(){if p:=recover();p!=nil{if e,ok:=p.(*Refusal);ok{out=nil;err=e}else{panic(p)}}}()\nswitch contract {\n")
	for _, svc := range s.Services {
		fmt.Fprintf(&b, "case %q: switch method {\n", svc.WireName)
		for _, m := range svc.Methods {
			fmt.Fprintf(&b, "case %q:\nr:=&reader{buf:input,depth:1};r.ws();args,e:=r.decode%s();if e!=nil{return nil,e};r.ws();if r.pos!=len(r.buf){return nil,r.refuse(\"trailing_bytes\")};_ = args\n", m.Name, argsName(svc, m))
			prefix := "e="
			if !m.Oneway && m.Result.Type != "void" {
				prefix = "value,e:="
			}
			fmt.Fprintf(&b, "%sNew%sClient(t).%s(%s);if e!=nil{return nil,e}\n", prefix, svc.Name, goName(m.Name), callArgs(m, "go"))
			if m.Oneway {
				b.WriteString("return []byte(\"{}\"),nil\n")
				continue
			}
			fmt.Fprintf(&b, "result:=%s{", resultName(svc, m))
			if m.Result.Type != "void" {
				b.WriteString("Value:value")
			}
			fmt.Fprintf(&b, "};return enc%s(nil,&result,1),nil\n", resultName(svc, m))
		}
		b.WriteString("}\n")
	}
	b.WriteString("};return nil,DispatchError(\"unknown_method\")\n}\n")
	body := goUnexportCarriers(b.String(), s)
	if formatted, e := format.Source([]byte(body)); e == nil {
		return string(formatted)
	}
	return body
}

package main

import (
	"fmt"
	"strings"
)

func validateRustServices(s *Definition) error {
	if err := validateRustVocabularies(s); err != nil {
		return err
	}
	if len(s.Services) == 0 {
		return nil
	}
	used := map[string]bool{}
	for _, n := range strings.Fields("FrameTransport CallError ServiceError service_decode service_encode") {
		used[n] = true
	}
	for _, st := range s.Structs {
		if used[st.Name] || namespaceKeyword("rust", st.Name) {
			return fmt.Errorf("Rust service identifier collision %q", st.Name)
		}
		used[st.Name] = true
		for _, f := range st.Fields {
			if namespaceKeyword("rust", f.Ident("rust")) || strings.Contains(f.Ident("rust"), ".") {
				return fmt.Errorf("invalid Rust service record field %q", f.Ident("rust"))
			}
		}
	}
	for _, svc := range s.Services {
		for _, n := range []string{svc.Name, svc.Name + "Client"} {
			if used[n] || namespaceKeyword("rust", n) {
				return fmt.Errorf("Rust service identifier collision %q", n)
			}
			used[n] = true
		}
		methods := map[string]string{}
		for _, m := range svc.Methods {
			name := rustIdent(m.Name)
			if name == "new" || name == "transport" {
				return fmt.Errorf("Rust service method collision %q", m.Name)
			}
			if other, ok := methods[name]; ok {
				return fmt.Errorf("Rust service methods %s and %s are both %s", other, m.Name, name)
			}
			methods[name] = m.Name
			// Arguments become generated record fields; a keyword needs rust.name.
			for _, a := range m.Args {
				if namespaceKeyword("rust", a.Ident("rust")) || strings.Contains(a.Ident("rust"), ".") {
					return fmt.Errorf("invalid Rust service argument %q of %s; set rust.name", a.Ident("rust"), m.Name)
				}
			}
		}
	}
	return nil
}

// validateRustVocabularies refuses a vocabulary whose members collide once
// spelled as Rust variants or constants.
func validateRustVocabularies(s *Definition) error {
	for _, en := range s.Enums {
		seen := map[string]string{}
		reserved := "ALL"
		for _, m := range en.Members {
			name := upperCamelName(m.Name)
			if en.Ann["unknown"] != "refuse" {
				name = screamingName(m.Name)
			}
			if other, ok := seen[name]; ok {
				return fmt.Errorf("enum %s: members %s and %s are both %s in Rust", en.Name, other, m.Name, name)
			}
			if name == reserved || name == "Self" {
				return fmt.Errorf("enum %s: member %s is spelled %s in Rust, which the generated vocabulary reserves", en.Name, m.Name, name)
			}
			seen[name] = m.Name
		}
	}
	return nil
}

func rsServices(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	if !s.NoIPC {
		for _, st := range s.Structs {
			var body strings.Builder
			emitEqualities(&body, st, "rust", false)
			for _, f := range st.Fields {
				e := "v." + f.Ident("rust")
				if elem := s.Repeated(f.Type); elem != "" {
					fmt.Fprintf(&body, "    for value in &%s {\n        service_check_%s(value)?;\n    }\n", e, rsFn(elem))
				} else if s.IsStruct(f.Type) {
					if f.Omit == "absent" {
						fmt.Fprintf(&body, "    if let Some(value) = &%s {\n        service_check_%s(value)?;\n    }\n", e, rsFn(f.Type))
					} else {
						fmt.Fprintf(&body, "    service_check_%s(&%s)?;\n", rsFn(f.Type), e)
					}
				}
			}
			param := "v"
			if body.Len() == 0 {
				param = "_v"
			}
			fmt.Fprintf(b, "\nfn service_check_%s(%s: &%s) -> Result<(), Refusal> {\n", rsFn(st.Name), param, st.Name)
			if strings.Contains(body.String(), "r.refuse(") {
				b.WriteString("    let r = Reader { buf: &[], pos: 0, depth: 0 };\n")
			}
			b.WriteString(body.String())
			b.WriteString("    Ok(())\n}\n")
		}
		if s.SharedRustTransport {
			b.WriteString("\n/// Common generated framing boundary; implementation owns waiting and identity.\npub use abstraction_frame::FrameTransport;\n")
		} else {
			b.WriteString(`
/// The adapter owns waiting budgets and cancellation. Calls are never retried.
pub trait FrameTransport {
    type Error;
    fn write_frame(&self, frame: &[u8]) -> Result<(), Self::Error>;
    fn exchange_frame(&self, frame: &[u8]) -> Result<Vec<u8>, Self::Error>;
}`)
		}
		b.WriteString(`
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ServiceError {
    pub code: String,
    pub message: String,
}

#[derive(Debug)]
pub enum CallError<E> {
    /// Transport failure does not establish whether the receiver accepted work.
    Transport(E),
    Refusal(Refusal),
    Dispatch(&'static str),
    Service(ServiceError),
}

fn service_decode<T>(data: &[u8], decode: fn(&mut Reader) -> Result<T, Refusal>) -> Result<T, Refusal> {
    let mut r = Reader { buf: data, pos: 0, depth: 0 };
    r.skip_ws();
    let value = decode(&mut r)?;
    r.skip_ws();
    if r.pos != data.len() {
        return r.refuse("trailing_bytes");
    }
    Ok(value)
}

fn service_encode<T>(value: &T, check: fn(&T) -> Result<(), Refusal>, encode: fn(&mut Vec<u8>, &T, i32), decode: fn(&mut Reader) -> Result<T, Refusal>) -> Result<Vec<u8>, Refusal> {
    check(value)?;
    let mut data = Vec::new();
    encode(&mut data, value, 0);
    service_decode(&data, decode)?;
    Ok(data)
}
`)
	}
	for _, svc := range s.Services {
		if svc.Doc != "" {
			b.WriteString("\n" + rsDocComment(svc.Doc, ""))
		} else {
			b.WriteString("\n")
		}
		fmt.Fprintf(b, "pub trait %s {\n    type Error;\n", svc.Name)
		for _, m := range svc.Methods {
			b.WriteString(rsDocComment(m.Doc, "    "))
			fmt.Fprintf(b, "    fn %s(&self%s) -> Result<%s, Self::Error>;\n", rustIdent(m.Name), rsServiceArgs(s, m), rsServiceResult(s, m))
		}
		b.WriteString("}\n")
		if s.NoIPC {
			continue
		}
		fmt.Fprintf(b, "\npub struct %sClient<T> {\n    transport: T,\n}\n\nimpl<T> %sClient<T> {\n    pub fn new(transport: T) -> Self {\n        Self { transport }\n    }\n\n    pub fn transport(&self) -> &T {\n        &self.transport\n    }\n}\n", svc.Name, svc.Name)
		fmt.Fprintf(b, "\nimpl<T: FrameTransport> %s for %sClient<T> {\n    type Error = CallError<T::Error>;\n", svc.Name, svc.Name)
		for i, m := range svc.Methods {
			if i > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(b, "    fn %s(&self%s) -> Result<%s, Self::Error> {\n", rustIdent(m.Name), rsServiceArgs(s, m), rsServiceResult(s, m))
			var names []string
			for _, f := range m.Args {
				names = append(names, f.Ident("rust"))
			}
			if len(names) == 0 {
				fmt.Fprintf(b, "        let args = %s {};\n", argsName(svc, m))
			} else {
				fmt.Fprintf(b, "        let args = %s { %s };\n", argsName(svc, m), strings.Join(names, ", "))
			}
			fmt.Fprintf(b, "        let arguments = service_encode(&args, service_check_%s, enc_%s, decode_%s).map_err(CallError::Refusal)?;\n", rsFn(argsName(svc, m)), rsFn(argsName(svc, m)), rsFn(argsName(svc, m)))
			fmt.Fprintf(b, "        let request = OAServiceFrame { version: 1, service: %q.into(), method: %q.into(), arguments };\n", svc.WireName, m.Name)
			b.WriteString("        let frame = service_encode(&request, service_check_oa_service_frame, enc_oa_service_frame, decode_oa_service_frame).map_err(CallError::Refusal)?;\n")
			if m.Oneway {
				b.WriteString("        self.transport.write_frame(&frame).map_err(CallError::Transport)\n    }\n")
				continue
			}
			b.WriteString("        let reply = self.transport.exchange_frame(&frame).map_err(CallError::Transport)?;\n        let reply = service_decode(&reply, decode_oa_service_reply).map_err(CallError::Refusal)?;\n        if reply.version != 1 {\n            return Err(CallError::Dispatch(\"unknown_version\"));\n        }\n")
			fmt.Fprintf(b, "        if reply.service != %q || reply.method != %q {\n            return Err(CallError::Dispatch(\"mismatched_response\"));\n        }\n", svc.WireName, m.Name)
			b.WriteString("        if !reply.ok {\n            let error = service_decode(&reply.payload, decode_oa_service_error).map_err(CallError::Refusal)?;\n            if error.code.is_empty() {\n                return Err(CallError::Dispatch(\"invalid_error\"));\n            }\n            return Err(CallError::Service(ServiceError { code: error.code, message: error.message }));\n        }\n")
			if m.Result.Type == "void" {
				fmt.Fprintf(b, "        service_decode(&reply.payload, decode_%s).map_err(CallError::Refusal)?;\n        Ok(())\n", rsFn(resultName(svc, m)))
			} else {
				fmt.Fprintf(b, "        let result = service_decode(&reply.payload, decode_%s).map_err(CallError::Refusal)?;\n        Ok(result.value)\n", rsFn(resultName(svc, m)))
			}
			b.WriteString("    }\n")
		}
		b.WriteString("}\n")
	}
}

func rsServiceArgs(s *Definition, m Method) string {
	var b strings.Builder
	for _, f := range m.Args {
		fmt.Fprintf(&b, ", %s: %s", f.Ident("rust"), rsType(s, f))
	}
	return b.String()
}

// rsDocComment renders a definition doc annotation as /// lines.
func rsDocComment(doc, indent string) string {
	if strings.TrimSpace(doc) == "" {
		return ""
	}
	return structDoc(Struct{Ann: map[string]string{"doc": doc}}, indent+"/// ")
}

func rsServiceResult(s *Definition, m Method) string {
	if m.Result.Type == "void" {
		return "()"
	}
	return rsType(s, m.Result)
}

package main

import (
	"fmt"
	"strings"
)

func validateRustServices(s *Definition) error {
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
		for _, m := range svc.Methods {
			if namespaceKeyword("rust", m.Name) || m.Name == "new" || m.Name == "transport" {
				return fmt.Errorf("Rust service method collision %q", m.Name)
			}
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
			fmt.Fprintf(b, "\n#[allow(unused_variables)]\nfn service_check_%s(v: &%s) -> Result<(), Refusal> {\n    let r = Reader { buf: &[], pos: 0, depth: 0 };\n", lower(st.Name), st.Name)
			emitEqualities(b, st, "rust", false)
			emitEnumChecks(b, st, "rust", false)
			for _, f := range st.Fields {
				e := "v." + f.Ident("rust")
				if elem := s.Repeated(f.Type); elem != "" {
					fmt.Fprintf(b, "    for value in &%s { service_check_%s(value)?; }\n", e, lower(elem))
				} else if s.IsStruct(f.Type) {
					if f.Omit == "absent" {
						fmt.Fprintf(b, "    if let Some(value) = &%s { service_check_%s(value)?; }\n", e, lower(f.Type))
					} else {
						fmt.Fprintf(b, "    service_check_%s(&%s)?;\n", lower(f.Type), e)
					}
				}
			}
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
#[derive(Debug)]
pub struct ServiceError { pub code: String, pub message: String }
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
    if r.pos != data.len() { return r.refuse("trailing_bytes"); }
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
		fmt.Fprintf(b, "\n#[allow(non_snake_case)]\npub trait %s {\n    type Error;\n", svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "    fn %s(&self%s) -> Result<%s, Self::Error>;\n", m.Name, rsServiceArgs(s, m), rsServiceResult(s, m))
		}
		b.WriteString("}\n")
		if s.NoIPC {
			continue
		}
		fmt.Fprintf(b, "\npub struct %sClient<T> { transport: T }\nimpl<T> %sClient<T> {\n    pub fn new(transport: T) -> Self { Self { transport } }\n    pub fn transport(&self) -> &T { &self.transport }\n}\n", svc.Name, svc.Name)
		fmt.Fprintf(b, "#[allow(non_snake_case)]\nimpl<T: FrameTransport> %s for %sClient<T> {\n    type Error = CallError<T::Error>;\n", svc.Name, svc.Name)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "    fn %s(&self%s) -> Result<%s, Self::Error> {\n", m.Name, rsServiceArgs(s, m), rsServiceResult(s, m))
			fmt.Fprintf(b, "        let args = %s {", argsName(svc, m))
			for i, f := range m.Args {
				fmt.Fprintf(b, " %s: arg%d,", f.Ident("rust"), i)
			}
			b.WriteString(" };\n")
			fmt.Fprintf(b, "        let arguments = service_encode(&args, service_check_%s, enc_%s, decode_%s).map_err(CallError::Refusal)?;\n", lower(argsName(svc, m)), lower(argsName(svc, m)), lower(argsName(svc, m)))
			fmt.Fprintf(b, "        let request = OAServiceFrame { version: 1, service: %q.into(), method: %q.into(), arguments };\n", svc.WireName, m.Name)
			b.WriteString("        let frame = service_encode(&request, service_check_oaserviceframe, enc_oaserviceframe, decode_oaserviceframe).map_err(CallError::Refusal)?;\n")
			if m.Oneway {
				b.WriteString("        self.transport.write_frame(&frame).map_err(CallError::Transport)\n    }\n")
				continue
			}
			b.WriteString("        let reply = self.transport.exchange_frame(&frame).map_err(CallError::Transport)?;\n        let reply = service_decode(&reply, decode_oaservicereply).map_err(CallError::Refusal)?;\n        if reply.version != 1 { return Err(CallError::Dispatch(\"unknown_version\")); }\n")
			fmt.Fprintf(b, "        if reply.service != %q || reply.method != %q { return Err(CallError::Dispatch(\"mismatched_response\")); }\n", svc.WireName, m.Name)
			b.WriteString("        if !reply.ok {\n            let error = service_decode(&reply.payload, decode_oaserviceerror).map_err(CallError::Refusal)?;\n            if error.code.is_empty() { return Err(CallError::Dispatch(\"invalid_error\")); }\n            return Err(CallError::Service(ServiceError { code: error.code, message: error.message }));\n        }\n")
			fmt.Fprintf(b, "        let result = service_decode(&reply.payload, decode_%s).map_err(CallError::Refusal)?;\n", lower(resultName(svc, m)))
			if m.Result.Type == "void" {
				b.WriteString("        let _ = result;\n        Ok(())\n")
			} else {
				b.WriteString("        Ok(result.value)\n")
			}
			b.WriteString("    }\n")
		}
		b.WriteString("}\n")
	}
}

func rsServiceArgs(s *Definition, m Method) string {
	var b strings.Builder
	for i, f := range m.Args {
		fmt.Fprintf(&b, ", arg%d: %s", i, rsType(s, f))
	}
	return b.String()
}
func rsServiceResult(s *Definition, m Method) string {
	if m.Result.Type == "void" {
		return "()"
	}
	return rsType(s, m.Result)
}

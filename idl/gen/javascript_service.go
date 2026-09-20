package main

import (
	"fmt"
	"strconv"
	"strings"
)

// validateJSNames refuses a definition whose JavaScript spelling would collide.
// Properties may be reserved words; parameters may not, and JavaScript has no
// trailing-underscore convention, so such a contract name needs javascript.name.
func validateJSNames(s *Definition) error {
	for _, st := range s.Structs {
		props := map[string]string{}
		if st.PreservesUnknown() {
			props["extras"] = "extras"
		}
		for _, f := range st.Fields {
			n := f.Ident("javascript")
			if prior, ok := props[n]; ok {
				return fmt.Errorf("JavaScript property %q is both %s.%s and %s.%s; add javascript.name to one", n, st.Name, prior, st.Name, f.Name)
			}
			props[n] = f.Name
		}
	}
	for _, en := range s.Enums {
		members := map[string]string{}
		for _, m := range en.Members {
			n := jsPascal(m.Name)
			if o, ok := m.Ann["javascript.name"]; ok {
				n = o
			}
			if prior, ok := members[n]; ok {
				return fmt.Errorf("JavaScript member %s.%s is both %s and %s", pascalCase(en.Name), n, prior, m.Name)
			}
			members[n] = m.Name
		}
	}
	for _, svc := range s.Services {
		methods := map[string]string{}
		for _, m := range svc.Methods {
			n := camelCase(m.Name)
			if prior, ok := methods[n]; ok {
				return fmt.Errorf("JavaScript method %s.%s is both %s and %s", pascalCase(svc.Name), n, prior, m.Name)
			}
			methods[n] = m.Name
			params := map[string]string{}
			for _, f := range m.Args {
				p := f.Ident("javascript")
				if jsReservedParameter(p) {
					return fmt.Errorf("JavaScript parameter %q of %s is a reserved word; add javascript.name to %s", p, m.Name, f.Name)
				}
				if prior, ok := params[p]; ok {
					return fmt.Errorf("JavaScript parameter %q of %s is both %s and %s; add javascript.name to one", p, m.Name, prior, f.Name)
				}
				params[p] = f.Name
			}
		}
	}
	return nil
}

func validateJSServices(s *Definition) error {
	if err := validateJSNames(s); err != nil {
		return err
	}
	if len(s.Services) == 0 {
		return nil
	}
	reserved := "Out Reader Refusal DispatchError ServiceError ENC HEX _serviceRecords _serviceCheck _serviceDecode _serviceEncode _serviceRequest _serviceResponse"
	if hasBinary(s) {
		reserved += " binaryPresent encodeBinary readBinary"
	}
	used := map[string]bool{}
	for _, n := range strings.Fields(reserved) {
		used[n] = true
	}
	valid := func(n string) bool {
		if n == "" || strings.Contains(n, ".") || namespaceKeyword("javascript", n) {
			return false
		}
		for i := 0; i < len(n); i++ {
			if i == 0 && !isIdentStart(n[i]) || i > 0 && !isIdent(n[i]) {
				return false
			}
		}
		return true
	}
	for _, st := range s.Structs {
		if !valid(jsStem(st.Name)) || used[jsStem(st.Name)] {
			return fmt.Errorf("javascript service record name %q is reserved", st.Name)
		}
		for _, f := range st.Fields {
			if !valid(f.Ident("javascript")) {
				return fmt.Errorf("javascript field identifier %q is invalid", f.Ident("javascript"))
			}
			if strings.Contains(f.Type, ".") && !jsImportedType(s, f.Type) {
				return fmt.Errorf("javascript service field %s needs unsupported %s codecs", f.Name, f.Type)
			}
		}
	}
	for _, svc := range s.Services {
		if !valid(pascalCase(svc.Name)) || used[pascalCase(svc.Name)] {
			return fmt.Errorf("javascript service name %q is reserved", svc.Name)
		}
		for _, m := range svc.Methods {
			n := camelCase(m.Name)
			if !valid(n) || n == "constructor" || n == "then" || n == "_transport" {
				return fmt.Errorf("javascript service method name %q is reserved", m.Name)
			}
			for _, f := range append(append([]Field{}, m.Args...), m.Result) {
				if strings.Contains(f.Type, ".") && !jsImportedType(s, f.Type) {
					return fmt.Errorf("javascript service value needs unsupported %s codecs", f.Type)
				}
			}
		}
	}
	return nil
}

func jsParams(m Method) string {
	var params []string
	for _, f := range m.Args {
		params = append(params, f.Ident("javascript"))
	}
	return strings.Join(params, ", ")
}

// jsInterface is the method shape a --no-ipc definition offers an implementer.
func jsInterface(b *strings.Builder, svc Service) {
	fmt.Fprintf(b, "\nexport class %s {\n", pascalCase(svc.Name))
	for _, m := range svc.Methods {
		fmt.Fprintf(b, "  async %s(%s) { throw new Error(\"not implemented\"); }\n", camelCase(m.Name), jsParams(m))
	}
	b.WriteString("}\n")
}

func jsService(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	common := jsServiceCommon
	if hasBinary(s) {
		common = strings.Replace(common, `else if (kind === "json")`, `else if (kind === "binary") valid = value instanceof Uint8Array;
  else if (kind === "json")`, 1)
	}
	b.WriteString(common)
	b.WriteString("\nconst _serviceRecords = Object.create(null);\n")
	for _, st := range s.Structs {
		fmt.Fprintf(b, "_serviceRecords[%q] = [", st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(b, "[%q,%q,%q],", f.Ident("javascript"), f.Type, f.Omit)
		}
		b.WriteString("];\n")
	}
	b.WriteString(`
function _serviceRequest(service, method, argumentsBytes) {
  return _serviceEncode(writeOAServiceFrame, {
    version:1, service, method, arguments:new TextDecoder("utf-8",{fatal:true}).decode(argumentsBytes)
  },0);
}
`)
	if hasReplies(s) {
		b.WriteString(`
function _serviceResponse(frame, service, method) {
  if (!(frame instanceof Uint8Array)) throw new TypeError("transport frame must be Uint8Array");
  const reply = _serviceDecode(readOAServiceReply, frame, 0);
  if (reply.version !== 1) throw new DispatchError("unknown_version");
  if (reply.service !== service || reply.method !== method) throw new DispatchError("mismatched_response");
  if (!reply.ok) {
    const error = _serviceDecode(readOAServiceError, reply.payload, 1);
    if (!error.code) throw new DispatchError("invalid_error");
    throw new ServiceError(error.code, error.message);
  }
  return reply.payload;
}
`)
	}
	for _, svc := range s.Services {
		fmt.Fprintf(b, "\nexport class %sClient {\n  constructor(transport) { this._transport = transport; }\n", pascalCase(svc.Name))
		for _, m := range svc.Methods {
			args := jsStem(argsName(svc, m))
			fmt.Fprintf(b, "\n  async %s(%s) {\n    const _args = new%s();\n", camelCase(m.Name), jsParams(m), args)
			for _, f := range m.Args {
				fmt.Fprintf(b, "    _args.%s = %s;\n", f.Ident("javascript"), f.Ident("javascript"))
			}
			fmt.Fprintf(b, "    _serviceCheck(%q, _args);\n    const _payload = _serviceEncode(write%s, _args, 1);\n    _serviceDecode(read%s, _payload, 1);\n    const _request = _serviceRequest(%q, %q, _payload);\n", argsName(svc, m), args, args, svc.WireName, m.Name)
			if m.Oneway {
				b.WriteString("    await this._transport.writeFrame(_request);\n")
			} else {
				fmt.Fprintf(b, "    const _reply = _serviceResponse(await this._transport.exchangeFrame(_request), %q, %q);\n    const _result = _serviceDecode(read%s, _reply, 1);\n", svc.WireName, m.Name, jsStem(resultName(svc, m)))
				if m.Result.Type != "void" {
					b.WriteString("    return _result.value;\n")
				}
			}
			b.WriteString("  }\n")
		}
		b.WriteString("}\n")
		fmt.Fprintf(b, "export const %sService = Object.freeze({wireName:%s,Client:%sClient});\n", pascalCase(svc.Name), strconv.Quote(svc.WireName), pascalCase(svc.Name))
	}
}

const jsServiceCommon = `
export class DispatchError extends Error {
  constructor(code) { super(code); this.name = "DispatchError"; this.code = code; }
}
export class ServiceError extends Error {
  constructor(code, message = "") { super(message || code); this.name = "ServiceError"; this.code = code; }
}
function _serviceDecode(decoder, frame, depth) {
  const reader = new Reader(typeof frame === "string" ? ENC.encode(frame) : frame);
  reader.depth = depth; reader.ws();
  const value = decoder(reader); reader.ws();
  if (reader.pos !== reader.buf.length) throw reader.refuse("trailing_bytes");
  return value;
}
function _serviceEncode(encoder, value, depth) {
  const out = new Out(); encoder(out, value, depth); return out.bytes();
}
function _serviceCheck(kind, value, depth = 0) {
  if (depth > DEPTH_LIMIT) throw new Refusal("depth_exceeded", 0);
  let valid = false;
  if (kind === "string") {
    valid = typeof value === "string";
    if (valid) for (const ch of value) {
      const cp = ch.codePointAt(0);
      if (cp >= 0xd800 && cp <= 0xdfff) throw new Refusal("bad_string", 0);
    }
  } else if (kind === "i32") {
    valid = typeof value === "number" && Number.isInteger(value) && value >= -2147483648 && value <= 2147483647;
  } else if (kind === "i64") {
    valid = typeof value === "bigint" && value >= -9223372036854775808n && value <= 9223372036854775807n;
  } else if (kind === "bool") valid = typeof value === "boolean";
  else if (kind === "json") {
    valid = typeof value === "string" || value instanceof Uint8Array;
    if (valid) { const r = new Reader(typeof value === "string" ? ENC.encode(value) : value); r.depth = depth; r.ws(); r.rawValue(); r.ws(); if (r.pos !== r.buf.length) throw r.refuse("trailing_bytes"); }
  } else if (kind.startsWith("list<")) {
    valid = Array.isArray(value);
    if (valid) for (const item of value) _serviceCheck(kind.slice(5,-1), item, depth + 1);
  } else if (kind.startsWith("map<string,")) {
    valid = value !== null && typeof value === "object" && !Array.isArray(value);
    if (valid) for (const [key,item] of Object.entries(value)) { _serviceCheck("string",key,depth+1); _serviceCheck(kind.slice(11,-1),item,depth+1); }
  } else {
    const fields = _serviceRecords[kind];
    valid = fields !== undefined && value !== null && typeof value === "object" && !Array.isArray(value);
    if (valid) for (const [name,type,omit] of fields) {
      if (!Object.hasOwn(value,name)) throw new Refusal("missing_field",0);
      const item = value[name];
      if (omit === "absent" && item === null) continue;
      _serviceCheck(type,item,depth+1);
    }
  }
  if (!valid) throw new Refusal("wrong_type", 0);
}
`

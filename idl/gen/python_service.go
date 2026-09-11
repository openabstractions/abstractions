package main

import (
	"fmt"
	"strings"
)

func validatePythonServices(s *Definition) error {
	if len(s.Services) == 0 {
		return nil
	}
	reserved := "bytes bytearray memoryview str int bool list dict set type len isinstance getattr hasattr super object Exception TypeError ValueError UnicodeError NotImplementedError Refusal decode encode esc esc_byte num pad strs raw rawmap strmap enc_list _derive _Reader _service_decode _service_request _service_response _service_encode _service_check _SERVICE_RECORDS"
	used := map[string]bool{}
	for _, n := range strings.Fields(reserved) {
		used[n] = true
	}
	valid := func(n string) bool {
		if n == "" || namespaceKeyword("python", n) || strings.Contains(n, ".") {
			return false
		}
		for i := 0; i < len(n); i++ {
			if (i == 0 && !isIdentStart(n[i])) || (i > 0 && !isIdent(n[i])) {
				return false
			}
		}
		return true
	}
	claim := func(n string) error {
		if !valid(n) || used[n] {
			return fmt.Errorf("invalid or colliding Python service identifier %q", n)
		}
		used[n] = true
		return nil
	}
	for _, st := range serviceTypes(s).Structs {
		for _, n := range []string{st.Name, "_decode_" + lower(st.Name), "enc_" + lower(st.Name)} {
			if e := claim(n); e != nil {
				return e
			}
		}
		attrs := map[string]bool{}
		for _, f := range st.Fields {
			n := f.Ident("python")
			if !valid(n) || strings.HasPrefix(n, "__") || attrs[n] {
				return fmt.Errorf("invalid or colliding Python field identifier %q", n)
			}
			attrs[n] = true
		}
	}
	for _, en := range s.Enums {
		for _, n := range []string{upper(en.Name) + "_NAMES", upper(en.Name) + "_UNKNOWN"} {
			if e := claim(n); e != nil {
				return e
			}
		}
		for _, key := range en.MemberAnn() {
			if e := claim(upper(en.Name) + "_" + upper(key)); e != nil {
				return e
			}
		}
	}
	for _, c := range s.Consts {
		if e := claim(upper(c.Name)); e != nil {
			return e
		}
	}
	for _, svc := range s.Services {
		for _, n := range []string{svc.Name, svc.Name + "Client"} {
			if e := claim(n); e != nil {
				return e
			}
		}
		for _, m := range svc.Methods {
			if !valid(m.Name) || m.Name == "_transport" || strings.HasPrefix(m.Name, "__") {
				return fmt.Errorf("invalid Python service method %q", m.Name)
			}
			for _, f := range m.Args {
				if f.Ident("python") == "self" || used[f.Ident("python")] {
					return fmt.Errorf("Python service argument shadows generated name; use python.name alias")
				}
			}
		}
	}
	return nil
}
func pyServiceType(f Field) string {
	switch f.Type {
	case "void":
		return "None"
	case "i32", "i64":
		return "int"
	case "string":
		return "str"
	case "bool":
		return "bool"
	case "json":
		return "bytes | str"
	}
	if strings.HasPrefix(f.Type, "list<") {
		return "list"
	}
	if strings.HasPrefix(f.Type, "map<") {
		return "dict"
	}
	return f.Type
}
func pyService(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	b.WriteString(pyServiceCommon)
	if hasReplies(s) {
		b.WriteString(pyServiceResponse)
	}
	b.WriteString("\n_SERVICE_RECORDS = {\n")
	for _, st := range s.Structs {
		fmt.Fprintf(b, "    %q: (%s, [", st.Name, st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(b, "(%q,%q,%q),", f.Ident("python"), f.Type, f.Omit)
		}
		b.WriteString("]),\n")
	}
	b.WriteString("}\n")
	for _, svc := range s.Services {
		fmt.Fprintf(b, "\n\nclass %s:\n", svc.Name)
		fmt.Fprintf(b, "    __doc__ = %q\n", svc.Doc)
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "    def %s(self", m.Name)
			for _, f := range m.Args {
				fmt.Fprintf(b, ", %s: %q", f.Ident("python"), pyServiceType(f))
			}
			fmt.Fprintf(b, ") -> %q:\n        raise NotImplementedError\n", pyServiceType(m.Result))
		}
		fmt.Fprintf(b, "\n\nclass %sClient(%s):\n    def __init__(self, transport):\n        self._transport = transport\n", svc.Name, svc.Name)
		for _, m := range svc.Methods {
			pyClientMethod(b, svc, m)
		}
	}
}

func pyClientMethod(b *strings.Builder, svc Service, m Method) {
	used := map[string]bool{}
	for _, f := range m.Args {
		used[f.Ident("python")] = true
	}
	local := func(n string) string {
		for used[n] {
			n += "_"
		}
		used[n] = true
		return n
	}
	args, arguments, request, payload, result := local("_oa_args"), local("_oa_arguments"), local("_oa_request"), local("_oa_payload"), local("_oa_result")
	fmt.Fprintf(b, "\n    def %s(self", m.Name)
	for _, f := range m.Args {
		fmt.Fprintf(b, ", %s: %q", f.Ident("python"), pyServiceType(f))
	}
	fmt.Fprintf(b, ") -> %q:\n", pyServiceType(m.Result))
	for _, f := range m.Args {
		fmt.Fprintf(b, "        _service_check(%q, %s)\n", f.Type, f.Ident("python"))
	}
	fmt.Fprintf(b, "        %s = %s()\n", args, argsName(svc, m))
	for _, f := range m.Args {
		fmt.Fprintf(b, "        %s.%s = %s\n", args, f.Ident("python"), f.Ident("python"))
	}
	fmt.Fprintf(b, "        %s = _service_encode(enc_%s, %s, 1)\n        _service_decode(_decode_%s, %s, 1)\n", arguments, lower(argsName(svc, m)), args, lower(argsName(svc, m)), arguments)
	fmt.Fprintf(b, "        %s = _service_request(%q, %q, %s)\n", request, svc.WireName, m.Name, arguments)
	if m.Oneway {
		fmt.Fprintf(b, "        self._transport.write_frame(%s)\n        return None\n", request)
		return
	}
	fmt.Fprintf(b, "        %s = _service_response(self._transport.exchange_frame(%s), %q, %q)\n        %s = _service_decode(_decode_%s, %s, 1)\n", payload, request, svc.WireName, m.Name, result, lower(resultName(svc, m)), payload)
	if m.Result.Type == "void" {
		b.WriteString("        return None\n")
	} else {
		fmt.Fprintf(b, "        return %s.value\n", result)
	}
}

const pyServiceCommon = `

class DispatchError(ValueError):
    def __init__(self, code):
        super().__init__(code)
        self.code = code


def _service_decode(decoder, frame, depth=0):
    if not isinstance(frame, (bytes, bytearray, memoryview)):
        raise TypeError("transport frame must be bytes")
    r = _Reader(bytes(frame))
    r.depth = depth
    r.ws()
    value = decoder(r)
    r.ws()
    if r.pos != len(r.buf):
        raise r.refuse("trailing_bytes")
    return value


def _service_encode(encoder, value, depth):
    out = bytearray()
    try:
        encoder(out, value, depth)
    except UnicodeError:
        raise Refusal("bad_string", 0) from None
    return bytes(out)


def _service_request(service, method, arguments):
    v = OAServiceFrame(version=1, service=service, method=method, arguments=arguments)
    frame = _service_encode(enc_oaserviceframe, v, 0)
    _service_decode(_decode_oaserviceframe, frame)
    return frame


def _service_check(kind, value, depth=0):
    # Python coercions must not silently change typed service arguments.
    if depth >= _DEPTH_LIMIT and (kind.startswith(("list<", "map<")) or kind in _SERVICE_RECORDS):
        raise Refusal("depth_exceeded", 0)
    if kind == "string":
        valid = type(value) is str
    elif kind in ("i32", "i64"):
        valid = type(value) is int
        if valid:
            limit = 1 << (31 if kind == "i32" else 63)
            if not -limit <= value < limit:
                raise Refusal("number_spelling", 0)
    elif kind == "bool":
        valid = type(value) is bool
    elif kind == "json":
        valid = isinstance(value, (str, bytes, bytearray, memoryview))
    elif kind.startswith("list<"):
        valid = type(value) is list
        if valid:
            for item in value:
                _service_check(kind[5:-1], item, depth + 1)
    elif kind.startswith("map<string,"):
        valid = type(value) is dict
        if valid:
            for key, item in value.items():
                _service_check("string", key, depth + 1)
                _service_check(kind[11:-1], item, depth + 1)
    else:
        cls, fields = _SERVICE_RECORDS[kind]
        valid = isinstance(value, cls)
        if valid:
            for name, field_kind, omit in fields:
                if not hasattr(value, name):
                    raise Refusal("missing_field", 0)
                item = getattr(value, name)
                if omit == "absent" and item is None:
                    continue
                _service_check(field_kind, item, depth + 1)
    if not valid:
        raise Refusal("wrong_type", 0)
`
const pyServiceResponse = `

class ServiceError(Exception):
    def __init__(self, code, message=""):
        super().__init__(message if message else code)
        self.code = code
        self.message = message


def _service_response(frame, service, method):
    reply = _service_decode(_decode_oaservicereply, frame)
    if reply.version != 1:
        raise DispatchError("unknown_version")
    if reply.service != service or reply.method != method:
        raise DispatchError("mismatched_response")
    if not reply.ok:
        error = _service_decode(_decode_oaserviceerror, reply.payload, 1)
        if not error.code:
            raise DispatchError("invalid_error")
        raise ServiceError(error.code, error.message)
    return reply.payload
`

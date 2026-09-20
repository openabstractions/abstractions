package main

import (
	"fmt"
	"strings"
)

// validatePythonNames refuses a definition whose Python spelling would collide
// or would not be an identifier. A collision is a generation error naming both
// contract names; a python.name annotation on the field resolves it.
func validatePythonNames(s *Definition) error {
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
	for _, st := range s.Structs {
		attrs := map[string]string{}
		if st.PreservesUnknown() {
			attrs["extras"] = "extras"
		}
		for _, f := range st.Fields {
			n := f.Ident("python")
			if !valid(n) || strings.HasPrefix(n, "__") || n == "dataclasses" {
				return fmt.Errorf("invalid Python field identifier %q for %s.%s", n, st.Name, f.Name)
			}
			if prior, ok := attrs[n]; ok {
				return fmt.Errorf("Python field identifier %q is both %s.%s and %s.%s; add python.name to one", n, st.Name, prior, st.Name, f.Name)
			}
			attrs[n] = f.Name
		}
	}
	for _, en := range s.Enums {
		members := map[string]string{}
		for _, m := range en.Members {
			n := upperSnake(m.Name)
			if o, ok := m.Ann["python.name"]; ok {
				n = o
			}
			if !valid(n) || strings.HasPrefix(n, "_") {
				return fmt.Errorf("invalid Python member identifier %q for %s.%s", n, en.Name, m.Name)
			}
			if prior, ok := members[n]; ok {
				return fmt.Errorf("Python member identifier %s.%s is both %s and %s", pascalCase(en.Name), n, prior, m.Name)
			}
			members[n] = m.Name
		}
	}
	return nil
}

func validatePythonServices(s *Definition) error {
	if err := validatePythonNames(s); err != nil {
		return err
	}
	if len(s.Services) == 0 {
		return nil
	}
	reserved := "bytes bytearray memoryview str int bool list dict set type len isinstance getattr hasattr super object Exception TypeError ValueError UnicodeError NotImplementedError Refusal DispatchError ServiceError dataclasses enum decode encode _refusal_rank _REFUSALS _member _micros_timestamp _wide_timestamp _esc _esc_byte _num _pad _strs _raw _rawmap _strmap _write_list _read_list _derive _Reader _StrEnum _closed_enum _open_enum _service_decode _service_request _service_response _service_encode _service_check _SERVICE_RECORDS"
	used := map[string]string{}
	for _, n := range strings.Fields(reserved) {
		used[n] = "the generated module"
	}
	if len(s.Services) > 1 {
		used["service_name"] = "the generated module"
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
	claim := func(n, owner string) error {
		if !valid(n) {
			return fmt.Errorf("invalid Python identifier %q for %s", n, owner)
		}
		if prior, ok := used[n]; ok {
			return fmt.Errorf("Python identifier %q is both %s and %s", n, prior, owner)
		}
		used[n] = owner
		return nil
	}
	carriers := serviceTypes(s)
	for _, st := range carriers.Structs {
		for _, n := range []string{pyClass(carriers, st.Name), pyReader(st.Name), pyWriter(st.Name)} {
			if e := claim(n, "record "+st.Name); e != nil {
				return e
			}
		}
	}
	for _, en := range s.Enums {
		if e := claim(pascalCase(en.Name), "enum "+en.Name); e != nil {
			return e
		}
		for _, key := range en.MemberAnn() {
			if e := claim(upperSnake(en.Name)+"_"+upperSnake(key), "enum "+en.Name+" annotation "+key); e != nil {
				return e
			}
		}
	}
	for _, c := range s.Consts {
		if e := claim(upperSnake(c.Name), "constant "+c.Name); e != nil {
			return e
		}
	}
	for _, svc := range s.Services {
		for _, n := range []string{pascalCase(svc.Name), pascalCase(svc.Name) + "Client"} {
			if e := claim(n, "service "+svc.Name); e != nil {
				return e
			}
		}
		methods := map[string]string{}
		for _, m := range svc.Methods {
			n := pyKeywordSafe(snakeCase(m.Name))
			if !valid(n) || strings.HasPrefix(n, "_") {
				return fmt.Errorf("invalid Python service method %q for %s", n, m.Name)
			}
			if prior, ok := methods[n]; ok {
				return fmt.Errorf("Python method %s.%s is both %s and %s", pascalCase(svc.Name), n, prior, m.Name)
			}
			methods[n] = m.Name
			args := map[string]string{}
			for _, f := range m.Args {
				a := f.Ident("python")
				if !valid(a) || a == "self" || strings.HasPrefix(a, "__") {
					return fmt.Errorf("invalid Python argument %q for %s.%s; use a python.name alias", a, m.Name, f.Name)
				}
				if prior, ok := args[a]; ok {
					return fmt.Errorf("Python argument %q of %s is both %s and %s; use a python.name alias", a, m.Name, prior, f.Name)
				}
				args[a] = f.Name
			}
		}
	}
	return nil
}

// pyServiceType is a service argument or result annotation.
func pyServiceType(s *Definition, f Field) string {
	return pyHint(s, f)
}

func pyMethodName(m Method) string { return pyKeywordSafe(snakeCase(m.Name)) }

func pySignature(s *Definition, m Method) string {
	var b strings.Builder
	fmt.Fprintf(&b, "    def %s(self", pyMethodName(m))
	for _, f := range m.Args {
		fmt.Fprintf(&b, ", %s: %s", f.Ident("python"), pyServiceType(s, f))
	}
	fmt.Fprintf(&b, ") -> %s:\n", pyServiceType(s, m.Result))
	return b.String()
}

// pyInterface is a service's abstract shape: one method per contract method,
// each documented and each raising NotImplementedError.
func pyInterface(b *strings.Builder, s *Definition, svc Service) {
	fmt.Fprintf(b, "\n\nclass %s:\n", pascalCase(svc.Name))
	b.WriteString(pyDocstring(svc.Doc, "    "))
	for _, m := range svc.Methods {
		b.WriteString("\n")
		b.WriteString(pySignature(s, m))
		b.WriteString(pyDocstring(m.Doc, "        "))
		b.WriteString("        raise NotImplementedError\n")
	}
}

func pyService(b *strings.Builder, s *Definition) {
	if len(s.Services) == 0 {
		return
	}
	common := pyServiceCommon
	if hasBinary(s) {
		common = strings.Replace(common, "    if kind == \"string\":", "    if kind == \"binary\":\n        valid = type(value) is bytes\n    elif kind == \"string\":", 1)
	}
	b.WriteString(common)
	if len(s.Services) > 1 {
		fmt.Fprintf(b, `
def service_name(frame):
    """Validate envelope/version for routing; dispatchers validate typed arguments."""
    value = _service_decode(%s, frame)
    if value.version != 1:
        raise DispatchError("unknown_version")
    return value.service

`, pyReader("OAServiceFrame"))
	}
	if hasReplies(s) {
		b.WriteString(pyServiceResponse)
	}
	b.WriteString("\n_SERVICE_RECORDS = {\n")
	for _, st := range s.Structs {
		fmt.Fprintf(b, "    %q: (%s, [", st.Name, pyClass(s, st.Name))
		for _, f := range st.Fields {
			fmt.Fprintf(b, "(%q, %q, %q), ", f.Ident("python"), pyKind(f), f.Omit)
		}
		b.WriteString("]),\n")
	}
	b.WriteString("}\n")
	for _, svc := range s.Services {
		pyInterface(b, s, svc)
		fmt.Fprintf(b, "\n\nclass %sClient(%s):\n", pascalCase(svc.Name), pascalCase(svc.Name))
		fmt.Fprintf(b, "    \"\"\"Calls %s through a transport that exchanges frames.\"\"\"\n\n", svc.WireName)
		b.WriteString("    def __init__(self, transport):\n        self._transport = transport\n")
		for _, m := range svc.Methods {
			pyClientMethod(b, s, svc, m)
		}
	}
}

func pyClientMethod(b *strings.Builder, s *Definition, svc Service, m Method) {
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
	args, arguments, request, payload, result := local("_args"), local("_arguments"), local("_request"), local("_payload"), local("_result")
	b.WriteString("\n")
	b.WriteString(pySignature(s, m))
	for _, f := range m.Args {
		fmt.Fprintf(b, "        _service_check(%q, %s)\n", pyKind(f), f.Ident("python"))
	}
	fmt.Fprintf(b, "        %s = %s()\n", args, pyClass(s, argsName(svc, m)))
	for _, f := range m.Args {
		fmt.Fprintf(b, "        %s.%s = %s\n", args, f.Ident("python"), f.Ident("python"))
	}
	fmt.Fprintf(b, "        %s = _service_encode(%s, %s, 1)\n        _service_decode(%s, %s, 1)\n", arguments, pyWriter(argsName(svc, m)), args, pyReader(argsName(svc, m)), arguments)
	fmt.Fprintf(b, "        %s = _service_request(%q, %q, %s)\n", request, svc.WireName, m.Name, arguments)
	if m.Oneway {
		fmt.Fprintf(b, "        self._transport.write_frame(%s)\n        return None\n", request)
		return
	}
	fmt.Fprintf(b, "        %s = _service_response(self._transport.exchange_frame(%s), %q, %q)\n        %s = _service_decode(%s, %s, 1)\n", payload, request, svc.WireName, m.Name, result, pyReader(resultName(svc, m)), payload)
	if m.Result.Type == "void" {
		b.WriteString("        return None\n")
	} else {
		fmt.Fprintf(b, "        return %s.value\n", result)
	}
}

const pyServiceCommon = `

class DispatchError(ValueError):
    """A frame that names no version, service or method this module speaks."""

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
    v = _ServiceFrame(version=1, service=service, method=method, arguments=arguments)
    frame = _service_encode(_write_oa_service_frame, v, 0)
    _service_decode(_read_oa_service_frame, frame)
    return frame


def _service_check(kind, value, depth=0):
    # Python coercions must not silently change typed service arguments.
    if depth >= _DEPTH_LIMIT and (kind.startswith(("list<", "map<")) or kind in _SERVICE_RECORDS):
        raise Refusal("depth_exceeded", 0)
    if kind == "string":
        valid = type(value) is str
    elif kind == "enum":
        valid = value is None or isinstance(value, str)
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
    """The service answered the call with an error code and message."""

    def __init__(self, code, message=""):
        super().__init__(message if message else code)
        self.code = code
        self.message = message


def _service_response(frame, service, method):
    reply = _service_decode(_read_oa_service_reply, frame)
    if reply.version != 1:
        raise DispatchError("unknown_version")
    if reply.service != service or reply.method != method:
        raise DispatchError("mismatched_response")
    if not reply.ok:
        error = _service_decode(_read_oa_service_error, reply.payload, 1)
        if not error.code:
            raise DispatchError("invalid_error")
        raise ServiceError(error.code, error.message)
    return reply.payload
`

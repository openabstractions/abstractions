package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var emittedWord = regexp.MustCompile(`refuse\("([a-z_]+)"\)`)

var emittedImport = regexp.MustCompile(`(?m)^\s*(?:#include\s*<([^>]+)>|use\s+([\w:]+)\s*;|import\s+(\S+)|from\s+(\S+)\s+import|.*\brequire\(\s*['"]([^'"]+))`)

func verifyCode(e emitted) error {
	if err := checkWords(e); err != nil {
		return err
	}
	return checkImports(e)
}

// checkImports is the jurisdiction rule with something behind it. A generated
// module that can reach a socket, a clock or a filesystem has stopped being a
// description of bytes, and the difference is visible at exactly one place: the
// imports it emits. Widening this list is how a backend admits it now depends
// on something, which is a decision and not a detail.
func checkImports(e emitted) error {
	var taken []string
	for _, m := range emittedImport.FindAllStringSubmatch(e.body, -1) {
		for _, name := range m[1:] {
			if name != "" && !contains(e.imports, name) && !contains(taken, name) {
				taken = append(taken, name)
			}
		}
	}
	sort.Strings(taken)
	if len(taken) > 0 {
		return fmt.Errorf("the %s backend emits code that imports %s; generated code describes bytes and reaches nothing", e.lang, strings.Join(taken, ", "))
	}
	return nil
}

// checkWords is what makes the refusal block the source rather than a copy of
// one. A backend can only say a word the definition declares, and a word the
// definition declares must be reachable in every backend, so the fifteen cannot
// drift apart the way ten transcribed status codes did.
//
// The two directions read different bodies, and that is what stops a selection
// weakening the check. Whether a word is DECLARED is asked of the artefact as
// shipped, because a word said there is said to whoever uses it. Whether a word
// is REACHABLE is asked of the backend over the whole definition, because that
// is a property of the backend and an artefact carrying fewer surfaces must not
// be able to answer it.
func checkWords(e emitted) error {
	declared := map[string]bool{}
	for _, r := range e.def.Refusals {
		declared[r.Word] = true
	}
	said := map[string]bool{}
	for _, m := range emittedWord.FindAllStringSubmatch(e.body, -1) {
		said[m[1]] = true
	}
	reachable := map[string]bool{}
	for _, m := range emittedWord.FindAllStringSubmatch(e.whole, -1) {
		reachable[m[1]] = true
	}
	var undeclared, unreachable []string
	for w := range said {
		if !declared[w] {
			undeclared = append(undeclared, w)
		}
	}
	for w := range declared {
		if !reachable[w] {
			unreachable = append(unreachable, w)
		}
	}
	sort.Strings(undeclared)
	sort.Strings(unreachable)
	if len(undeclared) > 0 {
		return fmt.Errorf("the %s backend refuses with %s, which the definition does not declare", e.lang, strings.Join(undeclared, ", "))
	}
	if len(unreachable) > 0 {
		return fmt.Errorf("the definition declares %s, which the %s backend can never say", strings.Join(unreachable, ", "), e.lang)
	}
	return nil
}

func opNames(s *Definition) []string {
	out := make([]string, len(s.Proto.Operations))
	for i, o := range s.Proto.Operations {
		out[i] = o.Name
	}
	return out
}

func verdictNames(s *Definition) []string {
	en := s.Enum(s.Proto.Verdicts)
	out := make([]string, len(en.Members))
	for i, m := range en.Members {
		out[i] = m.Name
	}
	return out
}

func envelopeUses(s *Definition, types ...string) bool {
	if s.Proto == nil {
		return false
	}
	for _, name := range []string{s.Proto.Request, s.Proto.Response} {
		for _, f := range s.Struct(name).Fields {
			if contains(types, f.Type) {
				return true
			}
		}
	}
	return false
}

func envelopes(s *Definition) []string { return []string{s.Proto.Request, s.Proto.Response} }

const goRawFlat = `
func rawFlat(out []byte, s string) []byte {
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '"':
			j := i + 1
			for j < len(s) {
				if s[j] == '\\' {
					j += 2
					continue
				}
				if s[j] == '"' {
					j++
					break
				}
				j++
			}
			out = append(out, s[i:j]...)
			i = j
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}
`

const goStrsFlat = `
func strsFlat(out []byte, v []string) []byte {
	out = append(out, '[')
	for i, s := range v {
		if i > 0 {
			out = append(out, ',')
		}
		out = esc(out, s)
	}
	return append(out, ']')
}
`

const goRawsFlat = `
func rawsFlat(out []byte, v []Raw) []byte {
	out = append(out, '[')
	for i, s := range v {
		if i > 0 {
			out = append(out, ',')
		}
		out = rawFlat(out, s)
	}
	return append(out, ']')
}
`

const goRawmapFlat = `
func rawmapFlat(out []byte, m map[string]Raw) []byte {
	out = append(out, '{')
	for i, k := range sortedKeys(m) {
		if i > 0 {
			out = append(out, ',')
		}
		out = esc(out, k)
		out = append(out, ':')
		out = rawFlat(out, m[k])
	}
	return append(out, '}')
}
`

const goStrmapFlat = `
func strmapFlat(out []byte, m map[string]string) []byte {
	out = append(out, '{')
	for i, k := range sortedKeys(m) {
		if i > 0 {
			out = append(out, ',')
		}
		out = esc(out, k)
		out = append(out, ':')
		out = esc(out, m[k])
	}
	return append(out, '}')
}
`

const goRawList = `
func (r *reader) rawList() ([]Raw, error) {
	if r.at() != '[' {
		return nil, r.refuse("wrong_type")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	r.pos++
	out := []Raw{}
	r.ws()
	if r.at() != ']' {
		for {
			r.ws()
			v, err := r.rawValue()
			if err != nil {
				return nil, err
			}
			out = append(out, v)
			r.ws()
			if r.at() != ',' {
				break
			}
			r.pos++
		}
	}
	if r.at() != ']' {
		return nil, r.refuse("malformed")
	}
	r.pos++
	r.depth--
	return out, nil
}
`

func goStrings(b *strings.Builder, name string, xs []string) {
	fmt.Fprintf(b, "\nvar %s = []string{", name)
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", x)
	}
	b.WriteString("}\n")
}

func goProtocol(b *strings.Builder, s *Definition) {
	// The order is the half of a refusal that agreeing on the words leaves open:
	// an input can break two rules, and two readers that refuse the same set can
	// still disagree on the word for half of it.
	b.WriteString("\n// Refusals is in the order two of them are chosen between.\n")
	goStrings(b, "Refusals", s.Words())
	b.WriteString("\nfunc RefusalRank(word string) int {\n\tfor i, w := range Refusals {\n\t\tif w == word {\n\t\t\treturn i\n\t\t}\n\t}\n\treturn -1\n}\n")
	if s.Proto == nil {
		return
	}
	if envelopeUses(s, "json", "list<json>") {
		b.WriteString(goRawFlat)
	}
	if envelopeUses(s, "list<string>") {
		b.WriteString(goStrsFlat)
	}
	if envelopeUses(s, "list<json>") {
		b.WriteString(goRawsFlat + goRawList)
	}
	if envelopeUses(s, "map<string,json>") {
		b.WriteString(goRawmapFlat)
	}
	if envelopeUses(s, "map<string,string>") {
		b.WriteString(goStrmapFlat)
	}
	goStrings(b, "Operations", opNames(s))
	b.WriteString("\nfunc IsOperation(name string) bool {\n\tfor _, o := range Operations {\n\t\tif o == name {\n\t\t\treturn true\n\t\t}\n\t}\n\treturn false\n}\n")
	fmt.Fprintf(b, "\nconst UnknownOperation = %q\n", s.Proto.Unknown)
	goStrings(b, "Verdicts", verdictNames(s))
	b.WriteString("\nfunc IsVerdict(name string) bool {\n\tfor _, v := range Verdicts {\n\t\tif v == name {\n\t\t\treturn true\n\t\t}\n\t}\n\treturn false\n}\n")
	for _, name := range envelopes(s) {
		goEncoder(b, s, *s.Struct(name), true)
		fmt.Fprintf(b, "\nfunc Encode%s(v *%s) []byte {\n\treturn encWire%s(nil, v)\n}\n", name, name, name)
		fmt.Fprintf(b, "\nfunc Decode%s(in []byte) (*%s, error) {\n\tr := &reader{buf: in}\n\tr.ws()\n\tv, err := r.decode%s()\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tr.ws()\n\tif r.pos < len(r.buf) {\n\t\treturn nil, r.refuse(\"trailing_bytes\")\n\t}\n\treturn v, nil\n}\n", name, name, name)
	}
}

func goValueFlat(s *Definition, f Field, e string) string {
	switch f.Type {
	case "string":
		return "out = esc(out, " + e + ")"
	case "i32":
		return "out = num(out, int64(" + e + "))"
	case "i64":
		return "out = num(out, " + e + ")"
	case "bool":
		return "if " + e + " {\n\t\t\tout = append(out, 't', 'r', 'u', 'e')\n\t\t} else {\n\t\t\tout = append(out, 'f', 'a', 'l', 's', 'e')\n\t\t}"
	case "json":
		return "out = rawFlat(out, " + e + ")"
	case "list<string>":
		return "out = strsFlat(out, " + e + ")"
	case "list<json>":
		return "out = rawsFlat(out, " + e + ")"
	case "map<string,json>":
		return "out = rawmapFlat(out, " + e + ")"
	case "map<string,string>":
		return "out = strmapFlat(out, " + e + ")"
	}
	return "out = encWire" + f.Type + "(out, &" + e + ")"
}

const pyRawFlat = `

def raw_flat(out, s):
    b = s.encode("utf-8") if isinstance(s, str) else s
    i, n = 0, len(b)
    while i < n:
        c = b[i]
        if c in _WS:
            i += 1
        elif c == 0x22:
            j = i + 1
            while j < n:
                if b[j] == 0x5C:
                    j += 2
                    continue
                if b[j] == 0x22:
                    j += 1
                    break
                j += 1
            out += b[i:j]
            i = j
        else:
            out.append(c)
            i += 1
`

const pyStrsFlat = `

def strs_flat(out, v):
    out += b"["
    for i, s in enumerate(v):
        if i:
            out += b","
        esc(out, s)
    out += b"]"
`

const pyRawsFlat = `

def raws_flat(out, v):
    out += b"["
    for i, s in enumerate(v):
        if i:
            out += b","
        raw_flat(out, s)
    out += b"]"
`

const pyRawmapFlat = `

def rawmap_flat(out, m):
    out += b"{"
    for i, k in enumerate(sorted(m, key=lambda k: k.encode("utf-8"))):
        if i:
            out += b","
        esc(out, k)
        out += b":"
        raw_flat(out, m[k])
    out += b"}"
`

const pyStrmapFlat = `

def strmap_flat(out, m):
    out += b"{"
    for i, k in enumerate(sorted(m, key=lambda k: k.encode("utf-8"))):
        if i:
            out += b","
        esc(out, k)
        out += b":"
        esc(out, m[k])
    out += b"}"
`

const pyRawList = `

def _raw_list(r):
    if r.at() != _LBRACK:
        raise r.refuse("wrong_type")
    r.enter()
    r.pos += 1
    out = []
    r.ws()
    if r.at() != _RBRACK:
        while True:
            r.ws()
            out.append(r.raw_value())
            r.ws()
            if r.at() != _COMMA:
                break
            r.pos += 1
    if r.at() != _RBRACK:
        raise r.refuse("malformed")
    r.pos += 1
    r.depth -= 1
    return out
`

func pyStrings(b *strings.Builder, name string, xs []string) {
	fmt.Fprintf(b, "\n\n%s = [", name)
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", x)
	}
	b.WriteString("]\n")
}

func pyProtocol(b *strings.Builder, s *Definition) {
	b.WriteString("\n\n# REFUSALS is in the order two of them are chosen between.")
	pyStrings(b, "REFUSALS", s.Words())
	b.WriteString("\n\ndef refusal_rank(word):\n    return REFUSALS.index(word) if word in REFUSALS else -1\n")
	if s.Proto == nil {
		return
	}
	if envelopeUses(s, "json", "list<json>") {
		b.WriteString(pyRawFlat)
	}
	if envelopeUses(s, "list<string>") {
		b.WriteString(pyStrsFlat)
	}
	if envelopeUses(s, "list<json>") {
		b.WriteString(pyRawsFlat + pyRawList)
	}
	if envelopeUses(s, "map<string,json>") {
		b.WriteString(pyRawmapFlat)
	}
	if envelopeUses(s, "map<string,string>") {
		b.WriteString(pyStrmapFlat)
	}
	pyStrings(b, "OPERATIONS", opNames(s))
	b.WriteString("\n\ndef is_operation(name):\n    return name in OPERATIONS\n")
	fmt.Fprintf(b, "\n\nUNKNOWN_OPERATION = %q\n", s.Proto.Unknown)
	pyStrings(b, "VERDICTS", verdictNames(s))
	b.WriteString("\n\ndef is_verdict(name):\n    return name in VERDICTS\n")
	for _, name := range envelopes(s) {
		pyEncoder(b, s, *s.Struct(name), true)
		fmt.Fprintf(b, "\n\ndef encode_%s(v):\n    out = bytearray()\n    enc_wire_%s(out, v)\n    return bytes(out)\n", lower(name), lower(name))
		fmt.Fprintf(b, "\n\ndef decode_%s(data):\n    r = _Reader(bytes(data))\n    r.ws()\n    v = _decode_%s(r)\n    r.ws()\n    if r.pos < len(r.buf):\n        raise r.refuse(\"trailing_bytes\")\n    return v\n", lower(name), lower(name))
	}
}

func pyValueFlat(s *Definition, f Field, e string) string {
	switch f.Type {
	case "string":
		return "esc(out, " + e + ")"
	case "i32", "i64":
		return "num(out, " + e + ")"
	case "bool":
		return `out += b"true" if ` + e + ` else b"false"`
	case "json":
		return "raw_flat(out, " + e + ")"
	case "list<string>":
		return "strs_flat(out, " + e + ")"
	case "list<json>":
		return "raws_flat(out, " + e + ")"
	case "map<string,json>":
		return "rawmap_flat(out, " + e + ")"
	case "map<string,string>":
		return "strmap_flat(out, " + e + ")"
	}
	return "enc_wire_" + lower(f.Type) + "(out, " + e + ")"
}

const jsRawFlat = `
function rawFlat(out, s) {
  const b = typeof s === "string" ? ENC.encode(s) : s;
  let i = 0;
  while (i < b.length) {
    const c = b[i];
    if (isWs(c)) { i++; continue; }
    if (c === 0x22) {
      let j = i + 1;
      while (j < b.length) {
        if (b[j] === 0x5c) { j += 2; continue; }
        if (b[j] === 0x22) { j++; break; }
        j++;
      }
      for (let k = i; k < j; k++) out.byte(b[k]);
      i = j;
    } else {
      out.byte(c);
      i++;
    }
  }
}
`

const jsStrsFlat = `
function strsFlat(out, v) {
  out.byte(0x5b);
  for (let i = 0; i < v.length; i++) {
    if (i > 0) out.byte(0x2c);
    esc(out, v[i]);
  }
  out.byte(0x5d);
}
`

const jsRawsFlat = `
function rawsFlat(out, v) {
  out.byte(0x5b);
  for (let i = 0; i < v.length; i++) {
    if (i > 0) out.byte(0x2c);
    rawFlat(out, v[i]);
  }
  out.byte(0x5d);
}
`

const jsRawmapFlat = `
function rawmapFlat(out, m) {
  const keys = Object.keys(m).sort(byteLess);
  out.byte(0x7b);
  for (let i = 0; i < keys.length; i++) {
    if (i > 0) out.byte(0x2c);
    esc(out, keys[i]);
    out.byte(0x3a);
    rawFlat(out, m[keys[i]]);
  }
  out.byte(0x7d);
}
`

const jsStrmapFlat = `
function strmapFlat(out, m) {
  const keys = Object.keys(m).sort(byteLess);
  out.byte(0x7b);
  for (let i = 0; i < keys.length; i++) {
    if (i > 0) out.byte(0x2c);
    esc(out, keys[i]);
    out.byte(0x3a);
    esc(out, m[keys[i]]);
  }
  out.byte(0x7d);
}
`

const jsRawList = `
function rawList(r) {
  if (r.at() !== 0x5b) throw r.refuse("wrong_type");
  r.enter();
  r.pos++;
  const out = [];
  r.ws();
  if (r.at() !== 0x5d) {
    for (;;) {
      r.ws();
      out.push(r.rawValue());
      r.ws();
      if (r.at() !== 0x2c) break;
      r.pos++;
    }
  }
  if (r.at() !== 0x5d) throw r.refuse("malformed");
  r.pos++;
  r.depth--;
  return out;
}
`

func jsStrings(b *strings.Builder, name string, xs []string) {
	fmt.Fprintf(b, "\nexport const %s = [", name)
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", x)
	}
	b.WriteString("];\n")
}

func jsProtocol(b *strings.Builder, s *Definition) {
	b.WriteString("\n// refusals is in the order two of them are chosen between.")
	jsStrings(b, "refusals", s.Words())
	b.WriteString("\nexport function refusalRank(word) {\n  return refusals.indexOf(word);\n}\n")
	if s.Proto == nil {
		return
	}
	if envelopeUses(s, "json", "list<json>") {
		b.WriteString(jsRawFlat)
	}
	if envelopeUses(s, "list<string>") {
		b.WriteString(jsStrsFlat)
	}
	if envelopeUses(s, "list<json>") {
		b.WriteString(jsRawsFlat + jsRawList)
	}
	if envelopeUses(s, "map<string,json>") {
		b.WriteString(jsRawmapFlat)
	}
	if envelopeUses(s, "map<string,string>") {
		b.WriteString(jsStrmapFlat)
	}
	jsStrings(b, "operations", opNames(s))
	b.WriteString("\nexport function isOperation(name) {\n  return operations.includes(name);\n}\n")
	fmt.Fprintf(b, "\nexport const unknownOperation = %q;\n", s.Proto.Unknown)
	jsStrings(b, "verdicts", verdictNames(s))
	b.WriteString("\nexport function isVerdict(name) {\n  return verdicts.includes(name);\n}\n")
	for _, name := range envelopes(s) {
		jsEncoder(b, s, *s.Struct(name), true)
		fmt.Fprintf(b, "\nexport function encode%s(v) {\n  const out = new Out();\n  enc_wire_%s(out, v);\n  return out.bytes();\n}\n", name, lower(name))
		fmt.Fprintf(b, "\nexport function decode%s(data) {\n  const r = new Reader(data);\n  r.ws();\n  const v = decode_%s(r);\n  r.ws();\n  if (r.pos < r.buf.length) throw r.refuse(\"trailing_bytes\");\n  return v;\n}\n", name, lower(name))
	}
}

func jsValueFlat(s *Definition, f Field, e string) string {
	switch f.Type {
	case "string":
		return "esc(out, " + e + ");"
	case "i32", "i64":
		return "num(out, " + e + ");"
	case "bool":
		return "out.ascii(" + e + ` ? "true" : "false");`
	case "json":
		return "rawFlat(out, " + e + ");"
	case "list<string>":
		return "strsFlat(out, " + e + ");"
	case "list<json>":
		return "rawsFlat(out, " + e + ");"
	case "map<string,json>":
		return "rawmapFlat(out, " + e + ");"
	case "map<string,string>":
		return "strmapFlat(out, " + e + ");"
	}
	return "enc_wire_" + lower(f.Type) + "(out, " + e + ");"
}

const cppRawFlat = `
inline void raw_flat(std::string& out, const Raw& s) {
    std::size_t i = 0;
    while (i < s.size()) {
        const unsigned char c = static_cast<unsigned char>(s[i]);
        if (c == ' ' || c == '\t' || c == '\n' || c == '\r') { ++i; continue; }
        if (c == '"') {
            std::size_t j = i + 1;
            while (j < s.size()) {
                if (s[j] == '\\') { j += 2; continue; }
                if (s[j] == '"') { ++j; break; }
                ++j;
            }
            out.append(s, i, j - i);
            i = j;
        } else {
            out += static_cast<char>(c);
            ++i;
        }
    }
}
`

const cppStrsFlat = `
inline void strs_flat(std::string& out, const std::vector<std::string>& v) {
    out += '[';
    for (std::size_t i = 0; i < v.size(); ++i) {
        if (i > 0) out += ',';
        esc(out, v[i]);
    }
    out += ']';
}
`

const cppRawsFlat = `
inline void raws_flat(std::string& out, const std::vector<Raw>& v) {
    out += '[';
    for (std::size_t i = 0; i < v.size(); ++i) {
        if (i > 0) out += ',';
        raw_flat(out, v[i]);
    }
    out += ']';
}
`

const cppRawmapFlat = `
inline void rawmap_flat(std::string& out, const std::map<std::string, Raw>& m) {
    out += '{';
    std::size_t i = 0;
    for (const auto& kv : m) {
        if (i++ > 0) out += ',';
        esc(out, kv.first);
        out += ':';
        raw_flat(out, kv.second);
    }
    out += '}';
}
`

const cppStrmapFlat = `
inline void strmap_flat(std::string& out, const std::map<std::string, std::string>& m) {
    out += '{';
    std::size_t i = 0;
    for (const auto& kv : m) {
        if (i++ > 0) out += ',';
        esc(out, kv.first);
        out += ':';
        esc(out, kv.second);
    }
    out += '}';
}
`

const cppRawList = `
inline std::vector<Raw> raw_list(Reader& r) {
    if (r.at() != '[') r.refuse("wrong_type");
    r.enter();
    ++r.pos;
    std::vector<Raw> out;
    r.skip_ws();
    if (r.at() != ']') {
        for (;;) {
            r.skip_ws();
            out.push_back(r.raw_value());
            r.skip_ws();
            if (r.at() != ',') break;
            ++r.pos;
        }
    }
    if (r.at() != ']') r.refuse("malformed");
    ++r.pos;
    --r.depth;
    return out;
}
`

func cppStrings(b *strings.Builder, name string, xs []string) {
	fmt.Fprintf(b, "\ninline const std::vector<std::string> %s = {", name)
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", x)
	}
	b.WriteString("};\n")
}

// cppWireHelpers is emitted before the struct decoders because C++ resolves a
// call against what it has already seen, and raw_list is called from one.
func cppWireHelpers(b *strings.Builder, s *Definition) {
	if s.Proto == nil {
		return
	}
	if envelopeUses(s, "json", "list<json>") {
		b.WriteString(cppRawFlat)
	}
	if envelopeUses(s, "list<string>") {
		b.WriteString(cppStrsFlat)
	}
	if envelopeUses(s, "list<json>") {
		b.WriteString(cppRawsFlat + cppRawList)
	}
	if envelopeUses(s, "map<string,json>") {
		b.WriteString(cppRawmapFlat)
	}
	if envelopeUses(s, "map<string,string>") {
		b.WriteString(cppStrmapFlat)
	}
}

func cppProtocol(b *strings.Builder, s *Definition) {
	b.WriteString("\n// kRefusals is in the order two of them are chosen between.")
	cppStrings(b, "kRefusals", s.Words())
	b.WriteString("\ninline int refusal_rank(std::string_view word) {\n    for (std::size_t i = 0; i < kRefusals.size(); ++i)\n        if (kRefusals[i] == word) return static_cast<int>(i);\n    return -1;\n}\n")
	if s.Proto == nil {
		return
	}
	cppStrings(b, "kOperations", opNames(s))
	b.WriteString("\ninline bool is_operation(std::string_view name) {\n    for (const auto& o : kOperations) if (o == name) return true;\n    return false;\n}\n")
	fmt.Fprintf(b, "\ninline constexpr std::string_view kUnknownOperation = %q;\n", s.Proto.Unknown)
	cppStrings(b, "kVerdicts", verdictNames(s))
	b.WriteString("\ninline bool is_verdict(std::string_view name) {\n    for (const auto& v : kVerdicts) if (v == name) return true;\n    return false;\n}\n")
	for _, name := range envelopes(s) {
		cppEncoder(b, s, *s.Struct(name), true)
		fmt.Fprintf(b, "\ninline std::string encode_%s(const %s& v) {\n    std::string out;\n    enc_wire_%s(out, v);\n    return out;\n}\n", lower(name), name, lower(name))
		fmt.Fprintf(b, "\ninline %s decode_%s_document(std::string_view data) {\n    Reader r{data};\n    r.skip_ws();\n    %s v = decode_%s(r);\n    r.skip_ws();\n    if (r.pos < r.buf.size()) r.refuse(\"trailing_bytes\");\n    return v;\n}\n", name, lower(name), name, lower(name))
	}
}

func cppValueFlat(s *Definition, f Field, e string) string {
	switch f.Type {
	case "string":
		return "esc(out, " + e + ");"
	case "i32", "i64":
		return "num(out, " + e + ");"
	case "bool":
		return "out += " + e + ` ? "true" : "false";`
	case "json":
		return "raw_flat(out, " + e + ");"
	case "list<string>":
		return "strs_flat(out, " + e + ");"
	case "list<json>":
		return "raws_flat(out, " + e + ");"
	case "map<string,json>":
		return "rawmap_flat(out, " + e + ");"
	case "map<string,string>":
		return "strmap_flat(out, " + e + ");"
	}
	return "enc_wire_" + lower(f.Type) + "(out, " + e + ");"
}

const rsRawFlat = `
pub fn raw_flat(out: &mut Vec<u8>, s: &Raw) {
    let mut i = 0;
    while i < s.len() {
        let c = s[i];
        if c == b' ' || c == b'\t' || c == b'\n' || c == b'\r' {
            i += 1;
        } else if c == b'"' {
            let mut j = i + 1;
            while j < s.len() {
                if s[j] == b'\\' {
                    j += 2;
                    continue;
                }
                if s[j] == b'"' {
                    j += 1;
                    break;
                }
                j += 1;
            }
            out.extend_from_slice(&s[i..j.min(s.len())]);
            i = j;
        } else {
            out.push(c);
            i += 1;
        }
    }
}
`

const rsStrsFlat = `
pub fn strs_flat(out: &mut Vec<u8>, v: &[String]) {
    out.push(b'[');
    for (i, s) in v.iter().enumerate() {
        if i > 0 {
            out.push(b',');
        }
        esc(out, s);
    }
    out.push(b']');
}
`

const rsRawsFlat = `
pub fn raws_flat(out: &mut Vec<u8>, v: &[Raw]) {
    out.push(b'[');
    for (i, s) in v.iter().enumerate() {
        if i > 0 {
            out.push(b',');
        }
        raw_flat(out, s);
    }
    out.push(b']');
}
`

const rsRawmapFlat = `
pub fn rawmap_flat(out: &mut Vec<u8>, m: &BTreeMap<String, Raw>) {
    out.push(b'{');
    for (i, (k, v)) in m.iter().enumerate() {
        if i > 0 {
            out.push(b',');
        }
        esc(out, k);
        out.push(b':');
        raw_flat(out, v);
    }
    out.push(b'}');
}
`

const rsStrmapFlat = `
pub fn strmap_flat(out: &mut Vec<u8>, m: &BTreeMap<String, String>) {
    out.push(b'{');
    for (i, (k, v)) in m.iter().enumerate() {
        if i > 0 {
            out.push(b',');
        }
        esc(out, k);
        out.push(b':');
        esc(out, v);
    }
    out.push(b'}');
}
`

const rsRawList = `
fn raw_list(r: &mut Reader) -> Result<Vec<Raw>, Refusal> {
    if r.at() != b'[' {
        return r.refuse("wrong_type");
    }
    r.enter()?;
    r.pos += 1;
    let mut out = Vec::new();
    r.skip_ws();
    if r.at() != b']' {
        loop {
            r.skip_ws();
            out.push(r.raw_value()?);
            r.skip_ws();
            if r.at() != b',' {
                break;
            }
            r.pos += 1;
        }
    }
    if r.at() != b']' {
        return r.refuse("malformed");
    }
    r.pos += 1;
    r.depth -= 1;
    Ok(out)
}
`

func rsStrings(b *strings.Builder, name string, xs []string) {
	fmt.Fprintf(b, "\npub const %s: [&str; %d] = [", name, len(xs))
	for i, x := range xs {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", x)
	}
	b.WriteString("];\n")
}

func rsProtocol(b *strings.Builder, s *Definition) {
	b.WriteString("\n// REFUSALS is in the order two of them are chosen between.")
	rsStrings(b, "REFUSALS", s.Words())
	b.WriteString("\npub fn refusal_rank(word: &str) -> i32 {\n    match REFUSALS.iter().position(|w| *w == word) {\n        Some(i) => i as i32,\n        None => -1,\n    }\n}\n")
	if s.Proto == nil {
		return
	}
	if envelopeUses(s, "json", "list<json>") {
		b.WriteString(rsRawFlat)
	}
	if envelopeUses(s, "list<string>") {
		b.WriteString(rsStrsFlat)
	}
	if envelopeUses(s, "list<json>") {
		b.WriteString(rsRawsFlat + rsRawList)
	}
	if envelopeUses(s, "map<string,json>") {
		b.WriteString(rsRawmapFlat)
	}
	if envelopeUses(s, "map<string,string>") {
		b.WriteString(rsStrmapFlat)
	}
	rsStrings(b, "OPERATIONS", opNames(s))
	b.WriteString("\npub fn is_operation(name: &str) -> bool {\n    OPERATIONS.contains(&name)\n}\n")
	fmt.Fprintf(b, "\npub const UNKNOWN_OPERATION: &str = %q;\n", s.Proto.Unknown)
	rsStrings(b, "VERDICTS", verdictNames(s))
	b.WriteString("\npub fn is_verdict(name: &str) -> bool {\n    VERDICTS.contains(&name)\n}\n")
	for _, name := range envelopes(s) {
		rsEncoder(b, s, *s.Struct(name), true)
		fmt.Fprintf(b, "\npub fn encode_%s(v: &%s) -> Vec<u8> {\n    let mut out = Vec::new();\n    enc_wire_%s(&mut out, v);\n    out\n}\n", lower(name), name, lower(name))
		fmt.Fprintf(b, "\npub fn decode_%s_document(data: &[u8]) -> Result<%s, Refusal> {\n    let mut r = Reader { buf: data, pos: 0, depth: 0 };\n    r.skip_ws();\n    let v = decode_%s(&mut r)?;\n    r.skip_ws();\n    if r.pos < r.buf.len() {\n        return r.refuse(\"trailing_bytes\");\n    }\n    Ok(v)\n}\n", lower(name), name, lower(name))
	}
}

func rsValueFlat(s *Definition, f Field, e string) string {
	switch f.Type {
	case "string":
		return "esc(out, &" + e + ");"
	case "i32":
		return "num(out, " + e + " as i64);"
	case "i64":
		return "num(out, " + e + ");"
	case "bool":
		return "out.extend_from_slice(if " + e + ` { b"true" } else { b"false" });`
	case "json":
		return "raw_flat(out, &" + e + ");"
	case "list<string>":
		return "strs_flat(out, &" + e + ");"
	case "list<json>":
		return "raws_flat(out, &" + e + ");"
	case "map<string,json>":
		return "rawmap_flat(out, &" + e + ");"
	case "map<string,string>":
		return "strmap_flat(out, &" + e + ");"
	}
	return "enc_wire_" + lower(f.Type) + "(out, &" + e + ");"
}

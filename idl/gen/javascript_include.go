package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// JavaScript typed includes import each dependency's generated module through
// an explicit specifier. Imported records keep the dependency's own codecs,
// refusal policy and value check; the importing module holds only bridges.

func jsModuleSpecifier(v string) bool {
	return v != "" && !strings.ContainsAny(v, "\"'`\\ \t\n\r")
}

func jsImportedType(s *Definition, t string) bool {
	if e := listElement(t); e != "" {
		t = e
	}
	_, ok := s.importedRecord(t)
	return ok
}

func validateJSIncludes(s *Definition) error {
	aliases := map[string]bool{}
	specifiers := map[string]string{}
	for _, imp := range s.Imports {
		aliases[imp.Alias] = true
		spec := s.JSImports[imp.Alias]
		if spec == "" {
			return fmt.Errorf("include %s requires --js-import=%s=<module-specifier>", imp.Alias, imp.Alias)
		}
		if other := specifiers[spec]; other != "" {
			return fmt.Errorf("ambiguous JavaScript module mapping %s for includes %s and %s", spec, other, imp.Alias)
		}
		specifiers[spec] = imp.Alias
	}
	var unused []string
	for alias := range s.JSImports {
		if !aliases[alias] {
			unused = append(unused, alias)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		return fmt.Errorf("--js-import names %s, which the definition does not include", strings.Join(unused, ", "))
	}
	names := map[string]string{}
	claim := func(name, owner string) error {
		if prior, ok := names[name]; ok {
			return fmt.Errorf("javascript named codec collision %s between %s and %s", name, prior, owner)
		}
		names[name] = owner
		return nil
	}
	for _, n := range []string{"encode", "decode", "refusals", "refusalRank", "member", "microsTimestamp", "wideTimestamp", "derive", "_namedCheck", "_namedRecords", "_importedChecks"} {
		names[n] = "generated module"
	}
	for _, c := range s.Consts {
		if err := claim(camelCase(c.Name), "constant "+c.Name); err != nil {
			return err
		}
	}
	if s.Vocab != nil {
		for _, n := range []string{camelCase(s.Vocab.Name) + "Terms", camelCase(s.Vocab.Name) + "StripCritical"} {
			if err := claim(n, "vocabulary "+s.Vocab.Name); err != nil {
				return err
			}
		}
	}
	for _, st := range s.Structs {
		if strings.HasPrefix(lower(st.Name), "oaimported") {
			return fmt.Errorf("reserved imported carrier name %s", st.Name)
		}
		if s.Envelope(st.Name) {
			continue
		}
		stem := jsStem(st.Name)
		for _, n := range []string{"new" + stem, "check" + stem, "encode" + stem, "decode" + stem, "encode" + stem + "At", "decode" + stem + "At"} {
			if err := claim(n, "record "+st.Name); err != nil {
				return err
			}
		}
	}
	return nil
}

func emitJSIncluded(b backend, s *Definition) string {
	s = importCarriers(s)
	body := b.emit(s)
	body = jsMustReplace(body, "constructor(buf) { this.buf = buf; this.pos = 0; this.depth = 0; }", "constructor(buf) { this.buf = buf; this.pos = 0; this.depth = 0; this.limit = DEPTH_LIMIT; }")
	body = jsMustReplace(body, "if (this.depth > DEPTH_LIMIT) throw", "if (this.depth > this.limit) throw")
	if len(s.Foreign) > 0 && strings.Contains(body, "function _serviceCheck(") {
		body = jsMustReplace(body, jsRecordBranch, jsImportedBranch+jsRecordBranch)
	}
	if len(s.Imports) > 0 && s.Document != "" && !s.Envelope(s.Document) {
		term := ""
		if s.Encoding.TrailingNewline() {
			term = "  out.byte(0x0a);\n"
		}
		plain := fmt.Sprintf("\nexport function encode(v) {\n  const out = new Out();\n  write%s(out, v, 0);\n%s  return out.bytes();\n}\n", jsStem(s.Document), term)
		body = jsMustReplace(body, plain, fmt.Sprintf("\nexport function encode(v) {\n  return encode%s(v);\n}\n", jsStem(s.Document)))
	}
	var head strings.Builder
	used := map[string]bool{}
	for _, n := range foreignNames(s) {
		used[s.Foreign[n].Alias] = true
	}
	for _, imp := range s.Imports {
		if used[imp.Alias] {
			fmt.Fprintf(&head, "import * as %s from %s;\n", jsDependency(imp.Alias), strconv.Quote(jsInternalSpecifier(s.JSImports[imp.Alias])))
		}
	}
	if head.Len() > 0 {
		head.WriteString("\n")
	}
	var tail strings.Builder
	for _, n := range foreignNames(s) {
		imp := s.Foreign[n]
		fmt.Fprintf(&tail, jsImportedBridge, jsStem(n), jsDependency(imp.Alias), jsStem(imp.Name))
	}
	if len(s.Foreign) > 0 {
		tail.WriteString("\nconst _importedChecks = Object.create(null);\n")
		for _, n := range foreignNames(s) {
			fmt.Fprintf(&tail, "_importedChecks[%q] = checkImported%s;\n", n, jsStem(n))
		}
	}
	tail.WriteString(jsNamedChecks(s))
	for _, st := range s.Structs {
		if s.Envelope(st.Name) {
			continue
		}
		derive := ""
		if s.Vocab != nil && st.Name == s.Document && len(s.Services) == 0 {
			derive = "  derive(r, v);\n"
		}
		result := "  return bytes;\n"
		if s.Encoding.TrailingNewline() {
			result = "  const out = new Uint8Array(bytes.length + 1);\n  out.set(bytes);\n  out[bytes.length] = 0x0a;\n  return out;\n"
		}
		fmt.Fprintf(&tail, jsNamedCodec, jsStem(st.Name), derive, result)
	}
	return head.String() + body + tail.String()
}

// Standalone records reuse the generated service value checker. Imported
// records delegate to the owning module's exported check.
func jsNamedChecks(s *Definition) string {
	s = enumCarriers(s)
	var b strings.Builder
	b.WriteString("\nconst _namedRecords = Object.create(null);\n")
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "_namedRecords[%q] = [", st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "[%q,%q,%q],", f.Ident("javascript"), f.Type, f.Omit)
		}
		b.WriteString("];\n")
	}
	checker := jsServiceCommon[strings.Index(jsServiceCommon, "function _serviceCheck("):]
	if hasBinary(s) {
		checker = jsMustReplace(checker, `else if (kind === "json")`, `else if (kind === "binary") valid = value instanceof Uint8Array;
  else if (kind === "json")`)
	}
	if len(s.Foreign) > 0 {
		checker = jsMustReplace(checker, jsRecordBranch, jsImportedBranch+jsRecordBranch)
	}
	checker = strings.NewReplacer("_serviceCheck", "_namedCheck", "_serviceRecords", "_namedRecords", "DEPTH_LIMIT", "limit",
		"depth = 0)", "depth = 0, limit = DEPTH_LIMIT)", "depth + 1)", "depth + 1, limit)", "depth+1)", "depth+1,limit)").Replace(checker)
	b.WriteString(checker)
	for _, st := range s.Structs {
		if s.Envelope(st.Name) {
			continue
		}
		fmt.Fprintf(&b, "\nexport function check%s(v, depth = 0, limit = DEPTH_LIMIT) {\n  _namedCheck(%q, v, depth, Math.min(limit, DEPTH_LIMIT));\n}\n", jsStem(st.Name), st.Name)
	}
	return b.String()
}

func jsMustReplace(body, old, replacement string) string {
	if !strings.Contains(body, old) {
		panic("javascript include emitter anchor missing: " + old)
	}
	return strings.Replace(body, old, replacement, 1)
}

const jsRecordBranch = "  } else {\n    const fields = _serviceRecords[kind];"

const jsImportedBranch = `  } else if (_importedChecks[kind] !== undefined) {
    valid = value !== null && typeof value === "object" && !Array.isArray(value);
    if (valid) _importedChecks[kind](value, depth, DEPTH_LIMIT);
`

// jsDependency is the module alias of an included definition's internal module.
func jsDependency(alias string) string { return "dependency" + jsPascal(alias) }

// jsInternalSpecifier names an included package's internal module. A package
// specifier gains the "/internal" subpath its package.json exports; a file
// specifier naming index.mjs names internal.mjs beside it.
func jsInternalSpecifier(spec string) string {
	if strings.HasSuffix(spec, "index.mjs") {
		return strings.TrimSuffix(spec, "index.mjs") + "internal.mjs"
	}
	if strings.HasSuffix(spec, ".mjs") || strings.HasSuffix(spec, ".js") {
		return spec
	}
	return spec + "/internal"
}

const jsImportedBridge = `
function new%[1]s() { return %[2]s.new%[3]s(); }

function write%[1]s(out, v, depth) {
  let bytes;
  try {
    bytes = %[2]s.encode%[3]sAt(v, depth);
  } catch (e) {
    if (e instanceof %[2]s.Refusal) throw new Refusal(e.word, e.offset);
    throw e;
  }
  for (const c of bytes) out.byte(c);
}

function read%[1]s(r) {
  try {
    const [v, n] = %[2]s.decode%[3]sAt(r.buf.subarray(r.pos), r.depth, r.limit);
    r.pos += n;
    return v;
  } catch (e) {
    if (e instanceof %[2]s.Refusal) throw new Refusal(e.word, r.pos + e.offset);
    throw e;
  }
}

function checkImported%[1]s(v, depth, limit) {
  try {
    %[2]s.check%[3]s(v, depth, limit);
  } catch (e) {
    if (e instanceof %[2]s.Refusal) throw new Refusal(e.word, e.offset);
    throw e;
  }
}
`

const jsNamedCodec = `
export function decode%[1]sAt(data, depth, limit) {
  const r = new Reader(data);
  if (depth < 0 || limit < 1) throw r.refuse("depth_exceeded");
  r.depth = depth;
  r.limit = Math.min(limit, DEPTH_LIMIT);
  r.ws();
  const v = read%[1]s(r);
%[2]s  return [v, r.pos];
}

export function encode%[1]sAt(v, depth) {
  if (depth < 0 || depth >= DEPTH_LIMIT) throw new Refusal("depth_exceeded", 0);
  check%[1]s(v, depth, DEPTH_LIMIT);
  const out = new Out();
  write%[1]s(out, v, depth);
  const bytes = out.bytes();
  decode%[1]sAt(bytes, depth, DEPTH_LIMIT);
  return bytes;
}

export function encode%[1]s(v) {
  const bytes = encode%[1]sAt(v, 0);
%[3]s}

export function decode%[1]s(data) {
  const [v, n] = decode%[1]sAt(data, 0, DEPTH_LIMIT);
  const r = new Reader(data);
  r.pos = n;
  r.ws();
  if (r.pos !== r.buf.length) throw r.refuse("trailing_bytes");
  return v;
}
`

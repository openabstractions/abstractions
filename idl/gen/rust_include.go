package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Rust typed includes reference each dependency's generated crate through an
// explicit path. Imported records keep the dependency's own codecs, refusal
// policy and value check; the importing crate holds only bridges that carry
// the enclosing depth budget and translate refusal offsets.

var rustCratePath = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(::[A-Za-z_][A-Za-z0-9_]*)*$`)

func validateRustIncludes(s *Definition) error {
	aliases := map[string]bool{}
	paths := map[string]string{}
	for _, imp := range s.Imports {
		aliases[imp.Alias] = true
		path := s.RustImports[imp.Alias]
		if path == "" {
			return fmt.Errorf("include %s requires --rust-import=%s=<crate-path>", imp.Alias, imp.Alias)
		}
		if other := paths[path]; other != "" {
			return fmt.Errorf("ambiguous Rust crate mapping %s for includes %s and %s", path, other, imp.Alias)
		}
		paths[path] = imp.Alias
	}
	var unused []string
	for alias := range s.RustImports {
		if !aliases[alias] {
			unused = append(unused, alias)
		}
	}
	sort.Strings(unused)
	if len(unused) > 0 {
		return fmt.Errorf("--rust-import names %s, which the definition does not include", strings.Join(unused, ", "))
	}
	names := map[string]string{}
	for _, st := range s.Structs {
		if strings.HasPrefix(lower(st.Name), "oaimported") {
			return fmt.Errorf("reserved imported carrier name %s", st.Name)
		}
		if s.Envelope(st.Name) {
			continue
		}
		l := lower(st.Name)
		for _, n := range []string{"enc_" + l, "decode_" + l, "named_encode_" + l, "check_" + l, "encode_" + l + "_at", "decode_" + l + "_at", "encode_" + l + "_document", "decode_" + l + "_document"} {
			if prior, ok := names[n]; ok {
				return fmt.Errorf("rust named codec collision %s between %s and record %s", n, prior, st.Name)
			}
			names[n] = "record " + st.Name
		}
	}
	return nil
}

func emitRustIncluded(b backend, s *Definition) string {
	s = importCarriers(s)
	body := b.emit(s)
	body = rsMustReplace(body, "struct Reader<'a> {\n    buf: &'a [u8],\n    pos: usize,\n    depth: i32,\n}", "struct Reader<'a> {\n    buf: &'a [u8],\n    pos: usize,\n    depth: i32,\n    limit: i32,\n}")
	body = rsMustReplace(body, "if self.depth > DEPTH_LIMIT {", "if self.depth > self.limit {")
	// A selection without decoders constructs no reader; every construction present gains the default limit.
	body = strings.ReplaceAll(body, "depth: 0 }", "depth: 0, limit: DEPTH_LIMIT }")
	if len(s.Imports) > 0 && s.Document != "" && !s.Envelope(s.Document) {
		term := ""
		if s.Encoding.TrailingNewline() {
			term = "    out.push(b'\\n');\n"
		}
		plain := fmt.Sprintf("\npub fn encode(v: &%s) -> Vec<u8> {\n    let mut out = Vec::new();\n    enc_%s(&mut out, v, 0);\n%s    out\n}\n", s.Document, lower(s.Document), term)
		body = rsMustReplace(body, plain, fmt.Sprintf("\npub fn encode(v: &%s) -> Vec<u8> {\n    match encode_%s_document(v) {\n        Ok(out) => out,\n        Err(e) => std::panic::panic_any(e),\n    }\n}\n", s.Document, lower(s.Document)))
	}
	var tail strings.Builder
	for _, n := range foreignNames(s) {
		imp := s.Foreign[n]
		fmt.Fprintf(&tail, rsImportedBridge, n, lower(n), s.RustImports[imp.Alias], imp.Name, lower(imp.Name))
		if !s.NoIPC && len(s.Services) > 0 {
			fmt.Fprintf(&tail, "\nfn service_check_%s(v: &%s) -> Result<(), Refusal> {\n    check_imported_%s(v)\n}\n", lower(n), n, lower(n))
		}
	}
	for _, st := range s.Structs {
		if s.Envelope(st.Name) {
			continue
		}
		bind, derive := "let v", ""
		if s.Vocab != nil && st.Name == s.Document && len(s.Services) == 0 {
			bind, derive = "let mut v", "    derive(&r, &mut v)?;\n"
		}
		out, term := "let out", ""
		if s.Encoding.TrailingNewline() {
			out, term = "let mut out", "    out.push(b'\\n');\n"
		}
		fmt.Fprintf(&tail, rsNamedCodec, st.Name, lower(st.Name), bind, derive, out, term)
	}
	return body + tail.String()
}

func rsMustReplace(body, old, replacement string) string {
	if !strings.Contains(body, old) {
		panic("rust include emitter anchor missing: " + old)
	}
	return strings.Replace(body, old, replacement, 1)
}

// %[1]s carrier, %[2]s lower carrier, %[3]s crate path, %[4]s imported record, %[5]s lower imported record.
const rsImportedBridge = `
pub type %[1]s = %[3]s::%[4]s;

pub fn enc_%[2]s(out: &mut Vec<u8>, v: &%[1]s, depth: i32) {
    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let mut bytes = Vec::new();
        %[3]s::enc_%[5]s(&mut bytes, v, depth);
        bytes
    })) {
        Ok(bytes) => out.extend_from_slice(&bytes),
        Err(p) => match p.downcast::<%[3]s::Refusal>() {
            Ok(e) => std::panic::panic_any(Refusal { word: e.word, offset: e.offset }),
            Err(p) => std::panic::resume_unwind(p),
        },
    }
}

fn decode_%[2]s(r: &mut Reader) -> Result<%[1]s, Refusal> {
    match %[3]s::decode_%[5]s_at(&r.buf[r.pos..], r.depth, r.limit) {
        Ok((v, n)) => {
            r.pos += n;
            Ok(v)
        }
        Err(e) => Err(Refusal { word: e.word, offset: r.pos + e.offset }),
    }
}

fn check_imported_%[2]s(v: &%[1]s) -> Result<(), Refusal> {
    %[3]s::check_%[5]s(v, 0, DEPTH_LIMIT).map_err(|e| Refusal { word: e.word, offset: e.offset })
}
`

// %[1]s record, %[2]s lower record, %[3]s value binding, %[4]s derive, %[5]s output binding, %[6]s document terminator.
const rsNamedCodec = `
fn named_encode_%[2]s(v: &%[1]s, depth: i32) -> Result<Vec<u8>, Refusal> {
    match std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        let mut out = Vec::new();
        enc_%[2]s(&mut out, v, depth);
        out
    })) {
        Ok(out) => Ok(out),
        Err(p) => match p.downcast::<Refusal>() {
            Ok(e) => Err(*e),
            Err(p) => std::panic::resume_unwind(p),
        },
    }
}

pub fn decode_%[2]s_at(data: &[u8], depth: i32, limit: i32) -> Result<(%[1]s, usize), Refusal> {
    let mut r = Reader { buf: data, pos: 0, depth, limit: if limit < DEPTH_LIMIT { limit } else { DEPTH_LIMIT } };
    if depth < 0 || limit < 1 {
        return r.refuse("depth_exceeded");
    }
    r.skip_ws();
    %[3]s = decode_%[2]s(&mut r)?;
%[4]s    Ok((v, r.pos))
}

pub fn check_%[2]s(v: &%[1]s, depth: i32, limit: i32) -> Result<(), Refusal> {
    if depth < 0 || limit < 1 {
        return Err(Refusal { word: "depth_exceeded", offset: 0 });
    }
    let out = named_encode_%[2]s(v, depth)?;
    decode_%[2]s_at(&out, depth, limit).map(|_| ())
}

pub fn encode_%[2]s_at(v: &%[1]s, depth: i32) -> Result<Vec<u8>, Refusal> {
    if depth < 0 || depth >= DEPTH_LIMIT {
        return Err(Refusal { word: "depth_exceeded", offset: 0 });
    }
    let out = named_encode_%[2]s(v, depth)?;
    decode_%[2]s_at(&out, depth, DEPTH_LIMIT)?;
    Ok(out)
}

pub fn encode_%[2]s_document(v: &%[1]s) -> Result<Vec<u8>, Refusal> {
    %[5]s = encode_%[2]s_at(v, 0)?;
%[6]s    Ok(out)
}

pub fn decode_%[2]s_document(data: &[u8]) -> Result<%[1]s, Refusal> {
    let (v, n) = decode_%[2]s_at(data, 0, DEPTH_LIMIT)?;
    let mut r = Reader { buf: data, pos: n, depth: 0, limit: DEPTH_LIMIT };
    r.skip_ws();
    if r.pos != data.len() {
        return r.refuse("trailing_bytes");
    }
    Ok(v)
}
`

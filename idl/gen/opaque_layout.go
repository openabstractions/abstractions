package main

import (
	"fmt"
	"strings"
)

// The two opaque layouts ([DEF-E6]).
//
// Every backend's prelude carries the verbatim raw helper: it writes an opaque
// value's bytes as the caller holds them and drops the whitespace around the
// value. A definition that declares opaque = "reindent" gets the helper below
// in its place. It keeps every string token (escapes included), number
// spelling, member and member order exactly as they arrived, and writes the
// value's insignificant whitespace again at the declared indent, so two
// writers holding one value spelled on one line and on six write the same
// bytes. abstraction-job declares it because JOB-J1 compares record bytes
// between implementations and JOB-J7 leaves a payload's whitespace out.
//
// The helper lays a value out token by token and never fails. A malformed value
// handed to an encoder comes out malformed, and every reader refuses it.

const opaqueReindent = "reindent"

// reindentsOpaque reports whether s re-lays out its opaque values.
func reindentsOpaque(s *Definition) bool { return s.Encoding.Opaque == opaqueReindent }

var reindentRaw = map[string]string{
	"go": `// raw writes an opaque value's tokens as they are and lays its whitespace out
// again at the declared indent ([DEF-E6] opaque = "reindent").
func raw(out []byte, s string, depth int) []byte {
	ws := func(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }
	for i := 0; i < len(s); {
		c := s[i]
		if ws(c) {
			i++
			continue
		}
		switch c {
		case '"':
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
			if j > len(s) {
				j = len(s)
			}
			out = append(out, s[i:j]...)
			i = j
		case '{', '[':
			out = append(out, c)
			i++
			j := i
			for j < len(s) && ws(s[j]) {
				j++
			}
			if j < len(s) && (s[j] == '}' || s[j] == ']') {
				out = append(out, s[j])
				i = j + 1
			} else {
				depth++
				out = append(out, '\n')
				out = pad(out, depth)
			}
		case '}', ']':
			depth--
			out = append(out, '\n')
			out = pad(out, depth)
			out = append(out, c)
			i++
		case ',':
			out = append(out, ',', '\n')
			out = pad(out, depth)
			i++
		case ':':
			out = append(out, ':', ' ')
			i++
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}
`,
	"python": `def _raw(out, s, depth):
    # An opaque value's tokens as they are, its whitespace laid out again at
    # the declared indent ([DEF-E6] opaque = "reindent").
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
        elif c in (0x7B, 0x5B):
            out.append(c)
            i += 1
            j = i
            while j < n and b[j] in _WS:
                j += 1
            if j < n and b[j] in (0x7D, 0x5D):
                out.append(b[j])
                i = j + 1
            else:
                depth += 1
                out += b"\n"
                _pad(out, depth)
        elif c in (0x7D, 0x5D):
            depth -= 1
            out += b"\n"
            _pad(out, depth)
            out.append(c)
            i += 1
        elif c == 0x2C:
            out += b",\n"
            _pad(out, depth)
            i += 1
        elif c == 0x3A:
            out += b": "
            i += 1
        else:
            out.append(c)
            i += 1
`,
	"cpp": `// raw writes an opaque value's tokens as they are and lays its whitespace out
// again at the declared indent ([DEF-E6] opaque = "reindent").
inline void raw(std::string& out, const std::string& s, int depth) {
    const std::size_t n = s.size();
    for (std::size_t i = 0; i < n;) {
        const unsigned char c = static_cast<unsigned char>(s[i]);
        if (ws(c)) {
            ++i;
        } else if (c == '"') {
            std::size_t j = i + 1;
            while (j < n) {
                if (s[j] == '\\') { j += 2; continue; }
                if (s[j] == '"') { ++j; break; }
                ++j;
            }
            out.append(s, i, j - i);
            i = j;
        } else if (c == '{' || c == '[') {
            out += static_cast<char>(c);
            ++i;
            std::size_t j = i;
            while (j < n && ws(static_cast<unsigned char>(s[j]))) ++j;
            if (j < n && (s[j] == '}' || s[j] == ']')) {
                out += s[j];
                i = j + 1;
            } else {
                ++depth;
                out += '\n';
                pad(out, depth);
            }
        } else if (c == '}' || c == ']') {
            --depth;
            out += '\n';
            pad(out, depth);
            out += static_cast<char>(c);
            ++i;
        } else if (c == ',') {
            out += ",\n";
            pad(out, depth);
            ++i;
        } else if (c == ':') {
            out += ": ";
            ++i;
        } else {
            out += static_cast<char>(c);
            ++i;
        }
    }
}
`,
	"javascript": `// raw writes an opaque value's tokens as they are and lays its whitespace out
// again at the declared indent ([DEF-E6] opaque = "reindent").
function raw(out, s, depth) {
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
      if (j > b.length) j = b.length;
      for (let k = i; k < j; k++) out.byte(b[k]);
      i = j;
    } else if (c === 0x7b || c === 0x5b) {
      out.byte(c);
      i++;
      let j = i;
      while (j < b.length && isWs(b[j])) j++;
      if (j < b.length && (b[j] === 0x7d || b[j] === 0x5d)) { out.byte(b[j]); i = j + 1; }
      else { depth++; out.byte(0x0a); pad(out, depth); }
    } else if (c === 0x7d || c === 0x5d) {
      depth--;
      out.byte(0x0a);
      pad(out, depth);
      out.byte(c);
      i++;
    } else if (c === 0x2c) {
      out.ascii(",\n");
      pad(out, depth);
      i++;
    } else if (c === 0x3a) {
      out.ascii(": ");
      i++;
    } else {
      out.byte(c);
      i++;
    }
  }
}
`,
	"rust": `// raw writes an opaque value's tokens as they are and lays its whitespace out
// again at the declared indent ([DEF-E6] opaque = "reindent").
fn raw(out: &mut ::std::vec::Vec<u8>, b: &[u8], depth: i32) {
    let mut depth = depth;
    let mut i = 0;
    while i < b.len() {
        let c = b[i];
        if ws(c) {
            i += 1;
        } else if c == b'"' {
            let mut j = i + 1;
            while j < b.len() {
                if b[j] == b'\\' {
                    j += 2;
                    continue;
                }
                if b[j] == b'"' {
                    j += 1;
                    break;
                }
                j += 1;
            }
            let j = j.min(b.len());
            out.extend_from_slice(&b[i..j]);
            i = j;
        } else if c == b'{' || c == b'[' {
            out.push(c);
            i += 1;
            let mut j = i;
            while j < b.len() && ws(b[j]) {
                j += 1;
            }
            if j < b.len() && (b[j] == b'}' || b[j] == b']') {
                out.push(b[j]);
                i = j + 1;
            } else {
                depth += 1;
                out.push(b'\n');
                pad(out, depth);
            }
        } else if c == b'}' || c == b']' {
            depth -= 1;
            out.push(b'\n');
            pad(out, depth);
            out.push(c);
            i += 1;
        } else if c == b',' {
            out.extend_from_slice(b",\n");
            pad(out, depth);
            i += 1;
        } else if c == b':' {
            out.extend_from_slice(b": ");
            i += 1;
        } else {
            out.push(c);
            i += 1;
        }
    }
}
`,
}

// verbatimRawSpan finds the verbatim raw helper in a backend's prelude: from
// its first line to the end of its body.
func verbatimRawSpan(lang, prelude string) (int, int) {
	// keep is how much of end stays after the helper: Python's blank lines.
	start, end, keep := "// raw writes an opaque value's bytes as they are", "\n}\n", 0
	if lang == "python" {
		start, end, keep = "def _raw(out, s, depth):\n", "\n\n\n", 2
	}
	if strings.Count(prelude, start) != 1 {
		panic(fmt.Sprintf("opaqueLayout: the %s prelude no longer holds the verbatim raw helper once", lang))
	}
	i := strings.Index(prelude, start)
	j := strings.Index(prelude[i:], end)
	if j < 0 {
		panic(fmt.Sprintf("opaqueLayout: the %s verbatim raw helper has no end", lang))
	}
	return i, i + j + len(end) - keep
}

// opaqueLayout returns prelude with the raw helper the definition's opaque
// layout calls for.
func opaqueLayout(s *Definition, lang, prelude string) string {
	if !reindentsOpaque(s) {
		return prelude
	}
	body, ok := reindentRaw[lang]
	if !ok {
		panic("opaqueLayout: no reindenting raw helper for " + lang)
	}
	i, j := verbatimRawSpan(lang, prelude)
	return prelude[:i] + body + prelude[j:]
}

// reindentedOpaque reports whether t reaches an opaque value through a record
// a reindenting definition owns. That record's encoder lives in the owner's
// generated package and lays the value out again, and a service frame carries
// opaque bytes verbatim ([DEF-S1]).
func reindentedOpaque(s *Definition, t string, seen map[string]bool) bool {
	if imp, ok := s.importedRecord(t); ok {
		if reindentsOpaque(imp.Def) {
			return carriesOpaque(imp.Def, imp.Name, map[string]bool{})
		}
		return reindentedOpaque(imp.Def, imp.Name, map[string]bool{})
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	if elem := s.Repeated(t); elem != "" {
		return reindentedOpaque(s, elem, seen)
	}
	if st := s.Struct(t); st != nil {
		for _, f := range st.Fields {
			if reindentedOpaque(s, f.Type, seen) {
				return true
			}
		}
	}
	return false
}

// carriesOpaque reports whether t holds an opaque value at any depth: a json
// field, a raw-value map or list, or a struct that preserves unknown fields.
func carriesOpaque(s *Definition, t string, seen map[string]bool) bool {
	if serviceNestedOpaque(s, t, map[string]bool{}) {
		return true
	}
	if imp, ok := s.importedRecord(t); ok {
		return carriesOpaque(imp.Def, imp.Name, map[string]bool{})
	}
	if seen[t] {
		return false
	}
	seen[t] = true
	if elem := s.Repeated(t); elem != "" {
		return carriesOpaque(s, elem, seen)
	}
	if st := s.Struct(t); st != nil {
		if st.PreservesUnknown() {
			return true
		}
		for _, f := range st.Fields {
			if carriesOpaque(s, f.Type, seen) {
				return true
			}
		}
	}
	return false
}

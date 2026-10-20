package main

import (
	"fmt"
	"strconv"
	"strings"
)

const encodedSizeWord = "encoded_size"

// maxEncoded reads the validated compact-byte ceiling. It never substitutes
// for a physical worst-case size in a parent's static proof.
func (st Struct) maxEncoded() int64 {
	n, _ := strconv.ParseInt(st.Ann["max_encoded"], 10, 64)
	return n
}

func (s *Definition) hasMaxEncoded() bool {
	for _, st := range s.Structs {
		if st.maxEncoded() > 0 {
			return true
		}
	}
	return false
}

func (s *Definition) validateMaxEncoded(st Struct) error {
	text, ok := st.Ann["max_encoded"]
	if !ok {
		return nil
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n < 1 || n > recordCeiling || strconv.FormatInt(n, 10) != text {
		return fmt.Errorf("struct %s: max_encoded = %q must be a canonical positive decimal at most %d (IPC-F1)", st.Name, text, recordCeiling)
	}
	if !structureRefusal(s.Refusals, encodedSizeWord) {
		return fmt.Errorf("struct %s: max_encoded requires %s(stage=\"structure\")", st.Name, encodedSizeWord)
	}
	if st.Ann["chunk"] == "true" {
		return fmt.Errorf("struct %s: max_encoded and chunk = \"true\" are incompatible; aggregate frame bounds do not govern stream chunks", st.Name)
	}
	return nil
}

// The preflight counts compact bytes, retains no decoded content and leaves
// the real reader untouched. Typed decoding owns syntax/member validation,
// including duplicate_field versus duplicate_key precedence.
func emitMaxEncodedReader(b *strings.Builder, s *Definition, lang string) {
	if !s.hasMaxEncoded() {
		return
	}
	if lang != "cpp" {
		emitMaxEncodedWriter(b, s, lang)
	}
	body := map[string]string{
		"go":         maxEncodedGo,
		"python":     maxEncodedPython,
		"cpp":        maxEncodedCpp,
		"javascript": maxEncodedJS,
		"rust":       maxEncodedRust,
	}[lang]
	b.WriteString(strings.NewReplacer("@DEPTH@", strconv.Itoa(s.Encoding.DepthLimit), "@RAW_LIMIT@", strconv.FormatInt(recordCeiling, 10), "@RAW_WORK@", strconv.FormatInt(recordCeiling+1, 10)).Replace(body))
}

func emitMaxEncodedDecode(b *strings.Builder, st Struct, lang string) {
	if n := st.maxEncoded(); n > 0 {
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tif err := r.checkEncodedSize(%d); err != nil { return nil, err }\n", n)
		case "python":
			fmt.Fprintf(b, "    _check_encoded_size(r, %d)\n", n)
		case "cpp":
			fmt.Fprintf(b, "    check_encoded_size(r, %d);\n", n)
		case "javascript":
			fmt.Fprintf(b, "  checkEncodedSize(r, %d);\n", n)
		case "rust":
			fmt.Fprintf(b, "    check_encoded_size(r, %d)?;\n", n)
		}
	}
}

func emitMaxEncodedStart(b *strings.Builder, st Struct, lang string) {
	if st.maxEncoded() == 0 {
		return
	}
	b.WriteString(map[string]string{
		"go":         "\tencodedCounter := compactEncodedCounter{start: len(out), pos: len(out)}\n",
		"python":     "    encoded_counter = _CompactEncodedCounter(len(out))\n",
		"cpp":        "    CompactEncodedCounter encoded_counter(out.size());\n",
		"javascript": "  const encodedCounter = new CompactEncodedCounter(out.b.length);\n",
		"rust":       "    let mut encoded_counter = CompactEncodedCounter::new(out.len());\n",
	}[lang])
}

func emitMaxEncodedEnd(b *strings.Builder, st Struct, lang string) {
	if n := st.maxEncoded(); n > 0 {
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tencodedCounter.check(out, %d)\n", n)
		case "python":
			fmt.Fprintf(b, "    encoded_counter.check(out, %d)\n", n)
		case "cpp":
			fmt.Fprintf(b, "    encoded_counter.check(out, %d);\n", n)
		case "javascript":
			fmt.Fprintf(b, "  encodedCounter.check(out.b, %d);\n", n)
		case "rust":
			fmt.Fprintf(b, "    encoded_counter.check(out, %d);\n", n)
		}
	}
}

const maxEncodedGo = `
func (r *reader) checkEncodedSize(limit int) error {
	depth, quoted, escaped, count := r.depth, false, false, 0
	for i := r.pos; i < len(r.buf); i++ {
		if i-r.pos >= @RAW_LIMIT@ { return r.refuse("encoded_size") }
		c := r.buf[i]
		if !quoted && (c == ' ' || c == '\t' || c == '\r' || c == '\n') { continue }
		count++
		if count > limit { return r.refuse("encoded_size") }
		if quoted {
			if escaped { escaped = false } else if c == '\\' { escaped = true } else if c == '"' { quoted = false }
		} else {
			switch c {
			case '"': quoted = true
			case '{', '[':
				depth++
				if depth > r.depthLimit() { return r.refuse("depth_exceeded") }
			case '}', ']':
				depth--
				if depth <= r.depth { return nil }
			}
		}
	}
	return nil
}
`

const maxEncodedPython = `
def _check_encoded_size(r, limit):
    depth, quoted, escaped, count = r.depth, False, False, 0
    for i in range(r.pos, min(len(r.buf), r.pos + @RAW_WORK@)):
        if i-r.pos >= @RAW_LIMIT@: raise r.refuse("encoded_size")
        c = r.buf[i]
        if not quoted and c in (0x20, 9, 10, 13): continue
        count += 1
        if count > limit: raise r.refuse("encoded_size")
        if quoted:
            if escaped: escaped = False
            elif c == 0x5c: escaped = True
            elif c == 0x22: quoted = False
        elif c == 0x22: quoted = True
        elif c in (0x7b, 0x5b):
            depth += 1
            if depth > @DEPTH@: raise r.refuse("depth_exceeded")
        elif c in (0x7d, 0x5d):
            depth -= 1
            if depth <= r.depth: return
`

const maxEncodedCpp = `
inline void check_encoded_size(const Reader& r, std::size_t limit) {
    auto depth = r.depth; bool quoted = false, escaped = false; std::size_t count = 0;
    for (auto i = r.pos; i < r.buf.size(); ++i) {
        if (i-r.pos >= @RAW_LIMIT@) r.refuse("encoded_size");
        const auto c = r.buf[i];
        if (!quoted && (c == ' ' || c == '\t' || c == '\r' || c == '\n')) continue;
        if (++count > limit) r.refuse("encoded_size");
        if (quoted) {
            if (escaped) escaped = false;
            else if (c == '\\') escaped = true;
            else if (c == '"') quoted = false;
        } else if (c == '"') quoted = true;
        else if (c == '{' || c == '[') { if (++depth > r.depth_limit()) r.refuse("depth_exceeded"); }
        else if (c == '}' || c == ']') { if (--depth <= r.depth) return; }
    }
}
`

const maxEncodedJS = `
function checkEncodedSize(r, limit) {
  let depth = r.depth, quoted = false, escaped = false, count = 0;
  for (let i = r.pos; i < r.buf.length; i++) {
    if (i-r.pos >= @RAW_LIMIT@) throw r.refuse("encoded_size");
    const c = r.buf[i];
    if (!quoted && (c === 32 || c === 9 || c === 10 || c === 13)) continue;
    if (++count > limit) throw r.refuse("encoded_size");
    if (quoted) {
      if (escaped) escaped = false;
      else if (c === 0x5c) escaped = true;
      else if (c === 0x22) quoted = false;
    } else if (c === 0x22) quoted = true;
    else if (c === 0x7b || c === 0x5b) { if (++depth > @DEPTH@) throw r.refuse("depth_exceeded"); }
    else if (c === 0x7d || c === 0x5d) { if (--depth <= r.depth) return; }
  }
}
`

const maxEncodedRust = `
fn check_encoded_size(r: &Reader<'_>, limit: usize) -> ::std::result::Result<(), Refusal> {
    let mut depth = r.depth; let mut quoted = false; let mut escaped = false; let mut count: usize = 0;
    for i in r.pos..r.buf.len() {
        if i-r.pos >= @RAW_LIMIT@ { return r.refuse("encoded_size"); }
        let c = r.buf[i];
        if !quoted && matches!(c, b' ' | b'\t' | b'\r' | b'\n') { continue; }
        count += 1;
        if count > limit { return r.refuse("encoded_size"); }
        if quoted {
            if escaped { escaped = false; } else if c == b'\\' { escaped = true; } else if c == b'"' { quoted = false; }
        } else if c == b'"' { quoted = true; }
        else if c == b'{' || c == b'[' { depth += 1; if depth > @DEPTH@ { return r.refuse("depth_exceeded"); } }
        else if c == b'}' || c == b']' { depth -= 1; if depth <= r.depth { return Ok(()); } }
    }
    Ok(())
}
`

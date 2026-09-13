package main

import (
	"fmt"
	"strconv"
	"strings"
)

const jsEscMinimal = `
export function esc(out, s) {
  out.byte(0x22);
  for (const c of ENC.encode(s)) escByte(out, c);
  out.byte(0x22);
}
`

const jsEscASCII = `
function unit(out, u) {
  out.ascii("\\u" + HEX[(u >> 12) & 0xf] + HEX[(u >> 8) & 0xf] + HEX[(u >> 4) & 0xf] + HEX[u & 0xf]);
}

export function esc(out, s) {
  out.byte(0x22);
  for (const ch of s) {
    const cp = ch.codePointAt(0);
    if (cp < 0x80) escByte(out, cp);
    else if (cp > 0xffff) {
      const c = cp - 0x10000;
      unit(out, 0xd800 + (c >> 10));
      unit(out, 0xdc00 + (c & 0x3ff));
    } else unit(out, cp);
  }
  out.byte(0x22);
}
`

const jsCommon = `const HEX = "0123456789abcdef";
const ENC = new TextEncoder();
const SHORT = { 0x22: '\\"', 0x5c: "\\\\", 0x08: "\\b", 0x0c: "\\f", 0x0a: "\\n", 0x0d: "\\r", 0x09: "\\t" };

export class Out {
  constructor() { this.b = []; }
  byte(c) { this.b.push(c); }
  ascii(s) { for (let i = 0; i < s.length; i++) this.b.push(s.charCodeAt(i)); }
  bytes() { return Uint8Array.from(this.b); }
}

function escByte(out, c) {
  const short = SHORT[c];
  if (short !== undefined) out.ascii(short);
  else if (c < 0x20) out.ascii("\\u00" + HEX[c >> 4] + HEX[c & 0xf]);
  else out.byte(c);
}

// Every integer the definition calls i64 is a BigInt here, because Number
// rounds above 2^53 and two values in the conformance record are i64 extremes.
export function num(out, n) { out.ascii(BigInt(n).toString()); }

export function pad(out, depth) { for (let i = 0; i < depth * @INDENT@; i++) out.byte(0x20); }

export function strs(out, v, depth) {
  if (v.length === 0) { out.ascii("[]"); return; }
  out.ascii("[\n");
  for (let i = 0; i < v.length; i++) {
    pad(out, depth + 1);
    esc(out, v[i]);
    if (i + 1 < v.length) out.byte(0x2c);
    out.byte(0x0a);
  }
  pad(out, depth);
  out.byte(0x5d);
}

const isWs = (c) => c === 0x20 || c === 0x09 || c === 0x0a || c === 0x0d;

export function raw(out, s, depth) {
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

// A JavaScript string sorts by UTF-16 code unit, which puts U+1D11E before
// U+FFFD where every other target puts it after. The definition declares UTF-8
// byte order, so the comparison is done on the encoded bytes.
function byteLess(a, b) {
  const x = ENC.encode(a), y = ENC.encode(b);
  const n = Math.min(x.length, y.length);
  for (let i = 0; i < n; i++) if (x[i] !== y[i]) return x[i] - y[i];
  return x.length - y.length;
}

export function rawmap(out, m, depth) {
  const keys = Object.keys(m).sort(byteLess);
  if (keys.length === 0) { out.ascii("{}"); return; }
  out.ascii("{\n");
  for (let i = 0; i < keys.length; i++) {
    pad(out, depth + 1);
    esc(out, keys[i]);
    out.ascii(": ");
    raw(out, m[keys[i]], depth + 1);
    if (i + 1 < keys.length) out.byte(0x2c);
    out.byte(0x0a);
  }
  pad(out, depth);
  out.byte(0x7d);
}
`

const jsStrMap = `
export function strmap(out, m, depth) {
  const keys = Object.keys(m).sort(byteLess);
  if (keys.length === 0) { out.ascii("{}"); return; }
  out.ascii("{\n");
  for (let i = 0; i < keys.length; i++) {
    pad(out, depth + 1);
    esc(out, keys[i]);
    out.ascii(": ");
    esc(out, m[keys[i]]);
    if (i + 1 < keys.length) out.byte(0x2c);
    out.byte(0x0a);
  }
  pad(out, depth);
  out.byte(0x7d);
}
`

const jsEncList = `
export function encList(out, v, depth, enc) {
  if (v.length === 0) { out.ascii("[]"); return; }
  out.ascii("[\n");
  for (let i = 0; i < v.length; i++) {
    pad(out, depth + 1);
    enc(out, v[i], depth + 1);
    if (i + 1 < v.length) out.byte(0x2c);
    out.byte(0x0a);
  }
  pad(out, depth);
  out.byte(0x5d);
}
`

const jsDecodeCommon = `
// A BOM inside a JSON string is data, not a stream signature.
const DEC = new TextDecoder("utf-8", {ignoreBOM: true});
const DEPTH_LIMIT = @DEPTH@;
const I64_DIGITS = 19;
const UNESCAPE = { 0x22: 0x22, 0x5c: 0x5c, 0x2f: 0x2f, 0x62: 0x08, 0x66: 0x0c, 0x6e: 0x0a, 0x72: 0x0d, 0x74: 0x09 };

export class Refusal extends Error {
  constructor(word, offset) {
    super("refused: " + word + " at byte " + offset);
    this.name = "Refusal";
    this.word = word;
    this.offset = offset;
  }
}

function appendRune(out, cp) {
  if (cp < 0x80) out.push(cp);
  else if (cp < 0x800) out.push(0xc0 | (cp >> 6), 0x80 | (cp & 0x3f));
  else if (cp < 0x10000) out.push(0xe0 | (cp >> 12), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
  else out.push(0xf0 | (cp >> 18), 0x80 | ((cp >> 12) & 0x3f), 0x80 | ((cp >> 6) & 0x3f), 0x80 | (cp & 0x3f));
}

// TextDecoder substitutes U+FFFD for a bad sequence rather than reporting it, so
// the bytes are checked before they are decoded and the refusal says so.
function validUtf8(b) {
  for (let i = 0; i < b.length; ) {
    const c = b[i];
    if (c < 0x80) { i++; continue; }
    let n, cp;
    if (c >> 5 === 0x6) { n = 2; cp = c & 0x1f; }
    else if (c >> 4 === 0xe) { n = 3; cp = c & 0x0f; }
    else if (c >> 3 === 0x1e) { n = 4; cp = c & 0x07; }
    else return false;
    if (i + n > b.length) return false;
    for (let k = 1; k < n; k++) {
      if (b[i + k] >> 6 !== 0x2) return false;
      cp = (cp << 6) | (b[i + k] & 0x3f);
    }
    const lowest = [0, 0, 0x80, 0x800, 0x10000][n];
    if (cp < lowest || cp > 0x10ffff || (cp >= 0xd800 && cp <= 0xdfff)) return false;
    i += n;
  }
  return true;
}

class Reader {
  constructor(buf) { this.buf = buf; this.pos = 0; this.depth = 0; }

  refuse(word) { return new Refusal(word, this.pos); }

  at() { return this.pos < this.buf.length ? this.buf[this.pos] : 0; }

  digit() { const c = this.at(); return c >= 0x30 && c <= 0x39; }

  ws() { while (this.pos < this.buf.length && isWs(this.buf[this.pos])) this.pos++; }

  enter() {
    this.depth++;
    if (this.depth > DEPTH_LIMIT) throw this.refuse("depth_exceeded");
  }

  string() {
    if (this.at() !== 0x22) throw this.refuse("wrong_type");
    this.pos++;
    const out = [];
    for (;;) {
      if (this.pos >= this.buf.length) throw this.refuse("malformed");
      const c = this.buf[this.pos];
      if (c === 0x22) {
        this.pos++;
        const bytes = Uint8Array.from(out);
        if (!validUtf8(bytes)) throw this.refuse("bad_string");
        return DEC.decode(bytes);
      }
      if (c < 0x20) throw this.refuse("bad_string");
      if (c === 0x5c) this.escape(out);
      else { out.push(c); this.pos++; }
    }
  }

  escape(out) {
    this.pos++;
    if (this.pos >= this.buf.length) throw this.refuse("malformed");
    const c = this.buf[this.pos];
    this.pos++;
    const plain = UNESCAPE[c];
    if (plain !== undefined) { out.push(plain); return; }
    if (c !== 0x75) throw this.refuse("bad_string");
    let u = this.hex4();
    if (u >= 0xdc00 && u <= 0xdfff) throw this.refuse("bad_string");
    if (u >= 0xd800 && u <= 0xdbff) {
      if (this.pos + 1 >= this.buf.length || this.buf[this.pos] !== 0x5c || this.buf[this.pos + 1] !== 0x75)
        throw this.refuse("bad_string");
      this.pos += 2;
      const low = this.hex4();
      if (low < 0xdc00 || low > 0xdfff) throw this.refuse("bad_string");
      u = 0x10000 + ((u - 0xd800) << 10) + (low - 0xdc00);
    }
    appendRune(out, u);
  }

  hex4() {
    if (this.pos + 4 > this.buf.length) throw this.refuse("bad_string");
    let u = 0;
    for (let i = 0; i < 4; i++) {
      const c = this.buf[this.pos + i];
      if (c >= 0x30 && c <= 0x39) u = (u << 4) | (c - 0x30);
      else if (c >= 0x61 && c <= 0x66) u = (u << 4) | (c - 0x61 + 10);
      else if (c >= 0x41 && c <= 0x46) u = (u << 4) | (c - 0x41 + 10);
      else throw this.refuse("bad_string");
    }
    this.pos += 4;
    return u;
  }

  integer(low, high) {
    const c = this.at();
    if (c !== 0x2d && (c < 0x30 || c > 0x39)) throw this.refuse("wrong_type");
    const negative = c === 0x2d;
    if (negative) this.pos++;
    if (!this.digit()) throw this.refuse("malformed");
    const start = this.pos;
    if (this.at() === 0x30) this.pos++;
    else while (this.digit()) this.pos++;
    const after = this.at();
    if (after === 0x2e || after === 0x65 || after === 0x45 || this.digit()) throw this.refuse("number_spelling");
    if (this.pos - start > I64_DIGITS) throw this.refuse("number_spelling");
    let n = BigInt(String.fromCharCode(...this.buf.subarray(start, this.pos)));
    if (negative) {
      if (n === 0n) throw this.refuse("number_spelling");
      n = -n;
    }
    if (n < low || n > high) throw this.refuse("number_spelling");
    return n;
  }

  boolean() {
    if (this.at() === 0x74) { this.literal("true"); return true; }
    if (this.at() === 0x66) { this.literal("false"); return false; }
    throw this.refuse("wrong_type");
  }

  literal(word) {
    for (let i = 0; i < word.length; i++)
      if (this.buf[this.pos + i] !== word.charCodeAt(i)) throw this.refuse("malformed");
    this.pos += word.length;
  }

  strList() {
    if (this.at() !== 0x5b) throw this.refuse("wrong_type");
    this.enter();
    this.pos++;
    const out = [];
    this.ws();
    if (this.at() !== 0x5d) {
      for (;;) {
        this.ws();
        out.push(this.string());
        this.ws();
        if (this.at() !== 0x2c) break;
        this.pos++;
      }
    }
    if (this.at() !== 0x5d) throw this.refuse("malformed");
    this.pos++;
    this.depth--;
    return out;
  }

  rawMap() {
    if (this.at() !== 0x7b) throw this.refuse("wrong_type");
    this.enter();
    this.pos++;
    const out = Object.create(null);
    this.ws();
    if (this.at() !== 0x7d) {
      for (;;) {
        this.ws();
        if (this.at() !== 0x22) throw this.refuse("malformed");
        const k = this.string();
@DUPKEY@        this.ws();
        if (this.at() !== 0x3a) throw this.refuse("malformed");
        this.pos++;
        this.ws();
        out[k] = this.rawValue();
        this.ws();
        if (this.at() !== 0x2c) break;
        this.pos++;
      }
    }
    if (this.at() !== 0x7d) throw this.refuse("malformed");
    this.pos++;
    this.depth--;
    return out;
  }

  rawValue() {
    const start = this.pos;
    this.skipValue();
    return this.buf.subarray(start, this.pos);
  }

  skipValue() {
    const c = this.at();
    if (c === 0x22) this.skipString();
    else if (c === 0x7b || c === 0x5b) this.skipContainer();
    else if (c === 0x74) this.literal("true");
    else if (c === 0x66) this.literal("false");
    else if (c === 0x6e) this.literal("null");
    else if (c === 0x2d || (c >= 0x30 && c <= 0x39)) this.skipNumber();
    else throw this.refuse("malformed");
  }

  // An opaque value is validated and carried, never interpreted. Its syntax is
  // the record's own — the same string reader, so an escape, a control byte or a
  // lone surrogate is judged identically at any depth — and what it is free to
  // spell, it keeps: the bytes between these two offsets are what a writer
  // re-emits. A number is walked, never converted, so a value no host type holds
  // survives the reader that carried it.
  skipString() {
    this.string();
  }

  someDigits() {
    let n = 0;
    while (this.at() >= 0x30 && this.at() <= 0x39) { this.pos++; n++; }
    if (n === 0) throw this.refuse("number_spelling");
  }

  skipNumber() {
    if (this.at() === 0x2d) this.pos++;
    const c = this.at();
    if (c === 0x30) this.pos++;
    else if (c > 0x30 && c <= 0x39) { while (this.at() >= 0x30 && this.at() <= 0x39) this.pos++; }
    else throw this.refuse("number_spelling");
    if (this.at() === 0x2e) { this.pos++; this.someDigits(); }
    if (this.at() === 0x65 || this.at() === 0x45) {
      this.pos++;
      if (this.at() === 0x2b || this.at() === 0x2d) this.pos++;
      this.someDigits();
    }
  }

  skipContainer() {
    const opener = this.at();
    const shut = opener === 0x7b ? 0x7d : 0x5d;
    this.enter();
    this.pos++;
    this.ws();
    const seen = new Set();
    if (this.at() !== shut) {
      for (;;) {
        this.ws();
        if (opener === 0x7b) {
          if (this.at() !== 0x22) throw this.refuse("malformed");
          const k = this.string();
@SKIPDUP@          this.ws();
          if (this.at() !== 0x3a) throw this.refuse("malformed");
          this.pos++;
          this.ws();
        }
        this.skipValue();
        this.ws();
        if (this.at() !== 0x2c) break;
        this.pos++;
      }
    }
    if (this.at() !== shut) throw this.refuse("malformed");
    this.pos++;
    this.depth--;
  }
}
`

const jsStrMapDecode = `
function strMap(r) {
  if (r.at() !== 0x7b) throw r.refuse("wrong_type");
  r.enter();
  r.pos++;
  const out = Object.create(null);
  r.ws();
  if (r.at() !== 0x7d) {
    for (;;) {
      r.ws();
      if (r.at() !== 0x22) throw r.refuse("malformed");
      const k = r.string();
@STRDUP@      r.ws();
      if (r.at() !== 0x3a) throw r.refuse("malformed");
      r.pos++;
      r.ws();
      out[k] = r.string();
      r.ws();
      if (r.at() !== 0x2c) break;
      r.pos++;
    }
  }
  if (r.at() !== 0x7d) throw r.refuse("malformed");
  r.pos++;
  r.depth--;
  return out;
}
`

const jsDecodeList = `
function decodeList(r, elem) {
  if (r.at() !== 0x5b) throw r.refuse("wrong_type");
  r.enter();
  r.pos++;
  const out = [];
  r.ws();
  if (r.at() !== 0x5d) {
    for (;;) {
      r.ws();
      out.push(elem(r));
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

const jsDupKeyRefuse = `        if (k in out) throw this.refuse("duplicate_key");
`

const jsStrDupKey = `      if (k in out) throw r.refuse("duplicate_key");
`

const jsSkipDupKey = `          if (seen.has(k)) throw this.refuse("duplicate_key");
          seen.add(k);
`

const jsTimestamp = `
const isDigits = (s, i, n) => {
  if (s.length < i + n) return false;
  for (let k = 0; k < n; k++) if (s[i + k] < "0" || s[i + k] > "9") return false;
  return true;
};

const datePart = (s) =>
  s.length >= 19 && isDigits(s, 0, 4) && s[4] === "-" && isDigits(s, 5, 2) && s[7] === "-" &&
  isDigits(s, 8, 2) && isDigits(s, 11, 2) && s[13] === ":" && isDigits(s, 14, 2) && s[16] === ":" &&
  isDigits(s, 17, 2);

// [DEF-G1] rfc3339-wide: what a reader accepts. Any fraction of one to nine
// digits or none, either case of the separators, and a numeric offset.
function lexicalTimestamp(s) {
  if (!datePart(s) || (s[10] !== "T" && s[10] !== "t")) return false;
  let i = 19;
  if (i < s.length && s[i] === ".") {
    i++;
    const start = i;
    while (i < s.length && s[i] >= "0" && s[i] <= "9") i++;
    if (i - start < 1 || i - start > 9) return false;
  }
  if (i >= s.length) return false;
  if (s[i] === "Z" || s[i] === "z") return i + 1 === s.length;
  if (s[i] !== "+" && s[i] !== "-") return false;
  return s.length === i + 6 && isDigits(s, i + 1, 2) && s[i + 3] === ":" && isDigits(s, i + 4, 2);
}

// [DEF-G2] rfc3339-micros: what a writer emits. Exactly six fractional digits,
// upper-case separators, UTC.
export function microsTimestamp(s) {
  return s.length === 27 && normalizedTimestamp(s) === s;
}

function readTimestamp(r) {
  const at = r.pos;
  const s = r.string();
  if (!wideTimestamp(s)) {
    r.pos = at;
    throw r.refuse("bad_timestamp");
  }
  return s;
}
`

func jsDecoder(b *strings.Builder, s *Definition) {
	for _, st := range s.Structs {
		fmt.Fprintf(b, "\nexport function new%s() {\n  return {", st.Name)
		for i, f := range st.Fields {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(b, " %s: %s", f.Ident("javascript"), jsDefault(s, f))
		}
		if st.PreservesUnknown() {
			if len(st.Fields) > 0 {
				b.WriteString(",")
			}
			b.WriteString(" extras: Object.create(null)")
		}
		b.WriteString(" };\n}\n")
	}
	for _, st := range s.Structs {
		jsStructDecoder(b, s, st)
	}
	if s.Document != "" {
		fmt.Fprintf(b, "\nexport function decode(data) {\n  const r = new Reader(data);\n  r.ws();\n  const v = decode_%s(r);\n", lower(s.Document))
		b.WriteString("  r.ws();\n  if (r.pos < r.buf.length) throw r.refuse(\"trailing_bytes\");\n")
		if s.Vocab != nil {
			b.WriteString("  derive(r, v);\n")
		}
		b.WriteString("  return v;\n}\n")
	}
	jsDerive(b, s)
}

func jsStructDecoder(b *strings.Builder, s *Definition, st Struct) {
	fmt.Fprintf(b, "\nfunction decode_%s(r) {\n", lower(st.Name))
	b.WriteString("  if (r.at() !== 0x7b) throw r.refuse(\"wrong_type\");\n  r.enter();\n  r.pos++;\n")
	fmt.Fprintf(b, "  const v = new%s();\n  let seen = 0;\n  r.ws();\n", st.Name)
	b.WriteString("  if (r.at() !== 0x7d) {\n    for (;;) {\n      r.ws();\n")
	b.WriteString("      if (r.at() !== 0x22) throw r.refuse(\"malformed\");\n      const key = r.string();\n")
	b.WriteString("      r.ws();\n      if (r.at() !== 0x3a) throw r.refuse(\"malformed\");\n      r.pos++;\n      r.ws();\n")
	for i, f := range st.Fields {
		kw := "} else if"
		if i == 0 {
			kw = "      if"
		}
		fmt.Fprintf(b, "%s (key === %q) {\n", kw, f.Name)
		fmt.Fprintf(b, "        if (seen & %d) throw r.refuse(\"duplicate_field\");\n", 1<<i)
		fmt.Fprintf(b, "        seen |= %d;\n", 1<<i)
		fmt.Fprintf(b, "        v.%s = %s;\n      ", f.Ident("javascript"), jsRead(s, f))
	}
	if len(st.Fields) == 0 {
		b.WriteString("      if (false) {\n      ")
	}
	b.WriteString("} else {\n")
	if st.RefuseUnknown() {
		b.WriteString("        throw r.refuse(\"unknown_field\");\n")
	} else if st.PreservesUnknown() {
		if s.Encoding.RefuseDuplicateKeys() {
			b.WriteString("        if (Object.hasOwn(v.extras, key)) throw r.refuse(\"duplicate_key\");\n")
		}
		b.WriteString("        v.extras[key] = r.rawValue();\n")
	} else {
		b.WriteString("        r.skipValue();\n")
	}
	b.WriteString("      }\n      r.ws();\n      if (r.at() !== 0x2c) break;\n      r.pos++;\n    }\n  }\n")
	b.WriteString("  if (r.at() !== 0x7d) throw r.refuse(\"malformed\");\n  r.pos++;\n  r.depth--;\n")
	if req := requiredMask(st); req != 0 {
		fmt.Fprintf(b, "  if (((seen & %d) >>> 0) !== %d) throw r.refuse(\"missing_field\");\n", req, req)
	}
	emitEqualities(b, st, "javascript", false)
	emitEnumChecks(b, st, "javascript", false)
	b.WriteString("  return v;\n}\n")
}

func jsDefault(s *Definition, f Field) string {
	if f.Omit == "absent" && (s.IsStruct(f.Type) || enumAbsent(f) || f.Type == "binary") {
		return "null"
	}
	switch f.Type {
	case "binary":
		return "new Uint8Array(0)"
	case "string", "json":
		return `""`
	case "i32":
		return "0"
	case "i64":
		return "0n"
	case "bool":
		return "false"
	case "list<string>", "list<json>":
		return "[]"
	case "map<string,json>", "map<string,string>":
		return "{}"
	}
	if s.Repeated(f.Type) != "" {
		return "[]"
	}
	return "new" + f.Type + "()"
}

func jsRead(s *Definition, f Field) string {
	switch f.Type {
	case "binary":
		return "readBinary(r)"
	case "string":
		if f.Grammar.Named() {
			return "readTimestamp(r)"
		}
		return "r.string()"
	case "i32":
		return "Number(r.integer(-2147483648n, 2147483647n))"
	case "i64":
		return "r.integer(-9223372036854775808n, 9223372036854775807n)"
	case "bool":
		return "r.boolean()"
	case "json":
		return "r.rawValue()"
	case "list<string>":
		return "r.strList()"
	case "map<string,json>":
		return "r.rawMap()"
	case "list<json>":
		return "rawList(r)"
	case "map<string,string>":
		return "strMap(r)"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "decodeList(r, decode_" + lower(elem) + ")"
	}
	return "decode_" + lower(f.Type) + "(r)"
}

func jsDerive(b *strings.Builder, s *Definition) {
	v := s.Vocab
	if v == nil {
		return
	}
	fmt.Fprintf(b, "\nexport const %sTerms = [", lowerCamel(v.Name))
	for i, t := range v.Terms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("];\n")
	fmt.Fprintf(b, "export const %sStripCritical = [", lowerCamel(v.Name))
	first := true
	for _, t := range v.Terms {
		if !t.StripCritical {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		first = false
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("];\n")
	b.WriteString("\nfunction derive(r, v) {\n")
	fmt.Fprintf(b, "  const present = new Set(v.%s);\n", v.Names)
	b.WriteString("  const kept = [];\n")
	fmt.Fprintf(b, "  for (const name of v.%s) {\n", v.Critical)
	fmt.Fprintf(b, "    if (%sStripCritical.includes(name)) continue;\n", lowerCamel(v.Name))
	fmt.Fprintf(b, "    if (!%sTerms.includes(name)) throw r.refuse(\"unknown_critical\");\n", lowerCamel(v.Name))
	b.WriteString("    if (!present.has(name)) throw r.refuse(\"not_a_subset\");\n    kept.push(name);\n  }\n")
	fmt.Fprintf(b, "  v.%s = kept;\n", v.Critical)
	for _, t := range v.Terms {
		fmt.Fprintf(b, "  if (%s !== present.has(%q)) throw r.refuse(\"content_mismatch\");\n", jsTest(s, t), t.Name)
	}
	b.WriteString("}\n")
	if v.UsesMember() {
		b.WriteString(jsMember)
	}
}

func jsTest(s *Definition, t Term) string {
	if t.Always() {
		return "true"
	}
	e := "v"
	for _, f := range t.Path {
		e += "." + f.Ident("javascript")
	}
	switch {
	case t.Member != "":
		return fmt.Sprintf("member(%s, %q)", e, t.Member)
	case t.Membership():
		parts := make([]string, len(t.Is))
		for i, w := range t.Is {
			parts[i] = fmt.Sprintf("%q", w)
		}
		return fmt.Sprintf("[%s].includes(%s)", strings.Join(parts, ", "), e)
	}
	return "Boolean(" + jsPresent(s, t.Last(), e) + ")"
}

const jsMember = `
// [DEF-A8] Whether an opaque value is an object naming this member with
// something other than null. The key is decoded, so two spellings of one name
// are one name; the value is neither decoded nor judged.
export function member(raw, name) {
  const r = new Reader(typeof raw === "string" ? ENC.encode(raw) : raw);
  try {
    r.ws();
    if (r.at() !== 0x7b) return false;
    r.pos++;
    r.ws();
    while (r.at() === 0x22) {
      const k = r.string();
      r.ws();
      r.pos++;
      r.ws();
      if (k === name) return r.at() !== 0x6e;
      r.skipValue();
      r.ws();
      if (r.at() !== 0x2c) return false;
      r.pos++;
      r.ws();
    }
  } catch (e) {
    if (e instanceof Refusal) return false;
    throw e;
  }
  return false;
}
`

func genJS(s *Definition) string {
	s = enumCarriers(s)
	if s.NoIPC {
		return genInterfaceOnly(s, "javascript")
	}
	s = serviceTypes(s)
	var b strings.Builder
	esc := jsEscMinimal
	if s.Encoding.EscapeNonASCII() {
		esc = jsEscASCII
	}
	prelude := jsCommon + esc
	if hasBinary(s) {
		prelude += jsBinary
	}
	if s.StringMapDocument() {
		prelude += jsStrMap
	}
	if s.HasRepeated() {
		prelude += jsEncList
	}
	b.WriteString(strings.NewReplacer("@INDENT@", strconv.Itoa(s.Encoding.Indent)).Replace(prelude))
	jsVocabulary(&b, s)
	for _, st := range s.Structs {
		if !s.Envelope(st.Name) {
			jsEncoder(&b, s, st, false)
		}
	}
	tail := ""
	if s.Encoding.TrailingNewline() {
		tail = "  out.byte(0x0a);\n"
	}
	if s.Document != "" {
		fmt.Fprintf(&b, "\nexport function encode(v) {\n  const out = new Out();\n  enc_%s(out, v, 0);\n%s  return out.bytes();\n}\n",
			lower(s.Document), tail)
	}
	dup, skipDup, strDup := "", "", ""
	if s.Encoding.RefuseDuplicateKeys() {
		dup, skipDup, strDup = jsDupKeyRefuse, jsSkipDupKey, jsStrDupKey
	}
	decode := jsDecodeCommon
	if s.HasStringMap() {
		decode += jsStrMapDecode
	}
	if s.HasRepeated() {
		decode += jsDecodeList
	}
	b.WriteString(strings.NewReplacer(
		"@DEPTH@", strconv.Itoa(s.Encoding.DepthLimit),
		"@DUPKEY@", dup,
		"@SKIPDUP@", skipDup,
		"@STRDUP@", strDup,
	).Replace(decode))
	if s.Timestamps() {
		b.WriteString(jsTimestamp)
		b.WriteString(jsTimestampNormalize)
	}
	if s.PreservesUnknown() {
		b.WriteString(jsPreserve)
	}
	jsDecoder(&b, s)
	jsProtocol(&b, s)
	jsService(&b, s)
	return b.String()
}

func jsVocabulary(b *strings.Builder, s *Definition) {
	for _, en := range s.Enums {
		fmt.Fprintf(b, "\nexport const %sNames = [", en.Name)
		for i, m := range en.Members {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%q", m.Name)
		}
		b.WriteString("];\n")
		fmt.Fprintf(b, "export const %s%s = %q;\n", en.Name, enumPolicyName(en, "javascript"), en.Ann["unknown"])
		for _, key := range en.MemberAnn() {
			fmt.Fprintf(b, "export const %s%s = {\n", en.Name, exported(key))
			for _, m := range en.Members {
				if v, ok := m.Ann[key]; ok {
					fmt.Fprintf(b, "  %q: %q,\n", m.Name, v)
				}
			}
			b.WriteString("};\n")
		}
	}
	for _, c := range s.Consts {
		fmt.Fprintf(b, "\nexport const %s = [", lowerCamel(c.Name))
		if c.Type == "list<i32>" {
			for i, n := range c.Ints {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%d", n)
			}
		} else {
			for i, v := range c.Strings {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%q", v)
			}
		}
		b.WriteString("];\n")
	}
}

func jsEncoder(b *strings.Builder, s *Definition, st Struct, flat bool) {
	p := plan(st)
	if flat {
		fmt.Fprintf(b, "\nfunction enc_wire_%s(out, v) {\n", lower(st.Name))
	} else {
		fmt.Fprintf(b, "\nexport function enc_%s(out, v, depth) {\n", lower(st.Name))
	}
	emitEqualities(b, st, "javascript", true)
	emitEnumChecks(b, st, "javascript", true)
	b.WriteString("  out.byte(0x7b);\n")
	if p.flag {
		b.WriteString("  let first = true;\n")
	}
	for i, f := range st.Fields {
		e := "v." + f.Ident("javascript")
		ind := "  "
		if f.Omit != "never" {
			fmt.Fprintf(b, "  if (%s) {\n", jsPresent(s, f, e))
			ind = "    "
		}
		switch p.before[i] {
		case "always":
			fmt.Fprintf(b, "%sout.byte(0x2c);\n", ind)
		case "flag":
			fmt.Fprintf(b, "%sif (!first) out.byte(0x2c);\n", ind)
		}
		if p.clears(i) {
			fmt.Fprintf(b, "%sfirst = false;\n", ind)
		}
		if flat {
			fmt.Fprintf(b, "%sesc(out, %q);\n%sout.byte(0x3a);\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, jsValueFlat(s, f, e))
		} else {
			fmt.Fprintf(b, "%sout.byte(0x0a);\n%spad(out, depth + 1);\n", ind, ind)
			fmt.Fprintf(b, "%sesc(out, %q);\n%sout.ascii(\": \");\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, jsValue(s, f, e))
		}
		if f.Omit != "never" {
			b.WriteString("  }\n")
		}
	}
	if st.PreservesUnknown() {
		fmt.Fprintf(b, "  first = extraFields(out, v.extras, [%s], depth, first);\n", quotedFieldNames(st))
	}
	if !flat {
		switch {
		case p.closeAlways:
			b.WriteString("  out.byte(0x0a);\n  pad(out, depth);\n")
		case len(st.Fields) > 0 || st.PreservesUnknown():
			b.WriteString("  if (!first) { out.byte(0x0a); pad(out, depth); }\n")
		}
	}
	b.WriteString("  out.byte(0x7d);\n}\n")
}

func jsPresent(s *Definition, f Field, e string) string {
	if f.Omit == "absent" && (s.IsStruct(f.Type) || enumAbsent(f) || f.Type == "binary") {
		return e + " !== undefined && " + e + " !== null"
	}
	switch f.Type {
	case "binary":
		return "binaryPresent(" + e + ")"
	case "i64":
		return e + " !== 0n"
	case "i32":
		return e + " !== 0"
	case "bool":
		return e
	case "list<string>", "list<json>":
		return e + ".length !== 0"
	case "map<string,json>", "map<string,string>":
		return "Object.keys(" + e + ").length !== 0"
	}
	if s.Repeated(f.Type) != "" {
		return e + ".length !== 0"
	}
	return e + ` !== ""`
}

func jsValue(s *Definition, f Field, e string) string {
	if f.Type == "json" && f.Ann["service_raw"] == "true" {
		return "for (const byte of (typeof " + e + " === \"string\" ? ENC.encode(" + e + ") : " + e + ")) out.byte(byte);"
	}
	if f.Grammar.Named() {
		return "esc(out, writeTimestamp(" + e + "));"
	}
	switch f.Type {
	case "binary":
		return "esc(out, encodeBinary(" + e + "));"
	case "string":
		return "esc(out, " + e + ");"
	case "i32", "i64":
		return "num(out, " + e + ");"
	case "bool":
		return "out.ascii(" + e + ` ? "true" : "false");`
	case "json":
		return "raw(out, " + e + ", depth + 1);"
	case "list<string>":
		return "strs(out, " + e + ", depth + 1);"
	case "map<string,json>":
		return "rawmap(out, " + e + ", depth + 1);"
	case "map<string,string>":
		return "strmap(out, " + e + ", depth + 1);"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "encList(out, " + e + ", depth + 1, enc_" + lower(elem) + ");"
	}
	return "enc_" + lower(f.Type) + "(out, " + e + ", depth + 1);"
}

func lowerCamel(n string) string {
	s := exported(n)
	return strings.ToLower(s[:1]) + s[1:]
}

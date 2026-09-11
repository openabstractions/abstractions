package main

import (
	"fmt"
	"strconv"
	"strings"
)

const pyEscMinimal = `
def esc(out, s):
    out += b'"'
    for c in s.encode("utf-8"):
        esc_byte(out, c)
    out += b'"'
`

const pyEscASCII = `
def esc(out, s):
    out += b'"'
    for ch in s:
        cp = ord(ch)
        if cp < 0x80:
            esc_byte(out, cp)
        elif cp > 0xFFFF:
            cp -= 0x10000
            unit(out, 0xD800 + (cp >> 10))
            unit(out, 0xDC00 + (cp & 0x3FF))
        else:
            unit(out, cp)
    out += b'"'


def unit(out, u):
    out += b"\\u" + bytes([_HEX[(u >> 12) & 0xF], _HEX[(u >> 8) & 0xF],
                           _HEX[(u >> 4) & 0xF], _HEX[u & 0xF]])
`

const pyCommon = `_HEX = b"0123456789abcdef"
_WS = (0x20, 0x09, 0x0A, 0x0D)
_SHORT = {0x22: b'\\"', 0x5C: b"\\\\", 0x08: b"\\b", 0x0C: b"\\f",
          0x0A: b"\\n", 0x0D: b"\\r", 0x09: b"\\t"}


def esc_byte(out, c):
    short = _SHORT.get(c)
    if short is not None:
        out += short
    elif c < 0x20:
        out += b"\\u00" + bytes([_HEX[c >> 4], _HEX[c & 0xF]])
    else:
        out.append(c)


def num(out, n):
    out += str(int(n)).encode("ascii")


def pad(out, depth):
    out += b" " * (depth * @INDENT@)


def strs(out, v, depth):
    if not v:
        out += b"[]"
        return
    out += b"[\n"
    for i, s in enumerate(v):
        pad(out, depth + 1)
        esc(out, s)
        if i + 1 < len(v):
            out += b","
        out += b"\n"
    pad(out, depth)
    out += b"]"


def raw(out, s, depth):
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
                pad(out, depth)
        elif c in (0x7D, 0x5D):
            depth -= 1
            out += b"\n"
            pad(out, depth)
            out.append(c)
            i += 1
        elif c == 0x2C:
            out += b",\n"
            pad(out, depth)
            i += 1
        elif c == 0x3A:
            out += b": "
            i += 1
        else:
            out.append(c)
            i += 1


def rawmap(out, m, depth):
    keys = sorted(m, key=lambda k: k.encode("utf-8"))
    if not keys:
        out += b"{}"
        return
    out += b"{\n"
    for i, k in enumerate(keys):
        pad(out, depth + 1)
        esc(out, k)
        out += b": "
        raw(out, m[k], depth + 1)
        if i + 1 < len(keys):
            out += b","
        out += b"\n"
    pad(out, depth)
    out += b"}"
`

const pyStrMap = `

def strmap(out, m, depth):
    keys = sorted(m, key=lambda k: k.encode("utf-8"))
    if not keys:
        out += b"{}"
        return
    out += b"{\n"
    for i, k in enumerate(keys):
        pad(out, depth + 1)
        esc(out, k)
        out += b": "
        esc(out, m[k])
        if i + 1 < len(keys):
            out += b","
        out += b"\n"
    pad(out, depth)
    out += b"}"
`

const pyEncList = `

def enc_list(out, v, depth, enc):
    if not v:
        out += b"[]"
        return
    out += b"[\n"
    for i, x in enumerate(v):
        pad(out, depth + 1)
        enc(out, x, depth + 1)
        if i + 1 < len(v):
            out += b","
        out += b"\n"
    pad(out, depth)
    out += b"]"
`

const pyDecodeCommon = `

_QUOTE, _BACKSLASH, _COLON, _COMMA = 0x22, 0x5C, 0x3A, 0x2C
_LBRACE, _RBRACE, _LBRACK, _RBRACK = 0x7B, 0x7D, 0x5B, 0x5D
_MINUS, _ZERO, _NINE, _DOT, _LOWER_U, _LOWER_N = 0x2D, 0x30, 0x39, 0x2E, 0x75, 0x6E
_UNESCAPE = {0x22: 0x22, 0x5C: 0x5C, 0x2F: 0x2F, 0x62: 0x08,
             0x66: 0x0C, 0x6E: 0x0A, 0x72: 0x0D, 0x74: 0x09}
_DEPTH_LIMIT = @DEPTH@
_I64_DIGITS = 19


class Refusal(ValueError):
    def __init__(self, word, offset):
        super().__init__("refused: %s at byte %d" % (word, offset))
        self.word = word
        self.offset = offset


class _Reader:
    __slots__ = ("buf", "pos", "depth")

    def __init__(self, buf):
        self.buf = buf
        self.pos = 0
        self.depth = 0

    def refuse(self, word):
        return Refusal(word, self.pos)

    def at(self):
        return self.buf[self.pos] if self.pos < len(self.buf) else 0

    def ws(self):
        while self.pos < len(self.buf) and self.buf[self.pos] in _WS:
            self.pos += 1

    def enter(self):
        self.depth += 1
        if self.depth > _DEPTH_LIMIT:
            raise self.refuse("depth_exceeded")

    def digit(self):
        return _ZERO <= self.at() <= _NINE

    def string(self):
        if self.at() != _QUOTE:
            raise self.refuse("wrong_type")
        self.pos += 1
        out = bytearray()
        while True:
            if self.pos >= len(self.buf):
                raise self.refuse("malformed")
            c = self.buf[self.pos]
            if c == _QUOTE:
                self.pos += 1
                try:
                    return out.decode("utf-8")
                except UnicodeDecodeError:
                    raise self.refuse("bad_string") from None
            if c < 0x20:
                raise self.refuse("bad_string")
            if c == _BACKSLASH:
                self.escape(out)
            else:
                out.append(c)
                self.pos += 1

    def escape(self, out):
        self.pos += 1
        if self.pos >= len(self.buf):
            raise self.refuse("malformed")
        c = self.buf[self.pos]
        self.pos += 1
        plain = _UNESCAPE.get(c)
        if plain is not None:
            out.append(plain)
            return
        if c != _LOWER_U:
            raise self.refuse("bad_string")
        u = self.hex4()
        if 0xDC00 <= u <= 0xDFFF:
            raise self.refuse("bad_string")
        if 0xD800 <= u <= 0xDBFF:
            if (self.pos + 1 >= len(self.buf) or self.buf[self.pos] != _BACKSLASH
                    or self.buf[self.pos + 1] != _LOWER_U):
                raise self.refuse("bad_string")
            self.pos += 2
            low = self.hex4()
            if not 0xDC00 <= low <= 0xDFFF:
                raise self.refuse("bad_string")
            u = 0x10000 + ((u - 0xD800) << 10) + (low - 0xDC00)
        out += chr(u).encode("utf-8")

    def hex4(self):
        if self.pos + 4 > len(self.buf):
            raise self.refuse("bad_string")
        u = 0
        for i in range(4):
            c = self.buf[self.pos + i]
            if _ZERO <= c <= _NINE:
                u = u << 4 | (c - _ZERO)
            elif 0x61 <= c <= 0x66:
                u = u << 4 | (c - 0x61 + 10)
            elif 0x41 <= c <= 0x46:
                u = u << 4 | (c - 0x41 + 10)
            else:
                raise self.refuse("bad_string")
        self.pos += 4
        return u

    def integer(self, low, high):
        c = self.at()
        if c != _MINUS and not _ZERO <= c <= _NINE:
            raise self.refuse("wrong_type")
        negative = c == _MINUS
        if negative:
            self.pos += 1
        if not self.digit():
            raise self.refuse("malformed")
        start = self.pos
        if self.at() == _ZERO:
            self.pos += 1
        else:
            while self.digit():
                self.pos += 1
        if self.at() in (_DOT, 0x65, 0x45) or self.digit():
            raise self.refuse("number_spelling")
        if self.pos - start > _I64_DIGITS:
            raise self.refuse("number_spelling")
        n = int(self.buf[start:self.pos])
        if negative:
            if n == 0:
                raise self.refuse("number_spelling")
            n = -n
        if n < low or n > high:
            raise self.refuse("number_spelling")
        return n

    def boolean(self):
        if self.at() == 0x74:
            self.literal(b"true")
            return True
        if self.at() == 0x66:
            self.literal(b"false")
            return False
        raise self.refuse("wrong_type")

    def literal(self, word):
        if self.buf[self.pos:self.pos + len(word)] != word:
            raise self.refuse("malformed")
        self.pos += len(word)

    def str_list(self):
        if self.at() != _LBRACK:
            raise self.refuse("wrong_type")
        self.enter()
        self.pos += 1
        out = []
        self.ws()
        if self.at() != _RBRACK:
            while True:
                self.ws()
                out.append(self.string())
                self.ws()
                if self.at() != _COMMA:
                    break
                self.pos += 1
        if self.at() != _RBRACK:
            raise self.refuse("malformed")
        self.pos += 1
        self.depth -= 1
        return out

    def raw_map(self):
        if self.at() != _LBRACE:
            raise self.refuse("wrong_type")
        self.enter()
        self.pos += 1
        out = {}
        self.ws()
        if self.at() != _RBRACE:
            while True:
                self.ws()
                if self.at() != _QUOTE:
                    raise self.refuse("malformed")
                k = self.string()
@DUPKEY@                self.ws()
                if self.at() != _COLON:
                    raise self.refuse("malformed")
                self.pos += 1
                self.ws()
                out[k] = self.raw_value()
                self.ws()
                if self.at() != _COMMA:
                    break
                self.pos += 1
        if self.at() != _RBRACE:
            raise self.refuse("malformed")
        self.pos += 1
        self.depth -= 1
        return out

    def raw_value(self):
        start = self.pos
        self.skip_value()
        return self.buf[start:self.pos]

    def skip_value(self):
        c = self.at()
        if c == _QUOTE:
            self.skip_string()
        elif c in (_LBRACE, _LBRACK):
            self.skip_container()
        elif c == 0x74:
            self.literal(b"true")
        elif c == 0x66:
            self.literal(b"false")
        elif c == 0x6E:
            self.literal(b"null")
        elif c == _MINUS or _ZERO <= c <= _NINE:
            self.skip_number()
        else:
            raise self.refuse("malformed")

    # An opaque value is validated and carried, never interpreted. Its syntax is
    # the record's own — the same string reader, so an escape, a control byte or
    # a lone surrogate is judged identically at any depth — and what it is free
    # to spell, it keeps: the bytes between these two offsets are what a writer
    # re-emits. A number is walked, never converted, so a value no host type
    # holds survives the reader that carried it.
    def skip_string(self):
        self.string()

    def some_digits(self):
        n = 0
        while _ZERO <= self.at() <= _NINE:
            self.pos += 1
            n += 1
        if n == 0:
            raise self.refuse("number_spelling")

    def skip_number(self):
        if self.at() == _MINUS:
            self.pos += 1
        c = self.at()
        if c == _ZERO:
            self.pos += 1
        elif _ZERO < c <= _NINE:
            while _ZERO <= self.at() <= _NINE:
                self.pos += 1
        else:
            raise self.refuse("number_spelling")
        if self.at() == _DOT:
            self.pos += 1
            self.some_digits()
        if self.at() in (0x65, 0x45):
            self.pos += 1
            if self.at() in (0x2B, _MINUS):
                self.pos += 1
            self.some_digits()

    def skip_container(self):
        opener = self.at()
        shut = _RBRACE if opener == _LBRACE else _RBRACK
        self.enter()
        self.pos += 1
        self.ws()
        seen = set()
        if self.at() != shut:
            while True:
                self.ws()
                if opener == _LBRACE:
                    if self.at() != _QUOTE:
                        raise self.refuse("malformed")
                    k = self.string()
@SKIPDUP@                    self.ws()
                    if self.at() != _COLON:
                        raise self.refuse("malformed")
                    self.pos += 1
                    self.ws()
                self.skip_value()
                self.ws()
                if self.at() != _COMMA:
                    break
                self.pos += 1
        if self.at() != shut:
            raise self.refuse("malformed")
        self.pos += 1
        self.depth -= 1
`

const pyStrMapDecode = `
    def str_map(self):
        if self.at() != _LBRACE:
            raise self.refuse("wrong_type")
        self.enter()
        self.pos += 1
        out = {}
        self.ws()
        if self.at() != _RBRACE:
            while True:
                self.ws()
                if self.at() != _QUOTE:
                    raise self.refuse("malformed")
                k = self.string()
@DUPKEY@                self.ws()
                if self.at() != _COLON:
                    raise self.refuse("malformed")
                self.pos += 1
                self.ws()
                out[k] = self.string()
                self.ws()
                if self.at() != _COMMA:
                    break
                self.pos += 1
        if self.at() != _RBRACE:
            raise self.refuse("malformed")
        self.pos += 1
        self.depth -= 1
        return out
`

const pyDecodeList = `

def _decode_list(r, elem):
    if r.at() != _LBRACK:
        raise r.refuse("wrong_type")
    r.enter()
    r.pos += 1
    out = []
    r.ws()
    if r.at() != _RBRACK:
        while True:
            r.ws()
            out.append(elem(r))
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

const pyDupKeyRefuse = `                if k in out:
                    raise self.refuse("duplicate_key")
`

const pySkipDupKey = `                    if k in seen:
                        raise self.refuse("duplicate_key")
                    seen.add(k)
`

const pyTimestamp = `

def _digits(s, i, n):
    return len(s) >= i + n and all("0" <= s[i + k] <= "9" for k in range(n))


def _date_part(s):
    return (len(s) >= 19 and _digits(s, 0, 4) and s[4] == "-" and _digits(s, 5, 2)
            and s[7] == "-" and _digits(s, 8, 2) and _digits(s, 11, 2) and s[13] == ":"
            and _digits(s, 14, 2) and s[16] == ":" and _digits(s, 17, 2))


def _lexical_timestamp(s):
    """[DEF-G1] rfc3339-wide: what a reader accepts. Any fraction of one to nine
    digits or none, either case of the separators, and a numeric offset."""
    if not _date_part(s) or s[10] not in "Tt":
        return False
    i = 19
    if i < len(s) and s[i] == ".":
        i += 1
        start = i
        while i < len(s) and "0" <= s[i] <= "9":
            i += 1
        if not 1 <= i - start <= 9:
            return False
    if i >= len(s):
        return False
    if s[i] in "Zz":
        return i + 1 == len(s)
    if s[i] not in "+-":
        return False
    return len(s) == i + 6 and _digits(s, i + 1, 2) and s[i + 3] == ":" and _digits(s, i + 4, 2)


def micros_timestamp(s):
    """[DEF-G2] rfc3339-micros: what a writer emits. Exactly six fractional
    digits, upper-case separators, UTC."""
    return len(s) == 27 and _normalized_timestamp(s) == s


def _read_timestamp(r):
    at = r.pos
    s = r.string()
    if not wide_timestamp(s):
        r.pos = at
        raise r.refuse("bad_timestamp")
    return s
`

func pyDecoder(b *strings.Builder, s *Definition) {
	for _, st := range s.Structs {
		pyStructDecoder(b, s, st)
	}
	if s.Document != "" {
		b.WriteString("\n\ndef decode(data):\n    r = _Reader(bytes(data))\n    r.ws()\n")
		fmt.Fprintf(b, "    v = _decode_%s(r)\n    r.ws()\n", lower(s.Document))
		b.WriteString("    if r.pos < len(r.buf):\n        raise r.refuse(\"trailing_bytes\")\n")
		if s.Vocab != nil {
			b.WriteString("    _derive(r, v)\n")
		}
		b.WriteString("    return v\n")
	}
	pyDerive(b, s)
}

func pyStructDecoder(b *strings.Builder, s *Definition, st Struct) {
	fmt.Fprintf(b, "\n\ndef _decode_%s(r):\n", lower(st.Name))
	b.WriteString("    if r.at() != _LBRACE:\n        raise r.refuse(\"wrong_type\")\n")
	fmt.Fprintf(b, "    r.enter()\n    r.pos += 1\n    v = %s()\n    seen = 0\n    r.ws()\n", st.Name)
	b.WriteString("    if r.at() != _RBRACE:\n        while True:\n            r.ws()\n")
	b.WriteString("            if r.at() != _QUOTE:\n                raise r.refuse(\"malformed\")\n")
	b.WriteString("            key = r.string()\n            r.ws()\n")
	b.WriteString("            if r.at() != _COLON:\n                raise r.refuse(\"malformed\")\n")
	b.WriteString("            r.pos += 1\n            r.ws()\n")
	for i, f := range st.Fields {
		kw := "elif"
		if i == 0 {
			kw = "if"
		}
		fmt.Fprintf(b, "            %s key == %q:\n", kw, f.Name)
		fmt.Fprintf(b, "                if seen & %d:\n                    raise r.refuse(\"duplicate_field\")\n", 1<<i)
		fmt.Fprintf(b, "                seen |= %d\n", 1<<i)
		fmt.Fprintf(b, "                v.%s = %s\n", f.Ident("python"), pyRead(s, f))
	}
	if len(st.Fields) == 0 {
		b.WriteString("            if False:\n                pass\n")
	}
	b.WriteString("            else:\n")
	if st.RefuseUnknown() {
		b.WriteString("                raise r.refuse(\"unknown_field\")\n")
	} else if st.PreservesUnknown() {
		if s.Encoding.RefuseDuplicateKeys() {
			b.WriteString("                if key in v.extras:\n                    raise r.refuse(\"duplicate_key\")\n")
		}
		b.WriteString("                v.extras[key] = r.raw_value()\n")
	} else {
		b.WriteString("                r.skip_value()\n")
	}
	b.WriteString("            r.ws()\n            if r.at() != _COMMA:\n                break\n            r.pos += 1\n")
	b.WriteString("    if r.at() != _RBRACE:\n        raise r.refuse(\"malformed\")\n    r.pos += 1\n    r.depth -= 1\n")
	if req := requiredMask(st); req != 0 {
		fmt.Fprintf(b, "    if seen & %d != %d:\n        raise r.refuse(\"missing_field\")\n", req, req)
	}
	emitEqualities(b, st, "python", false)
	if len(s.Services) > 0 && st.Name == s.Document && s.Vocab != nil {
		b.WriteString("    _derive(r, v)\n")
	}
	b.WriteString("    return v\n")
}

func pyRead(s *Definition, f Field) string {
	switch f.Type {
	case "string":
		if f.Grammar.Named() {
			return "_read_timestamp(r)"
		}
		return "r.string()"
	case "i32":
		return "r.integer(-2147483648, 2147483647)"
	case "i64":
		return "r.integer(-9223372036854775808, 9223372036854775807)"
	case "bool":
		return "r.boolean()"
	case "json":
		return "r.raw_value()"
	case "list<string>":
		return "r.str_list()"
	case "map<string,json>":
		return "r.raw_map()"
	case "list<json>":
		return "_raw_list(r)"
	case "map<string,string>":
		return "r.str_map()"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "_decode_list(r, _decode_" + lower(elem) + ")"
	}
	return "_decode_" + lower(f.Type) + "(r)"
}

func pyDerive(b *strings.Builder, s *Definition) {
	v := s.Vocab
	if v == nil {
		return
	}
	fmt.Fprintf(b, "\n\n%s_TERMS = [", upper(v.Name))
	for i, t := range v.Terms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("]\n")
	fmt.Fprintf(b, "%s_STRIP_CRITICAL = frozenset({", upper(v.Name))
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
	b.WriteString("})\n")
	b.WriteString("\n\ndef _derive(r, v):\n")
	fmt.Fprintf(b, "    present = set(v.%s)\n", v.Names)
	b.WriteString("    kept = []\n")
	fmt.Fprintf(b, "    for name in v.%s:\n", v.Critical)
	fmt.Fprintf(b, "        if name in %s_STRIP_CRITICAL:\n            continue\n", upper(v.Name))
	fmt.Fprintf(b, "        if name not in %s_TERMS:\n            raise r.refuse(\"unknown_critical\")\n", upper(v.Name))
	b.WriteString("        if name not in present:\n            raise r.refuse(\"not_a_subset\")\n")
	b.WriteString("        kept.append(name)\n")
	fmt.Fprintf(b, "    v.%s = kept\n", v.Critical)
	for _, t := range v.Terms {
		fmt.Fprintf(b, "    if %s != (%q in present):\n        raise r.refuse(\"content_mismatch\")\n", pyTest(s, t), t.Name)
	}
	if v.UsesMember() {
		b.WriteString(pyMember)
	}
}

func pyTest(s *Definition, t Term) string {
	if t.Always() {
		return "True"
	}
	e := "v"
	for _, f := range t.Path {
		e += "." + f.Ident("python")
	}
	switch {
	case t.Member != "":
		return fmt.Sprintf("member(%s, %q)", e, t.Member)
	case t.Membership():
		parts := make([]string, len(t.Is))
		for i, w := range t.Is {
			parts[i] = fmt.Sprintf("%q", w)
		}
		return fmt.Sprintf("(%s in (%s,))", e, strings.Join(parts, ", "))
	}
	return "bool(" + pyPresent(s, t.Last(), e) + ")"
}

const pyMember = `

def member(raw, name):
    """[DEF-A8] Whether an opaque value is an object naming this member with
    something other than null. The key is decoded, so two spellings of one name
    are one name; the value is neither decoded nor judged."""
    r = _Reader(raw.encode("utf-8") if isinstance(raw, str) else bytes(raw))
    try:
        r.ws()
        if r.at() != _LBRACE:
            return False
        r.pos += 1
        r.ws()
        while r.at() == _QUOTE:
            k = r.string()
            r.ws()
            r.pos += 1
            r.ws()
            if k == name:
                return r.at() != _LOWER_N
            r.skip_value()
            r.ws()
            if r.at() != _COMMA:
                return False
            r.pos += 1
            r.ws()
    except Refusal:
        return False
    return False
`

func genPy(s *Definition) string {
	if s.NoIPC {
		return genInterfaceOnly(s, "python")
	}
	s = serviceTypes(s)
	var b strings.Builder
	esc := pyEscMinimal
	if s.Encoding.EscapeNonASCII() {
		esc = pyEscASCII
	}
	prelude := pyCommon + esc
	if s.StringMapDocument() {
		prelude += pyStrMap
	}
	if s.HasRepeated() {
		prelude += pyEncList
	}
	b.WriteString(strings.NewReplacer("@INDENT@", strconv.Itoa(s.Encoding.Indent)).Replace(prelude))
	pyVocabulary(&b, s)
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "\n\nclass %s:\n    def __init__(self, **kw):\n", st.Name)
		if len(st.Fields) == 0 && !st.PreservesUnknown() {
			b.WriteString("        pass\n")
		}
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "        self.%s = kw.get(%q, %s)\n", f.Ident("python"), f.Name, pyDefault(s, f))
		}
		if st.PreservesUnknown() {
			b.WriteString("        self.extras = kw.get(\"extras\", {})\n")
		}
	}
	for _, st := range s.Structs {
		if !s.Envelope(st.Name) {
			pyEncoder(&b, s, st, false)
		}
	}
	tail := ""
	if s.Encoding.TrailingNewline() {
		tail = "    out += b\"\\n\"\n"
	}
	if s.Document != "" {
		fmt.Fprintf(&b, "\n\ndef encode(v):\n    out = bytearray()\n    enc_%s(out, v, 0)\n%s    return bytes(out)\n",
			lower(s.Document), tail)
	}
	dup, skipDup := "", ""
	if s.Encoding.RefuseDuplicateKeys() {
		dup, skipDup = pyDupKeyRefuse, pySkipDupKey
	}
	decode := pyDecodeCommon
	if s.HasStringMap() {
		decode += pyStrMapDecode
	}
	if s.HasRepeated() {
		decode += pyDecodeList
	}
	b.WriteString(strings.NewReplacer(
		"@DEPTH@", strconv.Itoa(s.Encoding.DepthLimit),
		"@DUPKEY@", dup,
		"@SKIPDUP@", skipDup,
	).Replace(decode))
	if s.Timestamps() {
		b.WriteString(pyTimestamp)
		b.WriteString(pyTimestampNormalize)
	}
	if s.PreservesUnknown() {
		b.WriteString(pyPreserve)
	}
	pyDecoder(&b, s)
	pyProtocol(&b, s)
	pyService(&b, s)
	return b.String()
}

func pyVocabulary(b *strings.Builder, s *Definition) {
	for _, en := range s.Enums {
		fmt.Fprintf(b, "\n\n%s_NAMES = [", upper(en.Name))
		for i, m := range en.Members {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%q", m.Name)
		}
		b.WriteString("]\n")
		fmt.Fprintf(b, "%s_UNKNOWN = %q\n", upper(en.Name), en.Ann["unknown"])
		for _, key := range en.MemberAnn() {
			fmt.Fprintf(b, "%s_%s = {\n", upper(en.Name), upper(key))
			for _, m := range en.Members {
				if v, ok := m.Ann[key]; ok {
					fmt.Fprintf(b, "    %q: %q,\n", m.Name, v)
				}
			}
			b.WriteString("}\n")
		}
	}
	for _, c := range s.Consts {
		fmt.Fprintf(b, "\n\n%s = [", upper(c.Name))
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
		b.WriteString("]\n")
	}
}

func pyEncoder(b *strings.Builder, s *Definition, st Struct, flat bool) {
	p := plan(st)
	if flat {
		fmt.Fprintf(b, "\n\ndef enc_wire_%s(out, v):\n", lower(st.Name))
	} else {
		fmt.Fprintf(b, "\n\ndef enc_%s(out, v, depth):\n", lower(st.Name))
	}
	emitEqualities(b, st, "python", true)
	b.WriteString("    out += b\"{\"\n")
	if p.flag {
		b.WriteString("    first = True\n")
	}
	for i, f := range st.Fields {
		e := "v." + f.Ident("python")
		ind := "    "
		if f.Omit != "never" {
			fmt.Fprintf(b, "    if %s:\n", pyPresent(s, f, e))
			ind = "        "
		}
		switch p.before[i] {
		case "always":
			fmt.Fprintf(b, "%sout += b\",\"\n", ind)
		case "flag":
			fmt.Fprintf(b, "%sif not first:\n%s    out += b\",\"\n", ind, ind)
		}
		if p.clears(i) {
			fmt.Fprintf(b, "%sfirst = False\n", ind)
		}
		if flat {
			fmt.Fprintf(b, "%sesc(out, %q)\n%sout += b\":\"\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, pyValueFlat(s, f, e))
			continue
		}
		fmt.Fprintf(b, "%sout += b\"\\n\"\n%spad(out, depth + 1)\n", ind, ind)
		fmt.Fprintf(b, "%sesc(out, %q)\n%sout += b\": \"\n", ind, f.Name, ind)
		fmt.Fprintf(b, "%s%s\n", ind, pyValue(s, f, e))
	}
	if st.PreservesUnknown() {
		fmt.Fprintf(b, "    first = _extra_fields(out, v.extras, [%s], depth, first)\n", quotedFieldNames(st))
	}
	if !flat {
		switch {
		case p.closeAlways:
			b.WriteString("    out += b\"\\n\"\n    pad(out, depth)\n")
		case len(st.Fields) > 0 || st.PreservesUnknown():
			b.WriteString("    if not first:\n        out += b\"\\n\"\n        pad(out, depth)\n")
		}
	}
	b.WriteString("    out += b\"}\"\n")
}

func pyDefault(s *Definition, f Field) string {
	if f.Omit == "absent" && s.IsStruct(f.Type) {
		return "None"
	}
	switch f.Type {
	case "string", "json":
		return `""`
	case "i32", "i64":
		return "0"
	case "bool":
		return "False"
	case "list<string>", "list<json>":
		return "[]"
	case "map<string,json>", "map<string,string>":
		return "{}"
	}
	if s.Repeated(f.Type) != "" {
		return "[]"
	}
	return f.Type + "()"
}

func pyPresent(s *Definition, f Field, e string) string {
	if f.Omit == "absent" && s.IsStruct(f.Type) {
		return e + " is not None"
	}
	switch f.Type {
	case "i32", "i64":
		return e + " != 0"
	}
	return e
}

func pyValue(s *Definition, f Field, e string) string {
	if f.Type == "json" && f.Ann["service_raw"] == "true" {
		return "out += " + e + ".encode(\"utf-8\") if isinstance(" + e + ", str) else " + e
	}

	if f.Grammar.Named() {
		return "esc(out, _write_timestamp(" + e + "))"
	}
	switch f.Type {
	case "string":
		return "esc(out, " + e + ")"
	case "i32", "i64":
		return "num(out, " + e + ")"
	case "bool":
		return `out += b"true" if ` + e + ` else b"false"`
	case "json":
		return "raw(out, " + e + ", depth + 1)"
	case "list<string>":
		return "strs(out, " + e + ", depth + 1)"
	case "map<string,json>":
		return "rawmap(out, " + e + ", depth + 1)"
	case "map<string,string>":
		return "strmap(out, " + e + ", depth + 1)"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "enc_list(out, " + e + ", depth + 1, enc_" + lower(elem) + ")"
	}
	return "enc_" + lower(f.Type) + "(out, " + e + ", depth + 1)"
}

func lower(n string) string { return strings.ToLower(n) }
func upper(n string) string { return strings.ToUpper(n) }

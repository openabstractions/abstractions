package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The Python backend emits two modules. _codec.py holds every record,
// vocabulary, codec and client; __init__.py re-exports the public names, so
// `from abstraction.rights.api import AuthorizationClient` is the whole import
// an application writes. Codec helpers, argument and result envelopes and
// dispatch tables carry a leading underscore and never leave _codec.py.

const pyEscMinimal = `
def _esc(out, s):
    out += b'"'
    for c in s.encode("utf-8"):
        _esc_byte(out, c)
    out += b'"'
`

const pyEscASCII = `
def _esc(out, s):
    out += b'"'
    for ch in s:
        cp = ord(ch)
        if cp < 0x80:
            _esc_byte(out, cp)
        elif cp > 0xFFFF:
            cp -= 0x10000
            _unit(out, 0xD800 + (cp >> 10))
            _unit(out, 0xDC00 + (cp & 0x3FF))
        else:
            _unit(out, cp)
    out += b'"'


def _unit(out, u):
    out += b"\\u" + bytes([_HEX[(u >> 12) & 0xF], _HEX[(u >> 8) & 0xF],
                           _HEX[(u >> 4) & 0xF], _HEX[u & 0xF]])
`

const pyCommon = `from __future__ import annotations

import dataclasses
import enum

_HEX = b"0123456789abcdef"
_WS = (0x20, 0x09, 0x0A, 0x0D)
_SHORT = {0x22: b'\\"', 0x5C: b"\\\\", 0x08: b"\\b", 0x0C: b"\\f",
          0x0A: b"\\n", 0x0D: b"\\r", 0x09: b"\\t"}


def _esc_byte(out, c):
    short = _SHORT.get(c)
    if short is not None:
        out += short
    elif c < 0x20:
        out += b"\\u00" + bytes([_HEX[c >> 4], _HEX[c & 0xF]])
    else:
        out.append(c)


def _num(out, n):
    out += str(int(n)).encode("ascii")


def _pad(out, depth):
    out += b" " * (depth * @INDENT@)


def _strs(out, v, depth):
    if not v:
        out += b"[]"
        return
    out += b"[\n"
    for i, s in enumerate(v):
        _pad(out, depth + 1)
        _esc(out, s)
        if i + 1 < len(v):
            out += b","
        out += b"\n"
    _pad(out, depth)
    out += b"]"


def _raw(out, s, depth):
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


def _rawmap(out, m, depth):
    keys = sorted(m, key=lambda k: k.encode("utf-8"))
    if not keys:
        out += b"{}"
        return
    out += b"{\n"
    for i, k in enumerate(keys):
        _pad(out, depth + 1)
        _esc(out, k)
        out += b": "
        _raw(out, m[k], depth + 1)
        if i + 1 < len(keys):
            out += b","
        out += b"\n"
    _pad(out, depth)
    out += b"}"
`

const pyStrMap = `

def _strmap(out, m, depth):
    keys = sorted(m, key=lambda k: k.encode("utf-8"))
    if not keys:
        out += b"{}"
        return
    out += b"{\n"
    for i, k in enumerate(keys):
        _pad(out, depth + 1)
        _esc(out, k)
        out += b": "
        _esc(out, m[k])
        if i + 1 < len(keys):
            out += b","
        out += b"\n"
    _pad(out, depth)
    out += b"}"
`

const pyEncList = `

def _write_list(out, v, depth, enc):
    if not v:
        out += b"[]"
        return
    out += b"[\n"
    for i, x in enumerate(v):
        _pad(out, depth + 1)
        enc(out, x, depth + 1)
        if i + 1 < len(v):
            out += b","
        out += b"\n"
    _pad(out, depth)
    out += b"]"
`

// pyEnumBase is the one base every generated vocabulary derives from. Members
// are strings, so a member compares equal to its wire word and encodes as it.
const pyEnumBase = `

try:
    from enum import StrEnum as _StrEnum
except ImportError:  # Python 3.10
    class _StrEnum(str, enum.Enum):
        def __str__(self):
            return self.value


def _closed_enum(cls, value):
    if value is not None and not isinstance(value, str):
        raise Refusal("wrong_type", 0)
    try:
        cls(value)
    except ValueError:
        raise Refusal("bad_enum", 0) from None


def _open_enum(cls, value):
    try:
        return cls(value)
    except ValueError:
        return value
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
    """Bytes the contract refuses: word names the rule, offset the byte."""

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

def _read_list(r, elem):
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


def _micros_timestamp(s):
    """[DEF-G2] rfc3339-micros: what a writer emits. Exactly six fractional
    digits, upper-case separators, UTC."""
    return len(s) == 27 and _normalized_timestamp(s) == s


def _read_timestamp(r):
    at = r.pos
    s = r.string()
    if not _wide_timestamp(s):
        r.pos = at
        raise r.refuse("bad_timestamp")
    return s
`

// pyInternalRecord reports whether a struct is a carrier the backend made up:
// a service's argument or result envelope, the service frame, or the alias of
// an included record. None of them is part of the Python API.
func pyInternalRecord(s *Definition, name string) bool {
	switch name {
	case "OAServiceFrame", "OAServiceReply", "OAServiceError":
		return true
	}
	if _, ok := s.Foreign[name]; ok {
		return true
	}
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			if name == argsName(svc, m) || name == resultName(svc, m) {
				return true
			}
		}
	}
	return false
}

// pyClass is the Python class a contract struct becomes.
func pyClass(s *Definition, name string) string {
	if pyInternalRecord(s, name) {
		return "_" + strings.TrimPrefix(name, "OA")
	}
	return pascalCase(name)
}

func pyStem(name string) string   { return snakeCase(name) }
func pyWriter(name string) string { return "_write_" + pyStem(name) }
func pyReader(name string) string { return "_read_" + pyStem(name) }
func pyDependency(alias string) string {
	return "_dependency_" + snakeCase(alias)
}

// pyTypeName is the name a type hint uses for a record, including a record an
// included definition owns.
func pyTypeName(s *Definition, name string) string {
	if imp, ok := s.Foreign[name]; ok {
		return pyDependency(imp.Alias) + "." + pascalCase(imp.Name)
	}
	return pyClass(s, name)
}

func pyEnumClosed(en *Enum) bool { return en.Ann["unknown"] != "grant" }

// pyHint is a field's annotation. Absence is None; an open vocabulary keeps a
// word no member names as a plain str.
func pyHint(s *Definition, f Field) string {
	if en := f.EnumType; en != nil {
		if f.EnumList {
			if pyEnumClosed(en) {
				return "list[" + pascalCase(en.Name) + "]"
			}
			return "list[" + pascalCase(en.Name) + " | str]"
		}
		switch {
		case f.Omit == "absent" || pyEnumClosed(en):
			return pascalCase(en.Name) + " | None"
		default:
			return pascalCase(en.Name) + " | str"
		}
	}
	optional := func(t string) string {
		if f.Omit == "absent" {
			return t + " | None"
		}
		return t
	}
	switch f.Type {
	case "void":
		return "None"
	case "binary":
		return optional("bytes")
	case "string":
		return "str"
	case "i32", "i64":
		return "int"
	case "bool":
		return "bool"
	case "json":
		return "bytes | str"
	case "list<string>":
		return "list[str]"
	case "list<json>":
		return "list[bytes | str]"
	case "map<string,json>":
		return "dict[str, bytes | str]"
	case "map<string,string>":
		return "dict[str, str]"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "list[" + pyTypeName(s, elem) + "]"
	}
	if s.IsStruct(f.Type) {
		return optional(pyTypeName(s, f.Type))
	}
	return f.Type
}

// pyKind is the value checker's name for a field's type. A vocabulary field is
// checked as a string and judged by the codec against its enumeration.
func pyKind(f Field) string {
	if f.EnumType != nil && f.EnumList {
		return "list<enum>"
	}
	if f.EnumType != nil && !f.EnumList {
		return "enum"
	}
	return f.Type
}

// pyDocstring renders a documentation annotation as a docstring at an indent.
func pyDocstring(doc, indent string) string {
	text := strings.Join(strings.Fields(doc), " ")
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, `\`, `\\`)
	text = strings.ReplaceAll(text, `"""`, `\"\"\"`)
	if strings.HasSuffix(text, `"`) {
		text += " "
	}
	lines := structDoc(Struct{Ann: map[string]string{"doc": text}}, indent)
	lines = strings.TrimSuffix(lines, "\n")
	return indent + `"""` + strings.TrimPrefix(lines, indent) + `"""` + "\n"
}

func pyDecoder(b *strings.Builder, s *Definition) {
	for _, st := range s.Structs {
		pyStructDecoder(b, s, st)
	}
	if s.Document != "" {
		b.WriteString("\n\ndef decode(data):\n    r = _Reader(bytes(data))\n    r.ws()\n")
		fmt.Fprintf(b, "    v = %s(r)\n    r.ws()\n", pyReader(s.Document))
		b.WriteString("    if r.pos < len(r.buf):\n        raise r.refuse(\"trailing_bytes\")\n")
		if s.Vocab != nil {
			b.WriteString("    _derive(r, v)\n")
		}
		b.WriteString("    return v\n")
	}
	pyDerive(b, s)
}

func pyStructDecoder(b *strings.Builder, s *Definition, st Struct) {
	fmt.Fprintf(b, "\n\ndef %s(r):\n", pyReader(st.Name))
	b.WriteString("    if r.at() != _LBRACE:\n        raise r.refuse(\"wrong_type\")\n")
	fmt.Fprintf(b, "    r.enter()\n    r.pos += 1\n    v = %s()\n    seen = 0\n    r.ws()\n", pyClass(s, st.Name))
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
		fmt.Fprintf(b, "                v.%s = %s\n", f.Ident("python"), pyReadValue(s, f))
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
	pyEnumChecks(b, st, false)
	if len(s.Services) > 0 && st.Name == s.Document && s.Vocab != nil {
		b.WriteString("    _derive(r, v)\n")
	}
	b.WriteString("    return v\n")
}

// pyEnumChecks judges vocabulary fields. The encoder refuses a word a closed
// vocabulary does not name, and anything that is not a string; the decoder turns
// the wire word into its member, or keeps the word where the vocabulary is open.
func pyEnumChecks(b *strings.Builder, st Struct, encode bool) {
	for i, f := range st.Fields {
		en := f.EnumType
		if en == nil {
			continue
		}
		e := "v." + f.Ident("python")
		cls := pascalCase(en.Name)
		if f.EnumList {
			if encode {
				fmt.Fprintf(b, "    for item in %s:\n", e)
				if pyEnumClosed(en) {
					fmt.Fprintf(b, "        _closed_enum(%s, item)\n", cls)
				} else {
					b.WriteString("        if not isinstance(item, str):\n            raise Refusal(\"wrong_type\", 0)\n")
				}
			} else if pyEnumClosed(en) {
				fmt.Fprintf(b, "    try:\n        %s = [%s(item) for item in %s]\n    except ValueError:\n        raise r.refuse(\"bad_enum\") from None\n", e, cls, e)
			} else {
				fmt.Fprintf(b, "    %s = [_open_enum(%s, item) for item in %s]\n", e, cls, e)
			}
			continue
		}
		guard := ""
		switch {
		case f.Omit == "absent":
			guard = e + " is not None"
		case f.Omit == "zero" && encode:
			guard = e
		case f.Omit == "zero":
			guard = fmt.Sprintf("seen & %d", 1<<i)
		}
		indent := "    "
		if guard != "" {
			fmt.Fprintf(b, "    if %s:\n", guard)
			indent += "    "
		}
		switch {
		case encode && pyEnumClosed(en):
			fmt.Fprintf(b, "%s_closed_enum(%s, %s)\n", indent, cls, e)
		case encode:
			fmt.Fprintf(b, "%sif not isinstance(%s, str):\n%s    raise Refusal(\"wrong_type\", 0)\n", indent, e, indent)
		case pyEnumClosed(en):
			fmt.Fprintf(b, "%stry:\n%s    %s = %s(%s)\n%sexcept ValueError:\n%s    raise r.refuse(\"bad_enum\") from None\n", indent, indent, e, cls, e, indent, indent)
		default:
			fmt.Fprintf(b, "%s%s = _open_enum(%s, %s)\n", indent, e, cls, e)
		}
	}
}

func pyReadValue(s *Definition, f Field) string {
	switch f.Type {
	case "binary":
		return "_decode_binary(r.string())"
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
		return "_read_list(r, " + pyReader(elem) + ")"
	}
	return pyReader(f.Type) + "(r)"
}

// vocabIdent is the language identifier of a document field a vocabulary names.
func vocabIdent(s *Definition, name, lang string) string {
	if v := s.Vocab; v != nil {
		if st := s.Struct(v.Of); st != nil {
			for _, f := range st.Fields {
				if f.Name == name {
					return f.Ident(lang)
				}
			}
		}
	}
	return Field{Name: name}.Ident(lang)
}

func pyDerive(b *strings.Builder, s *Definition) {
	v := s.Vocab
	if v == nil {
		return
	}
	terms, strip := upperSnake(v.Name)+"_TERMS", upperSnake(v.Name)+"_STRIP_CRITICAL"
	fmt.Fprintf(b, "\n\n%s = [", terms)
	for i, t := range v.Terms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("]\n")
	fmt.Fprintf(b, "%s = frozenset({", strip)
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
	names, critical := vocabIdent(s, v.Names, "python"), vocabIdent(s, v.Critical, "python")
	b.WriteString("\n\ndef _derive(r, v):\n")
	fmt.Fprintf(b, "    present = set(v.%s)\n", names)
	b.WriteString("    kept = []\n")
	fmt.Fprintf(b, "    for name in v.%s:\n", critical)
	fmt.Fprintf(b, "        if name in %s:\n            continue\n", strip)
	fmt.Fprintf(b, "        if name not in %s:\n            raise r.refuse(\"unknown_critical\")\n", terms)
	b.WriteString("        if name not in present:\n            raise r.refuse(\"not_a_subset\")\n")
	b.WriteString("        kept.append(name)\n")
	fmt.Fprintf(b, "    v.%s = kept\n", critical)
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
		return fmt.Sprintf("_member(%s, %q)", e, t.Member)
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

def _member(raw, name):
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

// pyRecordOrder emits a record after every record its field defaults
// construct, so a default factory names a class that already exists.
func pyRecordOrder(s *Definition) []Struct {
	done := map[string]bool{}
	var out []Struct
	var visit func(st Struct)
	visit = func(st Struct) {
		if done[st.Name] {
			return
		}
		done[st.Name] = true
		for _, f := range st.Fields {
			if f.Omit != "absent" && f.EnumType == nil {
				if dep := s.byName[f.Type]; dep != nil {
					visit(*dep)
				}
			}
		}
		out = append(out, st)
	}
	for _, st := range s.Structs {
		visit(st)
	}
	return out
}

func pyRecord(b *strings.Builder, s *Definition, st Struct) {
	b.WriteString("\n\n@dataclasses.dataclass(kw_only=True)\n")
	fmt.Fprintf(b, "class %s:\n", pyClass(s, st.Name))
	doc := pyDocstring(st.Ann["doc"], "    ")
	b.WriteString(doc)
	if len(st.Fields) == 0 && !st.PreservesUnknown() && doc == "" {
		b.WriteString("    pass\n")
	}
	if doc != "" && (len(st.Fields) > 0 || st.PreservesUnknown()) {
		b.WriteString("\n")
	}
	for _, f := range st.Fields {
		fmt.Fprintf(b, "    %s: %s = %s\n", f.Ident("python"), pyHint(s, f), pyDefault(s, f))
	}
	if st.PreservesUnknown() {
		b.WriteString("    extras: dict[str, bytes | str] = dataclasses.field(default_factory=dict)\n")
	}
}

func pyVocabularyEnums(b *strings.Builder, s *Definition) {
	if len(s.Enums) == 0 {
		return
	}
	b.WriteString(pyEnumBase)
	for _, en := range s.Enums {
		fmt.Fprintf(b, "\n\nclass %s(_StrEnum):\n", pascalCase(en.Name))
		if len(en.Members) == 0 {
			b.WriteString("    pass\n")
		}
		for _, m := range en.Members {
			name := upperSnake(m.Name)
			if n, ok := m.Ann["python.name"]; ok {
				name = n
			}
			fmt.Fprintf(b, "    %s = %q\n", name, m.WireName())
		}
		for _, key := range en.MemberAnn() {
			if strings.HasSuffix(key, ".name") {
				continue
			}
			fmt.Fprintf(b, "\n\n%s_%s = {\n", upperSnake(en.Name), upperSnake(key))
			for _, m := range en.Members {
				if v, ok := m.Ann[key]; ok {
					fmt.Fprintf(b, "    %s.%s: %q,\n", pascalCase(en.Name), upperSnake(m.Name), v)
				}
			}
			b.WriteString("}\n")
		}
	}
}

func genPy(s *Definition) string {
	s = enumCarriers(s)
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
	b.WriteString(importPrelude(s, "python"))
	pyVocabularyEnums(&b, s)
	pyVocabulary(&b, s)
	for _, st := range pyRecordOrder(s) {
		pyRecord(&b, s, st)
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
		if len(s.Imports) > 0 {
			fmt.Fprintf(&b, "\n\ndef encode(v):\n    return encode_%s(v)\n", pyStem(s.Document))
		} else {
			fmt.Fprintf(&b, "\n\ndef encode(v):\n    out = bytearray()\n    %s(out, v, 0)\n%s    return bytes(out)\n",
				pyWriter(s.Document), tail)
		}
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
	if hasBinary(s) {
		b.WriteString(pyBinary)
	}
	pyService(&b, s)
	return b.String()
}

func pyVocabulary(b *strings.Builder, s *Definition) {
	for _, c := range s.Consts {
		fmt.Fprintf(b, "\n\n%s = [", upperSnake(c.Name))
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
		fmt.Fprintf(b, "\n\ndef _write_flat_%s(out, v):\n", pyStem(st.Name))
	} else {
		fmt.Fprintf(b, "\n\ndef %s(out, v, depth):\n", pyWriter(st.Name))
	}
	emitEqualities(b, st, "python", true)
	pyEnumChecks(b, st, true)
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
			fmt.Fprintf(b, "%s_esc(out, %q)\n%sout += b\":\"\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, pyValueFlat(s, f, e))
			continue
		}
		fmt.Fprintf(b, "%sout += b\"\\n\"\n%s_pad(out, depth + 1)\n", ind, ind)
		fmt.Fprintf(b, "%s_esc(out, %q)\n%sout += b\": \"\n", ind, f.Name, ind)
		fmt.Fprintf(b, "%s%s\n", ind, pyValue(s, f, e))
	}
	if st.PreservesUnknown() {
		fmt.Fprintf(b, "    first = _extra_fields(out, v.extras, [%s], depth, first)\n", quotedFieldNames(st))
	}
	if !flat {
		switch {
		case p.closeAlways:
			b.WriteString("    out += b\"\\n\"\n    _pad(out, depth)\n")
		case len(st.Fields) > 0 || st.PreservesUnknown():
			b.WriteString("    if not first:\n        out += b\"\\n\"\n        _pad(out, depth)\n")
		}
	}
	b.WriteString("    out += b\"}\"\n")
}

func pyDefault(s *Definition, f Field) string {
	if en := f.EnumType; en != nil {
		if f.EnumList {
			return "dataclasses.field(default_factory=list)"
		}
		if f.Omit == "absent" || pyEnumClosed(en) {
			return "None"
		}
		return `""`
	}
	if f.Omit == "absent" && (s.IsStruct(f.Type) || f.Type == "binary") {
		return "None"
	}
	switch f.Type {
	case "binary":
		return "b\"\""
	case "string", "json":
		return `""`
	case "i32", "i64":
		return "0"
	case "bool":
		return "False"
	case "list<string>", "list<json>":
		return "dataclasses.field(default_factory=list)"
	case "map<string,json>", "map<string,string>":
		return "dataclasses.field(default_factory=dict)"
	}
	if s.Repeated(f.Type) != "" {
		return "dataclasses.field(default_factory=list)"
	}
	return "dataclasses.field(default_factory=" + pyClass(s, f.Type) + ")"
}

func pyPresent(s *Definition, f Field, e string) string {
	if f.Omit == "absent" && (s.IsStruct(f.Type) || f.Type == "binary" || enumAbsent(f)) {
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
		return "_esc(out, _write_timestamp(" + e + "))"
	}
	switch f.Type {
	case "binary":
		return "_esc(out, _encode_binary(" + e + "))"
	case "string":
		return "_esc(out, " + e + ")"
	case "i32", "i64":
		return "_num(out, " + e + ")"
	case "bool":
		return `out += b"true" if ` + e + ` else b"false"`
	case "json":
		return "_raw(out, " + e + ", depth + 1)"
	case "list<string>":
		return "_strs(out, " + e + ", depth + 1)"
	case "map<string,json>":
		return "_rawmap(out, " + e + ", depth + 1)"
	case "map<string,string>":
		return "_strmap(out, " + e + ", depth + 1)"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "_write_list(out, " + e + ", depth + 1, " + pyWriter(elem) + ")"
	}
	return pyWriter(f.Type) + "(out, " + e + ", depth + 1)"
}

func lower(n string) string { return strings.ToLower(n) }
func upper(n string) string { return strings.ToUpper(n) }

// pyPublicNames lists what _codec.py offers an application: every top-level
// class, function and constant it defines without a leading underscore.
func pyPublicNames(body string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		name := ""
		switch {
		case strings.HasPrefix(line, "class "), strings.HasPrefix(line, "def "):
			rest := line[strings.Index(line, " ")+1:]
			end := strings.IndexAny(rest, "(:")
			if end > 0 {
				name = rest[:end]
			}
		default:
			if i := strings.Index(line, " = "); i > 0 && !strings.ContainsAny(line[:i], " \t.,[(") {
				name = line[:i]
			}
		}
		if name == "" || strings.HasPrefix(name, "_") || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// genPyPublic is __init__.py: the package an application imports.
func genPyPublic(s *Definition, private string) string {
	names := pyPublicNames(private)
	var b strings.Builder
	ns := namespaceFor(s.Namespaces, "python")
	if ns == "" {
		b.WriteString("\"\"\"Generated contract types, vocabularies and service clients.\"\"\"\n")
	} else {
		fmt.Fprintf(&b, "\"\"\"%s: generated contract types, vocabularies and service clients.\"\"\"\n", ns)
	}
	if len(names) == 0 {
		b.WriteString("\n__all__ = []\n")
		return b.String()
	}
	b.WriteString("\nfrom ._codec import (\n")
	for _, n := range names {
		fmt.Fprintf(&b, "    %s,\n", n)
	}
	b.WriteString(")\n\n__all__ = [\n")
	for _, n := range names {
		fmt.Fprintf(&b, "    %q,\n", n)
	}
	b.WriteString("]\n")
	b.WriteString(`
# Report every class and function as a member of this package, so help(),
# tracebacks and pickles name the import an application writes.
for _name in __all__:
    _value = globals()[_name]
    if isinstance(_value, type) or callable(_value):
        _value.__module__ = __name__
del _name, _value
`)
	return b.String()
}

package main

import (
	"fmt"
	"strconv"
	"strings"
)

const rsEscMinimal = `
pub fn esc(out: &mut Vec<u8>, s: &str) {
    out.push(b'"');
    for c in s.as_bytes() {
        esc_byte(out, *c);
    }
    out.push(b'"');
}
`

const rsEscASCII = `
fn unit(out: &mut Vec<u8>, u: u32) {
    out.extend_from_slice(b"\\u");
    for shift in [12, 8, 4, 0] {
        out.push(HEX[((u >> shift) & 0xF) as usize]);
    }
}

pub fn esc(out: &mut Vec<u8>, s: &str) {
    out.push(b'"');
    for ch in s.chars() {
        let cp = ch as u32;
        if cp < 0x80 {
            esc_byte(out, cp as u8);
        } else if cp > 0xFFFF {
            let c = cp - 0x10000;
            unit(out, 0xD800 + (c >> 10));
            unit(out, 0xDC00 + (c & 0x3FF));
        } else {
            unit(out, cp);
        }
    }
    out.push(b'"');
}
`

const rsCommon = `#![allow(dead_code)]

use std::collections::BTreeMap;

// An opaque payload is bytes the contract forbids us to reinterpret, and String
// would refuse to hold one that is not UTF-8 rather than carry it back out.
pub type Raw = Vec<u8>;

const HEX: &[u8; 16] = b"0123456789abcdef";

fn esc_byte(out: &mut Vec<u8>, c: u8) {
    match c {
        b'"' => out.extend_from_slice(b"\\\""),
        b'\\' => out.extend_from_slice(b"\\\\"),
        0x08 => out.extend_from_slice(b"\\b"),
        0x0c => out.extend_from_slice(b"\\f"),
        b'\n' => out.extend_from_slice(b"\\n"),
        b'\r' => out.extend_from_slice(b"\\r"),
        b'\t' => out.extend_from_slice(b"\\t"),
        _ if c < 0x20 => {
            out.extend_from_slice(b"\\u00");
            out.push(HEX[(c >> 4) as usize]);
            out.push(HEX[(c & 0x0F) as usize]);
        }
        _ => out.push(c),
    }
}

pub fn num(out: &mut Vec<u8>, n: i64) {
    out.extend_from_slice(n.to_string().as_bytes());
}

pub fn pad(out: &mut Vec<u8>, depth: i32) {
    for _ in 0..depth * @INDENT@ {
        out.push(b' ');
    }
}

pub fn strs(out: &mut Vec<u8>, v: &[String], depth: i32) {
    if v.is_empty() {
        out.extend_from_slice(b"[]");
        return;
    }
    out.extend_from_slice(b"[\n");
    for (i, s) in v.iter().enumerate() {
        pad(out, depth + 1);
        esc(out, s);
        if i + 1 < v.len() {
            out.push(b',');
        }
        out.push(b'\n');
    }
    pad(out, depth);
    out.push(b']');
}

fn ws(c: u8) -> bool {
    c == b' ' || c == b'\t' || c == b'\n' || c == b'\r'
}

pub fn raw(out: &mut Vec<u8>, b: &[u8], depth: i32) {
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

// A BTreeMap<String, _> walks its keys in String's Ord, which compares the
// UTF-8 bytes — the order the definition declares. Nothing sorts here.
pub fn rawmap(out: &mut Vec<u8>, m: &BTreeMap<String, Raw>, depth: i32) {
    if m.is_empty() {
        out.extend_from_slice(b"{}");
        return;
    }
    out.extend_from_slice(b"{\n");
    for (i, (k, v)) in m.iter().enumerate() {
        pad(out, depth + 1);
        esc(out, k);
        out.extend_from_slice(b": ");
        raw(out, v, depth + 1);
        if i + 1 < m.len() {
            out.push(b',');
        }
        out.push(b'\n');
    }
    pad(out, depth);
    out.push(b'}');
}
`

const rsStrMap = `
pub fn strmap(out: &mut Vec<u8>, m: &BTreeMap<String, String>, depth: i32) {
    if m.is_empty() {
        out.extend_from_slice(b"{}");
        return;
    }
    out.extend_from_slice(b"{\n");
    for (i, (k, v)) in m.iter().enumerate() {
        pad(out, depth + 1);
        esc(out, k);
        out.extend_from_slice(b": ");
        esc(out, v);
        if i + 1 < m.len() {
            out.push(b',');
        }
        out.push(b'\n');
    }
    pad(out, depth);
    out.push(b'}');
}
`

const rsEncList = `
pub fn enc_list<T>(out: &mut Vec<u8>, v: &[T], depth: i32, enc: fn(&mut Vec<u8>, &T, i32)) {
    if v.is_empty() {
        out.extend_from_slice(b"[]");
        return;
    }
    out.extend_from_slice(b"[\n");
    for (i, x) in v.iter().enumerate() {
        pad(out, depth + 1);
        enc(out, x, depth + 1);
        if i + 1 < v.len() {
            out.push(b',');
        }
        out.push(b'\n');
    }
    pad(out, depth);
    out.push(b']');
}
`

const rsDecodeCommon = `
const DEPTH_LIMIT: i32 = @DEPTH@;

#[derive(Debug)]
pub struct Refusal {
    pub word: &'static str,
    pub offset: usize,
}

impl std::fmt::Display for Refusal {
    fn fmt(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
        write!(f, "refused: {} at byte {}", self.word, self.offset)
    }
}

impl std::error::Error for Refusal {}

fn append_rune(out: &mut Vec<u8>, cp: u32) {
    if cp < 0x80 {
        out.push(cp as u8);
    } else if cp < 0x800 {
        out.push(0xC0 | (cp >> 6) as u8);
        out.push(0x80 | (cp & 0x3F) as u8);
    } else if cp < 0x10000 {
        out.push(0xE0 | (cp >> 12) as u8);
        out.push(0x80 | ((cp >> 6) & 0x3F) as u8);
        out.push(0x80 | (cp & 0x3F) as u8);
    } else {
        out.push(0xF0 | (cp >> 18) as u8);
        out.push(0x80 | ((cp >> 12) & 0x3F) as u8);
        out.push(0x80 | ((cp >> 6) & 0x3F) as u8);
        out.push(0x80 | (cp & 0x3F) as u8);
    }
}

struct Reader<'a> {
    buf: &'a [u8],
    pos: usize,
    depth: i32,
}

impl<'a> Reader<'a> {
    fn refuse<T>(&self, word: &'static str) -> Result<T, Refusal> {
        Err(Refusal { word, offset: self.pos })
    }

    fn at(&self) -> u8 {
        if self.pos < self.buf.len() { self.buf[self.pos] } else { 0 }
    }

    fn skip_ws(&mut self) {
        while self.pos < self.buf.len() && ws(self.buf[self.pos]) {
            self.pos += 1;
        }
    }

    fn enter(&mut self) -> Result<(), Refusal> {
        self.depth += 1;
        if self.depth > DEPTH_LIMIT {
            return self.refuse("depth_exceeded");
        }
        Ok(())
    }

    fn string(&mut self) -> Result<String, Refusal> {
        if self.at() != b'"' {
            return self.refuse("wrong_type");
        }
        self.pos += 1;
        let mut out: Vec<u8> = Vec::new();
        loop {
            if self.pos >= self.buf.len() {
                return self.refuse("malformed");
            }
            let c = self.buf[self.pos];
            if c == b'"' {
                self.pos += 1;
                return match String::from_utf8(out) {
                    Ok(s) => Ok(s),
                    Err(_) => self.refuse("bad_string"),
                };
            }
            if c < 0x20 {
                return self.refuse("bad_string");
            }
            if c == b'\\' {
                self.escape(&mut out)?;
            } else {
                out.push(c);
                self.pos += 1;
            }
        }
    }

    fn escape(&mut self, out: &mut Vec<u8>) -> Result<(), Refusal> {
        self.pos += 1;
        if self.pos >= self.buf.len() {
            return self.refuse("malformed");
        }
        let c = self.buf[self.pos];
        self.pos += 1;
        match c {
            b'"' | b'\\' | b'/' => out.push(c),
            b'b' => out.push(0x08),
            b'f' => out.push(0x0c),
            b'n' => out.push(b'\n'),
            b'r' => out.push(b'\r'),
            b't' => out.push(b'\t'),
            b'u' => {
                let mut u = self.hex4()?;
                if (0xDC00..=0xDFFF).contains(&u) {
                    return self.refuse("bad_string");
                }
                if (0xD800..=0xDBFF).contains(&u) {
                    if self.pos + 1 >= self.buf.len()
                        || self.buf[self.pos] != b'\\'
                        || self.buf[self.pos + 1] != b'u'
                    {
                        return self.refuse("bad_string");
                    }
                    self.pos += 2;
                    let low = self.hex4()?;
                    if !(0xDC00..=0xDFFF).contains(&low) {
                        return self.refuse("bad_string");
                    }
                    u = 0x10000 + ((u - 0xD800) << 10) + (low - 0xDC00);
                }
                append_rune(out, u);
            }
            _ => return self.refuse("bad_string"),
        }
        Ok(())
    }

    fn hex4(&mut self) -> Result<u32, Refusal> {
        if self.pos + 4 > self.buf.len() {
            return self.refuse("bad_string");
        }
        let mut u: u32 = 0;
        for i in 0..4 {
            let c = self.buf[self.pos + i];
            let d = match c {
                b'0'..=b'9' => c - b'0',
                b'a'..=b'f' => c - b'a' + 10,
                b'A'..=b'F' => c - b'A' + 10,
                _ => return self.refuse("bad_string"),
            };
            u = u << 4 | d as u32;
        }
        self.pos += 4;
        Ok(u)
    }

    fn integer(&mut self, low: i64, high: i64) -> Result<i64, Refusal> {
        let c = self.at();
        if c != b'-' && !c.is_ascii_digit() {
            return self.refuse("wrong_type");
        }
        let negative = c == b'-';
        if negative {
            self.pos += 1;
        }
        if !self.at().is_ascii_digit() {
            return self.refuse("malformed");
        }
        let mut u: u64 = 0;
        if self.at() == b'0' {
            self.pos += 1;
        } else {
            while self.at().is_ascii_digit() {
                if u > 922337203685477580 {
                    return self.refuse("number_spelling");
                }
                u = u * 10 + (self.at() - b'0') as u64;
                self.pos += 1;
            }
        }
        let after = self.at();
        if after == b'.' || after == b'e' || after == b'E' || after.is_ascii_digit() {
            return self.refuse("number_spelling");
        }
        if negative && u == 0 {
            return self.refuse("number_spelling");
        }
        let n = if negative {
            if u > 1u64 << 63 {
                return self.refuse("number_spelling");
            }
            if u == 1u64 << 63 { i64::MIN } else { -(u as i64) }
        } else {
            if u > i64::MAX as u64 {
                return self.refuse("number_spelling");
            }
            u as i64
        };
        if n < low || n > high {
            return self.refuse("number_spelling");
        }
        Ok(n)
    }

    fn boolean(&mut self) -> Result<bool, Refusal> {
        match self.at() {
            b't' => {
                self.literal(b"true")?;
                Ok(true)
            }
            b'f' => {
                self.literal(b"false")?;
                Ok(false)
            }
            _ => self.refuse("wrong_type"),
        }
    }

    fn literal(&mut self, word: &[u8]) -> Result<(), Refusal> {
        if self.pos + word.len() > self.buf.len() || &self.buf[self.pos..self.pos + word.len()] != word {
            return self.refuse("malformed");
        }
        self.pos += word.len();
        Ok(())
    }

    fn str_list(&mut self) -> Result<Vec<String>, Refusal> {
        if self.at() != b'[' {
            return self.refuse("wrong_type");
        }
        self.enter()?;
        self.pos += 1;
        let mut out = Vec::new();
        self.skip_ws();
        if self.at() != b']' {
            loop {
                self.skip_ws();
                out.push(self.string()?);
                self.skip_ws();
                if self.at() != b',' {
                    break;
                }
                self.pos += 1;
            }
        }
        if self.at() != b']' {
            return self.refuse("malformed");
        }
        self.pos += 1;
        self.depth -= 1;
        Ok(out)
    }

    fn raw_map(&mut self) -> Result<BTreeMap<String, Raw>, Refusal> {
        if self.at() != b'{' {
            return self.refuse("wrong_type");
        }
        self.enter()?;
        self.pos += 1;
        let mut out = BTreeMap::new();
        self.skip_ws();
        if self.at() != b'}' {
            loop {
                self.skip_ws();
                if self.at() != b'"' {
                    return self.refuse("malformed");
                }
                let k = self.string()?;
@DUPKEY@                self.skip_ws();
                if self.at() != b':' {
                    return self.refuse("malformed");
                }
                self.pos += 1;
                self.skip_ws();
                let v = self.raw_value()?;
                out.insert(k, v);
                self.skip_ws();
                if self.at() != b',' {
                    break;
                }
                self.pos += 1;
            }
        }
        if self.at() != b'}' {
            return self.refuse("malformed");
        }
        self.pos += 1;
        self.depth -= 1;
        Ok(out)
    }

    fn raw_value(&mut self) -> Result<Raw, Refusal> {
        let start = self.pos;
        self.skip_value()?;
        Ok(self.buf[start..self.pos].to_vec())
    }

    fn skip_value(&mut self) -> Result<(), Refusal> {
        let c = self.at();
        match c {
            b'"' => self.skip_string(),
            b'{' | b'[' => self.skip_container(),
            b't' => self.literal(b"true"),
            b'f' => self.literal(b"false"),
            b'n' => self.literal(b"null"),
            b'-' => self.skip_number(),
            _ if c.is_ascii_digit() => self.skip_number(),
            _ => self.refuse("malformed"),
        }
    }

    // An opaque value is validated and carried, never interpreted. Its syntax is
    // the record's own — the same string reader, so an escape, a control byte or
    // a lone surrogate is judged identically at any depth — and what it is free
    // to spell, it keeps: the bytes between these two offsets are what a writer
    // re-emits. A number is walked, never converted, so a value no host type
    // holds survives the reader that carried it.
    fn skip_string(&mut self) -> Result<(), Refusal> {
        self.string()?;
        Ok(())
    }

    fn some_digits(&mut self) -> Result<(), Refusal> {
        let mut n = 0;
        while self.at().is_ascii_digit() {
            self.pos += 1;
            n += 1;
        }
        if n == 0 {
            return self.refuse("number_spelling");
        }
        Ok(())
    }

    fn skip_number(&mut self) -> Result<(), Refusal> {
        if self.at() == b'-' {
            self.pos += 1;
        }
        match self.at() {
            b'0' => self.pos += 1,
            c if c.is_ascii_digit() => {
                while self.at().is_ascii_digit() {
                    self.pos += 1;
                }
            }
            _ => return self.refuse("number_spelling"),
        }
        if self.at() == b'.' {
            self.pos += 1;
            self.some_digits()?;
        }
        if self.at() == b'e' || self.at() == b'E' {
            self.pos += 1;
            if self.at() == b'+' || self.at() == b'-' {
                self.pos += 1;
            }
            self.some_digits()?;
        }
        Ok(())
    }

    fn skip_container(&mut self) -> Result<(), Refusal> {
        let opener = self.at();
        let shut = if opener == b'{' { b'}' } else { b']' };
        self.enter()?;
        self.pos += 1;
        self.skip_ws();
@SKIPSEEN@        if self.at() != shut {
            loop {
                self.skip_ws();
                if opener == b'{' {
                    if self.at() != b'"' {
                        return self.refuse("malformed");
                    }
                    @SKIPKEY@self.string()?;
@SKIPDUP@                    self.skip_ws();
                    if self.at() != b':' {
                        return self.refuse("malformed");
                    }
                    self.pos += 1;
                    self.skip_ws();
                }
                self.skip_value()?;
                self.skip_ws();
                if self.at() != b',' {
                    break;
                }
                self.pos += 1;
            }
        }
        if self.at() != shut {
            return self.refuse("malformed");
        }
        self.pos += 1;
        self.depth -= 1;
        Ok(())
    }
}
`

const rsStrMapDecode = `
fn str_map(r: &mut Reader) -> Result<BTreeMap<String, String>, Refusal> {
    if r.at() != b'{' {
        return r.refuse("wrong_type");
    }
    r.enter()?;
    r.pos += 1;
    let mut out = BTreeMap::new();
    r.skip_ws();
    if r.at() != b'}' {
        loop {
            r.skip_ws();
            if r.at() != b'"' {
                return r.refuse("malformed");
            }
            let k = r.string()?;
@STRDUP@            r.skip_ws();
            if r.at() != b':' {
                return r.refuse("malformed");
            }
            r.pos += 1;
            r.skip_ws();
            let v = r.string()?;
            out.insert(k, v);
            r.skip_ws();
            if r.at() != b',' {
                break;
            }
            r.pos += 1;
        }
    }
    if r.at() != b'}' {
        return r.refuse("malformed");
    }
    r.pos += 1;
    r.depth -= 1;
    Ok(out)
}
`

const rsDecodeList = `
fn decode_list<T>(
    r: &mut Reader,
    elem: fn(&mut Reader) -> Result<T, Refusal>,
) -> Result<Vec<T>, Refusal> {
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
            out.push(elem(r)?);
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

const rsDupKeyRefuse = `                if out.contains_key(&k) {
                    return self.refuse("duplicate_key");
                }
`

const rsStrDupKey = `            if out.contains_key(&k) {
                return r.refuse("duplicate_key");
            }
`

const rsSkipDupKey = `                    if seen.insert(k, true).is_some() {
                        return self.refuse("duplicate_key");
                    }
`

const rsTimestamp = `
fn digits(s: &[u8], i: usize, n: usize) -> bool {
    s.len() >= i + n && s[i..i + n].iter().all(|c| c.is_ascii_digit())
}

fn date_part(s: &[u8]) -> bool {
    s.len() >= 19
        && digits(s, 0, 4) && s[4] == b'-' && digits(s, 5, 2) && s[7] == b'-' && digits(s, 8, 2)
        && digits(s, 11, 2) && s[13] == b':' && digits(s, 14, 2) && s[16] == b':' && digits(s, 17, 2)
}

/// [DEF-G1] rfc3339-wide: what a reader accepts. Any fraction of one to nine
/// digits or none, either case of the separators, and a numeric offset.
fn lexical_timestamp(text: &str) -> bool {
    let s = text.as_bytes();
    if !date_part(s) || (s[10] != b'T' && s[10] != b't') {
        return false;
    }
    let mut i = 19;
    if i < s.len() && s[i] == b'.' {
        i += 1;
        let start = i;
        while i < s.len() && s[i].is_ascii_digit() {
            i += 1;
        }
        if i - start < 1 || i - start > 9 {
            return false;
        }
    }
    if i >= s.len() {
        return false;
    }
    if s[i] == b'Z' || s[i] == b'z' {
        return i + 1 == s.len();
    }
    if s[i] != b'+' && s[i] != b'-' {
        return false;
    }
    s.len() == i + 6 && digits(s, i + 1, 2) && s[i + 3] == b':' && digits(s, i + 4, 2)
}

/// [DEF-G2] rfc3339-micros: what a writer emits. Exactly six fractional digits,
/// upper-case separators, UTC.
pub fn micros_timestamp(text: &str) -> bool {
    text.len() == 27 && normalized_timestamp(text) == text
}

fn read_timestamp(r: &mut Reader) -> Result<String, Refusal> {
    let at = r.pos;
    let s = r.string()?;
    if !wide_timestamp(&s) {
        r.pos = at;
        return r.refuse("bad_timestamp");
    }
    Ok(s)
}
`

func rsDecoder(b *strings.Builder, s *Definition) {
	for _, st := range s.Structs {
		rsStructDecoder(b, s, st)
	}
	if s.Document != "" {
		fmt.Fprintf(b, "\npub fn decode(data: &[u8]) -> Result<%s, Refusal> {\n", s.Document)
		b.WriteString("    let mut r = Reader { buf: data, pos: 0, depth: 0 };\n    r.skip_ws();\n")
		bind := "let v"
		if s.Vocab != nil {
			bind = "let mut v"
		}
		fmt.Fprintf(b, "    %s = decode_%s(&mut r)?;\n    r.skip_ws();\n", bind, lower(s.Document))
		b.WriteString("    if r.pos < r.buf.len() {\n        return r.refuse(\"trailing_bytes\");\n    }\n")
		if s.Vocab != nil {
			b.WriteString("    derive(&r, &mut v)?;\n")
		}
		b.WriteString("    Ok(v)\n}\n")
	}
	rsDerive(b, s)
}

func rsStructDecoder(b *strings.Builder, s *Definition, st Struct) {
	fmt.Fprintf(b, "\nfn decode_%s(r: &mut Reader) -> Result<%s, Refusal> {\n", lower(st.Name), st.Name)
	b.WriteString("    if r.at() != b'{' {\n        return r.refuse(\"wrong_type\");\n    }\n")
	fmt.Fprintf(b, "    r.enter()?;\n    r.pos += 1;\n    let mut v = %s::default();\n    let mut seen: u32 = 0;\n    r.skip_ws();\n", st.Name)
	b.WriteString("    if r.at() != b'}' {\n        loop {\n            r.skip_ws();\n")
	b.WriteString("            if r.at() != b'\"' {\n                return r.refuse(\"malformed\");\n            }\n")
	b.WriteString("            let key = r.string()?;\n            r.skip_ws();\n")
	b.WriteString("            if r.at() != b':' {\n                return r.refuse(\"malformed\");\n            }\n")
	b.WriteString("            r.pos += 1;\n            r.skip_ws();\n            match key.as_str() {\n")
	for i, f := range st.Fields {
		fmt.Fprintf(b, "                %q => {\n", f.Name)
		fmt.Fprintf(b, "                    if seen & %d != 0 {\n                        return r.refuse(\"duplicate_field\");\n                    }\n", 1<<i)
		fmt.Fprintf(b, "                    seen |= %d;\n", 1<<i)
		fmt.Fprintf(b, "                    v.%s = %s;\n                }\n", f.Ident("rust"), rsRead(s, f))
	}
	b.WriteString("                _ => {\n")
	if st.RefuseUnknown() {
		b.WriteString("                    return r.refuse(\"unknown_field\");\n")
	} else if st.PreservesUnknown() {
		if s.Encoding.RefuseDuplicateKeys() {
			b.WriteString("                    if v.extras.contains_key(&key) { return r.refuse(\"duplicate_key\"); }\n")
		}
		b.WriteString("                    let raw = r.raw_value()?; v.extras.insert(key, raw);\n")
	} else {
		b.WriteString("                    r.skip_value()?;\n")
	}
	b.WriteString("                }\n            }\n            r.skip_ws();\n            if r.at() != b',' {\n                break;\n            }\n            r.pos += 1;\n        }\n    }\n")
	b.WriteString("    if r.at() != b'}' {\n        return r.refuse(\"malformed\");\n    }\n    r.pos += 1;\n    r.depth -= 1;\n")
	if req := requiredMask(st); req != 0 {
		fmt.Fprintf(b, "    if seen & %d != %d {\n        return r.refuse(\"missing_field\");\n    }\n", req, req)
	}
	emitEqualities(b, st, "rust", false)
	emitEnumChecks(b, st, "rust", false)
	b.WriteString("    Ok(v)\n}\n")
}

func rsRead(s *Definition, f Field) string {
	if enumAbsent(f) {
		return "Some(r.string()?)"
	}
	switch f.Type {
	case "string":
		if f.Grammar.Named() {
			return "read_timestamp(r)?"
		}
		return "r.string()?"
	case "i32":
		return "r.integer(-2147483648, 2147483647)? as i32"
	case "i64":
		return "r.integer(i64::MIN, i64::MAX)?"
	case "bool":
		return "r.boolean()?"
	case "json":
		return "r.raw_value()?"
	case "list<string>":
		return "r.str_list()?"
	case "map<string,json>":
		return "r.raw_map()?"
	case "list<json>":
		return "raw_list(r)?"
	case "map<string,string>":
		return "str_map(r)?"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "decode_list(r, decode_" + lower(elem) + ")?"
	}
	if f.Omit == "absent" {
		return "Some(decode_" + lower(f.Type) + "(r)?)"
	}
	return "decode_" + lower(f.Type) + "(r)?"
}

func rsDerive(b *strings.Builder, s *Definition) {
	v := s.Vocab
	if v == nil {
		return
	}
	fmt.Fprintf(b, "\npub const %s_TERMS: [&str; %d] = [", upper(v.Name), len(v.Terms))
	for i, t := range v.Terms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("];\n")
	var strip []Term
	for _, t := range v.Terms {
		if t.StripCritical {
			strip = append(strip, t)
		}
	}
	fmt.Fprintf(b, "pub const %s_STRIP_CRITICAL: [&str; %d] = [", upper(v.Name), len(strip))
	for i, t := range strip {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("];\n")
	fmt.Fprintf(b, "\nfn derive(r: &Reader, v: &mut %s) -> Result<(), Refusal> {\n", v.Of)
	fmt.Fprintf(b, "    let names = v.%s.clone();\n", v.Names)
	b.WriteString("    let present = |n: &str| names.iter().any(|x| x == n);\n")
	b.WriteString("    let mut kept: Vec<String> = Vec::new();\n")
	fmt.Fprintf(b, "    for name in &v.%s {\n", v.Critical)
	fmt.Fprintf(b, "        if %s_STRIP_CRITICAL.contains(&name.as_str()) {\n            continue;\n        }\n", upper(v.Name))
	fmt.Fprintf(b, "        if !%s_TERMS.contains(&name.as_str()) {\n            return r.refuse(\"unknown_critical\");\n        }\n", upper(v.Name))
	b.WriteString("        if !present(name) {\n            return r.refuse(\"not_a_subset\");\n        }\n        kept.push(name.clone());\n    }\n")
	fmt.Fprintf(b, "    v.%s = kept;\n", v.Critical)
	for _, t := range v.Terms {
		fmt.Fprintf(b, "    if (%s) != present(%q) {\n        return r.refuse(\"content_mismatch\");\n    }\n", rsTest(s, t), t.Name)
	}
	b.WriteString("    Ok(())\n}\n")
	if v.UsesMember() {
		b.WriteString(rsMember)
	}
}

func rsTest(s *Definition, t Term) string {
	if t.Always() {
		return "true"
	}
	e := "v"
	for _, f := range t.Path {
		e += "." + f.Ident("rust")
	}
	switch {
	case t.Member != "":
		return fmt.Sprintf("member(&%s, %q)", e, t.Member)
	case t.Membership():
		parts := make([]string, len(t.Is))
		for i, w := range t.Is {
			parts[i] = fmt.Sprintf("%q", w)
		}
		return fmt.Sprintf("matches!(%s.as_str(), %s)", e, strings.Join(parts, " | "))
	}
	return rsPresent(s, t.Last(), e)
}

const rsMember = `
/// [DEF-A8] Whether an opaque value is an object naming this member with
/// something other than null. The key is decoded, so two spellings of one name
/// are one name; the value is neither decoded nor judged.
pub fn member(raw: &[u8], name: &str) -> bool {
    let mut r = Reader { buf: raw, pos: 0, depth: 0 };
    r.skip_ws();
    if r.at() != b'{' {
        return false;
    }
    r.pos += 1;
    r.skip_ws();
    while r.at() == b'"' {
        let k = match r.string() {
            Ok(k) => k,
            Err(_) => return false,
        };
        r.skip_ws();
        r.pos += 1;
        r.skip_ws();
        if k == name {
            return r.at() != b'n';
        }
        if r.skip_value().is_err() {
            return false;
        }
        r.skip_ws();
        if r.at() != b',' {
            return false;
        }
        r.pos += 1;
        r.skip_ws();
    }
    false
}
`

func genRust(s *Definition) string {
	s = enumCarriers(s)
	var b strings.Builder
	esc := rsEscMinimal
	if s.Encoding.EscapeNonASCII() {
		esc = rsEscASCII
	}
	prelude := rsCommon + esc
	if s.StringMapDocument() {
		prelude += rsStrMap
	}
	if s.HasRepeated() {
		prelude += rsEncList
	}
	b.WriteString(strings.NewReplacer("@INDENT@", strconv.Itoa(s.Encoding.Indent)).Replace(prelude))
	rsVocabulary(&b, s)
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "\n#[derive(Default)]\npub struct %s {\n", st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "    pub %s: %s,\n", f.Ident("rust"), rsType(s, f))
		}
		if st.PreservesUnknown() {
			b.WriteString("    pub extras: BTreeMap<String, Raw>,\n")
		}
		b.WriteString("}\n")
	}
	for _, st := range s.Structs {
		if !s.Envelope(st.Name) {
			rsEncoder(&b, s, st, false)
		}
	}
	tail := ""
	if s.Encoding.TrailingNewline() {
		tail = "    out.push(b'\\n');\n"
	}
	if s.Document != "" {
		fmt.Fprintf(&b, "\npub fn encode(v: &%s) -> Vec<u8> {\n    let mut out = Vec::new();\n    enc_%s(&mut out, v, 0);\n%s    out\n}\n",
			s.Document, lower(s.Document), tail)
	}
	dup, skipSeen, skipKey, skipDup, strDup := "", "", "", "", ""
	if s.Encoding.RefuseDuplicateKeys() {
		dup = rsDupKeyRefuse
		skipSeen = "        let mut seen: BTreeMap<String, bool> = BTreeMap::new();\n"
		skipKey = "let k = "
		skipDup = rsSkipDupKey
		strDup = rsStrDupKey
	}
	decode := rsDecodeCommon
	if s.HasStringMap() {
		decode += rsStrMapDecode
	}
	if s.HasRepeated() {
		decode += rsDecodeList
	}
	b.WriteString(strings.NewReplacer(
		"@DEPTH@", strconv.Itoa(s.Encoding.DepthLimit),
		"@DUPKEY@", dup,
		"@SKIPSEEN@", skipSeen,
		"@SKIPKEY@", skipKey,
		"@SKIPDUP@", skipDup,
		"@STRDUP@", strDup,
	).Replace(decode))
	if s.Timestamps() {
		b.WriteString(rsTimestamp)
		b.WriteString(rsTimestampNormalize)
	}
	if s.PreservesUnknown() {
		b.WriteString(rsPreserve)
	}
	rsDecoder(&b, s)
	rsProtocol(&b, s)
	return b.String()
}

func rsVocabulary(b *strings.Builder, s *Definition) {
	for _, en := range s.Enums {
		fmt.Fprintf(b, "\npub const %s_NAMES: [&str; %d] = [", upper(en.Name), len(en.Members))
		for i, m := range en.Members {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%q", m.Name)
		}
		b.WriteString("];\n")
		fmt.Fprintf(b, "pub const %s_%s: &str = %q;\n", upper(en.Name), enumPolicyName(en, "rust"), en.Ann["unknown"])
		for _, key := range en.MemberAnn() {
			var rows []Member
			for _, m := range en.Members {
				if _, ok := m.Ann[key]; ok {
					rows = append(rows, m)
				}
			}
			fmt.Fprintf(b, "pub const %s_%s: [(&str, &str); %d] = [\n", upper(en.Name), upper(key), len(rows))
			for _, m := range rows {
				fmt.Fprintf(b, "    (%q, %q),\n", m.Name, m.Ann[key])
			}
			b.WriteString("];\n")
		}
	}
	for _, c := range s.Consts {
		if c.Type == "list<i32>" {
			fmt.Fprintf(b, "\npub const %s: [i32; %d] = [", upper(c.Name), len(c.Ints))
			for i, n := range c.Ints {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%d", n)
			}
		} else {
			fmt.Fprintf(b, "\npub const %s: [&str; %d] = [", upper(c.Name), len(c.Strings))
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

func rsEncoder(b *strings.Builder, s *Definition, st Struct, flat bool) {
	p := plan(st)
	if flat {
		fmt.Fprintf(b, "\npub fn enc_wire_%s(out: &mut Vec<u8>, v: &%s) {\n", lower(st.Name), st.Name)
	} else {
		fmt.Fprintf(b, "\npub fn enc_%s(out: &mut Vec<u8>, v: &%s, depth: i32) {\n", lower(st.Name), st.Name)
	}
	emitEqualities(b, st, "rust", true)
	emitEnumChecks(b, st, "rust", true)
	b.WriteString("    out.push(b'{');\n")
	if p.flag {
		b.WriteString("    let mut first = true;\n")
	}
	for i, f := range st.Fields {
		e := "v." + f.Ident("rust")
		ind := "    "
		if f.Omit != "never" {
			fmt.Fprintf(b, "    if %s {\n", rsPresent(s, f, e))
			ind = "        "
		}
		switch p.before[i] {
		case "always":
			fmt.Fprintf(b, "%sout.push(b',');\n", ind)
		case "flag":
			fmt.Fprintf(b, "%sif !first {\n%s    out.push(b',');\n%s}\n", ind, ind, ind)
		}
		if p.clears(i) {
			fmt.Fprintf(b, "%sfirst = false;\n", ind)
		}
		if flat {
			fmt.Fprintf(b, "%sesc(out, %q);\n%sout.push(b':');\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, rsValueFlat(s, f, e))
		} else {
			fmt.Fprintf(b, "%sout.push(b'\\n');\n%spad(out, depth + 1);\n", ind, ind)
			fmt.Fprintf(b, "%sesc(out, %q);\n%sout.extend_from_slice(b\": \");\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, rsValue(s, f, e))
		}
		if f.Omit != "never" {
			b.WriteString("    }\n")
		}
	}
	if st.PreservesUnknown() {
		fmt.Fprintf(b, "    first = extra_fields(out, &v.extras, &[%s], depth, first);\n", quotedFieldNames(st))
	}
	if !flat {
		switch {
		case p.closeAlways:
			b.WriteString("    out.push(b'\\n');\n    pad(out, depth);\n")
		case len(st.Fields) > 0 || st.PreservesUnknown():
			b.WriteString("    if !first {\n        out.push(b'\\n');\n        pad(out, depth);\n    }\n")
		}
	}
	b.WriteString("    out.push(b'}');\n}\n")
}

func rsType(s *Definition, f Field) string {
	if enumAbsent(f) {
		return "Option<String>"
	}
	switch f.Type {
	case "string":
		return "String"
	case "json":
		return "Raw"
	case "i32":
		return "i32"
	case "i64":
		return "i64"
	case "bool":
		return "bool"
	case "list<string>":
		return "Vec<String>"
	case "map<string,json>":
		return "BTreeMap<String, Raw>"
	case "list<json>":
		return "Vec<Raw>"
	case "map<string,string>":
		return "BTreeMap<String, String>"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "Vec<" + elem + ">"
	}
	if f.Omit == "absent" {
		return "Option<" + f.Type + ">"
	}
	return f.Type
}

func rsPresent(s *Definition, f Field, e string) string {
	if f.Omit == "absent" && (s.IsStruct(f.Type) || enumAbsent(f)) {
		return e + ".is_some()"
	}
	switch f.Type {
	case "i32", "i64":
		return e + " != 0"
	case "bool":
		return e
	}
	return "!" + e + ".is_empty()"
}

func rsValue(s *Definition, f Field, e string) string {
	if enumAbsent(f) {
		return "esc(out, " + e + ".as_ref().unwrap());"
	}
	if f.Grammar.Named() {
		return "esc(out, &write_timestamp(&" + e + "));"
	}
	switch f.Type {
	case "string":
		return "esc(out, &" + e + ");"
	case "json":
		return "raw(out, &" + e + ", depth + 1);"
	case "i32":
		return "num(out, " + e + " as i64);"
	case "i64":
		return "num(out, " + e + ");"
	case "bool":
		return "out.extend_from_slice(if " + e + ` { b"true" } else { b"false" });`
	case "list<string>":
		return "strs(out, &" + e + ", depth + 1);"
	case "map<string,json>":
		return "rawmap(out, &" + e + ", depth + 1);"
	case "map<string,string>":
		return "strmap(out, &" + e + ", depth + 1);"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "enc_list(out, &" + e + ", depth + 1, enc_" + lower(elem) + ");"
	}
	if f.Omit == "absent" {
		return "enc_" + lower(f.Type) + "(out, " + e + ".as_ref().unwrap(), depth + 1);"
	}
	return "enc_" + lower(f.Type) + "(out, &" + e + ", depth + 1);"
}

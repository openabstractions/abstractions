package main

import (
	"strconv"
	"strings"
)

// A fixed-size counter consumes only newly emitted bytes after each field and
// the closing token. The record's compact and raw limits are independent.
func emitMaxEncodedWriter(b *strings.Builder, s *Definition, lang string) {
	if !s.hasMaxEncoded() {
		return
	}
	body := map[string]string{
		"go": compactEncodedGo, "python": compactEncodedPython,
		"cpp": compactEncodedCpp, "javascript": compactEncodedJS,
		"rust": compactEncodedRust,
	}[lang]
	b.WriteString(strings.ReplaceAll(body, "@RAW_LIMIT@", strconv.FormatInt(recordCeiling, 10)))
}

const compactEncodedGo = `
type compactEncodedCounter struct {
	start, pos, count int
	quoted, escaped bool
}
func (c *compactEncodedCounter) check(out []byte, limit int) {
	if len(out)-c.start > @RAW_LIMIT@ { panic(&Refusal{Word: "encoded_size", Offset: 0}) }
	for ; c.pos < len(out); c.pos++ {
		b := out[c.pos]
		if !c.quoted && (b == ' ' || b == '\t' || b == '\r' || b == '\n') { continue }
		c.count++
		if c.count > limit { panic(&Refusal{Word: "encoded_size", Offset: 0}) }
		if c.quoted {
			if c.escaped { c.escaped = false } else if b == '\\' { c.escaped = true } else if b == '"' { c.quoted = false }
		} else if b == '"' { c.quoted = true }
	}
}
`

const compactEncodedPython = `
class _CompactEncodedCounter:
    __slots__ = ('start', 'pos', 'count', 'quoted', 'escaped')
    def __init__(self, start):
        self.start = self.pos = start
        self.count = 0
        self.quoted = self.escaped = False
    def check(self, out, limit):
        if len(out)-self.start > @RAW_LIMIT@: raise Refusal("encoded_size", 0)
        while self.pos < len(out):
            b = out[self.pos]
            self.pos += 1
            if not self.quoted and b in (0x20, 9, 10, 13): continue
            self.count += 1
            if self.count > limit: raise Refusal("encoded_size", 0)
            if self.quoted:
                if self.escaped: self.escaped = False
                elif b == 0x5c: self.escaped = True
                elif b == 0x22: self.quoted = False
            elif b == 0x22: self.quoted = True
`

const compactEncodedCpp = `
struct CompactEncodedCounter {
    std::size_t start, pos, count = 0;
    bool quoted = false, escaped = false;
    explicit CompactEncodedCounter(std::size_t at): start(at), pos(at) {}
    void check(std::string_view out, std::size_t limit) {
        if (out.size()-start > @RAW_LIMIT@) throw Refusal("encoded_size", 0);
        for (; pos < out.size(); ++pos) {
            const auto b = out[pos];
            if (!quoted && (b == ' ' || b == '\t' || b == '\r' || b == '\n')) continue;
            if (++count > limit) throw Refusal("encoded_size", 0);
            if (quoted) {
                if (escaped) escaped = false;
                else if (b == '\\') escaped = true;
                else if (b == '"') quoted = false;
            } else if (b == '"') quoted = true;
        }
    }
};
`

const compactEncodedJS = `
class CompactEncodedCounter {
  constructor(start) { this.start = this.pos = start; this.count = 0; this.quoted = this.escaped = false; }
  check(out, limit) {
    if (out.length-this.start > @RAW_LIMIT@) throw new Refusal("encoded_size", 0);
    for (; this.pos < out.length; this.pos++) {
      const b = out[this.pos];
      if (!this.quoted && (b === 32 || b === 9 || b === 10 || b === 13)) continue;
      if (++this.count > limit) throw new Refusal("encoded_size", 0);
      if (this.quoted) {
        if (this.escaped) this.escaped = false;
        else if (b === 0x5c) this.escaped = true;
        else if (b === 0x22) this.quoted = false;
      } else if (b === 0x22) this.quoted = true;
    }
  }
}
`

const compactEncodedRust = `
struct CompactEncodedCounter { start: usize, pos: usize, count: usize, quoted: bool, escaped: bool }
impl CompactEncodedCounter {
    fn new(start: usize) -> Self { Self { start, pos: start, count: 0, quoted: false, escaped: false } }
    fn check(&mut self, out: &[u8], limit: usize) {
        if out.len()-self.start > @RAW_LIMIT@ { std::panic::panic_any(Refusal { word: "encoded_size", offset: 0 }); }
        while self.pos < out.len() {
            let b = out[self.pos]; self.pos += 1;
            if !self.quoted && matches!(b, b' ' | b'\t' | b'\r' | b'\n') { continue; }
            self.count += 1;
            if self.count > limit { std::panic::panic_any(Refusal { word: "encoded_size", offset: 0 }); }
            if self.quoted {
                if self.escaped { self.escaped = false; } else if b == b'\\' { self.escaped = true; } else if b == b'"' { self.quoted = false; }
            } else if b == b'"' { self.quoted = true; }
        }
    }
}
`

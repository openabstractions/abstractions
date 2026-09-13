package main

import (
	"fmt"
	"go/format"
	"strconv"
	"strings"
)

const goEscMinimal = `
func esc(out []byte, s string) []byte {
	out = append(out, '"')
	for i := 0; i < len(s); i++ {
		out = escByte(out, s[i])
	}
	return append(out, '"')
}
`

const goEscASCII = `
func esc(out []byte, s string) []byte {
	out = append(out, '"')
	for i := 0; i < len(s); {
		c := s[i]
		if c < 0x80 {
			out = escByte(out, c)
			i++
			continue
		}
		cp, n := decode(s, i)
		out = escUnicode(out, cp)
		i += n
	}
	return append(out, '"')
}

func decode(s string, i int) (int32, int) {
	c := int32(s[i])
	switch {
	case c >= 0xF0:
		return (c&0x07)<<18 | int32(s[i+1]&0x3F)<<12 | int32(s[i+2]&0x3F)<<6 | int32(s[i+3]&0x3F), 4
	case c >= 0xE0:
		return (c&0x0F)<<12 | int32(s[i+1]&0x3F)<<6 | int32(s[i+2]&0x3F), 3
	default:
		return (c&0x1F)<<6 | int32(s[i+1]&0x3F), 2
	}
}

func escUnicode(out []byte, cp int32) []byte {
	if cp > 0xFFFF {
		cp -= 0x10000
		out = unit(out, 0xD800+(cp>>10))
		return unit(out, 0xDC00+(cp&0x3FF))
	}
	return unit(out, cp)
}

func unit(out []byte, u int32) []byte {
	const hex = "0123456789abcdef"
	return append(out, '\\', 'u', hex[(u>>12)&0xF], hex[(u>>8)&0xF], hex[(u>>4)&0xF], hex[u&0xF])
}
`

const goCommon = `package rec

type Raw = string

func escByte(out []byte, c byte) []byte {
	const hex = "0123456789abcdef"
	switch c {
	case '"':
		return append(out, '\\', '"')
	case '\\':
		return append(out, '\\', '\\')
	case 0x08:
		return append(out, '\\', 'b')
	case 0x0c:
		return append(out, '\\', 'f')
	case '\n':
		return append(out, '\\', 'n')
	case '\r':
		return append(out, '\\', 'r')
	case '\t':
		return append(out, '\\', 't')
	}
	if c < 0x20 {
		return append(out, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
	}
	return append(out, c)
}

func num(out []byte, n int64) []byte {
	if n == 0 {
		return append(out, '0')
	}
	var d [24]byte
	p := len(d)
	u := uint64(n)
	if n < 0 {
		u = -uint64(n)
	}
	for u > 0 {
		p--
		d[p] = byte('0' + u%10)
		u /= 10
	}
	if n < 0 {
		out = append(out, '-')
	}
	return append(out, d[p:]...)
}

func pad(out []byte, depth int) []byte {
	for i := 0; i < depth*@INDENT@; i++ {
		out = append(out, ' ')
	}
	return out
}

func strs(out []byte, v []string, depth int) []byte {
	if len(v) == 0 {
		return append(out, '[', ']')
	}
	out = append(out, '[', '\n')
	for i, s := range v {
		out = pad(out, depth+1)
		out = esc(out, s)
		if i+1 < len(v) {
			out = append(out, ',')
		}
		out = append(out, '\n')
	}
	out = pad(out, depth)
	return append(out, ']')
}

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

func byteLess(a, b string) bool {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func sortedKeys(m map[string]Raw) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && byteLess(keys[j], keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func rawmap(out []byte, m map[string]Raw, depth int) []byte {
	keys := sortedKeys(m)
	if len(keys) == 0 {
		return append(out, '{', '}')
	}
	out = append(out, '{', '\n')
	for i, k := range keys {
		out = pad(out, depth+1)
		out = esc(out, k)
		out = append(out, ':', ' ')
		out = raw(out, m[k], depth+1)
		if i+1 < len(keys) {
			out = append(out, ',')
		}
		out = append(out, '\n')
	}
	out = pad(out, depth)
	return append(out, '}')
}
`

const goStrMap = `
func strmap(out []byte, m map[string]string, depth int) []byte {
	keys := sortedKeys(m)
	if len(keys) == 0 {
		return append(out, '{', '}')
	}
	out = append(out, '{', '\n')
	for i, k := range keys {
		out = pad(out, depth+1)
		out = esc(out, k)
		out = append(out, ':', ' ')
		out = esc(out, m[k])
		if i+1 < len(keys) {
			out = append(out, ',')
		}
		out = append(out, '\n')
	}
	out = pad(out, depth)
	return append(out, '}')
}
`

const goEncList = `
func encList[T any](out []byte, v []T, depth int, enc func([]byte, *T, int) []byte) []byte {
	if len(v) == 0 {
		return append(out, '[', ']')
	}
	out = append(out, '[', '\n')
	for i := range v {
		out = pad(out, depth+1)
		out = enc(out, &v[i], depth+1)
		if i+1 < len(v) {
			out = append(out, ',')
		}
		out = append(out, '\n')
	}
	out = pad(out, depth)
	return append(out, ']')
}
`

const goDecodeCommon = `
type Refusal struct {
	Word   string
	Offset int
}

func (r *Refusal) Error() string {
	return "refused: " + r.Word + " at byte " + decimal(int64(r.Offset))
}

func decimal(n int64) string { return string(num(nil, n)) }

const depthLimit = @DEPTH@

type reader struct {
	buf   []byte
	pos   int
	depth int
}

func (r *reader) refuse(word string) error { return &Refusal{Word: word, Offset: r.pos} }

func (r *reader) at() byte {
	if r.pos < len(r.buf) {
		return r.buf[r.pos]
	}
	return 0
}

func (r *reader) ws() {
	for r.pos < len(r.buf) {
		switch r.buf[r.pos] {
		case ' ', '\t', '\n', '\r':
			r.pos++
		default:
			return
		}
	}
}

func (r *reader) enter() error {
	r.depth++
	if r.depth > depthLimit {
		return r.refuse("depth_exceeded")
	}
	return nil
}

func (r *reader) str() (string, error) {
	if r.at() != '"' {
		return "", r.refuse("wrong_type")
	}
	r.pos++
	var out []byte
	for {
		if r.pos >= len(r.buf) {
			return "", r.refuse("malformed")
		}
		c := r.buf[r.pos]
		switch {
		case c == '"':
			r.pos++
			if !validUTF8(out) {
				return "", r.refuse("bad_string")
			}
			return string(out), nil
		case c < 0x20:
			return "", r.refuse("bad_string")
		case c == '\\':
			var err error
			if out, err = r.escape(out); err != nil {
				return "", err
			}
		default:
			out = append(out, c)
			r.pos++
		}
	}
}

func (r *reader) escape(out []byte) ([]byte, error) {
	r.pos++
	if r.pos >= len(r.buf) {
		return nil, r.refuse("malformed")
	}
	c := r.buf[r.pos]
	r.pos++
	switch c {
	case '"', '\\', '/':
		return append(out, c), nil
	case 'b':
		return append(out, 0x08), nil
	case 'f':
		return append(out, 0x0c), nil
	case 'n':
		return append(out, '\n'), nil
	case 'r':
		return append(out, '\r'), nil
	case 't':
		return append(out, '\t'), nil
	case 'u':
		u, err := r.hex4()
		if err != nil {
			return nil, err
		}
		if u >= 0xDC00 && u <= 0xDFFF {
			return nil, r.refuse("bad_string")
		}
		if u >= 0xD800 && u <= 0xDBFF {
			if r.pos+1 >= len(r.buf) || r.buf[r.pos] != '\\' || r.buf[r.pos+1] != 'u' {
				return nil, r.refuse("bad_string")
			}
			r.pos += 2
			lo, err := r.hex4()
			if err != nil {
				return nil, err
			}
			if lo < 0xDC00 || lo > 0xDFFF {
				return nil, r.refuse("bad_string")
			}
			u = 0x10000 + (u-0xD800)<<10 + (lo - 0xDC00)
		}
		return appendRune(out, u), nil
	}
	return nil, r.refuse("bad_string")
}

func (r *reader) hex4() (int32, error) {
	if r.pos+4 > len(r.buf) {
		return 0, r.refuse("bad_string")
	}
	var u int32
	for i := 0; i < 4; i++ {
		c := r.buf[r.pos+i]
		switch {
		case c >= '0' && c <= '9':
			u = u<<4 | int32(c-'0')
		case c >= 'a' && c <= 'f':
			u = u<<4 | int32(c-'a'+10)
		case c >= 'A' && c <= 'F':
			u = u<<4 | int32(c-'A'+10)
		default:
			return 0, r.refuse("bad_string")
		}
	}
	r.pos += 4
	return u, nil
}

func appendRune(out []byte, cp int32) []byte {
	switch {
	case cp < 0x80:
		return append(out, byte(cp))
	case cp < 0x800:
		return append(out, byte(0xC0|cp>>6), byte(0x80|cp&0x3F))
	case cp < 0x10000:
		return append(out, byte(0xE0|cp>>12), byte(0x80|cp>>6&0x3F), byte(0x80|cp&0x3F))
	}
	return append(out, byte(0xF0|cp>>18), byte(0x80|cp>>12&0x3F), byte(0x80|cp>>6&0x3F), byte(0x80|cp&0x3F))
}

func validUTF8(b []byte) bool {
	for i := 0; i < len(b); {
		c := b[i]
		var n int
		var cp int32
		switch {
		case c < 0x80:
			i++
			continue
		case c>>5 == 0x6:
			n, cp = 2, int32(c&0x1F)
		case c>>4 == 0xE:
			n, cp = 3, int32(c&0x0F)
		case c>>3 == 0x1E:
			n, cp = 4, int32(c&0x07)
		default:
			return false
		}
		if i+n > len(b) {
			return false
		}
		for k := 1; k < n; k++ {
			if b[i+k]>>6 != 0x2 {
				return false
			}
			cp = cp<<6 | int32(b[i+k]&0x3F)
		}
		lowest := []int32{0, 0, 0x80, 0x800, 0x10000}[n]
		if cp < lowest || cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF) {
			return false
		}
		i += n
	}
	return true
}

func (r *reader) integer(min, max int64) (int64, error) {
	c := r.at()
	if c != '-' && (c < '0' || c > '9') {
		return 0, r.refuse("wrong_type")
	}
	neg := c == '-'
	if neg {
		r.pos++
	}
	d := r.at()
	if d < '0' || d > '9' {
		return 0, r.refuse("malformed")
	}
	var u uint64
	if d == '0' {
		r.pos++
	} else {
		for d = r.at(); d >= '0' && d <= '9'; d = r.at() {
			if u > 922337203685477580 {
				return 0, r.refuse("number_spelling")
			}
			u = u*10 + uint64(d-'0')
			r.pos++
		}
	}
	switch c := r.at(); {
	case c == '.' || c == 'e' || c == 'E' || (c >= '0' && c <= '9'):
		return 0, r.refuse("number_spelling")
	}
	if neg && u == 0 {
		return 0, r.refuse("number_spelling")
	}
	var n int64
	switch {
	case neg && u == 1<<63:
		n = -1 << 63
	case u > 1<<63-1:
		return 0, r.refuse("number_spelling")
	case neg:
		n = -int64(u)
	default:
		n = int64(u)
	}
	if n < min || n > max {
		return 0, r.refuse("number_spelling")
	}
	return n, nil
}

func (r *reader) boolean() (bool, error) {
	switch r.at() {
	case 't':
		return true, r.literal("true")
	case 'f':
		return false, r.literal("false")
	}
	return false, r.refuse("wrong_type")
}

func (r *reader) literal(word string) error {
	if r.pos+len(word) > len(r.buf) || string(r.buf[r.pos:r.pos+len(word)]) != word {
		return r.refuse("malformed")
	}
	r.pos += len(word)
	return nil
}

func (r *reader) strList() ([]string, error) {
	if r.at() != '[' {
		return nil, r.refuse("wrong_type")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	r.pos++
	out := []string{}
	r.ws()
	if r.at() != ']' {
		for {
			r.ws()
			s, err := r.str()
			if err != nil {
				return nil, err
			}
			out = append(out, s)
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

func (r *reader) rawMap() (map[string]Raw, error) {
	if r.at() != '{' {
		return nil, r.refuse("wrong_type")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	r.pos++
	out := map[string]Raw{}
	r.ws()
	if r.at() != '}' {
		for {
			r.ws()
			if r.at() != '"' {
				return nil, r.refuse("malformed")
			}
			k, err := r.str()
			if err != nil {
				return nil, err
			}
@DUPKEY@			r.ws()
			if r.at() != ':' {
				return nil, r.refuse("malformed")
			}
			r.pos++
			r.ws()
			v, err := r.rawValue()
			if err != nil {
				return nil, err
			}
			out[k] = v
			r.ws()
			if r.at() != ',' {
				break
			}
			r.pos++
		}
	}
	if r.at() != '}' {
		return nil, r.refuse("malformed")
	}
	r.pos++
	r.depth--
	return out, nil
}

func (r *reader) rawValue() (Raw, error) {
	start := r.pos
	if err := r.skipValue(); err != nil {
		return "", err
	}
	return Raw(r.buf[start:r.pos]), nil
}

func (r *reader) skipValue() error {
	c := r.at()
	switch {
	case c == '"':
		return r.skipString()
	case c == '{' || c == '[':
		return r.skipContainer()
	case c == 't':
		return r.literal("true")
	case c == 'f':
		return r.literal("false")
	case c == 'n':
		return r.literal("null")
	case c == '-' || (c >= '0' && c <= '9'):
		return r.skipNumber()
	}
	return r.refuse("malformed")
}

// An opaque value is validated and carried, never interpreted. Its syntax is
// the record's own — the same string reader, so an escape, a control byte or a
// lone surrogate is judged identically at any depth — and what it is free to
// spell, it keeps: the bytes between these two offsets are what a writer
// re-emits. A number is walked, never converted, so a value no host type holds
// survives the reader that carried it.
func (r *reader) skipString() error {
	_, err := r.str()
	return err
}

func (r *reader) skipNumber() error {
	if r.at() == '-' {
		r.pos++
	}
	switch c := r.at(); {
	case c == '0':
		r.pos++
	case c >= '1' && c <= '9':
		for c := r.at(); c >= '0' && c <= '9'; c = r.at() {
			r.pos++
		}
	default:
		return r.refuse("number_spelling")
	}
	if r.at() == '.' {
		r.pos++
		if err := r.someDigits(); err != nil {
			return err
		}
	}
	if c := r.at(); c == 'e' || c == 'E' {
		r.pos++
		if c := r.at(); c == '+' || c == '-' {
			r.pos++
		}
		if err := r.someDigits(); err != nil {
			return err
		}
	}
	return nil
}

func (r *reader) someDigits() error {
	n := 0
	for c := r.at(); c >= '0' && c <= '9'; c = r.at() {
		r.pos++
		n++
	}
	if n == 0 {
		return r.refuse("number_spelling")
	}
	return nil
}

func (r *reader) skipContainer() error {
	open := r.buf[r.pos]
	shut := byte('}')
	if open == '[' {
		shut = ']'
	}
	if err := r.enter(); err != nil {
		return err
	}
	r.pos++
	r.ws()
@SKIPSEEN@	if r.at() != shut {
		for {
			r.ws()
			if open == '{' {
				if r.at() != '"' {
					return r.refuse("malformed")
				}
				@SKIPKEY@r.str()
				if err != nil {
					return err
				}
@SKIPDUP@				r.ws()
				if r.at() != ':' {
					return r.refuse("malformed")
				}
				r.pos++
				r.ws()
			}
			if err := r.skipValue(); err != nil {
				return err
			}
			r.ws()
			if r.at() != ',' {
				break
			}
			r.pos++
		}
	}
	if r.at() != shut {
		return r.refuse("malformed")
	}
	r.pos++
	r.depth--
	return nil
}
`

const goStrMapDecode = `
func (r *reader) strMap() (map[string]string, error) {
	if r.at() != '{' {
		return nil, r.refuse("wrong_type")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	r.pos++
	out := map[string]string{}
	r.ws()
	if r.at() != '}' {
		for {
			r.ws()
			if r.at() != '"' {
				return nil, r.refuse("malformed")
			}
			k, err := r.str()
			if err != nil {
				return nil, err
			}
@DUPKEY@			r.ws()
			if r.at() != ':' {
				return nil, r.refuse("malformed")
			}
			r.pos++
			r.ws()
			v, err := r.str()
			if err != nil {
				return nil, err
			}
			out[k] = v
			r.ws()
			if r.at() != ',' {
				break
			}
			r.pos++
		}
	}
	if r.at() != '}' {
		return nil, r.refuse("malformed")
	}
	r.pos++
	r.depth--
	return out, nil
}
`

const goDecodeList = `
func decodeList[T any](r *reader, elem func(*reader) (*T, error)) ([]T, error) {
	if r.at() != '[' {
		return nil, r.refuse("wrong_type")
	}
	if err := r.enter(); err != nil {
		return nil, err
	}
	r.pos++
	out := []T{}
	r.ws()
	if r.at() != ']' {
		for {
			r.ws()
			v, err := elem(r)
			if err != nil {
				return nil, err
			}
			out = append(out, *v)
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

const goDupKeyRefuse = `			if _, again := out[k]; again {
				return nil, r.refuse("duplicate_key")
			}
`

const goSkipDupKey = `				if seen[k] {
					return r.refuse("duplicate_key")
				}
				seen[k] = true
`

const goTimestamp = `
func digits(s string, i, n int) bool {
	if i+n > len(s) {
		return false
	}
	for k := 0; k < n; k++ {
		if s[i+k] < '0' || s[i+k] > '9' {
			return false
		}
	}
	return true
}

func datePart(s string) bool {
	return len(s) >= 19 && digits(s, 0, 4) && s[4] == '-' && digits(s, 5, 2) && s[7] == '-' &&
		digits(s, 8, 2) && digits(s, 11, 2) && s[13] == ':' && digits(s, 14, 2) && s[16] == ':' && digits(s, 17, 2)
}

// [DEF-G1] rfc3339-wide: what a reader accepts. Any fraction of one to nine
// digits or none, either case of the separators, and a numeric offset.
func lexicalTimestamp(s string) bool {
	if !datePart(s) || (s[10] != 'T' && s[10] != 't') {
		return false
	}
	i := 19
	if i < len(s) && s[i] == '.' {
		i++
		n := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			n++
		}
		if n < 1 || n > 9 {
			return false
		}
	}
	if i >= len(s) {
		return false
	}
	if s[i] == 'Z' || s[i] == 'z' {
		return i+1 == len(s)
	}
	if s[i] != '+' && s[i] != '-' {
		return false
	}
	return len(s) == i+6 && digits(s, i+1, 2) && s[i+3] == ':' && digits(s, i+4, 2)
}

// [DEF-G2] rfc3339-micros: what a writer emits. Exactly six fractional digits,
// upper-case separators, UTC.
func MicrosTimestamp(s string) bool {
	return len(s) == 27 && normalizedTimestamp(s) == s
}

func (r *reader) timestamp() (string, error) {
	at := r.pos
	s, err := r.str()
	if err != nil {
		return "", err
	}
	if !WideTimestamp(s) {
		r.pos = at
		return "", r.refuse("bad_timestamp")
	}
	return s, nil
}
`

func goDecoder(b *strings.Builder, s *Definition) {
	for _, st := range s.Structs {
		goStructDecoder(b, s, st)
	}
	if s.Document != "" {
		fmt.Fprintf(b, "\nfunc Decode(in []byte) (*%s, error) {\n", s.Document)
		b.WriteString("\tr := &reader{buf: in}\n\tr.ws()\n")
		fmt.Fprintf(b, "\tv, err := r.decode%s()\n\tif err != nil {\n\t\treturn nil, err\n\t}\n", s.Document)
		b.WriteString("\tr.ws()\n\tif r.pos < len(r.buf) {\n\t\treturn nil, r.refuse(\"trailing_bytes\")\n\t}\n")
		if s.Vocab != nil {
			b.WriteString("\tif err := r.derive(v); err != nil {\n\t\treturn nil, err\n\t}\n")
		}
		b.WriteString("\treturn v, nil\n}\n")
	}
	goDerive(b, s)
}

func goStructDecoder(b *strings.Builder, s *Definition, st Struct) {
	fmt.Fprintf(b, "\nfunc (r *reader) decode%s() (*%s, error) {\n", st.Name, st.Name)
	b.WriteString("\tif r.at() != '{' {\n\t\treturn nil, r.refuse(\"wrong_type\")\n\t}\n")
	b.WriteString("\tif err := r.enter(); err != nil {\n\t\treturn nil, err\n\t}\n\tr.pos++\n")
	fmt.Fprintf(b, "\tv := &%s{}\n", st.Name)
	if len(st.Fields) > 0 {
		b.WriteString("\tvar seen uint32\n")
	}
	b.WriteString("\tr.ws()\n")
	b.WriteString("\tif r.at() != '}' {\n\t\tfor {\n\t\t\tr.ws()\n")
	b.WriteString("\t\t\tif r.at() != '\"' {\n\t\t\t\treturn nil, r.refuse(\"malformed\")\n\t\t\t}\n")
	b.WriteString("\t\t\tkey, err := r.str()\n\t\t\tif err != nil {\n\t\t\t\treturn nil, err\n\t\t\t}\n")
	b.WriteString("\t\t\tr.ws()\n\t\t\tif r.at() != ':' {\n\t\t\t\treturn nil, r.refuse(\"malformed\")\n\t\t\t}\n")
	b.WriteString("\t\t\tr.pos++\n\t\t\tr.ws()\n\t\t\tswitch key {\n")
	for i, f := range st.Fields {
		fmt.Fprintf(b, "\t\t\tcase %q:\n", f.Name)
		fmt.Fprintf(b, "\t\t\t\tif seen&%d != 0 {\n\t\t\t\t\treturn nil, r.refuse(\"duplicate_field\")\n\t\t\t\t}\n", 1<<i)
		fmt.Fprintf(b, "\t\t\t\tseen |= %d\n", 1<<i)
		fmt.Fprintf(b, "\t\t\t\t%s\n", goRead(s, f))
	}
	b.WriteString("\t\t\tdefault:\n")
	if st.RefuseUnknown() {
		b.WriteString("\t\t\t\treturn nil, r.refuse(\"unknown_field\")\n")
	} else if st.PreservesUnknown() {
		b.WriteString("\t\t\t\tif v.Extras == nil { v.Extras = map[string]Raw{} }\n")
		if s.Encoding.RefuseDuplicateKeys() {
			b.WriteString("\t\t\t\tif _, exists := v.Extras[key]; exists { return nil, r.refuse(\"duplicate_key\") }\n")
		}
		b.WriteString("\t\t\t\tx, err := r.rawValue(); if err != nil { return nil, err }; v.Extras[key] = x\n")
	} else {
		b.WriteString("\t\t\t\tif err := r.skipValue(); err != nil {\n\t\t\t\t\treturn nil, err\n\t\t\t\t}\n")
	}
	b.WriteString("\t\t\t}\n\t\t\tr.ws()\n\t\t\tif r.at() != ',' {\n\t\t\t\tbreak\n\t\t\t}\n\t\t\tr.pos++\n\t\t}\n\t}\n")
	b.WriteString("\tif r.at() != '}' {\n\t\treturn nil, r.refuse(\"malformed\")\n\t}\n\tr.pos++\n\tr.depth--\n")
	if req := requiredMask(st); req != 0 {
		fmt.Fprintf(b, "\tif seen&%d != %d {\n\t\treturn nil, r.refuse(\"missing_field\")\n\t}\n", req, req)
	}
	emitEqualities(b, st, "go", false)
	emitEnumChecks(b, st, "go", false)
	if len(s.Services) > 0 && st.Name == s.Document && s.Vocab != nil {
		b.WriteString("if err:=r.derive(v);err!=nil{return nil,err}\n")
	}
	b.WriteString("\treturn v, nil\n}\n")
}

func requiredMask(st Struct) uint32 {
	var m uint32
	for i, f := range st.Fields {
		if f.Omit == "never" {
			m |= 1 << i
		}
	}
	return m
}

func goRead(s *Definition, f Field) string {
	e := "v." + exported(f.Ident("go"))
	get := func(call, assign string) string {
		return "x, err := " + call + "\n\t\t\t\tif err != nil {\n\t\t\t\t\treturn nil, err\n\t\t\t\t}\n\t\t\t\t" + assign
	}
	switch f.Type {
	case "binary":
		return get("r.binary()", e+" = x")
	case "string":
		if f.Grammar.Named() {
			return get("r.timestamp()", e+" = x")
		}
		if enumAbsent(f) {
			return get("r.str()", e+" = &x")
		}
		return get("r.str()", e+" = x")
	case "i32":
		return get("r.integer(-2147483648, 2147483647)", e+" = int32(x)")
	case "i64":
		return get("r.integer(-9223372036854775808, 9223372036854775807)", e+" = x")
	case "bool":
		return get("r.boolean()", e+" = x")
	case "json":
		return get("r.rawValue()", e+" = x")
	case "list<string>":
		return get("r.strList()", e+" = x")
	case "map<string,json>":
		return get("r.rawMap()", e+" = x")
	case "list<json>":
		return get("r.rawList()", e+" = x")
	case "map<string,string>":
		return get("r.strMap()", e+" = x")
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return get("decodeList(r, (*reader).decode"+elem+")", e+" = x")
	}
	if f.Omit == "absent" {
		return get("r.decode"+f.Type+"()", e+" = x")
	}
	return get("r.decode"+f.Type+"()", e+" = *x")
}

func goDerive(b *strings.Builder, s *Definition) {
	v := s.Vocab
	if v == nil {
		return
	}
	fmt.Fprintf(b, "\nvar %sTerms = []string{", v.Name)
	for i, t := range v.Terms {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(b, "%q", t.Name)
	}
	b.WriteString("}\n")
	fmt.Fprintf(b, "\nvar %sStripCritical = map[string]bool{\n", v.Name)
	for _, t := range v.Terms {
		if t.StripCritical {
			fmt.Fprintf(b, "\t%q: true,\n", t.Name)
		}
	}
	b.WriteString("}\n")
	fmt.Fprintf(b, "\nfunc (r *reader) derive(v *%s) error {\n", v.Of)
	fmt.Fprintf(b, "\tin := map[string]bool{}\n\tfor _, n := range v.%s {\n\t\tin[n] = true\n\t}\n", exported(v.Names))
	fmt.Fprintf(b, "\tknown := map[string]bool{}\n\tfor _, n := range %sTerms {\n\t\tknown[n] = true\n\t}\n", v.Name)
	fmt.Fprintf(b, "\tkept := v.%s[:0]\n", exported(v.Critical))
	fmt.Fprintf(b, "\tfor _, n := range v.%s {\n", exported(v.Critical))
	fmt.Fprintf(b, "\t\tswitch {\n\t\tcase %sStripCritical[n]:\n\t\t\tcontinue\n", v.Name)
	b.WriteString("\t\tcase !known[n]:\n\t\t\treturn r.refuse(\"unknown_critical\")\n")
	b.WriteString("\t\tcase !in[n]:\n\t\t\treturn r.refuse(\"not_a_subset\")\n\t\t}\n\t\tkept = append(kept, n)\n\t}\n")
	fmt.Fprintf(b, "\tv.%s = kept\n", exported(v.Critical))
	for _, t := range v.Terms {
		fmt.Fprintf(b, "\tif (%s) != in[%q] {\n\t\treturn r.refuse(\"content_mismatch\")\n\t}\n", goTest(s, t), t.Name)
	}
	b.WriteString("\treturn nil\n}\n")
	if v.UsesMember() {
		b.WriteString(goMember)
	}
}

// goTest is the term's predicate as Go, over a chain of field accesses.
func goTest(s *Definition, t Term) string {
	if t.Always() {
		return "true"
	}
	e := "v"
	for _, f := range t.Path {
		e += "." + exported(f.Ident("go"))
	}
	switch {
	case t.Member != "":
		return fmt.Sprintf("Member(%s, %q)", e, t.Member)
	case t.Membership():
		parts := make([]string, len(t.Is))
		for i, w := range t.Is {
			parts[i] = fmt.Sprintf("%s == %q", e, w)
		}
		return strings.Join(parts, " || ")
	}
	return goPresent(s, t.Last(), e)
}

const goMember = `
// [DEF-A8] Whether an opaque value is an object naming this member with
// something other than null. The key is decoded, so two spellings of one name
// are one name; the value is neither decoded nor judged.
func Member(v Raw, name string) bool {
	r := &reader{buf: []byte(v)}
	r.ws()
	if r.at() != '{' {
		return false
	}
	r.pos++
	r.ws()
	for r.at() == '"' {
		k, err := r.str()
		if err != nil {
			return false
		}
		r.ws()
		r.pos++
		r.ws()
		if k == name {
			return r.at() != 'n'
		}
		if r.skipValue() != nil {
			return false
		}
		r.ws()
		if r.at() != ',' {
			return false
		}
		r.pos++
		r.ws()
	}
	return false
}
`

func genGo(s *Definition) string {
	s = enumCarriers(s)
	if s.NoIPC {
		return genInterfaceOnly(s, "go")
	}
	s = serviceTypes(s)
	var b strings.Builder
	esc := goEscMinimal
	if s.Encoding.EscapeNonASCII() {
		esc = goEscASCII
	}
	prelude := goCommon + esc
	if s.StringMapDocument() {
		prelude += goStrMap
	}
	if s.HasRepeated() {
		prelude += goEncList
	}
	b.WriteString(strings.NewReplacer("@INDENT@", strconv.Itoa(s.Encoding.Indent)).Replace(prelude))
	b.WriteString(importPrelude(s, "go"))
	goVocabulary(&b, s)
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "\ntype %s struct {\n", st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "\t%s %s\n", exported(f.Ident("go")), goType(s, f))
		}
		if st.PreservesUnknown() {
			b.WriteString("\tExtras map[string]Raw\n")
		}
		b.WriteString("}\n")
	}
	for _, st := range s.Structs {
		if !s.Envelope(st.Name) {
			goEncoder(&b, s, st, false)
		}
	}
	tail := ""
	if s.Encoding.TrailingNewline() {
		tail = ", '\\n'"
	}
	if s.Document != "" {
		if len(s.Imports) > 0 {
			fmt.Fprintf(&b, "\nfunc Encode(v *%s) []byte {return Encode%s(v)}\n", s.Document, s.Document)
		} else {
			fmt.Fprintf(&b, "\nfunc Encode(v *%s) []byte {\n\treturn append(enc%s(nil, v, 0)%s)\n}\n", s.Document, s.Document, tail)
		}
	}
	dup, skipSeen, skipKey, skipDup := "", "", "_, err := ", ""
	if s.Encoding.RefuseDuplicateKeys() {
		dup, skipSeen, skipKey, skipDup = goDupKeyRefuse, "\tseen := map[string]bool{}\n", "k, err := ", goSkipDupKey
	}
	decode := goDecodeCommon
	if s.HasStringMap() {
		decode += goStrMapDecode
	}
	if s.HasRepeated() {
		decode += goDecodeList
	}
	b.WriteString(strings.NewReplacer(
		"@DEPTH@", strconv.Itoa(s.Encoding.DepthLimit),
		"@DUPKEY@", dup,
		"@SKIPSEEN@", skipSeen,
		"@SKIPKEY@", skipKey,
		"@SKIPDUP@", skipDup,
	).Replace(decode))
	if s.Timestamps() {
		b.WriteString(goTimestamp)
		b.WriteString(goTimestampNormalize)
	}
	if s.PreservesUnknown() {
		b.WriteString(goPreserve)
	}
	goDecoder(&b, s)
	goProtocol(&b, s)
	if hasBinary(s) {
		b.WriteString(goBinary)
	}
	goService(&b, s)
	pretty, err := format.Source([]byte(b.String()))
	if err != nil {
		fail(fmt.Errorf("the Go backend emitted something gofmt refuses: %w", err))
	}
	return string(pretty)
}

func goVocabulary(b *strings.Builder, s *Definition) {
	for _, en := range s.Enums {
		fmt.Fprintf(b, "\nvar %sNames = []string{", en.Name)
		for i, m := range en.Members {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(b, "%q", m.Name)
		}
		b.WriteString("}\n")
		for _, m := range en.Members {
			fmt.Fprintf(b, "\nconst %s%s = %q\n", en.Name, exported(m.Name), m.Name)
		}
		fmt.Fprintf(b, "\nconst %s%s = %q\n", en.Name, enumPolicyName(en, "go"), en.Ann["unknown"])
		for _, key := range en.MemberAnn() {
			fmt.Fprintf(b, "\nvar %s%s = map[string]string{\n", en.Name, exported(key))
			for _, m := range en.Members {
				if v, ok := m.Ann[key]; ok {
					fmt.Fprintf(b, "\t%q: %q,\n", m.Name, v)
				}
			}
			b.WriteString("}\n")
		}
	}
	for _, c := range s.Consts {
		if c.Type == "list<i32>" {
			fmt.Fprintf(b, "\nvar %s = []int32{", exported(c.Name))
			for i, n := range c.Ints {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%d", n)
			}
		} else {
			fmt.Fprintf(b, "\nvar %s = []string{", exported(c.Name))
			for i, v := range c.Strings {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(b, "%q", v)
			}
		}
		b.WriteString("}\n")
	}
}

func goEncoder(b *strings.Builder, s *Definition, st Struct, flat bool) {
	p := plan(st)
	if flat {
		fmt.Fprintf(b, "\nfunc encWire%s(out []byte, v *%s) []byte {\n", st.Name, st.Name)
	} else {
		fmt.Fprintf(b, "\nfunc enc%s(out []byte, v *%s, depth int) []byte {\n", st.Name, st.Name)
	}
	emitEqualities(b, st, "go", true)
	emitEnumChecks(b, st, "go", true)
	b.WriteString("\tout = append(out, '{')\n")
	if p.flag {
		b.WriteString("\tfirst := true\n")
	}
	for i, f := range st.Fields {
		e := "v." + exported(f.Ident("go"))
		ind := "\t"
		if f.Omit != "never" {
			fmt.Fprintf(b, "\tif %s {\n", goPresent(s, f, e))
			ind = "\t\t"
		}
		switch p.before[i] {
		case "always":
			fmt.Fprintf(b, "%sout = append(out, ',')\n", ind)
		case "flag":
			fmt.Fprintf(b, "%sif !first {\n%s\tout = append(out, ',')\n%s}\n", ind, ind, ind)
		}
		if p.clears(i) {
			fmt.Fprintf(b, "%sfirst = false\n", ind)
		}
		if flat {
			fmt.Fprintf(b, "%sout = esc(out, %q)\n%sout = append(out, ':')\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, goValueFlat(s, f, e))
		} else {
			fmt.Fprintf(b, "%sout = append(out, '\\n')\n%sout = pad(out, depth+1)\n", ind, ind)
			fmt.Fprintf(b, "%sout = esc(out, %q)\n%sout = append(out, ':', ' ')\n", ind, f.Name, ind)
			fmt.Fprintf(b, "%s%s\n", ind, goValue(s, f, e))
		}
		if f.Omit != "never" {
			b.WriteString("\t}\n")
		}
	}
	if st.PreservesUnknown() {
		fmt.Fprintf(b, "\tout, first = extraFields(out, v.Extras, []string{%s}, depth, first)\n", quotedFieldNames(st))
	}
	if !flat {
		switch {
		case p.closeAlways:
			b.WriteString("\tout = append(out, '\\n')\n\tout = pad(out, depth)\n")
		case len(st.Fields) > 0 || st.PreservesUnknown():
			b.WriteString("\tif !first {\n\t\tout = append(out, '\\n')\n\t\tout = pad(out, depth)\n\t}\n")
		}
	}
	b.WriteString("\treturn append(out, '}')\n}\n")
}

func goType(s *Definition, f Field) string {
	if enumAbsent(f) {
		return "*string"
	}
	switch f.Type {
	case "binary":
		return "[]byte"
	case "string":
		return "string"
	case "i32":
		return "int32"
	case "i64":
		return "int64"
	case "bool":
		return "bool"
	case "json":
		return "Raw"
	case "list<string>":
		return "[]string"
	case "map<string,json>":
		return "map[string]Raw"
	case "list<json>":
		return "[]Raw"
	case "map<string,string>":
		return "map[string]string"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "[]" + elem
	}
	if f.Omit == "absent" {
		return "*" + f.Type
	}
	return f.Type
}

func goPresent(s *Definition, f Field, e string) string {
	if f.Omit == "absent" {
		if s.IsStruct(f.Type) || f.Type == "binary" || enumAbsent(f) {
			return e + " != nil"
		}
		return e + ` != ""`
	}
	switch f.Type {
	case "string", "json":
		return e + ` != ""`
	case "i32", "i64":
		return e + " != 0"
	case "bool":
		return e
	}
	return "len(" + e + ") != 0"
}

func goValue(s *Definition, f Field, e string) string {
	if enumAbsent(f) {
		return "out = esc(out, *" + e + ")"
	}
	if f.Type == "json" && f.Ann["service_raw"] == "true" {
		return "out = append(out, " + e + "...)"
	}
	if f.Grammar.Named() {
		return "out = esc(out, writeTimestamp(" + e + "))"
	}
	switch f.Type {
	case "binary":
		return "out = esc(out, encodeBinary(" + e + "))"
	case "string":
		return "out = esc(out, " + e + ")"
	case "i32":
		return "out = num(out, int64(" + e + "))"
	case "i64":
		return "out = num(out, " + e + ")"
	case "bool":
		return "if " + e + " {\n\t\t\tout = append(out, 't', 'r', 'u', 'e')\n\t\t} else {\n\t\t\tout = append(out, 'f', 'a', 'l', 's', 'e')\n\t\t}"
	case "json":
		return "out = raw(out, " + e + ", depth+1)"
	case "list<string>":
		return "out = strs(out, " + e + ", depth+1)"
	case "map<string,json>":
		return "out = rawmap(out, " + e + ", depth+1)"
	case "map<string,string>":
		return "out = strmap(out, " + e + ", depth+1)"
	}
	if elem := s.Repeated(f.Type); elem != "" {
		return "out = encList(out, " + e + ", depth+1, enc" + elem + ")"
	}
	if f.Omit == "absent" {
		return "out = enc" + f.Type + "(out, " + e + ", depth+1)"
	}
	return "out = enc" + f.Type + "(out, &" + e + ", depth+1)"
}

func exported(n string) string {
	parts := strings.Split(n, "_")
	for i, p := range parts {
		if p != "" {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "")
}

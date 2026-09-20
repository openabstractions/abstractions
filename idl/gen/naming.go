package main

import (
	"strings"
	"unicode"
)

// Contract identifiers are spelled by whoever wrote the contract: methods in
// PascalCase, fields in snake_case, the odd camelCase constant. A language
// binding respells each one in the convention of the language it is emitted
// into. The wire keeps the contract spelling; only the identifier moves.
//
// words splits an identifier at underscores, hyphens, dots and case changes.
// An acronym stays one word: HTTPServer is HTTP, Server; ReadV2 is Read, V2.
func words(name string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	rs := []rune(name)
	for i, r := range rs {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) > 0 && unicode.IsUpper(r) {
			prev := cur[len(cur)-1]
			nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
			if unicode.IsLower(prev) || unicode.IsDigit(prev) && (!allUpper(cur) || nextLower) || unicode.IsUpper(prev) && nextLower {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func allUpper(rs []rune) bool {
	for _, r := range rs {
		if unicode.IsLower(r) {
			return false
		}
	}
	return true
}

// snakeCase is PEP 8's spelling of functions, methods, parameters and fields.
func snakeCase(name string) string {
	ws := words(name)
	for i, w := range ws {
		ws[i] = strings.ToLower(w)
	}
	return strings.Join(ws, "_")
}

// upperSnake is PEP 8's spelling of constants and enumeration members.
func upperSnake(name string) string { return strings.ToUpper(snakeCase(name)) }

// pascalCase keeps a name that is already CapWords, acronyms included, as PEP 8
// asks (HTTPServerError); any other spelling is joined word by word.
func pascalCase(name string) string {
	if name != "" && unicode.IsUpper([]rune(name)[0]) && !strings.ContainsAny(name, "_-.") {
		return name
	}
	ws := words(name)
	for i, w := range ws {
		ws[i] = capitalize(strings.ToLower(w))
	}
	return strings.Join(ws, "")
}

// jsPascal is a JavaScript member of a frozen vocabulary object: each word is
// capitalized once, so not_granted is NotGranted.
func jsPascal(name string) string {
	ws := words(name)
	for i, w := range ws {
		ws[i] = capitalize(strings.ToLower(w))
	}
	return strings.Join(ws, "")
}

// camelCase is the JavaScript spelling of functions, methods, parameters and
// fields. An acronym is a word, as the common style guides spell it: HTTPServer
// is httpServer, GetHTTPStatus is getHttpStatus.
func camelCase(name string) string {
	ws := words(name)
	for i, w := range ws {
		w = strings.ToLower(w)
		if i > 0 {
			w = capitalize(w)
		}
		ws[i] = w
	}
	return strings.Join(ws, "")
}

func capitalize(w string) string {
	if w == "" {
		return w
	}
	rs := []rune(w)
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

// pyKeywordSafe applies PEP 8's rule for a name that collides with a keyword:
// one trailing underscore (class_). self is treated the same way, because a
// method or dataclass initializer already binds it.
func pyKeywordSafe(n string) string {
	if namespaceKeyword("python", n) || n == "self" {
		return n + "_"
	}
	return n
}

// jsReservedParameter is a word a strict-mode module cannot bind as a
// parameter. JavaScript has no trailing-underscore convention, so a contract
// argument spelled this way needs a javascript.name annotation instead.
func jsReservedParameter(n string) bool {
	return namespaceKeyword("javascript", n) || n == "arguments" || n == "eval"
}

// pyName and jsName are the identifiers a contract field or argument takes in
// each language, after any explicit per-language override.
func pyName(name string) string { return pyKeywordSafe(snakeCase(name)) }
func jsName(name string) string { return camelCase(name) }

// Every contract identifier reaches a language through these functions. The
// wire keeps the definition's spelling; only the name a program types changes,
// and a per-language override annotation (rust.name, cpp.name, go.name) is used
// verbatim before any of them runs.

// identWords splits a definition identifier into words. Underscores separate
// words; so does a lower-to-upper step (policyRevision), and an upper letter
// that ends an acronym (OAServiceFrame is OA, Service, Frame). Digits stay with
// the word before them, so sha256 and v1 are one word each.
func identWords(n string) []string {
	var words []string
	for _, part := range strings.Split(n, "_") {
		rs := []rune(part)
		start := 0
		for i := 1; i < len(rs); i++ {
			prev, cur := rs[i-1], rs[i]
			next := rune(0)
			if i+1 < len(rs) {
				next = rs[i+1]
			}
			lowerToUpper := (unicode.IsLower(prev) || unicode.IsDigit(prev)) && unicode.IsUpper(cur)
			acronymEnd := unicode.IsUpper(prev) && unicode.IsUpper(cur) && unicode.IsLower(next) &&
				!(next == 's' && (i+2 == len(rs) || !unicode.IsLower(rs[i+2])))
			if lowerToUpper || acronymEnd {
				words = append(words, string(rs[start:i]))
				start = i
			}
		}
		if start < len(rs) {
			words = append(words, string(rs[start:]))
		}
	}
	return words
}

func capitalized(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + strings.ToLower(w[1:])
}

// snakeName is Rust's and C++'s function, method, parameter and field form.
func snakeName(n string) string {
	words := identWords(n)
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, "_")
}

// screamingName is Rust's constant form.
func screamingName(n string) string {
	words := identWords(n)
	for i, w := range words {
		words[i] = strings.ToUpper(w)
	}
	return strings.Join(words, "_")
}

// upperCamelName is RFC 430's type and variant form, where an acronym is one
// word (Uuid, not UUID), and the C++ convention's type and enumerator form.
func upperCamelName(n string) string {
	words := identWords(n)
	for i, w := range words {
		words[i] = capitalized(w)
	}
	return strings.Join(words, "")
}

// goInitialisms are the words Go spells in one case (Effective Go, and the
// staticcheck ST1003 list), plus the digests and platform words the contracts use.
var goInitialisms = map[string]bool{}

func init() {
	for _, w := range strings.Fields("acl api ascii cpu css dns eof gid gpu guid html http https id ip ipc json oa os pid qps ram rpc sha1 sha256 sha512 sla smtp sql ssh tcp tls ttl udp ui uid uri url utc utf8 uuid vm xml xmpp xsrf xss") {
		goInitialisms[w] = true
	}
}

func goWord(w string) string {
	l := strings.ToLower(w)
	if goInitialisms[l] {
		return strings.ToUpper(l)
	}
	if strings.HasSuffix(l, "s") && goInitialisms[strings.TrimSuffix(l, "s")] {
		return strings.ToUpper(strings.TrimSuffix(l, "s")) + "s"
	}
	return capitalized(w)
}

// goName is Go's exported form: MixedCaps with initialisms in one case.
func goName(n string) string {
	var b strings.Builder
	for _, w := range identWords(n) {
		b.WriteString(goWord(w))
	}
	return b.String()
}

// goLocalName is Go's unexported form, used for parameters and for types no
// other package needs: the first word lower case, including an initialism.
func goLocalName(n string) string {
	words := identWords(n)
	var b strings.Builder
	for i, w := range words {
		if i == 0 {
			b.WriteString(strings.ToLower(w))
			continue
		}
		b.WriteString(goWord(w))
	}
	return b.String()
}

// goParam names a Go parameter from the contract. A keyword or a name the
// generated method body already uses gains a trailing underscore.
func goParam(f Field, reserved map[string]bool) string {
	n := goLocalName(f.Name)
	if goIdentKeyword(n) || reserved[n] {
		n += "_"
	}
	return n
}

func goIdentKeyword(n string) bool {
	if namespaceKeyword("go", n) {
		return true
	}
	for _, w := range strings.Fields("bool byte complex64 complex128 error float32 float64 int int8 int16 int32 int64 rune string uint uint8 uint16 uint32 uint64 uintptr any comparable true false iota nil append cap clear close complex copy delete imag len make max min new panic print println real recover") {
		if n == w {
			return true
		}
	}
	return false
}

// cppIdent is the C++ snake_case form. A keyword gains a trailing underscore.
func cppIdent(n string) string {
	s := snakeName(n)
	if namespaceKeyword("cpp", s) {
		s += "_"
	}
	return s
}

// cppConstant is the project's k-prefixed PascalCase constant form.
func cppConstant(parts ...string) string {
	var b strings.Builder
	b.WriteString("k")
	for _, p := range parts {
		b.WriteString(upperCamelName(p))
	}
	return b.String()
}

// rustIdent is the Rust snake_case form for methods. A keyword gains a
// trailing underscore; fields and arguments keep the rust.name requirement.
func rustIdent(n string) string {
	s := snakeName(n)
	if namespaceKeyword("rust", s) {
		s += "_"
	}
	return s
}

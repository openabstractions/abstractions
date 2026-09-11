package main

import (
	"fmt"
	"strings"
)

const utf8BOM = "\xef\xbb\xbf"

type token struct {
	kind string
	text string
	line int
}

type lexer struct {
	src  string
	pos  int
	line int
	toks []token
}

func lex(src string) ([]token, error) {
	l := &lexer{src: strings.TrimPrefix(src, utf8BOM), line: 1}
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '\n':
			l.line++
			l.pos++
		case c == ' ' || c == '\t' || c == '\r':
			l.pos++
		case l.starts("//") || l.starts("#"):
			for l.pos < len(l.src) && l.src[l.pos] != '\n' {
				l.pos++
			}
		case l.starts("/*"):
			end := strings.Index(l.src[l.pos+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("line %d: unterminated block comment", l.line)
			}
			l.line += strings.Count(l.src[l.pos:l.pos+end+4], "\n")
			l.pos += end + 4
		case c == '"':
			s, err := l.quoted()
			if err != nil {
				return nil, err
			}
			l.emit("string", s)
		case isIdentStart(c):
			start := l.pos
			for l.pos < len(l.src) && isIdent(l.src[l.pos]) {
				l.pos++
			}
			l.emit("ident", l.src[start:l.pos])
		case c >= '0' && c <= '9' || c == '-':
			start := l.pos
			l.pos++
			for l.pos < len(l.src) && l.src[l.pos] >= '0' && l.src[l.pos] <= '9' {
				l.pos++
			}
			l.emit("int", l.src[start:l.pos])
		case strings.IndexByte("{}()<>,;:=[]*", c) >= 0:
			l.pos++
			l.emit("punct", string(c))
		default:
			return nil, fmt.Errorf("line %d: unexpected character %q", l.line, string(c))
		}
	}
	l.emit("eof", "")
	return l.toks, nil
}

func (l *lexer) starts(s string) bool { return strings.HasPrefix(l.src[l.pos:], s) }

func (l *lexer) emit(kind, text string) {
	l.toks = append(l.toks, token{kind: kind, text: text, line: l.line})
}

func (l *lexer) quoted() (string, error) {
	l.pos++
	start := l.pos
	for l.pos < len(l.src) && l.src[l.pos] != '"' {
		if l.src[l.pos] == '\n' {
			return "", fmt.Errorf("line %d: unterminated string", l.line)
		}
		l.pos++
	}
	if l.pos >= len(l.src) {
		return "", fmt.Errorf("line %d: unterminated string", l.line)
	}
	s := l.src[start:l.pos]
	l.pos++
	return s, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isIdent(c byte) bool { return isIdentStart(c) || c >= '0' && c <= '9' || c == '.' }

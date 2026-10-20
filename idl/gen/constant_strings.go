package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Constant strings keep their bytes across each language's literal grammar.
func constantString(value, language string) string {
	if language == "go" || language == "python" {
		return strconv.Quote(value)
	}
	if language == "javascript" {
		b, _ := json.Marshal(value)
		return string(b)
	}
	var b strings.Builder
	b.WriteByte('"')
	if language == "cpp" {
		for _, v := range []byte(value) {
			switch v {
			case '\\', '"':
				b.WriteByte('\\')
				b.WriteByte(v)
			default:
				if v >= 32 && v < 127 {
					b.WriteByte(v)
				} else {
					fmt.Fprintf(&b, "\\%03o", v)
				}
			}
		}
	} else if language == "rust" {
		for _, v := range value {
			switch v {
			case '\\', '"':
				b.WriteByte('\\')
				b.WriteRune(v)
			default:
				if unicode.IsControl(v) {
					fmt.Fprintf(&b, "\\u{%x}", v)
				} else {
					b.WriteRune(v)
				}
			}
		}
	} else {
		panic("constant string language")
	}
	b.WriteByte('"')
	return b.String()
}

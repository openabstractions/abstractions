package main

import (
	"fmt"
	"strings"
)

// scalarAbsent is the opt-in presence carried by optional scalar fields.
func scalarAbsent(f Field) bool {
	return f.Omit == "absent" && (f.Type == "i32" || f.Type == "i64" || f.Type == "bool")
}

// Dynamic encoders validate a present scalar before constraints or writing.
func emitScalarPresenceTypes(b *strings.Builder, st Struct, lang string) {
	for _, f := range st.Fields {
		if !scalarAbsent(f) {
			continue
		}
		e := "v." + f.Ident(lang)
		switch lang {
		case "python":
			typ := "int"
			if f.Type == "bool" {
				typ = "bool"
			}
			fmt.Fprintf(b, "    if %s is not None and type(%s) is not %s: raise Refusal(\"wrong_type\", 0)\n", e, e, typ)
			if f.Type == "i32" || f.Type == "i64" {
				lo, hi := "-9223372036854775808", "9223372036854775807"
				if f.Type == "i32" {
					lo, hi = "-2147483648", "2147483647"
				}
				fmt.Fprintf(b, "    if %s is not None and not (%s <= %s <= %s): raise Refusal(\"number_spelling\", 0)\n", e, lo, e, hi)
			}
		case "javascript":
			typ := "bigint"
			if f.Type == "bool" {
				typ = "boolean"
			}
			cond := "typeof " + e + " !== " + fmt.Sprintf("%q", typ)
			if f.Type == "i32" {
				cond = "typeof " + e + " !== \"number\" || !Number.isInteger(" + e + ")"
			}
			fmt.Fprintf(b, "  if (%s !== undefined && (%s)) throw new Refusal(\"wrong_type\", 0);\n", e, cond)
			if f.Type == "i32" || f.Type == "i64" {
				lo, hi := "-9223372036854775808n", "9223372036854775807n"
				if f.Type == "i32" {
					lo, hi = "-2147483648", "2147483647"
				}
				fmt.Fprintf(b, "  if (%s !== undefined && %s !== null && (%s < %s || %s > %s)) throw new Refusal(\"number_spelling\", 0);\n", e, e, e, lo, e, hi)
			}
		}
	}
}

// A scalar's JavaScript absence is undefined; records keep their null idiom.
func jsScalarPresenceChecks(s *Definition, body string) string {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if scalarAbsent(f) {
				return jsMustReplace(body,
					`if (omit === "absent" && item === null) continue;`,
					`if (omit === "absent" && (["i32","i64","bool"].includes(type) ? item === undefined : item === null)) continue;`)
			}
		}
	}
	return body
}

package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Equality constrains a supplied integer; it never supplies a missing value.
func (p *parser) validateEqualities() error {
	fields := []Field{}
	for _, st := range p.def.Structs {
		fields = append(fields, st.Fields...)
	}
	for _, svc := range p.def.Services {
		for _, m := range svc.Methods {
			fields = append(fields, m.Args...)
		}
	}
	for _, f := range fields {
		value, has := f.Ann["equals"]
		word, hasWord := f.Ann["equals_refusal"]
		if !has && !hasWord {
			continue
		}
		if !has || !hasWord || word == "" {
			return fmt.Errorf("field %s requires both equals and equals_refusal", f.Name)
		}
		if f.Omit != "never" || (f.Type != "i32" && f.Type != "i64") {
			return fmt.Errorf("field %s: equals requires a required i32 or i64", f.Name)
		}
		bits := 64
		if f.Type == "i32" {
			bits = 32
		}
		n, e := strconv.ParseInt(value, 10, bits)
		if e != nil || strconv.FormatInt(n, 10) != value {
			return fmt.Errorf("field %s: equals is a canonical in-range %s decimal", f.Name, f.Type)
		}
		found := false
		for _, r := range p.def.Refusals {
			if r.Word == word {
				found = r.Stage == "structure"
			}
		}
		if !found {
			return fmt.Errorf("field %s: equals_refusal %q must name a declared structure refusal", f.Name, word)
		}
	}
	return nil
}

func emitEqualities(b *strings.Builder, st Struct, lang string, encode bool) {
	for _, f := range st.Fields {
		value, has := f.Ann["equals"]
		if !has {
			continue
		}
		word := f.Ann["equals_refusal"]
		name := f.Ident(lang)
		if lang == "go" {
			name = exported(name)
		}
		e := "v." + name
		if lang == "javascript" && f.Type == "i64" {
			value += "n"
		}
		if lang == "cpp" && f.Type == "i64" {
			if value == "-9223372036854775808" {
				value = "INT64_MIN"
			} else {
				value += "LL"
			}
		}
		switch lang {
		case "go":
			action := fmt.Sprintf("return nil, r.refuse(%q)", word)
			if encode {
				action = fmt.Sprintf("panic(&Refusal{Word:%q,Offset:0})", word)
			}
			fmt.Fprintf(b, "\tif %s != %s { %s }\n", e, value, action)
		case "python":
			if encode {
				fmt.Fprintf(b, "    if type(%s) is not int: raise Refusal(\"wrong_type\", 0)\n", e)
			}
			action := fmt.Sprintf("r.refuse(%q)", word)
			if encode {
				action = fmt.Sprintf("Refusal(%q, 0)", word)
			}
			fmt.Fprintf(b, "    if %s != %s: raise %s\n", e, value, action)
		case "cpp":
			action := fmt.Sprintf("r.refuse(%q)", word)
			if encode {
				action = fmt.Sprintf("throw Refusal(%q,0)", word)
			}
			fmt.Fprintf(b, "    if (%s != %s) %s;\n", e, value, action)
		case "rust":
			action := fmt.Sprintf("return r.refuse(%q)", word)
			if encode {
				action = fmt.Sprintf("std::panic::panic_any(Refusal{word:%q,offset:0})", word)
			}
			fmt.Fprintf(b, "    if %s != %s { %s; }\n", e, value, action)
		case "javascript":
			action := fmt.Sprintf("r.refuse(%q)", word)
			if encode {
				action = fmt.Sprintf("new Refusal(%q,0)", word)
			}
			fmt.Fprintf(b, "  if (%s !== %s) throw %s;\n", e, value, action)
		}
	}
}

func (s *Definition) HasEqualities() bool {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if _, ok := f.Ann["equals"]; ok {
				return true
			}
		}
	}
	return false
}

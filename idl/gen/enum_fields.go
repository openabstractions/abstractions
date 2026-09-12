package main

import (
	"fmt"
	"strings"
)

// Lower only the private backend copy. The parsed definition and documentation
// retain the enum identity; carriers share the existing string codec.
func enumCarriers(s *Definition) *Definition {
	o := *s
	lower := func(f Field) Field {
		if en := s.Enum(f.Type); en != nil {
			f.EnumType = en
			f.Type = "string"
		}
		return f
	}
	o.Structs = append([]Struct(nil), s.Structs...)
	o.byName = map[string]*Struct{}
	for i := range o.Structs {
		o.Structs[i].Fields = append([]Field(nil), o.Structs[i].Fields...)
		for j, f := range o.Structs[i].Fields {
			o.Structs[i].Fields[j] = lower(f)
		}
		o.byName[o.Structs[i].Name] = &o.Structs[i]
	}
	o.Services = append([]Service(nil), s.Services...)
	for i := range o.Services {
		o.Services[i].Methods = append([]Method(nil), o.Services[i].Methods...)
		for j := range o.Services[i].Methods {
			m := &o.Services[i].Methods[j]
			m.Result = lower(m.Result)
			m.Args = append([]Field(nil), m.Args...)
			for k, f := range m.Args {
				m.Args[k] = lower(f)
			}
		}
	}
	return &o
}

func enumAbsent(f Field) bool { return f.EnumType != nil && f.Omit == "absent" }

func emitEnumChecks(b *strings.Builder, st Struct, lang string, encode bool) {
	for i, f := range st.Fields {
		en := f.EnumType
		if en == nil {
			continue
		}
		name := f.Ident(lang)
		if lang == "go" {
			name = exported(name)
		}
		e := "v." + name
		guard := ""
		value := e
		if enumAbsent(f) {
			switch lang {
			case "go":
				guard = e + " != nil"
				value = "*" + e
			case "cpp":
				guard = e + ".has_value()"
				value = "*" + e
			case "python":
				guard = e + " is not None"
			case "javascript":
				guard = e + " !== null"
			case "rust":
				guard = e + ".is_some()"
				value = e + ".as_ref().unwrap()"
			}
		}
		if f.Omit == "zero" {
			if encode {
				switch lang {
				case "go", "cpp":
					guard = e + ` != ""`
				case "python":
					guard = e + ` != ""`
				case "javascript":
					guard = e + ` !== ""`
				case "rust":
					guard = "!" + e + ".is_empty()"
				}
			} else {
				guard = fmt.Sprintf("(seen & %d) != 0", 1<<i)
				if lang == "javascript" {
					guard = fmt.Sprintf("(seen & %d) !== 0", 1<<i)
				}
			}
		}
		var terms []string
		for _, m := range en.Members {
			op := " != "
			if lang == "javascript" {
				op = " !== "
			}
			terms = append(terms, value+op+fmt.Sprintf("%q", m.Name))
		}
		join := " && "
		if lang == "python" {
			join = " and "
		}
		bad := strings.Join(terms, join)
		if bad == "" {
			bad = "true"
			if lang == "python" {
				bad = "True"
			}
		}
		if en.Ann["unknown"] == "grant" {
			bad = ""
		}
		action := ""
		wrong := ""
		switch lang {
		case "go":
			action = `return nil, r.refuse("bad_enum")`
			if encode {
				action = `panic(&Refusal{Word:"bad_enum",Offset:0})`
			}
		case "cpp":
			action = `r.refuse("bad_enum")`
			if encode {
				action = `throw Refusal("bad_enum",0)`
			}
		case "rust":
			action = `return r.refuse("bad_enum")`
			if encode {
				action = `std::panic::panic_any(Refusal{word:"bad_enum",offset:0})`
			}
		case "python":
			action = `raise r.refuse("bad_enum")`
			if encode {
				action = `raise Refusal("bad_enum",0)`
				wrong = "if type(" + value + ") is not str: raise Refusal(\"wrong_type\",0)"
			}
		case "javascript":
			action = `throw r.refuse("bad_enum")`
			if encode {
				action = `throw new Refusal("bad_enum",0)`
				wrong = "if (typeof " + value + " !== \"string\") throw new Refusal(\"wrong_type\",0);"
			}
		}
		if bad == "" && wrong == "" {
			continue
		}
		if lang == "python" {
			indent := "    "
			if guard != "" {
				fmt.Fprintf(b, "    if %s:\n", guard)
				indent += "    "
			}
			if wrong != "" {
				fmt.Fprintf(b, "%s%s\n", indent, wrong)
			}
			if bad != "" {
				fmt.Fprintf(b, "%sif %s: %s\n", indent, bad, action)
			}
			continue
		}
		if guard != "" {
			fmt.Fprintf(b, "    if (%s) {\n", guard)
		}
		if wrong != "" {
			fmt.Fprintf(b, "    %s\n", wrong)
		}
		if bad != "" {
			if lang == "go" || lang == "rust" {
				fmt.Fprintf(b, "    if %s { %s; }\n", bad, action)
			} else {
				fmt.Fprintf(b, "    if (%s) { %s; }\n", bad, action)
			}
		}
		if guard != "" {
			b.WriteString("    }\n")
		}
	}
}

func hasEnumFields(s *Definition) bool {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if f.EnumType != nil || s.Enum(f.Type) != nil {
				return true
			}
		}
	}
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			if m.Result.EnumType != nil || s.Enum(m.Result.Type) != nil {
				return true
			}
			for _, f := range m.Args {
				if f.EnumType != nil || s.Enum(f.Type) != nil {
					return true
				}
			}
		}
	}
	return false
}
func enumPolicyName(en Enum, lang string) string {
	if lang != "go" {
		if lang == "python" || lang == "rust" {
			return "UNKNOWN"
		}
		return "Unknown"
	}
	suffix := "Unknown"
	if lang == "python" || lang == "rust" {
		suffix = "UNKNOWN"
	}
	for {
		used := false
		for _, m := range en.Members {
			n := exported(m.Name)
			if lang == "python" || lang == "rust" {
				n = upper(m.Name)
			}
			if n == suffix {
				used = true
			}
		}
		if !used {
			return suffix
		}
		if lang == "python" || lang == "rust" {
			suffix += "_POLICY"
		} else {
			suffix += "Policy"
		}
	}
}

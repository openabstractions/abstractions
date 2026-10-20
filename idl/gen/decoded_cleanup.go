package main

import (
	"fmt"
	"strings"
)

// The parser already bounds the supported acyclic record graph. This IR keeps
// logical binary ownership separate from string/opaque-JSON carrier choices.
type decodedCleanup struct {
	Kind    string
	Fields  []decodedCleanupField
	Element *decodedCleanup
}
type decodedCleanupField struct {
	Field Field
	Value *decodedCleanup
}

func lowerDecodedCleanup(s *Definition, typ string, seen map[*Struct]bool) *decodedCleanup {
	if typ == "binary" {
		return &decodedCleanup{Kind: "binary"}
	}
	if elem := listElement(typ); elem != "" {
		if value := lowerDecodedCleanup(s, elem, seen); value != nil {
			return &decodedCleanup{Kind: "list", Element: value}
		}
		return nil
	}
	st, owner := s.reachedStruct(typ)
	if st == nil || seen[st] {
		return nil
	}
	seen[st] = true
	defer delete(seen, st)
	var fields []decodedCleanupField
	for _, field := range st.Fields {
		if value := lowerDecodedCleanup(owner, field.Type, seen); value != nil {
			fields = append(fields, decodedCleanupField{field, value})
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return &decodedCleanup{Kind: "record", Fields: fields}
}

func hasDecodedCleanup(s *Definition) bool {
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			if m.ResultPostcondition != nil && m.ResultPostcondition.Cleanup != nil {
				return true
			}
		}
	}
	return false
}

func emitDecodedCleanupPrimitive(b *strings.Builder, s *Definition, lang string) {
	if !hasDecodedCleanup(s) {
		return
	}
	switch lang {
	case "go":
		b.WriteString("\n// Keep the write in a separate compiler-visible call with an observable slice argument.\n//go:noinline\nfunc serviceClearDecodedBinary(value []byte){for i:=range value{value[i]=0}}\n")
	case "cpp":
		b.WriteString("\nnamespace detail {\ninline void clear_decoded_binary(std::vector<std::uint8_t>& value){volatile std::uint8_t* bytes=value.data();for(std::size_t i=0;i<value.size();++i)bytes[i]=0;}\n}\n")
	case "rust":
		b.WriteString("\nfn service_clear_decoded_binary(value: &mut [u8]) {\n    for byte in value {\n        // SAFETY: each reference names a live, exclusively borrowed byte in this decoder-owned allocation.\n        unsafe { ::std::ptr::write_volatile(byte, 0); }\n    }\n    ::std::sync::atomic::compiler_fence(::std::sync::atomic::Ordering::SeqCst);\n}\n")
	case "javascript":
		b.WriteString("\nfunction _serviceClearDecodedBinary(value) { value.fill(0); }\n")
	}
}

// Called solely for freshly decoded CLIENT results, never handler results or
// request/transport buffers. Python's decoded binary is immutable bytes.
func emitDecodedCleanup(b *strings.Builder, n *decodedCleanup, lang, value string) {
	if n == nil || lang == "python" {
		return
	}
	serial := 0
	var walk func(*decodedCleanup, string)
	walk = func(node *decodedCleanup, expr string) {
		switch node.Kind {
		case "binary":
			switch lang {
			case "go":
				fmt.Fprintf(b, "serviceClearDecodedBinary(%s);\n", expr)
			case "cpp":
				fmt.Fprintf(b, "detail::clear_decoded_binary(%s);\n", expr)
			case "rust":
				fmt.Fprintf(b, "service_clear_decoded_binary(%s.as_mut_slice());\n", expr)
			case "javascript":
				fmt.Fprintf(b, "_serviceClearDecodedBinary(%s);\n", expr)
			}
		case "list":
			local := fmt.Sprintf("clean_value%d", serial)
			serial++
			switch lang {
			case "go":
				fmt.Fprintf(b, "for _,%s:=range %s{\n", local, expr)
			case "cpp":
				fmt.Fprintf(b, "for(auto& %s:%s){\n", local, expr)
			case "rust":
				fmt.Fprintf(b, "for %s in %s.iter_mut(){\n", local, expr)
			case "javascript":
				fmt.Fprintf(b, "for(const %s of %s){\n", local, expr)
			}
			walk(node.Element, local)
			b.WriteString("}\n")
		case "record":
			for _, child := range node.Fields {
				name := child.Field.Ident(lang)
				if lang == "go" {
					name = exported(name)
				}
				field := expr + "." + name
				if child.Field.Omit == "absent" {
					switch lang {
					case "go":
						fmt.Fprintf(b, "if %s!=nil{\n", field)
					case "cpp":
						fmt.Fprintf(b, "if(%s){\n", field)
						field = "(*" + field + ")"
					case "rust":
						local := fmt.Sprintf("clean_value%d", serial)
						serial++
						fmt.Fprintf(b, "if let Some(%s)=%s.as_mut(){\n", local, field)
						field = local
					case "javascript":
						fmt.Fprintf(b, "if(%s!==undefined&&%s!==null){\n", field, field)
					}
				}
				walk(child.Value, field)
				if child.Field.Omit == "absent" {
					b.WriteString("}\n")
				}
			}
		}
	}
	walk(n, value)
}

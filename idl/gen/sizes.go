package main

// The large-field rule ([DEF-A12]). A record's largest encoding is computed
// from its declared bounds ([DEF-A11]); a record whose every value is bounded
// and whose largest encoding exceeds one frame's default (1 MiB, abstraction-ipc
// IPC-F1) is refused unless it says it is one chunk of a stream that a
// reference names. A record holding an unbounded value has no largest
// encoding; idl/inventory.py's rule R9 holds every such field to a shrink-only
// list until its owner declares a bound.

import (
	"fmt"
	"io"
	"strings"
)

// recordCeiling is the largest encoding a record may have without chunk.
const recordCeiling = 1 << 20

// sizeCap stops arithmetic long before int64 overflows; any larger record is
// far past recordCeiling anyway.
const sizeCap = int64(1) << 40

// encodedSize is a record's largest encoding in bytes, or the first value
// that has no bound, named by its path.
type encodedSize struct {
	bytes int64
	open  string
}

func (a encodedSize) plus(b encodedSize) encodedSize {
	if a.open != "" {
		return a
	}
	if b.open != "" {
		return b
	}
	return encodedSize{bytes: min(a.bytes+b.bytes, sizeCap)}
}

func (a encodedSize) times(n int64) encodedSize {
	if a.open != "" {
		return a
	}
	if n > 0 && a.bytes > sizeCap/n {
		return encodedSize{bytes: sizeCap}
	}
	return encodedSize{bytes: a.bytes * n}
}

func fixedSize(n int64) encodedSize { return encodedSize{bytes: n} }

// stringSize keeps a conservative escape ceiling for the declared writer.
// Minimal writers escape quotes/backslashes twice; the closed ASCII name
// class contains neither. Other classes retain the six-byte ceiling.
func (s *Definition) stringSize(f Field, position string, n int64) encodedSize {
	factor := int64(6)
	if s.Encoding.Escape == "minimal" {
		for _, c := range f.Checks {
			if c.Kind != checkPattern || c.Element != position {
				continue
			}
			switch c.Class {
			case "no_control":
				factor = min(factor, 2)
			case "ascii_name", "ascii_lower_token":
				factor = 1
			}
		}
	}
	return fixedSize(factor).times(n).plus(fixedSize(2))
}

// lengthBound is f's declared length maximum, and false when it declares none.
func lengthBound(f Field) (int64, bool) {
	for _, c := range f.Checks {
		if c.Kind == checkLength && c.Element == "" && c.Max != "" {
			var n int64
			fmt.Sscan(c.Max, &n)
			return n, true
		}
	}
	return 0, false
}

// recordSize is the largest encoding of st at nesting depth, reached through
// path. seen holds the records on the way down: a record that contains itself
// has no largest encoding.
func (s *Definition) recordSize(st *Struct, depth int, path string, seen map[*Struct]bool) encodedSize {
	if seen[st] {
		return encodedSize{open: path + " (recursive)"}
	}
	if st.PreservesUnknown() {
		return encodedSize{open: path + " (unknown members preserved)"}
	}
	seen[st] = true
	defer delete(seen, st)
	indent := int64(s.Encoding.Indent)
	inner := int64(depth+1) * indent
	total := fixedSize(2 + inner) // braces, the newline before the closing brace and its indentation
	for _, f := range st.Fields {
		// Indentation, the quoted name, ": ", the separator and the newline.
		member := fixedSize(inner + int64(len(f.Name)) + 6)
		total = total.plus(member).plus(s.valueSize(f, depth+1, path+"."+f.Name, seen))
	}
	return total
}

// valueSize is the largest encoding of one field's value.
func (s *Definition) valueSize(f Field, depth int, path string, seen map[*Struct]bool) encodedSize {
	enum, list := f.EnumType, f.EnumList
	if enum == nil {
		if enum = s.Enum(f.Type); enum == nil {
			enum, list = s.Enum(listElement(f.Type)), true
		}
	}
	if enum != nil {
		word := int64(0)
		for _, m := range enum.Members {
			word = max(word, int64(len(m.WireName())))
		}
		if enum.Ann["unknown"] != "refuse" {
			return encodedSize{open: path + " (open vocabulary)"}
		}
		one := fixedSize(2 + word)
		if !list {
			return one
		}
		return s.listSize(f, one, depth, path)
	}
	if f.Grammar.Named() {
		return fixedSize(64) // a timestamp's written grammar is a fixed width
	}
	switch f.Type {
	case "bool":
		return fixedSize(5)
	case "i32":
		return fixedSize(11)
	case "i64":
		return fixedSize(20)
	case "string":
		n, ok := lengthBound(f)
		if !ok {
			return encodedSize{open: path}
		}
		return s.stringSize(f, "", n)
	case "binary":
		n, ok := lengthBound(f)
		if !ok {
			return encodedSize{open: path}
		}
		return fixedSize(2 + 4*((n+2)/3))
	case "json":
		n, ok := lengthBound(f)
		if !ok || s.Encoding.Opaque != "verbatim" {
			return encodedSize{open: path}
		}
		return fixedSize(n)
	case "list<string>", "list<json>", "map<string,string>", "map<string,json>":
		element := func(position string) encodedSize {
			for _, c := range f.Checks {
				if c.Kind == checkLength && c.Element == position && c.Max != "" {
					var n int64
					fmt.Sscan(c.Max, &n)
					return s.stringSize(f, position, n)
				}
			}
			return encodedSize{open: path + " (elements)"}
		}
		if f.Type == "list<string>" {
			one := element("[]")
			if one.open != "" {
				return one
			}
			return s.listSize(f, one, depth, path)
		}
		if f.Type == "map<string,string>" {
			key, value := element("[key]"), element("[value]")
			if key.open != "" {
				return key
			}
			if value.open != "" {
				return value
			}
			n, ok := lengthBound(f)
			if !ok {
				return encodedSize{open: path}
			}
			inner := int64(depth+1) * int64(s.Encoding.Indent)
			return fixedSize(2 + inner).plus(key.plus(value).plus(fixedSize(inner + 4)).times(n))
		}
		return encodedSize{open: path + " (elements)"}
	}
	if elem := listElement(f.Type); elem != "" {
		st, def := s.reachedStruct(elem)
		if st == nil {
			return encodedSize{open: path}
		}
		return s.listSize(f, def.recordSize(st, depth+1, path+"[]", seen), depth, path)
	}
	if st, def := s.reachedStruct(f.Type); st != nil {
		return def.recordSize(st, depth, path, seen)
	}
	return encodedSize{open: path}
}

func (s *Definition) listSize(f Field, element encodedSize, depth int, path string) encodedSize {
	n, ok := lengthBound(f)
	if !ok {
		return encodedSize{open: path}
	}
	inner := int64(depth+1) * int64(s.Encoding.Indent)
	return fixedSize(2 + inner).plus(element.plus(fixedSize(inner + 2)).times(n))
}

// reachedStruct finds a record this definition names, its own or an
// included definition's, with the definition that owns it.
func (s *Definition) reachedStruct(name string) (*Struct, *Definition) {
	if st := s.byName[name]; st != nil {
		return st, s
	}
	if r, ok := s.importedRecord(name); ok && r.Def != nil {
		if st := r.Def.byName[r.Name]; st != nil {
			return st, r.Def
		}
	}
	return nil, nil
}

// sizedRecords is every record and every method's argument set, by name, in
// declaration order.
func (s *Definition) sizedRecords() []Struct {
	out := append([]Struct(nil), s.Structs...)
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			out = append(out, Struct{Name: argsName(svc, m), Fields: m.Args, UnknownFields: "refuse"})
		}
	}
	return out
}

// validateSizes refuses a fully bounded record above recordCeiling that is
// not a chunk, and a chunk annotation the profile does not define.
func (s *Definition) validateSizes() error {
	var problems []string
	for _, st := range s.sizedRecords() {
		if err := s.validateMaxEncoded(st); err != nil {
			problems = append(problems, err.Error())
			continue
		}
		chunk, marked := st.Ann["chunk"]
		if marked && chunk != "true" {
			problems = append(problems, fmt.Sprintf("struct %s: chunk = %q; a chunk says chunk = \"true\"", st.Name, chunk))
		}
		size := s.recordSize(&st, 0, st.Name, map[*Struct]bool{})
		if size.open == "" && size.bytes > recordCeiling && !marked && st.maxEncoded() == 0 {
			problems = append(problems, fmt.Sprintf("record %s encodes to as many as %d bytes, above one frame's 1048576; carry the large value by reference, or mark the record chunk = \"true\" when it is one bounded piece of a stream ([DEF-A12])", st.Name, size.bytes))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

// runSizes answers gen --sizes; handled reports whether args named it.
func runSizes(args []string, stdout io.Writer) (bool, error) {
	if len(args) == 0 || args[0] != "--sizes" {
		return false, nil
	}
	if len(args) != 2 {
		return true, fmt.Errorf("usage: gen --sizes <definition.thrift>")
	}
	def, err := loadDefinition(args[1])
	if err != nil {
		return true, fmt.Errorf("%s: %w", args[1], err)
	}
	var lines []string
	for _, st := range def.sizedRecords() {
		size := def.recordSize(&st, 0, st.Name, map[*Struct]bool{})
		mark := ""
		if st.Ann["chunk"] == "true" {
			mark = "\tchunk"
		}
		if n := st.maxEncoded(); n > 0 {
			mark += fmt.Sprintf("\tmax_encoded=%d", n)
		}
		if size.open != "" {
			lines = append(lines, fmt.Sprintf("%s\tunbounded\t%s%s", st.Name, size.open, mark))
		} else {
			lines = append(lines, fmt.Sprintf("%s\t%d%s", st.Name, size.bytes, mark))
		}
	}
	_, err = io.WriteString(stdout, strings.Join(lines, "\n")+"\n")
	return true, err
}

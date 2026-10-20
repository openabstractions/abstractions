package main

// The validation family, lowered once ([DEF-A11]). A definition's value
// constraints become a list of checks here, in declaration order, and each
// backend only prints that list in its own syntax. A rule therefore has one
// reading of the definition and five spellings, never five readings.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// checkKind names one declared constraint.
type checkKind int

const (
	checkEquals  checkKind = iota // equals: a required integer equals a literal
	checkRange                    // range: an integer within inclusive bounds
	checkLength                   // length: a string's UTF-8 bytes, a binary's octets, a json value's bytes, a list's or map's entries
	checkPattern                  // pattern: every character of a string belongs to a named class
)

// boundWord is the one refusal a length, range or pattern violation reads
// ([IPC-A6]): a structure-stage word every definition that declares one of
// them declares.
const boundWord = "bad_value"

// The text classes pattern names. Five standard libraries carry five regular
// expression dialects, and C++ and Rust carry none we may use, so a pattern is
// one of these closed classes rather than an expression.
var patternClasses = map[string]string{
	"no_control":        "no control character (U+0000 to U+001F, U+007F to U+009F)",
	"ascii_name":        "only ASCII letters, digits, '_', '.' and '-'",
	"ascii_lower_token": "only lowercase ASCII letters, digits, '_', '.', ':' and '-'",
}

// check is one declared constraint on one field of one record.
type check struct {
	Field    Field
	Kind     checkKind
	Word     string // the refusal a violation reads
	Value    string // equals: the canonical decimal literal
	Min, Max string // range, length: canonical decimal bounds, "" when open
	Class    string // pattern: the class name
	Element  string // fixed collection position: [], [key], or [value]
	Expr     string // emission-only element expression; never schema metadata
}

// lowerField reads every constraint one field declares and refuses one the
// field cannot carry. Its checks come in the order of checkKind.
func lowerField(f Field, refusals []Refusal) ([]check, error) {
	out, err := lowerEquals(f, refusals)
	if err != nil {
		return nil, err
	}
	declared := false
	if spec, ok := f.Ann["range"]; ok {
		declared = true
		if f.EnumType != nil || (f.Type != "i32" && f.Type != "i64") {
			return nil, fmt.Errorf("field %s: range bounds an i32 or i64", f.Name)
		}
		bits := 64
		if f.Type == "i32" {
			bits = 32
		}
		lo, hi, err := parseBounds(spec, bits, false)
		if err != nil {
			return nil, fmt.Errorf("field %s: range %w", f.Name, err)
		}
		out = append(out, check{Field: f, Kind: checkRange, Word: boundWord, Min: lo, Max: hi})
	}
	if spec, ok := f.Ann["length"]; ok {
		declared = true
		if !lengthType(f) {
			return nil, fmt.Errorf("field %s: length bounds a string, binary, json, list or map, not %s", f.Name, f.Type)
		}
		lo, hi, err := parseBounds(spec, 63, true)
		if err != nil {
			return nil, fmt.Errorf("field %s: length %w", f.Name, err)
		}
		out = append(out, check{Field: f, Kind: checkLength, Word: boundWord, Min: lo, Max: hi})
	}
	if class, ok := f.Ann["pattern"]; ok {
		declared = true
		if f.Type != "string" || f.EnumType != nil || f.Grammar.Named() {
			return nil, fmt.Errorf("field %s: pattern constrains a string", f.Name)
		}
		if _, known := patternClasses[class]; !known {
			return nil, fmt.Errorf("field %s: pattern = %q; this profile defines %s", f.Name, class, strings.Join(sortedKeys(patternClasses), ", "))
		}
		out = append(out, check{Field: f, Kind: checkPattern, Word: boundWord, Class: class})
	}
	if declared && !structureRefusal(refusals, boundWord) {
		return nil, fmt.Errorf("field %s declares a constraint, and a definition that declares one declares %s(stage=\"structure\") in its refusal block", f.Name, boundWord)
	}
	return out, nil
}

// lengthType reports whether length can bound f: the types whose size grows
// with the value.
func lengthType(f Field) bool {
	if f.EnumType != nil || f.Grammar.Named() {
		return false
	}
	switch f.Type {
	case "string", "binary", "json":
		return true
	}
	return strings.HasPrefix(f.Type, "list<") || strings.HasPrefix(f.Type, "map<")
}

// parseBounds reads "min..max", "..max" or "min..": canonical decimals within
// a signed integer of bits bits, min at most max, and both non-negative when
// count says the bound counts something.
func parseBounds(spec string, bits int, count bool) (string, string, error) {
	lo, hi, ok := strings.Cut(spec, "..")
	if !ok || (lo == "" && hi == "") {
		return "", "", fmt.Errorf("%q is min..max, ..max or min..", spec)
	}
	read := func(s string) (int64, error) {
		n, err := strconv.ParseInt(s, 10, bits)
		if err != nil || strconv.FormatInt(n, 10) != s {
			return 0, fmt.Errorf("%q: %q is not a canonical in-range decimal", spec, s)
		}
		if count && n < 0 {
			return 0, fmt.Errorf("%q: a count is not negative", spec)
		}
		return n, nil
	}
	var min, max int64
	var err error
	if lo != "" {
		if min, err = read(lo); err != nil {
			return "", "", err
		}
	}
	if hi != "" {
		if max, err = read(hi); err != nil {
			return "", "", err
		}
	}
	if lo != "" && hi != "" && min > max {
		return "", "", fmt.Errorf("%q: min is above max", spec)
	}
	if count && lo == "0" {
		lo = "" // nothing counts fewer than zero
	}
	return lo, hi, nil
}

// lowerDefinitionChecks lowers every record field and method argument once,
// at parse time, so an emitter only ever reads checks the parser accepted.
func (p *parser) lowerDefinitionChecks() error {
	named := map[string][]check{}
	lowerNamed := func(td Typedef) ([]check, error) {
		ann := map[string]string{}
		for _, key := range []string{"length", "pattern"} {
			if value, ok := td.Ann[key]; ok {
				ann[key] = value
			}
		}
		checks, err := lowerField(Field{Name: td.Alias, Type: td.Base, Omit: "never", Grammar: td.Grammar, Ann: ann}, p.def.Refusals)
		return checks, err
	}
	for _, td := range p.def.Typedefs {
		checks, err := lowerNamed(td)
		if err != nil {
			return err
		}
		named[td.Alias] = checks
	}
	lower := func(fields []Field) error {
		for i := range fields {
			checks, err := lowerField(fields[i], p.def.Refusals)
			if err != nil {
				return err
			}
			for _, position := range []string{"", "[]", "[key]", "[value]"} {
				alias := fields[i].TypeAliases[position]
				if _, known := named[alias]; !known {
					if td, ok := p.def.scalarAlias(alias); ok {
						checks, err := lowerNamed(td)
						if err != nil {
							return err
						}
						named[alias] = checks
					}
				}
				for _, inherited := range named[alias] {
					inherited.Field = fields[i]
					inherited.Element = position
					merged := false
					for j, current := range checks {
						if current.Element != position || current.Kind != inherited.Kind {
							continue
						}
						if current.Kind == checkLength {
							lo, hi := current.Min, current.Max
							value := func(s string) int64 { n, _ := strconv.ParseInt(s, 10, 64); return n }
							if inherited.Min != "" && (lo == "" || value(inherited.Min) > value(lo)) {
								lo = inherited.Min
							}
							if inherited.Max != "" && (hi == "" || value(inherited.Max) < value(hi)) {
								hi = inherited.Max
							}
							if lo != "" && hi != "" && value(lo) > value(hi) {
								return fmt.Errorf("field %s: named and field length bounds do not intersect", fields[i].Name)
							}
							checks[j].Min, checks[j].Max = lo, hi
							merged = true
							break
						}
						if current.Kind == checkPattern && current.Class == inherited.Class {
							merged = true
							break
						}
					}
					if !merged {
						checks = append(checks, inherited)
					}
				}
			}
			sort.SliceStable(checks, func(a, b int) bool {
				rank := func(position string) int {
					switch position {
					case "":
						return 0
					case "[]":
						return 1
					case "[key]":
						return 2
					default:
						return 3
					}
				}
				if checks[a].Element != checks[b].Element {
					return rank(checks[a].Element) < rank(checks[b].Element)
				}
				if checks[a].Kind != checks[b].Kind {
					return checks[a].Kind < checks[b].Kind
				}
				return checks[a].Class < checks[b].Class
			})
			fields[i].Checks = checks
		}
		return nil
	}
	for i := range p.def.Structs {
		if err := lower(p.def.Structs[i].Fields); err != nil {
			return err
		}
	}
	for i := range p.def.Services {
		for j := range p.def.Services[i].Methods {
			if err := lower(p.def.Services[i].Methods[j].Args); err != nil {
				return err
			}
			result := []Field{p.def.Services[i].Methods[j].Result}
			if err := lower(result); err != nil {
				return err
			}
			p.def.Services[i].Methods[j].Result = result[0]
		}
	}
	return nil
}

func structureRefusal(refusals []Refusal, word string) bool {
	found := false
	for _, r := range refusals {
		if r.Word == word {
			found = r.Stage == "structure"
		}
	}
	return found
}

// lowerChecks is every check a record declares: its fields in declaration
// order, each with the checks the parser lowered for it.
func lowerChecks(st Struct) []check {
	var out []check
	for _, f := range st.Fields {
		out = append(out, f.Checks...)
	}
	return out
}

// constraintText is a field's declared length, range and pattern as the
// reference page and the compatibility checker spell them, "" for none.
func constraintText(f Field) string {
	var parts []string
	for _, c := range f.Checks {
		prefix := ""
		if c.Element != "" {
			prefix = "element " + c.Element + " "
		}
		switch c.Kind {
		case checkRange:
			parts = append(parts, "range "+c.Min+".."+c.Max)
		case checkLength:
			unit := lengthUnit(f)
			if c.Element != "" {
				unit = "UTF-8 bytes"
			}
			parts = append(parts, prefix+"length "+c.Min+".."+c.Max+" "+unit)
		case checkPattern:
			parts = append(parts, prefix+"pattern "+c.Class)
		}
	}
	return strings.Join(parts, "; ")
}

func lengthUnit(f Field) string {
	switch f.Type {
	case "string", "json":
		return "UTF-8 bytes"
	case "binary":
		return "bytes"
	}
	return "entries"
}

// checkSite is where a printed check runs.
type checkSite int

const (
	siteDecode     checkSite = iota // a decoder at the record's boundary, reading v
	siteEncode                      // an encoder before it writes v
	siteRustLocals                  // Rust's decoder before it builds the record, reading its locals
)

// emitChecks prints checks in lang for site.
func emitChecks(b *strings.Builder, checks []check, lang string, site checkSite) {
	for _, c := range checks {
		if c.Element != "" {
			emitElementCheck(b, c, lang, site)
			continue
		}
		switch c.Kind {
		case checkEquals:
			emitEquals(b, c, lang, site)
		case checkRange, checkLength:
			emitBound(b, c, lang, site)
		case checkPattern:
			emitPattern(b, c, lang, site)
		}
	}
}

// Element constraints reuse exactly the scalar check after fixed container
// traversal. A present element is required even when its parent is optional.
func emitElementCheck(b *strings.Builder, c check, lang string, site checkSite) {
	parent := checkSubject(c.Field, lang, site)
	position := c.Element
	c.Element = ""
	c.Field.Type = "string"
	c.Field.Omit = "never"
	c.Field.Grammar = Grammar{}
	c.Expr = "oaElement"
	var inner strings.Builder
	if lang == "rust" || lang == "python" {
		c.Expr = "oa_element"
	}
	if c.Kind == checkLength {
		emitBound(&inner, c, lang, site)
	} else {
		emitPattern(&inner, c, lang, site)
	}
	switch lang {
	case "go":
		if position == "[key]" {
			fmt.Fprintf(b, "\tfor oaElement := range %s {\n", parent)
		} else {
			fmt.Fprintf(b, "\tfor _, oaElement := range %s {\n", parent)
		}
		b.WriteString(inner.String())
		b.WriteString("\t}\n")
	case "cpp":
		if position == "[]" {
			fmt.Fprintf(b, "    for (const auto& oaElement : %s) {\n", parent)
		} else {
			member := "first"
			if position == "[value]" {
				member = "second"
			}
			fmt.Fprintf(b, "    for (const auto& oaEntry : %s) { const auto& oaElement = oaEntry.%s;\n", parent, member)
		}
		b.WriteString(inner.String())
		b.WriteString("    }\n")
	case "rust":
		iterator := "iter"
		if position == "[key]" {
			iterator = "keys"
		}
		if position == "[value]" {
			iterator = "values"
		}
		fmt.Fprintf(b, "    for oa_element in %s.%s() {\n", parent, iterator)
		b.WriteString(inner.String())
		b.WriteString("    }\n")
	case "python":
		kind, values := "list", parent
		if position != "[]" {
			kind = "dict"
			if position == "[value]" {
				values += ".values()"
			}
		}
		fmt.Fprintf(b, "    if isinstance(%s, %s):\n        for oa_element in %s:\n", parent, kind, values)
		for _, line := range strings.Split(strings.TrimSuffix(inner.String(), "\n"), "\n") {
			b.WriteString("        " + line + "\n")
		}
	case "javascript":
		guard, values := "Array.isArray("+parent+")", parent
		if position != "[]" {
			guard = parent + " && typeof " + parent + " === \"object\" && !Array.isArray(" + parent + ")"
			fn := "keys"
			if position == "[value]" {
				fn = "values"
			}
			values = "Object." + fn + "(" + parent + ")"
		}
		fmt.Fprintf(b, "  if (%s) { for (const oaElement of %s) { if (typeof oaElement === \"string\") {\n", guard, values)
		b.WriteString(inner.String())
		b.WriteString("  } } }\n")
	}
}

func checkSubject(f Field, lang string, site checkSite) string {
	if site == siteRustLocals {
		return rsLocal(f)
	}
	name := f.Ident(lang)
	if lang == "go" {
		name = exported(name)
	}
	return "v." + name
}

func emitEquals(b *strings.Builder, c check, lang string, site checkSite) {
	f, word, value := c.Field, c.Word, c.Value
	if site == siteRustLocals {
		fmt.Fprintf(b, "    if %s != %s {\n        return r.refuse(%q);\n    }\n", rsLocal(f), value, word)
		return
	}
	encode := site == siteEncode
	e := checkSubject(f, lang, site)
	if lang == "javascript" && f.Type == "i64" {
		value += "n"
	}
	if lang == "cpp" && f.Type == "i64" {
		value = cppInt64(value)
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

func cppInt64(value string) string {
	if value == "-9223372036854775808" {
		return "INT64_MIN"
	}
	return value + "LL"
}

// refusal is the statement a violation runs at site, without its terminator.
func refusal(lang string, site checkSite, word string) string {
	encode := site == siteEncode
	switch lang {
	case "go":
		if encode {
			return fmt.Sprintf("panic(&Refusal{Word:%q,Offset:0})", word)
		}
		return fmt.Sprintf("return nil, r.refuse(%q)", word)
	case "python":
		if encode {
			return fmt.Sprintf("raise Refusal(%q, 0)", word)
		}
		return fmt.Sprintf("raise r.refuse(%q)", word)
	case "cpp":
		if encode {
			return fmt.Sprintf("throw Refusal(%q,0)", word)
		}
		return fmt.Sprintf("r.refuse(%q)", word)
	case "rust":
		if encode {
			return fmt.Sprintf("std::panic::panic_any(Refusal{word:%q,offset:0})", word)
		}
		return fmt.Sprintf("return r.refuse(%q)", word)
	case "javascript":
		if encode {
			return fmt.Sprintf("throw new Refusal(%q,0)", word)
		}
		return fmt.Sprintf("throw r.refuse(%q)", word)
	}
	panic("no refusal for " + lang)
}

// emitBound prints a range or length check. The checked quantity is the
// integer itself or the value's size. Omit-zero fields skip zero; scalar
// presence checks every present value.
func emitBound(b *strings.Builder, c check, lang string, site checkSite) {
	if scalarAbsent(c.Field) {
		e := checkSubject(c.Field, lang, site)
		var inner strings.Builder
		c.Field.Omit = "never"
		switch lang {
		case "go", "cpp":
			c.Expr = "*" + e
		case "rust":
			c.Expr = "oa_value"
		default:
			c.Expr = e
		}
		emitBound(&inner, c, lang, site)
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tif %s != nil {\n%s\t}\n", e, inner.String())
		case "cpp":
			fmt.Fprintf(b, "    if (%s.has_value()) {\n%s    }\n", e, inner.String())
		case "rust":
			fmt.Fprintf(b, "    if let Some(oa_value) = %s {\n%s    }\n", e, inner.String())
		case "python":
			fmt.Fprintf(b, "    if %s is not None:\n", e)
			for _, line := range strings.Split(strings.TrimSuffix(inner.String(), "\n"), "\n") {
				b.WriteString("    " + line + "\n")
			}
		case "javascript":
			fmt.Fprintf(b, "  if (%s !== undefined && %s !== null) {\n%s  }\n", e, e, inner.String())
		}
		return
	}
	f := c.Field
	e := checkSubject(f, lang, site)
	if c.Expr != "" {
		e = c.Expr
	}
	// An optional field's zero is skipped only where the bound would refuse it.
	optional := f.Omit != "never" && !zeroInside(c)
	act := refusal(lang, site, c.Word)
	lit := func(s string) string {
		switch {
		case c.Kind == checkRange && lang == "javascript" && f.Type == "i64":
			return s + "n"
		case c.Kind == checkRange && lang == "cpp" && f.Type == "i64":
			return cppInt64(s)
		case c.Kind == checkLength && lang == "cpp":
			return s + "ULL"
		}
		return s
	}
	zero := "0"
	if lang == "javascript" && c.Kind == checkRange && f.Type == "i64" {
		zero = "0n"
	}
	outside := func(n string) string {
		var parts []string
		if c.Min != "" {
			parts = append(parts, n+" < "+lit(c.Min))
		}
		if c.Max != "" {
			parts = append(parts, n+" > "+lit(c.Max))
		}
		cond := strings.Join(parts, " || ")
		if lang == "rust" && len(parts) > 1 {
			// Clippy's manual_range_contains: Rust says an inclusive range once.
			cond = "!(" + lit(c.Min) + "..=" + lit(c.Max) + ").contains(&" + n + ")"
			parts = parts[:1]
		}
		if optional {
			ne := " != "
			if lang == "javascript" {
				ne = " !== "
			}
			if len(parts) > 1 {
				cond = "(" + cond + ")"
			}
			and := " && "
			if lang == "python" {
				and = " and "
			}
			cond = n + ne + zero + and + cond
		}
		if lang == "python" {
			cond = strings.ReplaceAll(cond, " || ", " or ")
		}
		return cond
	}
	if c.Kind == checkRange {
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tif %s { %s }\n", outside(e), act)
		case "python":
			if site == siteEncode {
				fmt.Fprintf(b, "    if type(%s) is not int: raise Refusal(\"wrong_type\", 0)\n", e)
			}
			fmt.Fprintf(b, "    if %s: %s\n", outside(e), act)
		case "cpp":
			fmt.Fprintf(b, "    if (%s) %s;\n", outside(e), act)
		case "rust":
			fmt.Fprintf(b, "    if %s { %s; }\n", outside(e), act)
		case "javascript":
			fmt.Fprintf(b, "  if (%s) %s;\n", outside(e), act)
		}
		return
	}
	switch lang {
	case "go":
		fmt.Fprintf(b, "\tif n := len(%s); %s { %s }\n", e, outside("n"), act)
	case "python":
		size, guard := pySize(f, e)
		indent := "    "
		if site == siteEncode {
			fmt.Fprintf(b, "    if isinstance(%s, %s):\n", e, guard)
			indent = "        "
		}
		fmt.Fprintf(b, "%s_size = %s\n%sif %s: %s\n", indent, size, indent, outside("_size"), act)
	case "cpp":
		fmt.Fprintf(b, "    if (const auto n = static_cast<std::uint64_t>(%s); %s) %s;\n", cppSize(f, e), outside("n"), act)
	case "rust":
		fmt.Fprintf(b, "    let n = %s;\n    if %s { %s; }\n", rsSize(f, e), outside("n"), act)
	case "javascript":
		fmt.Fprintf(b, "  { const n = %s; if (%s) %s; }\n", jsSize(f, e), outside("n"), act)
	}
}

// pySize is the Python expression for e's size, and the types an encoder
// measures; a value of another type reaches the writer, which refuses it.
func pySize(f Field, e string) (string, string) {
	switch f.Type {
	case "string":
		return "len(" + e + ".encode(\"utf-8\", \"surrogatepass\"))", "str"
	case "json":
		return "len(" + e + ".encode(\"utf-8\", \"surrogatepass\") if isinstance(" + e + ", str) else " + e + ")", "(str, bytes, bytearray)"
	case "binary":
		if f.Omit == "absent" {
			return "len(" + e + " or b\"\")", "(bytes, bytearray, type(None))"
		}
		return "len(" + e + ")", "(bytes, bytearray)"
	}
	if strings.HasPrefix(f.Type, "map<") {
		return "len(" + e + ")", "dict"
	}
	return "len(" + e + ")", "list"
}

func cppSize(f Field, e string) string {
	if f.Type == "binary" && f.Omit == "absent" {
		return "(" + e + " ? " + e + "->size() : 0)"
	}
	return e + ".size()"
}

func rsSize(f Field, e string) string {
	if f.Type == "binary" && f.Omit == "absent" {
		return e + ".as_ref().map_or(0, Vec::len)"
	}
	return e + ".len()"
}

func jsSize(f Field, e string) string {
	switch f.Type {
	case "string":
		return "ENC.encode(" + e + ").length"
	case "json":
		return "(typeof " + e + " === \"string\" ? ENC.encode(" + e + ").length : " + e + ".length)"
	case "binary":
		if f.Omit == "absent" {
			return "(" + e + " == null ? 0 : " + e + ".length)"
		}
		return e + ".length"
	}
	if strings.HasPrefix(f.Type, "map<") {
		return "Object.keys(" + e + ").length"
	}
	return e + ".length"
}

// emitPattern prints a text-class check. The empty string belongs to every
// class, so an optional field's zero passes without a gate.
func emitPattern(b *strings.Builder, c check, lang string, site checkSite) {
	e := checkSubject(c.Field, lang, site)
	if c.Expr != "" {
		e = c.Expr
	}
	act := refusal(lang, site, c.Word)
	switch c.Class {
	case "no_control":
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tfor _, c := range %s { if c < 0x20 || c >= 0x7f && c <= 0x9f { %s } }\n", e, act)
		case "python":
			guard := ""
			if site == siteEncode {
				guard = "isinstance(" + e + ", str) and "
			}
			fmt.Fprintf(b, "    if %sany(c < \"\\x20\" or \"\\x7f\" <= c <= \"\\x9f\" for c in %s): %s\n", guard, e, act)
		case "cpp":
			fmt.Fprintf(b, "    for (std::size_t i = 0; i < %[1]s.size(); ++i) { const auto c = static_cast<unsigned char>(%[1]s[i]); if (c < 0x20 || c == 0x7f || (c == 0xc2 && i + 1 < %[1]s.size() && static_cast<unsigned char>(%[1]s[i + 1]) <= 0x9f)) %[2]s; }\n", e, act)
		case "rust":
			fmt.Fprintf(b, "    if %s.chars().any(char::is_control) { %s; }\n", e, act)
		case "javascript":
			fmt.Fprintf(b, "  if (/[\\u0000-\\u001f\\u007f-\\u009f]/.test(%s)) %s;\n", e, act)
		}
	case "ascii_name":
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tfor i := 0; i < len(%[1]s); i++ { if c := %[1]s[i]; !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '-') { %[2]s } }\n", e, act)
		case "python":
			guard := ""
			if site == siteEncode {
				guard = "isinstance(" + e + ", str) and "
			}
			fmt.Fprintf(b, "    if %snot all(c in \"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_.-\" for c in %s): %s\n", guard, e, act)
		case "cpp":
			fmt.Fprintf(b, "    for (const char ch : %s) { const auto c = static_cast<unsigned char>(ch); if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '.' || c == '-')) %s; }\n", e, act)
		case "rust":
			fmt.Fprintf(b, "    if !%s.bytes().all(|c| c.is_ascii_alphanumeric() || c == b'_' || c == b'.' || c == b'-') { %s; }\n", e, act)
		case "javascript":
			fmt.Fprintf(b, "  if (!/^[A-Za-z0-9_.-]*$/.test(%s)) %s;\n", e, act)
		}
	case "ascii_lower_token":
		switch lang {
		case "go":
			fmt.Fprintf(b, "\tfor i := 0; i < len(%[1]s); i++ { if c := %[1]s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == ':' || c == '_' || c == '.' || c == '-') { %[2]s } }\n", e, act)
		case "python":
			guard := ""
			if site == siteEncode {
				guard = "isinstance(" + e + ", str) and "
			}
			fmt.Fprintf(b, "    if %snot all(c in \"abcdefghijklmnopqrstuvwxyz0123456789_.:-\" for c in %s): %s\n", guard, e, act)
		case "cpp":
			fmt.Fprintf(b, "    for (const char ch : %s) { const auto c = static_cast<unsigned char>(ch); if (!((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == ':' || c == '_' || c == '.' || c == '-')) %s; }\n", e, act)
		case "rust":
			fmt.Fprintf(b, "    if !%s.bytes().all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || c == b':' || c == b'_' || c == b'.' || c == b'-') { %s; }\n", e, act)
		case "javascript":
			fmt.Fprintf(b, "  if (/[^a-z0-9_.:-]/.test(%s)) %s;\n", e, act)
		}
	}
}

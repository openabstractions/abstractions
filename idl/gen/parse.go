package main

import (
	"fmt"
	"strconv"
	"strings"
)

type parser struct {
	toks     []token
	i        int
	typedefs map[string]string
	grammars map[string]Grammar
	def      *Definition
	seenEnc  bool
}

var forbidden = map[string]string{
	"service":     "behaviour is not in the schema: a lease, an epoch and a refusal live in the contract page and the scenario corpus",
	"exception":   "behaviour is not in the schema: a refusal is a closed vocabulary, declared as an enum",
	"union":       "a union's absent arm and an absent field are two spellings of one thing",
	"senum":       "withdrawn from Thrift itself",
	"include":     "one file, one definition: a transitive definition graph has no single normative text",
	"cpp_include": "one file, one definition",
	"oneway":      "behaviour is not in the schema",
}

var encodingKeys = map[string][]string{
	"escape":         {"minimal", "ascii"},
	"map_keys":       {"utf8-bytes"},
	"numbers":        {"integer-decimal"},
	"opaque":         {"verbatim"},
	"terminator":     {"newline", "none"},
	"duplicate_keys": {"refuse", "last"},
}

var encodingRequired = []string{
	"escape", "indent", "map_keys", "numbers", "opaque", "terminator",
	"duplicate_keys", "depth_limit",
}

var grammarAccepts = map[string][]string{
	"rfc3339-micros": {"rfc3339-micros"},
	"rfc3339-wide":   {"rfc3339-micros", "rfc3339-wide"},
}

var scalarTypes = map[string]bool{
	"bool": true, "i32": true, "i64": true, "string": true, "json": true,
}

func parse(src string) (*Definition, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{
		toks:     toks,
		typedefs: map[string]string{},
		grammars: map[string]Grammar{},
		def:      &Definition{byName: map[string]*Struct{}},
	}
	if err := p.file(); err != nil {
		return nil, err
	}
	return p.def, p.validate()
}

func (p *parser) peek() token { return p.toks[p.i] }
func (p *parser) next() token { t := p.toks[p.i]; p.i++; return t }
func (p *parser) at(s string) bool {
	t := p.peek()
	return (t.kind == "ident" || t.kind == "punct") && t.text == s
}

func (p *parser) want(s string) error {
	if !p.at(s) {
		return fmt.Errorf("line %d: expected %q, found %q", p.peek().line, s, p.peek().text)
	}
	p.i++
	return nil
}

func (p *parser) ident() (string, error) {
	t := p.next()
	if t.kind != "ident" {
		return "", fmt.Errorf("line %d: expected a name, found %q", t.line, t.text)
	}
	return t.text, nil
}

func (p *parser) file() error {
	for p.peek().kind != "eof" {
		word := p.peek().text
		if why, bad := forbidden[word]; bad {
			return fmt.Errorf("line %d: %s is not in this profile — %s", p.peek().line, word, why)
		}
		var err error
		switch word {
		case "encoding":
			err = p.encoding()
		case "typedef":
			err = p.typedef()
		case "namespace":
			p.i += 3
		case "struct":
			err = p.structDef()
		case "enum":
			err = p.enumDef()
		case "vocabulary":
			err = p.vocabularyDef()
		case "refusal":
			err = p.refusalDef()
		case "protocol":
			err = p.protocolDef()
		case "const":
			err = p.constDef()
		default:
			return fmt.Errorf("line %d: %q begins nothing this profile defines", p.peek().line, word)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) encoding() error {
	if p.seenEnc {
		return fmt.Errorf("line %d: a definition declares its encoding once", p.peek().line)
	}
	p.seenEnc = true
	p.i++
	name, err := p.ident()
	if err != nil {
		return err
	}
	if name != "json" {
		return fmt.Errorf("line %d: %q is not an encoding this profile defines", p.peek().line, name)
	}
	if err := p.want("{"); err != nil {
		return err
	}
	seen := map[string]bool{}
	for !p.at("}") {
		line := p.peek().line
		key, err := p.ident()
		if err != nil {
			return err
		}
		if err := p.want("="); err != nil {
			return err
		}
		val := p.next()
		if val.kind != "string" && val.kind != "int" {
			return fmt.Errorf("line %d: %s takes a quoted value", line, key)
		}
		if seen[key] {
			return fmt.Errorf("line %d: %s is declared twice", line, key)
		}
		seen[key] = true
		if err := p.setEncoding(key, val.text, line); err != nil {
			return err
		}
		if p.at(",") || p.at(";") {
			p.i++
		}
	}
	p.i++
	for _, key := range encodingRequired {
		if !seen[key] {
			return fmt.Errorf("the encoding block does not say %s, and an unstated rule is how three languages came to disagree", key)
		}
	}
	return nil
}

func (p *parser) setEncoding(key, val string, line int) error {
	if key == "indent" {
		n, err := strconv.Atoi(val)
		if err != nil || n < 0 || n > 8 {
			return fmt.Errorf("line %d: indent is a width from 0 to 8", line)
		}
		p.def.Encoding.Indent = n
		return nil
	}
	if key == "depth_limit" {
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 || n > 1024 {
			return fmt.Errorf("line %d: depth_limit is a nesting depth from 1 to 1024", line)
		}
		p.def.Encoding.DepthLimit = n
		return nil
	}
	allowed, known := encodingKeys[key]
	if !known {
		return fmt.Errorf("line %d: %q is not an encoding property this profile defines", line, key)
	}
	if !contains(allowed, val) {
		return fmt.Errorf("line %d: %s = %q; this profile defines %s", line, key, val, strings.Join(allowed, ", "))
	}
	switch key {
	case "escape":
		p.def.Encoding.Escape = val
	case "map_keys":
		p.def.Encoding.MapKeys = val
	case "numbers":
		p.def.Encoding.Numbers = val
	case "opaque":
		p.def.Encoding.Opaque = val
	case "terminator":
		p.def.Encoding.Terminator = val
	case "duplicate_keys":
		p.def.Encoding.DuplicateKeys = val
	}
	return nil
}

func (p *parser) typedef() error {
	p.i++
	base, err := p.ident()
	if err != nil {
		return err
	}
	alias, err := p.ident()
	if err != nil {
		return err
	}
	if !scalarTypes[base] {
		return fmt.Errorf("line %d: a typedef renames a scalar, not %q", p.peek().line, base)
	}
	line := p.peek().line
	ann, err := p.annotations()
	if err != nil {
		return err
	}
	g := Grammar{Read: ann["read"], Write: ann["write"]}
	if g.Read != "" || g.Write != "" {
		if err := checkGrammars(alias, base, g, line); err != nil {
			return err
		}
	}
	p.typedefs[alias] = base
	p.grammars[alias] = g
	p.def.Typedefs = append(p.def.Typedefs, Typedef{Alias: alias, Base: base, Grammar: g})
	return nil
}

func checkGrammars(alias, base string, g Grammar, line int) error {
	if base != "string" {
		return fmt.Errorf("line %d: %s names a grammar, and a grammar reads a string", line, alias)
	}
	if g.Read == "" || g.Write == "" {
		return fmt.Errorf("line %d: %s names one grammar; a narrow write and a wide read are two, and the width between them is the whole guarantee", line, alias)
	}
	accepted, known := grammarAccepts[g.Read]
	if !known {
		return fmt.Errorf("line %d: read = %q is not a grammar this profile defines", line, g.Read)
	}
	if _, known := grammarAccepts[g.Write]; !known {
		return fmt.Errorf("line %d: write = %q is not a grammar this profile defines", line, g.Write)
	}
	if !contains(accepted, g.Write) {
		return fmt.Errorf("line %d: %s reads %s and writes %s, which is narrower on read than on write; a reader that refuses what its own writer emits is not interoperable", line, alias, g.Read, g.Write)
	}
	return nil
}

func (p *parser) vocabularyDef() error {
	p.i++
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("{"); err != nil {
		return err
	}
	v := Vocabulary{Name: name}
	seen := map[string]bool{}
	for !p.at("}") {
		line := p.peek().line
		idTok := p.next()
		if idTok.kind != "int" {
			return fmt.Errorf("line %d: every term carries an id, and %q is not one", line, idTok.text)
		}
		id, _ := strconv.Atoi(idTok.text)
		if err := p.want(":"); err != nil {
			return err
		}
		nameTok := p.next()
		if nameTok.kind != "string" {
			return fmt.Errorf("line %d: a term is a quoted wire name, not %q", line, nameTok.text)
		}
		if seen[nameTok.text] {
			return fmt.Errorf("line %d: %q appears twice in %s", line, nameTok.text, name)
		}
		seen[nameTok.text] = true
		ann, err := p.annotations()
		if err != nil {
			return err
		}
		when := ann["when"]
		if when == "" {
			return fmt.Errorf("line %d: %q does not say when it is derived; this profile defines when = \"always\", when = \"<path>\" and when = \"<path>\" with is = \"<word>,...\"", line, nameTok.text)
		}
		if sc := ann["strip_critical"]; sc != "" && sc != "true" {
			return fmt.Errorf("line %d: strip_critical = %q; the flag is \"true\" or it is not there", line, sc)
		}
		for k := range ann {
			if k != "when" && k != "strip_critical" && k != "is" {
				return fmt.Errorf("line %d: %q carries %q, and a term takes when, is and strip_critical; a flag this profile does not read is a rule nobody enforces", line, nameTok.text, k)
			}
		}
		is, err := membershipWords(ann, line, nameTok.text)
		if err != nil {
			return err
		}
		if p.at(",") || p.at(";") {
			p.i++
		}
		v.Terms = append(v.Terms, Term{ID: id, Name: nameTok.text, When: when, Is: is, StripCritical: ann["strip_critical"] == "true"})
	}
	p.i++
	ann, err := p.annotations()
	if err != nil {
		return err
	}
	v.Of, v.Names, v.Critical = ann["of"], ann["names"], ann["critical"]
	if v.Of == "" || v.Names == "" || v.Critical == "" {
		return fmt.Errorf("vocabulary %s does not say which struct it is derived from and which two fields carry it: of, names and critical are all required", name)
	}
	if p.def.Vocab != nil {
		return fmt.Errorf("vocabulary %s is the second in this definition; a document is derived from itself once, and two derivations are two truths", name)
	}
	p.def.Vocab = &v
	return nil
}

var refusalStages = []string{"grammar", "structure", "document", "derivation"}

func stageRank(s string) int {
	for i, name := range refusalStages {
		if name == s {
			return i
		}
	}
	return -1
}

func (p *parser) refusalDef() error {
	p.i++
	if p.def.Refusals != nil {
		return fmt.Errorf("line %d: a definition declares its refusals once; two vocabularies are two public surfaces", p.peek().line)
	}
	if err := p.want("{"); err != nil {
		return err
	}
	seen, stage := map[string]bool{}, 0
	for !p.at("}") {
		line := p.peek().line
		idTok := p.next()
		if idTok.kind != "int" {
			return fmt.Errorf("line %d: every refusal carries an id, and %q is not one", line, idTok.text)
		}
		id, _ := strconv.Atoi(idTok.text)
		if id != len(p.def.Refusals)+1 {
			return fmt.Errorf("line %d: refusal ids ascend from 1 without a gap, because the order they are declared in is the order two of them are chosen between; found %d where %d was next", line, id, len(p.def.Refusals)+1)
		}
		if err := p.want(":"); err != nil {
			return err
		}
		word, err := p.ident()
		if err != nil {
			return err
		}
		if seen[word] {
			return fmt.Errorf("line %d: %q is declared twice", line, word)
		}
		seen[word] = true
		ann, err := p.annotations()
		if err != nil {
			return err
		}
		rank := stageRank(ann["stage"])
		if rank < 0 {
			return fmt.Errorf("line %d: %s does not say at which stage it is reached; this profile defines %s", line, word, strings.Join(refusalStages, ", "))
		}
		if rank < stage {
			return fmt.Errorf("line %d: %s is declared after a later stage; a reader reaches %s before %s, so the declaration is in that order too", line, word, ann["stage"], refusalStages[stage])
		}
		stage = rank
		if p.at(",") || p.at(";") {
			p.i++
		}
		p.def.Refusals = append(p.def.Refusals, Refusal{ID: id, Word: word, Stage: ann["stage"]})
	}
	p.i++
	if len(p.def.Refusals) == 0 {
		return fmt.Errorf("the refusal block is empty, and a decoder that can refuse nothing is a decoder that reads everything")
	}
	return nil
}

var protocolKeys = []string{"request", "response", "operation", "verdict", "verdicts", "unknown_operation"}

func (p *parser) protocolDef() error {
	p.i++
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("{"); err != nil {
		return err
	}
	pr := Protocol{Name: name}
	seen := map[string]bool{}
	for !p.at("}") {
		line := p.peek().line
		idTok := p.next()
		if idTok.kind != "int" {
			return fmt.Errorf("line %d: every operation carries an id, and %q is not one", line, idTok.text)
		}
		id, _ := strconv.Atoi(idTok.text)
		if id != len(pr.Operations)+1 {
			return fmt.Errorf("line %d: operation ids ascend from 1 without a gap; found %d where %d was next", line, id, len(pr.Operations)+1)
		}
		if err := p.want(":"); err != nil {
			return err
		}
		op, err := p.ident()
		if err != nil {
			return err
		}
		if seen[op] {
			return fmt.Errorf("line %d: operation %q appears twice in %s", line, op, name)
		}
		seen[op] = true
		if _, err := p.annotations(); err != nil {
			return err
		}
		if p.at(",") || p.at(";") {
			p.i++
		}
		pr.Operations = append(pr.Operations, Operation{ID: id, Name: op})
	}
	p.i++
	ann, err := p.annotations()
	if err != nil {
		return err
	}
	for _, k := range protocolKeys {
		if ann[k] == "" {
			return fmt.Errorf("protocol %s does not say %s; an envelope nobody can name is an envelope three peers each invent", name, k)
		}
	}
	pr.Request, pr.Response = ann["request"], ann["response"]
	pr.OpField, pr.Verdict = ann["operation"], ann["verdict"]
	pr.Verdicts, pr.Unknown = ann["verdicts"], ann["unknown_operation"]
	if p.def.Proto != nil {
		return fmt.Errorf("protocol %s is the second in this definition; two envelopes over one record are two wires", name)
	}
	p.def.Proto = &pr
	return nil
}

func (p *parser) annotations() (map[string]string, error) {
	ann := map[string]string{}
	if !p.at("(") {
		return ann, nil
	}
	p.i++
	for !p.at(")") {
		line := p.peek().line
		key, err := p.ident()
		if err != nil {
			return nil, err
		}
		if err := p.want("="); err != nil {
			return nil, err
		}
		val := p.next()
		if val.kind != "string" {
			return nil, fmt.Errorf("line %d: an annotation value is quoted", line)
		}
		ann[key] = val.text
		if p.at(",") {
			p.i++
		}
	}
	p.i++
	return ann, nil
}

func (p *parser) typeRef() (string, error) {
	t := p.next()
	if t.kind != "ident" {
		return "", fmt.Errorf("line %d: expected a type, found %q", t.line, t.text)
	}
	name := t.text
	if base, ok := p.typedefs[name]; ok {
		return base, nil
	}
	if name != "list" && name != "map" {
		return name, nil
	}
	if err := p.want("<"); err != nil {
		return "", err
	}
	var parts []string
	for {
		inner, err := p.typeRef()
		if err != nil {
			return "", err
		}
		parts = append(parts, inner)
		if p.at(",") {
			p.i++
			continue
		}
		break
	}
	if err := p.want(">"); err != nil {
		return "", err
	}
	return name + "<" + strings.Join(parts, ",") + ">", nil
}

func (p *parser) structDef() error {
	p.i++
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("{"); err != nil {
		return err
	}
	st := Struct{Name: name}
	ids, names := map[int]bool{}, map[string]bool{}
	for !p.at("}") {
		f, err := p.field()
		if err != nil {
			return err
		}
		if ids[f.ID] {
			return fmt.Errorf("line %d: field id %d appears twice in %s", f.Line, f.ID, name)
		}
		if names[f.Name] {
			return fmt.Errorf("line %d: field %s appears twice in %s", f.Line, f.Name, name)
		}
		ids[f.ID], names[f.Name] = true, true
		st.Fields = append(st.Fields, f)
	}
	p.i++
	st.Ann, err = p.annotations()
	if err != nil {
		return err
	}
	st.Document = st.Ann["document"] == "true"
	st.UnknownFields = st.Ann["unknown_fields"]
	if st.UnknownFields != "refuse" && st.UnknownFields != "grant" && !st.PreservesUnknown() {
		return fmt.Errorf("struct %s does not say what a reader does with a field it has never heard of; this profile defines unknown_fields = \"refuse\", \"grant\", or \"preserve\"", name)
	}
	if st.PreservesUnknown() {
		for _, f := range st.Fields {
			for _, lang := range []string{"go", "python", "cpp", "javascript", "rust"} {
				ident := f.Ident(lang)
				if lang == "go" {
					ident = exported(ident)
				}
				if f.Name == "extras" || ident == "extras" || (lang == "go" && ident == "Extras") {
					return fmt.Errorf("struct %s preserves unknown fields: extras/Extras is reserved for generated storage (%s field %s)", name, lang, f.Name)
				}
			}
		}
	}
	p.def.Structs = append(p.def.Structs, st)
	p.def.byName[name] = &p.def.Structs[len(p.def.Structs)-1]
	return nil
}

func (p *parser) field() (Field, error) {
	line := p.peek().line
	idTok := p.next()
	if idTok.kind != "int" {
		return Field{}, fmt.Errorf("line %d: every field carries an id, and %q is not one", line, idTok.text)
	}
	id, _ := strconv.Atoi(idTok.text)
	if id < 1 {
		return Field{}, fmt.Errorf("line %d: a field id is positive", line)
	}
	if err := p.want(":"); err != nil {
		return Field{}, err
	}
	req, err := p.ident()
	if err != nil {
		return Field{}, err
	}
	if req != "required" && req != "optional" {
		return Field{}, fmt.Errorf("line %d: a field is required or optional, not %q", line, req)
	}
	alias := p.peek().text
	typ, err := p.typeRef()
	if err != nil {
		return Field{}, err
	}
	name, err := p.ident()
	if err != nil {
		return Field{}, err
	}
	if p.at("=") {
		return Field{}, fmt.Errorf("line %d: %s carries a default value; a default written back is not an absence, so this profile has none", line, name)
	}
	ann, err := p.annotations()
	if err != nil {
		return Field{}, err
	}
	if p.at(",") || p.at(";") {
		p.i++
	}
	omit := ann["omit"]
	switch {
	case req == "required" && omit != "":
		return Field{}, fmt.Errorf("line %d: %s is required and cannot say when it is omitted", line, name)
	case req == "required":
		omit = "never"
	case omit == "":
		return Field{}, fmt.Errorf("line %d: optional %s does not say whether it is omitted when zero or when absent", line, name)
	case omit != "zero" && omit != "absent":
		return Field{}, fmt.Errorf("line %d: omit = %q; this profile defines zero and absent", line, omit)
	}
	named := ""
	if _, ok := p.typedefs[alias]; ok {
		named = alias
	}
	return Field{ID: id, Type: typ, Alias: named, Name: name, Omit: omit, Grammar: p.grammars[alias], Ann: ann, Line: line}, nil
}

func (p *parser) enumDef() error {
	p.i++
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("{"); err != nil {
		return err
	}
	en := Enum{Name: name}
	for !p.at("}") {
		line := p.peek().line
		idTok := p.next()
		if idTok.kind != "int" {
			return fmt.Errorf("line %d: every member carries an id, and %q is not one", line, idTok.text)
		}
		id, _ := strconv.Atoi(idTok.text)
		if err := p.want(":"); err != nil {
			return err
		}
		member, err := p.ident()
		if err != nil {
			return err
		}
		ann, err := p.annotations()
		if err != nil {
			return err
		}
		if p.at(",") || p.at(";") {
			p.i++
		}
		en.Members = append(en.Members, Member{ID: id, Name: member, Ann: ann})
	}
	p.i++
	en.Ann, err = p.annotations()
	if err != nil {
		return err
	}
	if u := en.Ann["unknown"]; u != "grant" && u != "refuse" {
		return fmt.Errorf("enum %s does not say what an unknown member does; this profile defines unknown = \"grant\" and unknown = \"refuse\"", name)
	}
	p.def.Enums = append(p.def.Enums, en)
	return nil
}

func (p *parser) constDef() error {
	p.i++
	typ, err := p.typeRef()
	if err != nil {
		return err
	}
	if typ != "list<i32>" && typ != "list<string>" {
		return fmt.Errorf("line %d: a const is list<i32> or list<string>, not %s", p.peek().line, typ)
	}
	name, err := p.ident()
	if err != nil {
		return err
	}
	if err := p.want("="); err != nil {
		return err
	}
	if err := p.want("["); err != nil {
		return err
	}
	c := Const{Name: name, Type: typ}
	for !p.at("]") {
		t := p.next()
		switch {
		case typ == "list<i32>" && t.kind == "int":
			n, _ := strconv.ParseInt(t.text, 10, 64)
			c.Ints = append(c.Ints, n)
		case typ == "list<string>" && t.kind == "string":
			c.Strings = append(c.Strings, t.text)
		default:
			return fmt.Errorf("line %d: %q does not belong in a %s", t.line, t.text, typ)
		}
		if p.at(",") {
			p.i++
		}
	}
	p.i++
	p.def.Consts = append(p.def.Consts, c)
	return nil
}

var encodableCollections = map[string]bool{
	"list<string>": true, "map<string,json>": true, "map<string,string>": true,
}

func isCollection(t string) bool {
	return strings.HasPrefix(t, "list<") || strings.HasPrefix(t, "map<")
}

func (p *parser) validate() error {
	docs := 0
	for _, st := range p.def.Structs {
		if st.Document {
			docs++
			p.def.Document = st.Name
		}
		for _, f := range st.Fields {
			if isCollection(f.Type) && f.Omit == "absent" {
				return fmt.Errorf("line %d: %s is a collection omitted when absent, and an absent collection and an empty one are one thing on the wire; a collection says omit = \"zero\"", f.Line, f.Name)
			}
			if scalarTypes[f.Type] || encodableCollections[f.Type] {
				continue
			}
			if f.Type == "list<json>" {
				if !p.def.Envelope(st.Name) {
					return fmt.Errorf("line %d: %s carries list<json>, which travels in an envelope and not in a document; a document holding a list of documents is a document", f.Line, f.Name)
				}
				continue
			}
			if e := p.def.Repeated(f.Type); e != "" {
				if p.def.Envelope(st.Name) {
					return fmt.Errorf("line %d: %s.%s repeats %s, and an envelope is a parameter list rather than a tree; carry it as json", f.Line, st.Name, f.Name, e)
				}
				continue
			}
			if p.def.IsStruct(f.Type) {
				if p.def.Envelope(st.Name) {
					return fmt.Errorf("line %d: %s.%s is a struct, and an envelope is a parameter list rather than a document; nest it in a json field", f.Line, st.Name, f.Name)
				}
				if f.Omit == "zero" {
					return fmt.Errorf("line %d: %s is a struct, and a struct has no zero to omit on", f.Line, f.Name)
				}
				continue
			}
			return fmt.Errorf("line %d: this profile does not encode %s", f.Line, f.Type)
		}
	}
	if docs != 1 {
		return fmt.Errorf("exactly one struct carries (document = \"true\"); found %d", docs)
	}
	if !p.seenEnc {
		return fmt.Errorf("the definition declares no encoding, so it has no legal spelling")
	}
	if len(p.def.Refusals) == 0 {
		return fmt.Errorf("the definition declares no refusals, and a decoder's public surface is the word it says no with")
	}
	if err := p.validateVocabulary(); err != nil {
		return err
	}
	return p.validateProtocol()
}

func (p *parser) validateProtocol() error {
	pr := p.def.Proto
	if pr == nil {
		return nil
	}
	if len(pr.Operations) == 0 {
		return fmt.Errorf("protocol %s names no operation", pr.Name)
	}
	for _, role := range []struct{ what, name string }{{"request", pr.Request}, {"response", pr.Response}} {
		st := p.def.Struct(role.name)
		if st == nil {
			return fmt.Errorf("protocol %s carries its %s in %s, which this definition does not declare", pr.Name, role.what, role.name)
		}
		if st.Document {
			return fmt.Errorf("protocol %s carries its %s in the document; an envelope carries a document and is not one", pr.Name, role.what)
		}
		if st.UnknownFields != "grant" {
			return fmt.Errorf("protocol %s requires unknown_fields = \"grant\" for its %s envelope %s; an envelope grants", pr.Name, role.what, role.name)
		}
	}
	op := p.fieldOf(pr.Request, pr.OpField)
	switch {
	case op == nil:
		return fmt.Errorf("protocol %s names the operation in %s.%s, and %s has no such field", pr.Name, pr.Request, pr.OpField, pr.Request)
	case op.Type != "string":
		return fmt.Errorf("protocol %s names the operation in %s.%s, which is %s and not string", pr.Name, pr.Request, pr.OpField, op.Type)
	case op.Omit != "never":
		return fmt.Errorf("protocol %s makes %s.%s optional, and a request that does not say what it asks for is not a request", pr.Name, pr.Request, pr.OpField)
	}
	verdict := p.fieldOf(pr.Response, pr.Verdict)
	switch {
	case verdict == nil:
		return fmt.Errorf("protocol %s carries the verdict in %s.%s, and %s has no such field", pr.Name, pr.Response, pr.Verdict, pr.Response)
	case verdict.Type != "string":
		return fmt.Errorf("protocol %s carries the verdict in %s.%s, which is %s and not string", pr.Name, pr.Response, pr.Verdict, verdict.Type)
	case verdict.Omit != "zero":
		return fmt.Errorf("protocol %s does not omit %s.%s on zero; an answer that succeeded says nothing, and a peer reading a present-but-empty verdict has to know that empty means yes", pr.Name, pr.Response, pr.Verdict)
	}
	en := p.def.Enum(pr.Verdicts)
	if en == nil {
		return fmt.Errorf("protocol %s draws its verdicts from %s, which this definition does not declare as an enum", pr.Name, pr.Verdicts)
	}
	if en.Ann["unknown"] != "grant" {
		return fmt.Errorf("protocol %s draws its verdicts from %s, which refuses a member it has never heard of; a peer that cannot carry an unfamiliar verdict back to its caller turns the far side's answer into no answer", pr.Name, pr.Verdicts)
	}
	for _, m := range en.Members {
		if m.Name == pr.Unknown {
			return nil
		}
	}
	return fmt.Errorf("protocol %s answers an operation it does not know with %q, which is not a member of %s", pr.Name, pr.Unknown, pr.Verdicts)
}

func (p *parser) fieldOf(structName, fieldName string) *Field {
	st := p.def.Struct(structName)
	if st == nil {
		return nil
	}
	for i := range st.Fields {
		if st.Fields[i].Name == fieldName {
			return &st.Fields[i]
		}
	}
	return nil
}

func (p *parser) validateVocabulary() error {
	v := p.def.Vocab
	if v == nil {
		return nil
	}
	host := p.def.Struct(v.Of)
	if host == nil {
		return fmt.Errorf("vocabulary %s is derived from %s, which this definition does not declare", v.Name, v.Of)
	}
	if !host.Document {
		return fmt.Errorf("vocabulary %s is derived from %s, which is not the document; a derivation is a property of the whole record", v.Name, v.Of)
	}
	carrier := func(role, want string) (Field, error) {
		for _, f := range host.Fields {
			if f.Name != want {
				continue
			}
			if f.Type != "list<string>" {
				return Field{}, fmt.Errorf("vocabulary %s carries %s in %s.%s, which is %s and not list<string>", v.Name, role, v.Of, want, f.Type)
			}
			return f, nil
		}
		return Field{}, fmt.Errorf("vocabulary %s carries %s in %s.%s, and %s has no such field", v.Name, role, v.Of, want, v.Of)
	}
	if _, err := carrier("names", v.Names); err != nil {
		return err
	}
	crit, err := carrier("critical", v.Critical)
	if err != nil {
		return err
	}
	if crit.Omit == "never" {
		return fmt.Errorf("vocabulary %s makes %s.%s required, and a record that requires nothing of its reader has an empty list to write", v.Name, v.Of, v.Critical)
	}
	for i := range v.Terms {
		if err := p.resolveTerm(&v.Terms[i]); err != nil {
			return err
		}
	}
	return nil
}

// A predicate is one of three shapes and nothing else: the term is always
// present; a path from the document ends at an optional field, or one key into
// an opaque value; or a path ends at a string field and names the words it may
// hold. The path walks required structs only, so the generated test is a plain
// chain of field accesses in every language, and it enters an opaque value by
// exactly one key, because a reader that walks further is parsing a payload
// this definition does not own.
func (p *parser) resolveTerm(t *Term) error {
	v := p.def.Vocab
	if t.Always() {
		if t.Membership() {
			return fmt.Errorf("term %q says when = \"always\" and is = %q; a term that is always present names no words", t.Name, strings.Join(t.Is, ","))
		}
		return nil
	}
	segments := strings.Split(t.When, ".")
	current := p.def.Struct(v.Of)
	walked := v.Of
	for i, seg := range segments {
		f := fieldNamed(current, seg)
		if f == nil {
			return fmt.Errorf("term %q is derived from %s.%s, and %s has no field %q", t.Name, v.Of, t.When, walked, seg)
		}
		t.Path = append(t.Path, *f)
		walked += "." + seg
		last := i == len(segments)-1
		switch {
		case f.Type == "json" && !last:
			if t.Membership() {
				return fmt.Errorf("term %q reads %s for one of %q; a membership predicate reads a field this definition declares, never a value inside an opaque one", t.Name, t.When, strings.Join(t.Is, ","))
			}
			if len(segments) != i+2 {
				return fmt.Errorf("term %q walks %s, and a path enters an opaque value by exactly one key; %s is a payload this definition does not read", t.Name, t.When, walked)
			}
			t.Member = segments[i+1]
			if t.Member == "" {
				return fmt.Errorf("term %q names an empty member of %s", t.Name, walked)
			}
			return nil
		case !last && p.def.IsStruct(f.Type):
			if f.Omit != "never" {
				return fmt.Errorf("term %q walks %s, which is optional; a path walks required structs and tests its last step only", t.Name, walked)
			}
			current = p.def.Struct(f.Type)
		case !last:
			return fmt.Errorf("term %q walks %s, which is %s; a path enters a struct or an opaque value", t.Name, walked, f.Type)
		case t.Membership() && (f.Type != "string" || f.Grammar.Named()):
			return fmt.Errorf("term %q is derived from %s, which is %s; is = names the words a string field may hold", t.Name, walked, f.Type)
		case !t.Membership() && f.Omit == "never":
			return fmt.Errorf("term %q is derived from %s, which is required and therefore always present; a term that is always present says when = \"always\"", t.Name, walked)
		case i == 0 && (f.Name == v.Names || f.Name == v.Critical):
			return fmt.Errorf("term %q is derived from the list that carries it", t.Name)
		}
	}
	return nil
}

func fieldNamed(st *Struct, name string) *Field {
	if st == nil {
		return nil
	}
	for i := range st.Fields {
		if st.Fields[i].Name == name {
			return &st.Fields[i]
		}
	}
	return nil
}

func membershipWords(ann map[string]string, line int, term string) ([]string, error) {
	raw, ok := ann["is"]
	if !ok {
		return nil, nil
	}
	var words []string
	for _, w := range strings.Split(raw, ",") {
		w = strings.TrimSpace(w)
		if w == "" {
			return nil, fmt.Errorf("line %d: %q says is = %q, and every word in it is non-empty", line, term, raw)
		}
		if contains(words, w) {
			return nil, fmt.Errorf("line %d: %q names %q twice in is", line, term, w)
		}
		words = append(words, w)
	}
	return words, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

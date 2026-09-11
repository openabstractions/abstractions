package main

import "strings"

type Encoding struct {
	Escape        string
	Indent        int
	MapKeys       string
	Numbers       string
	Opaque        string
	Terminator    string
	DuplicateKeys string
	DepthLimit    int
}

func (e Encoding) TrailingNewline() bool     { return e.Terminator == "newline" }
func (e Encoding) EscapeNonASCII() bool      { return e.Escape == "ascii" }
func (e Encoding) RefuseDuplicateKeys() bool { return e.DuplicateKeys == "refuse" }

type Grammar struct {
	Read  string
	Write string
}

func (g Grammar) Named() bool { return g.Read != "" }

type Typedef struct {
	Alias   string
	Base    string
	Grammar Grammar
}

type Field struct {
	ID      int
	Type    string
	Alias   string
	Name    string
	Omit    string
	Grammar Grammar
	Ann     map[string]string
	Line    int
}

type Struct struct {
	Name          string
	Fields        []Field
	Ann           map[string]string
	Document      bool
	UnknownFields string
}

func (s Struct) RefuseUnknown() bool    { return s.UnknownFields == "refuse" }
func (s Struct) PreservesUnknown() bool { return s.UnknownFields == "preserve" }

func (s *Definition) PreservesUnknown() bool {
	for _, st := range s.Structs {
		if st.PreservesUnknown() {
			return true
		}
	}
	return false
}

// Path is the fields a predicate walks from the document, root first, and is
// nil for "always". Member is the key one level into the opaque value the path
// ends at, and "" when the path ends at a field. Is is the words a membership
// term accepts, and nil for a presence term.
type Term struct {
	ID            int
	Name          string
	When          string
	Is            []string
	StripCritical bool
	Path          []Field
	Member        string
}

func (t Term) Always() bool     { return t.When == "always" }
func (t Term) Membership() bool { return len(t.Is) > 0 }
func (t Term) Last() Field      { return t.Path[len(t.Path)-1] }

type Vocabulary struct {
	Name     string
	Terms    []Term
	Of       string
	Names    string
	Critical string
}

func (v *Vocabulary) UsesMember() bool {
	for _, t := range v.Terms {
		if t.Member != "" {
			return true
		}
	}
	return false
}

type Member struct {
	ID   int
	Name string
	Ann  map[string]string
}

type Enum struct {
	Name    string
	Members []Member
	Ann     map[string]string
}

type Const struct {
	Name    string
	Type    string
	Strings []string
	Ints    []int64
}

type Refusal struct {
	ID    int
	Word  string
	Stage string
}

type Operation struct {
	ID   int
	Name string
}

type Protocol struct {
	Name       string
	Operations []Operation
	Request    string
	Response   string
	OpField    string
	Verdict    string
	Verdicts   string
	Unknown    string
}

type Method struct {
	Oneway bool
	Result Field
	Doc    string
	Name   string
	Args   []Field
}
type Service struct {
	Doc      string
	WireName string
	Name     string
	Methods  []Method
}

type Definition struct {
	Namespaces map[string]string
	Services   []Service
	Encoding   Encoding
	Typedefs   []Typedef
	Structs    []Struct
	Enums      []Enum
	Consts     []Const
	Vocab      *Vocabulary
	Refusals   []Refusal
	Proto      *Protocol
	Document   string

	byName map[string]*Struct
}

func (s *Definition) Envelope(name string) bool {
	return s.Proto != nil && (s.Proto.Request == name || s.Proto.Response == name)
}

func (s *Definition) Enum(name string) *Enum {
	for i := range s.Enums {
		if s.Enums[i].Name == name {
			return &s.Enums[i]
		}
	}
	return nil
}

func (s *Definition) ListOfJSON() bool {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if f.Type == "list<json>" {
				return true
			}
		}
	}
	return false
}

func (s *Definition) Words() []string {
	out := make([]string, len(s.Refusals))
	for i, r := range s.Refusals {
		out[i] = r.Word
	}
	return out
}

func (s *Definition) IsStruct(t string) bool { _, ok := s.byName[t]; return ok }

func listElement(t string) string {
	if strings.HasPrefix(t, "list<") && strings.HasSuffix(t, ">") {
		return t[len("list<") : len(t)-1]
	}
	return ""
}

// Repeated names the struct a field repeats, and "" for every other type.
func (s *Definition) Repeated(t string) string {
	if e := listElement(t); s.IsStruct(e) {
		return e
	}
	return ""
}

// DocumentUses asks whether a type is reached outside the envelope, because an
// indented helper and a flat one are emitted by different questions.
func (s *Definition) DocumentUses(pred func(Field) bool) bool {
	for _, st := range s.Structs {
		if s.Envelope(st.Name) {
			continue
		}
		for _, f := range st.Fields {
			if pred(f) {
				return true
			}
		}
	}
	return false
}

func (s *Definition) HasRepeated() bool {
	return s.DocumentUses(func(f Field) bool { return s.Repeated(f.Type) != "" })
}

func (s *Definition) StringMapDocument() bool {
	return s.DocumentUses(func(f Field) bool { return f.Type == "map<string,string>" })
}

func (s *Definition) HasStringMap() bool {
	return s.StringMapDocument() || envelopeUses(s, "map<string,string>")
}

func (s *Definition) Struct(name string) *Struct { return s.byName[name] }

func (s *Definition) Timestamps() bool {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if f.Grammar.Named() {
				return true
			}
		}
	}
	return false
}

func (f Field) Ident(lang string) string {
	if n, ok := f.Ann[lang+".name"]; ok {
		return n
	}
	return f.Name
}

type separators struct {
	before      []string
	flag        bool
	clearFlagTo int
	closeAlways bool
}

func plan(st Struct) separators {
	p := separators{before: make([]string, len(st.Fields)), clearFlagTo: len(st.Fields)}
	if st.PreservesUnknown() {
		p.flag = true
		for i := range p.before {
			p.before[i] = "flag"
		}
		return p
	}
	written := false
	for i, f := range st.Fields {
		switch {
		case i == 0:
			p.before[i] = "none"
		case written:
			p.before[i] = "always"
		default:
			p.before[i] = "flag"
			p.flag = true
		}
		if f.Omit == "never" && !written {
			written = true
			p.clearFlagTo = i
		}
	}
	p.closeAlways = written
	if !written && len(st.Fields) > 0 {
		p.flag = true
	}
	return p
}

func (p separators) clears(i int) bool { return p.flag && i < p.clearFlagTo }

func (e Enum) MemberAnn() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range e.Members {
		for k := range m.Ann {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

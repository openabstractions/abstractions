package main

import (
	"fmt"
	"sort"
	"strings"
)

// A surface is a declaration an artefact can carry: a struct, an enum, a
// constant, the vocabulary, the protocol. The encoding block, the refusal words
// and the typedefs are not surfaces — they bind everything a definition emits,
// so there is no artefact that carries one and not the others.
func surfaces(s *Definition) []string {
	var out []string
	for _, st := range s.Structs {
		out = append(out, st.Name)
	}
	for _, en := range s.Enums {
		out = append(out, en.Name)
	}
	for _, c := range s.Consts {
		out = append(out, c.Name)
	}
	if s.Vocab != nil {
		out = append(out, s.Vocab.Name)
	}
	if s.Proto != nil {
		out = append(out, s.Proto.Name)
	}
	for _, svc := range s.Services {
		out = append(out, svc.Name)
	}
	return out
}

// needs is every surface one surface cannot be emitted without. A struct needs
// every struct its fields carry, and a struct reached only through a repeated
// field is carried the same way. Two further edges point the way a reader does
// not expect and are the reason a selection is closed rather than filtered:
//
// A document needs its vocabulary. A record codec that decodes the fields and
// skips the derivation accepts records the contract refuses, and it compiles
// perfectly while doing it.
//
// An envelope struct needs its protocol. The protocol is what makes it an
// envelope: without it the same struct is a document and is emitted indented,
// with a different encoder under the same name.
func needs(s *Definition, name string) []string {
	var out []string
	add := func(n string) {
		if n != "" && n != name && !contains(out, n) {
			out = append(out, n)
		}
	}
	if st := s.Struct(name); st != nil {
		for _, f := range st.Fields {
			if s.IsStruct(f.Type) || s.Enum(f.Type) != nil {
				add(f.Type)
			}
			add(s.Repeated(f.Type))
		}
		if s.Vocab != nil && s.Vocab.Of == name {
			add(s.Vocab.Name)
		}
		if s.Envelope(name) {
			add(s.Proto.Name)
		}
	}
	if s.Vocab != nil && s.Vocab.Name == name {
		add(s.Vocab.Of)
	}
	if s.Proto != nil && s.Proto.Name == name {
		add(s.Proto.Request)
		add(s.Proto.Response)
		add(s.Proto.Verdicts)
	}
	for _, svc := range s.Services {
		if svc.Name == name {
			for _, m := range svc.Methods {
				if !m.Oneway {
					if s.IsStruct(m.Result.Type) || s.Enum(m.Result.Type) != nil {
						add(m.Result.Type)
					}
					add(s.Repeated(m.Result.Type))
				}
				for _, f := range m.Args {
					if s.IsStruct(f.Type) || s.Enum(f.Type) != nil {
						add(f.Type)
					}
					add(s.Repeated(f.Type))
				}
			}
		}
	}
	return out
}

// selected narrows a definition to the surfaces a target declares. The list is
// the whole contents of the artefact and not a set of roots: what a selection
// pulls in behind it is exactly what a reader of scripts/generate.targets would
// otherwise have to compute in their head, and the artefact is compared against
// that line byte for byte. So an unclosed list is refused, and the refusal is
// the line that would have worked.
func selected(s *Definition, only []string) (*Definition, error) {
	if len(only) == 0 {
		return s, nil
	}
	all := surfaces(s)
	var unknown []string
	for _, n := range only {
		if !contains(all, n) {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("the selection names %s, which this definition does not declare; it declares %s",
			strings.Join(unknown, ", "), strings.Join(all, ", "))
	}
	var missing []string
	for _, n := range only {
		for _, d := range needs(s, n) {
			if contains(only, d) {
				continue
			}
			if m := d + ", which " + n + " needs"; !contains(missing, m) {
				missing = append(missing, m)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("the selection does not close: it leaves out %s. A selection is the whole contents of the artefact, so name them too",
			strings.Join(missing, "; "))
	}
	return prune(s, only), nil
}

func prune(s *Definition, only []string) *Definition {
	out := &Definition{
		Encoding:   s.Encoding,
		Namespaces: s.Namespaces,
		Typedefs:   s.Typedefs,
		Refusals:   s.Refusals,
		byName:     map[string]*Struct{},
	}
	for _, st := range s.Structs {
		if contains(only, st.Name) {
			out.Structs = append(out.Structs, st)
		}
	}
	for i := range out.Structs {
		out.byName[out.Structs[i].Name] = &out.Structs[i]
	}
	for _, en := range s.Enums {
		if contains(only, en.Name) {
			out.Enums = append(out.Enums, en)
		}
	}
	for _, c := range s.Consts {
		if contains(only, c.Name) {
			out.Consts = append(out.Consts, c)
		}
	}
	if s.Vocab != nil && contains(only, s.Vocab.Name) {
		out.Vocab = s.Vocab
	}
	if s.Proto != nil && contains(only, s.Proto.Name) {
		out.Proto = s.Proto
	}
	if contains(only, s.Document) {
		out.Document = s.Document
	}
	for _, svc := range s.Services {
		if contains(only, svc.Name) {
			out.Services = append(out.Services, svc)
		}
	}
	return out
}

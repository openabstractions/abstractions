package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type IncludedDefinition struct {
	Alias, Path string
	Def         *Definition
}
type ImportedRecord struct {
	Alias, Name string
	Def         *Definition
}

func loadDefinition(path string) (*Definition, error) {
	active := map[string]bool{}
	loaded := map[string]*Definition{}
	var load func(string) (*Definition, error)
	load = func(path string) (*Definition, error) {
		absolute, e := filepath.Abs(path)
		if e != nil {
			return nil, e
		}
		absolute, e = filepath.EvalSymlinks(absolute)
		if e != nil {
			return nil, e
		}
		absolute = filepath.Clean(absolute)
		if d := loaded[absolute]; d != nil {
			return d, nil
		}
		if len(active) >= 64 {
			return nil, fmt.Errorf("include depth exceeds 64 definitions")
		}
		if active[absolute] {
			return nil, fmt.Errorf("cyclic include: %s", absolute)
		}
		active[absolute] = true
		defer delete(active, absolute)
		data, e := os.ReadFile(absolute)
		if e != nil {
			return nil, e
		}
		d, err := parseWithIncludes(string(data), func(relative string) (*Definition, error) {
			return load(filepath.Join(filepath.Dir(absolute), relative))
		})
		if err == nil {
			loaded[absolute] = d
		}
		return d, err
	}
	return load(path)
}

func (p *parser) includeDef() error {
	p.i++
	t := p.next()
	if t.kind != "string" {
		return fmt.Errorf("include requires a quoted relative Thrift path")
	}
	if filepath.IsAbs(t.text) || strings.Contains(t.text, "\\") || filepath.Ext(t.text) != ".thrift" {
		return fmt.Errorf("include requires a relative .thrift path with forward slashes")
	}
	alias := strings.TrimSuffix(filepath.Base(t.text), ".thrift")
	if !serviceIdentifier(alias) {
		return fmt.Errorf("invalid include alias %q", alias)
	}
	for _, imp := range p.def.Imports {
		if imp.Alias == alias {
			return fmt.Errorf("ambiguous include alias %s", alias)
		}
	}
	if p.load == nil {
		return fmt.Errorf("include %s requires a source file location", t.text)
	}
	d, e := p.load(t.text)
	if e != nil {
		return e
	}
	p.def.Imports = append(p.def.Imports, IncludedDefinition{alias, t.text, d})
	return nil
}

func (s *Definition) importedRecord(name string) (ImportedRecord, bool) {
	if r, ok := s.Foreign[name]; ok {
		return r, true
	}
	parts := strings.Split(name, ".")
	if len(parts) != 2 {
		return ImportedRecord{}, false
	}
	for _, imp := range s.Imports {
		if imp.Alias == parts[0] && imp.Def.byName[parts[1]] != nil {
			return ImportedRecord{imp.Alias, parts[1], imp.Def}, true
		}
	}
	return ImportedRecord{}, false
}

// Backend-local aliases retain the imported language type. No imported fields
// or codec bodies are copied into the consuming definition.
func importCarriers(s *Definition) *Definition {
	o := *s
	o.Foreign = map[string]ImportedRecord{}
	names := map[string]string{}
	var lower func(string) string
	lower = func(t string) string {
		if e := listElement(t); e != "" {
			return "list<" + lower(e) + ">"
		}
		if imp, ok := s.importedRecord(t); ok {
			if name := names[t]; name != "" {
				return name
			}
			name := fmt.Sprintf("OAImported%d", len(names))
			names[t] = name
			o.Foreign[name] = imp
			return name
		}
		return t
	}
	o.Structs = append([]Struct(nil), s.Structs...)
	o.byName = map[string]*Struct{}
	for i := range o.Structs {
		o.Structs[i].Fields = append([]Field(nil), o.Structs[i].Fields...)
		for j := range o.Structs[i].Fields {
			o.Structs[i].Fields[j].Type = lower(o.Structs[i].Fields[j].Type)
		}
		o.byName[o.Structs[i].Name] = &o.Structs[i]
	}
	o.Services = append([]Service(nil), s.Services...)
	for i := range o.Services {
		o.Services[i].Methods = append([]Method(nil), o.Services[i].Methods...)
		for j := range o.Services[i].Methods {
			m := &o.Services[i].Methods[j]
			m.Result.Type = lower(m.Result.Type)
			m.Args = append([]Field(nil), m.Args...)
			for k := range m.Args {
				m.Args[k].Type = lower(m.Args[k].Type)
			}
		}
	}
	return &o
}
func foreignNames(s *Definition) []string {
	var names []string
	for n := range s.Foreign {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func validateIncludes(s *Definition, lang string) error {
	if len(s.Imports) == 0 && !s.NamedCodecs {
		return nil
	}
	if lang != "go" && lang != "cpp" && lang != "python" && lang != "docs" {
		return fmt.Errorf("%s backend does not support typed includes or named record codecs", lang)
	}
	if err := validateImportedGraph(s); err != nil {
		return err
	}
	for _, n := range surfaces(s) {
		if strings.Contains(n, ".") {
			return fmt.Errorf("declaration %s uses a qualified name; only imported type references may be qualified", n)
		}
		if strings.HasPrefix(n, "OAImported") || strings.HasPrefix(n, "oa_dependency_") {
			return fmt.Errorf("reserved imported carrier name %s", n)
		}
	}
	seenNamespaces := map[string]bool{}
	seenGoPaths := map[string]bool{}
	for _, imp := range s.Imports {
		if err := validateImportedGraph(imp.Def); err != nil {
			return err
		}
		ns := namespaceFor(imp.Def.Namespaces, lang)
		if seenNamespaces[ns] && ns != "" {
			return fmt.Errorf("ambiguous included %s namespace %s", lang, ns)
		}
		seenNamespaces[ns] = true
		if namespaceKeyword(lang, imp.Alias) {
			return fmt.Errorf("include alias %s is reserved in %s", imp.Alias, lang)
		}
		for _, name := range surfaces(s) {
			if name == imp.Alias {
				return fmt.Errorf("include alias %s collides with declaration", name)
			}
		}
		if !s.NoIPC && len(s.Services) > 0 && s.Encoding.RefuseDuplicateKeys() && includeAllowsDuplicateKeys(imp.Def) {
			return fmt.Errorf("include %s permits last-wins duplicate keys but the IPC envelope refuses them; use --no-ipc or compatible encoding policies", imp.Alias)
		}
		if namespaceFor(imp.Def.Namespaces, lang) == "" && lang != "docs" {
			return fmt.Errorf("include %s requires a %s namespace", imp.Alias, lang)
		}
		if lang == "go" && s.GoImports[imp.Alias] != "" {
			path := s.GoImports[imp.Alias]
			if seenGoPaths[path] {
				return fmt.Errorf("ambiguous Go package mapping %s", path)
			}
			seenGoPaths[path] = true
		}
		if lang == "go" && s.GoImports[imp.Alias] == "" {
			return fmt.Errorf("include %s requires --go-import=%s=<package-path>", imp.Alias, imp.Alias)
		}
		if lang != "docs" && namespaceFor(imp.Def.Namespaces, lang) == namespaceFor(s.Namespaces, lang) {
			return fmt.Errorf("include %s collides with importing %s namespace", imp.Alias, lang)
		}
	}
	return nil
}

func validateImportedGraph(s *Definition) error {
	states := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if states[name] == 1 {
			return fmt.Errorf("cyclic record reference %s", name)
		}
		if states[name] == 2 {
			return nil
		}
		st := s.byName[name]
		if st == nil {
			return nil
		}
		states[name] = 1
		for _, f := range st.Fields {
			n := f.Type
			if e := listElement(n); e != "" {
				n = e
			}
			if err := visit(n); err != nil {
				return err
			}
		}
		states[name] = 2
		return nil
	}
	declared := map[string]bool{}
	for _, n := range surfaces(s) {
		declared[lower(n)] = true
	}
	for _, st := range s.Structs {
		if err := visit(st.Name); err != nil {
			return err
		}
		for _, n := range []string{"Encode" + st.Name, "Decode" + st.Name, "Encode" + st.Name + "At", "Decode" + st.Name + "At", "encode_" + lower(st.Name), "decode_" + lower(st.Name), "encode_" + lower(st.Name) + "_at", "decode_" + lower(st.Name) + "_at", "check_" + lower(st.Name)} {
			if declared[lower(n)] {
				return fmt.Errorf("named codec collision %s", n)
			}
		}
	}
	return nil
}

func includeAllowsDuplicateKeys(s *Definition) bool {
	if !s.Encoding.RefuseDuplicateKeys() {
		return true
	}
	for _, imp := range s.Imports {
		if includeAllowsDuplicateKeys(imp.Def) {
			return true
		}
	}
	return false
}

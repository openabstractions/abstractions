package main

// The compatibility checker and the definition hash ([DEF-V1], [DEF-V2];
// idl/CONVENTIONS.md [CONV-V1]). A service version is its wire_name. Within one
// version a method may be added; a closed struct or a closed enum may not gain a
// member, and nothing published may lose or change one. The hash names the wire
// meaning of one service version, so two definitions that differ only in
// comments, docs, language names, field ids, the order of declarations or the
// order of an enum's members hash alike. A struct's members and a method's
// arguments keep their order, because a writer emits them in that order.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// shape is one declaration a service reaches, in canonical form.
type shape struct {
	kind       string // "struct" or "enum"
	closed     bool   // unknown_fields="refuse" or unknown="refuse"
	header     string // everything about the declaration except its members
	members    []string
	lines      map[string]string // member name -> canonical member line
	require    map[string]bool   // struct members that are required
	bounds     map[string]Field  // struct members that declare a length, range or pattern
	maxEncoded int64             // aggregate record value-span ceiling; zero is unrestricted
}

// serviceShape is one service version in canonical form.
type serviceShape struct {
	wire     string
	encoding string
	errors   []string // the declared error codes, sorted
	methods  map[string]string
	decls    map[string]*shape
	bounds   map[string]Field     // "<method>.<argument>" for each argument that declares a bound
	refusals map[string][]Refusal // owning definition prefix -> refusal precedence
}

// declaresBound reports whether f carries a length, range or pattern. Its
// text joins the canonical form only then, so a definition that declares
// none hashes as it did before constraints existed.
func declaresBound(f Field) bool { return constraintText(f) != "" }

func encodingLine(e Encoding) string {
	return fmt.Sprintf("encoding escape=%s indent=%d map_keys=%s numbers=%s opaque=%s terminator=%s duplicate_keys=%s depth_limit=%d",
		e.Escape, e.Indent, e.MapKeys, e.Numbers, e.Opaque, e.Terminator, e.DuplicateKeys, e.DepthLimit)
}

func fieldLine(f Field, qualify func(string) string) string {
	presence := "optional"
	if f.Omit == "never" {
		presence = "required"
	}
	line := f.Name + " " + presence + " " + qualifyType(f.Type, qualify) + " omit=" + f.Omit
	if f.Grammar.Named() {
		line += " read=" + f.Grammar.Read + " write=" + f.Grammar.Write
	}
	for _, k := range []string{"equals", "equals_refusal", "service_raw"} {
		if v, ok := f.Ann[k]; ok {
			line += " " + k + "=" + strconv.Quote(v)
		}
	}
	return line
}

// typeNames are the declared names a type string refers to, in order.
func typeNames(t string) []string {
	return strings.FieldsFunc(t, func(r rune) bool { return r == '<' || r == '>' || r == ',' || r == ' ' })
}

func qualifyType(t string, qualify func(string) string) string {
	var b strings.Builder
	word := strings.Builder{}
	flush := func() {
		if word.Len() > 0 {
			b.WriteString(qualify(word.String()))
			word.Reset()
		}
	}
	for _, r := range t {
		if r == '<' || r == '>' || r == ',' || r == ' ' {
			flush()
			if r != ' ' {
				b.WriteRune(r)
			}
			continue
		}
		word.WriteRune(r)
	}
	flush()
	return b.String()
}

// reach is a declaration found from a service: the definition that owns it, its
// local name, and the name the canonical form gives it.
type reach struct {
	def   *Definition
	name  string
	label string
}

// resolve finds what a type name inside def refers to. A qualified name follows
// the include whose alias it carries; every scalar and container word resolves
// to nothing.
func resolve(def *Definition, prefix, name string) (reach, bool) {
	if alias, local, ok := strings.Cut(name, "."); ok {
		for _, imp := range def.Imports {
			if imp.Alias == alias && imp.Def != nil {
				return resolve(imp.Def, prefix+alias+".", local)
			}
		}
		return reach{}, false
	}
	if def.Struct(name) != nil || def.Enum(name) != nil {
		return reach{def, name, prefix + name}, true
	}
	return reach{}, false
}

func shapeOf(svc Service, def *Definition) serviceShape {
	out := serviceShape{wire: svc.WireName, encoding: encodingLine(def.Encoding), methods: map[string]string{}, decls: map[string]*shape{}, bounds: map[string]Field{}}
	out.refusals = map[string][]Refusal{"": def.Refusals}
	out.errors = append([]string(nil), svc.ErrorCodes...)
	sort.Strings(out.errors)
	var queue []reach
	seen := map[string]bool{}
	visit := func(owner *Definition, prefix, t string) {
		for _, n := range typeNames(t) {
			if r, ok := resolve(owner, prefix, n); ok && !seen[r.label] {
				seen[r.label] = true
				queue = append(queue, r)
			}
		}
	}
	rootQualify := func(n string) string { return n }
	for _, m := range svc.Methods {
		var args []string
		for _, a := range m.Args {
			// Declaration order: a call writes its arguments in it.
			args = append(args, fieldLine(a, rootQualify))
			out.bounds[m.Name+"."+a.Name] = a
			visit(def, "", a.Type)
		}
		visit(def, "", m.Result.Type)
		result := qualifyType(m.Result.Type, rootQualify)
		out.bounds[m.Name+".<result>"] = m.Result
		if m.Result.Grammar.Named() {
			result += " read=" + m.Result.Grammar.Read + " write=" + m.Result.Grammar.Write
		}
		out.methods[m.Name] = fmt.Sprintf("method %s oneway=%t result=%s args=[%s]", m.Name, m.Oneway, result, strings.Join(args, "; "))
	}
	for len(queue) > 0 {
		r := queue[0]
		queue = queue[1:]
		prefix := strings.TrimSuffix(r.label, r.name)
		out.refusals[prefix] = r.def.Refusals
		qualify := func(n string) string {
			if res, ok := resolve(r.def, prefix, n); ok {
				return res.label
			}
			return n
		}
		sh := &shape{lines: map[string]string{}, require: map[string]bool{}, bounds: map[string]Field{}}
		if st := r.def.Struct(r.name); st != nil {
			sh.kind = "struct"
			sh.closed = st.RefuseUnknown()
			sh.maxEncoded = st.maxEncoded()
			encoding := r.def.Encoding
			if st.CodecDepthLimit > 0 {
				encoding.DepthLimit = st.CodecDepthLimit
			}
			sh.header = fmt.Sprintf("struct %s unknown_fields=%s document=%t %s", r.label, st.UnknownFields, st.Document, encodingLine(encoding))
			for _, f := range st.Fields {
				sh.members = append(sh.members, f.Name)
				sh.lines[f.Name] = fieldLine(f, qualify)
				sh.require[f.Name] = f.Omit == "never"
				sh.bounds[f.Name] = f
				visit(r.def, prefix, f.Type)
			}
		} else {
			en := r.def.Enum(r.name)
			sh.kind = "enum"
			sh.closed = en.Ann["unknown"] == "refuse"
			sh.header = fmt.Sprintf("enum %s unknown=%s", r.label, en.Ann["unknown"])
			for _, m := range en.Members {
				sh.members = append(sh.members, m.Name)
				sh.lines[m.Name] = "member " + m.Name + " wire=" + strconv.Quote(m.WireName())
			}
			// An enum is written as its member's word, so the order of its
			// members changes no byte.
			sort.Strings(sh.members)
		}
		out.decls[r.label] = sh
	}
	return out
}

// canonical is the text the definition hash is taken over: the service's wire
// name, encoding, error codes, methods by name with their arguments in
// declaration order, and every declaration they reach by name, a struct's
// members in declaration order and an enum's members by name.
func (s serviceShape) canonical() string {
	lines := []string{"service " + s.wire, s.encoding, "errors " + strings.Join(s.errors, ",")}
	for _, name := range sortedKeys(s.methods) {
		lines = append(lines, s.methods[name])
	}
	for _, name := range sortedKeys(s.bounds) {
		if declaresBound(s.bounds[name]) {
			lines = append(lines, "bound "+name+" "+constraintText(s.bounds[name]))
		}
	}
	for _, name := range sortedKeys(s.decls) {
		d := s.decls[name]
		lines = append(lines, d.header)
		if d.maxEncoded > 0 {
			lines = append(lines, fmt.Sprintf("  max_encoded=%d", d.maxEncoded))
		}
		for _, m := range d.members {
			lines = append(lines, "  "+d.lines[m])
			if f, ok := d.bounds[m]; ok && declaresBound(f) {
				lines = append(lines, "    bound "+constraintText(f))
			}
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func (s serviceShape) hash() string {
	sum := sha256.Sum256([]byte(s.canonical()))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// definitionHashes is every service version of def with its hash, in
// declaration order.
func definitionHashes(def *Definition) [][2]string {
	var out [][2]string
	for _, svc := range def.Services {
		out = append(out, [2]string{svc.WireName, shapeOf(svc, def).hash()})
	}
	return out
}

// compatibility lists every change from old to new that a service version
// which keeps its wire_name may not carry. An empty list is compatible.
func compatibility(published, candidate *Definition) []string {
	var problems []string
	next := map[string]Service{}
	for _, svc := range candidate.Services {
		next[svc.WireName] = svc
	}
	for _, before := range published.Services {
		after, kept := next[before.WireName]
		if !kept {
			continue // retired, or replaced by a new version: nothing published is changed
		}
		was, is := shapeOf(before, published), shapeOf(after, candidate)
		w := before.WireName
		// DEF-R1 IDs are contiguous priority ranks, not numeric error identities.
		// New staged words may interleave, but known words retain their stages
		// and relative precedence in each definition the service reaches.
		for _, prefix := range sortedKeys(was.refusals) {
			current, reached := is.refusals[prefix]
			if !reached {
				continue // a changed method/type already reports lost reachability
			}
			stages := map[string]string{}
			var oldWords, newWords []string
			for _, refusal := range current {
				stages[refusal.Word] = refusal.Stage
				newWords = append(newWords, refusal.Word)
			}
			for _, refusal := range was.refusals[prefix] {
				oldWords = append(oldWords, refusal.Word)
				stage, kept := stages[refusal.Word]
				if !kept {
					problems = append(problems, fmt.Sprintf("%s: refusal %s%s was removed", w, prefix, refusal.Word))
				} else if stage != refusal.Stage {
					problems = append(problems, fmt.Sprintf("%s: refusal %s%s changed stage from %s to %s", w, prefix, refusal.Word, refusal.Stage, stage))
				}
			}
			if !sameOrder(oldWords, newWords) {
				problems = append(problems, fmt.Sprintf("%s: refusals %schanged relative precedence", w, prefix))
			}
		}
		if was.encoding != is.encoding {
			problems = append(problems, fmt.Sprintf("%s: the encoding declaration changed; every published byte changes with it", w))
		}
		// ServiceErrorCode is open, so a reader carries a code it never saw and
		// a code may be added. A published code stays declared.
		declared := map[string]bool{}
		for _, code := range is.errors {
			declared[code] = true
		}
		for _, code := range was.errors {
			if !declared[code] {
				problems = append(problems, fmt.Sprintf("%s: error code %s was removed", w, code))
			}
		}
		for _, name := range sortedKeys(was.methods) {
			now, ok := is.methods[name]
			switch {
			case !ok:
				problems = append(problems, fmt.Sprintf("%s: method %s was removed", w, name))
			case now != was.methods[name]:
				problems = append(problems, fmt.Sprintf("%s: method %s changed its arguments or result", w, name))
			}
		}
		for _, name := range sortedKeys(is.bounds) {
			if reason := narrowed(was.bounds, name, is.bounds[name]); reason != "" {
				problems = append(problems, fmt.Sprintf("%s: argument %s %s", w, name, reason))
			}
		}
		for _, label := range sortedKeys(was.decls) {
			d, ok := is.decls[label]
			if !ok {
				continue // no longer reached: a changed method already says so
			}
			p := was.decls[label]
			if d.maxEncoded > 0 && (p.maxEncoded == 0 || d.maxEncoded < p.maxEncoded) {
				problems = append(problems, fmt.Sprintf("%s: %s %s narrowed max_encoded", w, p.kind, label))
			}
			for _, m := range sortedKeys(d.bounds) {
				if _, kept := p.lines[m]; !kept {
					continue // a new member is judged by the member rules
				}
				if reason := narrowed(p.bounds, m, d.bounds[m]); reason != "" {
					problems = append(problems, fmt.Sprintf("%s: %s %s member %s %s", w, p.kind, label, m, reason))
				}
			}
			if p.header != d.header {
				problems = append(problems, fmt.Sprintf("%s: %s %s changed what its reader does with an unknown member or how it is encoded", w, p.kind, label))
			}
			for _, m := range p.members {
				line, ok := d.lines[m]
				switch {
				case !ok:
					problems = append(problems, fmt.Sprintf("%s: %s %s lost member %s", w, p.kind, label, m))
				case line != p.lines[m]:
					problems = append(problems, fmt.Sprintf("%s: %s %s changed member %s", w, p.kind, label, m))
				}
			}
			if p.kind == "struct" && !sameOrder(p.members, d.members) {
				problems = append(problems, fmt.Sprintf("%s: struct %s reordered its members; a writer emits them in declaration order, so every published record changes", w, label))
			}
			for _, m := range d.members {
				if _, ok := p.lines[m]; ok {
					continue
				}
				switch {
				case p.closed:
					problems = append(problems, fmt.Sprintf("%s: closed %s %s gained member %s; a published reader refuses it, so it needs a new service version", w, p.kind, label, m))
				case d.require[m]:
					problems = append(problems, fmt.Sprintf("%s: struct %s gained required member %s; a published writer omits it", w, label, m))
				}
			}
		}
	}
	return problems
}

// narrowed says how candidate's declared bounds refuse a value the published
// bounds of the same member admitted, or "" when they do not. A first declared
// constraint also narrows the generated published language and requires the
// service's next version ([DEF-V1]).
func narrowed(published map[string]Field, name string, candidate Field) string {
	before, ok := published[name]
	if !ok {
		return ""
	}
	find := func(f Field, kind checkKind, element, class string) *check {
		for i := range f.Checks {
			if f.Checks[i].Kind == kind && f.Checks[i].Element == element && (kind != checkPattern || f.Checks[i].Class == class) {
				return &f.Checks[i]
			}
		}
		return nil
	}
	inside := func(was, is *check) bool {
		lower := func(s string) (int64, bool) {
			if s == "" {
				return 0, false
			}
			n, _ := strconv.ParseInt(s, 10, 64)
			return n, true
		}
		wasMin, wasHasMin := lower(was.Min)
		isMin, isHasMin := lower(is.Min)
		wasMax, wasHasMax := lower(was.Max)
		isMax, isHasMax := lower(is.Max)
		if isHasMin && (!wasHasMin || isMin > wasMin) {
			return false
		}
		if isHasMax && (!wasHasMax || isMax < wasMax) {
			return false
		}
		return true
	}
	for i := range candidate.Checks {
		is := &candidate.Checks[i]
		was := find(before, is.Kind, is.Element, is.Class)
		if was == nil && (is.Kind == checkLength || is.Kind == checkRange || is.Kind == checkPattern) {
			if is.Kind == checkPattern {
				for _, prior := range before.Checks {
					if prior.Kind == checkPattern && prior.Element == is.Element {
						return "changed its declared pattern from " + strconv.Quote(constraintText(before)) + " to " + strconv.Quote(constraintText(candidate)) + "; a published writer may send what the new class refuses"
					}
				}
			}
			return "added declared constraints " + strconv.Quote(constraintText(candidate)) + "; a published writer may send what the new bound refuses"
		}
		if was != nil && (is.Kind == checkRange || is.Kind == checkLength) && !inside(was, is) {
			return "narrowed its declared bounds from " + strconv.Quote(constraintText(before)) + " to " + strconv.Quote(constraintText(candidate)) + "; a published writer may send what the new bound refuses"
		}
	}
	return ""
}

// sameOrder reports whether the members both lists hold appear in the same
// relative order. An added or removed member is judged on its own.
func sameOrder(was, is []string) bool {
	position := map[string]int{}
	for i, m := range is {
		position[m] = i
	}
	last := -1
	for _, m := range was {
		i, kept := position[m]
		if !kept {
			continue
		}
		if i < last {
			return false
		}
		last = i
	}
	return true
}

const compatUsage = "usage: gen --compat <published.thrift> <candidate.thrift>\n       gen --hash <definition.thrift>"

// runVersioning answers --compat and --hash; handled reports whether args named one.
func runVersioning(args []string, stdout io.Writer) (handled bool, err error) {
	if len(args) == 0 || (args[0] != "--compat" && args[0] != "--hash") {
		return false, nil
	}
	if args[0] == "--hash" {
		if len(args) != 2 {
			return true, fmt.Errorf("%s", compatUsage)
		}
		def, err := loadDefinition(args[1])
		if err != nil {
			return true, fmt.Errorf("%s: %w", args[1], err)
		}
		for _, row := range definitionHashes(def) {
			fmt.Fprintf(stdout, "%s %s\n", row[0], row[1])
		}
		return true, nil
	}
	if len(args) != 3 {
		return true, fmt.Errorf("%s", compatUsage)
	}
	old, err := loadDefinition(args[1])
	if err != nil {
		return true, fmt.Errorf("%s: %w", args[1], err)
	}
	candidate, err := loadDefinition(args[2])
	if err != nil {
		return true, fmt.Errorf("%s: %w", args[2], err)
	}
	if problems := compatibility(old, candidate); len(problems) > 0 {
		return true, fmt.Errorf("%s is not compatible with %s:\n  %s", args[2], args[1], strings.Join(problems, "\n  "))
	}
	for _, row := range definitionHashes(candidate) {
		fmt.Fprintf(stdout, "%s %s\n", row[0], row[1])
	}
	return true, nil
}

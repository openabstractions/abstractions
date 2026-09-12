package main

import (
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
)

func mono(s string) string { return "<code>" + html.EscapeString(s) + "</code>" }

func rule(tag string) string { return `<span class="tag">[` + tag + `]</span>` }

func heading(b *strings.Builder, level int, id, title, tag string) {
	if tag != "" {
		title += " " + rule(tag)
	}
	fmt.Fprintf(b, "<h%d id=%q>%s</h%d>\n", level, id, title, level)
}

func table(b *strings.Builder, head []string, rows [][]string) {
	b.WriteString(`<div class="wrap"><table>` + "\n<thead><tr>")
	for _, h := range head {
		fmt.Fprintf(b, "<th>%s</th>", html.EscapeString(h))
	}
	b.WriteString("</tr></thead>\n<tbody>\n")
	for _, r := range rows {
		b.WriteString("<tr>")
		for _, c := range r {
			fmt.Fprintf(b, "<td>%s</td>", c)
		}
		b.WriteString("</tr>\n")
	}
	b.WriteString("</tbody></table></div>\n")
}

func genDocs(s *Definition) string {
	b := &strings.Builder{}
	docsOpen(b)
	docsEncoding(b, s)
	docsTypes(b, s)
	docsStructs(b, s)
	docsEnums(b, s)
	docsConsts(b, s)
	docsVocabulary(b, s)
	docsRefusals(b, s)
	if !s.NoIPC {
		docsProtocol(b, s)
	}
	docsServices(b, s)
	docsClose(b)
	return b.String()
}

func docsOpen(b *strings.Builder) {
	b.WriteString(`<!doctype html>
<html lang="en">
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Open Abstractions — schema</title>
<link rel="stylesheet" href="style.css">

<main>
<nav class="site"><a class="name" href="index.html">Open Abstractions</a>
<a href="index.html">Overview</a> <a href="cases.html">Cases</a> <a href="reference.html">Reference</a> <a href="evidence.html">Evidence</a> <a href="coverage.html">Coverage</a> <a href="adopt.html">Adopt</a>
<a class="right" href="https://github.com/openabstractions">github.com/openabstractions</a></nav>
<p class="meta" style="margin-top:10px"><a href="#encoding">Encoding</a> · <a href="#types">Types</a> · <a href="#structs">Structs</a> · <a href="#enums">Enumerations</a> · <a href="#constants">Constants</a> · <a href="#vocabulary">Vocabulary</a> · <a href="#refusals">Refusals</a> · <a href="#protocol">Protocol</a></p>

<h1>Schema</h1>
<p class="lead">One definition; this page and every encoder are emitted from it. Nothing below is written by hand, and each rule tag links the construct to the profile that admits it.</p>
`)
}

func docsClose(b *strings.Builder) {
	b.WriteString(`
<footer>
<p><a href="index.html">Overview</a> · <a href="cases.html">Cases</a> · <a href="reference.html">Reference</a> · <a href="evidence.html">Evidence</a> · <a href="coverage.html">Coverage</a> · <a href="adopt.html">Adopt</a> ·
<a href="https://github.com/openabstractions">github.com/openabstractions</a></p>
</footer>
</main>
</html>
`)
}

func docsEncoding(b *strings.Builder, s *Definition) {
	heading(b, 2, "encoding", "Encoding", "DEF-E1")
	e := s.Encoding
	settings := [][3]string{
		{"escape", e.Escape, "DEF-E2"},
		{"indent", strconv.Itoa(e.Indent), "DEF-E3"},
		{"map_keys", e.MapKeys, "DEF-E4"},
		{"numbers", e.Numbers, "DEF-E5"},
		{"opaque", e.Opaque, "DEF-E6"},
		{"terminator", e.Terminator, "DEF-E7"},
		{"duplicate_keys", e.DuplicateKeys, "DEF-E8"},
		{"depth_limit", strconv.Itoa(e.DepthLimit), "DEF-E9"},
	}
	rows := make([][]string, 0, len(settings))
	for _, x := range settings {
		rows = append(rows, []string{mono(x[0]), mono(x[1]), rule(x[2])})
	}
	table(b, []string{"setting", "value", "rule"}, rows)
}

func isContainer(t string) bool {
	return strings.HasPrefix(t, "list<") || strings.HasPrefix(t, "map<")
}

func fieldTypes(s *Definition) []string {
	seen := map[string]bool{}
	var names []string
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if !seen[f.Type] {
				seen[f.Type] = true
				names = append(names, f.Type)
			}
		}
	}
	sort.Strings(names)
	return names
}

func docsTypes(b *strings.Builder, s *Definition) {
	heading(b, 2, "types", "Types", "")
	var builtIn, declared, containers, repeated, strMaps, opaque []string
	for _, n := range fieldTypes(s) {
		switch {
		case n == "json":
			opaque = append(opaque, mono(n))
		case s.Repeated(n) != "":
			repeated = append(repeated, mono(n))
		case n == "map<string,string>":
			strMaps = append(strMaps, mono(n))
		case isContainer(n):
			containers = append(containers, mono(n))
		case s.IsStruct(n):
			declared = append(declared, mono(n))
		default:
			builtIn = append(builtIn, mono(n))
		}
	}
	kinds := []struct {
		label   string
		members []string
	}{
		{"built in", builtIn},
		{"declared below", declared},
		{"container " + rule("DEF-T3"), containers},
		{"repeated record " + rule("DEF-A9"), repeated},
		{"string map " + rule("DEF-A10"), strMaps},
		{"opaque " + rule("DEF-A3"), opaque},
	}
	rows := make([][]string, 0, len(kinds))
	for _, kind := range kinds {
		if len(kind.members) > 0 {
			rows = append(rows, []string{kind.label, strings.Join(kind.members, " ")})
		}
	}
	table(b, []string{"kind", "types"}, rows)
	if len(s.Typedefs) == 0 {
		return
	}
	heading(b, 3, "named-types", "Named types", "DEF-T2")
	fmt.Fprintf(b, "<p class=\"meta\">One spelling is written %s and a wider set is read %s; the width between them is the interoperability guarantee.</p>\n",
		rule("DEF-G2"), rule("DEF-G1"))
	rows = make([][]string, 0, len(s.Typedefs))
	for _, td := range s.Typedefs {
		w, r := "—", "—"
		if td.Grammar.Write != "" {
			w = mono(td.Grammar.Write)
		}
		if td.Grammar.Read != "" {
			r = mono(td.Grammar.Read)
		}
		rows = append(rows, []string{mono(td.Alias), mono(td.Base), w, r})
	}
	table(b, []string{"name", "is", "written", "read"}, rows)
}

func presence(f Field) string {
	switch f.Omit {
	case "never":
		if value, ok := f.Ann["equals"]; ok {
			return "required; equals " + mono(value) + "; refusal " + mono(f.Ann["equals_refusal"])
		}
		return "required"
	default:
		return "optional · omitted when " + html.EscapeString(f.Omit)
	}
}

func otherNames(f Field) string {
	var keys []string
	for k := range f.Ann {
		if strings.HasSuffix(k, ".name") {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return "—"
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, mono(strings.TrimSuffix(k, ".name"))+" "+mono(f.Ann[k]))
	}
	return strings.Join(out, ", ")
}

func structNotes(s *Definition, st Struct) []string {
	var notes []string
	if st.Document {
		notes = append(notes, "the document "+rule("DEF-A1"))
	}
	if s.Proto != nil && s.Proto.Request == st.Name {
		notes = append(notes, `the <a href="#protocol">protocol request</a>`)
	}
	if s.Proto != nil && s.Proto.Response == st.Name {
		notes = append(notes, `the <a href="#protocol">protocol response</a>`)
	}
	notes = append(notes, "unknown fields "+mono(st.UnknownFields))
	return notes
}

func docsStructs(b *strings.Builder, s *Definition) {
	heading(b, 2, "structs", "Structs", "DEF-T1")
	fmt.Fprintf(b, "<p class=\"meta\">A field is required, or says when it is omitted %s. Each struct says on its own what it does with a field name it has never heard of %s. A wire name never moves; where a language must spell it differently the field says so %s.</p>\n",
		rule("DEF-A4"), rule("DEF-A7"), rule("DEF-A2"))
	for _, st := range s.Structs {
		fmt.Fprintf(b, "<h3 id=%q>%s</h3>\n", "struct-"+st.Name, mono(st.Name))
		fmt.Fprintf(b, "<p class=\"meta\">%s</p>\n", strings.Join(structNotes(s, st), " · "))
		rows := make([][]string, 0, len(st.Fields))
		for _, f := range st.Fields {
			t := f.Type
			if f.Alias != "" {
				t = f.Alias
			}
			rows = append(rows, []string{
				strconv.Itoa(f.ID), mono(f.Name), mono(t), presence(f), otherNames(f),
			})
		}
		table(b, []string{"id", "field", "type", "presence", "named elsewhere"}, rows)
	}
}

func docsEnums(b *strings.Builder, s *Definition) {
	if len(s.Enums) == 0 {
		return
	}
	heading(b, 2, "enums", "Enumerations", "DEF-T5")
	if hasEnumFields(s) {
		b.WriteString("<p>Enum fields carry JSON strings containing exact member names, never numeric IDs. Unknown names are preserved with grant and refused as bad_enum with refuse on read and write. Optional absent fields preserve presence separately from an empty string.</p>\n")
	}
	fmt.Fprintf(b, "<p class=\"meta\">An enumeration is a closed vocabulary of wire names, and each one says on its own what a reader does with a member it has never heard of %s.</p>\n", rule("DEF-A5"))
	for _, en := range s.Enums {
		fmt.Fprintf(b, "<h3 id=%q>%s</h3>\n", "enum-"+en.Name, mono(en.Name))
		fmt.Fprintf(b, "<p class=\"meta\">a member this reader does not know is %s</p>\n",
			mono(en.Ann["unknown"]))
		keys := en.MemberAnn()
		head := append([]string{"id", "member"}, keys...)
		rows := make([][]string, 0, len(en.Members))
		for _, m := range en.Members {
			r := []string{strconv.Itoa(m.ID), mono(m.Name)}
			for _, k := range keys {
				if v, ok := m.Ann[k]; ok {
					r = append(r, mono(v))
				} else {
					r = append(r, "—")
				}
			}
			rows = append(rows, r)
		}
		table(b, head, rows)
	}
}

func docsConsts(b *strings.Builder, s *Definition) {
	if len(s.Consts) == 0 {
		return
	}
	heading(b, 2, "constants", "Constants", "DEF-A6")
	rows := make([][]string, 0, len(s.Consts))
	for _, c := range s.Consts {
		var vals []string
		for _, n := range c.Ints {
			vals = append(vals, strconv.FormatInt(n, 10))
		}
		vals = append(vals, c.Strings...)
		rows = append(rows, []string{mono(c.Name), mono(c.Type), mono(strings.Join(vals, ", "))})
	}
	table(b, []string{"name", "type", "value"}, rows)
}

func docsVocabulary(b *strings.Builder, s *Definition) {
	if s.Vocab == nil {
		return
	}
	v := s.Vocab
	heading(b, 2, "vocabulary", "Vocabulary "+mono(v.Name), "DEF-A8")
	fmt.Fprintf(b, "<p class=\"meta\">declared of %s · carried in %s · the subset a reader must know in %s</p>\n",
		mono(v.Of), mono(v.Names), mono(v.Critical))
	rows := make([][]string, 0, len(v.Terms))
	for _, t := range v.Terms {
		strip := "—"
		if t.StripCritical {
			strip = mono("true")
		}
		rows = append(rows, []string{strconv.Itoa(t.ID), mono(t.Name), docsPredicate(t), strip})
	}
	table(b, []string{"id", "term", "present when", "critical marking stripped"}, rows)
}

func docsPredicate(t Term) string {
	switch {
	case t.Always():
		return "always"
	case t.Member != "":
		return mono(t.When) + " is a member of the opaque value, and not null"
	case t.Membership():
		words := make([]string, len(t.Is))
		for i, w := range t.Is {
			words[i] = mono(w)
		}
		return mono(t.When) + " is one of " + strings.Join(words, ", ")
	}
	return mono(t.When) + " is present"
}

func docsRefusals(b *strings.Builder, s *Definition) {
	if len(s.Refusals) == 0 {
		return
	}
	heading(b, 2, "refusals", "Refusal words", "DEF-R1")
	rows := make([][]string, 0, len(s.Refusals))
	for _, r := range s.Refusals {
		rows = append(rows, []string{strconv.Itoa(r.ID), mono(r.Word), mono(r.Stage)})
	}
	table(b, []string{"order", "word", "stage"}, rows)
}

func docsProtocol(b *strings.Builder, s *Definition) {
	if s.Proto == nil {
		return
	}
	p := s.Proto
	heading(b, 2, "protocol", "Protocol "+mono(p.Name), "DEF-P1")
	fmt.Fprintf(b, "<p class=\"meta\">request %s · response %s · the operation in %s · the verdict in %s from %s · an operation this peer does not know is %s</p>\n",
		mono(p.Request), mono(p.Response), mono(p.OpField), mono(p.Verdict), mono(p.Verdicts), mono(p.Unknown))
	rows := make([][]string, 0, len(p.Operations))
	for _, op := range p.Operations {
		rows = append(rows, []string{strconv.Itoa(op.ID), mono(op.Name)})
	}
	table(b, []string{"id", "operation"}, rows)
}

func docsServices(b *strings.Builder, s *Definition) {
	for _, svc := range s.Services {
		heading(b, 2, "service-"+svc.Name, "Service "+mono(svc.Name), "DEF-S1")
		fmt.Fprintf(b, "<p>%s</p>\n", html.EscapeString(svc.Doc))
		if s.NoIPC {
			b.WriteString("<p>Interface-only output: implement these methods directly. No IPC client, dispatcher, message envelope or transport binding is emitted. Record codecs remain available. Method return/error signatures are retained; oneway marks the schema declaration, not a delivery or persistence guarantee for a direct call.</p>\n")
		} else {
			fmt.Fprintf(b, "<p>Wire identity: %s</p>\n", mono(svc.WireName))
			b.WriteString("<p>Frames carry version, service, method and typed arguments. Transport owns framing, associated exchanges, deadlines and connections. Unknown version/service/method and malformed arguments are rejected before invoking a handler. One-way WriteFrame success means local submission, not remote acceptance or persistence.</p>\n")
			if svcReplies(svc) {
				b.WriteString("<p>Request-response methods use ExchangeFrame. Replies carry version, service, method, ok and payload; success payload contains typed value (or an empty object for void). Error payload contains nonempty code and message, which may be empty. ServiceError preserves unknown codes. Unexpected handler failures become handler_error without private details; invalid results become invalid_result. Clients validate reply identity and result before exposing it. A malformed frame or transport failure remains a local error; the transport must associate each response with its exchange. C++ dispatchers are pure protocol bindings, not listening servers.</p>\n")
			}
		}
		for _, m := range svc.Methods {
			fmt.Fprintf(b, "<p>%s</p>\n", html.EscapeString(m.Doc))
			mode := "request-response"
			result := m.Result.Type
			if m.Oneway {
				mode = "oneway"
				result = "void"
			}
			fmt.Fprintf(b, "<h3>%s</h3><p>%s %s</p><ul>\n", mono(m.Name), mono(mode), mono(result))
			for _, f := range m.Args {
				fmt.Fprintf(b, "<li>Argument %d: %s %s (%s, including zero/false/empty)</li>\n", f.ID, mono(f.Type), mono(f.Name), presence(f))
			}
			b.WriteString("</ul>\n")
		}
	}
}

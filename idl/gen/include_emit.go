package main

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

func includeImports(b backend, s *Definition) []string {
	out := append([]string(nil), b.imports...)
	for _, imp := range s.Imports {
		switch b.lang {
		case "go":
			out = append(out, dependencyAlias(imp.Alias))
		case "cpp":
			out = append(out, strings.ReplaceAll(namespaceFor(imp.Def.Namespaces, "cpp"), ".", "/")+"/rec.h")
		case "python":
			out = append(out, namespaceFor(imp.Def.Namespaces, "python")+".rec")
		}
	}
	return out
}
func importPrelude(s *Definition, lang string) string {
	var b strings.Builder
	for _, n := range foreignNames(s) {
		imp := s.Foreign[n]
		switch lang {
		case "go":
			fmt.Fprintf(&b, "type %s = %s.%s\n", n, dependencyAlias(imp.Alias), imp.Name)
		case "cpp":
			fmt.Fprintf(&b, "using %s = ::%s::%s;\nstruct Reader;\ninline void enc_%s(std::string&, const %s&, int);\ninline %s decode_%s(Reader&);\n", n, strings.ReplaceAll(namespaceFor(imp.Def.Namespaces, lang), ".", "::"), imp.Name, lower(n), n, n, lower(n))
		case "python":
			fmt.Fprintf(&b, "%s = %s.%s\n", n, dependencyAlias(imp.Alias), imp.Name)
		}
	}
	return b.String()
}
func emitIncluded(b backend, s *Definition) string {
	if len(s.Imports) == 0 && !s.NamedCodecs {
		return b.emit(s)
	}
	if b.lang == "docs" {
		body := b.emit(s)
		var links strings.Builder
		links.WriteString("<section><h2>Included definitions</h2><ul>")
		for _, imp := range s.Imports {
			fmt.Fprintf(&links, "<li>%s: <code>%s</code>; qualified record types retain the included definition's codec policy.</li>", imp.Alias, html.EscapeString(imp.Path))
		}
		links.WriteString("</ul></section>")
		return strings.Replace(body, "</body>", links.String()+"</body>", 1)
	}
	s = importCarriers(s)
	body := b.emit(s)
	var imports strings.Builder
	used := map[string]bool{}
	for _, n := range foreignNames(s) {
		used[s.Foreign[n].Alias] = true
	}
	for _, imp := range s.Imports {
		if !used[imp.Alias] {
			continue
		}
		switch b.lang {
		case "go":
			fmt.Fprintf(&imports, "import %s %q\n", dependencyAlias(imp.Alias), s.GoImports[imp.Alias])
		case "cpp":
			fmt.Fprintf(&imports, "#include <%s/rec.h>\n", strings.ReplaceAll(namespaceFor(imp.Def.Namespaces, b.lang), ".", "/"))
		case "python":
			fmt.Fprintf(&imports, "import %s.rec as %s\n", namespaceFor(imp.Def.Namespaces, b.lang), dependencyAlias(imp.Alias))
		}
	}
	switch b.lang {
	case "go":
		body = strings.Replace(body, "package rec\n", "package rec\n"+imports.String(), 1)
		body = strings.Replace(body, "depth int\n}", "depth int\n limit int\n}", 1)
		body = strings.ReplaceAll(body, "r.depth > depthLimit", "r.depth > r.depthLimit()")
		body += "\nfunc(r *reader)depthLimit()int{if r.limit>0 && r.limit<depthLimit{return r.limit};return depthLimit}\n"
	case "cpp":
		body = imports.String() + body
		body = strings.Replace(body, "int depth = 0;", "int depth = 0;\n int limit = kDepthLimit;", 1)
		body = strings.ReplaceAll(body, "++depth > kDepthLimit", "++depth > limit")
	case "python":
		body = imports.String() + body
		body = strings.Replace(body, `("buf", "pos", "depth")`, `("buf", "pos", "depth", "limit")`, 1)
		body = strings.Replace(body, "self.depth = 0", "self.depth = 0\n        self.limit = _DEPTH_LIMIT", 1)
		body = strings.ReplaceAll(body, "self.depth > _DEPTH_LIMIT", "self.depth > self.limit")
	}
	if b.lang == "python" && len(s.Foreign) > 0 {
		body = strings.Replace(body, "    else:\n        cls, fields = _SERVICE_RECORDS[kind]", "    elif kind in _NAMED_IMPORTED:\n        cls, check = _NAMED_IMPORTED[kind]\n        valid = isinstance(value,cls)\n        if valid:\n            check(value,depth,_DEPTH_LIMIT)\n    else:\n        cls, fields = _SERVICE_RECORDS[kind]", 1)
	}
	var tail strings.Builder
	for _, n := range foreignNames(s) {
		imp := s.Foreign[n]
		switch b.lang {
		case "go":
			fmt.Fprintf(&tail, "\nfunc enc%s(out []byte,v *%s,depth int)(result []byte){defer func(){if p:=recover();p!=nil{if e,ok:=p.(*%s.Refusal);ok{panic(&Refusal{Word:e.Word,Offset:e.Offset})};panic(p)}}();return append(out,%s.Encode%sAt(v,depth)...)}\nfunc(r *reader)decode%s()(*%s,error){v,n,e:=%s.Decode%sAt(r.buf[r.pos:],r.depth,r.depthLimit());if x,ok:=e.(*%s.Refusal);ok{e=&Refusal{Word:x.Word,Offset:r.pos+x.Offset}};r.pos+=n;return v,e}\n", n, n, dependencyAlias(imp.Alias), dependencyAlias(imp.Alias), imp.Name, n, n, dependencyAlias(imp.Alias), imp.Name, dependencyAlias(imp.Alias))
		case "cpp":
			ns := "::" + strings.ReplaceAll(namespaceFor(imp.Def.Namespaces, b.lang), ".", "::") + "::"
			fmt.Fprintf(&tail, "\ninline void enc_%s(std::string& out,const %s& v,int depth){try{out += %sencode_%s_at(v,depth);}catch(const %sRefusal& e){throw Refusal(e.word,e.offset);}}\ninline %s decode_%s(Reader& r){try{std::size_t n=0;auto v=%sdecode_%s_at(r.buf.substr(r.pos),r.depth,r.limit,n);r.pos+=n;return v;}catch(const %sRefusal& e){throw Refusal(e.word,r.pos+e.offset);}}\n", lower(n), n, ns, lower(imp.Name), ns, n, lower(n), ns, lower(imp.Name), ns)
		case "python":
			fmt.Fprintf(&tail, "\ndef enc_%s(out,v,depth):\n    try:\n        out += %s.encode_%s_at(v,depth)\n    except %s.Refusal as e:\n        raise Refusal(e.word,e.offset) from e\ndef _decode_%s(r):\n    try:\n        v,n = %s.decode_%s_at(r.buf[r.pos:],r.depth,r.limit)\n    except %s.Refusal as e:\n        raise Refusal(e.word,r.pos+e.offset) from e\n    r.pos += n\n    return v\n", lower(n), dependencyAlias(imp.Alias), lower(imp.Name), dependencyAlias(imp.Alias), lower(n), dependencyAlias(imp.Alias), lower(imp.Name), dependencyAlias(imp.Alias))
		}
	}
	if b.lang == "python" {
		tail.WriteString(pyNamedChecks(s))
	}
	for _, st := range s.Structs {
		if s.Envelope(st.Name) {
			continue
		}
		emitNamed(&tail, s, st, b.lang)
	}
	if b.lang == "cpp" {
		if len(s.Imports) > 0 && s.Document != "" {
			fmt.Fprintf(&tail, "\ninline std::string encode(const %s& v){return encode_%s(v);}\n", s.Document, lower(s.Document))
		}
		return strings.Replace(body, "\n}  // namespace rec\n", tail.String()+"\n}  // namespace rec\n", 1)
	}
	return body + tail.String()
}
func emitNamed(b *strings.Builder, s *Definition, st Struct, lang string) {
	n := st.Name
	l := lower(n)
	term := ""
	if s.Encoding.TrailingNewline() {
		term = "\n"
	}
	q := strconv.Quote(term)
	derive := ""
	if s.Vocab != nil && st.Name == s.Document && len(s.Services) == 0 {
		switch lang {
		case "go":
			derive = "if e==nil{e=r.derive(v)};"
		case "cpp":
			derive = "derive(r,v);"
		case "python":
			derive = "    _derive(r,v)\n"
		}
	}
	switch lang {
	case "go":
		fmt.Fprintf(b, `
func Decode%[1]sAt(in []byte,depth,limit int)(*%[1]s,int,error){r:=&reader{buf:in,depth:depth,limit:limit};if depth<0||limit<1{return nil,0,r.refuse("depth_exceeded")};r.ws();v,e:=r.decode%[1]s();%[3]sreturn v,r.pos,e}
func Encode%[1]sAt(v *%[1]s,depth int)[]byte{if depth<0||depth>=depthLimit{panic(&Refusal{Word:"depth_exceeded"})};out:=enc%[1]s(nil,v,depth);if _,_,e:=Decode%[1]sAt(out,depth,depthLimit);e!=nil{panic(e)};return out}
func Encode%[1]s(v *%[1]s)[]byte{return append(Encode%[1]sAt(v,0),%[2]s...)}
func Decode%[1]s(in []byte)(*%[1]s,error){v,n,e:=Decode%[1]sAt(in,0,depthLimit);if e!=nil{return nil,e};r:=&reader{buf:in,pos:n};r.ws();if r.pos!=len(in){return nil,r.refuse("trailing_bytes")};return v,nil}
`, n, q, derive)
	case "cpp":
		fmt.Fprintf(b, `
inline %[1]s decode_%[2]s_at(std::string_view in,int depth,int limit,std::size_t& consumed){Reader r{in};if(depth<0||limit<1)r.refuse("depth_exceeded");r.depth=depth;r.limit=limit<kDepthLimit?limit:kDepthLimit;r.skip_ws();auto v=decode_%[2]s(r);%[4]sconsumed=r.pos;return v;}
inline std::string encode_%[2]s_at(const %[1]s& v,int depth){Reader r{""};if(depth<0||depth>=kDepthLimit)r.refuse("depth_exceeded");std::string out;enc_%[2]s(out,v,depth);std::size_t n=0;decode_%[2]s_at(out,depth,kDepthLimit,n);return out;}
inline std::string encode_%[2]s(const %[1]s& v){return encode_%[2]s_at(v,0)+%[3]s;}
inline %[1]s decode_%[2]s(std::string_view in){std::size_t n=0;auto v=decode_%[2]s_at(in,0,kDepthLimit,n);Reader r{in};r.pos=n;r.skip_ws();if(r.pos!=in.size())r.refuse("trailing_bytes");return v;}
`, n, l, q, derive)
	case "python":
		fmt.Fprintf(b, `
def decode_%[1]s_at(data,depth,limit):
    r = _Reader(bytes(data))
    if depth < 0 or limit < 1:
        raise r.refuse("depth_exceeded")
    r.depth = depth
    r.limit = min(limit,_DEPTH_LIMIT)
    r.ws()
    v = _decode_%[1]s(r)
%[3]s    return v,r.pos
def encode_%[1]s_at(v,depth):
    if depth < 0 or depth >= _DEPTH_LIMIT:
        raise _Reader(b'').refuse("depth_exceeded")
    check_%[1]s(v,depth,_DEPTH_LIMIT)
    out = bytearray()
    enc_%[1]s(out,v,depth)
    decode_%[1]s_at(out,depth,_DEPTH_LIMIT)
    return bytes(out)
def encode_%[1]s(v):
    return encode_%[1]s_at(v,0)+%[2]s.encode()
def decode_%[1]s(data):
    v,n = decode_%[1]s_at(data,0,_DEPTH_LIMIT)
    r = _Reader(bytes(data))
    r.pos = n
    r.ws()
    if r.pos != len(r.buf):
        raise r.refuse("trailing_bytes")
    return v
`, l, q, derive)
	}
}

// Reuse the service's strict Python value checker for standalone named records.
// Imported records delegate their fields to the owning module's generated check.
func pyNamedChecks(s *Definition) string {
	s = enumCarriers(s)
	var b strings.Builder
	b.WriteString("\n_NAMED_IMPORTED = {}\n")
	for _, n := range foreignNames(s) {
		imp := s.Foreign[n]
		fmt.Fprintf(&b, "def check_%s(v,depth,limit):\n    try:\n        %s.check_%s(v,depth,limit)\n    except %s.Refusal as e:\n        raise Refusal(e.word,e.offset) from e\n_NAMED_IMPORTED[%q] = (%s,check_%s)\n", lower(n), dependencyAlias(imp.Alias), lower(imp.Name), dependencyAlias(imp.Alias), n, n, lower(n))
	}
	b.WriteString("_NAMED_RECORDS = {\n")
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "%q:(%s,[", st.Name, st.Name)
		for _, f := range st.Fields {
			fmt.Fprintf(&b, "(%q,%q,%q),", f.Ident("python"), f.Type, f.Omit)
		}
		b.WriteString("]),\n")
	}
	b.WriteString("}\n")
	checker := pyServiceCommon[strings.Index(pyServiceCommon, "def _service_check"):]
	checker = strings.NewReplacer("_service_check", "_named_check", "_SERVICE_RECORDS", "_NAMED_RECORDS", "_DEPTH_LIMIT", "limit", "depth=0):", "depth=0,limit=_DEPTH_LIMIT):", "depth + 1)", "depth + 1, limit)").Replace(checker)
	checker = strings.Replace(checker, "    if kind == \"string\":", "    if kind == \"binary\":\n        valid = type(value) is bytes\n    elif kind == \"string\":", 1)
	checker = strings.Replace(checker, "    else:\n        cls, fields = _NAMED_RECORDS[kind]", "    elif kind in _NAMED_IMPORTED:\n        cls, check = _NAMED_IMPORTED[kind]\n        valid = isinstance(value,cls)\n        if valid:\n            check(value,depth,limit)\n    else:\n        cls, fields = _NAMED_RECORDS[kind]", 1)
	b.WriteString(checker)
	for _, st := range s.Structs {
		fmt.Fprintf(&b, "\ndef check_%s(v,depth=0,limit=_DEPTH_LIMIT):\n    _named_check(%q,v,depth,min(limit,_DEPTH_LIMIT))\n", lower(st.Name), st.Name)
	}
	return b.String()
}

func dependencyAlias(alias string) string { return "oa_dependency_" + alias }

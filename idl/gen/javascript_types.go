package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// genJSTypes is index.d.mts: TypeScript declarations for exactly the names
// index.mjs exports. Records are interfaces, closed vocabularies are unions of
// their wire words, i64 is bigint, and clients return promises of the result
// record. The declarations are derived from the definition, then compared with
// the names internal.mjs exports: a public name without a declaration, or a
// declared value index.mjs does not export, is a generation error.
func genJSTypes(def *Definition, private string) (string, error) {
	public := map[string]bool{}
	for _, n := range jsPublicNames(private) {
		public[n] = true
	}
	s := def
	if !s.NoIPC {
		s = serviceTypes(def)
	}
	d := jsTypes{s: s, values: map[string]string{}, types: map[string]string{}}
	if err := d.collect(); err != nil {
		return "", err
	}
	// The declarations spell these built-ins; a contract type of the same name
	// would shadow them inside the module.
	for _, n := range []string{"Array", "Error", "Promise", "Readonly", "Uint8Array"} {
		if _, ok := d.types[n]; ok {
			return "", fmt.Errorf("javascript declarations: contract type %s shadows the TypeScript built-in the declarations use; rename it", n)
		}
		if _, ok := d.values[n]; ok {
			return "", fmt.Errorf("javascript declarations: contract name %s shadows the TypeScript built-in the declarations use; rename it", n)
		}
	}
	var missing, extra []string
	for n := range public {
		if _, ok := d.values[n]; !ok {
			missing = append(missing, n)
		}
	}
	for n := range d.values {
		if !public[n] {
			extra = append(extra, n)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		return "", fmt.Errorf("javascript declarations: index.mjs exports %s with no declaration", strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		return "", fmt.Errorf("javascript declarations: %s declared and not exported by index.mjs", strings.Join(extra, ", "))
	}

	var b strings.Builder
	if ns := namespaceFor(s.Namespaces, "javascript"); ns != "" {
		fmt.Fprintf(&b, "// %s: declarations for index.mjs.\n", ns)
	} else {
		b.WriteString("// Declarations for index.mjs.\n")
	}
	for _, imp := range d.imports() {
		fmt.Fprintf(&b, "import type * as %s from %s;\n", jsDependency(imp), strconv.Quote(s.JSImports[imp]))
	}
	if len(s.Services) > 0 && !s.NoIPC {
		b.WriteString(jsTransportDecl)
	}
	names := make([]string, 0, len(d.types)+len(d.values))
	for n := range d.types {
		names = append(names, n)
	}
	for n := range d.values {
		if _, ok := d.types[n]; !ok {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if t, ok := d.types[n]; ok {
			b.WriteString("\n" + t)
		}
		if v, ok := d.values[n]; ok {
			if _, both := d.types[n]; !both {
				b.WriteString("\n")
			}
			b.WriteString(v)
		}
	}
	// Only what is written export is public; the transport shape stays local.
	b.WriteString("\nexport {};\n")
	return b.String(), nil
}

const jsTransportDecl = `
/** The frame transport a generated client calls: a facade binding or a connector's transport. */
interface FrameTransport {
  exchangeFrame(frame: Uint8Array): Promise<Uint8Array>;
  writeFrame(frame: Uint8Array): Promise<void>;
}
`

type jsTypes struct {
	s       *Definition
	values  map[string]string // runtime exports
	types   map[string]string // type-only declarations, keyed by the name they share
	aliases map[string]bool
}

func (d *jsTypes) collect() error {
	s := d.s
	d.aliases = map[string]bool{}
	for _, en := range s.Enums {
		name := pascalCase(en.Name)
		var members []string
		for _, m := range en.Members {
			key := jsPascal(m.Name)
			if n, ok := m.Ann["javascript.name"]; ok {
				key = n
			}
			members = append(members, fmt.Sprintf("%s: %q", key, m.WireName()))
		}
		d.values[name] = fmt.Sprintf("export declare const %s: Readonly<{ %s }>;\n", name, strings.Join(members, "; "))
		d.types[name] = fmt.Sprintf("export type %s = (typeof %s)[keyof typeof %s];\n", name, name, name)
		for _, key := range en.MemberAnn() {
			if strings.HasSuffix(key, ".name") {
				continue
			}
			var pairs []string
			for _, m := range en.Members {
				if v, ok := m.Ann[key]; ok {
					pairs = append(pairs, fmt.Sprintf("%q: %q", m.WireName(), v))
				}
			}
			n := pascalCase(en.Name) + jsPascal(key)
			d.values[n] = fmt.Sprintf("export declare const %s: Readonly<{ %s }>;\n", n, strings.Join(pairs, "; "))
		}
	}
	for _, c := range s.Consts {
		var items []string
		if c.Type == "list<i32>" {
			for _, n := range c.Ints {
				items = append(items, strconv.FormatInt(n, 10))
			}
		} else {
			for _, v := range c.Strings {
				items = append(items, strconv.Quote(v))
			}
		}
		d.values[camelCase(c.Name)] = fmt.Sprintf("export declare const %s: readonly [%s];\n", camelCase(c.Name), strings.Join(items, ", "))
	}
	for _, st := range s.Structs {
		stem := jsStem(st.Name)
		if jsInternalRecord(s, st.Name) {
			// Service envelopes and frames never reach an application.
			continue
		}
		var fields strings.Builder
		for _, f := range st.Fields {
			t, err := d.field(f)
			if err != nil {
				return fmt.Errorf("javascript declarations: %s.%s: %w", st.Name, f.Name, err)
			}
			fmt.Fprintf(&fields, "  %s: %s;\n", f.Ident("javascript"), t)
		}
		if st.PreservesUnknown() {
			fields.WriteString("  extras: Record<string, Uint8Array>;\n")
		}
		d.types[stem] = fmt.Sprintf("%sexport interface %s {\n%s}\n", structDoc(st, "// "), stem, fields.String())
		d.values["new"+stem] = fmt.Sprintf("export declare function new%s(): %s;\n", stem, stem)
	}
	d.values["Refusal"] = "export declare class Refusal extends Error {\n  constructor(word: string, offset: number);\n  word: " + strings.ReplaceAll(quotedList(s.Words()), ", ", " | ") + ";\n  offset: number;\n}\n"
	if s.Document != "" {
		doc := jsStem(s.Document)
		d.values["encode"] = fmt.Sprintf("export declare function encode(v: %s): Uint8Array;\n", doc)
		d.values["decode"] = fmt.Sprintf("export declare function decode(data: Uint8Array): %s;\n", doc)
	}
	if s.NamedCodecs || len(s.Imports) > 0 {
		for _, st := range s.Structs {
			if s.Envelope(st.Name) || jsInternalRecord(s, st.Name) {
				continue
			}
			stem := jsStem(st.Name)
			d.values["encode"+stem] = fmt.Sprintf("export declare function encode%s(v: %s): Uint8Array;\n", stem, stem)
			d.values["decode"+stem] = fmt.Sprintf("export declare function decode%s(data: Uint8Array): %s;\n", stem, stem)
		}
	}
	if v := s.Vocab; v != nil {
		var terms, strip []string
		for _, t := range v.Terms {
			terms = append(terms, t.Name)
			if t.StripCritical {
				strip = append(strip, t.Name)
			}
		}
		d.values[camelCase(v.Name)+"Terms"] = fmt.Sprintf("export declare const %sTerms: readonly [%s];\n", camelCase(v.Name), quotedList(terms))
		d.values[camelCase(v.Name)+"StripCritical"] = fmt.Sprintf("export declare const %sStripCritical: readonly [%s];\n", camelCase(v.Name), quotedList(strip))
	}
	if p := s.Proto; p != nil && !s.NoIPC {
		d.values["operations"] = "export declare const operations: readonly [" + quotedList(opNames(s)) + "];\n"
		d.values["isOperation"] = "export declare function isOperation(name: string): boolean;\n"
		d.values["unknownOperation"] = fmt.Sprintf("export declare const unknownOperation: %q;\n", p.Unknown)
		d.values["verdicts"] = "export declare const verdicts: readonly [" + quotedList(verdictNames(s)) + "];\n"
		d.values["isVerdict"] = "export declare function isVerdict(name: string): boolean;\n"
		for _, name := range envelopes(s) {
			stem := jsStem(name)
			d.values["encode"+stem] = fmt.Sprintf("export declare function encode%s(v: %s): Uint8Array;\n", stem, stem)
			d.values["decode"+stem] = fmt.Sprintf("export declare function decode%s(data: Uint8Array): %s;\n", stem, stem)
		}
	}
	if len(s.Services) == 0 {
		return nil
	}
	if s.NoIPC {
		for _, svc := range s.Services {
			methods, err := d.methods(svc)
			if err != nil {
				return err
			}
			d.values[pascalCase(svc.Name)] = fmt.Sprintf("export declare class %s {\n%s}\n", pascalCase(svc.Name), methods)
		}
		return nil
	}
	d.values["DispatchError"] = "export declare class DispatchError extends Error {\n  constructor(code: string);\n  code: string;\n}\n"
	d.values["ServiceError"] = "export declare class ServiceError extends Error {\n  constructor(code: string, message?: string);\n  code: string;\n}\n"
	for _, svc := range s.Services {
		methods, err := d.methods(svc)
		if err != nil {
			return err
		}
		client := pascalCase(svc.Name) + "Client"
		d.values[client] = fmt.Sprintf("%sexport declare class %s {\n  constructor(transport: FrameTransport);\n%s}\n", jsDoc(svc.Doc, ""), client, methods)
		d.values[pascalCase(svc.Name)+"Service"] = fmt.Sprintf("export declare const %sService: Readonly<{ wireName: %q; Client: typeof %s }>;\n", pascalCase(svc.Name), svc.WireName, client)
	}
	return nil
}

func (d *jsTypes) methods(svc Service) (string, error) {
	var b strings.Builder
	for _, m := range svc.Methods {
		var params []string
		for _, f := range m.Args {
			t, err := d.field(f)
			if err != nil {
				return "", fmt.Errorf("javascript declarations: %s.%s argument %s: %w", svc.Name, m.Name, f.Name, err)
			}
			params = append(params, f.Ident("javascript")+": "+t)
		}
		result := "void"
		if !m.Oneway && m.Result.Type != "void" {
			t, err := d.field(m.Result)
			if err != nil {
				return "", fmt.Errorf("javascript declarations: %s.%s result: %w", svc.Name, m.Name, err)
			}
			result = t
		}
		fmt.Fprintf(&b, "%s  %s(%s): Promise<%s>;\n", jsDoc(m.Doc, "  "), camelCase(m.Name), strings.Join(params, ", "), result)
	}
	return b.String(), nil
}

// field is the TypeScript type of a record field, argument or result.
func (d *jsTypes) field(f Field) (string, error) {
	t, err := d.typ(f.Type)
	if err != nil {
		return "", err
	}
	if en := d.s.Enum(f.Type); en != nil && en.Ann["unknown"] == "grant" {
		// An open vocabulary keeps an unrecognized word as a plain string.
		t += " | (string & {})"
	}
	absent := f.Omit == "absent" && (d.s.IsStruct(f.Type) || d.s.Enum(f.Type) != nil || f.Type == "binary")
	if _, imported := d.s.importedRecord(f.Type); imported && f.Omit == "absent" {
		absent = true
	}
	if absent {
		t += " | null"
	}
	return t, nil
}

func (d *jsTypes) typ(t string) (string, error) {
	switch t {
	case "string":
		return "string", nil
	case "i32":
		return "number", nil
	case "i64":
		return "bigint", nil
	case "bool":
		return "boolean", nil
	case "binary":
		return "Uint8Array", nil
	case "json":
		return "string | Uint8Array", nil
	case "map<string,string>":
		return "{ [key: string]: string }", nil
	case "map<string,json>":
		return "{ [key: string]: string | Uint8Array }", nil
	}
	if e := listElement(t); e != "" {
		inner, err := d.typ(e)
		if err != nil {
			return "", err
		}
		if en := d.s.Enum(e); en != nil && en.Ann["unknown"] == "grant" {
			inner += " | (string & {})"
		}
		if strings.Contains(inner, " ") {
			return "Array<" + inner + ">", nil
		}
		return inner + "[]", nil
	}
	if en := d.s.Enum(t); en != nil {
		return pascalCase(en.Name), nil
	}
	if imp, ok := d.s.importedRecord(t); ok {
		d.aliases[imp.Alias] = true
		return jsDependency(imp.Alias) + "." + jsStem(imp.Name), nil
	}
	if d.s.byName[t] != nil {
		return jsStem(t), nil
	}
	return "", fmt.Errorf("no TypeScript type for %s", t)
}

func (d *jsTypes) imports() []string {
	var out []string
	for a := range d.aliases {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

func quotedList(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return strings.Join(q, ", ")
}

// jsDoc renders a contract doc string as a JSDoc block.
func jsDoc(doc, indent string) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	return indent + "/** " + strings.ReplaceAll(strings.ReplaceAll(doc, "*/", "* /"), "\n", " ") + " */\n"
}

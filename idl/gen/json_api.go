package main

import (
	"fmt"
	"strings"
)

// Public to_json and from_json in every generated binding (CC-8): one record
// of the definition to its JSON and back, without reaching into a private
// codec module. The JSON is laid out as the definition's documents are and
// carries no document terminator; reading refuses what the record's reader
// refuses, trailing bytes included, and derives the vocabulary of the
// document record as decode does.

// jsonAPIRecords are the records an application may hand to to_json: the
// definition's own, less service carriers, protocol envelopes and records
// another definition owns.
func jsonAPIRecords(s *Definition) []Struct {
	if !declaresWord(s, "trailing_bytes") {
		return nil
	}
	var out []Struct
	for _, st := range s.Structs {
		if pyInternalRecord(s, st.Name) || s.Envelope(st.Name) {
			continue
		}
		out = append(out, st)
	}
	return out
}

func declaresWord(s *Definition, word string) bool {
	for _, w := range s.Words() {
		if w == word {
			return true
		}
	}
	return false
}

func derivesDocument(s *Definition, st Struct) bool { return s.Vocab != nil && st.Name == s.Document }

func goJSONAPI(b *strings.Builder, s *Definition) {
	recs := jsonAPIRecords(s)
	if len(recs) == 0 || !declaresWord(s, "wrong_type") {
		return
	}
	b.WriteString("\n// ToJSON writes one record of this package, given as a value or a pointer,\n// as JSON laid out as its documents are and without the document terminator.\n// Anything else, a nil pointer included, is refused with wrong_type.\nfunc ToJSON(v any) (out []byte, err error) {\n")
	b.WriteString("\tdefer func() {\n\t\tif p := recover(); p != nil {\n\t\t\tif e, ok := p.(*Refusal); ok {\n\t\t\t\tout, err = nil, e\n\t\t\t\treturn\n\t\t\t}\n\t\t\tpanic(p)\n\t\t}\n\t}()\n\tswitch v := v.(type) {\n")
	for _, st := range recs {
		fmt.Fprintf(b, "\tcase %s:\n\t\treturn enc%s(nil, &v, 0), nil\n\tcase *%s:\n\t\tif v == nil {\n\t\t\treturn nil, &Refusal{Word: \"wrong_type\"}\n\t\t}\n\t\treturn enc%s(nil, v, 0), nil\n", st.Name, st.Name, st.Name, st.Name)
	}
	b.WriteString("\t}\n\treturn nil, &Refusal{Word: \"wrong_type\"}\n}\n")
	b.WriteString("\n// FromJSON reads one record from data into v, a pointer to a record of this\n// package. A refused input returns *Refusal.\nfunc FromJSON(data []byte, v any) error {\n\tr := &reader{buf: data}\n\tr.ws()\n\tswitch p := v.(type) {\n")
	for _, st := range recs {
		fmt.Fprintf(b, "\tcase *%s:\n\t\tif p == nil {\n\t\t\treturn &Refusal{Word: \"wrong_type\"}\n\t\t}\n\t\tx, err := r.decode%s()\n\t\tif err != nil {\n\t\t\treturn err\n\t\t}\n\t\tr.ws()\n\t\tif r.pos < len(r.buf) {\n\t\t\treturn r.refuse(\"trailing_bytes\")\n\t\t}\n", st.Name, st.Name)
		if derivesDocument(s, st) {
			b.WriteString("\t\tif err := r.derive(x); err != nil {\n\t\t\treturn err\n\t\t}\n")
		}
		b.WriteString("\t\t*p = *x\n\t\treturn nil\n")
	}
	b.WriteString("\t}\n\treturn &Refusal{Word: \"wrong_type\"}\n}\n")
}

func pyJSONAPI(b *strings.Builder, s *Definition) {
	recs := jsonAPIRecords(s)
	if len(recs) == 0 {
		return
	}
	b.WriteString("\n\n_JSON_RECORDS = {\n")
	for _, st := range recs {
		fmt.Fprintf(b, "    %s: (%s, %s),\n", pyClass(s, st.Name), pyWriter(st.Name), pyReader(st.Name))
	}
	b.WriteString("}\n")
	b.WriteString(`

def to_json(value):
    """One record as JSON bytes, laid out as this module's documents are and
    without the document terminator."""
    codec = _JSON_RECORDS.get(type(value))
    if codec is None:
        raise TypeError("to_json takes a record of this module, not %s" % type(value).__name__)
    out = bytearray()
    codec[0](out, value, 0)
    return bytes(out)


def from_json(kind, data):
    """Read one record of class kind from data; a refused input raises Refusal."""
    codec = _JSON_RECORDS.get(kind)
    if codec is None:
        raise TypeError("from_json takes a record class of this module, not %r" % (kind,))
    r = _Reader(bytes(data))
    r.ws()
    v = codec[1](r)
    r.ws()
    if r.pos < len(r.buf):
        raise r.refuse("trailing_bytes")
`)
	for _, st := range recs {
		if derivesDocument(s, st) {
			fmt.Fprintf(b, "    if kind is %s:\n        _derive(r, v)\n", pyClass(s, st.Name))
		}
	}
	b.WriteString("    return v\n")
}

func jsJSONAPI(b *strings.Builder, s *Definition) {
	recs := jsonAPIRecords(s)
	if len(recs) == 0 {
		return
	}
	b.WriteString("\nconst JSON_RECORDS = Object.freeze({\n")
	for _, st := range recs {
		stem := jsStem(st.Name)
		fmt.Fprintf(b, "  %s: [write%s, read%s],\n", stem, stem, stem)
	}
	b.WriteString("});\n")
	b.WriteString(`
// toJson writes one record, named by kind, as JSON laid out as this module's
// documents are and without the document terminator.
export function toJson(kind, value) {
  const codec = Object.hasOwn(JSON_RECORDS, kind) ? JSON_RECORDS[kind] : undefined;
  if (codec === undefined) throw new TypeError("toJson takes a record name of this module, not " + String(kind));
  const out = new Out();
  codec[0](out, value, 0);
  return out.bytes();
}

// fromJson reads one record, named by kind, from data; a refused input throws Refusal.
export function fromJson(kind, data) {
  const codec = Object.hasOwn(JSON_RECORDS, kind) ? JSON_RECORDS[kind] : undefined;
  if (codec === undefined) throw new TypeError("fromJson takes a record name of this module, not " + String(kind));
  const r = new Reader(data);
  r.ws();
  const v = codec[1](r);
  r.ws();
  if (r.pos < r.buf.length) throw r.refuse("trailing_bytes");
`)
	for _, st := range recs {
		if derivesDocument(s, st) {
			fmt.Fprintf(b, "  if (kind === %q) derive(r, v);\n", jsStem(st.Name))
		}
	}
	b.WriteString("  return v;\n}\n")
}

// jsJSONAPITypes declares toJson and fromJson for index.d.mts.
func jsJSONAPITypes(s *Definition, types, values map[string]string) {
	recs := jsonAPIRecords(s)
	if len(recs) == 0 {
		return
	}
	var fields strings.Builder
	for _, st := range recs {
		stem := jsStem(st.Name)
		fmt.Fprintf(&fields, "  %s: %s;\n", stem, stem)
	}
	types["JsonRecords"] = "// JsonRecords maps each record name toJson and fromJson take to its type.\nexport interface JsonRecords {\n" + fields.String() + "}\n"
	values["toJson"] = "export declare const toJson: <K extends keyof JsonRecords>(kind: K, value: JsonRecords[K]) => Uint8Array;\n"
	values["fromJson"] = "export declare const fromJson: <K extends keyof JsonRecords>(kind: K, data: Uint8Array) => JsonRecords[K];\n"
}

func rsJSONAPI(b *strings.Builder, s *Definition) {
	recs := jsonAPIRecords(s)
	if len(recs) == 0 {
		return
	}
	b.WriteString("\n/// A record of this crate as JSON, laid out as its documents are and without\n/// the document terminator.\npub trait JsonRecord: ::std::marker::Sized {\n    fn to_json(&self) -> ::std::vec::Vec<u8>;\n    fn from_json(data: &[u8]) -> ::std::result::Result<Self, Refusal>;\n}\n")
	for _, st := range recs {
		fn := rsFn(st.Name)
		bind := "let v"
		if derivesDocument(s, st) {
			bind = "let mut v"
		}
		fmt.Fprintf(b, "\nimpl JsonRecord for %s {\n    fn to_json(&self) -> ::std::vec::Vec<u8> {\n        let mut out = ::std::vec::Vec::new();\n        enc_%s(&mut out, self, 0);\n        out\n    }\n", st.Name, fn)
		fmt.Fprintf(b, "    fn from_json(data: &[u8]) -> ::std::result::Result<Self, Refusal> {\n        let mut r = Reader { buf: data, pos: 0, depth: 0 };\n        r.skip_ws();\n        %s = decode_%s(&mut r)?;\n        r.skip_ws();\n        if r.pos < r.buf.len() {\n            return r.refuse(\"trailing_bytes\");\n        }\n", bind, fn)
		if derivesDocument(s, st) {
			b.WriteString("        derive(&r, &mut v)?;\n")
		}
		b.WriteString("        Ok(v)\n    }\n}\n")
	}
}

func cppJSONAPI(b *strings.Builder, s *Definition) {
	recs := jsonAPIRecords(s)
	if len(recs) == 0 {
		return
	}
	b.WriteString("\n// to_json writes one record as JSON, laid out as this definition's documents\n// are and without the document terminator; from_json<T> reads one and throws\n// Refusal for what the record's reader refuses.\ntemplate <class T> T from_json(std::string_view data);\n")
	for _, st := range recs {
		fn := cppFn(st.Name)
		fmt.Fprintf(b, "\ninline std::string to_json(const %s& v) {\n    std::string out;\n    detail::enc_%s(out, v, 0);\n    return out;\n}\n", st.Name, fn)
		fmt.Fprintf(b, "\ntemplate <> inline %s from_json<%s>(std::string_view data) {\n    detail::Reader r{data};\n    r.skip_ws();\n    %s v = detail::decode_%s(r);\n    r.skip_ws();\n    if (r.pos < r.buf.size()) r.refuse(\"trailing_bytes\");\n", st.Name, st.Name, st.Name, fn)
		if derivesDocument(s, st) {
			b.WriteString("    detail::derive(r, v);\n")
		}
		b.WriteString("    return v;\n}\n")
	}
}

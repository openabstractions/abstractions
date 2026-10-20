package main

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// The jsonschema backend writes one JSON Schema (draft 2020-12) document per
// record: what a validator outside OA can check about the record's JSON. A
// document is self-contained; the records it references sit in its $defs.
// What JSON Schema cannot say stays with the generated readers: duplicate
// member names at any depth, depth_limit, number spellings, timestamp grammar
// unpaired-surrogate source spelling after a repairing loader, and refusal words.

const jsonSchemaDialect = "https://json-schema.org/draft/2020-12/schema"

// jsonSchemaIndex is the backend's primary artefact: the list of the record
// documents beside it, so a reader finds every record from one file.
func genJSONSchema(s *Definition) string {
	names := make([]string, 0, len(s.Structs))
	for _, st := range s.Structs {
		names = append(names, st.Name+".schema.json")
	}
	sort.Strings(names)
	doc := map[string]any{"records": names}
	if s.Document != "" {
		doc["document"] = s.Document + ".schema.json"
	}
	return jsonText(doc)
}

// jsonSchemaRecords is one document per record, keyed by file name.
func jsonSchemaRecords(s *Definition) map[string]string {
	out := map[string]string{}
	for _, st := range s.Structs {
		defs := map[string]any{}
		doc := jsonSchemaStruct(s, st, defs)
		doc["$schema"] = jsonSchemaDialect
		doc["title"] = st.Name
		if len(defs) > 0 {
			doc["$defs"] = defs
		}
		out[st.Name+".schema.json"] = jsonText(doc)
	}
	return out
}

// jsonSchemaPath is where a record's document lands beside the index.
func jsonSchemaPath(index, file string) string { return path.Join(path.Dir(index), file) }

func jsonText(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic("jsonschema: " + err.Error())
	}
	return string(b) + "\n"
}

func jsonSchemaStruct(s *Definition, st Struct, defs map[string]any) map[string]any {
	props := map[string]any{}
	required := []string{}
	for _, f := range st.Fields {
		props[f.Name] = jsonSchemaField(s, f, defs)
		if f.Omit == "never" {
			required = append(required, f.Name)
		}
	}
	doc := map[string]any{"type": "object", "properties": props, "additionalProperties": !st.RefuseUnknown()}
	if spec, ok := st.Ann["schema_members"]; ok {
		// The parser validates the shared count-bound grammar. This declaration
		// describes JSON Schema; generated codecs keep their existing rules.
		lo, hi, _ := strings.Cut(spec, "..")
		if lo != "" {
			doc["minProperties"] = json.Number(lo)
		}
		if hi != "" {
			doc["maxProperties"] = json.Number(hi)
		}
	}
	if len(required) > 0 {
		doc["required"] = required
	}
	if s.Vocab != nil && s.Vocab.Of == st.Name {
		jsonSchemaVocabulary(s, doc, props)
	}
	return doc
}

// A vocabulary has a finite declared set of critical words. Each declared
// predicate is equivalent to that word's presence in names; unknown advisory
// names remain valid, and strip_critical words require no subset check.
func jsonSchemaVocabulary(s *Definition, doc, props map[string]any) {
	v := s.Vocab
	if len(v.Terms) == 0 {
		// Empty vocabularies are accepted definitions: advisory names stay open,
		// while no critical word can be understood. An empty-list bound avoids
		// emitting allOf, which requires at least one schema.
		props[v.Critical].(map[string]any)["maxItems"] = 0
		return
	}
	known := make([]string, 0, len(v.Terms))
	all := []any{}
	contains := func(field, word string) map[string]any {
		return jsonSchemaAt(field, map[string]any{"contains": map[string]any{"const": word}})
	}
	for _, term := range v.Terms {
		known = append(known, term.Name)
		named := contains(v.Names, term.Name)
		all = append(all, map[string]any{
			"if": jsonSchemaTerm(s, term), "then": named,
			"else": map[string]any{"not": named},
		})
		if !term.StripCritical {
			all = append(all, map[string]any{
				"if": contains(v.Critical, term.Name), "then": named,
			})
		}
	}
	props[v.Critical].(map[string]any)["items"] = map[string]any{"type": "string", "enum": known}
	doc["allOf"] = all
}

func jsonSchemaAt(name string, condition map[string]any) map[string]any {
	return map[string]any{"type": "object", "required": []string{name},
		"properties": map[string]any{name: condition}}
}

func jsonSchemaTerm(s *Definition, term Term) map[string]any {
	if term.Always() {
		return map[string]any{}
	}
	condition := map[string]any{}
	last := term.Last()
	switch {
	case term.Member != "":
		condition = jsonSchemaAt(term.Member, map[string]any{"not": map[string]any{"type": "null"}})
	case term.Membership():
		condition["enum"] = term.Is
	case last.Omit == "absent" || last.Type == "json":
		// An explicitly present opaque null/false/empty value is still raw JSON.
		// Only a member predicate inspects its object shape and null marker.
	case last.EnumList || strings.HasPrefix(last.Type, "list<") || s.Repeated(last.Type) != "":
		condition["minItems"] = 1
	case last.EnumType != nil || last.Type == "string" || last.Type == "binary":
		condition["not"] = map[string]any{"const": ""}
	case last.Type == "i32" || last.Type == "i64":
		condition["not"] = map[string]any{"const": 0}
	case last.Type == "bool":
		condition["const"] = true
	case strings.HasPrefix(last.Type, "map<"):
		condition["minProperties"] = 1
	default:
		condition["minItems"] = 1
	}
	for i := len(term.Path) - 1; i >= 0; i-- {
		condition = jsonSchemaAt(term.Path[i].Name, condition)
	}
	return condition
}

func jsonSchemaField(s *Definition, f Field, defs map[string]any) map[string]any {
	if f.EnumType != nil {
		word := map[string]any{"type": "string"}
		if f.EnumType.Ann["unknown"] == "refuse" {
			words := make([]string, 0, len(f.EnumType.Members))
			for _, m := range f.EnumType.Members {
				words = append(words, m.WireName())
			}
			word["enum"] = words
		}
		if f.EnumList {
			return map[string]any{"type": "array", "items": word}
		}
		return word
	}
	v := jsonSchemaType(s, f.Type, defs)
	if f.Grammar.Named() && f.Type == "string" {
		v["format"] = "date-time"
	}
	if value, ok := f.Ann["equals"]; ok {
		var n json.Number = json.Number(value)
		v["const"] = n
	}
	jsonSchemaBounds(f, v)
	return v
}

func jsonSchemaType(s *Definition, t string, defs map[string]any) map[string]any {
	switch t {
	case "string":
		return map[string]any{"type": "string"}
	case "binary":
		return map[string]any{"type": "string", "contentEncoding": "base64"}
	case "bool":
		return map[string]any{"type": "boolean"}
	case "i32":
		return map[string]any{"type": "integer", "minimum": int64(-1 << 31), "maximum": int64(1<<31 - 1)}
	case "i64":
		return map[string]any{"type": "integer", "minimum": int64(-1 << 63), "maximum": int64(1<<63 - 1)}
	case "json":
		return map[string]any{}
	case "list<string>":
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	case "list<json>":
		return map[string]any{"type": "array"}
	case "map<string,json>":
		return map[string]any{"type": "object"}
	case "map<string,string>":
		return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
	}
	if elem := s.Repeated(t); elem != "" {
		return map[string]any{"type": "array", "items": jsonSchemaType(s, elem, defs)}
	}
	if e := s.Enum(t); e != nil {
		return jsonSchemaField(s, Field{EnumType: e}, defs)
	}
	if elem := listElement(t); elem != "" {
		if e := s.Enum(elem); e != nil {
			return jsonSchemaField(s, Field{EnumType: e, EnumList: true}, defs)
		}
	}
	if imp, ok := s.importedRecord(t); ok {
		if st := imp.Def.Struct(imp.Name); st != nil {
			jsonSchemaDef(imp.Def, *st, t, defs)
			return map[string]any{"$ref": "#/$defs/" + t}
		}
	}
	if st := s.Struct(t); st != nil {
		jsonSchemaDef(s, *st, t, defs)
		return map[string]any{"$ref": "#/$defs/" + t}
	}
	panic(fmt.Sprintf("jsonschema: no schema for type %s", t))
}

// jsonSchemaDef records a referenced record once; a record that refers to
// itself, directly or through others, finds its own entry already there.
func jsonSchemaDef(s *Definition, st Struct, key string, defs map[string]any) {
	if _, ok := defs[key]; ok {
		return
	}
	defs[key] = map[string]any{}
	defs[key] = jsonSchemaStruct(s, st, defs)
}

// verifyJSONSchema checks that the index and every record document parse.
func verifyJSONSchema(e emitted) error {
	if !json.Valid([]byte(e.body)) {
		return fmt.Errorf("jsonschema index is not JSON")
	}
	for name, doc := range jsonSchemaRecords(e.def) {
		if !json.Valid([]byte(doc)) {
			return fmt.Errorf("jsonschema %s is not JSON", name)
		}
	}
	return nil
}

func validateJSONSchemaBackend(s *Definition) error {
	for _, st := range s.Structs {
		if strings.ContainsAny(st.Name, "/\\.") {
			return fmt.Errorf("record %s cannot name a JSON Schema file", st.Name)
		}
	}
	return nil
}

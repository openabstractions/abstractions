package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This hook consumes config@2's existing records. It introduces no schema
// syntax or per-key implementation; every emitted overlay reads descriptors.
func loadConfigCatalogue(def *Definition, path string) error {
	for _, name := range []string{"KeyDescriptor", "Value", "Entry", "Change", "KeyOrigin"} {
		if def.Struct(name) == nil {
			return fmt.Errorf("config catalogue requires %s", name)
		}
	}
	if _, err := configSourceType(def); err != nil {
		return err
	}
	valueFields := map[string]string{"kind": "ValueKind", "boolean": "bool", "integer": "i64", "bytes": "i64", "duration_ms": "i64", "text": "string", "choice": "string", "text_list": "list<string>", "text_map": "map<string,string>"}
	if len(def.Struct("Value").Fields) != len(valueFields) {
		return fmt.Errorf("config catalogue requires the config@2 Value shape")
	}
	for _, field := range def.Struct("Value").Fields {
		if valueFields[field.Name] != field.Type {
			return fmt.Errorf("config catalogue requires Value.%s with its declared type", field.Name)
		}
	}
	allowsDeclared := false
	for _, field := range def.Struct("KeyDescriptor").Fields {
		if field.Name == "allows_unlimited" && field.Type == "bool" && field.Omit == "never" {
			allowsDeclared = true
		}
	}
	if !allowsDeclared {
		return fmt.Errorf("config catalogue requires KeyDescriptor.allows_unlimited as a required bool")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 1<<20 || !utf8.Valid(data) {
		return fmt.Errorf("config catalogue exceeds its byte bound or is not UTF-8")
	}
	if err := configCatalogueJSONShape(data); err != nil {
		return err
	}
	decode := func(data []byte, into any) error {
		r := json.NewDecoder(bytes.NewReader(data))
		r.UseNumber()
		if err := r.Decode(into); err != nil {
			return err
		}
		if err := r.Decode(new(any)); err != io.EOF {
			return fmt.Errorf("trailing catalogue JSON")
		}
		return nil
	}
	var rows []any
	if err := decode(data, &rows); err != nil {
		return fmt.Errorf("config catalogue: %w", err)
	}
	if len(rows) == 0 || len(rows) > 64 {
		return fmt.Errorf("config catalogue requires 1..64 descriptors")
	}
	var schema map[string]any
	if err := decode([]byte(jsonSchemaRecords(def)["KeyDescriptor.schema.json"]), &schema); err != nil {
		return err
	}
	seen := map[string]bool{}
	for i, row := range rows {
		if err := schemaCheck(schema, schema, row); err != nil {
			return fmt.Errorf("config catalogue row %d: %w", i, err)
		}
		if err := configCatalogueRecordChecks(def, "KeyDescriptor", row.(map[string]any)); err != nil {
			return fmt.Errorf("config catalogue row %d: %w", i, err)
		}
		key, _ := row.(map[string]any)["key"].(string)
		if seen[key] {
			return fmt.Errorf("config catalogue duplicate key %q", key)
		}
		seen[key] = true
	}
	canonical, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	def.ConfigCatalogue = string(canonical)
	return nil
}

// JSON Schema intentionally cannot express byte-counted strings, duplicate
// source members or integer source spelling. Admit those same codec limits
// before embedding an otherwise schema-valid catalogue.
func configCatalogueJSONShape(data []byte) error {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] != '\\' {
				i++
				continue
			}
			if i+1 >= len(data) {
				return fmt.Errorf("config catalogue incomplete escape")
			}
			if data[i+1] != 'u' {
				i += 2
				continue
			}
			if i+6 > len(data) {
				return fmt.Errorf("config catalogue incomplete Unicode escape")
			}
			u, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
			if err != nil {
				return err
			}
			if u >= 0xd800 && u <= 0xdbff {
				if i+12 > len(data) || string(data[i+6:i+8]) != `\u` {
					return fmt.Errorf("config catalogue unpaired surrogate")
				}
				low, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return fmt.Errorf("config catalogue unpaired surrogate")
				}
				i += 12
				continue
			}
			if u >= 0xdc00 && u <= 0xdfff {
				return fmt.Errorf("config catalogue unpaired surrogate")
			}
			i += 6
		}
	}
	r := json.NewDecoder(bytes.NewReader(data))
	r.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return fmt.Errorf("config catalogue JSON depth")
		}
		token, err := r.Token()
		if err != nil {
			return err
		}
		if n, ok := token.(json.Number); ok {
			if _, err := n.Int64(); err != nil {
				return fmt.Errorf("config catalogue integer spelling: %w", err)
			}
		}
		if d, ok := token.(json.Delim); ok {
			switch d {
			case '{':
				seen := map[string]bool{}
				for r.More() {
					key, err := r.Token()
					if err != nil {
						return err
					}
					name := key.(string)
					if seen[name] {
						return fmt.Errorf("config catalogue duplicate JSON member %q", name)
					}
					seen[name] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
			case '[':
				for r.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
			default:
				return fmt.Errorf("config catalogue unexpected delimiter")
			}
			_, err = r.Token()
			return err
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := r.Token(); err != io.EOF {
		return fmt.Errorf("config catalogue trailing JSON")
	}
	return nil
}

func configJSONZero(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case string:
		return v == ""
	case json.Number:
		n, err := v.Int64()
		return err == nil && n == 0
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func configCatalogueRecordChecks(def *Definition, name string, obj map[string]any) error {
	st := def.Struct(name)
	if limit := st.maxEncoded(); limit > 0 {
		compact, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		if int64(len(compact)) > limit {
			return fmt.Errorf("%s exceeds max_encoded %d", name, limit)
		}
	}
	if name == "Value" {
		kind, _ := obj["kind"].(string)
		for _, payload := range configPayloads {
			if payload.kind != kind && !configJSONZero(obj[payload.field]) {
				return fmt.Errorf("Value has a non-tagged payload")
			}
		}
	}
	if name == "KeyDescriptor" {
		allows, _ := obj["allows_unlimited"].(bool)
		check := func(value any) error {
			if value, ok := value.(map[string]any); ok && value["kind"] == "unlimited" && !allows {
				return fmt.Errorf("descriptor does not allow unlimited")
			}
			return nil
		}
		if err := check(obj["default"]); err != nil {
			return err
		}
		if presets, ok := obj["presets"].([]any); ok {
			for _, preset := range presets {
				if err := check(preset); err != nil {
					return err
				}
			}
		}
	}
	for _, field := range st.Fields {
		v, present := obj[field.Name]
		if !present {
			continue
		}
		if field.Omit == "zero" && configJSONZero(v) {
			continue
		}
		for _, check := range field.Checks {
			if check.Kind != checkLength {
				continue
			}
			var texts []string
			if check.Element == "" {
				if text, ok := v.(string); ok {
					texts = append(texts, text)
				}
			} else {
				switch v := v.(type) {
				case []any:
					for _, item := range v {
						if text, ok := item.(string); ok {
							texts = append(texts, text)
						}
					}
				case map[string]any:
					for key, item := range v {
						if check.Element == "[key]" {
							texts = append(texts, key)
						} else if text, ok := item.(string); ok {
							texts = append(texts, text)
						}
					}
				}
			}
			for _, text := range texts {
				size := int64(len(text))
				min, _ := strconv.ParseInt(check.Min, 10, 64)
				max, _ := strconv.ParseInt(check.Max, 10, 64)
				if check.Min != "" && size < min || check.Max != "" && size > max {
					return fmt.Errorf("%s UTF-8 byte length", field.Name)
				}
			}
		}
		if child := def.Struct(field.Type); child != nil {
			if err := configCatalogueRecordChecks(def, child.Name, v.(map[string]any)); err != nil {
				return err
			}
		}
		if child := def.Repeated(field.Type); child != "" {
			for _, item := range v.([]any) {
				if err := configCatalogueRecordChecks(def, child, item.(map[string]any)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

var configPayloads = []struct{ kind, field string }{
	{"boolean", "boolean"}, {"integer", "integer"}, {"bytes", "bytes"}, {"duration", "duration_ms"},
	{"text", "text"}, {"choice", "choice"}, {"text_list", "text_list"}, {"text_map", "text_map"},
}

func configValueCondition(s *Definition, lang, prefix string, locals bool) string {
	st := s.Struct("Value")
	field := func(name string) Field {
		for _, f := range st.Fields {
			if f.Name == name {
				return f
			}
		}
		panic("config Value lacks " + name)
	}
	expr := func(name string) string {
		f := field(name)
		if locals {
			return rsLocal(f)
		}
		if lang == "go" {
			return prefix + exported(f.Ident(lang))
		}
		return prefix + f.Ident(lang)
	}
	kind := expr("kind")
	switch lang {
	case "go":
		kind += ".String()"
	case "cpp":
		kind = "wire_name(" + kind + ")"
	case "rust":
		kind += ".as_str()"
	}
	and, or, eq := " && ", " || ", " == "
	if lang == "python" {
		and, or = " and ", " or "
	}
	if lang == "javascript" {
		eq = " === "
	}
	known := []string{kind + eq + strconv.Quote("unlimited")}
	var checks []string
	for _, p := range configPayloads {
		selected := kind + eq + strconv.Quote(p.kind)
		known = append(known, selected)
		e := expr(p.field)
		zero := e + " == 0"
		switch p.kind {
		case "boolean":
			zero = "!" + e
			if lang == "python" {
				zero = "not " + e
			}
		case "text", "choice":
			zero = e + ` == ""`
			if lang == "rust" {
				zero = e + ".is_empty()"
			}
		case "text_list", "text_map":
			switch lang {
			case "go":
				zero = "len(" + e + ") == 0"
			case "python":
				zero = "not " + e
			case "cpp":
				zero = e + ".empty()"
			case "rust":
				zero = e + ".is_empty()"
			case "javascript":
				zero = e + ".length === 0"
				if p.kind == "text_map" {
					zero = "Object.keys(" + e + ").length === 0"
				}
			}
		default:
			if lang == "javascript" {
				zero = e + " === 0n"
			}
		}
		checks = append(checks, "("+selected+or+zero+")")
	}
	return "(" + strings.Join(known, or) + ")" + and + strings.Join(checks, and)
}

func configValueDecodeCheck(b *strings.Builder, s *Definition, st Struct, lang string) {
	if s.ConfigCatalogue == "" || st.Name != "Value" {
		return
	}
	prefix := "v."
	if lang == "rust" {
		prefix = ""
	}
	c := configValueCondition(s, lang, prefix, lang == "rust")
	switch lang {
	case "go":
		fmt.Fprintf(b, "if !(%s) { return nil,r.refuse(\"bad_value\") }\n", c)
	case "python":
		fmt.Fprintf(b, "    if not (%s):\n        raise r.refuse(\"bad_value\")\n", c)
	case "javascript":
		fmt.Fprintf(b, "  if (!(%s)) throw r.refuse(\"bad_value\");\n", c)
	case "cpp":
		fmt.Fprintf(b, "    if (!(%s)) r.refuse(\"bad_value\");\n", c)
	case "rust":
		fmt.Fprintf(b, "    if !(%s) { return r.refuse(\"bad_value\"); }\n", c)
	}
}

func configCatalogueReady(s *Definition) bool {
	return s.ConfigCatalogue != "" && s.Struct("KeyDescriptor") != nil && s.Struct("Entry") != nil && s.Struct("Change") != nil
}

// configSourceType follows the declared provenance list rather than a record name.
func configSourceType(s *Definition) (string, error) {
	origin := s.Struct("KeyOrigin")
	if origin != nil {
		for _, f := range origin.Fields {
			if f.Name == "shadowed" && strings.HasPrefix(f.Type, "list<") && strings.HasSuffix(f.Type, ">") {
				name := strings.TrimSuffix(strings.TrimPrefix(f.Type, "list<"), ">")
				source := s.Struct(name)
				if source != nil {
					location, path := false, false
					for _, field := range source.Fields {
						if field.Name == "location" && (field.Type == "Location" || (field.EnumType != nil && field.EnumType.Name == "Location")) {
							location = true
						}
						if field.Name == "path" && field.Type == "string" {
							path = true
						}
					}
					if location && path {
						return name, nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("config catalogue requires KeyOrigin.shadowed to list a source record with location and path")
}

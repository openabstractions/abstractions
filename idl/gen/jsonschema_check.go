package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"
)

// schemaCheck is a validator for the keywords the jsonschema backend emits,
// shared by generation-time catalogue admission and backend conformance.
func schemaCheck(root, schema map[string]any, v any) error {
	for _, child := range asList(schema["allOf"]) {
		if err := schemaCheck(root, child.(map[string]any), v); err != nil {
			return err
		}
	}
	if condition, ok := schema["if"].(map[string]any); ok {
		key := "else"
		if schemaCheck(root, condition, v) == nil {
			key = "then"
		}
		if child, ok := schema[key].(map[string]any); ok {
			if err := schemaCheck(root, child, v); err != nil {
				return err
			}
		}
	}
	if child, ok := schema["not"].(map[string]any); ok && schemaCheck(root, child, v) == nil {
		return fmt.Errorf("not condition matched")
	}
	if expected, ok := schema["const"]; ok {
		equal := reflect.DeepEqual(expected, v)
		a, an := expected.(json.Number)
		b, bn := v.(json.Number)
		if an && bn {
			x, _ := new(big.Rat).SetString(string(a))
			y, _ := new(big.Rat).SetString(string(b))
			equal = x.Cmp(y) == 0
		}
		if !equal {
			return fmt.Errorf("constant differs")
		}
	}
	if child, ok := schema["contains"].(map[string]any); ok {
		found := false
		for _, item := range asList(v) {
			found = found || schemaCheck(root, child, item) == nil
		}
		if !found {
			return fmt.Errorf("contains condition absent")
		}
	}

	if ref, ok := schema["$ref"].(string); ok {
		name := strings.TrimPrefix(ref, "#/$defs/")
		def, ok := root["$defs"].(map[string]any)[name].(map[string]any)
		if !ok {
			return fmt.Errorf("unresolved %s", ref)
		}
		return schemaCheck(root, def, v)
	}
	if typ, ok := schema["type"].(string); ok {
		okType := false
		switch typ {
		case "object":
			_, okType = v.(map[string]any)
		case "array":
			_, okType = v.([]any)
		case "string":
			_, okType = v.(string)
		case "boolean":
			_, okType = v.(bool)
		case "null":
			okType = v == nil
		case "integer":
			n, isNum := v.(json.Number)
			x, parsed := new(big.Rat).SetString(string(n))
			okType = isNum && parsed && x.IsInt()
		}
		if !okType {
			return fmt.Errorf("%v is not %s", v, typ)
		}
	}
	if words, ok := schema["enum"].([]any); ok {
		found := false
		for _, w := range words {
			found = found || w == v
		}
		if !found {
			return fmt.Errorf("%v not in enum", v)
		}
	}
	if n, ok := v.(json.Number); ok {
		x, parsed := new(big.Rat).SetString(string(n))
		if !parsed {
			return fmt.Errorf("invalid JSON number %s", n)
		}
		for _, bound := range []struct {
			key   string
			below bool
		}{{"minimum", true}, {"maximum", false}} {
			if raw, exists := schema[bound.key]; exists {
				limit, valid := new(big.Rat).SetString(fmt.Sprint(raw))
				if !valid {
					return fmt.Errorf("invalid schema bound %v", raw)
				}
				if (bound.below && x.Cmp(limit) < 0) || (!bound.below && x.Cmp(limit) > 0) {
					return fmt.Errorf("%s violates %s", n, bound.key)
				}
			}
		}
	}
	if text, ok := v.(string); ok {
		for _, bound := range []struct {
			key   string
			below bool
		}{{"minLength", true}, {"maxLength", false}} {
			if raw, exists := schema[bound.key]; exists {
				limit, valid := new(big.Rat).SetString(fmt.Sprint(raw))
				if !valid {
					return fmt.Errorf("invalid string bound %v", raw)
				}
				length := new(big.Rat).SetInt64(int64(utf8.RuneCountInString(text)))
				if bound.below && length.Cmp(limit) < 0 || !bound.below && length.Cmp(limit) > 0 {
					return fmt.Errorf("string violates %s", bound.key)
				}
			}
		}
		if pattern, ok := schema["pattern"].(string); ok {
			// JSON Schema spells Unicode escapes with ECMAScript \uNNNN;
			// Go's regexp parser spells the same code points \x{NNNN}.
			pattern = regexp.MustCompile(`\\u[0-9a-fA-F]{4}`).ReplaceAllStringFunc(pattern, func(s string) string { return `\x{` + s[2:] + `}` })
			matched, err := regexp.MatchString(pattern, text)
			if err != nil || !matched {
				return fmt.Errorf("string violates pattern")
			}
		}
	}
	if list, ok := v.([]any); ok {
		for _, bound := range []struct {
			key   string
			below bool
		}{{"minItems", true}, {"maxItems", false}} {
			if raw, exists := schema[bound.key]; exists {
				n, valid := new(big.Rat).SetString(fmt.Sprint(raw))
				if !valid {
					return fmt.Errorf("invalid item bound %v", raw)
				}
				count := new(big.Rat).SetInt64(int64(len(list)))
				if (bound.below && count.Cmp(n) < 0) || (!bound.below && count.Cmp(n) > 0) {
					return fmt.Errorf("array violates %s", bound.key)
				}
			}
		}
	}

	if items, ok := schema["items"].(map[string]any); ok {
		for _, e := range v.([]any) {
			if err := schemaCheck(root, items, e); err != nil {
				return err
			}
		}
	}
	if obj, ok := v.(map[string]any); ok {
		if names, ok := schema["propertyNames"].(map[string]any); ok {
			for key := range obj {
				if err := schemaCheck(root, names, key); err != nil {
					return err
				}
			}
		}
		for _, bound := range []struct {
			key   string
			below bool
		}{{"minProperties", true}, {"maxProperties", false}} {
			if raw, exists := schema[bound.key]; exists {
				n, valid := new(big.Rat).SetString(fmt.Sprint(raw))
				if !valid {
					return fmt.Errorf("invalid member bound %v", raw)
				}
				count := new(big.Rat).SetInt64(int64(len(obj)))
				if (bound.below && count.Cmp(n) < 0) || (!bound.below && count.Cmp(n) > 0) {
					return fmt.Errorf("object violates %s", bound.key)
				}
			}
		}
		props, _ := schema["properties"].(map[string]any)
		for _, r := range asList(schema["required"]) {
			if _, ok := obj[r.(string)]; !ok {
				return fmt.Errorf("missing %s", r)
			}
		}
		for k, e := range obj {
			if p, ok := props[k].(map[string]any); ok {
				if err := schemaCheck(root, p, e); err != nil {
					return fmt.Errorf("%s: %w", k, err)
				}
				continue
			}
			switch extra := schema["additionalProperties"].(type) {
			case bool:
				if !extra {
					return fmt.Errorf("unknown member %s", k)
				}
			case map[string]any:
				if err := schemaCheck(root, extra, e); err != nil {
					return fmt.Errorf("%s: %w", k, err)
				}
			}
		}
	}
	return nil
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

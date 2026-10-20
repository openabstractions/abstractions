package main

import (
	"encoding/json"
	"strconv"
	"strings"
)

// jsonSchemaPatterns is each text class as an ECMA-262 expression, the
// dialect JSON Schema's pattern keyword names.
var jsonSchemaPatterns = map[string]string{
	"no_control":        `^[^\u0000-\u001f\u007f-\u009f]*$`,
	"ascii_name":        `^[A-Za-z0-9_.-]*$`,
	"ascii_lower_token": `^[a-z0-9_.:-]*(?![\s\S])`,
}

// jsonSchemaBounds states f's declared length, range and pattern ([DEF-A11])
// in the schema keywords that can. A string's length counts UTF-8 bytes and
// maxLength counts characters, so the schema states the byte bound as a
// character bound: weaker, and still true of every value a reader accepts.
// An optional field's bound is stated only when its zero passes it, because a
// reader admits the zero a writer omits.
func jsonSchemaBounds(f Field, v map[string]any) {
	for _, c := range f.Checks {
		if c.Element != "" {
			key := "items"
			if c.Element == "[key]" {
				key = "propertyNames"
			}
			if c.Element == "[value]" {
				key = "additionalProperties"
			}
			child, ok := v[key].(map[string]any)
			if !ok {
				child = map[string]any{"type": "string"}
				v[key] = child
			}
			c.Element = ""
			scalar := Field{Type: "string", Omit: "never", Checks: []check{c}}
			jsonSchemaBounds(scalar, child)
			continue
		}
		if f.Omit != "never" && !scalarAbsent(f) && !zeroInside(c) {
			continue
		}
		switch c.Kind {
		case checkRange:
			if c.Min != "" {
				v["minimum"] = json.Number(c.Min)
			}
			if c.Max != "" {
				v["maximum"] = json.Number(c.Max)
			}
		case checkLength:
			low, high := "", ""
			switch {
			case f.Type == "string":
				if c.Min != "" {
					low = "minLength"
				}
				high = "maxLength"
			case strings.HasPrefix(f.Type, "list<"):
				low, high = "minItems", "maxItems"
			case strings.HasPrefix(f.Type, "map<"):
				low, high = "minProperties", "maxProperties"
			}
			if low != "" && c.Min != "" {
				min := c.Min
				if f.Type == "string" {
					min = "1" // at least one byte is at least one character
				}
				v[low] = json.Number(min)
			}
			if high != "" && c.Max != "" {
				v[high] = json.Number(c.Max)
			}
		case checkPattern:
			pattern := jsonSchemaPatterns[c.Class]
			if prior, ok := v["pattern"]; ok && prior != pattern {
				v["allOf"] = []any{map[string]any{"pattern": prior}, map[string]any{"pattern": pattern}}
				delete(v, "pattern")
			} else {
				v["pattern"] = pattern
			}
		}
	}
}

func zeroInside(c check) bool {
	if c.Kind == checkPattern {
		return true
	}
	min, _ := strconv.ParseInt(c.Min, 10, 64)
	max, _ := strconv.ParseInt(c.Max, 10, 64)
	return (c.Min == "" || min <= 0) && (c.Max == "" || max >= 0)
}

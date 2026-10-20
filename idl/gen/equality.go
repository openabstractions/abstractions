package main

import (
	"fmt"
	"strconv"
	"strings"
)

// lowerEquals reads one field's equals and equals_refusal. Equality
// constrains a supplied integer; it never supplies a missing value.
func lowerEquals(f Field, refusals []Refusal) ([]check, error) {
	value, has := f.Ann["equals"]
	word, hasWord := f.Ann["equals_refusal"]
	if !has && !hasWord {
		return nil, nil
	}
	if !has || !hasWord || word == "" {
		return nil, fmt.Errorf("field %s requires both equals and equals_refusal", f.Name)
	}
	if f.Omit != "never" || (f.Type != "i32" && f.Type != "i64") {
		return nil, fmt.Errorf("field %s: equals requires a required i32 or i64", f.Name)
	}
	bits := 64
	if f.Type == "i32" {
		bits = 32
	}
	n, e := strconv.ParseInt(value, 10, bits)
	if e != nil || strconv.FormatInt(n, 10) != value {
		return nil, fmt.Errorf("field %s: equals is a canonical in-range %s decimal", f.Name, f.Type)
	}
	if !structureRefusal(refusals, word) {
		return nil, fmt.Errorf("field %s: equals_refusal %q must name a declared structure refusal", f.Name, word)
	}
	return []check{{Field: f, Kind: checkEquals, Word: word, Value: value}}, nil
}

// emitEqualities prints a record's checks at a decoder (encode false) or an
// encoder (encode true) boundary.
func emitEqualities(b *strings.Builder, st Struct, lang string, encode bool) {
	site := siteDecode
	if encode {
		site = siteEncode
	}
	emitChecks(b, lowerChecks(st), lang, site)
}

func (s *Definition) HasEqualities() bool {
	for _, st := range s.Structs {
		for _, f := range st.Fields {
			if _, ok := f.Ann["equals"]; ok {
				return true
			}
		}
	}
	return false
}

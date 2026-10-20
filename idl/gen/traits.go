package main

// Method traits ([DEF-S3]; abstraction-ipc CONTRACT.md IPC-I1, IPC-I2;
// idl/CONVENTIONS.md CONV-I2). A method says how a repeat of it recovers:
// idempotent (a repeat with equal arguments has the first call's effect),
// readonly (it changes nothing), or idempotency_key naming the caller-owned
// key that makes a repeat safe. Generated clients retry nothing whatever the
// trait says; the trait tells a caller what it may do after a lost reply.
// Explicit idempotent=false retains the empty unsafe trait and promises no replay.

import (
	"fmt"
	"strings"
)

// methodTrait reads one method's trait annotations.
func methodTrait(method string, ann map[string]string) (string, string, error) {
	var traits []string
	trait, key := "", ""
	for _, word := range []string{"idempotent", "readonly"} {
		if v, ok := ann[word]; ok {
			if v != "true" && !(word == "idempotent" && v == "false") {
				allowed := `"true"`
				if word == "idempotent" {
					allowed += ` or "false"`
				}
				return "", "", fmt.Errorf("method %s: %s = %q; the trait says %s = %s", method, word, v, word, allowed)
			}
			traits = append(traits, word)
			if v == "true" {
				trait = word
			}
		}
	}
	if v, ok := ann["idempotency_key"]; ok {
		if v == "" {
			return "", "", fmt.Errorf("method %s: idempotency_key names no argument", method)
		}
		traits = append(traits, "idempotency_key")
		trait, key = "keyed", v
	}
	if len(traits) > 1 {
		return "", "", fmt.Errorf("method %s declares %s; a method declares one trait", method, strings.Join(traits, " and "))
	}
	return trait, key, nil
}

// validateTraitKeys requires each idempotency_key to name a required string
// argument, or a required string reached through required record fields.
func (p *parser) validateTraitKeys() error {
	for _, svc := range p.def.Services {
		for _, m := range svc.Methods {
			if m.Trait != "keyed" {
				continue
			}
			path := strings.Split(m.IdempotencyKey, ".")
			owner := p.def
			var found *Field
			for i := range m.Args {
				if m.Args[i].Name == path[0] {
					found = &m.Args[i]
				}
			}
			for _, field := range path[1:] {
				if found == nil || found.Omit != "never" {
					found = nil
					break
				}
				st := owner.byName[found.Type]
				if st == nil {
					if imported, ok := owner.importedRecord(found.Type); ok {
						owner = imported.Def
						st = owner.byName[imported.Name]
					}
				}
				found = nil
				if st != nil {
					for i := range st.Fields {
						if st.Fields[i].Name == field {
							found = &st.Fields[i]
						}
					}
				}
			}
			if found == nil || found.Type != "string" || found.Omit != "never" {
				return fmt.Errorf("service %s method %s: idempotency_key = %q names no required string argument or record field", svc.Name, m.Name, m.IdempotencyKey)
			}
		}
	}
	return nil
}

// traitText is a method's trait as the reference page states it.
func traitText(m Method) string {
	if m.NonIdempotent {
		return "no idempotency or replay guarantee"
	}
	switch m.Trait {
	case "keyed":
		return "idempotency key " + m.IdempotencyKey
	case "":
		return ""
	}
	return m.Trait
}

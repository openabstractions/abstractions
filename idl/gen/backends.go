package main

import (
	"fmt"
	"strings"
)

// backendsTable is the output of gen --backends: one Markdown row per backend
// stating what it generates. Records, clients and dispatchers come from the
// backends table; services, --no-ipc, includes and binary come from asking the
// backend's own validators about a probe definition, so the table changes
// when a backend's support changes. idl/LANGUAGE.md and idl/README.md embed
// this text between backendsBegin and backendsEnd, and TestBackendsEmbedded
// fails while either copy is stale.
func backendsTable() string {
	var b strings.Builder
	b.WriteString("| Backend | Records | Services | Clients | Dispatchers | `--no-ipc` | Includes and `--named-codecs` | `binary` |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|\n")
	service := backendProbe(`struct Record {1: required string value} (document="true", unknown_fields="grant")
service Probe { oneway void Write(1: Record record) } (wire_name="example.probe/probe@1")
`)
	direct := *service
	direct.NoIPC = true
	named := *backendProbe(`struct Record {1: required string value} (document="true", unknown_fields="grant")
`)
	named.NamedCodecs = true
	binary := backendProbe(`struct Record {1: required binary value} (document="true", unknown_fields="grant")
`)
	for _, be := range backends {
		code := be.emitsCode
		// The jsonschema backend writes record schemas only; a service is the
		// bindings' business, so its Services cell is no.
		services := validateServiceBackend(service, be.lang) == nil && be.lang != "jsonschema"
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s |\n", be.lang,
			backendCell(true, code),
			backendCell(services, code),
			backendCell(services && be.clients, true),
			backendCell(services && be.dispatchers, true),
			backendCell(validateServiceBackend(&direct, be.lang) == nil, code),
			backendCell(validateIncludes(&named, be.lang) == nil, code),
			backendCell(validateBinaryBackend(binary, be.lang) == nil, code))
	}
	return b.String()
}

// backendCell is "yes" for generated code, "documented" for a supported
// feature of a backend that writes a page, and "no" for a refused feature.
func backendCell(supported, code bool) string {
	switch {
	case !supported:
		return "no"
	case code:
		return "yes"
	}
	return "documented"
}

const backendsBegin = "<!-- gen --backends: begin; regenerate with GOWORK=off go -C idl/gen run . --backends -->\n"
const backendsEnd = "<!-- gen --backends: end -->"

func backendProbe(body string) *Definition {
	d, err := parse(backendProbeHead + body)
	if err != nil {
		panic("gen --backends probe: " + err.Error())
	}
	return d
}

const backendProbeHead = `encoding json {
  escape         = "minimal"
  indent         = "2"
  map_keys       = "utf8-bytes"
  numbers        = "integer-decimal"
  opaque         = "verbatim"
  terminator     = "newline"
  duplicate_keys = "refuse"
  depth_limit    = "64"
}
refusal {
   1: malformed        (stage = "grammar")
   2: bad_string       (stage = "grammar")
   3: number_spelling  (stage = "grammar")
   4: wrong_type       (stage = "grammar")
   5: bad_timestamp    (stage = "grammar")
   6: depth_exceeded   (stage = "grammar")
   7: duplicate_key    (stage = "grammar")
   8: duplicate_field  (stage = "structure")
   9: unknown_field    (stage = "structure")
  10: missing_field    (stage = "structure")
  11: trailing_bytes   (stage = "document")
  12: unknown_critical (stage = "derivation")
  13: not_a_subset     (stage = "derivation")
  14: content_mismatch (stage = "derivation")
}
`

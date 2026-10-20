package main

// A service-result postcondition is non-wire behavior, like observation traits.
// Standalone record codecs retain their structural language. Resolve the typed
// relation once; backends print only this IR at RPC result boundaries.
import (
	"fmt"
	"strings"
)

type resultPostcondition struct {
	Payload, Discriminator Field
	Members                []string // validated wire spellings in enum declaration order
	DiscriminatorString    bool     // open vocabularies use native string carriers
	Cleanup                *decodedCleanup
}

func lowerResultPostcondition(s *Definition, m Method) (*resultPostcondition, error) {
	if m.ResultPresent == "" && m.ResultWhen == "" && m.ResultIs == "" {
		return nil, nil
	}
	if m.Oneway || m.ResultPresent == "" || m.ResultWhen == "" || m.ResultIs == "" {
		return nil, fmt.Errorf("%s: result_present, result_when and result_is require one reply and all three names", m.Name)
	}
	st := s.Struct(m.Result.Type)
	if st == nil {
		return nil, fmt.Errorf("%s: result postcondition requires a record result", m.Name)
	}
	var payload, discriminator *Field
	for i := range st.Fields {
		f := &st.Fields[i]
		if f.Name == m.ResultPresent {
			payload = f
		}
		if f.Name == m.ResultWhen {
			discriminator = f
		}
	}
	if payload == nil || payload.Omit != "absent" || discriminator == nil || discriminator.Omit != "never" {
		return nil, fmt.Errorf("%s: result_present names an optional absent field and result_when a required sibling enum", m.Name)
	}
	en := s.Enum(discriminator.Type)
	if en == nil {
		return nil, fmt.Errorf("%s: result_when names a required sibling enum", m.Name)
	}
	condition := func(members []string) *resultPostcondition {
		return &resultPostcondition{Payload: *payload, Discriminator: *discriminator, Members: members, DiscriminatorString: en.Ann["unknown"] == "grant", Cleanup: lowerDecodedCleanup(s, m.Result.Type, map[*Struct]bool{})}
	}
	// Preserve the original exact-member language, including a wire spelling
	// containing a comma. Set syntax applies when no complete spelling matches.
	for _, member := range en.Members {
		if member.WireName() == m.ResultIs {
			return condition([]string{member.WireName()}), nil
		}
	}
	requested := map[string]bool{}
	for _, word := range strings.Split(m.ResultIs, ",") {
		if word == "" || requested[word] {
			return nil, fmt.Errorf("%s: result_is has an empty or duplicate member", m.Name)
		}
		requested[word] = true
	}
	var members []string
	for _, member := range en.Members {
		if requested[member.WireName()] {
			members = append(members, member.WireName())
			delete(requested, member.WireName())
		}
	}
	if len(requested) != 0 {
		return nil, fmt.Errorf("%s: result_is names an undeclared discriminator member", m.Name)
	}
	return condition(members), nil
}

// emitResultPostcondition rejects malformed method results using the existing
// common service error, without adding record refusal words or wire metadata.
func emitResultPostcondition(b *strings.Builder, m Method, lang, value string) {
	emitCheckedResultPostcondition(b, m, lang, value, false)
}

func emitClientResultPostcondition(b *strings.Builder, m Method, lang, value string) {
	emitCheckedResultPostcondition(b, m, lang, value, true)
}

func emitCheckedResultPostcondition(b *strings.Builder, m Method, lang, value string, client bool) {
	p := m.ResultPostcondition
	if p == nil {
		return
	}
	field := func(f Field) string {
		n := f.Ident(lang)
		if lang == "go" {
			n = exported(n)
		}
		return value + "." + n
	}
	d, v := field(p.Discriminator), field(p.Payload)
	comparison := map[string]string{"go": "%s.String()==%q", "cpp": "wire_name(%s)==%q", "python": "%s == %q", "javascript": "%s === %q", "rust": "%s.as_str() == %q"}[lang]
	if p.DiscriminatorString {
		switch lang {
		case "go":
			comparison = "string(%s)==%q"
		case "cpp":
			comparison = "%s==%q"
		}
	}
	var terms []string
	for _, member := range p.Members {
		terms = append(terms, fmt.Sprintf(comparison, d, member))
	}
	separator := " || "
	if lang == "python" {
		separator = " or "
	}
	condition := strings.Join(terms, separator)
	var cleanup strings.Builder
	if client {
		emitDecodedCleanup(&cleanup, p.Cleanup, lang, value)
	}
	switch lang {
	case "go":
		fmt.Fprintf(b, "if (%s)!=(%s!=nil){%serr=&ServiceError{Code:ServiceErrorCodeInvalidResult};return}\n", condition, v, cleanup.String())
	case "cpp":
		if cleanup.Len() != 0 {
			fmt.Fprintf(b, "if ((%s)!=%s.has_value()){\n%sthrow ServiceError(\"invalid_result\",\"\");}\n", condition, v, cleanup.String())
			break
		}
		fmt.Fprintf(b, "if ((%s)!=%s.has_value()) throw ServiceError(\"invalid_result\",\"\");\n", condition, v)
	case "python":
		fmt.Fprintf(b, "        if (%s) != (%s is not None):\n            raise ServiceError(\"invalid_result\", \"\")\n", condition, v)
	case "javascript":
		if cleanup.Len() != 0 {
			fmt.Fprintf(b, "    if ((%s) !== (%s !== undefined && %s !== null)){\n%sthrow new ServiceError(\"invalid_result\", \"\");}\n", condition, v, v, cleanup.String())
			break
		}
		fmt.Fprintf(b, "    if ((%s) !== (%s !== undefined && %s !== null)) throw new ServiceError(\"invalid_result\", \"\");\n", condition, v, v)
	case "rust":
		fmt.Fprintf(b, "        if (%s) != %s.is_some() { %sreturn Err(CallError::Service(ServiceError { code: \"invalid_result\".into(), message: ::std::string::String::new() })); }\n", condition, v, cleanup.String())
	}
}

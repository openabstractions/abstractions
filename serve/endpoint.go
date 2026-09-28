package main

import (
	"fmt"
	"io"
	"os"
)

// runtimeEndpointVar is ABSTRACTION_RUNTIME_ENDPOINT: it selects which
// endpoint a command dials when the command's own --endpoint flag names
// none, but it supplies no server identity by itself. Every command in this
// program applies the same three-step rule through resolveEndpoint.
const runtimeEndpointVar = "ABSTRACTION_RUNTIME_ENDPOINT"

// endpointSource says which of the three rules chose a command's endpoint:
// an explicit --endpoint flag, ABSTRACTION_RUNTIME_ENDPOINT, or the installed
// runtime's own default. Every failure message that names the endpoint also
// states the source, so a person who set the variable can tell whether it
// was honored.
type endpointSource int

const (
	endpointInstalled endpointSource = iota
	endpointExplicit
	endpointFromVar
)

// String names the source the way a failure message quotes it.
func (s endpointSource) String() string {
	switch s {
	case endpointExplicit:
		return "explicit endpoint"
	case endpointFromVar:
		return runtimeEndpointVar
	default:
		return "the installed runtime"
	}
}

// resolveEndpoint is the one rule every command in this program applies to
// choose a runtime endpoint: explicit takes an --endpoint flag's value, which
// wins when it is not empty; otherwise ABSTRACTION_RUNTIME_ENDPOINT wins when
// it is set; otherwise the installed runtime's own default applies, and
// endpoint comes back empty for the command's own installed-runtime discovery
// to fill in. Every command that connects to a runtime resolves its
// connection through this function, and states the same three steps in its
// own --help.
func resolveEndpoint(explicit string) (endpoint string, source endpointSource) {
	if explicit != "" {
		return explicit, endpointExplicit
	}
	if v := os.Getenv(runtimeEndpointVar); v != "" {
		return v, endpointFromVar
	}
	return "", endpointInstalled
}

// warnUnverifiedEndpoint writes the one sentence a command prints, to
// diagnostics, when ABSTRACTION_RUNTIME_ENDPOINT alone selected the
// endpoint: the installed runtime's registration does not verify a
// different server, so the command connects unverified instead of silently
// carrying that identity over
// (research/packaged-activation/GATEWAY-REFUSAL-2026-09-22.md).
func warnUnverifiedEndpoint(diagnostics io.Writer, command, endpoint string) {
	//unchecked: a warning with no return value to report a write failure through
	fmt.Fprintf(diagnostics, "%s: warning: %s names %s; the installed runtime's registration does not verify a different endpoint, so this connects unverified\n", command, runtimeEndpointVar, endpoint)
}

// endpointRuleSentence is the one sentence every command's --help states,
// naming the three-step rule resolveEndpoint applies.
const endpointRuleSentence = "The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime."

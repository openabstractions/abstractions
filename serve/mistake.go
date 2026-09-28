package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"syscall"
)

// alreadyReported marks an error whose command has already printed its own
// complete message to diagnostics. This program's single outer dispatch
// prints every other returned error once, prefixed "openabstractions: "; a
// command wraps its error in alreadyReported to keep that dispatch from
// printing a second, redundant copy.
type alreadyReported struct{ err error }

func (a *alreadyReported) Error() string { return a.err.Error() }
func (a *alreadyReported) Unwrap() error { return a.err }

// reported tells the dispatcher whether err's command already printed its
// own message, so the dispatcher's own "openabstractions: <err>" line stays
// the only place an error appears for every command that has not adopted
// this reporting shape yet, and never appears twice for one that has.
func reported(err error) bool {
	var marked *alreadyReported
	return errors.As(err, &marked)
}

// usageLine is the first line of a command's Usage: ... help text, the line
// every mistake shape below prints beside the one line naming the mistake.
func usageLine(usage string) string {
	if i := strings.IndexByte(usage, '\n'); i >= 0 {
		return usage[:i]
	}
	return usage
}

// commandMistake is the two-line shape a "which command" mistake prints:
// naming (an unknown command, a missing one, a missing subcommand) and how
// to list the commands that were available. helpCmd is the full command
// line that prints them, such as `openabstractions --help` or
// `openabstractions jobs --help`.
func commandMistake(diagnostics io.Writer, problem, helpCmd string) error {
	return writeMistake(diagnostics, fmt.Errorf("%s", problem),
		fmt.Sprintf("openabstractions: %s\n", problem),
		fmt.Sprintf("Run %q for the commands.\n", helpCmd))
}

// flagMistake is the three-line shape a leaf command's usage mistake
// prints: the problem, the command's own usage line, and how to see its
// flags. Nothing else follows.
func flagMistake(diagnostics io.Writer, command, usage, problem string) error {
	return writeMistake(diagnostics, fmt.Errorf("%s: %s", command, problem),
		fmt.Sprintf("openabstractions %s: %s\n", command, problem),
		usageLine(usage)+"\n",
		fmt.Sprintf("Run \"openabstractions %s --help\" for the flags.\n", command))
}

// writeMistake marks a mistake reported only after every diagnostic line was
// written. A failed write leaves the error unmarked for the outer dispatcher.
func writeMistake(diagnostics io.Writer, problem error, lines ...string) error {
	for _, line := range lines {
		n, err := io.WriteString(diagnostics, line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		if err != nil {
			return &exitError{exitUsage, fmt.Errorf("%w (diagnostic write: %w)", problem, err)}
		}
	}
	return &exitError{exitUsage, &alreadyReported{problem}}
}

// flagErrorName recovers the flag name from the flag package's own "flag
// provided but not defined: -name" parse error, without a leading dash.
func flagErrorName(err error) (string, bool) {
	const notDefined = "flag provided but not defined: -"
	msg := err.Error()
	if i := strings.Index(msg, notDefined); i >= 0 {
		return msg[i+len(notDefined):], true
	}
	return "", false
}

// flagAsTyped finds name (as the flag package's error names it, without a
// leading dash) among args and returns the token exactly as it was typed,
// one dash or two: the flag package's own error text always renders it with
// one, regardless of how many the caller used.
func flagAsTyped(args []string, name string) string {
	for _, a := range args {
		stripped := strings.TrimLeft(a, "-")
		if stripped == name || strings.HasPrefix(stripped, name+"=") {
			return a
		}
	}
	return "-" + name
}

// flagValueAsTyped finds --name (or -name) among args and returns the value
// text that followed it or was joined to it with '=', exactly as the caller
// typed it: a parsed flag.Value's own String() renormalizes it (a duration's
// zero becomes "0s", not the "0" someone typed), which a message naming
// "the value as typed" must not do.
func flagValueAsTyped(args []string, name string) (string, bool) {
	for i, a := range args {
		stripped := strings.TrimLeft(a, "-")
		if stripped == name && i+1 < len(args) {
			return args[i+1], true
		}
		if strings.HasPrefix(stripped, name+"=") {
			return strings.TrimPrefix(stripped, name+"="), true
		}
	}
	return "", false
}

// invalidFlagValue matches the flag package's own two renderings of a value
// that failed to parse: "invalid value \"X\" for flag -name: ..." and, for a
// bool, "invalid boolean value \"X\" for -name: ...".
var invalidFlagValue = regexp.MustCompile(`^invalid (?:boolean )?value "(.*)" for (?:flag )?-(\S+):`)

// flagValueProblem names the flag, the value as typed, and the form it
// accepts, in place of the flag package's own opaque "invalid value ...:
// parse error" (which, on other Go versions, is "strconv.ParseInt: parsing
// ...", jargon no less opaque to read). flags supplies the accepted form:
// each flag.Value's own concrete type says whether it takes a duration, a
// whole number or true/false.
func flagValueProblem(flags *flag.FlagSet, name, value string) string {
	kind, example := "value", ""
	if f := flags.Lookup(name); f != nil {
		switch fmt.Sprintf("%T", f.Value) {
		case "*flag.durationValue":
			kind, example = "duration", " (examples: 5s, 1m30s, 500ms)"
		case "*flag.int64Value", "*flag.intValue", "*flag.uint64Value", "*flag.uintValue":
			kind = "whole number"
		case "*flag.float64Value":
			kind = "number"
		case "*flag.boolValue":
			kind, example = "true or false", " (examples: true, false, 1, 0)"
		}
	}
	return fmt.Sprintf("--%s %q is not a %s%s", name, value, kind, example)
}

// badFlag turns a FlagSet.Parse error into the standard flagMistake shape.
// flag.ErrHelp is not a mistake and passes through unchanged, for the rare
// case help reaches FlagSet.Parse itself (a command whose own leading
// isHelp check did not catch it, such as --help after other flags).
func badFlag(flags *flag.FlagSet, diagnostics io.Writer, command, usage string, args []string, err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return err
	}
	problem := err.Error()
	switch {
	case invalidFlagValue.MatchString(problem):
		m := invalidFlagValue.FindStringSubmatch(problem)
		problem = flagValueProblem(flags, m[2], m[1])
	default:
		if name, ok := flagErrorName(err); ok {
			problem = "unknown flag " + flagAsTyped(args, name)
		}
	}
	return flagMistake(diagnostics, command, usage, problem)
}

// pathProblem names flagName, the path as typed, and why opening it failed,
// in place of the raw "open <path>: The system cannot find the file
// specified" (or its Unix equivalent) os.Open and os.Stat return.
func pathProblem(flagName, value string, err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Sprintf("%s %q does not exist", flagName, value)
	case errors.Is(err, os.ErrPermission):
		return fmt.Sprintf("%s %q is not readable", flagName, value)
	default:
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return fmt.Sprintf("%s %q: %s", flagName, value, pathErr.Err)
		}
		return fmt.Sprintf("%s %q: %v", flagName, value, err)
	}
}

// isConnectFailure reports whether err is the low-level dial failure a
// platform endpoint path gives when nothing listens there: not found (no
// pipe or socket by that name), refused (something else answered and
// declined), or timed out. It is never a typed service refusal; callers
// check for one of those first.
func isConnectFailure(err error) bool {
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// dialFailureText matches the standard library's own two renderings of a
// low-level dial failure, the only wording Go ever produces for one: "open
// <path>: ..." for a named pipe, "dial <net> <addr>: ..." for a socket,
// naming the endpoint and, after the colon, the reason.
var dialFailureText = regexp.MustCompile(`(?:^|: )(?:open|dial \S+) (\S+): (.+)$`)

// connectFailureText reports the same three low-level dial failures
// isConnectFailure recognizes from a live error (not found, refused, timed
// out), read instead from text a failure's own message left behind after it
// crossed a boundary that keeps only strings, such as probe's own
// Result.Detail. It matches only the standard library's own wording for
// those three, never a typed service refusal or any other error text.
func connectFailureText(detail string) (endpoint string, ok bool) {
	m := dialFailureText.FindStringSubmatch(detail)
	if m == nil {
		return "", false
	}
	reason := strings.ToLower(m[2])
	switch {
	case strings.Contains(reason, "cannot find the file specified"),
		strings.Contains(reason, "no such file or directory"),
		strings.Contains(reason, "connection refused"),
		strings.Contains(reason, "context deadline exceeded"),
		strings.Contains(reason, "i/o timeout"):
		return m[1], true
	}
	return "", false
}

// noRuntimeListensAt is the sentence every command's shared resolver path
// prints for a connect failure that named an endpoint and found nothing
// there: status describe's own words, naming which of resolveEndpoint's
// three rules chose the endpoint, so a person who set
// ABSTRACTION_RUNTIME_ENDPOINT can tell it was honored even though nothing
// answered there.
func noRuntimeListensAt(command, endpoint string, source endpointSource) string {
	return fmt.Sprintf(`%s: no runtime listens at %s (%s); run "openabstractions status" to list the endpoints that do`, command, endpoint, source)
}

// containsHelp reports whether args asks for help anywhere in it, not only
// in the first position: requested help always goes to stdout, wherever the
// caller put --help, -h or help among the other flags and arguments.
func containsHelp(args []string) bool {
	for _, a := range args {
		if isHelp(a) {
			return true
		}
	}
	return false
}

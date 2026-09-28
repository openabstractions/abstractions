package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

// probeClientName is the file name under which this program runs only `probe`.
// A copy of openabstractions under another path is a different rights subject:
// rules name the account and the absolute executable path the runtime proved.
const probeClientName = "openabstractions-probe"

const probeUsageHead = `Usage: openabstractions probe <capability> [action] [argument] [options]
       openabstractions-probe <capability> [action] [argument] [options]

Calls one capability through the resolved service, as this program, and
prints the typed outcome, naming the rights rule that decided it where the
runtime enforces one. Probes read, except the one marked WRITES, which runs
only when its capability and action are both named and is never part of
all.

Capabilities and actions (* marks the default when no action is named):
`

const probeUsageTail = `  all                     every default action that needs no argument and
                          writes nothing, then the decisions for their rules
  rights decide           with no ACTION RESOURCE, the decisions for every rule
                          the probes name

  --endpoint E          runtime resolver endpoint (default: the installed
                        runtime)
  --runtime-program P   with --endpoint, the absolute executable the runtime
                        must run as under this account, or the connection is
                        refused (compared as the canonical long path)
  --timeout D           deadline for each call (default 10s)
  --json                one JSON document per result

The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set,
else the installed runtime. To act as a different program, copy this
executable to another path, or build the serve source under the name
openabstractions-probe; that copy runs only probe, and rules name its path.

Exit codes: 0 the call answered, 1 no runtime resolved or transport failure,
2 usage, 3 typed refusal, 4 unavailable. probe all exits 0 once every call
answered or failed with its own result.

Learn more: openabstractions-flat/abstraction-facade/CONTRACT.md
`

// probeUsage lists the probes from the one list the Panel's Explore section
// also shows (abstraction-facade/go/probe).
func probeUsage() string {
	var rows strings.Builder
	table := tabwriter.NewWriter(&rows, 0, 0, 2, ' ', 0)
	for _, p := range probe.List() {
		operation := p.Operation
		if p.Default {
			operation += "*"
		}
		if p.Argument != "" {
			operation += " " + p.Argument
		}
		//unchecked: table's underlying writer is a strings.Builder, which never errors
		fmt.Fprintf(table, "  %s\t%s\t%s\n", p.Capability, operation, p.Note)
	}
	//unchecked: table's underlying writer is a strings.Builder, which never errors
	table.Flush()
	var b strings.Builder
	//unchecked: strings.Builder.WriteString never returns a non-nil error
	b.WriteString(probeUsageHead)
	for _, row := range strings.SplitAfter(rows.String(), "\n") {
		if row != "" {
			//unchecked: strings.Builder.WriteString never returns a non-nil error
			b.WriteString(strings.TrimRight(row, " \n") + "\n")
		}
	}
	//unchecked: strings.Builder.WriteString never returns a non-nil error
	b.WriteString(probeUsageTail)
	return b.String()
}

type probeSubject = probe.Subject

// probeOptions select the runtime and bound each call.
type probeOptions struct {
	endpoint, runtimeProgram string
	timeout                  time.Duration
	asJSON                   bool
}

func (o *probeOptions) bind(flags *flag.FlagSet) {
	flags.StringVar(&o.endpoint, "endpoint", "", "runtime resolver endpoint (default: the installed runtime)")
	flags.StringVar(&o.runtimeProgram, "runtime-program", "", "with --endpoint, the absolute executable the runtime must run as under this account")
	flags.DurationVar(&o.timeout, "timeout", 10*time.Second, "deadline for each call")
	flags.BoolVar(&o.asJSON, "json", false, "emit JSON")
}

// principal is this process's account as the runtime's listener binds it.
func principal() (identity.User, string, error) {
	account, err := user.Current()
	if err != nil {
		return identity.User{}, "", err
	}
	if runtime.GOOS == "windows" {
		return identity.User{Kind: "windows", SID: account.Uid, UID: -1, GID: -1}, account.Uid, nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return identity.User{}, "", err
	}
	return identity.User{Kind: "posix", UID: uid, GID: -1}, account.Uid, nil
}

// machine selects the installed runtime, an explicit endpoint verified as the
// named program, or an explicit endpoint as a deliberately supplied provider.
func (o probeOptions) machine(diagnostics io.Writer, command, usage string) (*client.Machine, error) {
	switch {
	case o.endpoint == "" && o.runtimeProgram != "":
		return nil, flagMistake(diagnostics, command, usage, "--runtime-program verifies an --endpoint; give both")
	case o.endpoint == "":
		return client.Discover(), nil
	case o.runtimeProgram == "":
		return client.New(o.endpoint), nil
	}
	if !filepath.IsAbs(o.runtimeProgram) {
		return nil, flagMistake(diagnostics, command, usage, "--runtime-program must be an absolute executable path")
	}
	who, _, err := principal()
	if err != nil {
		return nil, err
	}
	return client.NewVerified(o.endpoint, listen.ServerExpectation{Principal: who, Program: identity.CanonicalProgramPath(filepath.Clean(o.runtimeProgram))}), nil
}

// probeSelf names this program as a rights subject.
func probeSelf() probeSubject {
	self := probe.Self()
	self.Program = selfProgramPath(self.Program)
	return self
}

var local = client.Requirements{Scope: client.ScopeLocal}

func probeExit(source endpointSource, result probe.Result) error {
	decide := result.Capability == "rights"
	command := fmt.Sprintf("probe %s %s", result.Capability, result.Operation)
	if cause, ok := runtimeMismatchCause(result.Detail); ok {
		return &exitError{exitNotResolved, fmt.Errorf("%s", wrongRuntime(command, cause))}
	}
	switch {
	case result.Resolution != "":
		return probeNotResolved(command, source, result.Detail)
	case result.Outcome == "error":
		return probeNotResolved(command, source, result.Detail)
	case decide && (result.Outcome == "forbidden" || result.Outcome == "invalid" || result.Outcome == "unavailable"):
		return refusal("probe rights decide", result.Outcome, "")
	case probe.Answered(result.Outcome) || decide:
		return nil
	}
	return refusal("probe "+result.Capability+" "+result.Operation, result.Outcome, "")
}

// probeNotResolved is notResolved (jobs.go) for a probe: detail is
// probe.Result's own Detail, a string that already crossed probe.Run's
// return boundary, so the connect failure it names is read from that text
// (connectFailureText) rather than from a live error (isConnectFailure).
func probeNotResolved(command string, source endpointSource, detail string) error {
	if endpoint, ok := connectFailureText(detail); ok {
		return &exitError{exitNotResolved, errors.New(noRuntimeListensAt(command, endpoint, source))}
	}
	return &exitError{exitNotResolved, fmt.Errorf("%s: %s (endpoint: %s)\n%s", command, noRuntimeFirstLine, source, detail)}
}

func printProbe(output io.Writer, asJSON bool, source endpointSource, result probe.Result) error {
	if asJSON {
		return writeJSON(output, result)
	}
	if cause, ok := runtimeMismatchCause(result.Detail); ok {
		_, err := fmt.Fprintf(output, "%s %s (%s) as %s: %s\n%s\n", result.Capability, result.Operation, result.Contract, result.Subject.Program, wrongRuntimeFirstLine, cause)
		return err
	}
	detail := result.Detail
	if endpoint, ok := connectFailureText(detail); ok {
		detail = fmt.Sprintf(`no runtime listens at %s (%s); run "openabstractions status" to list the endpoints that do`, endpoint, source)
	}
	line := fmt.Sprintf("%s %s (%s) as %s: %s", result.Capability, result.Operation, result.Contract, result.Subject.Program, result.Outcome)
	if result.Rule != nil {
		line += fmt.Sprintf("; rule: %s on %s", result.Rule.Action, result.Rule.Resource)
	}
	if detail != "" {
		line += "; " + detail
	}
	if result.Summary != nil {
		if text, err := jsonText(result.Summary); err == nil {
			line += " " + text
		}
	}
	_, err := fmt.Fprintln(output, line)
	return err
}

func jsonText(v any) (string, error) {
	var b strings.Builder
	if err := writeJSON(&b, v); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// probeCommand runs `probe <capability> [action] [argument]`. The probe
// package's own field is still Operation; the command line says action
// (RENAME-PLAN §3, Panel and command line step 9).
func probeCommand(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, probeUsage())
		return err
	}
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// probeUsage already documents every option in its own Options: section;
	// the fallback on a genuine parse error prints it once, not a second time
	// as flag.PrintDefaults' own single-dash listing.
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options probeOptions
	options.bind(flags)
	positional := []string{}
	for {
		if err := flags.Parse(args); err != nil {
			return badFlag(flags, diagnostics, "probe", probeUsage(), args, err)
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) == 0 || isHelp(positional[0]) {
		_, err := io.WriteString(output, probeUsage())
		return err
	}
	if len(positional) > 4 || (len(positional) == 4 && positional[0] != "rights") {
		return flagMistake(diagnostics, "probe", probeUsage(), "unexpected arguments")
	}
	if options.timeout <= 0 {
		return flagMistake(diagnostics, "probe", probeUsage(), "--timeout must be positive")
	}
	capability, operation, argument := positional[0], "", ""
	if len(positional) > 1 {
		operation = positional[1]
	}
	if len(positional) > 2 {
		argument = strings.Join(positional[2:], " ")
	}
	// rightsMachine is the one resolver every runtime-connecting command uses:
	// --endpoint, else ABSTRACTION_RUNTIME_ENDPOINT (connected unverified, with
	// a notice), else the installed runtime, verified. probe honors the
	// variable through it exactly as rights and status do.
	machine, source, err := rightsMachine(options, diagnostics, "probe", probeUsage())
	if err != nil {
		return err
	}
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), options.timeout)
	}
	if capability == "all" {
		if operation != "" {
			return flagMistake(diagnostics, "probe all", probeUsage(), "takes no action")
		}
		for _, p := range probe.All() {
			ctx, cancel := call()
			err := printProbe(output, options.asJSON, source, probe.Run(ctx, machine, p, ""))
			cancel()
			if err != nil {
				return err
			}
		}
		return probeRights(machine, source, call, probe.Decisions(), options.asJSON, output)
	}
	if capability == "rights" && (operation == "" || operation == "decide") && argument == "" {
		return probeRights(machine, source, call, probe.Decisions(), options.asJSON, output)
	}
	p, known, found := probe.Find(capability, operation)
	switch {
	case !known:
		return flagMistake(diagnostics, "probe", probeUsage(), fmt.Sprintf("no capability called %q", capability))
	case !found:
		return flagMistake(diagnostics, "probe "+capability, probeUsage(), fmt.Sprintf("no action called %q", operation))
	case p.Writes && operation != p.Operation:
		return flagMistake(diagnostics, "probe "+p.Capability+" "+p.Operation, probeUsage(), "writes; name its action")
	}
	if err := probe.CheckArgument(p, argument); err != nil {
		return flagMistake(diagnostics, "probe "+capability+" "+operation, probeUsage(), err.Error())
	}
	ctx, cancel := call()
	defer cancel()
	result := probe.Run(ctx, machine, p, argument)
	if err := printProbe(output, options.asJSON, source, result); err != nil {
		return err
	}
	return probeExit(source, result)
}

// probeRights decides each rule for this program through Authorization.Decide.
// With one rule, the command exits by that decision.
func probeRights(machine *client.Machine, source endpointSource, call func() (context.Context, context.CancelFunc), rules []probe.Rule, asJSON bool, output io.Writer) error {
	decide, _, _ := probe.Find("rights", "decide")
	var last error
	for _, rule := range rules {
		ctx, cancel := call()
		result := probe.Run(ctx, machine, decide, rule.Action+" "+rule.Resource)
		cancel()
		if err := printProbe(output, asJSON, source, result); err != nil {
			return err
		}
		if len(rules) == 1 {
			last = probeExit(source, result)
		}
	}
	return last
}

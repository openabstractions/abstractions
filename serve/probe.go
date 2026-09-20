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

const probeUsageHead = `Usage: openabstractions probe <capability> [operation] [argument] [options]
       openabstractions-probe <capability> [operation] [argument] [options]

Calls one capability through the resolved service, as this program, and prints
the typed outcome: the service's own word, a refusal such as forbidden or
not_permitted, or the resolution status such as runtime_unavailable. Each result
names the rights rule that decides the call where the runtime enforces one, so
the same call can be repeated after a grant or a revoke.

Probes read, except the one marked WRITES, which runs only when its capability
and operation are both named and is never part of all.

Capabilities and operations (* marks the default when no operation is named):
`

const probeUsageTail = `  all                     every default operation that needs no argument and
                          writes nothing, then the decisions for their rules
  rights decide           with no ACTION RESOURCE, the decisions for every rule
                          the probes name

Options:
  --endpoint E          runtime resolver endpoint (default: the installed runtime)
  --runtime-program P   with --endpoint, the absolute executable the runtime must
                        run as under this account; the connection is refused otherwise
  --timeout D           waiting budget for each call (default 10s)
  --json                one JSON document per result

To act as a different program, copy this executable to another path, or build
the serve source under the name openabstractions-probe. That copy runs only
probe, and rules name its path.

Exit codes: 0 the call answered, 1 no runtime resolved or transport failure,
2 usage, 3 typed refusal, 4 unavailable. probe all exits 0 once every call
answered or failed with its own result.
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
		fmt.Fprintf(table, "  %s\t%s\t%s\n", p.Capability, operation, p.Note)
	}
	table.Flush()
	var b strings.Builder
	b.WriteString(probeUsageHead)
	for _, row := range strings.SplitAfter(rows.String(), "\n") {
		if row != "" {
			b.WriteString(strings.TrimRight(row, " \n") + "\n")
		}
	}
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
	flags.DurationVar(&o.timeout, "timeout", 10*time.Second, "waiting budget for each call")
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
func (o probeOptions) machine() (*client.Machine, error) {
	switch {
	case o.endpoint == "" && o.runtimeProgram != "":
		return nil, &exitError{exitUsage, errors.New("--runtime-program verifies an --endpoint; give both")}
	case o.endpoint == "":
		return client.Discover(), nil
	case o.runtimeProgram == "":
		return client.New(o.endpoint), nil
	}
	if !filepath.IsAbs(o.runtimeProgram) {
		return nil, &exitError{exitUsage, errors.New("--runtime-program must be an absolute executable path")}
	}
	who, _, err := principal()
	if err != nil {
		return nil, err
	}
	return client.NewVerified(o.endpoint, listen.ServerExpectation{Principal: who, Program: filepath.Clean(o.runtimeProgram)}), nil
}

// probeSelf names this program as a rights subject.
func probeSelf() probeSubject { return probe.Self() }

var local = client.Requirements{Scope: client.ScopeLocal}

func probeExit(result probe.Result) error {
	decide := result.Capability == "rights"
	switch {
	case result.Resolution != "":
		return &exitError{exitNotResolved, fmt.Errorf("probe %s %s: %s", result.Capability, result.Operation, result.Resolution)}
	case result.Outcome == "error":
		return &exitError{exitNotResolved, fmt.Errorf("probe %s %s: %s", result.Capability, result.Operation, result.Detail)}
	case decide && (result.Outcome == "forbidden" || result.Outcome == "invalid" || result.Outcome == "unavailable"):
		return refusal("probe rights decide", result.Outcome, "")
	case probe.Answered(result.Outcome) || decide:
		return nil
	}
	return refusal("probe "+result.Capability+" "+result.Operation, result.Outcome, "")
}

func printProbe(output io.Writer, asJSON bool, result probe.Result) error {
	if asJSON {
		return writeJSON(output, result)
	}
	line := fmt.Sprintf("%s %s (%s) as %s: %s", result.Capability, result.Operation, result.Contract, result.Subject.Program, result.Outcome)
	if result.Rule != nil {
		line += fmt.Sprintf("; rule: %s on %s", result.Rule.Action, result.Rule.Resource)
	}
	if result.Detail != "" {
		line += "; " + result.Detail
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

// probeCommand runs `probe <capability> [operation] [argument]`.
func probeCommand(args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("probe", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		if _, err := fmt.Fprint(diagnostics, probeUsage()); err == nil {
			flags.PrintDefaults()
		}
	}
	var options probeOptions
	options.bind(flags)
	positional := []string{}
	for {
		if err := flags.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return err
			}
			return &exitError{exitUsage, err}
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
		return &exitError{exitUsage, errors.New("probe: unexpected arguments; run openabstractions probe --help")}
	}
	if options.timeout <= 0 {
		return &exitError{exitUsage, errors.New("probe: --timeout must be positive")}
	}
	capability, operation, argument := positional[0], "", ""
	if len(positional) > 1 {
		operation = positional[1]
	}
	if len(positional) > 2 {
		argument = strings.Join(positional[2:], " ")
	}
	machine, err := options.machine()
	if err != nil {
		return err
	}
	call := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), options.timeout)
	}
	if capability == "all" {
		if operation != "" {
			return &exitError{exitUsage, errors.New("probe all takes no operation")}
		}
		for _, p := range probe.All() {
			ctx, cancel := call()
			err := printProbe(output, options.asJSON, probe.Run(ctx, machine, p, ""))
			cancel()
			if err != nil {
				return err
			}
		}
		return probeRights(machine, call, probe.Decisions(), options.asJSON, output)
	}
	if capability == "rights" && (operation == "" || operation == "decide") && argument == "" {
		return probeRights(machine, call, probe.Decisions(), options.asJSON, output)
	}
	p, known, found := probe.Find(capability, operation)
	switch {
	case !known:
		return &exitError{exitUsage, fmt.Errorf("probe: no capability called %q; run openabstractions probe --help", capability)}
	case !found:
		return &exitError{exitUsage, fmt.Errorf("probe %s: no operation called %q; run openabstractions probe --help", capability, operation)}
	case p.Writes && operation != p.Operation:
		return &exitError{exitUsage, fmt.Errorf("probe %s %s writes; name its operation", p.Capability, p.Operation)}
	}
	if err := probe.CheckArgument(p, argument); err != nil {
		return &exitError{exitUsage, err}
	}
	ctx, cancel := call()
	defer cancel()
	result := probe.Run(ctx, machine, p, argument)
	if err := printProbe(output, options.asJSON, result); err != nil {
		return err
	}
	return probeExit(result)
}

// probeRights decides each rule for this program through Authorization.Decide.
// With one rule, the command exits by that decision.
func probeRights(machine *client.Machine, call func() (context.Context, context.CancelFunc), rules []probe.Rule, asJSON bool, output io.Writer) error {
	decide, _, _ := probe.Find("rights", "decide")
	var last error
	for _, rule := range rules {
		ctx, cancel := call()
		result := probe.Run(ctx, machine, decide, rule.Action+" "+rule.Resource)
		cancel()
		if err := printProbe(output, asJSON, result); err != nil {
			return err
		}
		if len(rules) == 1 {
			last = probeExit(result)
		}
	}
	return last
}

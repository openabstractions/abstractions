package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
)

const applicationsUsage = `Usage: openabstractions applications <command>

Commands:
  list                 permission-filtered registered applications and current instances
  activate <name>      request one separately authorized bounded activation

list omits executable paths, start guidance and activation recipes. An instance
lists application-claimed interfaces and contexts; these grant no invocation
authority. activate returns ready only after a descriptor-program-and-session-
bound presence claims the registered readiness interface. It never focuses,
opens a document, terminates or restarts an application.

Every command accepts --endpoint, --timeout and --json. The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime.

Exit codes: 0 listed or ready, 1 runtime not resolved or transport failure,
2 usage, 3 typed refusal (disabled, forbidden, invalid, launch_refused,
identity_refused), 4 unavailable, 6 unknown, 7 not ready.
`

type applicationInterfaceOutput struct {
	Name     string `json:"name"`
	Protocol string `json:"protocol"`
	Contract string `json:"contract,omitempty"`
}

type applicationContextOutput struct {
	Name     string `json:"name"`
	Title    string `json:"title"`
	Revision string `json:"revision"`
}

type applicationInstanceOutput struct {
	Instance      string                       `json:"instance"`
	Interfaces    []applicationInterfaceOutput `json:"interfaces"`
	Contexts      []applicationContextOutput   `json:"contexts"`
	ExpiresUnixMs int64                        `json:"expires_unix_ms"`
}

type applicationOutput struct {
	Name      string                      `json:"name"`
	Title     string                      `json:"title"`
	Scope     string                      `json:"scope"`
	Instances []applicationInstanceOutput `json:"instances"`
}

type applicationListOutput struct {
	Outcome      string              `json:"outcome"`
	Cursor       string              `json:"cursor"`
	Applications []applicationOutput `json:"applications"`
}

type applicationActivationOutput struct {
	Outcome     string `json:"outcome"`
	Application string `json:"application"`
	Instance    string `json:"instance,omitempty"`
	Started     bool   `json:"started"`
	Reason      string `json:"reason,omitempty"`
}

func applicationsCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, applicationsUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "applications: a command is required", "openabstractions applications --help")
	}
	command := "applications " + args[0]
	if args[0] != "list" && args[0] != "activate" {
		return commandMistake(diagnostics, fmt.Sprintf("applications: no command called %q", args[0]), "openabstractions applications --help")
	}
	if containsHelp(args[1:]) {
		_, err := io.WriteString(output, applicationsUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	positional, err := parsePositional(flags, args[1:])
	if err != nil {
		return badFlag(flags, diagnostics, command, applicationsUsage, args[1:], err)
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, applicationsUsage, "--timeout must not be negative")
	}
	if command == "applications list" && len(positional) != 0 {
		return flagMistake(diagnostics, command, applicationsUsage, "takes no arguments")
	}
	// The name's own format is not checked here: an unregistered or
	// mis-typed name is the server's own "unknown" outcome (exit 6), not a
	// usage mistake this command decides client-side.
	if command == "applications activate" && len(positional) != 1 {
		return flagMistake(diagnostics, command, applicationsUsage, "name exactly one application ID")
	}

	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	applications, err := machine.ResolveApplications(call, client.Requirements{Scope: client.ScopeLocal})
	done()
	if err != nil {
		return notResolved(command, source, err)
	}
	transport := func(err error) error { return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)} }

	if command == "applications list" {
		call, done = w.call()
		page, err := applications.Observe(call, "", 0)
		done()
		if err != nil {
			return transport(err)
		}
		if page.Outcome != wire.ApplicationOutcomePage && page.Outcome != wire.ApplicationOutcomeStale {
			return refusal(command, page.Outcome.String(), "")
		}
		projected := projectApplications(page)
		if options.asJSON {
			return writeJSON(output, projected)
		}
		return printApplications(output, projected)
	}

	name := positional[0]
	call, done = w.call()
	result, err := applications.Activate(call, name)
	done()
	if err != nil {
		return transport(err)
	}
	projected := applicationActivationOutput{Outcome: result.Outcome.String(), Application: name, Instance: result.Instance, Started: result.Started, Reason: result.Reason}
	if options.asJSON {
		if err := writeJSON(output, projected); err != nil {
			return err
		}
	}
	if result.Outcome != wire.ApplicationActivationOutcomeReady {
		return refusal(command, result.Outcome.String(), result.Reason)
	}
	if options.asJSON {
		return nil
	}
	verb := "reused"
	if result.Started {
		verb = "started"
	}
	_, err = fmt.Fprintf(output, "%s %s; instance %s\n", verb, name, result.Instance)
	return err
}

func projectApplications(page wire.ApplicationPage) applicationListOutput {
	out := applicationListOutput{Outcome: page.Outcome.String(), Cursor: page.Cursor, Applications: []applicationOutput{}}
	for _, entry := range page.Applications {
		app := applicationOutput{Name: entry.Descriptor.Name, Title: entry.Descriptor.Title, Scope: entry.Scope.String(), Instances: []applicationInstanceOutput{}}
		for _, instance := range entry.Instances {
			item := applicationInstanceOutput{Instance: instance.Instance, Interfaces: []applicationInterfaceOutput{}, Contexts: []applicationContextOutput{}, ExpiresUnixMs: instance.ExpiresUnixMs}
			for _, iface := range instance.Interfaces {
				item.Interfaces = append(item.Interfaces, applicationInterfaceOutput{Name: iface.Name, Protocol: iface.Protocol, Contract: iface.Contract})
			}
			for _, context := range instance.Contexts {
				item.Contexts = append(item.Contexts, applicationContextOutput{Name: context.Name, Title: context.Title, Revision: context.Revision})
			}
			app.Instances = append(app.Instances, item)
		}
		out.Applications = append(out.Applications, app)
	}
	return out
}

func printApplications(output io.Writer, page applicationListOutput) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintln(table, "NAME\tTITLE\tSCOPE\tINSTANCES\tINTERFACES\tCONTEXTS")
	for _, app := range page.Applications {
		interfaces, contexts := []string{}, []string{}
		for _, instance := range app.Instances {
			for _, iface := range instance.Interfaces {
				interfaces = append(interfaces, iface.Name)
			}
			for _, context := range instance.Contexts {
				contexts = append(contexts, context.Title)
			}
		}
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "%s\t%s\t%s\t%d\t%s\t%s\n", app.Name, app.Title, app.Scope, len(app.Instances), strings.Join(interfaces, ","), strings.Join(contexts, ","))
	}
	return table.Flush()
}

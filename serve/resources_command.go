package main

import (
	"flag"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/openabstractions/abstraction-facade/go/client"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
)

const resourcesAskUsage = `Usage: openabstractions resources acquire [options] <resource> <bytes>

Acquires a lease on bytes of a resource for this command's own program, and
prints every holder the service asked to yield, in order, with what each
answered and how long it took.

The service decides abstraction.resource/hold on the resource before it reads
the table, so a program with no rule is refused and nothing is asked. Where
what is free already covers the request, nothing is asked either and the lease
is granted at once. Nothing is killed and no model is loaded.

  --wait DURATION   how long holders have to yield, 0..2m (default 1m)
  --release         release the lease before returning, for a caller that
                    wanted the arbitration and not the bytes
  --json            emit one JSON document on stdout
  --timeout D       deadline for the whole command, never shorter than
                    --wait plus 45s

Every command accepts --endpoint and --timeout. The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime.

resources ask is this command's old name, accepted until the next release.

Exit codes: 0 acquired, 1 runtime not resolved or transport failure, 2 usage,
3 a typed refusal (insufficient, holders_refused, not_permitted, invalid),
4 unavailable.
`

const resourcesAuditUsage = `Usage: openabstractions resources audit [options]

Prints the runtime's record of every ask, yield and refusal: who asked, who
was asked, the resource, the bytes the instrument saw freed, the answer and
the time it took. One JSON document per line, oldest first.

  --state-dir DIR   the runtime's state directory (default: this account's)
  --limit N         the last N records (default 64)

Exit codes: 0 printed, 1 the record could not be read, 2 usage.
`

const resourcesUsage = `Usage: openabstractions resources [options] [resource ...]

Prints who holds each scarce resource on this machine: the program, the
account it runs as, how much, and whether the instrument measured that amount
or its holder claimed it. Naming no resource prints every resource the
machine's table reports.

A verified row is a process the instrument measured, attributed to a program
and an account through the evidence rights uses. A claimed row is a model
server's own word that a model is resident. Its amount is 0 unless the row is
a lease, and then it is the grant the service wrote. What the server says it
holds is in the detail column. held is the sum of the verified rows only, and
capacity is empty when the instrument cannot say, which is the case on an APU
whose card memory is the machine's own memory.

  --fresh    read the instrument again instead of the sample it holds
  --json     emit one JSON document on stdout

A program always sees its own rows. Other programs' rows need a rule for
abstraction.resource/table.read on resource account.

This changes nothing: no model is loaded, unloaded or moved.

Subcommands:
  acquire <resource> <bytes>
                          acquire a lease on bytes of a resource and print
                          who was asked to yield; ask is its old name,
                          accepted until the next release
  audit                   the record of every ask, yield and refusal

Every command accepts --endpoint and --timeout. The endpoint is --endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the installed runtime.

Exit codes: 0 printed, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal, 4 unavailable.
`

type holderOutput struct {
	Program  string `json:"program"`
	Account  string `json:"account,omitempty"`
	Amount   int64  `json:"amount"`
	Evidence string `json:"evidence"`
	Lease    string `json:"lease,omitempty"`
	Grant    string `json:"grant,omitempty"`
	Since    string `json:"since,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type resourceOutput struct {
	Resource   string         `json:"resource"`
	Capacity   int64          `json:"capacity"`
	Held       int64          `json:"held"`
	Observed   string         `json:"observed"`
	Instrument string         `json:"instrument"`
	Holders    []holderOutput `json:"holders"`
}

type resourcesOutput struct {
	Resources []resourceOutput `json:"resources"`
}

func resourcesCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, resourcesUsage)
		return err
	}
	if len(args) > 0 {
		switch args[0] {
		case "acquire", "ask": // ask: acquire's old name, accepted for one release
			return resourcesAskCommand(args[1:], output, diagnostics)
		case "audit":
			return resourcesAuditCommand(args[1:], output, diagnostics)
		}
	}
	if containsHelp(args) {
		_, err := io.WriteString(output, resourcesUsage)
		return err
	}
	const command = "resources"
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	fresh := flags.Bool("fresh", false, "read the instrument again instead of the sample it holds")
	named, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, resourcesUsage, args, err)
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, resourcesUsage, "--timeout must not be negative")
	}

	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	endpoint, _ := resolveEndpoint(options.endpoint)
	call, done := w.call()
	table, err := machine.ResolveResourceTable(call, client.Requirements{Scope: client.ScopeLocal})
	done()
	if err != nil {
		return notResolvedEndpoint(command, endpoint, source, err)
	}
	transport := func(err error) error { return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)} }

	if len(named) == 0 {
		call, done = w.call()
		list, err := table.ResourcesContext(call)
		done()
		if err != nil {
			return transport(err)
		}
		named = list.Resources
	}
	projected := resourcesOutput{Resources: []resourceOutput{}}
	for _, resource := range named {
		call, done = w.call()
		state, err := table.HoldersContext(call, resource, *fresh)
		done()
		if err != nil {
			return transport(err)
		}
		projected.Resources = append(projected.Resources, projectResource(state))
	}
	if options.asJSON {
		return writeJSON(output, projected)
	}
	return printResources(output, projected)
}

func projectResource(state rwire.ResourceState) resourceOutput {
	out := resourceOutput{Resource: state.Resource, Capacity: state.Capacity, Held: state.Held,
		Observed: state.Observed, Instrument: state.Instrument, Holders: []holderOutput{}}
	for _, row := range state.Holders {
		out.Holders = append(out.Holders, holderOutput{Program: row.Program, Account: row.Account, Amount: row.Amount,
			Evidence: row.Evidence.String(), Lease: row.Lease, Grant: row.Grant, Since: row.Since, Detail: row.Detail})
	}
	return out
}

// printResources writes one block per resource: the resource's own line, then
// one line per holder. A resource with no holder says so rather than printing
// an empty table, because "nothing holds the card" is an answer.
func printResources(output io.Writer, printed resourcesOutput) error {
	for i, resource := range printed.Resources {
		if i > 0 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		capacity := "unknown"
		if resource.Capacity > 0 {
			capacity = gigabytes(resource.Capacity)
		}
		if _, err := fmt.Fprintf(output, "%s  held %s of %s  instrument %s  observed %s\n",
			resource.Resource, gigabytes(resource.Held), capacity, resource.Instrument, resource.Observed); err != nil {
			return err
		}
		if len(resource.Holders) == 0 {
			if _, err := fmt.Fprintln(output, "  no holder"); err != nil {
				return err
			}
			continue
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "  PROGRAM\tACCOUNT\tAMOUNT\tEVIDENCE\tSINCE\tDETAIL")
		for _, row := range resource.Holders {
			amount := gigabytes(row.Amount)
			if row.Amount == 0 {
				amount = "-"
			}
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "  %s\t%s\t%s\t%s\t%s\t%s\n", row.Program, dash(row.Account), amount, row.Evidence, dash(row.Since), dash(row.Detail))
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func gigabytes(bytes int64) string {
	return fmt.Sprintf("%.2f GiB", float64(bytes)/(1<<30))
}

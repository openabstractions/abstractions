package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// resourcesAskMargin is what the command allows beyond the wait it asked for:
// resolution, the reply itself, and the instrument read that confirms a yield.
const resourcesAskMargin = 45 * time.Second

// askedOutput is one holder the service asked to yield.
type askedOutput struct {
	Holder string `json:"holder"`
	Amount int64  `json:"amount"`
	Answer string `json:"answer"`
	TookMS int64  `json:"took_ms"`
}

// askOutput is what one acquire did: the typed outcome, the lease if there is
// one, and every holder asked, in order, whatever the outcome.
type askOutput struct {
	Resource string        `json:"resource"`
	Amount   int64         `json:"amount"`
	Outcome  string        `json:"outcome"`
	Lease    string        `json:"lease,omitempty"`
	RenewBy  string        `json:"renew_by,omitempty"`
	Released bool          `json:"released,omitempty"`
	Asked    []askedOutput `json:"asked"`
}

func resourcesAskCommand(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, resourcesAskUsage)
		return err
	}
	const command = "resources acquire"
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	var options serviceOptions
	options.bind(flags)
	wait := flags.Duration("wait", time.Minute, "how long holders have to yield")
	release := flags.Bool("release", false, "release the lease before returning")
	named, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, command, resourcesAskUsage, args, err)
	}
	if len(named) != 2 {
		return flagMistake(diagnostics, command, resourcesAskUsage, "name one resource and the bytes asked for")
	}
	amount, err := strconv.ParseInt(named[1], 10, 64)
	if err != nil || amount < 0 {
		return flagMistake(diagnostics, command, resourcesAskUsage, fmt.Sprintf("bytes must be a whole number of bytes, not %q", named[1]))
	}
	if *wait < 0 || *wait > resourceservice.MaxAcquireWait {
		return flagMistake(diagnostics, command, resourcesAskUsage, "--wait is 0..2m")
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, resourcesAskUsage, "--timeout must not be negative")
	}

	// The whole call has to outlast the wait it asks for: an Acquire waits
	// for holders to yield, and a deadline shorter than the wait would end
	// the arbitration this command started.
	budget := options.budget
	if floor := *wait + resourcesAskMargin; budget < floor {
		budget = floor
	}
	w := newWaiting(budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	endpoint, _ := resolveEndpoint(options.endpoint)
	call, done := w.call()
	leases, err := machine.ResolveResourceLeases(call, client.Requirements{Scope: client.ScopeLocal})
	done()
	if err != nil {
		return notResolvedEndpoint(command, endpoint, source, err)
	}
	call, done = w.call()
	result, err := leases.AcquireContext(call, named[0], amount, *wait)
	done()
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	printed := askOutput{Resource: named[0], Amount: amount, Outcome: result.Outcome.String(), Asked: []askedOutput{}}
	for _, row := range result.Asked {
		printed.Asked = append(printed.Asked, askedOutput{Holder: row.Holder, Amount: row.Amount, Answer: row.Answer, TookMS: row.TookMs})
	}
	if result.Lease != nil {
		printed.Lease, printed.RenewBy = result.Lease.ID, result.Lease.RenewBy
		if *release {
			call, done = w.call()
			change, err := leases.ReleaseContext(call, result.Lease.ID)
			done()
			printed.Released = err == nil && change.Applied
		}
	}
	if options.asJSON {
		if err := writeJSON(output, printed); err != nil {
			return err
		}
	} else if err := printAsk(output, printed); err != nil {
		return err
	}
	if result.Outcome != rwire.AcquireOutcomeAcquired {
		return askRefusal(command, result.Outcome)
	}
	return nil
}

// askRefusal maps an acquire outcome to its exit code, printing the word the
// contract spells.
func askRefusal(command string, outcome rwire.AcquireOutcome) error {
	code := exitRefusedCall
	if outcome == rwire.AcquireOutcomeUnavailable {
		code = exitUnavailable
	}
	return &exitError{code, fmt.Errorf("%s: %s", command, outcome)}
}

func printAsk(output io.Writer, printed askOutput) error {
	line := fmt.Sprintf("%s  %s  %s", printed.Resource, gigabytes(printed.Amount), printed.Outcome)
	if printed.Lease != "" {
		line += "  lease " + printed.Lease + "  renew by " + printed.RenewBy
	}
	if printed.Released {
		line += "  released"
	}
	if _, err := fmt.Fprintln(output, line); err != nil {
		return err
	}
	if len(printed.Asked) == 0 {
		_, err := fmt.Fprintln(output, "  nobody was asked")
		return err
	}
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintln(table, "  HOLDER\tFREED\tANSWER\tTOOK")
	for _, row := range printed.Asked {
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "  %s\t%s\t%s\t%s\n", row.Holder, gigabytes(row.Amount), row.Answer,
			(time.Duration(row.TookMS) * time.Millisecond).String())
	}
	return table.Flush()
}

func resourcesAuditCommand(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, resourcesAuditUsage)
		return err
	}
	const command = "resources audit"
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	state := flags.String("state-dir", "", "the runtime's state directory")
	limit := flags.Int("limit", 64, "the last N records")
	if err := flags.Parse(args); err != nil {
		return badFlag(flags, diagnostics, command, resourcesAuditUsage, args, err)
	}
	if flags.NArg() != 0 {
		return flagMistake(diagnostics, command, resourcesAuditUsage, "takes no arguments")
	}
	if *state == "" {
		resolved, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
		if err != nil {
			return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
		}
		*state = resolved
	}
	if !filepath.IsAbs(*state) {
		return flagMistake(diagnostics, command, resourcesAuditUsage, "--state-dir must be absolute")
	}
	lines, err := resourceservice.ReadAudit(filepath.Join(*state, "resources", leaseAuditFile), *limit)
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)}
	}
	if len(lines) == 0 {
		_, err := fmt.Fprintln(output, "nothing has been asked")
		return err
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	return nil
}

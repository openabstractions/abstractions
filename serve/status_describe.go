package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
)

// notAnEndpoint is what status describe says when its argument is neither a
// platform endpoint path nor a short provider-endpoint name: naming the two
// forms it accepts, with an example in each, and the command that lists this
// runtime's own.
const notAnEndpoint = `takes an endpoint, for example \\.\pipe\openabstractions-...-runtime-v1 (Windows) or /run/user/<uid>/openabstractions/runtime-v1.sock; run "openabstractions status" to see this runtime's endpoints.`

// looksLikeEndpointPath reports whether s has the shape of a platform
// endpoint path: a Windows named pipe, or an absolute path elsewhere. It
// does not check that anything answers there.
func looksLikeEndpointPath(s string) bool {
	if len(s) >= 9 && strings.EqualFold(s[:9], `\\.\pipe\`) {
		return true
	}
	if runtime.GOOS == "windows" {
		return false
	}
	return strings.HasPrefix(s, "/")
}

const statusDescribeUsage = `Usage: openabstractions status describe <endpoint> [--timeout D] [--json]

Calls abstraction.facade/endpoint@1 Describe on one local endpoint and prints
its Description: the program and version the provider gives itself, and each
service it hosts with its readiness. <endpoint> is a platform endpoint path
(\\.\pipe\... on Windows, a socket path elsewhere) or a local endpoint name,
such as a provider declaration's endpoint. The program name is the provider's
own claim and grants nothing.

Exit codes: 0 described, 1 the endpoint did not answer, 2 usage, 3 a typed
refusal (forbidden, invalid, unknown_service), 4 unavailable.
`

// statusDescribe prints one endpoint's endpoint@1 Description.
func statusDescribe(args []string, output, diagnostics io.Writer) error {
	if containsHelp(args) {
		_, err := io.WriteString(output, statusDescribeUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "status describe: an endpoint is required", "openabstractions status describe --help")
	}
	flags := flag.NewFlagSet("status describe", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	budget := flags.Duration("timeout", 5*time.Second, "time allowed for the call")
	asJSON := flags.Bool("json", false, "emit the Description as JSON")
	positional, err := parsePositional(flags, args)
	if err != nil {
		return badFlag(flags, diagnostics, "status describe", statusDescribeUsage, args, err)
	}
	if len(positional) != 1 {
		return flagMistake(diagnostics, "status describe", statusDescribeUsage, "name exactly one endpoint")
	}
	if *budget <= 0 {
		value := budget.String()
		if typed, ok := flagValueAsTyped(args, "timeout"); ok {
			value = typed
		}
		return flagMistake(diagnostics, "status describe", statusDescribeUsage, fmt.Sprintf("--timeout %q is not a positive duration (examples: 5s, 1m30s, 500ms)", value))
	}
	endpoint := positional[0]
	if providerEndpoint.MatchString(endpoint) {
		endpoint = listen.Endpoint(endpoint)
	} else if !looksLikeEndpointPath(endpoint) {
		return flagMistake(diagnostics, "status describe", statusDescribeUsage, notAnEndpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	description, err := client.New("").DescribeEndpoint(ctx, endpoint)
	var refusal *wire.ServiceError
	if errors.As(err, &refusal) {
		return refusalCode("status describe", string(refusal.Code))
	}
	if err != nil {
		if isConnectFailure(err) {
			return &exitError{exitNotResolved, fmt.Errorf(`status describe: no runtime listens at %s; run "openabstractions status" to list the endpoints that do`, endpoint)}
		}
		return &exitError{exitNotResolved, fmt.Errorf("status describe %s: %w", endpoint, err)}
	}
	if *asJSON {
		if err := writeJSON(output, description); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(output, "endpoint: %s\noutcome: %s\nprogram: %s\nversion: %s\n", endpoint, description.Outcome, description.Program, description.Version); err != nil {
			return err
		}
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintln(table, "CONTRACT\tREADINESS\tGUARANTEES\tCAPABILITIES")
		for _, s := range description.Services {
			readiness := s.Readiness.String()
			if s.Why != "" {
				readiness += ": " + s.Why
			}
			var capabilities []string
			for k, v := range s.Capabilities {
				capabilities = append(capabilities, k+"="+v)
			}
			//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
			fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", s.Contract, readiness, strings.Join(s.Guarantees, ","), strings.Join(capabilities, ","))
		}
		if err := table.Flush(); err != nil {
			return err
		}
	}
	if description.Outcome != wire.DescriptionOutcomeDescribed {
		return refusalCode("status describe", description.Outcome.String())
	}
	return nil
}

// refusalCode is the exit of a typed refusal word.
func refusalCode(command, word string) error {
	if word == "unavailable" {
		return &exitError{exitUnavailable, fmt.Errorf("%s: %s", command, word)}
	}
	return &exitError{exitRefusedCall, fmt.Errorf("%s: %s", command, word)}
}

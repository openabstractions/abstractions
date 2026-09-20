package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
)

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
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, statusDescribeUsage)
		return err
	}
	flags := flag.NewFlagSet("status describe", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	budget := flags.Duration("timeout", 5*time.Second, "time allowed for the call")
	asJSON := flags.Bool("json", false, "emit the Description as JSON")
	positional, err := parsePositional(flags, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, fmt.Errorf("status describe: %w", err)}
	}
	if len(positional) != 1 || *budget <= 0 {
		return &exitError{exitUsage, errors.New("status describe: name exactly one endpoint and a positive timeout")}
	}
	endpoint := positional[0]
	if providerEndpoint.MatchString(endpoint) {
		endpoint = listen.Endpoint(endpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	description, err := client.New("").DescribeEndpoint(ctx, endpoint)
	var refusal *wire.ServiceError
	if errors.As(err, &refusal) {
		return refusalCode("status describe", string(refusal.Code))
	}
	if err != nil {
		return &exitError{exitNotResolved, fmt.Errorf("status describe %s: %w", endpoint, err)}
	}
	if *asJSON {
		if err := writeJSON(output, description); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(output, "endpoint: %s\noutcome: %s\nprogram: %s\nversion: %s\n", endpoint, description.Outcome, description.Program, description.Version)
		table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
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

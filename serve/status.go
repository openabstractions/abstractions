package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
)

type capabilityStatus struct {
	Capability string `json:"capability"`
	Status     string `json:"status"`
}
type runtimeReport struct {
	Capabilities []capabilityStatus `json:"capabilities"`
	Error        string             `json:"error,omitempty"`
}

// Status observes the resolver's current registrations using the caller's
// authority. It performs no activation and sends no provider mutations.
func runtimeStatus(args []string, output, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	endpoint := flags.String("endpoint", "", "runtime bootstrap endpoint (default: current user)")
	budget := flags.Duration("timeout", 5*time.Second, "total time allowed for readiness queries")
	asJSON := flags.Bool("json", false, "emit capability statuses as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *budget <= 0 {
		return errors.New("status: supply valid flags and a positive timeout")
	}
	report := runtimeReport{Capabilities: []capabilityStatus{}}
	var failure error
	if *endpoint == "" {
		*endpoint, failure = resolution.CheckedDefaultEndpoint()
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	if failure == nil {
		client := resolution.NewClient(*endpoint, *budget)
		for _, item := range []struct{ capability, contract string }{
			{"abstraction.logging", "abstraction.logging/sink@1"},
			{"abstraction.config", "abstraction.config/reader@1"},
		} {
			result, err := client.Resolve(ctx, wire.ResolveRequest{Capability: item.capability, Contracts: []string{item.contract}, Scope: wire.ScopeLocal})
			if err != nil {
				failure = err
				break
			}
			report.Capabilities = append(report.Capabilities, capabilityStatus{item.capability, result.Status})
		}
	}
	if failure != nil {
		report.Error = failure.Error()
	}
	if *asJSON {
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
	} else {
		for _, item := range report.Capabilities {
			if _, err := fmt.Fprintf(output, "%s: %s\n", item.Capability, item.Status); err != nil {
				return err
			}
		}
	}
	if failure != nil {
		return failure
	}
	for _, item := range report.Capabilities {
		if item.Status != wire.ResolutionStatusResolved {
			return errors.New("runtime capabilities are unavailable or refused")
		}
	}
	return nil
}

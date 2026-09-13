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
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/resolution"
)

type capabilityStatus struct {
	Capability string          `json:"capability"`
	Contract   string          `json:"contract"`
	Status     string          `json:"status"`
	Result     json.RawMessage `json:"result,omitempty"`
}
type bootstrapStatus struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
}
type runtimeReport struct {
	Bootstrap    bootstrapStatus    `json:"bootstrap"`
	Capabilities []capabilityStatus `json:"capabilities"`
	Error        string             `json:"error,omitempty"`
}

// Start and status require the same installed runtime contracts.
func runtimeContracts() [][2]string {
	var result [][2]string
	for _, request := range client.DefaultStatusRequests() {
		result = append(result, [2]string{request.Capability, request.Contracts[0]})
	}
	return result
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
	explicitEndpoint := *endpoint != ""
	if *endpoint == "" {
		*endpoint, failure = resolution.CheckedDefaultEndpoint()
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	evidence := wire.BootstrapObservation{State: "unknown", Detail: "explicit endpoint; installed runtime lifecycle was not queried"}
	if !explicitEndpoint {
		evidence = bootstrap.ObserveInstalled(ctx)
	}
	report.Bootstrap = bootstrapStatus{State: evidence.State, Detail: evidence.Detail}
	if failure == nil {
		observation, err := client.New(*endpoint).Observe(ctx, client.DefaultStatusRequests(), evidence)
		failure = err
		for _, item := range observation.Capabilities {
			entry := capabilityStatus{Capability: item.Request.Capability, Contract: item.Request.Contracts[0]}
			if item.Result != nil {
				entry.Status = item.Result.Status
				entry.Result = wire.Encode(item.Result)
			}
			report.Capabilities = append(report.Capabilities, entry)
		}
	}
	if failure != nil {
		report.Error = failure.Error()
		if len(report.Capabilities) == 0 {
			for _, request := range client.DefaultStatusRequests() {
				report.Capabilities = append(report.Capabilities, capabilityStatus{Capability: request.Capability, Contract: request.Contracts[0]})
			}
		}
	}
	if *asJSON {
		if err := json.NewEncoder(output).Encode(report); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintf(output, "runtime supervision: %s (%s)\n", report.Bootstrap.State, report.Bootstrap.Detail); err != nil {
			return err
		}
		for _, item := range report.Capabilities {
			if _, err := fmt.Fprintf(output, "%s (%s): %s\n", item.Capability, item.Contract, item.Status); err != nil {
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

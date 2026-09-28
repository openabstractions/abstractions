package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/resolution"
)

const statusUsage = `Usage: openabstractions status [options]
       openabstractions status describe <endpoint>

Queries the resolved runtime's current capability readiness and changes no
registration or provider state. Prints this program's own identity path,
canonicalized the way the runtime compares it against a rights rule's
--program.

Options:
  --endpoint E   runtime bootstrap endpoint (default: current user)
  --timeout D    total time allowed for readiness queries (default 5s)
  --json         emit capability statuses as JSON

Runtime selection and trust, the same rule rights uses: with neither
--endpoint nor ABSTRACTION_RUNTIME_ENDPOINT, this verifies the installed
runtime's own registration (macOS: the installed XPC endpoint). The variable
alone selects an endpoint but names no server identity. It does not verify
the installed runtime's registration against a different endpoint; this
connects to the named endpoint unverified and says so once, on stderr.
--endpoint also connects unverified; status accepts no flag to supply an
explicit server expectation for it.

Exit codes: 0 every capability resolved, 1 no runtime answered or a
capability is unavailable or refused, 2 usage. Run
"openabstractions status describe --help" for its own exit codes.
`

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
	Bootstrap bootstrapStatus `json:"bootstrap"`
	// Profile is this caller's view of the profile folders: "real",
	// "virtualized(<package family>)", or "unknown: <reason>".
	Profile string `json:"profile"`
	// Program is this program's own identity path, canonicalized
	// (identity.CanonicalProgramPath) the same way the runtime's own peer
	// identity derives it for the same connection (selfProgramPath):
	// the path a rights rule names in --program to cover this program.
	Program      string             `json:"program"`
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
// authority and changes no registration or provider state. On macOS, connecting
// to the installed XPC endpoint may let launchd activate its registered job.
func runtimeStatus(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && args[0] == "describe" {
		return statusDescribe(args[1:], output, diagnostics)
	}
	if containsHelp(args) {
		_, err := io.WriteString(output, statusUsage)
		return err
	}
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	//unchecked: usage text on a help/error path; a failed write to diagnostics has nothing further to report
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	endpoint := flags.String("endpoint", "", "runtime bootstrap endpoint (default: current user)")
	budget := flags.Duration("timeout", 5*time.Second, "total time allowed for readiness queries")
	asJSON := flags.Bool("json", false, "emit capability statuses as JSON")
	if err := flags.Parse(args); err != nil {
		return badFlag(flags, diagnostics, "status", statusUsage, args, err)
	}
	if flags.NArg() != 0 {
		return flagMistake(diagnostics, "status", statusUsage, fmt.Sprintf("unexpected argument %q", flags.Arg(0)))
	}
	if *budget <= 0 {
		value := budget.String()
		if typed, ok := flagValueAsTyped(args, "timeout"); ok {
			value = typed
		}
		return flagMistake(diagnostics, "status", statusUsage, fmt.Sprintf("--timeout %q is not a positive duration (examples: 5s, 1m30s, 500ms)", value))
	}
	report := runtimeReport{Capabilities: []capabilityStatus{}, Program: probeSelf().Program}
	var failure error
	explicitEndpoint := *endpoint != ""
	// envEndpoint is set only when runtimeEndpointVar, not --endpoint, will
	// select the endpoint below (darwin's installed XPC lookup does not
	// consult it). rightsMachine in rights.go applies the same resolveEndpoint
	// rule; status computes it by hand because it also needs explicitEndpoint
	// and envEndpoint separately, to decide whether to query the installed
	// runtime's own bootstrap lifecycle below.
	var envEndpoint string
	source := endpointInstalled
	if explicitEndpoint {
		source = endpointExplicit
	} else if runtime.GOOS != "darwin" {
		if envEndpoint = os.Getenv(runtimeEndpointVar); envEndpoint != "" {
			source = endpointFromVar
		}
	}
	if *endpoint == "" {
		if runtime.GOOS == "darwin" {
			*endpoint, failure = bootstrap.InstalledEndpoint("runtime-v1")
		} else {
			*endpoint, failure = resolution.CheckedDefaultEndpoint()
		}
	}
	if envEndpoint != "" {
		warnUnverifiedEndpoint(diagnostics, "status", envEndpoint)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *budget)
	defer cancel()
	evidence := wire.BootstrapObservation{State: wire.BootstrapStateUnknown, Detail: "explicit endpoint; installed runtime lifecycle was not queried"}
	if !explicitEndpoint && envEndpoint == "" {
		evidence = bootstrap.ObserveInstalled(ctx)
	} else if envEndpoint != "" {
		evidence = wire.BootstrapObservation{State: wire.BootstrapStateUnknown, Detail: runtimeEndpointVar + " names a different endpoint; installed runtime lifecycle was not queried"}
	}
	report.Bootstrap = bootstrapStatus{State: evidence.State.String(), Detail: evidence.Detail}
	view, profileErr := bootstrap.CurrentProfileView()
	if profileErr != nil {
		report.Profile = "unknown: " + profileErr.Error()
	} else {
		report.Profile = view.String()
	}
	if failure == nil {
		machine := client.New(*endpoint)
		if !explicitEndpoint && runtime.GOOS == "darwin" {
			machine = client.Discover()
		}
		observation, err := machine.Observe(ctx, client.DefaultStatusRequests(), evidence)
		failure = err
		for _, item := range observation.Capabilities {
			entry := capabilityStatus{Capability: item.Request.Capability, Contract: item.Request.Contracts[0]}
			if item.Result != nil {
				entry.Status = item.Result.Status.String()
				entry.Result = wire.Encode(item.Result)
			}
			report.Capabilities = append(report.Capabilities, entry)
		}
	}
	if failure != nil {
		failure = statusFailure("status", source, failure)
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
		if _, err := fmt.Fprintf(output, "profile: %s\n", profileWords(view, profileErr)); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "this program: %s\n", report.Program); err != nil {
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
		if item.Status != wire.ResolutionStatusResolved.String() {
			return errors.New("runtime capabilities are unavailable or refused")
		}
	}
	return nil
}

// profileWords is the profile line in words, for a person reading plain
// text. report.Profile, read by --json, keeps the compact documented form:
// "real", "virtualized(<package family>)" or "unknown: <reason>".
func profileWords(view bootstrap.ProfileView, err error) string {
	if err != nil {
		return "this process's profile view could not be read: " + err.Error()
	}
	if view.Virtualized {
		return packagedAppPhrase(view.Family)
	}
	return "this shell sees this account's own AppData"
}

// statusFailure gives status's own connection failure the same two sentences
// every command uses: the runtime reached is not the one expected, or no
// runtime answered at all. source names which of the three rules
// resolveEndpoint applies chose the endpoint status tried.
func statusFailure(command string, source endpointSource, err error) error {
	if cause, ok := runtimeMismatchCause(err.Error()); ok {
		return errors.New(wrongRuntime(command, cause))
	}
	return fmt.Errorf("%s: %s (endpoint: %s)\n%s", command, noRuntimeFirstLine, source, err)
}

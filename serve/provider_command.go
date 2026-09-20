package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
)

const providerUsage = `Usage: openabstractions provider <command>

Declares providers outside the runtime in its registry,
abstraction.facade/registry@1. The runtime launches an on-demand provider when
a resolution first needs it, restarts it with backoff, and offers its contracts
to applications after the runtime's own services while the process at its
endpoint runs the declared program and endpoint@1 Describe lists every declared
contract ready. A remote runtime is reached over mutual TLS and its hosts are
listed as NAME/HOST.

Commands:
  provider list          every declaration, its readiness, described contracts
                         and accepted resources
  provider add <name> --program PATH --provider-endpoint NAME --contract CONTRACT
                         [--arg ARG]... [--activate on-demand|attach]
                         [--guarantee NAME]... [--model NAME]... [--profiles LIST] [--store NAME]...
                         declare a provider at the listed revision
  provider add <name> --remote HOST:PORT --server-name NAME --trust FILE
                         --client FILE --client-key FILE [--credential NAME]
                         [--profiles LIST]
                         declare another runtime at the listed revision
  provider remove <name> withdraw a declaration; an on-demand child ends

provider add:
  --program PATH       the absolute executable path the runtime launches and
                       requires of the process serving the endpoint; a program
                       cannot declare itself
  --provider-endpoint NAME
                       the local endpoint name the provider listens on
                       (a-z, 0-9, _ . -)
  --contract CONTRACT  a contract it serves, repeatable: any generated service's
                       wire name, such as abstraction.inference/chat@1 or
                       abstraction.storage/inventory-source@1
  --arg ARG            one program argument, repeatable, in order; {endpoint}
                       is replaced by the endpoint name
  --activate MODE      on-demand (default) launches the program when needed;
                       attach reads a provider something else started
  --guarantee NAME     a guarantee its candidates advertise, repeatable
  --model NAME         a model a native inference provider serves, repeatable;
	                   kept as its explicit mediation allowlist
  --profiles LIST      comma-separated profiles it serves, kept as resources
                       profile:NAME
  --store NAME         for an inventory source, a store the runtime accepts
                       from it, repeatable and required; kept as the resource
                       store:NAME, and writes the rule
                       abstraction.storage/inventory.provide on store:NAME for
                       the program
  --remote HOST:PORT   declares another runtime (oa-remote@1) reached over mutual
                       TLS; it serves abstraction.inference/chat@1 and
                       abstraction.router/router@1, and the runtime's operator
                       programs receive abstraction.inference/complete on
                       host:NAME
  --server-name NAME   the name the remote runtime's certificate carries
  --trust FILE         PEM roots trusted for the remote runtime
  --client FILE        this runtime's PEM client certificate
  --client-key FILE    this runtime's PEM client private key
  --credential NAME    the credential the remote runtime holds and applies

Every command accepts --endpoint, --timeout and --json. Declaring needs
abstraction.facade/provider.manage, which the command line and the Panel hold
by installation.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict, invalid), 4 unavailable, 6 unknown.
`

func providerCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 0 || isHelp(args[0]) {
		_, err := io.WriteString(output, providerUsage)
		return err
	}
	command := "provider " + args[0]
	switch args[0] {
	case "list", "add", "remove":
	default:
		return &exitError{exitUsage, fmt.Errorf("provider: no command called %q; run openabstractions provider --help", args[0])}
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		if _, err := fmt.Fprint(diagnostics, providerUsage); err == nil {
			flags.PrintDefaults()
		}
	}
	var options serviceOptions
	options.bind(flags)
	program := flags.String("program", "", "program path")
	endpoint := flags.String("provider-endpoint", "", "provider endpoint name")
	activate := flags.String("activate", "on-demand", "on-demand or attach")
	profiles := flags.String("profiles", "", "comma-separated profiles")
	remoteAt := flags.String("remote", "", "remote runtime host:port")
	serverName := flags.String("server-name", "", "remote runtime certificate name")
	trustRoots := flags.String("trust", "", "PEM roots trusted for the remote runtime")
	clientCert := flags.String("client", "", "PEM client certificate")
	clientKey := flags.String("client-key", "", "PEM client private key")
	credential := flags.String("credential", "", "credential the remote runtime applies")
	var contracts, arguments, guarantees, models, stores repeated
	flags.Var(&contracts, "contract", "contract served")
	flags.Var(&arguments, "arg", "program argument")
	flags.Var(&guarantees, "guarantee", "advertised guarantee")
	flags.Var(&models, "model", "served inference model")
	flags.Var(&stores, "store", "accepted inventory store")
	positional, err := parsePositional(flags, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return &exitError{exitUsage, fmt.Errorf("%s: %w", command, err)}
	}
	wantsName := command != "provider list"
	if wantsName != (len(positional) == 1) || len(positional) > 1 {
		if wantsName {
			return &exitError{exitUsage, fmt.Errorf("%s: name exactly one provider", command)}
		}
		return &exitError{exitUsage, fmt.Errorf("%s: takes no arguments", command)}
	}
	activation := wire.ActivationOnDemand
	if command == "provider add" {
		switch {
		case *remoteAt != "":
			if *program != "" || *endpoint != "" || len(contracts) > 0 || len(arguments) > 0 || len(models) > 0 || len(stores) > 0 {
				return &exitError{exitUsage, fmt.Errorf("%s: --remote takes no --program, --provider-endpoint, --contract, --arg or --store", command)}
			}
			activation = wire.ActivationRemote
		case *activate == "on-demand":
		case *activate == "attach":
			activation = wire.ActivationAttach
		default:
			return &exitError{exitUsage, fmt.Errorf("%s: --activate is on-demand or attach", command)}
		}
		if *remoteAt == "" && (*program == "" || !filepath.IsAbs(*program) || *endpoint == "" || len(contracts) == 0) {
			return &exitError{exitUsage, fmt.Errorf("%s: --program (absolute), --provider-endpoint and --contract are required", command)}
		}
	}
	if options.budget < 0 {
		return &exitError{exitUsage, fmt.Errorf("%s: --timeout must not be negative", command)}
	}
	w := newWaiting(options.budget)
	defer w.stop()
	call, done := w.call()
	registry, err := options.machine().ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, err)
	}
	transport := func(err error) error { return &exitError{exitNotResolved, fmt.Errorf("%s: %w", command, err)} }
	call, done = w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		return transport(err)
	}
	if list.Outcome != wire.DeclarationListOutcomePage {
		return refusal(command, list.Outcome.String(), "")
	}
	if command == "provider list" {
		if options.asJSON {
			return writeJSON(output, list)
		}
		return printDeclarations(output, list)
	}
	name := positional[0]
	call, done = w.call()
	var change wire.DeclarationChange
	if command == "provider add" {
		declaration := wire.Declaration{Name: name, Program: *program, Arguments: append([]string{}, arguments...), Endpoint: *endpoint,
			Transport: wire.DeclarationTransportNative, Contracts: contracts, Guarantees: guarantees, Activation: activation}
		if *program != "" {
			declaration.Program = filepath.Clean(*program)
		}
		for _, profile := range splitList(*profiles) {
			declaration.Resources = append(declaration.Resources, "profile:"+profile)
		}
		declaration.Models = append([]string{}, models...)
		for _, store := range stores {
			declaration.Resources = append(declaration.Resources, ResourceStore(store))
		}
		if *remoteAt != "" {
			declaration.Transport, declaration.Endpoint, declaration.Contracts = wire.DeclarationTransportRemote, "tls://"+*remoteAt, append([]string{}, remoteContracts...)
			declaration.Remote = &wire.RemoteTrust{ServerName: *serverName, Roots: absolute(*trustRoots), Certificate: absolute(*clientCert), Key: absolute(*clientKey), Credential: *credential}
		}
		change, err = registry.Declare(call, list.Revision, declaration)
	} else {
		change, err = registry.Withdraw(call, list.Revision, name)
	}
	done()
	if err != nil {
		return transport(err)
	}
	if change.Outcome != wire.DeclarationEditOutcomeApplied {
		return refusal(command, change.Outcome.String(), change.Reason)
	}
	return credentialReport(output, options.asJSON, change, fmt.Sprintf("%s %s; revision %s", strings.TrimPrefix(command, "provider "), name, change.Revision))
}

// printDeclarations writes the registry listing as a table.
func printDeclarations(output io.Writer, list wire.DeclarationList) error {
	table := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tACTIVATION\tCONTRACTS\tMODELS\tENDPOINT\tPROGRAM\tREADINESS\tDESCRIBED\tRESTARTS\tRESOURCES\tACCEPTED\tDECLARED BY\tDECLARED")
	for _, p := range list.Declarations {
		d := p.Declaration
		readiness := p.Readiness.String()
		if p.Why != "" {
			readiness += ": " + p.Why
		}
		var described []string
		for _, s := range p.Described {
			described = append(described, s.Contract+"="+s.Readiness.String())
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", d.Name, d.Activation, strings.Join(d.Contracts, ","), strings.Join(d.Models, ","), d.Endpoint, d.Program,
			readiness, strings.Join(described, ","), p.Restarts, strings.Join(d.Resources, ","), strings.Join(p.Accepted, ","), p.DeclaredBy,
			time.UnixMilli(p.DeclaredUnixMs).UTC().Format(time.RFC3339))
	}
	fmt.Fprintf(table, "revision: %s\n", list.Revision)
	return table.Flush()
}

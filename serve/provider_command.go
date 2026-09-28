package main

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
)

const providerUsage = `Usage: openabstractions provider <command>

Reads and writes the runtime's registry, abstraction.facade/registry@1: the
one directory of the programs it knows, in three roles.

  provider  serves OA contracts on an OA endpoint. The runtime launches an
            on-demand one when a resolution first needs it, restarts it with
            backoff, and offers its contracts to applications after the
            runtime's own services while the process at its endpoint runs the
            declared program and endpoint@1 Describe lists every declared
            contract ready.
  host      a foreign HTTP engine the router reaches at a base URL, with its
            wire kind, credential, profiles and ceiling. Add and remove one
            with "openabstractions inference server", which writes the same
            declaration.
  remote    another runtime over mutual TLS, whose hosts are listed as
            NAME/HOST.

A declaration is declared by the operator program that wrote it, by the
product whose own record names its address, or by the installation, which
ships declaration files in the directory "declarations" beside the installed
openabstractions executable. A shipped declaration may name its program as a
bare file name with no path separator; the runtime resolves it against the
tools directory "declarations" sits beside, the directory holding the
runtime's own operator programs, and that resolved absolute path is what it
launches and names to its rights rules. A bare name resolving to no file is
reported invalid, naming the shipped name and the resolved path. A
declaration added with "provider add" always names its program by an
absolute path. Withdrawing a declaration of a product or the installation
disables it by name, so a reinstall does not bring it back; declaring that
name again enables it.

Commands:
  provider list          every declaration, its role, readiness, described
                         contracts and accepted resources
  provider add <name> --program PATH --provider-endpoint NAME --contract CONTRACT
                         [--arg ARG]... [--activate on-demand|attach]
                         [--guarantee NAME]... [--model NAME]... [--profiles LIST] [--store NAME]...
                         declare a provider at the listed revision
  provider add <name> --remote HOST:PORT --server-name NAME --trust FILE
                         --client FILE --client-key FILE [--credential NAME]
                         [--profiles LIST]
                         declare another runtime at the listed revision
  provider remove <name> withdraw a declaration; an on-demand child ends. A
                         declaration of a product or the installation is
                         disabled by name instead of removed

provider add:
  --program PATH       the absolute executable path the runtime launches and
                       requires of the process serving the endpoint; a program
                       cannot declare itself. Stored and compared as the
                       canonical long path: a short DOS 8.3 launch alias
                       names the same program as its long spelling
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

Every command accepts --endpoint, --timeout and --json. The endpoint is
--endpoint if given, else ABSTRACTION_RUNTIME_ENDPOINT if set, else the
installed runtime. Declaring needs abstraction.facade/provider.manage, which
the command line and the Panel hold by installation.

Exit codes: 0 done, 1 runtime not resolved or transport failure, 2 usage,
3 typed refusal (forbidden, conflict, invalid), 4 unavailable, 6 unknown.
`

func providerCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(output, providerUsage)
		return err
	}
	if len(args) == 0 {
		return commandMistake(diagnostics, "provider: a command is required", "openabstractions provider --help")
	}
	command := "provider " + args[0]
	switch args[0] {
	case "list", "add", "remove":
	default:
		return commandMistake(diagnostics, fmt.Sprintf("provider: no command called %q", args[0]), "openabstractions provider --help")
	}
	if containsHelp(args[1:]) {
		_, err := io.WriteString(output, providerUsage)
		return err
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
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
		return badFlag(flags, diagnostics, command, providerUsage, args[1:], err)
	}
	wantsName := command != "provider list"
	if wantsName != (len(positional) == 1) || len(positional) > 1 {
		if wantsName {
			return flagMistake(diagnostics, command, providerUsage, "name exactly one provider")
		}
		return flagMistake(diagnostics, command, providerUsage, "takes no arguments")
	}
	activation := wire.ActivationOnDemand
	if command == "provider add" {
		switch {
		case *remoteAt != "":
			if *program != "" || *endpoint != "" || len(contracts) > 0 || len(arguments) > 0 || len(models) > 0 || len(stores) > 0 {
				return flagMistake(diagnostics, command, providerUsage, "--remote takes no --program, --provider-endpoint, --contract, --arg or --store")
			}
			activation = wire.ActivationRemote
		case *activate == "on-demand":
		case *activate == "attach":
			activation = wire.ActivationAttach
		default:
			return flagMistake(diagnostics, command, providerUsage, "--activate is on-demand or attach")
		}
		if *remoteAt == "" && (*program == "" || !filepath.IsAbs(*program) || *endpoint == "" || len(contracts) == 0) {
			return flagMistake(diagnostics, command, providerUsage, "--program (absolute), --provider-endpoint and --contract are required")
		}
	}
	if options.budget < 0 {
		return flagMistake(diagnostics, command, providerUsage, "--timeout must not be negative")
	}
	w := newWaiting(options.budget)
	defer w.stop()
	machine, source := options.machine(diagnostics, command)
	call, done := w.call()
	registry, err := machine.ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		return notResolved(command, source, err)
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
			declaration.Program = identity.CanonicalProgramPath(filepath.Clean(*program))
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
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintln(table, "NAME\tROLE\tACTIVATION\tCONTRACTS\tMODELS\tENDPOINT\tPROGRAM\tREADINESS\tDESCRIBED\tRESTARTS\tRESOURCES\tACCEPTED\tDECLARED BY\tDECLARED")
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
		// A host declares no OA endpoint or program: its engine stands there.
		endpoint, program := d.Endpoint, d.Program
		if h := d.Host; h != nil {
			endpoint, program = h.Base, h.Kind
			if h.Credential != "" {
				program += " " + h.Credential
			}
		}
		//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n", d.Name, p.Role, d.Activation, strings.Join(d.Contracts, ","), strings.Join(d.Models, ","), endpoint, program,
			readiness, strings.Join(described, ","), p.Restarts, strings.Join(d.Resources, ","), strings.Join(p.Accepted, ","), p.DeclaredBy,
			time.UnixMilli(p.DeclaredUnixMs).UTC().Format(time.RFC3339))
	}
	//unchecked: table buffers in memory; the write to the underlying output surfaces at Flush, which is checked below
	fmt.Fprintf(table, "revision: %s\n", list.Revision)
	return table.Flush()
}

// openabstractions is the common host command. Runtime composition and platform
// activation are independent of the capability API; legacy single-capability
// commands remain available during migration.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	config "github.com/openabstractions/abstraction-config/go/service"
	logging "github.com/openabstractions/abstraction-logging/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"

	identity "github.com/openabstractions/abstraction-identity"
)

// capabilities maps each `serve <capability>` name to its entry point.
// The installer's shortcut, service template, restart registration and `start`
// launch `serve supervisor`; TestNoLaunchPathNamesServeHost keeps them current.
var capabilities = map[string]func([]string) error{
	"runtime":    serveRuntime,
	"supervisor": serveHost,
	"asks":       accountCapability(asks.Serve, asks.ErrUsage),
	"rights":     accountCapability(serveRights, rights.ErrUsage),
	"router":     accountCapability(router.Serve, router.ErrUsage),
	"logging":    accountCapability(logging.Serve, logging.ErrUsage),
	"config":     accountCapability(config.Serve, config.ErrUsage),
	"router-v1":  accountCapability(routerservice.Serve, routerservice.ErrUsage),
}

// Legacy capability modules combine parsing and account storage in Serve.
// They make no private-state claim. Keep their lifetime lock and help behavior
// without duplicating each module's flag parser here.
func accountCapability(serve func([]string) error, usageErr error) func([]string) error {
	run := wrapCapabilityUsage(serve, usageErr)
	return func(args []string) error {
		if containsHelp(args) {
			return run(args)
		}
		release, err := runtimeSettingsGuard(context.Background(), runtimeFlags{})
		if err != nil {
			return err
		}
		defer release()
		speakSomewhere()
		return run(args)
	}
}

// wrapCapabilityUsage gives a capability module's Serve the same usage-error
// exit status as every other command here: exitUsage (2). The module already
// printed its own usage text and wrapped usageErr into the error it
// returned; this turns that into the *exitError main's dispatch reads. Any
// other error, including flag.ErrHelp and a startup failure, passes through
// unchanged.
func wrapCapabilityUsage(serve func([]string) error, usageErr error) func([]string) error {
	return func(args []string) error {
		err := serve(args)
		if err != nil && errors.Is(err, usageErr) {
			return &exitError{exitUsage, err}
		}
		return err
	}
}

// complain writes a last diagnostic to standard error ahead of an exit status
// that already reports the failure; it is where a failed diagnostic write ends.
func complain(v ...any) {
	writeHostLogLine(os.Stderr, strings.TrimSuffix(fmt.Sprintln(v...), "\n"))
}

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "logs" {
		exitOnFailure(logsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "factory-reset" {
		exitOnFailure(factoryResetCommand(os.Args[2:], os.Stdout))
		return
	}
	if probeClient(os.Args[0]) {
		exitOnFailure(probeCommand(os.Args[1:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "rights" {
		exitOnFailure(rightsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "inference" {
		exitOnFailure(inferenceCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "provider" {
		exitOnFailure(providerCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "resources" {
		exitOnFailure(resourcesCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "questions" {
		exitOnFailure(questionsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "machines" {
		exitOnFailure(machinesCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "models" {
		exitOnFailure(modelsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "applications" {
		exitOnFailure(applicationsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "opencode" {
		exitOnFailure(opencodeCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "probe" {
		exitOnFailure(probeCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "storage" {
		exitOnFailure(storageCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "download" {
		exitOnFailure(downloadCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "credentials" {
		exitOnFailure(credentialsCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "jobs" {
		exitOnFailure(jobsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "start" {
		// The upgrade exclusion's refusal exits 3, which SDK activation reads.
		exitOnFailure(runtimeStart(os.Args[2:], os.Stdout, os.Stderr))
		return
	}

	if len(os.Args) >= 2 && os.Args[1] == "host" {
		exitOnFailure(hostCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "service" {
		exitOnFailure(serviceCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "status" && os.Args[2] == "describe" {
		exitOnFailure(statusDescribe(os.Args[3:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "status" {
		exitOnFailure(runtimeStatus(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) == 2 && isHelp(os.Args[1]) {
		exitOnFailure(usage(os.Stdout))
		return
	}
	if len(os.Args) < 2 {
		exitOnFailure(commandMistake(os.Stderr, "no command given", "openabstractions --help"))
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "serve" {
		if os.Args[1] == "serve" {
			exitOnFailure(commandMistake(os.Stderr, "serve needs a capability", "openabstractions serve --help"))
			return
		}
		exitOnFailure(commandMistake(os.Stderr, fmt.Sprintf("no command called %q", os.Args[1]), "openabstractions --help"))
		return
	}
	if isHelp(os.Args[2]) {
		_, err := io.WriteString(os.Stdout, serveUsage)
		exitOnFailure(err)
		return
	}
	run, ok := capabilities[os.Args[2]]
	if !ok {
		exitOnFailure(commandMistake(os.Stderr, fmt.Sprintf("no capability called %q", os.Args[2]), "openabstractions serve --help"))
		return
	}
	exitOnFailure(run(os.Args[3:]))
}

// probeClient reports whether this image runs as the probe client, which
// accepts only probe arguments.
func probeClient(arg0 string) bool {
	// A fixed file name, compared as the file system compares names.
	name := filepath.Base(arg0)
	return identity.SamePathName(name, probeClientName) || identity.SamePathName(name, probeClientName+".exe")
}

// exitOnFailure ends the process with the status a failed command carries.
// A command that already printed its own complete message (reported) gets
// no second, redundant "openabstractions: <err>" line; every other command's
// error still gets that one line, the only place it appears.
func exitOnFailure(err error) {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	if !reported(err) {
		complain("openabstractions:", err)
	}
	os.Exit(exitStatus(err))
}

// exitStatus is a failed command's process exit status: an exitError's own
// code, otherwise 1.
func exitStatus(err error) int {
	var exit *exitError
	if errors.As(err, &exit) {
		return exit.code
	}
	return 1
}

// usageGroups lists every command by what a person does with it. usage and
// the bare-command message share this body and differ only in their first
// line.
const usageGroups = `
See what is running:
  factory-reset                preview or reset this account's saved OA settings
  status                       queries what the runtime can currently do
  status describe <endpoint>   prints one endpoint's own description
  probe                        calls one capability directly and prints its outcome

Downloads and work:
  download <url>                fetches a file through the runtime's job service
  jobs                          lists, watches and cancels this program's work
  jobs migrate-legacy           converts legacy job records with an operator mapping
  storage check                 checks managed storage compatibility

Credentials and rules:
  credentials                   registers secrets by name; the runtime applies them
  rights                        lists, grants, revokes and decides exact rights rules
  machines                      prepares pairing, observes native consent and removes local trust

Models and inference:
  models                        lends a model file one store holds to an installed engine
  inference                     manages model servers, the inference gateway, its keys and the audit
  applications                  lists visible applications, requests activation, removes an entry
  opencode open                 opens the official OpenCode CLI in its Windows sandbox
  resources                     prints who holds the card and each other scarce resource
  questions                     lists pending questions and records a person's answer
  logs --app PROGRAM --follow   follows one application's OA logs from the current end

For developers and packagers:
  start                         activates the installed user runtime
  provider                      declares provider processes the runtime launches or attaches to
  host register|unregister      writes or removes the Windows per-user service template
  service <command>             Windows Installer upgrade actions
  serve runtime                 hosts resolution, logging, config and durable jobs in one process
  serve supervisor              the per-user service that keeps the runtime running (Windows)
  serve asks                    answers questions a person has to answer
  serve rights                  holds the runtime's granted rights policy
  serve router                  reports the model servers on this machine
  serve logging                 receives identity-bound structured log records
  serve config                  answers configuration through the service
  serve router-v1                answers model discovery and routing requests

Every command accepts --help for its own options. Starting a foreground host
does not install or register it with the OS. To run a runtime beside an
installed one, give one name and one directory:
  openabstractions serve runtime --isolated <name> --state-dir <absolute dir>
It prints the ABSTRACTION_RUNTIME_ENDPOINT value for its clients.

Start with openabstractions status.

Install: go install github.com/openabstractions/abstractions/serve@<version>
(installs this command as serve, serve.exe on Windows; the installers name it
openabstractions).
`

func usage(w io.Writer) error {
	_, err := fmt.Fprint(w, "openabstractions is the command line of the OpenAbstractions runtime on this machine.\n"+usageGroups)
	return err
}

// serveUsage is `serve --help`'s own page: a serve-scoped usage line and the
// capabilities serve hosts, not the top-level catalogue every other command
// also appears in.
const serveUsage = `Usage: openabstractions serve <capability> [options]

Hosts one capability's own service process in the foreground. Starting it
does not install or register it with the OS.

  runtime      hosts resolution, logging, config and durable jobs in one process
  supervisor   the per-user service that keeps the runtime running (Windows)
  asks         legacy standalone question service; runtime uses its own question book
  rights       legacy standalone rights service; runtime uses its own policy service
  router       reports the model servers on this machine
  logging      receives identity-bound structured log records
  config       answers configuration through the service
  router-v1    answers model discovery and routing requests

Every capability accepts --help for its own options.

Exit codes: 0 a clean stop, 1 startup failure, 2 usage.
`

// openabstractions is the common host command. Runtime composition and platform
// activation are independent of the capability API; legacy single-capability
// commands remain available during migration.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	config "github.com/openabstractions/abstraction-config/go/service"
	logging "github.com/openabstractions/abstraction-logging/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

var capabilities = map[string]func([]string) error{
	"runtime":   serveRuntime,
	"host":      serveHost,
	"asks":      asks.Serve,
	"rights":    rights.Serve,
	"router":    router.Serve,
	"logging":   logging.Serve,
	"config":    config.Serve,
	"router-v1": routerservice.Serve,
}

// complain writes a last diagnostic to standard error ahead of an exit status
// that already reports the failure; it is where a failed diagnostic write ends.
func complain(v ...any) { log.New(os.Stderr, "", 0).Println(v...) }

func main() {
	speakSomewhere()
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
	if len(os.Args) >= 2 && os.Args[1] == "applications" {
		exitOnFailure(applicationsCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "probe" {
		exitOnFailure(probeCommand(os.Args[2:], os.Stdout, os.Stderr))
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "storage" {
		if err := storageCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
			complain("openabstractions:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "download" {
		if err := downloadCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
			complain("openabstractions:", err)
			var exit *exitError
			if errors.As(err, &exit) {
				os.Exit(exit.code)
			}
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "credentials" {
		if err := credentialsCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
			complain("openabstractions:", err)
			var exit *exitError
			if errors.As(err, &exit) {
				os.Exit(exit.code)
			}
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "jobs" {
		if err := jobsCommand(os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
			complain("openabstractions:", err)
			var exit *exitError
			if errors.As(err, &exit) {
				os.Exit(exit.code)
			}
			os.Exit(1)
		}
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
		if err := runtimeStatus(os.Args[2:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
			complain("openabstractions:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && isHelp(os.Args[1]) {
		usage()
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "serve" {
		usage()
		os.Exit(2)
	}
	if isHelp(os.Args[2]) {
		usage()
		return
	}
	run, ok := capabilities[os.Args[2]]
	if !ok {
		fmt.Fprintf(os.Stderr, "openabstractions: no capability called %q\n\n", os.Args[2])
		usage()
		os.Exit(2)
	}
	if err := run(os.Args[3:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "openabstractions:", err)
		var exit *exitError
		if errors.As(err, &exit) {
			os.Exit(exit.code)
		}
		os.Exit(1)
	}
}

// probeClient reports whether this image runs as the probe client, which
// accepts only probe arguments.
func probeClient(arg0 string) bool {
	name := strings.ToLower(filepath.Base(arg0))
	return strings.TrimSuffix(name, ".exe") == probeClientName
}

// exitOnFailure ends the process with the status a failed command carries.
func exitOnFailure(err error) {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return
	}
	complain("openabstractions:", err)
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

func usage() {
	fmt.Fprintln(os.Stderr, `openabstractions — the resident half of each layer

  openabstractions start          activates the installed user runtime
  openabstractions status         queries runtime capability readiness
  openabstractions status describe <endpoint>  prints an endpoint's endpoint@1 Description
  openabstractions storage check  checks managed storage compatibility
  openabstractions download <url>  fetches through the runtime's job service
  openabstractions jobs           lists, observes and cancels this program's work
  openabstractions credentials    registers secrets by name; the runtime applies them
  openabstractions rights         lists, grants, revokes, reads and decides exact rights rules
  openabstractions inference      manages inference hosts, the gateway window, its keys and the audit
  openabstractions provider       declares provider processes the runtime launches or attaches to
  openabstractions applications   lists visible applications and requests activation
  openabstractions probe          calls one read of each capability and prints its typed outcome
  openabstractions jobs migrate-legacy  converts legacy job records with an operator mapping
  openabstractions serve asks     answers questions a person has to answer
  openabstractions serve runtime  hosts resolution, logging, config and durable jobs
  openabstractions serve host     owns the runtime's lifetime on Windows (restart, sign-out, upgrade)
  openabstractions host register|unregister  writes or removes the Windows per-user service template
  openabstractions service begin-upgrade|end-upgrade|start|upgrade-check  Windows Installer upgrade actions
  openabstractions serve rights   holds what was granted, and the awake hold
  openabstractions serve router   reports the hosts on this machine
  openabstractions serve logging  receives identity-bound structured log records
  openabstractions serve config   answers configuration through the service
  openabstractions serve router-v1  serves typed model discovery and route requests

Runtime hosts a catalogue and its selected providers in one process. The other
verbs remain standalone hosts. Each verb accepts --help for its own options.
Starting a foreground host does not install or register it with the OS.
To run beside an installed runtime, give one name and one directory:
  openabstractions serve runtime --isolated <name> --state-dir <absolute dir>
It prints the ABSTRACTION_RUNTIME_ENDPOINT value for its clients.

"go install github.com/openabstractions/abstractions/serve@<version>" installs
this command as serve (serve.exe on Windows); the installers name it openabstractions.

download and jobs replace dl and jobctl, which leave with the legacy file store.
The other CLIs are elsewhere and unchanged: asks and router.`)
}

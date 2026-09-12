// openabstractions is the common host command. Runtime composition and platform
// activation are independent of the capability API; legacy single-capability
// commands remain available during migration.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	asks "github.com/openabstractions/abstraction-asks/go"
	config "github.com/openabstractions/abstraction-config/go/service"
	"github.com/openabstractions/abstraction-download/go/serve"
	logging "github.com/openabstractions/abstraction-logging/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

var capabilities = map[string]func([]string) error{
	"runtime":   serveRuntime,
	"asks":      asks.Serve,
	"rights":    rights.Serve,
	"router":    router.Serve,
	"jobd":      serve.Jobs,
	"logging":   logging.Serve,
	"config":    config.Serve,
	"router-v1": routerservice.Serve,
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		usage()
		return
	}
	if len(os.Args) < 3 || os.Args[1] != "serve" {
		usage()
		os.Exit(2)
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
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `openabstractions — the resident half of each layer

  openabstractions serve asks     answers questions a person has to answer
  openabstractions serve runtime  hosts resolution, logging and config together
  openabstractions serve rights   holds what was granted, and the awake hold
  openabstractions serve router   reports the hosts on this machine
  openabstractions serve jobd     finishes transfers nobody is watching
  openabstractions serve logging  receives identity-bound structured log records
  openabstractions serve config   answers configuration through the service
  openabstractions serve router-v1  serves typed model discovery and route requests

Runtime hosts a catalogue and its selected providers in one process. The other
verbs remain standalone hosts. Each verb accepts --help for its own options.
Starting a foreground host does not install or register it with the OS.

The CLIs are elsewhere and unchanged: dl, jobctl, asks, rights, router.`)
}

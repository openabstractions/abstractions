// openabstractions is the one resident program: one binary to build, sign,
// upgrade and check for a previous version, and one process per capability at
// run time.
//
// `openabstractions serve <capability>` is what a platform starts. It is not
// one host process — every platform's stated reason for starting a service is a
// reason against sharing one. Registering it is per platform and is
// not here: the registration decides when a capability starts and when it idles
// out, and this program only has to be something a registration can name.
//
// It is in the charter rather than in a layer because it belongs to no single
// layer, and a module that may depend on all four cannot sit inside one of them.
package main

import (
	"fmt"
	"os"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-download/go/serve"
	rights "github.com/openabstractions/abstraction-rights/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// capabilities is the whole of what this program is. A capability is a name a
// registration writes and a function that holds an endpoint until it is
// stopped; nothing else about it is this program's business.
var capabilities = map[string]func([]string) error{
	"asks":   asks.Serve,
	"rights": rights.Serve,
	"router": router.Serve,
	"jobd":   serve.Jobs,
}

func main() {
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
		fmt.Fprintln(os.Stderr, "openabstractions:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `openabstractions — the resident half of each layer

  openabstractions serve asks     answers questions a person has to answer
  openabstractions serve rights   holds what was granted, and the awake hold
  openabstractions serve router   reports the hosts on this machine
  openabstractions serve jobd     finishes transfers nobody is watching

One capability per process. Each takes the flags its own service takes;
--endpoint is where it listens, and every one of them defaults to the fixed
per-user name an installer can write into a registration.

The CLIs are elsewhere and unchanged: dl, jobctl, asks, rights, router.`)
}

// An application that knows nothing about NASes, BITS, shares or job stores.
//
// This is the entire integration a fork of Lemonade would carry. Note what is
// absent: no path, no hostname, no flag, no branch on which tier to use, no
// mention of a NAS, and not even this program's own name. Discover takes no
// arguments because there is nothing left for the caller to know — the OS says
// which executable is running, and the machine says which tiers it has.
package main

import (
	"context"
	"fmt"
	"os"

	download "github.com/openabstractions/abstraction-download/go"
	_ "github.com/openabstractions/abstraction-download/go/all" // the classpath, spelled in Go
)

func main() {
	r, err := download.Discover()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("tiers linked into this build : %v\n", download.RegisteredTiers())
	fmt.Printf("tier this machine will use   : %s\n", r.Tier())

	if len(os.Args) < 2 {
		return
	}
	// From here it is ordinary: submit, and let whatever answered do the work.
	id, err := download.Submit(r.Store, download.Spec{
		Sources: []download.Source{{Scheme: "https", Locator: os.Args[1]}},
		Sink:    download.Sink{Final: "files/example.bin"},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("submitted %s\n", id)
	if err := r.Delegate(context.Background(), id); err != nil {
		fmt.Println("nothing to delegate to; this process would do it itself")
		return
	}
	fmt.Printf("handed to %s\n", r.Tier())
}

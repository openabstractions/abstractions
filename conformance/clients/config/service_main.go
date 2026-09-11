// Optional isolated provider fixture. The default runner builds the central host.
package main

import (
	"fmt"
	"github.com/openabstractions/abstraction-config/go/service"
	"os"
)

func main() {
	if err := service.Serve(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

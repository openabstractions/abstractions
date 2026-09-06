// An application that knows nothing about NASes, BITS, shares or job stores.
//
// This is the entire integration a fork of Lemonade would carry: one import, no
// path, no hostname, no flag, and no branch on who does the work.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	abstraction "github.com/openabstractions/abstraction-facade/go"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	a, err := abstraction.Discover()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "this machine : %v\n", a.Bindings())

	downloads := a.Download()
	fmt.Fprintf(out, "bytes go to  : %s\n", downloads.Where())

	if len(args) == 0 {
		return nil
	}

	h, err := downloads.Get(args[0], "files/")
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "submitted %s\n", h.ID())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := h.Wait(ctx); err != nil {
		return err
	}
	where, err := h.Destination()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "delivered to %s\n", where)
	return nil
}

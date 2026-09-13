package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func storageCommand(args []string, output, diagnostics io.Writer) error {
	const usage = "Usage: openabstractions storage check [--state-dir absolute]\nChecks managed HTTP runtime storage metadata without starting or recovering it.\nCompatible existing stores may receive stable host-guard metadata.\nThe result is an advisory snapshot; runtime open rechecks compatibility.\n"
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, usage)
		return err
	}
	if args[0] != "check" {
		return fmt.Errorf("storage: unknown command %q; use storage --help", args[0])
	}
	flags := flag.NewFlagSet("storage check", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() { fmt.Fprint(diagnostics, usage); flags.PrintDefaults() }
	state := flags.String("state-dir", "", "absolute managed runtime state directory (default: current user's runtime-v1)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("storage check: unexpected arguments")
	}
	supplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "state-dir" {
			supplied = true
		}
	})
	if supplied && !filepath.IsAbs(*state) {
		return errors.New("storage check: --state-dir must be absolute")
	}
	if !supplied {
		var err error
		*state, err = runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
		if err != nil {
			return fmt.Errorf("storage check: %w", err)
		}
	}
	root := filepath.Join(*state, "jobs")
	if err := acceptanceprovider.CheckManaged(root, downloadserve.HTTPExecution{}); err != nil {
		return fmt.Errorf("storage check %q: %w", root, err)
	}
	// Existing compatible stores are checked again under the host guard. This
	// can create the stable guard metadata, but never initializes a missing store.
	if _, err := os.Stat(root); err == nil {
		guard, err := acceptanceprovider.AcquireHost(root)
		if err != nil {
			return fmt.Errorf("storage check %q: %w", root, err)
		}
		checkErr := acceptanceprovider.CheckManaged(root, downloadserve.HTTPExecution{})
		closeErr := guard.Close()
		if err := errors.Join(checkErr, closeErr); err != nil {
			return fmt.Errorf("storage check %q: %w", root, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("storage check %q: %w", root, err)
	}
	_, err := fmt.Fprintf(output, "Managed HTTP runtime storage preflight passed: %s\nAdvisory snapshot only; runtime open rechecks compatibility and recovery.\n", root)
	return err
}

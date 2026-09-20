package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
)

func storageCommand(args []string, output, diagnostics io.Writer) error {
	const usage = "Usage: openabstractions storage check [--state-dir absolute] [--read-only]\n" +
		"Checks managed HTTP runtime storage metadata without starting or recovering it.\n" +
		"Compatible existing stores may receive stable host-guard metadata.\n" +
		"The result is an advisory snapshot; runtime open rechecks compatibility.\n" +
		"--read-only takes no host guard and writes nothing. It fails with a live-host\n" +
		"error while a runtime holds the store's guard; a runtime may start after it.\n"
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
	readOnly := flags.Bool("read-only", false, "inspect metadata without the host guard; writes nothing and reports a live runtime")
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
	// A guarded check of an existing store writes host-guard metadata into the
	// account's default state.
	if _, err := os.Stat(root); err == nil && !supplied && !*readOnly {
		if err := refuseVirtualizedProfile("storage check", bootstrap.CurrentProfileView); err != nil {
			return err
		}
	}
	executor, err := managedJobExecutor(root, nil)
	if err != nil {
		return fmt.Errorf("storage check %q: %w", root, err)
	}
	// CheckManaged reads the owner header without creating directories, locks
	// or journals, for both modes.
	if err := acceptanceprovider.CheckManaged(root, executor); err != nil {
		return fmt.Errorf("storage check %q: %w", root, err)
	}
	if *readOnly {
		// No guard: a sidecar lock outside the store would exclude nobody, since
		// the runtime guards with the store's own acceptance/host.lock. The probe
		// opens that lock read-only and never creates it.
		active, err := acceptanceprovider.HostActive(root)
		if err != nil {
			return fmt.Errorf("storage check %q: host probe: %w", root, err)
		}
		if active {
			fmt.Fprintf(output, "Managed HTTP runtime storage metadata passed (read-only): %s\n", root)
			return fmt.Errorf("storage check %q: %w: a live runtime holds the store and may be writing it", root, acceptanceprovider.ErrHostActive)
		}
		_, err = fmt.Fprintf(output, "Managed HTTP runtime storage preflight passed (read-only): %s\n"+
			"Read-only snapshot: no host guard was taken and no runtime held one when probed; a runtime may start after this check.\n", root)
		return err
	}
	// Existing compatible stores are checked again under the host guard. This
	// can create the stable guard metadata, but never initializes a missing store.
	if _, err := os.Stat(root); err == nil {
		guard, err := acceptanceprovider.AcquireHost(root)
		if err != nil {
			return fmt.Errorf("storage check %q: %w", root, err)
		}
		checkErr := acceptanceprovider.CheckManaged(root, executor)
		closeErr := guard.Close()
		if err := errors.Join(checkErr, closeErr); err != nil {
			return fmt.Errorf("storage check %q: %w", root, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("storage check %q: %w", root, err)
	}
	_, err = fmt.Fprintf(output, "Managed HTTP runtime storage preflight passed: %s\nAdvisory snapshot only; runtime open rechecks compatibility and recovery.\n", root)
	return err
}

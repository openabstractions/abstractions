package main

import (
	"errors"
	"flag"
	"fmt"
	"runtime"
)

func validateOwnedPanelFlags(native, quiet bool, page string) error {
	return validateOwnedPanelArguments(runtime.GOOS, native, quiet, page, flag.CommandLine)
}

func validateOwnedPanelArguments(platform string, native, quiet bool, page string, flags *flag.FlagSet) error {
	if platform != "windows" {
		return errors.New("the owned Panel host requires Windows")
	}
	if !native || !quiet || page != "credentials" || flags.NArg() != 0 {
		return errors.New("the owned Panel recipe requires -native -quiet -page credentials")
	}
	var err error
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "owned-host", "native", "quiet", "page", "runtime-endpoint", "runtime-program", "runtime-state-dir":
		default:
			err = fmt.Errorf("the owned Panel recipe refuses -%s", f.Name)
		}
	})
	return err
}

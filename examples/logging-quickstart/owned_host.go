package main

import (
	"context"
	"fmt"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/appmodelhost"
	"os"
	"os/signal"
	"path/filepath"
)

func ownedHost() bool {
	if len(os.Args) < 2 || os.Args[1] != "--owned-host" {
		return false
	}
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "owned host accepts no arguments")
		os.Exit(2)
	}
	exe, err := os.Executable()
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		err = appmodelhost.ServeOwned(ctx, facade.Discover(), filepath.Join(filepath.Dir(exe), "abstraction_facade_appmodel.dll"), "logging-quickstart", "0.8.0", os.Stdin, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return true
}

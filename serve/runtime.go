package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	logging "github.com/openabstractions/abstraction-logging/go"
)

type runtimeFlags struct {
	endpoint, logEndpoint, configEndpoint, out string
	jobRoot, jobOwner, jobEndpoint             string
	supervised                                 bool
}

func parseRuntime(args []string, output io.Writer) (runtimeFlags, error) {
	var options runtimeFlags
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&options.supervised, "supervised", false, "installed-parent mode: private stdin pipe EOF cancels; stdout READY 1 after listeners initialize")
	flags.StringVar(&options.endpoint, "endpoint", "", "resolver bootstrap endpoint (default: runtime-v1)")
	flags.StringVar(&options.logEndpoint, "log-endpoint", "", "logging service endpoint")
	flags.StringVar(&options.configEndpoint, "config-endpoint", "", "configuration service endpoint")
	flags.StringVar(&options.out, "out", "", "service-owned log file (default: user cache/openabstractions/logging/records.jsonl)")
	flags.StringVar(&options.jobRoot, "jobs-root", "", "enable durable admission in this service-owned local job store")
	flags.StringVar(&options.jobOwner, "jobs-owner", "", "stable logical owner for --jobs-root; required together")
	flags.StringVar(&options.jobEndpoint, "jobs-endpoint", "", "job admission endpoint when enabled")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("runtime: unexpected arguments")
	}
	if (options.jobRoot == "") != (options.jobOwner == "") || (options.jobRoot == "" && options.jobEndpoint != "") {
		return options, fmt.Errorf("runtime: --jobs-root and --jobs-owner are required together; --jobs-endpoint requires both")
	}
	return options, nil
}

func serveRuntime(args []string) error {
	options, err := parseRuntime(args, os.Stderr)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if options.supervised {
		return superviseRuntime(ctx, options, os.Stdin, os.Stdout)
	}
	return runRuntime(ctx, options)
}

func runRuntime(ctx context.Context, options runtimeFlags) error {
	return runRuntimeReady(ctx, options, nil)
}

func runRuntimeReady(ctx context.Context, options runtimeFlags, ready func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.out == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return err
		}
		options.out = filepath.Join(cache, "openabstractions", "logging", "records.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(options.out), 0700); err != nil {
		return err
	}
	sink, err := logging.OpenFileSink(options.out)
	if err != nil {
		return err
	}
	defer sink.Close()
	runtime, err := host.Listen(host.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint, Sink: sink,
		JobRoot: options.jobRoot, JobOwner: options.jobOwner, JobEndpoint: options.jobEndpoint,
		OnError: func(err error) { fmt.Fprintln(os.Stderr, "runtime:", err) },
	})
	if err != nil {
		return err
	}
	defer runtime.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	return runtime.Serve(ctx)
}

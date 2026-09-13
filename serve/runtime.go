package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	model "github.com/openabstractions/abstraction-model/go"
)

type runtimeFlags struct {
	endpoint, logEndpoint, configEndpoint, out string
	jobRoot, jobOwner, jobEndpoint             string
	stateDir                                   string
	withoutJobs                                bool
	withoutModels                              bool
	modelEndpoint                              string
	supervised                                 bool
	executeDownloads                           bool
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
	flags.StringVar(&options.stateDir, "state-dir", "", "absolute private durable runtime state directory")
	flags.BoolVar(&options.withoutJobs, "without-jobs", false, "omit durable job execution")
	flags.BoolVar(&options.withoutModels, "without-models", false, "omit model registry lookup")
	flags.StringVar(&options.modelEndpoint, "model-endpoint", "", "model lookup service endpoint")
	flags.StringVar(&options.jobRoot, "jobs-root", "", "explicit service-owned job store; requires --jobs-owner")
	flags.StringVar(&options.jobOwner, "jobs-owner", "", "stable logical owner for --jobs-root; required together")
	flags.StringVar(&options.jobEndpoint, "jobs-endpoint", "", "job service endpoint")
	flags.BoolVar(&options.executeDownloads, "jobs-run-downloads", false, "execute accepted anonymous HTTP(S) downloads inside the service-owned jobs root")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("runtime: unexpected arguments")
	}
	if (options.jobRoot == "") != (options.jobOwner == "") {
		return options, fmt.Errorf("runtime: --jobs-root and --jobs-owner are required together")
	}
	if options.withoutJobs && (options.jobRoot != "" || options.jobEndpoint != "" || options.executeDownloads) {
		return options, fmt.Errorf("runtime: --without-jobs conflicts with job options")
	}
	if options.withoutModels && options.modelEndpoint != "" {
		return options, fmt.Errorf("runtime: --without-models conflicts with --model-endpoint")
	}
	if options.stateDir != "" && (!filepath.IsAbs(options.stateDir) || options.jobRoot != "") {
		return options, fmt.Errorf("runtime: --state-dir requires an absolute path and managed jobs")
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
	managed := !options.withoutJobs && options.jobRoot == ""
	if managed {
		state := options.stateDir
		if state == "" {
			var err error
			state, err = runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
			if err != nil {
				return err
			}
		}
		if !filepath.IsAbs(state) {
			return fmt.Errorf("runtime: absolute state directory required")
		}
		options.jobRoot = filepath.Join(state, "jobs")
		options.executeDownloads = true
	}
	sink, closeSink, err := runtimeLogSink(options.out)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runtime logging:", err)
	}
	if closeSink != nil {
		defer closeSink.Close()
	}
	var executor acceptanceprovider.Executor
	if options.executeDownloads {
		executor = downloadserve.HTTPExecution{OnError: func(err error) { fmt.Fprintln(os.Stderr, "runtime:", err) }}
	}
	var registry *model.Registry
	if !options.withoutModels {
		registry = model.NewServiceRegistry(model.HF{}, model.Ollama{})
		// Explicit bootstrap endpoints also isolate companion model listeners.
		if options.modelEndpoint == "" && options.endpoint != "" {
			options.modelEndpoint = options.endpoint + "-model"
		}
	}
	runtime, err := host.Listen(host.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint, Sink: sink,
		ModelRegistry: registry, ModelEndpoint: options.modelEndpoint,
		JobRoot: options.jobRoot, JobOwner: options.jobOwner, JobEndpoint: options.jobEndpoint,
		JobExecutor: executor, ManagedJobs: managed,
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

// Durable work survives user cache cleanup.
func runtimeStateDir(platform string, getenv func(string) string, home func() (string, error)) (string, error) {
	var base string
	switch platform {
	case "windows":
		base = getenv("LOCALAPPDATA")
	case "linux":
		base = getenv("XDG_DATA_HOME")
		if base == "" {
			h, err := home()
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(h) {
				return "", fmt.Errorf("runtime: absolute home required")
			}
			base = filepath.Join(h, ".local", "share")
		}
	case "darwin":
		h, err := home()
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(h) {
			return "", fmt.Errorf("runtime: absolute home required")
		}
		base = filepath.Join(h, "Library", "Application Support")
	default:
		return "", fmt.Errorf("runtime: unsupported state platform %q", platform)
	}
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("runtime: absolute user data directory required")
	}
	return filepath.Join(base, "openabstractions", "runtime-v1"), nil
}

func runtimeLogSink(path string) (logging.Sink, io.Closer, error) {
	if path == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return nil, nil, err
		}
		path = filepath.Join(cache, "openabstractions", "logging", "records.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	sink, err := logging.OpenFileSink(path)
	if err != nil {
		return nil, nil, err
	}
	return sink, sink, nil
}

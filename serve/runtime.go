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
	"strings"
	"syscall"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
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
	downloadBackend                            string
	downloadNASRoot                            string
	isolated                                   string
	gateway                                    string
}

func parseRuntime(args []string, output io.Writer) (runtimeFlags, error) {
	var options runtimeFlags
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.BoolVar(&options.supervised, "supervised", false, "installed-parent mode: private stdin pipe EOF cancels; stdout READY 1 after listeners initialize")
	flags.StringVar(&options.isolated, "isolated", "", "run beside an installed runtime: derive every endpoint from this name (a-z, 0-9, -); requires --state-dir")
	flags.StringVar(&options.endpoint, "endpoint", "", `resolver endpoint as a full platform path: \\.\pipe\<name> on Windows, an absolute socket path elsewhere (default: the installed runtime's per-user runtime-v1)`)
	flags.StringVar(&options.logEndpoint, "log-endpoint", "", "logging service endpoint, same form as --endpoint")
	flags.StringVar(&options.configEndpoint, "config-endpoint", "", "configuration service endpoint, same form as --endpoint")
	flags.StringVar(&options.out, "out", "", "service-owned log file (default: user cache/openabstractions/logging/records.jsonl; with --isolated, <state-dir>/logging/records.jsonl)")
	flags.StringVar(&options.stateDir, "state-dir", "", "absolute private durable runtime state directory")
	flags.BoolVar(&options.withoutJobs, "without-jobs", false, "omit durable job execution")
	flags.BoolVar(&options.withoutModels, "without-models", false, "omit model registry lookup")
	flags.StringVar(&options.modelEndpoint, "model-endpoint", "", "model lookup service endpoint (default with --endpoint: that endpoint followed by -model)")
	flags.StringVar(&options.jobRoot, "jobs-root", "", "explicit service-owned job store; requires --jobs-owner")
	flags.StringVar(&options.jobOwner, "jobs-owner", "", "stable logical owner for --jobs-root; required together")
	flags.StringVar(&options.jobEndpoint, "jobs-endpoint", "", "job service endpoint, same form as --endpoint")
	flags.BoolVar(&options.executeDownloads, "jobs-run-downloads", false, "execute accepted anonymous HTTP(S) downloads inside the service-owned jobs root")
	flags.StringVar(&options.downloadBackend, "jobs-download-backend", "", "download provider: native or nas (managed jobs only)")
	flags.StringVar(&options.downloadNASRoot, "jobs-download-nas-root", "", "absolute shared job store for the NAS download provider")
	flags.StringVar(&options.gateway, "gateway", "", "open the inference gateway window on 127.0.0.1:<port> for programs holding a local key (default: closed)")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if err := gatewayAddress(options.gateway); err != nil {
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
	if _, _, err := normalizeDownloadProvider(options.downloadBackend, options.downloadNASRoot); err != nil {
		return options, err
	}
	if options.withoutJobs && (options.downloadBackend != "" || options.downloadNASRoot != "") {
		return options, fmt.Errorf("runtime: --without-jobs conflicts with download provider options")
	}
	if options.withoutModels && options.modelEndpoint != "" {
		return options, fmt.Errorf("runtime: --without-models conflicts with --model-endpoint")
	}
	if options.stateDir != "" && (!filepath.IsAbs(options.stateDir) || options.jobRoot != "") {
		return options, fmt.Errorf("runtime: --state-dir requires an absolute path and managed jobs")
	}
	for _, item := range endpointFlags(options) {
		if err := endpointForm(runtime.GOOS, item.flag, item.value); err != nil {
			return options, err
		}
	}
	if options.isolated != "" {
		if err := isolateRuntime(&options, bootstrap.Endpoint); err != nil {
			return options, err
		}
	} else if err := completeSelection(options); err != nil {
		return options, err
	}
	return options, nil
}

// isolatedRuntimeUsage names the accepted way to run beside an installed runtime.
const isolatedRuntimeUsage = "use --isolated <name> --state-dir <absolute dir> to derive every endpoint from one name"

type endpointFlag struct{ flag, value string }

func endpointFlags(options runtimeFlags) []endpointFlag {
	return []endpointFlag{
		{"--endpoint", options.endpoint}, {"--log-endpoint", options.logEndpoint}, {"--config-endpoint", options.configEndpoint},
		{"--jobs-endpoint", options.jobEndpoint}, {"--model-endpoint", options.modelEndpoint},
	}
}

// endpointForm refuses endpoint text the listener cannot open, naming the flag
// and the accepted form. An empty value selects the default and is accepted.
func endpointForm(platform, flag, value string) error {
	if value == "" {
		return nil
	}
	if platform == "windows" {
		const prefix = `\\.\pipe\`
		if len(value) > len(prefix) && strings.EqualFold(value[:len(prefix)], prefix) {
			return nil
		}
		hint := ""
		if strings.Contains(strings.ToLower(value), "pipe") {
			hint = " (a shell such as Git Bash can rewrite backslashes; run from PowerShell or cmd)"
		}
		return fmt.Errorf(`runtime: %s %q is not a named pipe path; give the full form \\.\pipe\<name>%s, or %s`, flag, value, hint, isolatedRuntimeUsage)
	}
	if !strings.HasPrefix(value, "/") {
		return fmt.Errorf("runtime: %s %q is not an absolute socket path; give the full form /absolute/dir/<name>.sock, or %s", flag, value, isolatedRuntimeUsage)
	}
	return nil
}

// isolateRuntime derives each endpoint the caller left unset from the name, in
// the per-user namespace the installed runtime uses. Derived service names end
// in -runtime, -logging, -config, -jobs or -model; installed ones end in -v1.
func isolateRuntime(options *runtimeFlags, derive func(string) (string, error)) error {
	name := options.isolated
	valid := len(name) <= 64
	for _, c := range name {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			valid = false
		}
	}
	if !valid {
		return fmt.Errorf("runtime: --isolated %q must be 1-64 characters from a-z, 0-9 and -", name)
	}
	if options.stateDir == "" {
		return fmt.Errorf("runtime: --isolated %q requires --state-dir <absolute dir> to keep its jobs and log apart from the installed runtime's", name)
	}
	for _, item := range []struct {
		value   *string
		service string
		enabled bool
	}{
		{&options.endpoint, "runtime", true}, {&options.logEndpoint, "logging", true}, {&options.configEndpoint, "config", true},
		{&options.jobEndpoint, "jobs", !options.withoutJobs}, {&options.modelEndpoint, "model", !options.withoutModels},
	} {
		if *item.value != "" || !item.enabled {
			continue
		}
		endpoint, err := derive(name + "-" + item.service)
		if err != nil {
			return fmt.Errorf("runtime: --isolated %q: %w", name, err)
		}
		*item.value = endpoint
	}
	if options.out == "" {
		options.out = filepath.Join(options.stateDir, "logging", "records.jsonl")
	}
	return nil
}

// completeSelection refuses a partly explicit runtime. An explicit endpoint
// marks a separate runtime; each listener or store left on its default would
// share the installed runtime's endpoint, job store or log file.
func completeSelection(options runtimeFlags) error {
	var explicit, missing []string
	for _, item := range endpointFlags(options) {
		if item.value != "" {
			explicit = append(explicit, item.flag)
		}
	}
	if len(explicit) == 0 {
		return nil
	}
	for _, item := range []struct {
		flag    string
		missing bool
	}{
		{"--endpoint", options.endpoint == ""},
		{"--log-endpoint", options.logEndpoint == ""},
		{"--config-endpoint", options.configEndpoint == ""},
		{"--jobs-endpoint", !options.withoutJobs && options.jobEndpoint == ""},
		{"--model-endpoint", !options.withoutModels && options.modelEndpoint == "" && options.endpoint == ""},
		{"--state-dir", !options.withoutJobs && options.stateDir == "" && options.jobRoot == ""},
		{"--out", options.out == ""},
	} {
		if item.missing {
			missing = append(missing, item.flag)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("runtime: %s given without %s, which would share the installed runtime's defaults; give them too, or %s",
		strings.Join(explicit, ", "), strings.Join(missing, ", "), isolatedRuntimeUsage)
}

// describeIsolated tells the operator where an isolated runtime listens. The
// resolver endpoint is the value clients take as ABSTRACTION_RUNTIME_ENDPOINT.
func describeIsolated(w io.Writer, options runtimeFlags) {
	fmt.Fprintf(w, "runtime: isolated runtime %q is listening\n", options.isolated)
	fmt.Fprintf(w, "  ABSTRACTION_RUNTIME_ENDPOINT=%s\n", options.endpoint)
	for _, item := range []endpointFlag{
		{"logging", options.logEndpoint}, {"config", options.configEndpoint}, {"jobs", options.jobEndpoint},
		{"model", options.modelEndpoint}, {"state", options.stateDir}, {"log file", options.out},
	} {
		if item.value != "" {
			fmt.Fprintf(w, "  %-8s %s\n", item.flag, item.value)
		}
	}
}

func serveRuntime(args []string) error {
	options, err := parseRuntime(args, os.Stderr)
	if err != nil {
		return err
	}
	// A runtime on the account's default state must see the real profile. One
	// whose state directory is named explicitly serves only the clients that
	// name it, as tests and isolated runtimes do.
	if options.stateDir == "" {
		if err := refuseVirtualizedProfile("serve runtime", bootstrap.CurrentProfileView); err != nil {
			return err
		}
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
		complain("runtime logging:", err)
	}
	if closeSink != nil {
		defer closeSink.Close()
	}
	var executor acceptanceprovider.Executor
	report := func(err error) { complain("runtime:", err) }
	if managed {
		if executor, err = managedDownloadExecutor(options.jobRoot, report, options.downloadBackend, options.downloadNASRoot); err != nil {
			return fmt.Errorf("runtime jobs: %w", err)
		}
	} else if options.executeDownloads {
		executor, err = managedDownloadExecutor(options.jobRoot, report, options.downloadBackend, options.downloadNASRoot)
		if err != nil {
			return fmt.Errorf("runtime jobs: %w", err)
		}
	}
	var registry *model.Registry
	var registries []string
	if !options.withoutModels {
		resolvers := []model.Resolver{model.HF{}, model.Ollama{}}
		registry = model.NewServiceRegistry(resolvers...)
		for _, r := range resolvers {
			registries = append(registries, r.Registry())
		}
		// Explicit bootstrap endpoints also isolate companion model listeners.
		if options.modelEndpoint == "" && options.endpoint != "" {
			options.modelEndpoint = options.endpoint + "-model"
		}
	}
	rightsState, err := composeRights(options)
	if err != nil {
		complain("runtime rights:", err)
	}
	credentials, err := composeCredentials(options, rightsState, report)
	if err != nil {
		complain("runtime credentials:", err)
	}
	executor = credentials.executor(executor)
	hostOptions := host.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint, Sink: sink,
		ModelRegistry: registry, ModelEndpoint: options.modelEndpoint,
		JobRoot: options.jobRoot, JobOwner: options.jobOwner, JobEndpoint: options.jobEndpoint,
		JobExecutor: executor, ManagedJobs: managed,
		OnError: func(err error) { fmt.Fprintln(os.Stderr, "runtime:", err) },
	}
	hostOptions.ConfigStore, hostOptions.ConfigUserKey = isolatedConfigStore(options.stateDir)
	// A runtime with its own configuration file reads no machine rung either.
	hostOptions.ConfigWithoutMachine = hostOptions.ConfigStore != nil
	credentials.configure(&hostOptions)
	inference, err := composeInference(options, credentials, sink, report)
	if err != nil {
		complain("runtime inference:", err)
	}
	executor = composeInferenceJobs(executor, inference, credentials, report)
	hostOptions.JobExecutor = executor
	routerEndpoint, err := runtimeRouterEndpoint(options)
	if err != nil {
		credentials.close()
		inference.close()
		return err
	}
	inference.configure(&hostOptions, routerEndpoint)
	questions, err := composeAsks(options, rightsState, report)
	if err != nil {
		complain("runtime asks:", err)
	}
	questions.configure(&hostOptions)
	applications, applicationsEndpoint, appErr := composeApplications(options, rightsState, report)
	if appErr != nil {
		complain("runtime applications:", appErr)
	}
	if applications != nil {
		defer applications.Close()
	}
	configureApplications(&hostOptions, applications, applicationsEndpoint)
	var composedActions []string
	if hostOptions.JobRoot != "" {
		composedActions = append(composedActions, jobapi.ResourceActions...)
	}
	rightsState.configure(&hostOptions, composedActions)
	gateByRights(&hostOptions, rightsState.decider())
	if rightsState != nil {
		if err := rightsState.install(hostOptions.RightsActions, installationRules(hostOptions, registries)); err != nil {
			complain("runtime rights:", err)
		}
	}
	runtime, err := host.Listen(hostOptions)
	if err != nil {
		credentials.close()
		inference.close()
		return err
	}
	defer runtime.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	if options.isolated != "" {
		describeIsolated(os.Stderr, options)
	}
	if ready != nil {
		if err := ready(); err != nil {
			return err
		}
	}
	return runtime.Serve(ctx)
}

// isolatedConfigStore selects the configuration file an isolated runtime
// reads and edits, under its own state directory instead of the installed
// runtime's config.UserPath(). An empty state directory keeps the installed
// runtime's own user configuration store, which is the intended shared
// behavior for a runtime that was not given --state-dir.
func isolatedConfigStore(stateDir string) (casapi.Store, string) {
	if stateDir == "" {
		return nil, ""
	}
	return casapi.BoundedFileStore{MaxBytes: config.MaxUserFileBytes}, filepath.Join(stateDir, "config", "config.json")
}

// managedJobRoot is the managed job store the runtime opens for a state
// directory; an empty state selects the current user's runtime-v1 directory.
func managedJobRoot(state string) (string, error) {
	if state == "" {
		var err error
		if state, err = runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir); err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(state) {
		return "", fmt.Errorf("runtime: absolute state directory required")
	}
	return filepath.Join(state, "jobs"), nil
}

// managedJobExecutor is used by storage inspection and migration commands.
// It remains read-only and does not pin a runtime provider selection.
func managedJobExecutor(root string, onError func(error)) (acceptanceprovider.Executor, error) {
	profile, owned, err := acceptanceprovider.RecordedExecutionProfile(root)
	if err != nil {
		return nil, err
	}
	execution := downloadserve.HTTPExecution{OnError: onError}
	if owned && profile == downloadserve.LegacySinkProfile {
		return downloadserve.LegacySinkExecution{HTTPExecution: execution}, nil
	}
	return execution, nil
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

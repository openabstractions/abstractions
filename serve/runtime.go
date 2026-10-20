package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	casapi "github.com/openabstractions/abstraction-cas/go/api"
	config "github.com/openabstractions/abstraction-config/go"
	configservice "github.com/openabstractions/abstraction-config/go/service"
	archive "github.com/openabstractions/abstraction-download/go/archive"
	downloadserve "github.com/openabstractions/abstraction-download/go/serve"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"github.com/openabstractions/abstraction-job/go/acceptanceprovider"
	logging "github.com/openabstractions/abstraction-logging/go"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
	model "github.com/openabstractions/abstraction-model/go"
	"github.com/openabstractions/abstraction-provider-kit/go/modelplace"
)

type runtimeFlags struct {
	endpoint, logEndpoint, configEndpoint, out string
	jobRoot, jobOwner, jobEndpoint             string
	stateDir                                   string
	withoutJobs                                bool
	withoutModels                              bool
	modelEndpoint                              string
	supervised                                 bool
	xpc                                        bool
	executeDownloads                           bool
	downloadBackend                            string
	downloadNASRoot                            string
	isolated                                   string
	gateway                                    string
	// Only the in-process isolated test fixture sets this; no CLI/config field.
	// The state guard must prove private ownership before composition can bind.
	gatewayEphemeral    bool
	operatorProgram     string
	remoteConfiguration func(context.Context) (*runtimeRemoteConfig, error)
	// Set only after runRuntimeReady proves the effective state disjoint and
	// acquires its guard; CLI text and endpoint spelling do not set this bit.
	privateState bool
	// Private composition seams; CLI callers retain the bootstrap owners.
	endpointFor, installedEndpointFor func(string) (string, error)
}

const runtimeUsage = `Usage: openabstractions serve runtime [options]

Hosts resolution, logging, config and durable jobs in one process: the
runtime every other command connects to. Foreground only; it does not
install or register itself with the OS.

  --endpoint EP             resolver endpoint, a full platform path: \\.\pipe\
                            <name> on Windows, an absolute socket path
                            elsewhere (default: the installed runtime's
                            per-user runtime-v1)
  --log-endpoint EP         logging service endpoint, same form as --endpoint
  --config-endpoint EP      configuration service endpoint, same form
  --out PATH                service-owned log file (default: user cache/
                            openabstractions/logging/records.jsonl; with
                            --isolated, <state-dir>/logging/records.jsonl)
  --state-dir DIR           absolute private durable runtime state directory
  --without-jobs            omit durable job execution
  --without-models          omit model registry lookup
  --model-endpoint EP       model lookup service endpoint (default with
                            --endpoint: that endpoint followed by -model)
  --jobs-root DIR           explicit service-owned job store; requires
                            --jobs-owner
  --jobs-owner NAME         stable logical owner for --jobs-root; required
                            together
  --jobs-endpoint EP        job service endpoint, same form as --endpoint
  --jobs-run-downloads      execute accepted HTTP(S) downloads
                            inside the service-owned jobs root
  --jobs-download-backend B download provider: native or nas (managed jobs
                            only)
  --jobs-download-nas-root D  override the nas_store configuration setting
                             (NAS backend reads Settings at startup; a changed
                              store refuses an existing job-provider binding)
                            download provider
  --gateway PORT            open the inference gateway window on
                            127.0.0.1:PORT for programs holding a local key
                            (default: closed)
  --isolated NAME            run beside an installed runtime: derive every
                            endpoint from this name (a-z, 0-9, -); requires
                            --state-dir
  --operator-program PATH    with --isolated and --state-dir only: also
                            admit this executable as an operator, such
                            as a Panel built from source; the installed
                            runtime refuses it
  --supervised               installed-parent mode: private stdin pipe EOF
                            cancels; stdout READY 1 after listeners initialize
  --xpc                      use macOS LaunchAgent Mach services; requires
                            launchd registration. With --isolated, every
                            service uses its own development namespace

To run beside an installed runtime, give one name and one directory:
  openabstractions serve runtime --isolated <name> --state-dir <absolute dir>
It prints the ABSTRACTION_RUNTIME_ENDPOINT value for its clients.

To require explicit download grants for ordinary clients, turn off the download
account default in this isolated runtime, then grant the chosen program:
  openabstractions rights defaults --off download --endpoint <isolated-endpoint>
  openabstractions rights grant --program <absolute-app> --action abstraction.job/acceptance.submit --resource job:download --endpoint <isolated-endpoint>
The grant uses the existing Rights policy; it does not grant another job kind.

On macOS, add --xpc for native Program proof. Register its MachServices in
the same user's launchd domain as com.openabstractions.isolated.<name>.<service-v1>.

Exit codes: 0 a supervised runtime told to stop, 1 an unexpected argument, a
refused value or a startup failure, 2 an unknown flag or a malformed value,
4 this process sees a packaged app's private copy of AppData.
`

func parseRuntime(args []string, output io.Writer) (runtimeFlags, error) {
	return parseRuntimeWithEndpoints(args, output, nil, nil)
}

func parseRuntimeWithEndpoints(args []string, output io.Writer, endpointFor, installedEndpointFor func(string) (string, error)) (runtimeFlags, error) {
	options := runtimeFlags{endpointFor: endpointFor, installedEndpointFor: installedEndpointFor}
	if containsHelp(args) {
		if _, err := io.WriteString(output, runtimeUsage); err != nil {
			return options, err
		}
		return options, flag.ErrHelp
	}
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	// runtimeUsage already documents every option; the fallback on a genuine
	// parse error prints it once, not a second time as flag.PrintDefaults'
	// own single-dash listing.
	// badFlag below prints this program's own three-line mistake shape;
	flags.Usage = func() {} // the flag package's own per-error usage call must print nothing
	flags.BoolVar(&options.xpc, "xpc", false, "use macOS LaunchAgent Mach services; with --isolated derive a development namespace; requires launchd registration")
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
	flags.BoolVar(&options.executeDownloads, "jobs-run-downloads", false, "execute accepted HTTP(S) downloads inside the service-owned jobs root")
	flags.StringVar(&options.downloadBackend, "jobs-download-backend", "", "download provider: native or nas (managed jobs only)")
	flags.StringVar(&options.downloadNASRoot, "jobs-download-nas-root", "", "override the nas_store configuration setting for the NAS download provider")
	flags.StringVar(&options.gateway, "gateway", "", "open the inference gateway window on 127.0.0.1:<port> for programs holding a local key (default: closed)")
	flags.StringVar(&options.operatorProgram, "operator-program", "", "with --isolated and --state-dir only: also admit this executable as an operator, such as a Panel built from source; the installed runtime refuses it")
	if err := flags.Parse(args); err != nil {
		return options, badFlag(flags, output, "serve runtime", runtimeUsage, args, err)
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
	if options.downloadBackend != downloadBackendNAS || options.downloadNASRoot != "" {
		if _, _, err := normalizeDownloadProvider(options.downloadBackend, options.downloadNASRoot); err != nil {
			return options, err
		}
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
	if err := validateXPCOptions(options, runtime.GOOS); err != nil {
		return options, err
	}
	if options.isolated != "" {
		derive := options.bootstrapEndpoint
		if options.xpc {
			derive = options.defaultEndpoint
		}
		if err := isolateRuntime(&options, derive); err != nil {
			return options, err
		}
	} else if err := completeSelection(options); err != nil {
		return options, err
	}
	if err := checkRuntimeTransport(options); err != nil {
		return options, err
	}
	if _, err := isolatedOperatorProgramWithState(options, false); err != nil {
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

// isolateRuntime derives every unset core listener from one private name.
// Socket names preserve their -runtime/-logging/-config/-jobs/-model suffixes.
// XPC uses canonical service names beneath the bootstrap owner's isolated prefix.
func isolateRuntime(options *runtimeFlags, derive func(string) (string, error)) error {
	name := options.isolated
	valid := len(name) > 0 && len(name) <= 64
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
		value      *string
		service    string
		xpcService string
		enabled    bool
	}{
		{&options.endpoint, "runtime", "runtime-v1", true}, {&options.logEndpoint, "logging", "logging-v1", true}, {&options.configEndpoint, "config", "config-v1", true},
		{&options.jobEndpoint, "jobs", "job-acceptance-v1", !options.withoutJobs}, {&options.modelEndpoint, "model", "model-v1", !options.withoutModels},
	} {
		if *item.value != "" || !item.enabled {
			continue
		}
		service := name + "-" + item.service
		if options.xpc {
			service = item.xpcService
		}
		endpoint, err := derive(service)
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
	//unchecked: describeIsolated has no return value to report a write failure through
	fmt.Fprintf(w, "runtime: isolated runtime %q is listening\n", options.isolated)
	//unchecked: describeIsolated has no return value to report a write failure through
	fmt.Fprintf(w, "  ABSTRACTION_RUNTIME_ENDPOINT=%s\n", options.endpoint)
	for _, item := range []endpointFlag{
		{"logging", options.logEndpoint}, {"config", options.configEndpoint}, {"jobs", options.jobEndpoint},
		{"model", options.modelEndpoint}, {"state", options.stateDir}, {"log file", options.out},
	} {
		if item.value != "" {
			//unchecked: describeIsolated has no return value to report a write failure through
			fmt.Fprintf(w, "  %-8s %s\n", item.flag, item.value)
		}
	}
}

func serveRuntime(args []string) error {
	// Requested help (--help, help) prints to stdout and exits 0; parseRuntime's
	// own leading isHelp check would also catch this, but its output goes to
	// whichever writer this call passes it, which flags.Usage on a genuine
	// parse error also uses (stderr) below.
	if len(args) > 0 && isHelp(args[0]) {
		_, err := io.WriteString(os.Stdout, runtimeUsage)
		return err
	}
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

// runtimeModelEndpoint is the one name of the model listener: the explicit
// --model-endpoint, else the runtime namespace's model companion.
func runtimeModelEndpoint(options runtimeFlags) (string, error) {
	if options.modelEndpoint != "" {
		return options.modelEndpoint, nil
	}
	return options.companionEndpoint("model-v1", "", "model")
}

func runRuntimeReady(ctx context.Context, options runtimeFlags, ready func() error) error {
	var receive func(string) error
	if ready != nil {
		receive = func(string) error { return ready() }
	}
	return runRuntimeReadyAddress(ctx, options, receive)
}

// runRuntimeReadyAddress is the same startup owner with the actual gateway
// listener address available to private in-process readiness receiving.
func runRuntimeReadyAddress(ctx context.Context, options runtimeFlags, ready func(string) error) error {
	ctx, cancelRuntime := context.WithCancel(ctx)
	defer cancelRuntime()
	releaseSettings, err := runtimeSettingsGuard(ctx, options)
	if err != nil {
		return err
	}
	defer releaseSettings()
	// The instance claim comes before retained logging and every listener: a
	// runtime that loses it opens and binds nothing (RUN-R11).
	releaseClaim, err := claimRuntime(ctx, options)
	if err != nil {
		return err
	}
	defer releaseClaim()
	options.privateState = options.stateDir != ""
	speakRuntime(options)
	useHostLogLines()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkRuntimeTransport(options); err != nil {
		return err
	}
	releaseAppAdmission, err := configureRuntimeAppSandbox(options)
	if err != nil {
		return err
	}
	defer func() {
		if err := releaseAppAdmission(); err != nil {
			complain("runtime app admission cleanup:", err)
		}
	}()
	managed := !options.withoutJobs && options.jobRoot == ""
	if managed {
		state := options.stateDir
		if state == "" {
			var err error
			state, err = defaultRuntimeStateDir()
			if err != nil {
				return err
			}
		}
		if !filepath.IsAbs(state) {
			return fmt.Errorf("runtime: absolute state directory required")
		}
		options.jobRoot = modelplace.JobStoreIn(state)
		options.executeDownloads = true
		// The download service checks the person's download archive, which
		// the Panel sets, before the network, resumes the copies queued for
		// it, and hashes the archive's plain-named files in the background
		// [DL-R44].
		if dir, err := archive.StateFor(options.stateDir); err != nil {
			complain("runtime: download archive:", err)
		} else {
			archive.UseState(dir)
			archive.ResumeWrites()
			archive.StartInventory()
		}
	}
	sink, closeSink, err := runtimeLogSink(options.out)
	if err := composeCollision("runtime logging:", err); err != nil {
		return err
	}
	if closeSink != nil {
		defer closeSink.Close()
	}
	var executor acceptanceprovider.Executor
	var restartState *configservice.RestartState
	report := runtimeReporter(os.Stderr)
	// A managed runtime always executes downloads (set above).
	if options.executeDownloads {
		if executor, err = configuredDownloadExecutor(options, report); err != nil {
			return fmt.Errorf("runtime jobs: %w", err)
		}
		if nas, ok := executor.(*runtimeNASExecutor); ok {
			restartState = new(configservice.RestartState)
			nas.admit = func(selected string) error { return admitRuntimeNASStore(restartState, selected) }
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
		if options.endpoint != "" {
			if options.modelEndpoint, err = runtimeModelEndpoint(options); err != nil {
				return err
			}
		}
	}
	rightsState, err := composeRights(options)
	defer rightsState.close()
	if err := composeCollision("runtime rights:", err); err != nil {
		return err
	}
	credentials, err := composeCredentials(options, rightsState, report)
	// The deferred closes unwind composed applications, inference and credentials
	// on return. host.Listen also closes hosts it has taken over when it fails;
	// these deferred closes still run.
	defer credentials.close()
	if err := composeCollision("runtime credentials:", err); err != nil {
		return err
	}
	executor = credentials.executor(executor)
	hostOptions := host.Options{Endpoint: options.endpoint, LogEndpoint: options.logEndpoint, ConfigEndpoint: options.configEndpoint, Sink: sink,
		ModelRegistry: registry, ModelEndpoint: options.modelEndpoint,
		JobRoot: options.jobRoot, JobOwner: options.jobOwner, JobEndpoint: options.jobEndpoint,
		JobExecutor: executor, ManagedJobs: managed,
		OnError: func(err error) { writeHostLogLine(os.Stderr, "runtime: "+err.Error()) },
	}
	hostOptions.ConfigStore, hostOptions.ConfigUserKey = isolatedConfigStore(options.stateDir)
	// A runtime with its own configuration file reads no machine rung either.
	hostOptions.ConfigWithoutMachine = hostOptions.ConfigStore != nil
	if hostOptions.ConfigStore != nil {
		// The installed runtime's default store gets its observation source
		// from abstraction-config/go/service.Listen itself; a state-dir-scoped
		// store is this process's own file, so this composition supplies one.
		hostOptions.ConfigObservationSource = configservice.FileObservation(hostOptions.ConfigUserKey)
	}
	credentials.configure(&hostOptions)
	selfLogs, err := runtimeAttributedLogWriter(sink)
	if err != nil {
		return fmt.Errorf("runtime self logging: %w", err)
	}
	inference, err := composeInference(options, credentials, selfLogs, report)
	defer inference.close()
	if err := composeCollision("runtime inference:", err); err != nil {
		return err
	}
	executor = composeInferenceJobs(executor, inference, credentials, report)
	hostOptions.JobExecutor = executor
	routerEndpoint, err := runtimeRouterEndpoint(options)
	if err != nil {
		return err
	}
	resourceEndpoint, err := resourceTableEndpoint(options)
	if err != nil {
		return err
	}
	inference.configure(&hostOptions, routerEndpoint, resourceEndpoint)
	questions, err := composeAsks(options, rightsState, report)
	if err := composeCollision("runtime asks:", err); err != nil {
		return err
	}
	questions.configure(&hostOptions)
	inference.configureStorageRemoval(&hostOptions, questions)
	questions.useLogs(selfLogs)
	rightsState.useLogs(selfLogs)
	questions.useTierFacts(inference.hosts(), inference.managedHost(), registries)
	// The protected Security-log path (logging LOG-N1, facade FAC-H2) exists
	// only beside a trusted machine-wide helper; elsewhere the denial stays.
	protectedSources := composeProtected(options, questions, protected.OwnImage)
	if protectedSources != nil {
		hostOptions.LogProtectedSources = protectedSources
	}
	rightsState.requireApplicationRoles()
	applications, applicationsEndpoint, appErr := composeApplications(options, rightsState, report)
	if appErr == nil && applications != nil {
		if err := applications.configureMessageAudit(selfLogs); err != nil {
			applications.Close()
			applications, appErr = nil, err
		}
	}
	if err := composeCollision("runtime applications:", appErr); err != nil {
		return err
	}
	if applications != nil {
		defer applications.Close()
		applications.messageNotices = questions
		if err := composeCollision("runtime native applications:", enableApplicationV3(ctx, applications, rightsState, questions, sink)); err != nil {
			return err
		}
	}
	configureApplications(&hostOptions, applications, applicationsEndpoint)
	if inference != nil {
		bindSetupCheck(applications, hostOptions.Router, hostOptions.ResourceTable, hostOptions.ResourceLeases, inference.operator)
	}
	if applications != nil {
		// A program that registered itself is asked about in the native
		// prompt (runtime_self_registered.go).
		var review *shippedProgramReview
		if inference != nil {
			review = inference.programReview
		}
		questions.keepSelfRegisteredWithProgramReview(ctx, applications.directory, review)
		// The person's Set up answer keeps the apps Setup's question named.
		applications.setup.resumeSetupKeep(ctx, applications.directory, report)
		if inference != nil && inference.providers != nil && applications.v3 != nil {
			inference.providers.readyRegistrationDirectory.Store(applications.directory)
		}
	}
	stopApplicationsLogon := startApplicationsLogon(ctx, options, applications, reconcileRuntimeLogon, report)
	defer stopApplicationsLogon()
	composedActions := []string{remoteServeAction, host.ConfigKeyReadAction}
	if hostOptions.JobRoot != "" {
		composedActions = append(composedActions, jobapi.ResourceActions...)
	}
	gateByRights(&hostOptions, rightsState.decider())
	configureRuntimeTypedConfig(&hostOptions, applications, rightsState, options.out)
	hostOptions.ConfigTyped.RestartState = restartState
	configureRuntimeMessageStartSettings(applications, options)
	gateJobStanding(&hostOptions, rightsState)
	rightsState.configure(&hostOptions, composedActions)
	if rightsState != nil {
		if err := rightsState.install(hostOptions.RightsActions, installationRules(hostOptions, registries)); err != nil {
			complain("runtime rights:", err)
		}
		if err := rightsState.installStorageRemovalRules(hostOptions); err != nil {
			complain("runtime storage removal:", err)
		}
		// A declaration that names a card resource loads weights into it
		// itself, so its own program holds the resource; the operators' rules
		// are not its rules. This covers the installation's and a product's
		// declarations, already read once the runtime opens, and a provider
		// declared while the runtime is running gets the rule when its
		// declaration lands, not at the next start.
		// An inventory source's declaration names the stores it reads; its
		// own program provides them. The installation's local-stores
		// declaration reaches the runtime only this way.
		if inference != nil && inference.providers != nil {
			for _, written := range []func(){
				writeCardHoldRules(inference.operator, inference.providers),
				writeStoreRules(inference.operator, inference.providers),
			} {
				written()
				inference.providers.Watch(written)
			}
		}
	}
	// Catalogue lookup and receiving policy are configured above; the exact
	// action is registered before existing kept-provider grants are reconciled.
	if applications != nil && applications.directory != nil {
		if err := applications.directory.reconcileProviderConfigGrants(ctx); err != nil {
			report(fmt.Errorf("config provider key grants: %w", err))
		}
		// A kept integration's hold rules, written once its Keep lands, are
		// written again here for a Keep given before they existed.
		if hostOptions.ResourceLeases != nil {
			applications.directory.writeKeptHolds(ctx)
		}
	}
	if options.xpc {
		if err := xpcHostEndpoints(&hostOptions, options.defaultEndpoint); err != nil {
			return err
		}
	}
	if inference != nil && inference.providers != nil {
		inference.providers.setChildEnvironment(providerEnvironment(options, hostOptions))
	}
	// MachineControl is composed beside the native registry. Its explicit
	// receiving preparation owns paired listeners; startup creates no default.
	pairing, err := composeRuntimePairing(ctx, options, questions, inference, sink, report, applications)
	if err != nil {
		report(fmt.Errorf("runtime pairing unavailable: %w", err))
	} else {
		defer pairing.Close()
	}
	remoteListener, err := prepareRuntimeRemoteHost(ctx, options, rightsState, inference, sink, report)
	if err != nil {
		return err
	}
	runtime, err := host.Listen(hostOptions)
	if err != nil {
		return err
	}
	defer runtime.Close()
	var copyProviders *runtimeProviders
	if inference != nil {
		copyProviders = inference.providers
	}
	stopCopyChecks := startRuntimeSetupCopies(ctx, options, applications, copyProviders, report, selfLogs)
	defer stopCopyChecks()
	remoteFailure := make(chan error, 1)
	stopRemote, err := remoteListener.start(ctx, func(err error) {
		if errors.Is(err, errRemoteExposure) {
			report(err)
			return
		}
		remoteFailure <- err
		cancelRuntime()
	}, func(network, address string) (net.Listener, error) { return listenRuntimeRemote(ctx, network, address) })
	if err != nil {
		if !errors.Is(err, errRemoteExposure) {
			return err
		}
		report(err)
		stopRemote = func() {}
	}
	defer stopRemote()
	stopLegacy := serveLegacyCompatibility(ctx, options, report)
	defer stopLegacy()
	// Pending typed requests follow rights changes while the runtime serves;
	// deferred after runtime.Close, the stop runs first, so no settling pass
	// edits the Book once the asks host starts closing (ASK-A13).
	defer questions.followRights(ctx)()
	if err := ctx.Err(); err != nil {
		return err
	}
	// This runtime now owns its endpoints and serves nothing yet: the previous
	// runtime's pending system-access questions go before any request is
	// accepted (asks ASK-S1). A runtime that failed to bind returned above.
	if err := questions.withdrawStale(); err != nil {
		return err
	}
	if pairing != nil {
		if err := pairing.restoreNormal(); err != nil {
			report(err)
		}
	}
	retentionCtx, stopRetention := context.WithCancel(ctx)
	waitRetention := inference.serveInputRetention(retentionCtx)
	if fileSink, ok := sink.(*logging.FileSink); ok {
		joinSettings := followRuntimeLogSettings(retentionCtx, options, fileSink, report)
		joinLogs := fileSink.MaintainRetention(retentionCtx, func(err error) {
			writeHostLogLine(os.Stderr, "runtime: log retention: "+err.Error())
		})
		defer func() { stopRetention(); joinSettings(); joinLogs() }()
	}
	defer waitRetention()
	defer stopRetention()
	if options.isolated != "" {
		describeIsolated(os.Stderr, options)
	}
	stopProtected := protectedSources.serve(ctx, report)
	defer stopProtected()
	// Adoption can give a declaration its canonical generation and a new
	// binding ID. It completes, and the router reads the adopted hosts,
	// before the runtime reports ready, so no early request carries the
	// replaced binding. It follows withdrawStale: questions it opens stay.
	if inference != nil && inference.providers != nil {
		if err := inference.providers.adoptAtStartup(ctx); err != nil {
			return err
		}
	}
	if ready != nil {
		address := ""
		if inference != nil {
			address = inference.gateway.boundAddress()
		}
		if err := ready(address); err != nil {
			return err
		}
	}
	err = runtime.Serve(ctx)
	select {
	case failure := <-remoteFailure:
		return failure
	default:
		return err
	}
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
	return managedJobRootFrom(state, func() (string, error) {
		return runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	}, func() string { return modelplace.DefaultJobStore(os.Getenv) })
}

func managedJobRootFrom(state string, stateDirectory func() (string, error), defaultStore func() string) (string, error) {
	if state == "" {
		var err error
		if state, err = stateDirectory(); err != nil {
			return "", err
		}
		if root := defaultStore(); root != "" {
			return root, nil
		}
	}
	if !filepath.IsAbs(state) {
		return "", fmt.Errorf("runtime: absolute state directory required")
	}
	return modelplace.JobStoreIn(state), nil
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

// Durable work survives user cache cleanup; config.DataDirFor owns the
// per-user data folder and modelplace.RuntimeStateFor the runtime's folder
// in it, beside its job store.
func defaultRuntimeStateDir() (string, error) {
	return runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
}

func runtimeStateDir(platform string, getenv func(string) string, home func() (string, error)) (string, error) {
	return modelplace.RuntimeStateFor(platform, getenv, home)
}

// runtimeLogPath resolves only metadata; selecting a guard must not open a log.
func runtimeLogPath(path string) (string, error) {
	if path == "" {
		return logservice.DefaultRecordsPath()
	}
	return path, nil
}

func runtimeLogSink(path string) (logging.Sink, io.Closer, error) {
	path, err := runtimeLogPath(path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	sink, err := logging.OpenDefaultRetainedFileSink(path)
	if err != nil {
		return nil, nil, err
	}
	return sink, sink, nil
}

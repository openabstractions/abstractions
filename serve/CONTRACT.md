# OpenAbstractions runtime contract

Binds: none (composition of the services' own contracts)

This page is the contract of the `openabstractions` runtime: the process that
hosts resolution, logging, configuration, durable jobs and the composed
services on one account, and the commands that start it, supervise it and
report its readiness. Each composed service keeps its own contract; this page
owns what the runtime adds: activation per platform, the supervised READY/EOF
protocol, endpoint derivation and `--isolated`, operator admission, the rules
the runtime writes at start, the environment its providers receive,
composition and readiness, upgrade and downgrade, and the command-line
vocabulary with its exit codes. Every call between the runtime and its
callers uses the transport of the abstraction-ipc contract, whose rules
this page cites as IPC-*.
[HISTORY.md](HISTORY.md) keeps what this page replaced.

Trust domain: one account on one machine. The runtime serves the processes
the platform proves belong to its account; it decides every call as the
caller the transport bound (IPC-P1), and it admits operator calls only from
its operator programs (`RUN-A1`). A remote runtime is another trust domain,
reached only through the remote binding (IPC-R1).

## Reading this page

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT", "SHOULD",
"SHOULD NOT", "RECOMMENDED", "MAY" and "OPTIONAL" in this page are to be
interpreted as described in RFC 2119 and RFC 8174, when, and only when, they
appear in all capitals, as shown here.

A rule id such as `RUN-S1` is declared once, in bold brackets before its
title, at the head of the rule it names; tests cite it the same way. A
retired id is never reused; [HISTORY.md](HISTORY.md) keeps it with the
release it left. Each rule ends with one of two marks. **Held by** names the
test or check that goes red without the rule. **Untestable here** gives the
reason no test holds it yet and names the package that brings its scenario.

| letter | topic |
|---|---|
| A | admission: operators, and the caller of each command (S12's reserved meaning) |
| E | error and outcome: exit codes (S12's reserved meaning) |
| C | the command line: help, mistakes and output |
| N | endpoints: their form, client selection and `--isolated` |
| R | `serve runtime`: services, state and composition |
| S | supervision: the READY/EOF protocol and the Windows supervisor |
| T | `start`: activation per platform |
| Q | `status`: readiness |
| I | the rules the runtime writes at start |
| P | the provider environment |
| U | upgrade and downgrade |

`X` (extension) is reserved and unused here.

## Command line index

Every flag and exit code that `openabstractions serve runtime --help`,
`openabstractions start --help` and `openabstractions status --help` print
has one row here, naming the rule that states it. The `help` column says
whether the command's help prints the item today: `printed`, or `not
printed` for an item the command accepts or returns without its help saying
so. `scripts/unit_contracts.py` fails when a printed flag or exit code
has no row, when a `printed` row is no longer printed, or when a `not
printed` row starts being printed.

| command | item | rule | help |
|---|---|---|---|
| serve runtime | `--endpoint` | `RUN-N1` | printed |
| serve runtime | `--log-endpoint` | `RUN-N1` | printed |
| serve runtime | `--config-endpoint` | `RUN-N1` | printed |
| serve runtime | `--jobs-endpoint` | `RUN-N1` | printed |
| serve runtime | `--model-endpoint` | `RUN-N4` | printed |
| serve runtime | `--isolated` | `RUN-N5` | printed |
| serve runtime | `--xpc` | `RUN-N6` | printed |
| serve runtime | `--state-dir` | `RUN-R3` | printed |
| serve runtime | `--out` | `RUN-R3` | printed |
| serve runtime | `--without-jobs` | `RUN-R2` | printed |
| serve runtime | `--without-models` | `RUN-R2` | printed |
| serve runtime | `--jobs-root` | `RUN-R4` | printed |
| serve runtime | `--jobs-owner` | `RUN-R4` | printed |
| serve runtime | `--jobs-run-downloads` | `RUN-R4` | printed |
| serve runtime | `--jobs-download-backend` | `RUN-R4` | printed |
| serve runtime | `--jobs-download-nas-root` | `RUN-R4` | printed |
| serve runtime | `--gateway` | `RUN-R5` | printed |
| serve runtime | `--operator-program` | `RUN-A1` | printed |
| serve runtime | `--supervised` | `RUN-S1` | printed |
| serve runtime | exit 0 | `RUN-E1` | printed |
| serve runtime | exit 1 | `RUN-E1` | printed |
| serve runtime | exit 2 | `RUN-E1` | printed |
| serve runtime | exit 4 | `RUN-R6` | printed |
| start | `--timeout` | `RUN-T2` | printed |
| start | `--require-unelevated` | `RUN-T3` | not printed |
| start | exit 0 | `RUN-T2` | printed |
| start | exit 1 | `RUN-E1` | printed |
| start | exit 2 | `RUN-E1` | printed |
| start | exit 3 | `RUN-U1` | printed |
| start | exit 4 | `RUN-R6` | printed |
| status | `--endpoint` | `RUN-N3` | printed |
| status | `--timeout` | `RUN-Q2` | printed |
| status | `--json` | `RUN-Q3` | printed |
| status | exit 0 | `RUN-Q2` | printed |
| status | exit 1 | `RUN-Q2` | printed |
| status | exit 2 | `RUN-E1` | printed |

## The command line

**[RUN-C1] Help is a request.** A command MUST treat `--help`, `-h` or `help`
as its first argument as a request for its own help page. It prints the page
to standard output, changes nothing, and exits 0. Held by:
`serve/runtime_test.go` (`TestServeCommandHelpAndEndpointRefusal`),
`serve/main_test.go` (`TestServeHelpIsItsOwnScopedPage`),
`serve/status_test.go` (`TestRuntimeStatusHelpAndBadFlag`) and
`serve/start_test.go` (`TestStartFlagsDoNotActivate`). Help after other
arguments differs between commands (Divergences).

**[RUN-C2] A help page states the whole command.** A help page's first line
is `Usage: openabstractions <command> [options]`. The page lists every flag
the command accepts, each on its own line indented by two spaces, and states the
command's exit codes in one paragraph that begins `Exit codes:`. A flag kept
only for an installer's existing command line MAY stay unlisted, and this
page lists it as `not printed`. Held by: `scripts/unit_contracts.py`
(the command line index) and `serve/status_test.go`
(`TestStatusHelpNamesExitCodes`).

**[RUN-C3] A flag mistake has three lines.** An unknown flag, a value its
flag's type cannot parse, or an unexpected argument to `start` or `status`
MUST print three lines to standard error: the problem, the command's usage
line, and `Run "openabstractions <command> --help" for the flags.` The
command then exits 2 and has changed nothing. `serve runtime` refuses an
unexpected argument and a value its own checks refuse with one line and exit
1 (`RUN-E1`, Divergences).
Held by: `serve/main_test.go` (`TestBadFlagShapeAcrossCommands`,
`TestTakesNoArgumentsIsAThreeLineMistake`,
`TestMalformedValuesAreSentencesNotRawErrors`) and `serve/status_test.go`
(`TestStatusSplitsUnexpectedArgumentFromBadTimeout`).

**[RUN-C4] A command mistake has two lines.** An unknown or missing command
MUST print the problem and `Run "openabstractions --help" for the commands.`
(or the group's own help command) to standard error and exit 2. A bare
command group is a mistake. Held by: `serve/main_test.go`
(`TestMainMistakesNameThemselvesAndPointAtHelp`,
`TestBareCommandGroupsAreMistakesNotHelp`, `TestCapabilityUsageErrorsExitTwo`).

**[RUN-C5] Output and diagnostics.** A command writes its result to standard
output and every diagnostic to standard error. `--json` replaces the result
with one JSON document and changes no diagnostic. Untestable here, as one
rule over every command: CQ-6 records each command's output in the CQ-0 CLI
corpus, and that corpus holds the rule byte for byte.

**[RUN-C6] One command-line vocabulary.** The help test, the mistake shapes
of `RUN-C3` and `RUN-C4`, the JSON writer and every exit-code constant MUST
have one definition in the runtime's source, which every command uses.
Untestable here: the helpers live in several files today (Divergences); CQ-6
moves them into one file and records the table in the CQ-0 corpus.

## Exit codes

**[RUN-E1] The codes of the runtime, `start` and `status`.** These three
commands MUST exit with these codes and no others:

| code | `serve runtime` | `start` | `status` |
|---|---|---|---|
| 0 | a supervised runtime whose parent closed its pipe (`RUN-S1`) | the installation's runtime answers ready | every capability of the roster resolved |
| 1 | an unexpected argument, a value the runtime's own checks refuse (an endpoint form, an isolated name, a gateway port), or a startup failure | any other failure, the end of `--timeout` included | no runtime answered, or a capability is unavailable or refused |
| 2 | an unknown flag or a value its flag's type cannot parse (`RUN-C3`) | a flag or command mistake | a flag or command mistake |
| 3 | — | an installer holds the upgrade exclusion (`RUN-U1`) | — |
| 4 | this process sees a packaged app's private copy of AppData (`RUN-R6`) | the same refusal for the caller | — |

The SDK's on-demand activation reads `start`'s 3 and 4. Held by:
`serve/main_test.go` (`TestBadFlagShapeAcrossCommands`),
`serve/start_test.go` (`TestStartUpgradeRefusalKeepsExitStatus`),
`serve/profile_test.go` (`TestVirtualizedProfileRefusesWithExitFour`) and
`scripts/unit_contracts.py` for the codes each help page prints.

**[RUN-E2] One meaning per code across the binary.** A code MUST mean one
thing in every command of `openabstractions`. Untestable here: today 4 reads
"unavailable" in `jobs`, "conflict" in `jobs migrate` and "a packaged app's
private AppData" in `start` and `serve runtime`, and 3 and 5 differ the same
way (Divergences). CQ-6 writes the one table into the CQ-0 corpus, and the
download (K1) and job (X5) final forms take it.

## Endpoints

For the 0.8.0 namespace migration, canonical Windows names contain the native
process-token SID. Generic Unix sockets use an owned ordinary 0700 namespace
keyed by native UID; invalid supplied XDG ownership/mode/links refuse without
fallback. Installed macOS retains its fixed XPC convention. Client naming never
creates a missing directory. A string-returning compatibility default helper
returns empty on resolution refusal; actual operations preserve a useful error
before I/O and never fall back to an older endpoint.

For one train, safe old-name aliases answer only an existing-codec typed refusal,
including endpoint Describe, with no capability forwarding or store access.
A busy Windows legacy name is reported and leaves the new per-SID runtime usable.
Old flat `/tmp` aliases remain unbound because their parent is not private;
the runtime reports unavailable legacy compatibility. It never tightens `/tmp`,
replaces another listener or adopts a foreign socket/lock. Owned XDG aliases may
bind. This compatibility limit does not qualify old installed clients or sandbox
and two-account receiving by itself.

### Canonical naming and local admission

Bootstrap is the naming owner (ST-5's accepted account namespace). A service
token is nonempty ASCII `[a-z0-9-]+`. The core default tokens are `runtime-v1`
(resolver), `logging-v1`, `config-v1`, `job-acceptance-v1` and `model-v1`.
The following spellings are exact; `SID` is the canonical native process-token
SID and `UID` is the native effective UID in decimal. Environment claims such
as `USER` do not supply either identity. Mixed real/effective Unix UIDs refuse.

| Selection | Full endpoint |
|---|---|
| Windows service `S` | `\\.\pipe\openabstractions-user-SID-S` |
| Generic Unix with `XDG_RUNTIME_DIR=X` | `X/openabstractions/S.sock` |
| Generic Unix without that variable | `<os.TempDir()>/openabstractions-UID/S.sock` |
| Installed macOS service `S` | `xpc:com.openabstractions.S` |
| Isolated macOS XPC name `N`, service `S` | `xpc:com.openabstractions.isolated.N.S` |

For example, SID `S-1-5-21-1-2-3-1001` and token `runtime-v1` name
`\\.\pipe\openabstractions-user-S-1-5-21-1-2-3-1001-runtime-v1`.
UID 1001 with no XDG variable and temporary root `/tmp` names
`/tmp/openabstractions-1001/runtime-v1.sock`; XDG root `/run/user/1001`
names `/run/user/1001/openabstractions/runtime-v1.sock`.
Installed XPC uses the published service-token roster in
bootstrap's XPC owner (`../openabstractions-flat/abstraction-facade/go-core/bootstrap/endpoint_xpc.go`).
The resolver is `runtime-v1`, including inside the isolated XPC prefix.

An explicitly supplied `XDG_RUNTIME_DIR` must be absolute and already be an
ordinary directory owned by the native account with mode 0700. A missing or
invalid supplied directory refuses without fallback. Its `openabstractions`
child, or the UID-named temporary namespace when XDG is absent, may be missing:
client naming returns its path without creating it. An existing namespace must
itself pass the same owner/ordinary-directory/0700 check. These rules do not
require the temporary root or every ancestor to have mode 0700. The listener
uses identity's private-directory owner (`../openabstractions-flat/abstraction-identity/CONTRACT.md`)
to create a missing namespace and check an existing one; it does not repair an
invalid existing namespace. It refuses foreign or nonordinary lock/socket
objects without adopting them. Naming and listening have separate failure
points; successful naming of a missing namespace does not promise a listener.
The named-object checks belong to
layout.RuntimeEndpointRoot (`../openabstractions-flat/abstraction-storage/go/layout/runtime_endpoint_unix.go`)
using identity ID-D1: the supplied XDG root and final namespace are each
checked as named objects. Intermediate ancestors follow normal OS path resolution and are
not independently attested for links by these clauses. This supplies no
authority to an ancestor or substitute object; the actual named-root and
lock/socket checks and native caller/server admission remain mandatory.

For `--isolated N`, unset generic endpoints use tokens `N-runtime`, `N-logging`,
`N-config`, `N-jobs` and `N-model`. Explicit endpoints remain unchanged.
In particular `--isolated N --endpoint <custom>` with no model option derives
`N-model`; RUN-N4's resolver suffix applies when the model endpoint is still
unset after isolation. Without isolation, any explicit listener requires the
resolver, logging and config endpoints and `--out`. Enabled jobs also require
`--jobs-endpoint` and either `--state-dir` or the explicit
`--jobs-root`/`--jobs-owner` pair. Enabled model lookup may derive its endpoint
from the explicit resolver. Disabled jobs/models require no endpoint for that
service; their conflicting explicit options still refuse. RUN-N1/N2/N4/N5/N6
remain applicable.

Local admission follows identity's trust domain (`../openabstractions-flat/abstraction-identity/CONTRACT.md`)
and ID-K1 (`../openabstractions-flat/abstraction-identity/CONTRACT.md`).
An unconfigured Windows endpoint admits the account alone. Only exact
AppContainer SIDs named by the runtime's own admission configuration receive
the additional client-data/sync ACE; no connection supplies one, and broad
application-package groups refuse. Admission is endpoint-specific and conveys
no OA operation permission. This contract supplies no implicit sandbox
allowlist or container creation. The accepted Flatpak receiving case grants
the client `xdg-run/openabstractions` visibility; its actual sandbox receiving
and the plan's unspecified Snap rule remain separate acceptance work.

The OS normally refuses another account at open. A privileged process that
can open anyway is bound as its actual account and refused by the service's
authorization owner. Account inequality is not an unmet proof level and has
no additional IPC-F transport opcode. IPC-P3 (`../openabstractions-flat/abstraction-ipc/CONTRACT.md`)
governs proof refusal; the service's existing typed authorization refusal
governs an adequately proven caller lacking permission.

The one-train old-name roster and codec association are the finite
[retiredRuntimeAliases owner](legacy_endpoint.go). Its framed aliases use the
old transport naming convention for the published service tokens above and
the additional `router-v1`, `resource-table-v1`, `storage-content-v1`,
`credentials-v1`, `rights-authorization-v1`, `asks-application-v1`,
`applications-v1`, `registry-v1`, `inference-v1`, `inference-remote-v1` and
`lend-v1`. Old `asks`, `rights` and `router` names keep their JSON-line codecs.
On Windows the old form is `\\.\pipe\openabstractions-S`; on Unix it is
`XDG_RUNTIME_DIR/openabstractions-S.sock`, or
`<os.TempDir()>/openabstractions-S-<USER>.sock` without XDG. These names are
compatibility refusals only and supply no identity authority. A valid framed
request, including Describe, receives the existing Endpoint dispatcher's
`unknown_service` ServiceError. Valid JSON-line requests receive their
service's `unknown_operation` response. Malformed requests need no invented
legacy reply. The safe-parent and busy-name limits stated above still apply.
Here legacy `USER` is the `USER` environment variable verbatim, empty when
unset; it is retained solely to find an old refusal alias and is never used
for canonical account naming. The JSON-line refusal objects are the existing
response shapes below, each followed by one line feed:

```json
{"code":"unknown_operation","error":"endpoint renamed"}
```

That object is used by both old `asks` and `rights`. Old `router` retains its
required observation fields even in this refusal:

```json
{"code":"unknown_operation","error":"endpoint renamed","caller":{"bound":false},"took_ms":0,"cache_age_ms":0}
```

Their respective public codec owners are
asks Response (`../openabstractions-flat/abstraction-asks/go/wire.go`),
rights Response (`../openabstractions-flat/abstraction-rights/go/wire.go`) and
router Response (`../openabstractions-flat/abstraction-router/go/wire.go`).

**[RUN-N1] An endpoint is a full platform path.** An endpoint flag MUST name
`\\.\pipe\<name>` on Windows, an absolute socket path elsewhere, or an `xpc:`
service name with `--xpc` on macOS. A bare name is refused with the accepted
form. `--endpoint` is the resolver; `--log-endpoint`, `--config-endpoint`
and `--jobs-endpoint` name the logging, configuration and job listeners in
the same form. Held by: `serve/runtime_test.go`
(`TestRuntimeEndpointRefusesBareName`, `TestRuntimeArguments`).

**[RUN-N2] A runtime is wholly default or wholly named.** A runtime given
some endpoint flags MUST refuse to start unless it is also given every
listener, store and log the others would share with the installed runtime,
or `--isolated`. Held by: `serve/runtime_test.go`
(`TestRuntimePartialSelectionRefused`).

**[RUN-N3] A client's endpoint.** Every command that connects to a runtime
MUST choose its endpoint as `--endpoint` if given, else
`ABSTRACTION_RUNTIME_ENDPOINT` if set, else the installed runtime's
registration. Only the last verifies the server's identity. The variable
selects an endpoint and names no server identity (IPC-T2); a command it
selected says once, on standard error, that it connects unverified. `start`
ignores the variable and waits on its own installation's endpoint
(`RUN-T1`). Held by: `serve/endpoint_test.go`
(`TestEnvironmentEndpointHonoredPerCommand`,
`TestEnvironmentEndpointYieldsToExplicitEndpoint`) and `serve/status_test.go`
(`TestRightsAndStatusFollowTheSameEnvironmentEndpointRule`).

**[RUN-N4] The model listener follows the resolver.** Without
`--model-endpoint`, a runtime given `--endpoint` serves model lookup at that
endpoint followed by `-model`. `--without-models` with `--model-endpoint` is
refused. Held by: `serve/runtime_test.go` (`TestRuntimeArguments`).

**[RUN-N5] `--isolated NAME`.** A name of 1 to 64 characters from `a-z`,
`0-9` and `-` MUST derive every listener left unset: `<name>-runtime`,
`-logging`, `-config`, `-jobs` and `-model`. It requires `--state-dir`, and
the log file defaults to `<state-dir>/logging/records.jsonl`. The runtime
prints the `ABSTRACTION_RUNTIME_ENDPOINT` value its clients use on standard
error once it listens. Held by: `serve/runtime_test.go`
(`TestRuntimeIsolatedDerivesEverySelection`,
`TestRuntimeIsolatedServesDerivedEndpoints`).

**[RUN-N6] `--xpc`.** On macOS `--xpc` serves every listener as a LaunchAgent
Mach service and requires launchd registration. With `--isolated` each
service uses its own development namespace
`com.openabstractions.isolated.<name>.<service-v1>`. A selection that mixes
namespaces, or a custom one, is refused before any state is created. Held by:
`serve/runtime_xpc_test.go` (`TestInstalledXPCOptionsPreventMixedNamespaces`,
`TestIsolatedXPCSelectsEveryRuntimeEndpointInItsOwnNamespace`,
`TestIsolatedXPCRefusesPartialAndCustomSelections`,
`TestUnavailableIsolatedXPCRefusesBeforeState`).

## `serve runtime`

**[RUN-R1] Foreground composition.** `serve runtime` runs in the foreground
and MUST NOT install or register itself with the operating system. It hosts
resolution and the composed services in one process. Untestable here: the
negative half is a property of the code; the installers' own tests hold
registration, and the declared start table of W1-13 lists what the process
composes.

**[RUN-R2] The default services.** A runtime MUST host resolution, logging,
configuration, durable jobs with managed downloads, and model lookup.
`--without-jobs` omits durable jobs and conflicts with every job and download
option. `--without-models` omits model lookup. Held by:
`serve/runtime_test.go` (`TestRuntimeArguments`).

**[RUN-R3] State and log locations.** A runtime without `--state-dir` keeps
its durable state in this account's data folder under `runtime-v1`.
`--state-dir` MUST be absolute; it holds the jobs, a configuration file of its
own with no machine rung, and the isolated runtime's other state. `--out`
names the service-owned log file; its default is this account's cache folder
`openabstractions/logging/records.jsonl`. Held by: `serve/runtime_test.go`
(`TestRuntimeStateLocation`, `TestRuntimeArguments`).

**[RUN-R4] An explicit job store.** `--jobs-root` and `--jobs-owner` MUST be
given together and replace the managed store. `--jobs-run-downloads` runs
accepted anonymous HTTP(S) downloads inside that store.
`--jobs-download-backend` selects `native` or `nas` for managed jobs, and
`--jobs-download-nas-root` overrides the `nas_store` setting the NAS backend
reads at start; a changed store refuses an existing job-provider binding.
Held by: `serve/runtime_test.go` (`TestRuntimeArguments`) and
`serve/runtime_download_test.go`
(`TestRuntimeNASStoreOverridesAndIsolation`,
`TestManagedDownloadExecutorPinsNASSelectionAndRejectsReplacement`).

**[RUN-R5] The inference gateway window.** `--gateway PORT` opens the
inference gateway window on `127.0.0.1:PORT` for programs holding a local
key; without it the window stays closed. Any other address is refused. Held
by: `serve/runtime_inference_gateway_test.go`
(`TestGatewayAddressIsLoopbackOnly`,
`TestRuntimeGatewayWindowServesAKeyedProgram`).

**[RUN-R6] The real profile only.** A runtime on the account's default state,
and `start`, MUST refuse with exit 4 when this process sees a packaged app's
private copy of AppData, or when the profile view cannot be read. A runtime
given `--state-dir` serves only the clients that name it and skips the check.
Held by: `serve/profile_test.go`
(`TestServeRuntimeRefusesAVirtualizedDefaultState`,
`TestVirtualizedProfileRefusesWithExitFour`).

**[RUN-R7] Composition and readiness.** The runtime composes rights,
credentials, inference, questions and applications before its listeners
open, in that order. A step that fails leaves every other service serving.
A step whose listener name or retained log another process holds is a
startup failure: the runtime MUST NOT report ready without that service, and
the supervisor's bounded restart retries it (`RUN-S4`). Held by:
`serve/runtime_instance_test.go`
(`TestRuntimeWithATakenCoreServiceIsNotReady`).
Each failed step MUST register its capability as `not_ready` with a `why` of
the form `compose:<detail>`, and every service it gates answers its typed
`unavailable`. Untestable here: today a failed step writes only standard
error; P0-2 adds the readiness report and its test (rights composition fails
on an unwritable state folder, and `status --json` lists rights, credentials
and every rights-gated service as `not_ready` with that cause), and P1-7
turns the order into a declared graph checked for cycles.

**[RUN-R11] One runtime per state directory.** A runtime MUST take its
state directory's exclusive instance claim before it opens retained logging
or binds any listener, and hold it until it stops. The state directory is
`--state-dir`, or the account's `runtime-v1` without one; a runtime given
explicit listeners and no state directory composes no account state and
takes no claim. A runtime that cannot take the claim within two seconds
opens and binds nothing and fails startup. Held by:
`serve/runtime_instance_test.go`
(`TestConcurrentRuntimesLeaveOneServingEveryCoreService`,
`TestRuntimeLosingTheClaimBindsNothing`).

## Supervision

**[RUN-S1] `--supervised`.** A supervised runtime MUST check that standard
input is a pipe before it creates a file or a listener. End of input requests
cooperative cancellation. Unexpected bytes and read errors fail the child.
Held by: `serve/supervised_test.go`
(`TestSupervisedPipeRequiredBeforeFilesystem`, `TestSupervisedParentClose`,
`TestSupervisedCancellationDoesNotWaitForStdin`).

**[RUN-S2] One readiness line.** A supervised runtime MUST write `READY 1\n`
to standard output once, after every configured listener initialized and
before it serves. A failed start writes no readiness. A failed or short
readiness write closes the initialized listeners and fails startup. The line
reports listener initialization; capability readiness is read through the
resolver (`RUN-Q2`). Held by: `serve/supervised_test.go`
(`TestSupervisedFailedStartHasNoReadiness`,
`TestSupervisedReadinessFailureCleansListeners`,
`TestSupervisedShortReadinessCleansListeners`,
`TestSupervisedStartupFailureEndsTheReadinessWait`).

**[RUN-S3] The parent owns the pipe.** The parent MUST supply a private pipe
and own its only write end, and the child MUST NOT inherit a copy of the
writer. The parent closes its end to stop the child; the parent's death
closes it too. The parent consumes standard output and observes both
readiness and the child's exit. Held by: `serve/supervised_test.go`
(`TestSupervisedParentDeath`).

**[RUN-S4] The Windows supervisor.** `openabstractions serve supervisor` is the
Windows parent. It restarts a failed child after 2, 10 and 30 seconds and
exits with failure on the fourth consecutive failure; a child that ran five
minutes resets the count. Before its first launch it takes the account's
supervisor claim, waiting at most two seconds; when another supervisor holds
it, it exits cleanly without launching. Every start path (`start`, the
service instance, Restart Manager, a login Run value) launches a supervisor,
and the claim makes concurrent launches one. Before each launch it refuses
during an installer upgrade (exit 3) and exits cleanly when another runtime
answers the account's endpoint. It contains the child in a kill-on-close Job Object and
registers for restart with Restart Manager. To stop the child it closes the
pipe and waits 5 seconds for the child to exit, then forces it and waits 2
seconds more; both waits fit inside the 10 seconds the service manager waits
for a stop. Held by: `serve/host_test.go`
(`TestHostRestartsAFailedChildWithBoundedBackoff`,
`TestHostResetsBackoffAfterAHealthyRun`, `TestHostLeavesAServedEndpointAlone`,
`TestHostGuardRefusesBeforeLaunch`), `serve/runtime_instance_test.go`
(`TestConcurrentSupervisorsStartOneRuntime`), `serve/host_windows_test.go`
(`TestHostReplacesAKilledChildAndRegistersForRestart`,
`TestHostServiceRefusesDuringUpgradeAfterRunning`) and
`serve/upgrade_drain_windows_test.go`
(`TestTheJobDrainFitsTheSupervisorsShutdownWait`).

**[RUN-S5] No shutdown endpoint.** The runtime MUST expose no endpoint that
stops it. A runtime stops when its parent closes the pipe, on an interrupt,
or when its service manager stops it. Untestable here: a negative; the
declared start table of W1-13 lists every listener the runtime opens.

## `start`

**[RUN-T1] `start` activates its own installation.** `start` MUST activate
this installation's own registered runtime and wait on that runtime's
endpoint. It never activates or waits on a different runtime. Held by:
`serve/start_test.go` (`TestStartLifecycle`).

**[RUN-T2] Ready means the whole roster.** `start` reports ready, prints
`runtime ready` and exits 0 when every contract of the runtime's baseline
roster resolves. A `not_ready` or `unavailable` answer keeps it waiting
within `--timeout` (default 20 seconds); a refusal ends the wait. Held by:
`serve/start_test.go` (`TestStartReadyRequiresAllRuntimeContracts`,
`TestStartLifecycle`).

**[RUN-T3] An unelevated session.** `start` MUST refuse an elevated Windows
token and a root process elsewhere, before it activates anything.
`--require-unelevated` is accepted for the installer's command line and
changes nothing, since the refusal always applies. Held by:
`serve/start_test.go` (`TestStartLifecycle`).

**[RUN-T4] Activation per platform.** On Windows `start` starts this sign-in's
per-session service instance through the service control manager when one
exists, and otherwise launches the windowless image beside it, detached, as
`serve supervisor`; a caller in a packaged app's profile view launches it
through the desktop shell. The launched runtime outlives the caller: it breaks
away from the caller's job, and when that job forbids breakaway this account's
desktop shell starts it. On macOS it bootstraps or kickstarts the
LaunchAgent `com.openabstractions.runtime` in this user's `gui/<uid>` domain,
from the user's graphical session only. On Linux it starts the user unit
`abstraction-runtime.service`. Another platform refuses. Held by:
`serve/start_windows_test.go` (`TestWindowsActivationUsesOnlyOwnedInstalledPaths`),
`serve/start_job_windows_test.go` (`TestLaunchDetachedOutlivesStarterKillOnCloseJob`),
`serve/start_darwin_test.go` (`TestDarwinActivationUsesRegisteredRuntime`)
and `serve/start_linux_test.go` (`TestLinuxActivationRequiresInstalledUserUnit`).

**[RUN-T5] The presenter is optional.** After the runtime is ready, `start`
on Windows dispatches the installed Panel's background presenter to this
account's desktop when the installation has one. A presenter failure is a
diagnostic and leaves `start`'s result unchanged. Held by:
`serve/start_windows_test.go`
(`TestInstalledPresenterUsesSelectedSiblingAndGuardedShell`).

## `status`

**[RUN-Q1] `status` changes nothing.** `status` queries readiness under the
caller's own authority and MUST change no registration or provider state. It
prints this program's own identity path, canonicalized the way the runtime
compares it with a rights rule's program. Held by: `serve/status_test.go`
(`TestRuntimeStatusUsesLiveResolver`, `TestStatusPrintsThisProgramsOwnIdentity`).

**[RUN-Q2] The roster.** `status` resolves every contract of the runtime's
generated baseline roster within `--timeout` (default five seconds) and exits
0 only when every one resolved. A refusal is reported as refused. Held by:
`serve/status_test.go` (`TestRuntimeStatusPreservesRefusal`,
`TestRuntimeStatusRequiresJobAndEditorContracts`,
`TestRuntimeStatusMissingAndInvalid`).

**[RUN-Q3] `--json`.** `status --json` prints one document with `bootstrap`
(`state`, `detail`), `profile`, `program`, `capabilities` (each `capability`,
`contract`, `status` and, when the resolver answered, `result`) and `error`
when a query failed. Held by: `serve/status_test.go`
(`TestRuntimeStatusUsesLiveResolver`, `TestRuntimeStatusPreservesRefusal`).

## Admission

**[RUN-A1] Operators.** The runtime MUST admit operator calls (answering
questions, editing rules) only from its operator programs: the programs
installed beside it, the Panel among them. `--operator-program PATH` adds one
executable, and only with `--isolated` and `--state-dir`; the installed
runtime refuses the flag. Held by: `serve/runtime_operators_test.go`
(`TestOperatorSiblingsNameTheInstalledPanel`,
`TestIsolatedRuntimeAdmitsItsNamedOperatorProgram`,
`TestOperatorProgramIsRefusedOutsideAnIsolatedRuntime`).

**[RUN-A2] A command decides as its caller.** Every command reaches the
runtime as the program that runs it, and the runtime decides as that bound
caller (IPC-P5). No flag names another subject. Untestable here: a negative
over every command's flags; the CQ-0 CLI corpus lists them.

## Rules written at start

**[RUN-I1] Installation rules.** At start the runtime MUST write its
installation's rules into rights: its operators' grants, the lease rules of
the composed services, and a card-hold rule for each declared provider that
loads weights into a card itself. A provider declared while the runtime runs
gets its rule when its declaration lands. A kept OA integration image, an
entry the person kept whose current image is OA's release pin for it as
recorded in `connect-kept.json` (the person's Connect click, `panel_connect`,
or the installing operator's re-pin to the pin, `installer_update`), gets
hold on `card:0` and `memory` for its program when the record or Keep lands,
and again at start; an existing rule, a deny included, stays, and a
manifest's declarations count for nothing here (owner ruling 2026-11-12,
architect R35; `serve/keep_card_hold_windows_test.go`). Held by: `serve/runtime_leases_test.go` (`TestInstallationWritesTheLeaseRules`)
and `serve/runtime_defaults_test.go`
(`TestSelfRegistrationPermitsOperatorsWithoutOverwriting`).

## The provider environment

**[RUN-P1] A provider reaches this runtime.** A provider the runtime starts
MUST receive the endpoints of this runtime's own services in the variables
each service's client reads, and no runtime endpoint variable this runtime
did not set. A runtime on installed Mach services names none. Every other
variable passes through. Held by: `serve/provider_environment_test.go`
(`TestChildEnvironmentReplacesTheRuntimesVariables`,
`TestAnInstalledRuntimesChildDoesNotInheritARuntimeEndpoint`,
`TestAnXPCRuntimesChildGetsNoEndpointVariables`,
`TestIsolatedXPCChildUsesOnlyItsRuntimeServices`).

## Upgrade and downgrade

**[RUN-U1] The upgrade exclusion.** While an installer holds the upgrade
exclusion, every activation path MUST refuse with exit 3 before it launches
anything: `start`, `serve supervisor` and `service upgrade-check`. Only a
live, trusted exclusion record that covers this installation counts. Held by:
`serve/start_test.go` (`TestStartUpgradeRefusalKeepsExitStatus`) and
`serve/upgradeexclusion_windows_test.go`
(`TestActivationHonoursOnlyLiveCoveringTrustedRecords`).

**[RUN-U2] A downgrade states what it loses.** A runtime release whose state
an earlier release cannot read MUST name the file, the earlier releases that
refuse it and the steps to downgrade, in [HISTORY.md](HISTORY.md).
Untestable here: a statement about earlier builds; the W0-10 fixture records
each format change as compatible, incompatible or one-way.

**[RUN-U3] The upgrade drain.** A stopping runtime MUST close its job
listener first, so a submission from then on reaches the replacement or
fails before acceptance. It then sends a yield request with the reason
`service:stopped` and a deadline 3 seconds away to every job lease it holds
(abstraction-job JOB-R9), and its job executor has those 3 seconds to reach a
checkpoint and give its leases back. At the 3-second bound every lease the
executor still holds lapses. The runtime MUST let every call its job service
admitted before the stop return before it closes the job store, without a
bound: such a call holds no lease and no epoch fences it. The 3-second bound
applies to the executor alone. The replacement acquires each job at the next epoch and
resumes from the last checkpoint, and the store refuses every record write
the stopped runtime still makes at its old epoch. A kind's own file effects,
such as its work folder and output files, stop when the kind honours the
cancellation; a step that ignores it counts as output only what it publishes
inside the fenced record write. A runtime that starts while another still
drains the same store MUST wait up to 5 seconds for that store before its job
service fails its start (`RUN-R7`). The bound fits inside the 5 seconds the
Windows supervisor waits before it forces its child (`RUN-S4`); a lease held
by a forced child lapses at the yield request's deadline.

Scenario. A video job runs under runtime 1 at epoch 4 and last checkpointed
at frame 120. The installer stops runtime 1 and starts runtime 2 on the same
state. Runtime 1 closes its job listener and asks for the lease back by
`t+3s`. The job's current step ignores the stop. At `t+3s` the lease lapses
and runtime 1 closes the store. Runtime 2, waiting for the store since it
started, opens it, acquires the job at epoch 5 and resumes from frame 120.
Runtime 1's step then tries to record the finished video at epoch 4, and the
store refuses it as a stale epoch. The video kind publishes its output inside
that record write. The job completes once, under runtime 2, with one output
and its original receipt.

Held by: `openabstractions-flat/abstraction-facade/go/runtime/upgrade_drain_test.go`
(`TestUpgradeDrainHandsLeasesOverByEpoch`: two runtimes from temporary
folders on one store, the replacement started during the drain, a polite and
a stuck job whose kind publishes its output inside the record write;
`TestUpgradeDrainWaitsForAdmittedCalls`: a Submit inside admission past the
bound keeps the store from a second host until it returns;
`TestARuntimeStartGivesUpOnALiveHost`: a start against a live host fails
after the 5-second wait),
`openabstractions-flat/abstraction-job/go/acceptanceprovider/drain_test.go`
(`TestAStoppingHostYieldsItsLeasesAndTheSuccessorTakesTheNextEpoch`) and
`serve/upgrade_drain_windows_test.go`
(`TestTheJobDrainFitsTheSupervisorsShutdownWait`). The installed upgrade on a
nightly installer is the owner's acceptance case.

## Divergences

- `serve runtime` given `--help` after other arguments prints its help to
  standard error and exits 0; `start` and `status` print it to standard
  output (`RUN-C1`).
- `serve runtime` refuses an unexpected argument and a value its own checks
  refuse (`--isolated`, `--endpoint` and the other endpoint flags,
  `--gateway`) with one line, "openabstractions: <problem>", and exit 1,
  where `RUN-C3` gives other commands three lines and exit 2. CQ-6 gives
  every command one mistake shape.
- `serve runtime --help` prints a stray line, "download provider", under
  `--jobs-download-nas-root`.
- The exit-code constants and the help and mistake helpers live in
  `serve/jobs.go`, `serve/jobs_migrate.go`, `serve/profile.go`,
  `serve/upgrade.go` and `serve/mistake.go` (`RUN-C6`); 4 has three meanings
  across commands (`RUN-E2`).
- Composition failures write only standard error (`RUN-R7`).
- `serve/SUPERVISION.md` still states the supervision rules this page now
  owns; it becomes a pointer here when its next change lands.

## Remote listener

**[RUN-R8] Explicit receiving configuration.** The remote listener is off by
default. The receiving runtime's private pairing/configuration supplier owns its
address, mutual-TLS credentials and current exact-key admission policy. A missing
supplier opens no remote socket. An enabled supplier with unavailable pairing,
TLS or Rights MUST refuse before binding. Outgoing provider registrations supply
client trust only. The pairing UI/store remains an integration prerequisite.
**Held by:** `TestRemoteRuntimeOffAndInvalidConfigurationNeverListen`.

**[RUN-R9] Remote dispatch.** Each handshake checks the live paired key. Every
policy evaluation maps that key through identity's `SubjectOf` to the receiving
account's exact remote subject. Generated `ServeEndpointRemote` owns validated
contract/method decoding and profile admission. Local or unknown profiles refuse
`unsupported`/`remote:local_only`; person profiles refuse
`forbidden`/`remote:person_only`. `abstraction.facade/remote.serve` on `account`
guards offered access, including Describe. It grants no listener configuration
or startup authority. The initial offered methods are endpoint Describe, chat
Start/Observe/Cancel, embed Embed and router Models/Hosts. Describe lists only
the explicitly supplied chat@1, embed@1 and router@1 services, including an
unready supplied service with its readiness reason under
FAC-B1 (`../openabstractions-flat/abstraction-facade/CONTRACT.md`).
Each actual handler retains its existing resource/action or operation-ownership
guard. Messages is composed separately with its explicit peer-scoped methods
and existing per-call permission checks; job and download delegation remain
unserved by this initial inference offer.

Admission follows these existing owners in order. Refusal stops the call at
that stage; typed arguments are still validated before the actual handler
(`IPC-L1`). Unknown method metadata has the local profile under `A-RP1`.

| Stage | Owner and refusal |
|---|---|
| Live peer and configured minimum | Current exact-pin `SubjectOf` and receiving configuration; unavailable evidence refuses. |
| Closed envelope and service lookup | Generated envelope decoder retains its version/decode errors; absent selected service is `unknown_service`. |
| Method profile | Local or unknown, including unknown method metadata, is `unsupported`/`remote:local_only`; person is `forbidden`/`remote:person_only`. Endpoint methods other than Describe are `unknown_method`. |
| Explicit offer | A selected method outside the initial offered set is `unsupported`/`remote:local_only`. |
| Offered access | Require `abstraction.facade/remote.serve` on `account`, including before Describe roster disclosure. |
| Typed arguments and handler | Generated argument decoding retains its errors; the capability owns resource/action and operation-owner refusal. |

For chat, operation IDs and receiving account/program ownership follow
INF-Q1 through INF-Q5 (`../openabstractions-flat/abstraction-inference/CONTRACT.md`).
Each ingress checks offered access. Accepted operation standing authority,
revocation and rechecks follow that contract's `INF-A6`; connection departure
and stopping an observation retain the service-owned lifetime (`IPC-D5`).
**Held by:** `TestRemoteRuntimeDescribeUsesLiveGrantAndExplicitServices` and
`TestRemoteProviderStartAndEmbedKeepResolvedPolicyFence`, plus
`serve/messages_remote_windows_test.go` (`TestRemoteSendAsksReceivingPerson`,
`TestMessagesRemoteLeafKeepsBodyAndNativeOwnershipLocal`).

**[RUN-R10] Bounds and receiving lifetime.** Incoming frames are bounded at
1 MiB before allocation. Replies retain each offered contract's bound: 1 MiB for
chat/embed/Describe and 8 MiB for router views. Incoming execution is marked leaf
before owner dispatch: it may execute the selected receiving model server and
MUST refuse another native or remote OA inference hop. Resource ownership stays
on the answering machine under
RES-T6 (`../openabstractions-flat/abstraction-resource/CONTRACT.md`).
An optional minimum policy revision comes from receiving
configuration, stays bound to the mapped subject, and reaches each resolved
resource decision through the existing revision owner. Invalid, unavailable or
incomparable observations refuse without an ordinary-decision fallback. An
absent minimum requests a fresh current read and asserts no wire fence.
The revision type, order, observation freshness and exact subject binding are
owned by RIGHTS-O15 (`../openabstractions-flat/abstraction-rights/CONTRACT.md`).
Current-pin checks occur at each handshake and mapped policy evaluation;
accepted operation rechecks follow `INF-A6`. These boundaries supply no atomic
whole-call revocation or handler preemption guarantee.
The configured connection budget follows `IPC-D1`/`IPC-D2`; its expiry or
departure cancels the handler context under `IPC-D5`. The occupied slot remains
held until actual handler return (`IPC-A11`). Accepted operations retain their
own service lifetime.
The runtime closes and joins its listener and admitted calls on shutdown.
**Held by:** `TestServerSeparateRequestAndReplyBounds`,
`TestRemoteInferenceContextBindsSubjectAndMinimum` and
`TestRemoteRuntimeListenerOwnsShutdownAndLivePins`.

## Open decisions

- **The one exit-code table (CQ-6).** The SDK reads `start`'s 3 (upgrade
  exclusion) and 4 (packaged profile view), so those two keep their
  meaning. `jobs`' 4 (unavailable), `jobs migrate`'s 4 (conflict) and 5
  (another job host holds the store) then move, or the SDK's reading moves
  with an SDK release.
- **Runtime to runtime.** Which runtime identity holds the remote binding's
  certificate, how a remote runtime appears to resolution, and which
  services may cross are research first (runtime audit P2-6); the remote
  binding's rules are IPC-R1 to IPC-R5.

## Not built

- Typed readiness for composition failures and the declared composition
  graph (`RUN-R7`; P0-2, P1-7).
- One command-line vocabulary file and the CQ-0 CLI corpus (`RUN-C5`,
  `RUN-C6`; CQ-6).
- The declared start table of W1-13.
- A fence for a kind's own file effects during the upgrade drain: a step that
  ignores the cancellation keeps writing its work folder and output files
  until the stopped runtime's process exits (`RUN-U3`, abstraction-job
  JOB-R9; W1-27).

# Installed runtime parent

This provider-support package launches the exact sibling `openabstractions.exe`
as `serve runtime --supervised`. An explicit executable override must be absolute.
Args append runtime flags and cannot override supervision. The package imports
platform APIs and Go standard libraries; central runtime/facade code stays in the
child. Other platforms explicitly refuse until equivalent containment exists.

`Start(ctx, Options)` waits for `READY 1\n` with StartupTimeout. The parent owns
the sole stdin writer. `Close` closes it, waits ShutdownTimeout, then requests
job termination with ForceTimeout. Context cancellation initiates Close.
`Wait` returns the observed child failure and tree-cleanup failure. ErrForced is
returned for forced shutdown; callers can inspect joined underlying errors.
The defaults are 10s startup, 5s graceful shutdown, 5s forced completion. A nil
Stderr uses an owned NUL file, supporting a GUI/service parent with no console;
an explicitly supplied diagnostic file must be valid. The Windows console is
hidden with CREATE_NO_WINDOW.

Windows 10/Server 2016 or newer is required for PROC_THREAD_ATTRIBUTE_JOB_LIST.
CreateProcess receives the unnamed KILL_ON_JOB_CLOSE job atomically, plus an
explicit standard-handle inheritance list. Neither the job handle nor the pipe
writer is inherited. Unsupported attributes/nested-job restrictions fail creation.
The job allows breakaway (`supervision.Options.AllowBreakaway`, facade FAC-P5):
a process that asks with CREATE_BREAKAWAY_FROM_JOB leaves it, as the
service's owned application launch does, so an app the person opened
outlives a service restart. Nothing leaves silently, and the service's
providers run in their own nested jobs, which refuse breakaway.
`serve/application_owned_launch_supervised_windows_test.go` launches a
supervised test runtime through this package and checks that its owned
launch outlives the runtime.

Parent death closes the last job handle and Windows terminates its process tree.
No suspended-child or create-then-assign fallback exists.

While the parent lives, primary exit triggers termination of remaining job
members. Before termination the package captures waitable process handles;
it then observes zero active job processes and the captured process signals
within one force budget. A changed total-process count during capture/termination
fails with incomplete-observation evidence. Arbitrary descendant churn is not
reported as verified complete. OS calls themselves remain subject to Windows
scheduling; the software budgets bound observation loops and waits.

This establishes process containment. Accepted work still needs application-level
persistence/fencing when forced termination occurs. It registers no OS service.

## Processes the service starts directly

The supervised runtime (`serve runtime --supervised`) starts these children
itself. Each sits inside this package's job unless it breaks away.

| Child | Started by | Containment |
|---|---|---|
| Provider and engine hosts | `startProviderChild` (`serve/provider_process_windows.go`) | own nested `supervision` job; refuses breakaway |
| Native app CLI sessions | App session launcher | own nested `supervision` job |
| Foundry Local status probe | `runDeclarationProbe` (`serve/runtime_inference_declared.go`) | own nested `supervision` job; stdout through a pipe |
| Owned application launch | `startOwnedApplication` (`serve/application_owned_launch_windows.go`); the Messages start launcher (`serve/application_message_start_owned.go:39`) | CREATE_BREAKAWAY_FROM_JOB; outlives the runtime (FAC-P5) |
| Windows event-log reads | `runNativeCommand` (`abstraction-logging/go/service/native_sources.go`), `wevtutil.exe` from the system folder | this package's job |
| Elevated (protected) helper | `go-core/protected/launch_windows.go` (ShellExecuteExW runas) | Windows' elevation service starts it, outside the job |
| Applications opened through the shell | `serve/shelllaunch_windows.go` (IShellDispatch2::ShellExecute) | Explorer starts them, outside the job |

`startCommand`, `startDetached`, `runActivation` and the engine readiness
checks `proveHostEngine` and `proveMediaEngine` (`serve/models_host.go`, run
only from `openabstractions models host`) belong to the `openabstractions`
command line, which is not the supervised runtime.

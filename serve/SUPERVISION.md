# Installed-parent supervision

`openabstractions serve runtime --supervised` uses an inherited stdin pipe for
its lifetime and stdout for one readiness acknowledgment: `READY 1\n`.
The executable checks that stdin is a pipe before creating files or listeners.
The parent supplies a private pipe and owns its sole write end. It closes that
end for shutdown. Parent death also closes that end; the child must not inherit
a copy of the writer. The Windows process tests exercise this ownership.

EOF requests cooperative context cancellation. Unexpected control bytes and
read errors fail the child. Resolver initialization must succeed before the
acknowledgment is written. Independent provider failures remain observable
through resolution while healthy capabilities stay available. A failed or short readiness write
closes initialized listeners and fails startup. The marker reports local listener
initialization; capability-specific readiness remains available through the
resolver. Diagnostics use stderr. The parent must consume stdout and observe
both readiness and process exit.

Foreground invocation keeps its existing signal handling. Supervised invocation
also responds to signals and does not join a blocked stdin reader during shutdown.
There is no global shutdown endpoint.

The Windows parent implementation in `abstraction-download/go/serve/runtimehost`
supplies bounded startup, EOF shutdown and forced process-tree termination.
It attaches a kill-on-close Job Object during process creation. Completion checks
observe job accounting and captured process handles within the shutdown budget;
process churn that prevents complete observation is reported as an error.

Machine service registration opts in with `jobd service install --runtime`.
The supervisor retains its download worker and observes both lifetimes. The
runtime defaults to logging, configuration and managed durable jobs.
`openabstractions status --json` queries the generated baseline contract roster
through the resolver under the invoking account. It separately reports read-only
installation/supervision evidence. Running supervision does not establish
capability readiness; interrupted queries preserve unanswered results.

Per-user Startup invokes `jobdw start --runtime`. The supervisor starts the
contained runtime before exposing its worker bus. Central start checks the default contracts'
availability even when a supervisor already answers. This check establishes
capability availability; it does not establish ownership of the answering host.

Disposable installed recovery/removal checks, immediate activation on installation,
same-account multi-session ownership and accepted-work recovery after forced
termination remain pending. The parent containment package currently supports
Windows 10/Server 2016 and later.

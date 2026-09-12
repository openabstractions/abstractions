# Installed-parent supervision

`openabstractions serve runtime --supervised` uses an inherited stdin pipe for
its lifetime and stdout for one readiness acknowledgment: `READY 1\n`.
The executable checks that stdin is a pipe before creating files or listeners.
The parent supplies a private pipe and owns its sole write end. It closes that
end for shutdown. Parent death also closes that end; the child must not inherit
a copy of the writer. The Windows process tests exercise this ownership.

EOF requests cooperative context cancellation. Unexpected control bytes and
read errors fail the child. Provider/resolver listener initialization must all
succeed before the acknowledgment is written. A failed or short readiness write
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
runtime defaults to logging and config; job admission requires explicit store
and logical-owner configuration. `openabstractions status --json` queries the
baseline capabilities through the resolver under the invoking account.

Disposable installed recovery/removal checks, per-user shortcut activation,
same-account multi-session ownership and accepted-work recovery after forced
termination remain pending. The parent containment package currently supports
Windows 10/Server 2016 and later.

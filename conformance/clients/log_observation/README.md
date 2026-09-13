# Installed C++ log observation

Run `python conformance/clients/log_observation/run.py --help`, then `--run`
with an explicit `--cmake` path where necessary. `--race` requires the Go race
C compiler. The runner builds actual static source packages in a temporary
prefix and an outside consumer through `abstraction_facade_logging`.

The fixture runs the production Go runtime with a private FileSink. The C++
consumer waits at current end while a separate resolved writer appends a log,
then reads a burst through slow three-record pages. It cancels a wait, verifies
the prior logs remain readable, and uses the original observer again. Explicit
deadline and malformed page controls preserve bounds. Reopening the provider
and runtime gives a gap for the old cursor; a write-only provider does not
advertise observation.

The prefix contains logging/resolution/IPC. Jobs, config, storage, router, model,
asks and rights packages are absent. Missing logging and IPC dependencies fail
by name. Temporary runtime, data and prefix are cleaned up. Native evidence is
scoped to the executed platform; no installed owner runtime is used.

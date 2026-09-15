# Rust storage-changes and router fixture

Run `python conformance/clients/rust_capabilities/run.py --help`, then `--run`
on Windows (MSVC, with `--cmake` when CMake is outside PATH) or Linux (GCC).
The runner copies the facade, storage, router, model and download request Cargo source packages with
their generated `rs` files and tests the pure crates without the native
library. It installs static IPC into a temporary prefix, builds this consumer
offline and checks its native link lines against the installed prefix.

The Go fixture hosts an isolated runtime with a content store, writer, change
journal of four entries and an empty router. The consumer writes a policy mode
file that the storage read, change-observation and router policies read on
every call. The consumer checks:

- Observe, List and snapshot return `forbidden` and `unavailable` with no data.
- A committed object arrives as an `added` change within a waiting Observe.
- An unreadable object is skipped and the cursor still advances.
- Six commits past a stale cursor return `gap`; a two-object paged snapshot
  recovers every object and its cursor observes an empty current end.
- Router Models, Hosts and Pick return service codes `forbidden` and
  `policy_unavailable`, and Pick without a model refuses locally.
- The log observer waits at the current end until a separate binding writes a
  record, returns `forbidden` and `policy_unavailable` service codes from the
  history policy, and refuses out-of-range bounds locally.
- Model lookup resolves a portable request whose records come from the
  generated download request crate, returns `unsupported_mapping` for a
  provider-private sink, and returns `forbidden` and `unavailable` with no
  request.

The isolated home must stay empty. No OS service is registered.

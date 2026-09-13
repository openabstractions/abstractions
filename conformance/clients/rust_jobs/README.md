# Outside Rust durable job consumer

Run `python conformance/clients/rust_jobs/run.py --run --cmake <MSVC CMake executable>` on Windows. Help is the default. The runner builds/installs current-source static identity IPC in a temporary prefix, copies actual identity/facade/job Cargo packages and generated sources into an outside tree, builds offline, then runs the isolated Go runtime fixture. Temporary package/build outputs are removed. A separate pure source tree contains only the shared frame trait, resolver core and job protocol/helpers; its alternate-connector tests run without native IPC or logging sources.

The Go harness creates a generated HTTP download request and passes opaque request bytes as hex input. The Rust application uses generated job protocol through the validated facade binding. It receives no provider paths. A test transport drops one actual received Submit reply; Reconcile must recover the acceptance without another HTTP GET. The test asserts 262400 exact all-byte-values result bytes, owner retention, fresh/canceled waiting, unknown history, terminal CancelWork, own-scope inventory, runtime shutdown and empty client home.

Semantic tests cover forged receipts, guarantee weakening, malformed response combinations, changed result totals/operation IDs, no-progress chunks, EOF mismatch, empty result, partial writer failure, and typed not-ready copy refusal. The fake wire responses are hostile test inputs only; the production client delegates codecs to generated protocol.

This is current-source Windows/MSVC evidence. It does not exercise crash timing, mid-read cancellation, a fresh registry install, nonterminal work cancellation, Mac/Linux, or a production Rust server. Caller-owned persistent request storage is outside this fixture.

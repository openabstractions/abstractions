# Installed C++ storage resolution proof

Run `python conformance/clients/storage_binding/run.py --help`, then `--run`
with a CMake executable via `--cmake` if necessary. `--race` enables the Go race
detector and requires a configured C compiler.

The runner builds and installs the actual source packages in a temporary prefix.
Only facade resolution, storage content, and shared IPC are installed. It builds
an outside consumer through `find_package(abstraction_facade_storage)` and a
resolver-only consumer with storage discovery disabled. Negative configurations
refuse missing IPC and storage packages by name.

A temporary Go runtime registers the real content service and a private fixture
provider with explicit digest policy. The consumer resolves storage, reads exact
bounded bytes, checks denial and policy revocation, releases resources after
revocation, and checks cancellation/deadline propagation and malformed chunks.
Resource verification remains explicitly unverified. No installed runtime, owner
store, registry, service registration, or C++ server is used.

This is source-derived SDK evidence, not a released package/version claim.
The current native execution is Windows x64; Darwin Program proof limitations
are explicit in the fixture. Temporary binaries, data and prefix are removed.

The generated ContentReader descriptor now drives Open/Read/Close through the common ResolveService factory. Resource/range validation remains explicit; content lookup continues to make only an unverified naming claim.

# Installed C++ job inventory

`run.py --help` describes the fixture. Build and install the facade CMake project
with `ABSTRACTION_FACADE_BUILD_AGGREGATE=OFF` and
`ABSTRACTION_FACADE_BUILD_JOBS=ON`, then run:

```sh
python conformance/clients/job_inventory/run.py --run --prefix "$PREFIX"
```

The external CMake consumer links only `abstraction::facade_jobs`. Its generated
acceptance client submits five operations and its read-only inventory binding
traverses the actual Go runtime's caller-scoped inventory. It checks continuation
replay, stale cursor gap, explicit cancellation and deadline, intact subsequent
calls, and a denied method policy. Production validation rejects malformed page
bounds, duplicate receipts, invalid progress, and stalled cursors. Advisory progress may exceed an outdated total.

The fixture owns temporary endpoints/store and drains its runtime. It contacts no
installed runtime. `--race` enables Go's race detector; the C++ client is a native
installed-package consumer. Darwin is explicitly skipped while its Program proof
remains unavailable. This test does not establish cross-account authority or a
stable inventory snapshot during concurrent mutation.

The generated acceptance, operation and inventory descriptors now drive history, Submit/Reconcile, ObserveWork and page traversal through the common ResolveService factory. Consumer CMake builds are temporary to prevent stale package DIR cache reuse.

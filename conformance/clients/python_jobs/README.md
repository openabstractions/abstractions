# Installed Python durable job client proof

```sh
python conformance/clients/python_jobs/run.py --help
python conformance/clients/python_jobs/run.py --run --cmake /absolute/path/to/cmake
```

The existing temporary-prefix pattern builds current native IPC and installs the
actual identity/logging/config/job/download-request/facade Python packages offline.
It runs semantic counterexamples against the installed package, then an outside
Python process through an isolated Go runtime with real HTTPExecution. All source
bytes live on loopback; no owner runtime, installer, registry or external provider
is used. The Python client receives bootstrap and HTTP request metadata, with no
private provider paths. The Go fixture verifies exactly one GET and an empty
client home, then drains the runtime and removes its temporary state.

The transport loss wrapper forwards a real Submit and discards its actual reply
before generated decoding. Reconcile restores that acceptance, with the same
owner and operation ID. A native canceled wait leaves cancellation_requested
false. A restored binding works after bootstrap discovery is made unavailable.
CopyResult returns exactly 262400 all-byte-values bytes, using bounded chunks.
Unavailable history remains unknown; changed work with the same identity returns
key_conflict. Explicit CancelWork is separate. Inventory reads the same scope.

Tests reject changed owners/identities/weakened guarantees, malformed acceptance
combinations, result total/operation changes, short writers, no-progress chunks,
duplicate inventory and stalled cursor. Direct mock replies target semantic
validation; the runtime proof exercises actual generated wire codecs/native IPC.

Native Windows is measured. macOS Program proof remains unavailable. No full
matrix or platform activation test is part of this fixture. Generated request
packaging is added for typed application input; it contains no provider code.

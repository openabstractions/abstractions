# Installed JavaScript service proof

Run `python conformance/clients/js_services/run.py --help`, then `--run --race`
with `--cmake` pointing to the installed Visual Studio CMake where needed.
The Go race build requires a configured C compiler. The fixture targets Windows
x64 (MSVC, named pipes) and Linux x64 (GCC, Unix sockets) with an existing Node
runtime and its bundled npm. Node headers, plus node.lib on Windows, live in the
ignored checkout folder `.build/node-sdk/<version>/` in node-gyp layout. When
the running Node version is absent there, the bundled node-gyp fetches it from
nodejs.org and verifies it against SHASUMS256.txt; later runs reuse it offline.
`--node-sdk` selects another node-gyp devdir. On Linux the runner builds the
static IPC library position-independent, because the addon is a shared module,
and keeps its scratch tree in the system temporary directory.

The fixture builds the existing IPC library and Node-API addon, uses real
`npm pack` artifacts, and installs them with offline npm, scripts disabled, into
an isolated consumer tree. The installed packages are IPC, pure facade, logging, job acceptance, storage
content, config, rights, asks and download request vocabulary. Capability API
packages contain generated code and package metadata, with no native dependency.
All npm configuration/cache and native build artifacts stay in temporary paths.
The default addon import is package-relative, with the separate IPC library
linked statically. No global package install or service registration occurs.

The pure connector test covers reusable default waiting, fixed deadlines,
cumulative composite budgets and reference refusals with no native load. The
installed consumer writes an exact Unicode log record and reads it from the
production Go runtime. It checks typed invalid limits, unmet guarantees,
cancelled binding reuse, absent runtime and forged reference refusal. Private
peers exercise malformed, oversized and truncated replies plus timeout and
cancellation. With one libuv worker, queued calls cancel/expire while a prior
call remains blocked. Runtime data stays in the Go fixture's private directory.

The job consumer constructs a typed generated download Request, submits once,
reconciles the same receipt at the fixed selected endpoint, observes completion,
and reads 150000 result bytes in three bounded binary chunks. It validates owner,
request identity and requested guarantees across replies. Job inventory reports
the same receipt. The storage consumer reads the same-sized fixture through an
explicit receiving-program/digest policy, checks invalid range and forbidden
content, then releases the resource and observes its gap. Provider-private paths
are confined to fixture setup. Application calls receive only endpoints, source
URL and digest.

The installed packages also include the generated model client, which imports
the download request package by name. `packages.mjs` checks each installed
package's license, repository, homepage, issue tracker and description, and
requires declared dependencies to equal the generated bare module imports.

`services.mjs` runs the remaining advertised methods against one production
runtime: model lookup whose typed imported Request is submitted as a job, job
cancellation, log history observation, config reading/editing/observation,
rights decisions and operator policy edits, and asks admission, observation,
operator listing and answering. The storage content writer is refused before
any provider effect and stays refused with only a read grant. After the
JavaScript rights operator grants write, it uploads 150000 bytes in bounded
appends and checks out-of-order replay, resumption, invisible partial content,
hashed commit and duplicate reconciliation. A separate reader then reads the
exact bytes back. Identity conflict, oversized and empty/negative input,
early commit, beyond-size append, and revoked append/commit with owned abort
are checked too. With `--run` it uses the native connector.
`--pure` skips the native addon and Node SDK, installs the nine pure packages,
and uses `pipe_connector.mjs`, a test-only connector that verifies no server
identity. The Go services still verify the Node process's Program identity.
Pure-path evidence qualifies generated clients and service behavior; native
trust evidence for those methods requires `--run`.

This evidence supplies no browser, macOS or public-release verdict. One-way
logging remains transport submission; it is not a durable receipt.
